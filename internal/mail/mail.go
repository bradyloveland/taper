// Package mail sends plain-text email through an SMTP server.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Security modes for the connection to the SMTP server.
const (
	StartTLS = "starttls" // usually port 587
	TLS      = "tls"      // implicit TLS, usually port 465
	None     = "none"     // no encryption; only for a server on the same network
)

// Config is how to reach the SMTP server.
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
	Password string `json:"-"` // stored separately, encrypted
	From     string `json:"from"`
	FromName string `json:"from_name"`
}

// Ready reports whether enough is set to send.
func (c *Config) Ready() bool { return c.Host != "" && c.Port > 0 && c.From != "" }

// Check returns a message saying what's wrong with the settings, or "".
func (c *Config) Check() string {
	switch {
	case c.Host == "" || strings.ContainsAny(c.Host, " /:"):
		return "Enter the mail server's name, like smtp.example.org."
	case c.Port < 1 || c.Port > 65535:
		return "Enter the mail server's port: usually 587, or 465 for TLS."
	case c.Security != StartTLS && c.Security != TLS && c.Security != None:
		return "Choose how to connect to the mail server."
	}
	if a, err := mail.ParseAddress(c.From); err != nil || a.Address != c.From {
		return "Enter the address emails come from, like taper@yourschool.org."
	}
	if strings.ContainsAny(c.FromName, "\r\n") || strings.ContainsAny(c.Username, "\r\n") {
		return "The name and username can't contain line breaks."
	}
	return ""
}

// Message is one email.
type Message struct {
	To      []string
	Subject string
	Body    string // plain text
}

// Dialer opens the network connection; tests replace it.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Send delivers m.
func Send(ctx context.Context, c Config, m Message) error {
	return send(ctx, c, m, (&net.Dialer{Timeout: 15 * time.Second}).DialContext)
}

func send(ctx context.Context, c Config, m Message, dial Dialer) error {
	if !c.Ready() {
		return errors.New("email isn't set up")
	}
	if len(m.To) == 0 {
		return errors.New("no one to send to")
	}
	for _, to := range m.To {
		if a, err := mail.ParseAddress(to); err != nil || a.Address != to {
			return fmt.Errorf("%q isn't an email address", to)
		}
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return errors.New("the subject can't contain line breaks")
	}
	raw, err := build(c, m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("couldn't connect to %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsConf := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if c.Security == TLS {
		tc := tls.Client(conn, tlsConf)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("couldn't start TLS with %s: %w", addr, err)
		}
		conn = tc
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("the mail server didn't answer properly: %w", err)
	}
	defer cl.Close()
	if err := cl.Hello(helloName()); err != nil {
		return fmt.Errorf("the mail server refused the greeting: %w", err)
	}
	if c.Security == StartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("the mail server doesn't offer STARTTLS. Choose another security option, or check the port")
		}
		if err := cl.StartTLS(tlsConf); err != nil {
			return fmt.Errorf("couldn't start TLS: %w", err)
		}
	}
	if c.Username != "" {
		if err := cl.Auth(plainAuth(c)); err != nil {
			return fmt.Errorf("the mail server didn't accept the username and password: %w", err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return fmt.Errorf("the mail server refused the from address: %w", err)
	}
	for _, to := range m.To {
		if err := cl.Rcpt(to); err != nil {
			return fmt.Errorf("the mail server refused %s: %w", to, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the mail server didn't accept the email: %w", err)
	}
	return cl.Quit()
}

// plainAuth is PLAIN authentication. Go's smtp.PlainAuth refuses to send a
// password without TLS except to localhost; with security "none" the admin
// chose an unencrypted server on their own network, so it's allowed.
func plainAuth(c Config) smtp.Auth { return plain{c.Username, c.Password} }

type plain struct{ user, pass string }

func (a plain) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (a plain) Next([]byte, bool) ([]byte, error) { return nil, nil }

func helloName() string {
	return "taper.localdomain"
}

func build(c Config, m Message) ([]byte, error) {
	var b bytes.Buffer
	from := (&mail.Address{Name: c.FromName, Address: c.From}).String()
	domain := c.From[strings.LastIndex(c.From, "@")+1:]
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	h := [][2]string{
		{"From", from},
		{"To", strings.Join(m.To, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", m.Subject)},
		{"Date", time.Now().Format(time.RFC1123Z)},
		{"Message-ID", "<" + hex.EncodeToString(id) + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Transfer-Encoding", "quoted-printable"},
		{"Auto-Submitted", "auto-generated"},
	}
	for _, kv := range h {
		fmt.Fprintf(&b, "%s: %s\r\n", kv[0], kv[1])
	}
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

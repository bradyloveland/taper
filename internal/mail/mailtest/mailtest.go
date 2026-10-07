// Package mailtest is a tiny SMTP server for tests. It speaks just enough
// SMTP (no TLS) to receive messages, and keeps them.
package mailtest

import (
	"bufio"
	"encoding/base64"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Message is a received email.
type Message struct {
	From    string
	To      []string
	Subject string
	Body    string // decoded
	Raw     string
	User    string // who authenticated, if anyone
}

// Server collects messages.
type Server struct {
	Host string
	Port int
	// Password, if set, is required with the username "user".
	Password string

	mu   sync.Mutex
	msgs []Message
	l    net.Listener
}

// New starts a server, closed when the test ends.
func New(t *testing.T) *Server {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, l: l}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	return s
}

// Messages returns what's been received.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(line string) { io.WriteString(c, line+"\r\n") }
	say("220 mailtest ready")
	var m Message
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250-mailtest")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			arg := strings.TrimSpace(line[len("AUTH PLAIN"):])
			raw, _ := base64.StdEncoding.DecodeString(arg)
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && (s.Password == "" || (parts[1] == "user" && parts[2] == s.Password)) {
				m.User = parts[1]
				say("235 ok")
			} else {
				say("535 authentication failed")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			m.From = strings.Trim(line[len("MAIL FROM:"):], "<> ")
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			m.To = append(m.To, strings.Trim(line[len("RCPT TO:"):], "<> "))
			say("250 ok")
		case cmd == "DATA":
			say("354 go ahead")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			m.Raw = data.String()
			if pm, err := mail.ReadMessage(strings.NewReader(m.Raw)); err == nil {
				dec := new(mime.WordDecoder)
				m.Subject, _ = dec.DecodeHeader(pm.Header.Get("Subject"))
				b, _ := io.ReadAll(quotedprintable.NewReader(pm.Body))
				m.Body = strings.ReplaceAll(string(b), "\r\n", "\n")
			}
			s.mu.Lock()
			s.msgs = append(s.msgs, m)
			s.mu.Unlock()
			m = Message{User: m.User}
			say("250 queued")
		case cmd == "RSET":
			m = Message{User: m.User}
			say("250 ok")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented: " + strconv.Quote(line))
		}
	}
}

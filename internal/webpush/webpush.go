// Package webpush sends Web Push messages (RFC 8030) to browsers: the
// payload is encrypted for the browser (RFC 8291, aes128gcm), and the server
// identifies itself with a VAPID key (RFC 8292). Only the standard library is
// used.
//
// Push services (Apple, Google, Mozilla, Microsoft) only see the encrypted
// payload, the address of the device's subscription, and the server's public key.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Subscription is what a browser gives the page when it subscribes.
type Subscription struct {
	Endpoint string
	P256dh   []byte // the browser's public key, 65 bytes
	Auth     []byte // the browser's authentication secret, 16 bytes
}

// b64 is unpadded base64url, as Web Push uses everywhere.
var b64 = base64.RawURLEncoding

// Decode reads base64url with or without padding, as browsers send it.
func Decode(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(s, "="))
}

// --------------------------------------------------------------- VAPID keys

// GenerateKey makes a new VAPID key pair, as the private key's 32 bytes.
func GenerateKey() ([]byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return k.Bytes()
}

// Key is a VAPID key pair.
type Key struct {
	priv *ecdsa.PrivateKey
}

// ParseKey reads a private key made by GenerateKey.
func ParseKey(raw []byte) (*Key, error) {
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, err
	}
	return &Key{priv: k}, nil
}

// Public is the public key as base64url, for the browser's
// applicationServerKey.
func (k *Key) Public() string {
	pub, _ := k.priv.PublicKey.Bytes()
	return b64.EncodeToString(pub)
}

// authorization is the VAPID Authorization header for a push service: a
// JWT signed with ES256, naming the push service's origin, valid for 12 hours.
func (k *Key) authorization(endpoint *url.URL, subject string, now time.Time) (string, error) {
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": endpoint.Scheme + "://" + endpoint.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": subject,
	})
	if err != nil {
		return "", err
	}
	signing := head + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + b64.EncodeToString(sig) + ", k=" + k.Public(), nil
}

// --------------------------------------------------------------- encryption

const recordSize = 4096

// Encrypt encrypts a payload for a subscription (RFC 8291).
func Encrypt(payload []byte, sub Subscription) ([]byte, error) {
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(payload, sub, as, salt)
}

func encrypt(payload []byte, sub Subscription, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(sub.Auth) != 16 {
		return nil, errors.New("webpush: the auth secret must be 16 bytes")
	}
	if len(payload) > recordSize-16-1-86 {
		return nil, errors.New("webpush: the payload is too long")
	}
	ua, err := ecdh.P256().NewPublicKey(sub.P256dh)
	if err != nil {
		return nil, fmt.Errorf("webpush: the browser's key: %w", err)
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()

	// Combine the shared secret with the browser's auth secret.
	prkKey, err := hkdf.Extract(sha256.New, shared, sub.Auth)
	if err != nil {
		return nil, err
	}
	keyInfo := "WebPush: info\x00" + string(sub.P256dh) + string(asPub)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	// The content encryption key and nonce (RFC 8188).
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain := append(append([]byte{}, payload...), 0x02) // one record, the last

	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(recordSize))
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	out.Write(gcm.Seal(nil, nonce, plain, nil))
	return out.Bytes(), nil
}

// --------------------------------------------------------------- sending

// ErrGone means the subscription has ended (the person turned notifications
// off, or uninstalled the app): stop sending to it.
var ErrGone = errors.New("webpush: the subscription has ended")

// Message is one push.
type Message struct {
	Payload []byte
	TTL     time.Duration // how long the push service keeps it for an offline device
	Topic   string        // a newer message with the same topic replaces one not yet delivered
	Subject string        // a contact for the push service: a mailto: or https: address
}

// Send encrypts and delivers a message to a subscription.
func Send(ctx context.Context, client *http.Client, key *Key, sub Subscription, m Message) error {
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("webpush: bad endpoint")
	}
	body, err := Encrypt(m.Payload, sub)
	if err != nil {
		return err
	}
	auth, err := key.authorization(u, m.Subject, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.Itoa(int(m.TTL.Seconds())))
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", auth)
	if m.Topic != "" {
		req.Header.Set("Topic", m.Topic)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ErrGone
	case res.StatusCode >= 300:
		return fmt.Errorf("webpush: the push service answered %s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// KnownService reports whether an endpoint belongs to a browser's push
// service. Endpoints come from browsers, so only these are accepted: the
// server never posts to other addresses on a page's say-so.
func KnownService(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range []string{"fcm.googleapis.com", "android.googleapis.com", "updates.push.services.mozilla.com",
		"push.services.mozilla.com", "web.push.apple.com"} {
		if host == h {
			return true
		}
	}
	for _, suffix := range []string{".push.apple.com", ".notify.windows.com", ".push.services.mozilla.com"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// Decrypt is the browser's side of Encrypt, for tests.
func Decrypt(body []byte, ua *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("webpush: too short")
	}
	salt, idlen := body[:16], int(body[20])
	if len(body) < 21+idlen {
		return nil, errors.New("webpush: too short")
	}
	as, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		return nil, err
	}
	shared, err := ua.ECDH(as)
	if err != nil {
		return nil, err
	}
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(as.Bytes()), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		return nil, err
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		return nil, errors.New("webpush: missing the last-record delimiter")
	}
	return plain[:len(plain)-1], nil
}

package webpush

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := Decode(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The example in RFC 8291, appendix A.
const (
	rfcPlain     = "When I grow up, I want to be a watermelon"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcBody      = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func TestEncryptRFC8291(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(mustDecode(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(as.PublicKey().Bytes()); got != rfcASPublic {
		t.Fatalf("server public key %s", got)
	}
	sub := Subscription{P256dh: mustDecode(t, rfcUAPublic), Auth: mustDecode(t, rfcAuth)}
	body, err := encrypt([]byte(rfcPlain), sub, as, mustDecode(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(body); got != rfcBody {
		t.Fatalf("encrypted:\n got %s\nwant %s", got, rfcBody)
	}
}

// decrypt is the browser's side, to check messages end to end.
func decrypt(t *testing.T, body, uaPriv, auth []byte) string {
	t.Helper()
	ua, err := ecdh.P256().NewPrivateKey(uaPriv)
	if err != nil {
		t.Fatal(err)
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != 4096 || idlen != 65 {
		t.Fatalf("header: rs %d, idlen %d", rs, idlen)
	}
	as, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := ua.ECDH(as)
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(as.Bytes()), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatal("missing the last-record delimiter")
	}
	return string(plain[:len(plain)-1])
}

func TestSend(t *testing.T) {
	raw, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ParseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{P256dh: mustDecode(t, rfcUAPublic), Auth: mustDecode(t, rfcAuth)}
	var got *http.Request
	var body []byte
	status := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	sub.Endpoint = srv.URL + "/push/abc"

	m := Message{Payload: []byte(`{"title":"Hello"}`), TTL: time.Hour, Topic: "chat-3", Subject: "https://learn.example.org"}
	if err := Send(context.Background(), srv.Client(), key, sub, m); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("Content-Encoding") != "aes128gcm" || got.Header.Get("TTL") != "3600" || got.Header.Get("Topic") != "chat-3" {
		t.Fatalf("headers: %v", got.Header)
	}
	if plain := decrypt(t, body, mustDecode(t, rfcUAPrivate), sub.Auth); plain != `{"title":"Hello"}` {
		t.Fatalf("decrypted %q", plain)
	}

	// The VAPID token: signed by the key, for the push service's origin.
	auth := got.Header.Get("Authorization")
	tok, k, ok := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	if !ok || k != key.Public() {
		t.Fatalf("authorization: %s", auth)
	}
	parts := strings.Split(tok, ".")
	claims := map[string]any{}
	json.Unmarshal(mustDecode(t, parts[1]), &claims)
	if claims["aud"] != srv.URL || claims["sub"] != "https://learn.example.org" {
		t.Fatalf("claims: %v", claims)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), mustDecode(t, k))
	if err != nil {
		t.Fatal(err)
	}
	sig := mustDecode(t, parts[2])
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("bad signature")
	}

	status = http.StatusGone
	if err := Send(context.Background(), srv.Client(), key, sub, m); !errors.Is(err, ErrGone) {
		t.Fatalf("gone: %v", err)
	}
	status = http.StatusTooManyRequests
	if err := Send(context.Background(), srv.Client(), key, sub, m); err == nil || errors.Is(err, ErrGone) {
		t.Fatalf("429: %v", err)
	}
}

func TestKnownService(t *testing.T) {
	for ep, want := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/abc":                      true,
		"https://web.push.apple.com/QGx":                               true,
		"https://updates.push.services.mozilla.com/wpush/v2/gAAA":      true,
		"https://wns2-par02p.notify.windows.com/w/?token=x":            true,
		"http://fcm.googleapis.com/fcm/send/abc":                       false,
		"https://fcm.googleapis.com:8443/x":                            false,
		"https://evil.example/fcm.googleapis.com":                      false,
		"https://fcm.googleapis.com.evil.example/x":                    false,
		"https://user@fcm.googleapis.com/x":                            false,
		"https://10.0.0.1/x":                                           false,
		"https://notify.windows.com.evil.example/x":                    false,
		"https://" + base64.RawURLEncoding.EncodeToString([]byte("x")): false,
	} {
		if got := KnownService(ep); got != want {
			t.Errorf("KnownService(%q) = %v, want %v", ep, got, want)
		}
	}
}

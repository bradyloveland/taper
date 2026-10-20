// Package passkey checks passkeys (WebAuthn): registering one, and signing in
// with one. Only the standard library is used.
//
// Registration uses the public key the browser hands over directly
// (AuthenticatorAttestationResponse.getPublicKey(), in every current browser),
// with attestation "none", so no CBOR needs decoding. Passkeys must verify the
// person (a fingerprint, face or device PIN), which makes a passkey on its own
// as strong as a password and a code.
package passkey

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// COSE algorithm numbers Taper accepts, most preferred first.
const (
	ES256 = -7
	EdDSA = -8
	RS256 = -257
)

// Algorithms lists the accepted algorithms, for the browser.
var Algorithms = []int{ES256, EdDSA, RS256}

// Flags in authenticator data.
const (
	flagUP = 0x01 // the person was present
	flagUV = 0x04 // the person was verified (fingerprint, face, PIN)
	flagBE = 0x08 // the passkey can be backed up (synced)
	flagBS = 0x10 // the passkey is backed up
	flagAT = 0x40 // attested credential data follows
)

// ErrInvalid is returned for anything that doesn't check out. The reason is
// wrapped for logs; people only need to know it didn't work.
var ErrInvalid = errors.New("passkey: not valid")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// B64 is unpadded base64url, as WebAuthn uses.
var B64 = base64.RawURLEncoding

// Decode reads base64url with or without padding.
func Decode(s string) ([]byte, error) { return B64.DecodeString(strings.TrimRight(s, "=")) }

// Expect is what a ceremony must match.
type Expect struct {
	Challenge []byte
	Origin    string // like https://learn.example.org
	RPID      string // the host name, like learn.example.org
}

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

func checkClientData(raw []byte, typ string, e Expect) error {
	var cd clientData
	if err := json.Unmarshal(raw, &cd); err != nil {
		return invalid("client data: %v", err)
	}
	if cd.Type != typ {
		return invalid("type %q", cd.Type)
	}
	ch, err := Decode(cd.Challenge)
	if err != nil || len(e.Challenge) == 0 || subtle.ConstantTimeCompare(ch, e.Challenge) != 1 {
		return invalid("challenge")
	}
	if cd.Origin != e.Origin || cd.CrossOrigin {
		return invalid("origin %q, want %q", cd.Origin, e.Origin)
	}
	return nil
}

// authData is the parsed authenticator data.
type authData struct {
	flags     byte
	signCount uint32
	credID    []byte // with flagAT
}

func parseAuthData(raw []byte, e Expect) (authData, error) {
	var a authData
	if len(raw) < 37 {
		return a, invalid("authenticator data too short")
	}
	want := sha256.Sum256([]byte(e.RPID))
	if !bytes.Equal(raw[:32], want[:]) {
		return a, invalid("for another site")
	}
	a.flags = raw[32]
	a.signCount = binary.BigEndian.Uint32(raw[33:37])
	if a.flags&flagUP == 0 || a.flags&flagUV == 0 {
		return a, invalid("the person wasn't verified")
	}
	if a.flags&flagAT != 0 {
		// AAGUID (16), credential ID length (2), credential ID, then the key.
		if len(raw) < 37+18 {
			return a, invalid("attested data too short")
		}
		n := int(binary.BigEndian.Uint16(raw[53:55]))
		if n == 0 || n > 1023 || len(raw) < 55+n {
			return a, invalid("credential ID")
		}
		a.credID = raw[55 : 55+n]
	}
	return a, nil
}

// Credential is a registered passkey.
type Credential struct {
	ID        []byte
	PublicKey []byte // SubjectPublicKeyInfo, DER
	Alg       int
	SignCount uint32
	BackedUp  bool // synced, like iCloud Keychain or Google Password Manager
}

// Registration is what the browser sends after making a passkey.
type Registration struct {
	ID         []byte // rawId
	ClientData []byte // clientDataJSON
	AuthData   []byte // getAuthenticatorData()
	PublicKey  []byte // getPublicKey(): SubjectPublicKeyInfo
	Alg        int    // getPublicKeyAlgorithm()
}

// Register checks a new passkey and returns it.
func Register(r Registration, e Expect) (*Credential, error) {
	if err := checkClientData(r.ClientData, "webauthn.create", e); err != nil {
		return nil, err
	}
	ad, err := parseAuthData(r.AuthData, e)
	if err != nil {
		return nil, err
	}
	if ad.flags&flagAT == 0 || !bytes.Equal(ad.credID, r.ID) {
		return nil, invalid("credential ID doesn't match")
	}
	if _, err := parseKey(r.PublicKey, r.Alg); err != nil {
		return nil, err
	}
	return &Credential{ID: r.ID, PublicKey: r.PublicKey, Alg: r.Alg, SignCount: ad.signCount, BackedUp: ad.flags&flagBS != 0}, nil
}

func parseKey(spki []byte, alg int) (any, error) {
	k, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, invalid("public key: %v", err)
	}
	ok := false
	switch pk := k.(type) {
	case *ecdsa.PublicKey:
		ok = alg == ES256 && pk.Curve.Params().Name == "P-256"
	case ed25519.PublicKey:
		ok = alg == EdDSA
	case *rsa.PublicKey:
		ok = alg == RS256 && pk.N.BitLen() >= 2048
	}
	if !ok {
		return nil, invalid("unsupported key for algorithm %d", alg)
	}
	return k, nil
}

// Assertion is what the browser sends when signing in.
type Assertion struct {
	ID         []byte
	ClientData []byte
	AuthData   []byte
	Signature  []byte
	UserHandle []byte
}

// Verify checks a sign-in with a stored passkey, returning the new
// signature counter to save.
func Verify(a Assertion, c *Credential, e Expect) (uint32, error) {
	if !bytes.Equal(a.ID, c.ID) {
		return 0, invalid("another passkey")
	}
	if err := checkClientData(a.ClientData, "webauthn.get", e); err != nil {
		return 0, err
	}
	ad, err := parseAuthData(a.AuthData, e)
	if err != nil {
		return 0, err
	}
	k, err := parseKey(c.PublicKey, c.Alg)
	if err != nil {
		return 0, err
	}
	cdHash := sha256.Sum256(a.ClientData)
	msg := append(append([]byte{}, a.AuthData...), cdHash[:]...)
	digest := sha256.Sum256(msg)
	ok := false
	switch pk := k.(type) {
	case *ecdsa.PublicKey:
		ok = ecdsa.VerifyASN1(pk, digest[:], a.Signature)
	case ed25519.PublicKey:
		ok = ed25519.Verify(pk, msg, a.Signature)
	case *rsa.PublicKey:
		ok = rsa.VerifyPKCS1v15(pk, crypto.SHA256, digest[:], a.Signature) == nil
	}
	if !ok {
		return 0, invalid("signature")
	}
	// Synced passkeys always say 0. Otherwise the counter must go up, or the
	// passkey may have been copied.
	if (ad.signCount != 0 || c.SignCount != 0) && ad.signCount <= c.SignCount {
		return 0, invalid("signature counter went back (%d after %d)", ad.signCount, c.SignCount)
	}
	return ad.signCount, nil
}

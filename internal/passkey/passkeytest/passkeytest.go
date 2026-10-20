// Package passkeytest is a pretend authenticator for tests: it makes passkeys
// and signs in with them the way a phone or security key would.
package passkeytest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"

	"github.com/bradyloveland/taper/internal/passkey"
)

// Authenticator holds one passkey.
type Authenticator struct {
	ID         []byte
	Alg        int
	Count      uint32 // the signature counter; 0 for a synced passkey that never counts
	Counting   bool   // whether the counter goes up with each use
	Flags      byte   // authenticator data flags; defaults to present and verified
	UserHandle []byte
	signer     crypto.Signer
}

// New makes an authenticator with a new key for alg (passkey.ES256, EdDSA or RS256).
func New(alg int) *Authenticator {
	a := &Authenticator{Alg: alg, Flags: 0x01 | 0x04, ID: make([]byte, 16)}
	rand.Read(a.ID)
	switch alg {
	case passkey.EdDSA:
		_, a.signer, _ = ed25519.GenerateKey(rand.Reader)
	case passkey.RS256:
		a.signer, _ = rsa.GenerateKey(rand.Reader, 2048)
	default:
		a.signer, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	return a
}

func clientData(typ string, e passkey.Expect) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": passkey.B64.EncodeToString(e.Challenge), "origin": e.Origin})
	return b
}

func (a *Authenticator) authData(rpID string, attested bool) []byte {
	h := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, h[:]...)
	flags := a.Flags
	if attested {
		flags |= 0x40
	}
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, a.Count)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID
		out = binary.BigEndian.AppendUint16(out, uint16(len(a.ID)))
		out = append(out, a.ID...)
		out = append(out, 0xa0) // the COSE key (an empty map; Taper uses the SPKI)
	}
	return out
}

// Create makes the passkey for a site, as navigator.credentials.create.
func (a *Authenticator) Create(e passkey.Expect) passkey.Registration {
	spki, _ := x509.MarshalPKIXPublicKey(a.signer.Public())
	return passkey.Registration{ID: a.ID, ClientData: clientData("webauthn.create", e), AuthData: a.authData(e.RPID, true),
		PublicKey: spki, Alg: a.Alg}
}

// Get signs in, as navigator.credentials.get.
func (a *Authenticator) Get(e passkey.Expect) passkey.Assertion {
	if a.Counting {
		a.Count++
	}
	cd := clientData("webauthn.get", e)
	ad := a.authData(e.RPID, false)
	h := sha256.Sum256(cd)
	msg := append(append([]byte{}, ad...), h[:]...)
	var sig []byte
	switch k := a.signer.(type) {
	case ed25519.PrivateKey:
		sig = ed25519.Sign(k, msg)
	case *rsa.PrivateKey:
		d := sha256.Sum256(msg)
		sig, _ = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, d[:])
	case *ecdsa.PrivateKey:
		d := sha256.Sum256(msg)
		sig, _ = ecdsa.SignASN1(rand.Reader, k, d[:])
	}
	return passkey.Assertion{ID: a.ID, ClientData: cd, AuthData: ad, Signature: sig, UserHandle: a.UserHandle}
}

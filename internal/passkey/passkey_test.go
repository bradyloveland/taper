package passkey_test

import (
	"errors"
	"testing"

	"github.com/bradyloveland/taper/internal/passkey"
	"github.com/bradyloveland/taper/internal/passkey/passkeytest"
)

var site = passkey.Expect{Challenge: []byte("0123456789abcdef0123456789abcdef"), Origin: "https://learn.example.org", RPID: "learn.example.org"}

func TestRegisterAndSignIn(t *testing.T) {
	for _, alg := range passkey.Algorithms {
		a := passkeytest.New(alg)
		a.Counting = alg == passkey.RS256 // a security key that counts
		c, err := passkey.Register(a.Create(site), site)
		if err != nil {
			t.Fatalf("alg %d: register: %v", alg, err)
		}
		login := site
		login.Challenge = []byte("another challenge, 32 bytes long")
		n, err := passkey.Verify(a.Get(login), c, login)
		if err != nil {
			t.Fatalf("alg %d: sign in: %v", alg, err)
		}
		if a.Counting && n != 1 {
			t.Fatalf("counter %d", n)
		}
		c.SignCount = n
		// Replaying the same counter is refused for counting passkeys.
		if a.Counting {
			a.Count--
			if _, err := passkey.Verify(a.Get(login), c, login); !errors.Is(err, passkey.ErrInvalid) {
				t.Fatalf("counter went back: %v", err)
			}
		}
	}
}

func TestRefusals(t *testing.T) {
	a := passkeytest.New(passkey.ES256)
	c, err := passkey.Register(a.Create(site), site)
	if err != nil {
		t.Fatal(err)
	}
	other := passkeytest.New(passkey.ES256)
	other.ID = a.ID // a different key claiming the same passkey
	cases := map[string]func() error{
		"wrong challenge": func() error {
			x := site
			x.Challenge = []byte("not the challenge we sent, 32 by")
			_, err := passkey.Verify(a.Get(x), c, site)
			return err
		},
		"wrong origin": func() error {
			x := site
			x.Origin = "https://evil.example"
			_, err := passkey.Verify(a.Get(x), c, site)
			return err
		},
		"wrong site": func() error {
			x := site
			x.RPID = "evil.example"
			_, err := passkey.Verify(a.Get(x), c, site)
			return err
		},
		"not verified": func() error {
			b := *a
			b.Flags = 0x01 // present, but no fingerprint/face/PIN
			_, err := passkey.Verify(b.Get(site), c, site)
			return err
		},
		"wrong key": func() error {
			_, err := passkey.Verify(other.Get(site), c, site)
			return err
		},
		"register as sign-in": func() error {
			r := a.Create(site)
			_, err := passkey.Verify(passkey.Assertion{ID: a.ID, ClientData: r.ClientData, AuthData: r.AuthData}, c, site)
			return err
		},
		"mismatched ID": func() error {
			r := a.Create(site)
			r.ID = []byte("someone else's")
			_, err := passkey.Register(r, site)
			return err
		},
		"wrong algorithm": func() error {
			r := a.Create(site)
			r.Alg = passkey.RS256
			_, err := passkey.Register(r, site)
			return err
		},
	}
	for name, f := range cases {
		if err := f(); !errors.Is(err, passkey.ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

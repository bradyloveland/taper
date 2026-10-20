package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/passkey"
	"github.com/bradyloveland/taper/internal/passkey/passkeytest"
)

// site is what the browser in tests sees: the test server's address.
func (b *browser) site(challenge string) passkey.Expect {
	b.e.t.Helper()
	ch, err := passkey.Decode(challenge)
	if err != nil {
		b.e.t.Fatal(err)
	}
	u, _ := url.Parse(b.url())
	return passkey.Expect{Challenge: ch, Origin: u.Scheme + "://" + u.Host, RPID: u.Hostname()}
}

func jsonOf(t *testing.T, r *resp) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
		t.Fatalf("not JSON (%d): %s", r.Status, r.Body)
	}
	return m
}

// addPasskey adds a passkey for the signed-in browser, as the page's script does.
func addPasskey(t *testing.T, b *browser, a *passkeytest.Authenticator) *resp {
	t.Helper()
	tok := b.csrf()
	r := b.post("/account/passkeys/options", url.Values{"csrf": {tok}})
	opts := jsonOf(t, r)
	user := opts["user"].(map[string]any)
	a.UserHandle, _ = passkey.Decode(user["id"].(string))
	reg := a.Create(b.site(opts["challenge"].(string)))
	return b.post("/account/passkeys", url.Values{"csrf": {tok}, "id": {passkey.B64.EncodeToString(reg.ID)},
		"clientData": {passkey.B64.EncodeToString(reg.ClientData)}, "authData": {passkey.B64.EncodeToString(reg.AuthData)},
		"publicKey": {passkey.B64.EncodeToString(reg.PublicKey)}, "alg": {fmt.Sprint(reg.Alg)}})
}

// passkeyLogin signs in with a passkey, as the sign-in page's script does.
// change, if given, alters what the authenticator signs.
func passkeyLogin(t *testing.T, b *browser, a *passkeytest.Authenticator, change func(*passkey.Expect)) *resp {
	t.Helper()
	opts := jsonOf(t, b.post("/login/passkey/options", nil))
	e := b.site(opts["challenge"].(string))
	if opts["rpId"] != e.RPID {
		t.Fatalf("rpId %v", opts["rpId"])
	}
	if change != nil {
		change(&e)
	}
	as := a.Get(e)
	return b.post("/login/passkey", url.Values{"id": {passkey.B64.EncodeToString(as.ID)},
		"clientData": {passkey.B64.EncodeToString(as.ClientData)}, "authData": {passkey.B64.EncodeToString(as.AuthData)},
		"signature": {passkey.B64.EncodeToString(as.Signature)}, "userHandle": {passkey.B64.EncodeToString(as.UserHandle)},
		"remember": {"1"}, "next": {"/calendar"}})
}

func TestPasskeys(t *testing.T) {
	c := newCalEnv(t)
	s := c.signedIn("sam", "scholar-password")
	expect(t, s.get("/account"), http.StatusOK, `id="passkeys"`, "Add a passkey")
	expect(t, s.get("/account/passkeys"), http.StatusOK, "You haven't added a passkey yet", "data-passkey-add")

	// Adding one.
	phone := passkeytest.New(passkey.ES256)
	expect(t, addPasskey(t, s, phone), http.StatusOK, `"next":"/account/passkeys"`)
	expect(t, s.get("/account/passkeys"), http.StatusOK, "Your passkey is added", "Remove")
	expect(t, s.get("/account"), http.StatusOK, "1 added")
	if r := addPasskey(t, s, phone); r.Status != http.StatusBadRequest || !strings.Contains(r.Body, "already added") {
		t.Fatalf("adding twice: %d %s", r.Status, r.Body)
	}
	// A challenge can't be used by someone else's session.
	z := c.signedIn("zoe", "scholar-password")
	opts := jsonOf(t, s.postForm("/account/passkeys/options", nil))
	reg := passkeytest.New(passkey.ES256).Create(s.site(opts["challenge"].(string)))
	expect(t, z.postForm("/account/passkeys", url.Values{"id": {passkey.B64.EncodeToString(reg.ID)},
		"clientData": {passkey.B64.EncodeToString(reg.ClientData)}, "authData": {passkey.B64.EncodeToString(reg.AuthData)},
		"publicKey": {passkey.B64.EncodeToString(reg.PublicKey)}, "alg": {"-7"}}), http.StatusBadRequest, "took too long")

	// Signing in with it, on a new browser: no password, straight in.
	b := c.browser()
	expect(t, b.get("/login"), http.StatusOK, "data-passkey-login", `autocomplete="username webauthn"`)
	r := passkeyLogin(t, b, phone, nil)
	expect(t, r, http.StatusOK, `"next":"/calendar"`)
	expect(t, b.get("/"), http.StatusOK, "Good")
	list, _ := c.store.UserPasskeys(c.sam.ID)
	if list[0].LastUsedAt == 0 {
		t.Fatal("last used isn't recorded")
	}

	// Refusals: another site's origin, a challenge we didn't give, someone else's handle.
	expect(t, passkeyLogin(t, c.browser(), phone, func(e *passkey.Expect) { e.Origin = "https://evil.example" }), http.StatusUnauthorized, "didn't work")
	expect(t, passkeyLogin(t, c.browser(), phone, func(e *passkey.Expect) { e.Challenge = []byte("made up") }), http.StatusBadRequest, "took too long")
	stranger := *phone
	stranger.UserHandle = []byte("someone else")
	expect(t, passkeyLogin(t, c.browser(), &stranger, nil), http.StatusUnauthorized)
	// A cross-site POST is refused.
	x := c.browser()
	x.header = http.Header{"Origin": {"https://evil.example"}}
	expect(t, x.post("/login/passkey/options", nil), http.StatusForbidden)
	expect(t, x.post("/login/passkey", url.Values{"id": {"x"}}), http.StatusForbidden)

	// When the school requires two-step sign-in, the passkey counts, and a password alone doesn't.
	a := c.signedIn("admin", "admin-password")
	expectRedirect(t, a.postForm("/admin/settings/security", url.Values{"scholar": {"1"}}), "/admin/settings#security")
	expect(t, c.browser().login("sam", "scholar-password"), http.StatusUnauthorized, "sign in with your passkey")
	pb := c.browser()
	expect(t, passkeyLogin(t, pb, phone, nil), http.StatusOK)
	expect(t, pb.get("/"), http.StatusOK, "Good") // not sent to set up two-step sign-in
	// Zoe, without a passkey, is sent to set it up, and can add a passkey there.
	expectRedirect(t, z.get("/"), "/account/two-step")
	expect(t, z.get("/account/two-step"), http.StatusOK, `href="/account/passkeys"`)
	expect(t, z.get("/account/passkeys"), http.StatusOK, "Your school requires two-step sign-in")
	expect(t, addPasskey(t, z, passkeytest.New(passkey.EdDSA)), http.StatusOK)
	expect(t, z.get("/"), http.StatusOK, "Good")

	// An admin removes Sam's passkeys (a lost phone); then it no longer works.
	expect(t, a.get(fmt.Sprintf("/admin/people/%d", c.sam.ID)), http.StatusOK, "Remove passkeys")
	expectRedirect(t, a.postForm(fmt.Sprintf("/admin/people/%d/passkeys-off", c.sam.ID), nil), fmt.Sprintf("/admin/people/%d", c.sam.ID))
	expect(t, passkeyLogin(t, c.browser(), phone, nil), http.StatusUnauthorized)

	// Removing your own.
	zl, _ := c.store.UserPasskeys(c.zoe.ID)
	expectRedirect(t, z.postForm(fmt.Sprintf("/account/passkeys/%d/delete", zl[0].ID), nil), "/account/passkeys")
	z.get("/account/passkeys")
	expect(t, z.postForm(fmt.Sprintf("/account/passkeys/%d/delete", zl[0].ID), nil), http.StatusNotFound)

	// Turned-off accounts can't sign in with a passkey.
	m := c.signedIn("mia", "mentor-password")
	key := passkeytest.New(passkey.RS256)
	key.Counting = true
	expect(t, addPasskey(t, m, key), http.StatusOK)
	mia := mustUser(t, c.env, "mia")
	mia.Active = false
	c.store.UpdateUser(mia)
	expect(t, passkeyLogin(t, c.browser(), key, nil), http.StatusForbidden, "turned off")
}

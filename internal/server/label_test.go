package server

import (
	"net/http"
	"testing"
)

func TestLabel(t *testing.T) {
	for v, want := range map[string]string{"0.7.0": "", "0.8.0-dev": "Dev", "0.8.0-beta.1": "Beta", "1.0.0-rc.2": "Beta"} {
		if got := versionLabel(v); got != want {
			t.Errorf("versionLabel(%q) = %q, want %q", v, got, want)
		}
	}
	e := newEnvWith(t, func(o *Options) { o.Config.Label = "BETA" })
	e.srv.setupDone.Store(true)
	expect(t, e.browser().get("/login"), http.StatusOK, `<span class="env-label env-beta"`, ">Beta</span>", "<title>Beta · ")
}

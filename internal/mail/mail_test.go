package mail

import (
	"context"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/mail/mailtest"
)

func TestSend(t *testing.T) {
	srv := mailtest.New(t)
	srv.Password = "secret"
	c := Config{Host: srv.Host, Port: srv.Port, Security: None, Username: "user", Password: "secret",
		From: "taper@school.example", FromName: "Liberty Commonwealth"}
	if msg := c.Check(); msg != "" {
		t.Fatal(msg)
	}
	err := Send(context.Background(), c, Message{To: []string{"ann@example.org"}, Subject: "Héllo from Taper",
		Body: "Line one\nA very long line " + strings.Repeat("x", 100) + "\n= sign"})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 {
		t.Fatalf("messages: %d", len(got))
	}
	m := got[0]
	if m.From != "taper@school.example" || m.To[0] != "ann@example.org" || m.Subject != "Héllo from Taper" || m.User != "user" {
		t.Fatalf("message: %+v", m)
	}
	if !strings.Contains(m.Body, "Line one\nA very long line "+strings.Repeat("x", 100)+"\n= sign") {
		t.Fatalf("body: %q", m.Body)
	}
	if !strings.Contains(m.Raw, `From: "Liberty Commonwealth" <taper@school.example>`) {
		t.Fatalf("raw: %s", m.Raw)
	}

	c.Password = "wrong"
	if err := Send(context.Background(), c, Message{To: []string{"ann@example.org"}, Subject: "x", Body: "x"}); err == nil ||
		!strings.Contains(err.Error(), "didn't accept the username") {
		t.Fatalf("bad password: %v", err)
	}
	c.Security = StartTLS
	c.Password = "secret"
	if err := Send(context.Background(), c, Message{To: []string{"ann@example.org"}, Subject: "x", Body: "x"}); err == nil ||
		!strings.Contains(err.Error(), "doesn't offer STARTTLS") {
		t.Fatalf("starttls: %v", err)
	}
}

func TestChecks(t *testing.T) {
	good := Config{Host: "smtp.example.org", Port: 587, Security: StartTLS, From: "a@example.org"}
	if good.Check() != "" || !good.Ready() {
		t.Fatal("good config refused")
	}
	for _, bad := range []Config{
		{Host: "", Port: 587, Security: StartTLS, From: "a@example.org"},
		{Host: "smtp.example.org", Port: 0, Security: StartTLS, From: "a@example.org"},
		{Host: "smtp.example.org", Port: 587, Security: "ssl", From: "a@example.org"},
		{Host: "smtp.example.org", Port: 587, Security: StartTLS, From: "nope"},
		{Host: "smtp.example.org", Port: 587, Security: StartTLS, From: "a@example.org", FromName: "x\r\nBcc: evil@example.org"},
	} {
		if bad.Check() == "" {
			t.Errorf("accepted %+v", bad)
		}
	}
	ctx := context.Background()
	if err := Send(ctx, good, Message{To: []string{"x@example.org"}, Subject: "a\r\nBcc: evil@example.org"}); err == nil {
		t.Fatal("header injection in the subject accepted")
	}
	if err := Send(ctx, good, Message{To: []string{"not an address"}}); err == nil {
		t.Fatal("bad recipient accepted")
	}
	if err := Send(ctx, Config{}, Message{To: []string{"x@example.org"}}); err == nil {
		t.Fatal("unset config accepted")
	}
}

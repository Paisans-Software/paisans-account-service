package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRendererAccentIsSanitised(t *testing.T) {
	r, err := newRenderer(`red;}</style><script>`, "https://id.example.org")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.page(rec, 200, "message.html", view{Title: "Hi", Data: map[string]string{"Heading": "Hello", "Body": "b"}})
	out := rec.Body.String()
	if strings.Contains(out, "<script>") {
		t.Fatal("accent must not inject markup")
	}
	if !strings.Contains(out, "https://id.example.org/api/application-images/logo") {
		t.Fatal("logo comes from the identity provider")
	}
}

func TestRendererAccentValues(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"oklch(0.63 0.2 15)", "--accent-color: oklch(0.63 0.2 15);"},
		{"#336699", "--accent-color: #336699;"},
		{"default", ""},
		{"Default", ""},
		{"", ""},
		{"red;}</style><script>alert(1)</script>", ""},
		{"url(https://evil.example/x)", ""},
		{"oklch(0.6 0.2 15);background:url(x)", ""},
	} {
		r, err := newRenderer(c.in, "https://id.example.org")
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		r.page(rec, 200, "message.html", view{Title: "Hi", Data: map[string]string{"Heading": "Hello", "Body": "b"}})
		out := rec.Body.String()
		if c.want == "" {
			if strings.Contains(out, "<style>") || strings.Contains(out, "--accent-color") {
				t.Errorf("%q: want no accent block, got %s", c.in, out)
			}
			continue
		}
		if !strings.Contains(out, "<style>:root{"+c.want+"}</style>") {
			t.Errorf("%q: accent not rendered verbatim: %s", c.in, out)
		}
	}
}

func TestStylesheetCarriesNotice(t *testing.T) {
	b, err := staticFS.ReadFile("static/style.css")
	if err != nil || !strings.Contains(string(b), "Pocket ID v2.18.0") || !strings.Contains(string(b), "BSD-2-Clause") {
		t.Fatal("style.css must name its source version and licence")
	}
}

func TestEveryPageExecutes(t *testing.T) {
	r, err := newRenderer("#336699", "https://id.example.org")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	type declines struct {
		Count int
		Last  time.Time
	}
	type application struct {
		ID, DisplayName, Username, Email, InviterUsername, State string
		AgreedAt                                                 time.Time
		Declines                                                 declines
		Stale                                                    bool
	}
	type invite struct {
		ID, State string
		CreatedAt time.Time
	}
	sess := &Session{Sub: "u1", Username: "alice", Admin: true, CSRF: "tok"}
	pages := map[string]any{
		"home.html": map[string]any{
			"InvitesOn": true, "Remaining": "2 invites left", "NewLink": "https://x.example/join/abc", "Expires": "1 Nov",
			"Invites": []invite{{"i1", "open", now}, {"i2", "used", now}},
		},
		"admin.html": []application{
			{"a1", "Bob", "bob", "b@example.org", "alice", "pending", now, declines{2, now}, false},
			{"a2", "Cy", "cy", "c@example.org", "alice", "link_unsent", now, declines{}, false},
			{"a3", "Di", "di", "d@example.org", "alice", "approving", now, declines{}, false},
			{"a4", "Ed", "ed", "e@example.org", "alice", "approving", now, declines{}, true},
		},
		"invite.html":  map[string]string{"Inviter": "alice", "CoC": "Be kind.", "Username": "bob", "DisplayName": "Bob", "Email": "b@example.org"},
		"message.html": map[string]string{"Heading": "Hello", "Body": "b"},
	}
	for name, data := range pages {
		rec := httptest.NewRecorder()
		r.page(rec, 200, name, view{Title: name, CSRF: "tok", Session: sess, Data: data})
		out := rec.Body.String()
		if !strings.Contains(out, "</html>") {
			t.Errorf("%s did not render to the end: %q", name, out)
		}
	}
}

func TestRendererNeedsAbsoluteIDPURL(t *testing.T) {
	for _, u := range []string{"", "id.example.org", "/pocket"} {
		if _, err := newRenderer("", u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

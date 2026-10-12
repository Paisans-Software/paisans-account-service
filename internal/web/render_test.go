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
	}
	type invite struct {
		ID, State string
		CreatedAt time.Time
	}
	sess := &Session{Sub: "u1", Username: "alice", Admin: true, CSRF: "tok"}
	pages := map[string]any{
		"home.html": map[string]any{
			"Remaining": "2 invites left", "NewLink": "https://x.example/join/abc", "Expires": "1 Nov",
			"Invites": []invite{{"i1", "open", now}, {"i2", "used", now}},
		},
		"admin.html": []application{
			{"a1", "Bob", "bob", "b@example.org", "alice", "pending", now, declines{2, now}},
			{"a2", "Cy", "cy", "c@example.org", "alice", "link_unsent", now, declines{}},
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

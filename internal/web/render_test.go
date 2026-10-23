package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/flow"
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
		{"oklch(0.63 0.2 15)", "--primary: oklch(0.63 0.2 15); --ring: color-mix(in srgb, oklch(0.63 0.2 15) 50%, transparent);"},
		{"#336699", "--primary: #336699; --ring: color-mix(in srgb, #336699 50%, transparent);"},
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
			if strings.Contains(out, "<style>") || strings.Contains(out, "data-accent") {
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
	if n, err := staticFS.ReadFile("static/NOTICE-gloock.txt"); err != nil || !strings.Contains(string(n), "SIL OPEN FONT LICENSE Version 1.1") {
		t.Fatal("the Gloock font must ship with its licence")
	}
}

func TestSidebarMarksTheCurrentPage(t *testing.T) {
	r, err := newRenderer("", "https://id.example.org")
	if err != nil {
		t.Fatal(err)
	}
	render := func(name string, s *Session) string {
		rec := httptest.NewRecorder()
		r.page(rec, 200, name, view{Title: name, CSRF: "tok", Session: s, Data: map[string]any{}})
		return rec.Body.String()
	}
	admin := &Session{Username: "alice", Admin: true}
	if out := render("members.html", admin); !strings.Contains(out, `<a href="/admin/members" class="active" aria-current="page">Members</a>`) ||
		strings.Contains(out, `<a href="/" class="active"`) {
		t.Errorf("members page: %s", out)
	}
	if out := render("name.html", admin); !strings.Contains(out, `<a href="/" class="active" aria-current="page">Your account</a>`) {
		t.Errorf("name page: %s", out)
	}
	if out := render("home.html", &Session{Username: "bob"}); strings.Contains(out, "/admin") || !strings.Contains(out, `class="sidebar"`) {
		t.Errorf("a member sees no admin links: %s", out)
	}
	if out := render("message.html", nil); strings.Contains(out, `class="sidebar"`) {
		t.Errorf("no sidebar without a session: %s", out)
	}
}

func TestInviteLinkHasCopyButton(t *testing.T) {
	r, err := newRenderer("", "https://id.example.org")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.page(rec, 200, "home.html", view{CSRF: "tok", Session: &Session{Username: "alice"},
		Data: map[string]any{"InvitesOn": true, "NewLink": "https://join.example.org/i/abc"}})
	out := rec.Body.String()
	if !strings.Contains(out, `<p class="link" id="invite-link">https://join.example.org/i/abc</p>`) ||
		!strings.Contains(out, `data-copy="invite-link" hidden>Copy link</button>`) {
		t.Fatalf("no copy button for the link: %s", out)
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
	type invitee struct {
		Username, DisplayName string
		Gone, Unavailable     bool
	}
	type invite struct {
		ID, State string
		CreatedAt time.Time
		Invitee   *invitee
	}
	sess := &Session{Sub: "u1", Username: "alice", Admin: true, CSRF: "tok"}
	pages := map[string]any{
		"home.html": map[string]any{
			"InvitesOn": true, "Remaining": "2 invites left", "NewLink": "https://x.example/join/abc", "Expires": "1 Nov",
			"Invites": []invite{{"i1", "open", now, nil}, {"i2", "used", now, &invitee{Username: "bob", DisplayName: "Bob"}},
				{"i3", "used", now, &invitee{Gone: true}}, {"i4", "used", now, &invitee{Unavailable: true}}},
			"Apps": []flow.App{{ID: "c1", Name: "Forum", URL: "https://forum.example.org", Host: "forum.example.org", LogoURL: "https://id.example.org/api/oidc/clients/c1/logo"},
				{ID: "c2", Name: "wiki", URL: "https://wiki.example.org", Host: "wiki.example.org", Letter: "W"},
				{ID: "c3", URL: "https://blank.example.org", Host: "blank.example.org"}},
		},
		// The Your apps card with no app, and with Pocket ID unreachable.
		"home.html empty":       map[string]any{},
		"home.html unavailable": map[string]any{"AppsUnavailable": true},
		"admin.html": []application{
			{"a1", "Bob", "bob", "b@example.org", "alice", "pending", now, declines{2, now}, false},
			{"a2", "Cy", "cy", "c@example.org", "alice", "link_unsent", now, declines{}, false},
			{"a3", "Di", "di", "d@example.org", "alice", "approving", now, declines{}, false},
			{"a4", "Ed", "ed", "e@example.org", "alice", "approving", now, declines{}, true},
		},
		"invite.html":  map[string]string{"Inviter": "alice", "CoC": "Be kind.", "Username": "bob", "DisplayName": "Bob", "Email": "b@example.org"},
		"message.html": map[string]string{"Heading": "Hello", "Body": "b"},
	}
	for key, data := range pages {
		name, _, _ := strings.Cut(key, " ")
		rec := httptest.NewRecorder()
		r.page(rec, 200, name, view{Title: name, CSRF: "tok", Session: sess, Data: data})
		out := rec.Body.String()
		if !strings.Contains(out, "</html>") {
			t.Errorf("%s did not render to the end: %q", key, out)
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

func TestNamePageShowsUsernameAsCode(t *testing.T) {
	r, err := newRenderer("", "https://id.example.org")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.page(rec, 200, "name.html", view{CSRF: "tok", Session: &Session{Username: "jane"},
		Data: map[string]any{"Username": "jane", "Name": map[string]string{}}})
	if out := rec.Body.String(); !strings.Contains(out, "<p>Your username is <code>jane</code>; it can not be changed.</p>") {
		t.Fatalf("name page: %s", out)
	}
}

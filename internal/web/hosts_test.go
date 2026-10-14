package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// asOn is as on host: a request with sess's cookie and, for a form, its CSRF.
func (h harness) asOn(host string, sess Session, method, path string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.s.write(rec, sess)
	body := ""
	if form != nil {
		body = form.Encode()
	}
	req := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	req.RemoteAddr = "203.0.113.1:1"
	out := httptest.NewRecorder()
	h.h.ServeHTTP(out, req)
	return out
}

func TestRoutesByHost(t *testing.T) {
	h := newHarness(t, true)
	cases := []struct {
		host, method, path string
		want               int
	}{
		{"invite.example.org", "GET", "/i/nope", http.StatusNotFound}, // unknown token: the page's own 404, routed
		{"account.example.org", "GET", "/i/nope", http.StatusNotFound},
		{"invite.example.org", "GET", "/admin", http.StatusNotFound},
		{"invite.example.org", "GET", "/", http.StatusNotFound},
		{"invite.example.org", "GET", "/auth/login", http.StatusNotFound},
		{"invite.example.org", "GET", "/auth/callback", http.StatusNotFound},
		{"account.example.org", "GET", "/", http.StatusFound}, // to /auth/login
		{"ACCOUNT.example.org:443", "GET", "/", http.StatusFound},
		{"Account.Example.Org", "GET", "/", http.StatusFound},
		{"account.example.org:8443", "GET", "/", http.StatusFound},
		{"INVITE.example.org:443", "GET", "/admin", http.StatusNotFound},
		{"10.0.0.5:8082", "GET", "/healthz", http.StatusOK},
		{"account.example.org", "GET", "/healthz", http.StatusOK},
		{"invite.example.org", "GET", "/static/style.css", http.StatusOK},
		{"account.example.org", "GET", "/static/style.css", http.StatusOK},
		{"elsewhere.example.org", "GET", "/", http.StatusNotFound},
		{"elsewhere.example.org", "GET", "/static/style.css", http.StatusNotFound},
		{"10.0.0.5:8082", "GET", "/admin", http.StatusNotFound},
	}
	for _, c := range cases {
		rec := h.get(c.host, c.path)
		if rec.Code != c.want {
			t.Errorf("%s %s%s: %d, want %d", c.method, c.host, c.path, rec.Code, c.want)
		}
	}
}

// Every route answers on its own host, with a signed-in admin, and is a 404 on
// the other host even with the same cookie. /static/ is on both, and
// TestRoutesByHost covers it.
func TestEveryRouteOnlyOnItsHost(t *testing.T) {
	h := newHarness(t, true)
	applyBob(t, h)
	p, _ := h.flow.Pending(context.Background())
	app := itoa(p[0].ID)
	csrf := url.Values{"csrf": {"tok"}}
	type route struct {
		method, path string
		form         url.Values
	}
	account := []route{
		{"GET", "/", nil},
		{"GET", "/auth/login", nil},
		{"GET", "/auth/callback", nil},
		{"POST", "/auth/logout", csrf},
		{"POST", "/invites", createForm()},
		{"POST", "/invites/1/revoke", csrf},
		{"GET", "/admin", nil},
		{"POST", "/admin/applications/" + app + "/dismiss", csrf},
		{"POST", "/admin/applications/" + app + "/decline", csrf},
		{"POST", "/admin/applications/" + app + "/approve", csrf},
	}
	for _, r := range account {
		if rec := h.asOn(inviteHost, admin, r.method, r.path, r.form); rec.Code != http.StatusNotFound {
			t.Errorf("%s invite.%s: %d, want 404", r.method, r.path, rec.Code)
		}
		if rec := h.asOn(accountHost, admin, r.method, r.path, r.form); rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s account.%s: %d, want the route's own status", r.method, r.path, rec.Code)
		}
	}
	tok, inviteeCSRF := h.invite(t)
	invitee := []route{
		{"GET", "/i/" + tok, nil},
		{"POST", "/i/" + tok, form(inviteeCSRF)},
		{"GET", "/static/style.css", nil},
	}
	for _, r := range invitee[:2] {
		if rec := h.asOn(accountHost, admin, r.method, r.path, r.form); rec.Code != http.StatusNotFound {
			t.Errorf("%s account.%s: %d, want 404", r.method, r.path, rec.Code)
		}
	}
	for _, r := range invitee {
		if rec := h.asOn(inviteHost, Session{}, r.method, r.path, r.form); rec.Code != http.StatusOK {
			t.Errorf("%s invite.%s: %d, want 200", r.method, r.path, rec.Code)
		}
	}
}

func TestInviteRouteReachesHandlerOnlyOnInviteHost(t *testing.T) {
	h := newHarness(t, true)
	tok, _ := h.invite(t)
	for host, want := range map[string]int{inviteHost: http.StatusOK, accountHost: http.StatusNotFound} {
		if rec := h.get(host, "/i/"+tok); rec.Code != want {
			t.Errorf("%s: %d, want %d", host, rec.Code, want)
		}
	}
	// The mux's 404 on the account host is not the invite page's own.
	if body := h.get(accountHost, "/i/"+tok).Body.String(); strings.Contains(body, "can&#39;t be used") {
		t.Errorf("account host reached the invite handler: %s", body)
	}
}

// With invites off, a deployment that had issued invites still answers: old
// links are 404 on every host, and home and /admin render.
func TestInvitesOff(t *testing.T) {
	on := newHarness(t, true)
	tok, inviteeCSRF := on.invite(t)
	h := newHarnessInvites(t, false)
	for _, host := range []string{accountHost, inviteHost} {
		if rec := h.get(host, "/i/"+tok); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s/i/<token>: %d", host, rec.Code)
		}
		if rec := h.asOn(host, Session{}, "POST", "/i/"+tok, form(inviteeCSRF)); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s/i/<token>: %d", host, rec.Code)
		}
		if rec := h.get(host, "/i/x"); rec.Code != http.StatusNotFound {
			t.Errorf("%s/i/x: %d", host, rec.Code)
		}
	}
	for _, path := range []string{"/invites", "/invites/1/revoke", "/admin/applications/1/approve", "/admin/applications/1/decline", "/admin/applications/1/dismiss"} {
		f := url.Values{"csrf": {"tok"}}
		if path == "/invites" {
			f = createForm()
		}
		if rec := h.as(admin, "POST", path, f); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s with invites off: %d", path, rec.Code)
		}
	}
	if inv, _ := h.flow.Invites(context.Background(), admin.member()); len(inv) != 0 {
		t.Fatal("an invite was made with invites off")
	}
	rec := h.as(member, "GET", "/", nil)
	if body := rec.Body.String(); rec.Code != http.StatusOK || strings.Contains(body, "Invite someone") ||
		strings.Contains(body, "/invites") || !strings.Contains(body, "<p>Signed in as alice.</p>") {
		t.Errorf("home with invites off: %d %q", rec.Code, body)
	}
	rec = h.as(admin, "GET", "/", nil)
	if body := rec.Body.String(); rec.Code != http.StatusOK || strings.Contains(body, "Applications waiting") {
		t.Errorf("admin home with invites off: %d %q", rec.Code, body)
	}
	rec = h.as(admin, "GET", "/admin", nil)
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "Nothing is waiting.") {
		t.Errorf("admin with invites off: %d %q", rec.Code, body)
	}
	if rec := h.get("10.0.0.5:8082", "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("healthz with invites off: %d", rec.Code)
	}
	if rec := h.get(accountHost, "/static/style.css"); rec.Code != http.StatusOK {
		t.Errorf("static on the account host with invites off: %d", rec.Code)
	}
}

// The session and login cookies carry no Domain, so a browser sends them to
// the account host alone and the invitee origin never holds a session.
func TestCookiesHaveNoDomain(t *testing.T) {
	s := sessions{key: make([]byte, 32), secure: true}
	rec := httptest.NewRecorder()
	s.write(rec, member)
	a := &authenticator{s: s}
	a.login(rec, httptest.NewRequest("GET", "http://"+accountHost+"/auth/login", nil))
	names := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		names[c.Name] = true
		if c.Domain != "" {
			t.Errorf("%s carries Domain %q", c.Name, c.Domain)
		}
		if raw := rec.Result().Header.Values("Set-Cookie"); strings.Contains(strings.ToLower(strings.Join(raw, "\n")), "domain=") {
			t.Errorf("Set-Cookie carries a Domain: %q", raw)
		}
	}
	if !names["account_session"] || !names["account_login"] {
		t.Fatalf("cookies: %v", names)
	}
}

// The provider sends the member back to the account host, never the invite
// host.
func TestOIDCRedirectIsOnTheAccountHost(t *testing.T) {
	a := newTestAuthenticator(t, newFakeOP(t))
	rec := httptest.NewRecorder()
	a.login(rec, httptest.NewRequest("GET", "http://"+accountHost+"/auth/login", nil))
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query().Get("redirect_uri"); got != "https://account.example.org/auth/callback" {
		t.Fatalf("redirect_uri %q", got)
	}
}

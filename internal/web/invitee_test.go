package web

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-invite/internal/flow"
)

func (h harness) invite(t *testing.T) (string, string) {
	return h.inviteAs(t, flow.Member{Sub: "s-alice", Username: "alice"})
}

func (h harness) inviteAs(t *testing.T, m flow.Member) (string, string) {
	link, _, err := h.flow.CreateInvite(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	tok := link[strings.LastIndex(link, "/")+1:]
	th, _ := flow.TokenHash(tok)
	return tok, inviteeCSRF(make([]byte, 32), th)
}

func post(h harness, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.1:1"
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func form(csrf string) url.Values {
	return url.Values{"csrf": {csrf}, "agree": {"yes"}, "username": {"bob"}, "display_name": {"Bob"}, "email": {"bob@x.org"}}
}

func TestInvitePageShowsCoC(t *testing.T) {
	h := newHarness(t, true)
	tok, _ := h.invite(t)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/i/"+tok, nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Be kind") || !strings.Contains(rec.Body.String(), "alice") {
		t.Fatalf("%d", rec.Code)
	}
}

func TestBadTokenIsNeutral(t *testing.T) {
	h := newHarness(t, true)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/i/nope", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "can't be used") {
		t.Fatalf("%d", rec.Code)
	}
}

func TestInviteeCSRFBoundToInvite(t *testing.T) {
	h := newHarness(t, true)
	tokA, _ := h.invite(t)
	_, csrfB := h.inviteAs(t, flow.Member{Sub: "s-root", Username: "root", Admin: true}) // admins have no quota
	if rec := post(h, "/i/"+tokA, form(csrfB)); rec.Code != 403 {
		t.Fatalf("csrf from another invite accepted: %d", rec.Code)
	}
	if rec := post(h, "/i/"+tokA, form("")); rec.Code != 403 {
		t.Fatalf("missing csrf accepted: %d", rec.Code)
	}
	if p, _ := h.flow.Pending(context.Background()); len(p) != 0 {
		t.Fatal("nothing may be stored")
	}
}

func TestSubmitAndReuse(t *testing.T) {
	h := newHarness(t, true)
	tok, csrf := h.invite(t)
	if rec := post(h, "/i/"+tok, form(csrf)); rec.Code != 200 || !strings.Contains(rec.Body.String(), "admin will review") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := post(h, "/i/"+tok, form(csrf)); rec.Code != 404 {
		t.Fatalf("second use must be the neutral page: %d", rec.Code)
	}
}

func TestValidationKeepsInput(t *testing.T) {
	h := newHarness(t, true)
	tok, csrf := h.invite(t)
	f := form(csrf)
	f.Set("username", "-bad")
	rec := post(h, "/i/"+tok, f)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "bob@x.org") || !strings.Contains(rec.Body.String(), "Username must") {
		t.Fatalf("%d", rec.Code)
	}
}

func TestHostileValuesAreEscaped(t *testing.T) {
	h := newHarness(t, true)
	tok, csrf := h.invite(t)
	f := form(csrf)
	f.Set("username", "-bad")
	f.Set("display_name", `"><script>x</script>`)
	rec := post(h, "/i/"+tok, f)
	body := rec.Body.String()
	if rec.Code != 422 || strings.Contains(body, "<script>x") || !strings.Contains(body, "&lt;script&gt;x") {
		t.Fatalf("%d %s", rec.Code, body)
	}
}

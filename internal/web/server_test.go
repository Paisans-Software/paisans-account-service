package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/coc"
	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/idp"
	"github.com/paisans-software/paisans-invite/internal/idp/idptest"
	"github.com/paisans-software/paisans-invite/internal/mail/mailtest"
	"github.com/paisans-software/paisans-invite/internal/store"
	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

type harness struct {
	h    http.Handler
	s    sessions
	fake *idptest.Fake
	flow *flow.Service
	st   *store.Store
}

func newHarness(t *testing.T, approval bool) harness {
	return newHarnessIDP(t, approval, "https://id.example.org")
}

// newHarnessIDP is newHarness with the identity provider at idpURL, the value
// New takes from config.Config.IDPURL.
func newHarnessIDP(t *testing.T, approval bool, idpURL string) harness {
	return newHarnessWith(t, approval, idpURL, 7)
}

// newHarnessWith also takes the sign-in link's lifetime in days, the value
// New takes from config.Config.LoginLinkDays.
func newHarnessWith(t *testing.T, approval bool, idpURL string, linkDays int) harness {
	st := storetest.New(t)
	fake := &idptest.Fake{Users: []idp.User{{ID: "r", Username: "root", Email: "root@x.org"}}, Groups: map[string][]string{"admins": {"root"}}}
	key := make([]byte, 32)
	f := flow.New(st, fake, &mailtest.Recorder{}, coc.CoC{Version: "v", HTML: "<p>Be kind</p>"}, flow.Settings{
		Quota: 1, RequireApproval: approval, InviteLifetime: time.Hour, PublicURL: "https://invite.example.org", EmailHashKey: key, AdminGroup: "admins",
		LoginLinkDays: linkDays,
	}, time.Now)
	r, err := newRenderer("", idpURL)
	if err != nil {
		t.Fatal(err)
	}
	s := sessions{key: key}
	return harness{h: NewServer(Options{Flow: f, Sessions: s, Auth: http.NotFoundHandler(), Render: r, Health: st, SessionKey: key}), s: s, fake: fake, flow: f, st: st}
}

func (h harness) as(sess Session, method, path string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.s.write(rec, sess)
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	out := httptest.NewRecorder()
	h.h.ServeHTTP(out, req)
	return out
}

// createForm is the create form as the home page renders it: the session's
// CSRF token and a fresh one-time nonce.
func createForm() url.Values {
	return url.Values{"csrf": {"tok"}, "nonce": {flow.NewNonce()}}
}

var member = Session{Sub: "s-alice", Username: "alice", CSRF: "tok", Exp: time.Now().Add(time.Hour).Unix()}
var admin = Session{Sub: "s-root", Username: "root", Admin: true, CSRF: "tok", Exp: time.Now().Add(time.Hour).Unix()}

func TestAnonymousRedirectsToLogin(t *testing.T) {
	h := newHarness(t, true)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 302 || rec.Header().Get("Location") != "/auth/login?next=%2F" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCreateInviteShowsLinkOnceAndEnforcesQuota(t *testing.T) {
	h := newHarness(t, true)
	rec := h.as(member, "POST", "/invites", createForm())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "https://invite.example.org/i/") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = h.as(member, "POST", "/invites", createForm())
	if !strings.Contains(rec.Body.String(), "quota") {
		t.Fatal("second invite past a quota of 1 must be refused with a message")
	}
	if rec = h.as(admin, "POST", "/invites", createForm()); !strings.Contains(rec.Body.String(), "/i/") {
		t.Fatal("admins have no quota")
	}
}

func TestCSRFRequired(t *testing.T) {
	h := newHarness(t, true)
	if rec := h.as(member, "POST", "/invites", url.Values{"csrf": {"wrong"}}); rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestMemberCannotSeeAdmin(t *testing.T) {
	h := newHarness(t, true)
	if rec := h.as(member, "GET", "/admin", nil); rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
	if rec := h.as(member, "POST", "/admin/applications/1/approve", url.Values{"csrf": {"tok"}}); rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestAdminApproves(t *testing.T) {
	h := newHarness(t, true)
	applyBob(t, h)
	rec := h.as(admin, "GET", "/admin", nil)
	if !strings.Contains(rec.Body.String(), "bob@x.org") {
		t.Fatal("queue must list the application")
	}
	p, err := h.flow.Pending(context.Background())
	if err != nil || len(p) != 1 {
		t.Fatal(err, len(p))
	}
	rec = h.as(admin, "POST", "/admin/applications/"+itoa(p[0].ID)+"/approve", url.Values{"csrf": {"tok"}})
	if rec.Code != 303 || h.fake.Created != 1 {
		t.Fatalf("%d created=%d", rec.Code, h.fake.Created)
	}
}

func TestHealthz(t *testing.T) {
	h := newHarness(t, true)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}

func applyBob(t *testing.T, h harness) {
	t.Helper()
	link, _, err := h.flow.CreateInvite(context.Background(), flow.Member{Sub: "s-alice", Username: "alice"}, flow.NewNonce())
	if err != nil {
		t.Fatal(err)
	}
	tok := link[strings.LastIndex(link, "/")+1:]
	if _, err := h.flow.Apply(context.Background(), tok, flow.Applicant{Username: "bob", Email: "bob@x.org", DisplayName: "Bob", Agreed: true}); err != nil {
		t.Fatal(err)
	}
}

func TestCSRFRefusedOnEveryPost(t *testing.T) {
	h := newHarness(t, true)
	applyBob(t, h)
	p, _ := h.flow.Pending(context.Background())
	id := itoa(p[0].ID)
	inv, _ := h.flow.Invites(context.Background(), flow.Member{Sub: "s-alice"})
	for _, c := range []struct {
		s    Session
		path string
	}{
		{member, "/auth/logout"},
		{member, "/invites/" + itoa(inv[0].ID) + "/revoke"},
		{admin, "/admin/applications/" + id + "/approve"},
		{admin, "/admin/applications/" + id + "/decline"},
	} {
		if rec := h.as(c.s, "POST", c.path, url.Values{"csrf": {"wrong"}}); rec.Code != 403 {
			t.Fatalf("%s: %d", c.path, rec.Code)
		}
	}
	if h.fake.Created != 0 {
		t.Fatal("approve ran")
	}
	if p, _ := h.flow.Pending(context.Background()); len(p) != 1 {
		t.Fatal("decline ran")
	}
	if inv, _ := h.flow.Invites(context.Background(), flow.Member{Sub: "s-alice"}); inv[0].State == "revoked" {
		t.Fatal("revoke ran")
	}
}

func TestAnonymousAdminRedirects(t *testing.T) {
	h := newHarness(t, true)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin", nil))
	if rec.Code != 302 || rec.Header().Get("Location") != "/auth/login?next=%2Fadmin" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	// A form post cannot be replayed after sign-in, so it carries no next.
	rec = httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("POST", "/invites", nil))
	if rec.Code != 302 || rec.Header().Get("Location") != "/auth/login" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAdminDeclines(t *testing.T) {
	h := newHarness(t, true)
	applyBob(t, h)
	p, _ := h.flow.Pending(context.Background())
	rec := h.as(admin, "POST", "/admin/applications/"+itoa(p[0].ID)+"/decline", url.Values{"csrf": {"tok"}})
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	if p, _ := h.flow.Pending(context.Background()); len(p) != 0 {
		t.Fatal("still pending")
	}
}

func TestMemberRevokes(t *testing.T) {
	h := newHarness(t, true)
	h.as(member, "POST", "/invites", createForm())
	inv, _ := h.flow.Invites(context.Background(), flow.Member{Sub: "s-alice"})
	rec := h.as(member, "POST", "/invites/"+itoa(inv[0].ID)+"/revoke", url.Values{"csrf": {"tok"}})
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	if inv, _ := h.flow.Invites(context.Background(), flow.Member{Sub: "s-alice"}); inv[0].State != "revoked" {
		t.Fatal(inv[0].State)
	}
}

func TestNewLinkNeverLeavesTheResponseBody(t *testing.T) {
	h := newHarness(t, true)
	rec := h.as(member, "POST", "/invites", createForm())
	body := rec.Body.String()
	i := strings.Index(body, "https://invite.example.org/i/")
	if i < 0 {
		t.Fatal("no link")
	}
	tok := body[i+len("https://invite.example.org/i/"):][:43]
	if rec.Header().Get("Location") != "" {
		t.Fatal("link in Location")
	}
	for _, c := range rec.Result().Cookies() {
		if strings.Contains(c.Value, tok) {
			t.Fatal("link in cookie")
		}
	}
	if again := h.as(member, "GET", "/", nil); strings.Contains(again.Body.String(), tok) {
		t.Fatal("link shown twice")
	}
}

func TestAdminErrorBannerIsServerOwned(t *testing.T) {
	h := newHarness(t, true)
	if rec := h.as(admin, "GET", "/admin?error=Send+money+to+evil", nil); strings.Contains(rec.Body.String(), "evil") {
		t.Fatal("arbitrary text rendered")
	}
	rec := h.as(admin, "GET", "/admin?error=username_taken", nil)
	if !strings.Contains(rec.Body.String(), "That username is now taken.") {
		t.Fatal("mapped sentence missing")
	}
}

func TestRevokeBadIDIs400(t *testing.T) {
	h := newHarness(t, true)
	for _, id := range []string{"abc", "1x", "99999999999999999999"} {
		rec := h.as(member, "POST", "/invites/"+id+"/revoke", url.Values{"csrf": {"tok"}})
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Bad request") {
			t.Errorf("%s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
}

// A resubmitted create form, as after a refresh, makes no second invite and
// says where the first one is.
func TestCreateFormResubmittedMakesOneInvite(t *testing.T) {
	h := newHarness(t, true)
	f := createForm()
	first := h.as(admin, "POST", "/invites", f)
	if first.Code != 200 || !strings.Contains(first.Body.String(), "https://invite.example.org/i/") {
		t.Fatalf("%d %s", first.Code, first.Body.String())
	}
	again := h.as(admin, "POST", "/invites", f)
	body := again.Body.String()
	if again.Code != 200 || strings.Contains(body, "/i/") ||
		!strings.Contains(body, "That invite was already made. It&#39;s in your list below. If you didn&#39;t copy the link, revoke it and make a new one.") {
		t.Fatalf("%d %s", again.Code, body)
	}
	if inv, _ := h.flow.Invites(context.Background(), flow.Member{Sub: admin.Sub}); len(inv) != 1 {
		t.Fatalf("want one invite, got %d", len(inv))
	}
	h.as(admin, "POST", "/invites", createForm())
	if inv, _ := h.flow.Invites(context.Background(), flow.Member{Sub: admin.Sub}); len(inv) != 2 {
		t.Fatalf("a new form makes a new invite: %d", len(inv))
	}
}

// A member at quota who resubmits is told the invite exists, not that the
// quota is full.
func TestCreateFormResubmittedAtQuota(t *testing.T) {
	h := newHarness(t, true)
	f := createForm()
	h.as(member, "POST", "/invites", f)
	body := h.as(member, "POST", "/invites", f).Body.String()
	if !strings.Contains(body, "That invite was already made.") || strings.Contains(body, "reached your invite quota") {
		t.Fatal(body)
	}
}

func TestCreateFormNeedsNonce(t *testing.T) {
	h := newHarness(t, true)
	for name, n := range map[string][]string{
		"missing": nil, "empty": {""}, "not base64": {"!!!!!!!!!!!!!!!!!!!!!!"},
		"short": {"AAAAAAAAAAAAAAAAAAAA"}, "padded": {"AAAAAAAAAAAAAAAAAAAAAA=="},
	} {
		f := url.Values{"csrf": {"tok"}}
		if n != nil {
			f["nonce"] = n
		}
		if rec := h.as(member, "POST", "/invites", f); rec.Code != 400 {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if inv, _ := h.flow.Invites(context.Background(), flow.Member{Sub: member.Sub}); len(inv) != 0 {
		t.Fatal("nothing may be made")
	}
}

// Every render of the home page carries a new nonce, so the next press makes
// the next invite.
func TestHomeRendersFreshNonce(t *testing.T) {
	h := newHarness(t, true)
	re := regexp.MustCompile(`name="nonce" value="([A-Za-z0-9_-]{22})"`)
	a := re.FindStringSubmatch(h.as(member, "GET", "/", nil).Body.String())
	b := re.FindStringSubmatch(h.as(member, "GET", "/", nil).Body.String())
	if a == nil || b == nil || a[1] == b[1] {
		t.Fatalf("nonces %v %v", a, b)
	}
}

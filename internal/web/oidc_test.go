package web

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/coc"
	"github.com/paisans-software/paisans-account-service/internal/config"
	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/idp/idptest"
	"github.com/paisans-software/paisans-account-service/internal/mail/mailtest"
	"github.com/paisans-software/paisans-account-service/internal/store/storetest"
)

// fakeOP is a minimal OpenID provider: discovery, JWKS and a token endpoint
// that checks the client, the code and its PKCE verifier, and returns an
// RS256-signed ID token carrying the nonce the authorisation request sent.
type fakeOP struct {
	srv *httptest.Server
	key *rsa.PrivateKey
	mu  sync.Mutex
	// codes maps an issued code to what the authorisation request carried.
	codes map[string]grant
}

type grant struct {
	challenge, nonce, redirect string
	sub, username              string
	groups                     []string
}

func newFakeOP(t *testing.T) *fakeOP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	op := &fakeOP{key: key, codes: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": op.srv.URL, "authorization_endpoint": op.srv.URL + "/authorize",
			"token_endpoint": op.srv.URL + "/token", "jwks_uri": op.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", op.token)
	op.srv = httptest.NewServer(mux)
	t.Cleanup(op.srv.Close)
	return op
}

// authorize stands in for the person signing in at the provider: it reads the
// authorisation request the service redirected to and issues a code for it.
func (op *fakeOP) authorize(t *testing.T, location, sub, username string, groups []string) (state, code string) {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != op.srv.URL+"/authorize" {
		t.Fatalf("login did not redirect to the provider: %s", location)
	}
	q := u.Query()
	if q.Get("client_id") != "invite" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("nonce") == "" || q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Fatalf("authorisation request: %v", q)
	}
	code = randomString()
	op.mu.Lock()
	op.codes[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri"),
		sub: sub, username: username, groups: groups}
	op.mu.Unlock()
	return q.Get("state"), code
}

func (op *fakeOP) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	op.mu.Lock()
	g, found := op.codes[r.PostForm.Get("code")]
	delete(op.codes, r.PostForm.Get("code"))
	op.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if id != "invite" || secret != "secret" || r.PostForm.Get("grant_type") != "authorization_code" || !found ||
		r.PostForm.Get("redirect_uri") != g.redirect || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss": op.srv.URL, "aud": "invite", "sub": g.sub, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"nonce": g.nonce, "preferred_username": g.username, "groups": g.groups,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": op.sign(claims)})
}

func (op *fakeOP) sign(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	input := enc(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, op.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newTestAuthenticator(t *testing.T, op *fakeOP) *authenticator {
	t.Helper()
	cfg := config.Config{OIDCIssuer: op.srv.URL, OIDCClientID: "invite", OIDCClientSecret: "secret",
		AccountURL: "https://account.example.org", MemberGroup: "members", AdminGroup: "admins"}
	a, err := newAuthenticator(context.Background(), cfg, sessions{key: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// signIn runs the whole round trip: the login redirect, the provider's code,
// and the callback. tamperGrant may change what the provider recorded for the
// code, and tamperQuery the callback query, before the callback is sent.
func signIn(t *testing.T, a *authenticator, op *fakeOP, groups []string, tamperGrant func(*grant), tamperQuery func(url.Values)) *httptest.ResponseRecorder {
	t.Helper()
	login := httptest.NewRecorder()
	a.login(login, httptest.NewRequest("GET", "/auth/login?next=%2Fadmin", nil))
	if login.Code != http.StatusFound {
		t.Fatalf("login: %d", login.Code)
	}
	state, code := op.authorize(t, login.Header().Get("Location"), "sub-1", "alice", groups)
	if tamperGrant != nil {
		op.mu.Lock()
		g := op.codes[code]
		tamperGrant(&g)
		op.codes[code] = g
		op.mu.Unlock()
	}
	q := url.Values{"state": {state}, "code": {code}}
	if tamperQuery != nil {
		tamperQuery(q)
	}
	req := httptest.NewRequest("GET", "/auth/callback?"+q.Encode(), nil)
	for _, c := range login.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.callback(rec, req)
	return rec
}

func sessionFrom(a *authenticator, rec *httptest.ResponseRecorder) (Session, bool) {
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return a.s.read(req)
}

func TestSignInRoundTrip(t *testing.T) {
	op := newFakeOP(t)
	a := newTestAuthenticator(t, op)
	for name, c := range map[string]struct {
		groups      []string
		code        int
		admin       bool
		hasSession  bool
		redirectsTo string
	}{
		"member":     {[]string{"members"}, http.StatusFound, false, true, "/admin"},
		"admin":      {[]string{"admins"}, http.StatusFound, true, true, "/admin"},
		"non-member": {[]string{"other"}, http.StatusForbidden, false, false, ""},
		"no groups":  {nil, http.StatusForbidden, false, false, ""},
	} {
		rec := signIn(t, a, op, c.groups, nil, nil)
		if rec.Code != c.code || rec.Header().Get("Location") != c.redirectsTo {
			t.Fatalf("%s: %d %q", name, rec.Code, rec.Header().Get("Location"))
		}
		sess, ok := sessionFrom(a, rec)
		if ok != c.hasSession || (ok && (sess.Admin != c.admin || sess.Sub != "sub-1" || sess.Username != "alice" || sess.CSRF == "")) {
			t.Fatalf("%s: session %+v %v", name, sess, ok)
		}
	}
}

// Each sign-in that does not check out is refused without a session: a forged
// code or state, a PKCE verifier the code was not issued for (the provider
// refuses it), and an ID token minted for another sign-in's nonce.
func TestSignInRefusals(t *testing.T) {
	op := newFakeOP(t)
	a := newTestAuthenticator(t, op)
	for name, c := range map[string]struct {
		grant func(*grant)
		query func(url.Values)
		code  int
	}{
		"unknown code": {nil, func(q url.Values) { q.Set("code", "forged") }, http.StatusBadGateway},
		"state":        {nil, func(q url.Values) { q.Set("state", "other") }, http.StatusBadRequest},
		"pkce":         {func(g *grant) { g.challenge = "not-this-verifier" }, nil, http.StatusBadGateway},
		"nonce":        {func(g *grant) { g.nonce = "another-sign-in" }, nil, http.StatusBadGateway},
	} {
		rec := signIn(t, a, op, []string{"admins"}, c.grant, c.query)
		if rec.Code != c.code {
			t.Errorf("%s: %d, want %d", name, rec.Code, c.code)
		}
		if _, ok := sessionFrom(a, rec); ok {
			t.Errorf("%s: session set", name)
		}
	}
}

// New links members to Pocket ID at the issuer, its public URL, and never at
// ACCOUNT_IDP_URL, the private address the API key goes to.
func TestNewLinksToTheIssuer(t *testing.T) {
	op := newFakeOP(t)
	st := storetest.New(t)
	f := flow.New(st, &idptest.Fake{}, &mailtest.Recorder{}, coc.CoC{Version: "v", HTML: "<p>Be kind</p>"}, flow.Settings{
		Quota: 1, InviteLifetime: time.Hour, EmailHashKey: make([]byte, 32), AdminGroup: "admins",
		AccountURL: "https://" + accountHost, JoinURL: "https://" + joinHost, LoginLinkDays: 7, IDPURL: op.srv.URL,
	}, time.Now)
	cfg := config.Config{OIDCIssuer: op.srv.URL, OIDCClientID: "invite", OIDCClientSecret: "secret",
		AccountURL: "https://" + accountHost, JoinURL: "https://" + joinHost, Signup: config.SignupOpen,
		MemberGroup: "members", AdminGroup: "admins", SessionKey: make([]byte, 32), IDPURL: "http://10.44.0.1:1413"}
	h, err := New(context.Background(), cfg, f, st)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://"+joinHost+"/", nil))
	page := rec.Body.String() + rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(page, op.srv.URL+"/api/application-images/logo") || strings.Contains(page, "10.44.0.1") {
		t.Fatalf("the page must take Pocket ID's address from the issuer:\n%s", page)
	}
}

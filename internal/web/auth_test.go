package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdmitRefusesNonMember(t *testing.T) {
	a := &authenticator{memberGrp: "members", adminGrp: "admins", s: sessions{key: make([]byte, 32)}}
	cases := []struct {
		groups []string
		code   int
		admin  bool
	}{
		{nil, 403, false},
		{[]string{"other"}, 403, false},
		{[]string{"members"}, 302, false},
		{[]string{"admins"}, 302, true},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		a.admit(rec, httptest.NewRequest("GET", "/auth/callback", nil), "sub", "alice", c.groups)
		if rec.Code != c.code {
			t.Fatalf("%v: code %d", c.groups, rec.Code)
		}
		req := httptest.NewRequest("GET", "/", nil)
		for _, ck := range rec.Result().Cookies() {
			req.AddCookie(ck)
		}
		sess, ok := a.s.read(req)
		if ok != (c.code == 302) || (ok && sess.Admin != c.admin) {
			t.Fatalf("%v: session %v %v", c.groups, sess, ok)
		}
	}
}

func TestCallbackPreProviderRefusals(t *testing.T) {
	key := make([]byte, 32)
	a := &authenticator{s: sessions{key: key}}
	future := time.Now().Add(time.Hour).Unix()
	good, _ := sign(key, "login", loginState{State: "s", Nonce: "n", Verifier: "v", Exp: future})
	expired, _ := sign(key, "login", loginState{State: "s", Exp: time.Now().Add(-time.Minute).Unix()})
	wrongPurpose, _ := sign(key, "session", loginState{State: "s", Exp: future})
	cases := map[string]struct{ cookie, query string }{
		"missing":       {"", "s"},
		"tampered":      {good[:len(good)-2] + "AA", "s"},
		"expired":       {expired, "s"},
		"state":         {good, "other"},
		"wrong purpose": {wrongPurpose, "s"},
	}
	for name, c := range cases {
		req := httptest.NewRequest("GET", "/auth/callback?state="+c.query+"&code=x", nil)
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "invite_login", Value: c.cookie})
		}
		rec := httptest.NewRecorder()
		a.callback(rec, req)
		if rec.Code != 400 {
			t.Fatalf("%s: code %d", name, rec.Code)
		}
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == "invite_session" {
				t.Fatalf("%s: session cookie set", name)
			}
		}
	}
}

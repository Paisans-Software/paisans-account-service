package web

import (
	"net/http/httptest"
	"testing"
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

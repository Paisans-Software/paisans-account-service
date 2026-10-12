package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	s := sessions{key: make([]byte, 32)}
	rec := httptest.NewRecorder()
	s.write(rec, Session{Sub: "x", Username: "alice", CSRF: "c", Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	got, ok := s.read(req)
	if !ok || got.Username != "alice" {
		t.Fatalf("%v %v", got, ok)
	}
}

func TestSessionTamperAndExpiry(t *testing.T) {
	s := sessions{key: make([]byte, 32)}
	for name, c := range map[string]*http.Cookie{
		"tampered": {Name: "invite_session", Value: "eyJTdWIiOiJ4In0.AAAA"},
		"garbage":  {Name: "invite_session", Value: "nope"},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(c)
		if _, ok := s.read(req); ok {
			t.Fatal(name)
		}
	}
	rec := httptest.NewRecorder()
	s.write(rec, Session{Sub: "x", Exp: time.Now().Add(-time.Minute).Unix()})
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if _, ok := s.read(req); ok {
		t.Fatal("expired session accepted")
	}
}

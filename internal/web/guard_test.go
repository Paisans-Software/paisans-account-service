package web

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:5555"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := clientIP(r, true); got != "203.0.113.9" {
		t.Fatalf("trusted: rightmost entry, got %s", got)
	}
	if got := clientIP(r, false); got != "10.0.0.2" {
		t.Fatalf("untrusted: remote address, got %s", got)
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	allowed := 0
	for i := 0; i < 30; i++ {
		if l.allow("1.2.3.4") {
			allowed++
		}
	}
	if allowed != 20 {
		t.Fatalf("burst should be 20, got %d", allowed)
	}
	if !l.allow("5.6.7.8") {
		t.Fatal("buckets are per address")
	}
}

func TestInviteeCSRFDiffersPerInvite(t *testing.T) {
	k := make([]byte, 32)
	if inviteeCSRF(k, []byte("a")) == inviteeCSRF(k, []byte("b")) {
		t.Fatal("token must be bound to the invite")
	}
}

package web

import (
	"net/http"
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

func TestClientIPUsesLastHeaderLineAndFallsBack(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:5555"
	r.Header.Add("X-Forwarded-For", "6.6.6.6")
	r.Header.Add("X-Forwarded-For", "1.1.1.1, 203.0.113.9")
	if got := clientIP(r, true); got != "203.0.113.9" {
		t.Fatalf("got %s", got)
	}
	r.Header.Set("X-Forwarded-For", "6.6.6.6,")
	if got := clientIP(r, true); got != "10.0.0.2" {
		t.Fatalf("empty entry: got %s", got)
	}
}

func TestLimitKey(t *testing.T) {
	for in, want := range map[string]string{
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2::9":      "2001:db8:1:2::/64",
		"2001:db8:1:3::1":      "2001:db8:1:3::/64",
		"203.0.113.7":          "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"not an address":       "not an address",
		"2001:db8::1%eth0":     "2001:db8::1%eth0",
	} {
		if got := limitKey(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

// Through the handler: two addresses in one /64 share a bucket, another /64
// does not, and IPv4 stays per address.
func TestLimiterGroupsIPv6By64(t *testing.T) {
	s := &server{limit: newLimiter()}
	h := s.limited(func(w http.ResponseWriter, r *http.Request) {})
	hit := func(addr string) int {
		req := httptest.NewRequest("GET", "/i/x", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	for i := 0; i < 20; i++ {
		if hit("[2001:db8:1:2::1]:1") != 200 {
			t.Fatal("burst refused early")
		}
	}
	if hit("[2001:db8:1:2:ffff::9]:1") != 429 {
		t.Fatal("another address in the same /64 must share the bucket")
	}
	if hit("[2001:db8:1:3::1]:1") != 200 {
		t.Fatal("another /64 has its own bucket")
	}
	for i := 0; i < 20; i++ {
		hit("203.0.113.1:1")
	}
	if hit("203.0.113.1:1") != 429 || hit("203.0.113.2:1") != 200 {
		t.Fatal("IPv4 is limited per address")
	}
	for i := 0; i < 20; i++ {
		hit("[::ffff:198.51.100.1]:1")
	}
	if hit("198.51.100.1:1") != 429 || hit("[::ffff:198.51.100.2]:1") != 200 {
		t.Fatal("IPv4-mapped IPv6 is the IPv4 address")
	}
}

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return port
}

func TestHealthcheck(t *testing.T) {
	ok, bad := serve(t, 200), serve(t, 503)
	for _, tc := range []struct {
		name, addr string
		want       int
	}{
		{"ok port only", ":" + ok, 0},
		{"ok wildcard host", "0.0.0.0:" + ok, 0},
		{"unhealthy", ":" + bad, 1},
		{"malformed", "nonsense", 1},
	} {
		if got := healthcheck(tc.addr); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
	// A port that was just free and is now closed: nothing listens there.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	if got := healthcheck(":" + port); got != 1 {
		t.Errorf("unreachable: got %d", got)
	}
}

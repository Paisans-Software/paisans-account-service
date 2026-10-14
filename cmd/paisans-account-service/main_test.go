package main

import (
	"context"
	"crypto/sha256"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/config"
	"github.com/paisans-software/paisans-account-service/internal/store"
	"github.com/paisans-software/paisans-account-service/internal/store/storetest"
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

// The sweeper takes no notice of the signup mode: under open signup, an
// invite issued under invite signup still expires.
func TestSweepRunsUnderOpenSignup(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	h := sha256.Sum256([]byte("old"))
	inv, err := st.CreateInvite(ctx, h[:], "sub", "alice", "AAAAAAAAAAAAAAAAAAAAAA", time.Now().Add(-time.Minute), 0)
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		sweep(run, st, config.Config{Signup: config.SignupOpen, ApplicationWait: 14 * 24 * time.Hour, DeclineRetention: 183 * 24 * time.Hour})
	}()
	// sweep runs once at once, then waits an hour for the next.
	var list []store.Invite
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if list, err = st.ListInvites(ctx, "sub"); err == nil && len(list) == 1 && list[0].State == "expired" {
			break
		}
	}
	cancel()
	<-stopped
	if len(list) != 1 || list[0].ID != inv.ID || list[0].State != "expired" {
		t.Fatalf("invite not swept: %+v %v", list, err)
	}
}

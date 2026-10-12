package pocketid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/idp"
)

const week = 7 * 24 * time.Hour

func mustNew(t *testing.T, base string, hc *http.Client, ttl time.Duration) *Client {
	t.Helper()
	c, err := New(base, "KEY", hc, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// New refuses a lifetime Pocket ID would not accept or could not be sent in
// whole hours.
func TestNewBoundsLinkLifetime(t *testing.T) {
	for ttl, ok := range map[time.Duration]bool{
		time.Hour: true, 24 * time.Hour: true, 744 * time.Hour: true,
		0: false, -time.Hour: false, 59 * time.Minute: false, 745 * time.Hour: false,
		90 * time.Minute: false, 24*time.Hour + time.Second: false,
	} {
		c, err := New("https://id.example.org", "KEY", nil, ttl)
		if (err == nil) != ok || (c != nil) != ok {
			t.Errorf("%v: client %v, err %v", ttl, c != nil, err)
		}
		if err != nil && strings.Contains(err.Error(), "KEY") {
			t.Errorf("%v: error carries the key: %v", ttl, err)
		}
	}
}

type call struct {
	Method, Path, Query, Key, Raw string
	Body                          map[string]any
}

func fake(t *testing.T, users []map[string]any) (*Client, *[]call) {
	return fakePaged(t, users)
}

// fakePaged serves one page per argument. It honours the search query the way
// Pocket ID does on Postgres: a case-sensitive substring match.
func fakePaged(t *testing.T, pages ...[]map[string]any) (*Client, *[]call) {
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Key: r.Header.Get("X-API-Key")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			c.Raw = string(b)
			json.Unmarshal(b, &c.Body)
		}
		calls = append(calls, c)
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/users":
			page := 1
			fmt.Sscan(r.URL.Query().Get("pagination[page]"), &page)
			var data []map[string]any
			if page >= 1 && page <= len(pages) {
				for _, u := range pages[page-1] {
					term := r.URL.Query().Get("search")
					name, _ := u["username"].(string)
					email, _ := u["email"].(string)
					if term == "" || strings.Contains(name, term) || strings.Contains(email, term) {
						data = append(data, u)
					}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data, "pagination": map[string]any{"totalPages": len(pages), "currentPage": page}})
		case r.Method == "POST" && r.URL.Path == "/api/users":
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"id": "new-id", "username": c.Body["username"], "email": c.Body["email"]})
		case r.Method == "POST" && r.URL.Path == "/api/users/new-id/one-time-access-email":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return mustNew(t, srv.URL, srv.Client(), week), &calls
}

func TestUsernameTakenCaseInsensitive(t *testing.T) {
	c, _ := fake(t, []map[string]any{{"id": "1", "username": "Alice", "email": "a@x.org"}})
	if taken, err := c.UsernameTaken(context.Background(), "alice"); err != nil || !taken {
		t.Fatalf("want taken, got %v %v", taken, err)
	}
	if taken, _ := c.UsernameTaken(context.Background(), "alic"); taken {
		t.Fatal("a search hit that is not an exact match is not taken")
	}
}

func TestListingAsksForLargePages(t *testing.T) {
	c, calls := fake(t, []map[string]any{{"id": "1", "username": "a", "email": "a@x.org"}})
	c.UsernameTaken(context.Background(), "a")
	c.EmailTaken(context.Background(), "a@x.org")
	if len(*calls) != 2 {
		t.Fatalf("want 2 requests, got %d", len(*calls))
	}
	for _, got := range *calls {
		q, err := url.ParseQuery(got.Query)
		if err != nil || got.Method != "GET" || q.Get("pagination[limit]") != "100" {
			t.Fatalf("bad listing request: %+v", got)
		}
	}
}

func TestReadsEveryPage(t *testing.T) {
	c, calls := fakePaged(t,
		[]map[string]any{{"id": "1", "username": "a", "email": "a@x.org"}},
		[]map[string]any{{"id": "2", "username": "Zed", "email": "Z@X.org"}},
	)
	if taken, err := c.UsernameTaken(context.Background(), "zed"); err != nil || !taken {
		t.Fatalf("a user on page 2 must be found: %v %v", taken, err)
	}
	if taken, _ := c.EmailTaken(context.Background(), "z@x.org"); !taken {
		t.Fatal("an email on page 2 must be found")
	}
	if len(*calls) != 4 {
		t.Fatalf("want 2 requests per lookup, got %d", len(*calls))
	}
	if q, _ := url.ParseQuery((*calls)[1].Query); q.Get("pagination[page]") != "2" {
		t.Fatalf("second request did not ask for page 2: %q", (*calls)[1].Query)
	}
}

func TestEmailTaken(t *testing.T) {
	c, _ := fake(t, []map[string]any{{"id": "1", "username": "a", "email": "A@X.org"}})
	if taken, _ := c.EmailTaken(context.Background(), " a@x.org"); !taken {
		t.Fatal("email match ignores case and spaces")
	}
}

func TestCreateUserBody(t *testing.T) {
	c, calls := fake(t, nil)
	u, err := c.CreateUser(context.Background(), idp.NewUser{Username: "bob", Email: "b@x.org", DisplayName: "Bob"})
	if err != nil || u.ID != "new-id" {
		t.Fatalf("%v %v", u, err)
	}
	got := (*calls)[0]
	if got.Key != "KEY" {
		t.Fatal("API key header missing")
	}
	want := map[string]bool{"username": true, "email": true, "emailVerified": true, "displayName": true}
	for k := range got.Body {
		if !want[k] {
			t.Fatalf("unexpected body field %q", k)
		}
	}
	if got.Body["emailVerified"] != false {
		t.Fatal("emailVerified must be false")
	}
}

func TestSendLoginLink(t *testing.T) {
	c, calls := fake(t, nil)
	if err := c.SendLoginLink(context.Background(), "new-id"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].Path != "/api/users/new-id/one-time-access-email" {
		t.Fatal((*calls)[0].Path)
	}
	if got := (*calls)[0].Raw; got != `{"ttl":"168h"}` {
		t.Fatalf("login-link body: got %s, want {\"ttl\":\"168h\"}", got)
	}
}

// The link's lifetime is the client's configured one, in whole hours.
func TestSendLoginLinkUsesConfiguredLifetime(t *testing.T) {
	for days, want := range map[int]string{1: `{"ttl":"24h"}`, 14: `{"ttl":"336h"}`, 31: `{"ttl":"744h"}`} {
		c, calls := fake(t, nil)
		c.linkTTL = time.Duration(days) * 24 * time.Hour
		if err := c.SendLoginLink(context.Background(), "new-id"); err != nil {
			t.Fatal(err)
		}
		if got := (*calls)[0].Raw; got != want {
			t.Errorf("%d days: got %s, want %s", days, got, want)
		}
	}
}

// groupServer serves Pocket ID's group listing (a case-sensitive substring
// search, one group per page) and each group with its users.
func groupServer(t *testing.T, groups []map[string]any) (*Client, *[]call) {
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery})
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		var hits []map[string]any
		for _, g := range groups {
			if strings.Contains(g["name"].(string), r.URL.Query().Get("search")) {
				hits = append(hits, g)
			}
		}
		if r.URL.Path == "/api/user-groups" {
			page := 1
			fmt.Sscan(r.URL.Query().Get("pagination[page]"), &page)
			var data []map[string]any
			if page >= 1 && page <= len(hits) {
				data = []map[string]any{{"id": hits[page-1]["id"], "name": hits[page-1]["name"]}}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data, "pagination": map[string]any{"totalPages": len(hits), "currentPage": page}})
			return
		}
		for _, g := range groups {
			if r.URL.Path == "/api/user-groups/"+g["id"].(string) {
				json.NewEncoder(w).Encode(g)
				return
			}
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	return mustNew(t, srv.URL, srv.Client(), week), &calls
}

func TestAdminGroupEmails(t *testing.T) {
	c, calls := groupServer(t, []map[string]any{
		{"id": "g1", "name": "superadmins", "users": []map[string]any{{"id": "9", "username": "z", "email": "z@x.org"}}},
		{"id": "g2", "name": "admins", "users": []map[string]any{
			{"id": "1", "username": "a", "email": "a@x.org"},
			{"id": "2", "username": "b"},
			{"id": "3", "username": "c", "email": "c@x.org", "disabled": true},
		}},
	})
	got, err := c.AdminGroupEmails(context.Background(), "admins")
	if err != nil || len(got) != 1 || got[0] != "a@x.org" {
		t.Fatalf("%v %v", got, err)
	}
	for _, cl := range *calls {
		if cl.Method != "GET" {
			t.Fatalf("a read made a %s", cl.Method)
		}
	}
	if last := (*calls)[len(*calls)-1]; last.Path != "/api/user-groups/g2" {
		t.Fatalf("read the wrong group: %s", last.Path)
	}
	if _, err := c.AdminGroupEmails(context.Background(), "nobody"); err == nil {
		t.Fatal("a missing group must be an error")
	}
}

func TestErrorsDoNotEchoBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, "SECRET-IN-BODY")
	}))
	defer srv.Close()
	_, err := mustNew(t, srv.URL, srv.Client(), week).CreateUser(context.Background(), idp.NewUser{Username: "x", Email: "x@x.org"})
	if err == nil || strings.Contains(err.Error(), "SECRET-IN-BODY") || strings.Contains(err.Error(), "KEY") {
		t.Fatalf("error must carry status and path only: %v", err)
	}
}

func TestTransportErrorsDoNotEchoQuery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	c := mustNew(t, base, nil, week)
	check := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("want an error from a closed port")
		}
		for _, bad := range []string{"secret", "example.org", "KEY", "search"} {
			if strings.Contains(err.Error(), bad) {
				t.Fatalf("error leaks %q: %v", bad, err)
			}
		}
	}
	_, err := c.EmailTaken(context.Background(), "secret@example.org")
	check(err)
	check(c.do(context.Background(), http.MethodGet, "/api/users?search=secret%40example.org", nil, nil))
	check(mustNew(t, "://bad", nil, week).do(context.Background(), http.MethodGet, "/api/users?search=secret%40example.org", nil, nil))
}

func TestRedirectIsAnError(t *testing.T) {
	followed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed = true
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := mustNew(t, srv.URL, srv.Client(), week).EmailTaken(context.Background(), "a@x.org"); err == nil {
		t.Fatal("a 302 must be an error")
	}
	if followed {
		t.Fatal("the redirect target was requested")
	}
}

func TestListingIsCapped(t *testing.T) {
	pages := make([][]map[string]any, maxPages+1)
	c, calls := fakePaged(t, pages...)
	if _, err := c.UsernameTaken(context.Background(), "a"); err == nil {
		t.Fatal("a listing past the page cap must be an error")
	}
	if len(*calls) != maxPages {
		t.Fatalf("want %d requests, got %d", maxPages, len(*calls))
	}
}

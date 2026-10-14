package pocketid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/idp"
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
		c, err := New("http://pocket-id:1411", "KEY", nil, ttl)
		if (err == nil) != ok || (c != nil) != ok {
			t.Errorf("%v: client %v, err %v", ttl, c != nil, err)
		}
		if err != nil && strings.Contains(err.Error(), "KEY") {
			t.Errorf("%v: error carries the key: %v", ttl, err)
		}
	}
}

// New refuses a base URL that is not private, as pocketid-go judges it, before
// anything is sent; the error names the host and never the key.
func TestNewRefusesAPublicAddress(t *testing.T) {
	for _, c := range []struct {
		base     string
		networks []netip.Prefix
		ok       bool
	}{
		{"http://10.44.0.1:1413", nil, true},
		{"http://pocket-id:1411", nil, true},
		{"http://[fd00::1]:1413", nil, true},
		{"http://203.0.113.5:1413", []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}, true},
		{"https://id.example.org", nil, false},
		{"http://203.0.113.5:1413", nil, false},
		{"://bad", nil, false},
		{"/api", nil, false},
	} {
		got, err := New(c.base, "KEY", nil, week, c.networks...)
		if (err == nil) != c.ok || (got != nil) != c.ok {
			t.Errorf("%s: client %v, err %v", c.base, got != nil, err)
		}
		if err != nil && strings.Contains(err.Error(), "KEY") {
			t.Errorf("%s: error carries the key: %v", c.base, err)
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
	// pocketid-go's NewUser always sends the names and the admin flag, here
	// empty and false, and leaves out emailVerified, which Pocket ID then
	// stores as false.
	want := map[string]any{"username": "bob", "email": "b@x.org", "displayName": "Bob", "firstName": "", "lastName": "", "isAdmin": false}
	if !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("create body\n got %v\nwant %v", got.Body, want)
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
	if got := (*calls)[0].Raw; got != `{"ttl":"168h0m0s"}` {
		t.Fatalf("login-link body: got %s, want {\"ttl\":\"168h0m0s\"}", got)
	}
}

// The link's lifetime is the client's configured one, in whole hours, written
// as a Go duration, which Pocket ID parses.
func TestSendLoginLinkUsesConfiguredLifetime(t *testing.T) {
	for days, want := range map[int]string{1: `{"ttl":"24h0m0s"}`, 14: `{"ttl":"336h0m0s"}`, 31: `{"ttl":"744h0m0s"}`} {
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
// search) and every user with their groups.
func groupServer(t *testing.T, groups []map[string]any, users []map[string]any) (*Client, *[]call) {
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery})
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/api/user-groups":
			var hits []map[string]any
			for _, g := range groups {
				if strings.Contains(g["name"].(string), r.URL.Query().Get("search")) {
					hits = append(hits, g)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": hits, "pagination": map[string]any{"totalPages": 1}})
		case "/api/users":
			json.NewEncoder(w).Encode(map[string]any{"data": users, "pagination": map[string]any{"totalPages": 1}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return mustNew(t, srv.URL, srv.Client(), week), &calls
}

func TestAdminGroupEmails(t *testing.T) {
	in := func(ids ...string) []map[string]any {
		var out []map[string]any
		for _, id := range ids {
			out = append(out, map[string]any{"id": id})
		}
		return out
	}
	c, calls := groupServer(t,
		[]map[string]any{{"id": "g1", "name": "superadmins"}, {"id": "g2", "name": "admins"}},
		[]map[string]any{
			{"id": "9", "username": "z", "email": "z@x.org", "userGroups": in("g1")},
			{"id": "1", "username": "a", "email": "a@x.org", "userGroups": in("g2", "g1")},
			{"id": "2", "username": "b", "userGroups": in("g2")},
			{"id": "3", "username": "c", "email": "c@x.org", "disabled": true, "userGroups": in("g2")},
			{"id": "4", "username": "d", "email": "d@x.org"},
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
		for _, bad := range []string{"secret", "example.org", "KEY", "search", "pagination"} {
			if strings.Contains(err.Error(), bad) {
				t.Fatalf("error leaks %q: %v", bad, err)
			}
		}
	}
	_, err := c.EmailTaken(context.Background(), "secret@example.org")
	check(err)
	_, err = c.ListGroups(context.Background())
	check(err)
	_, err = c.AdminGroupEmails(context.Background(), "secret")
	check(err)
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

// The group listing, the one request the package makes itself, stops at a
// page cap so a misbehaving server cannot hold a request open.
func TestGroupListingIsCapped(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		json.NewEncoder(w).Encode(map[string]any{"data": groupsNamed(fmt.Sprint("g", n)), "pagination": map[string]any{"totalPages": maxPages + 1}})
	}))
	defer srv.Close()
	if _, err := mustNew(t, srv.URL, srv.Client(), week).ListGroups(context.Background()); err == nil {
		t.Fatal("a listing past the page cap must be an error")
	}
	if n != maxPages {
		t.Fatalf("want %d requests, got %d", maxPages, n)
	}
}

// A context the caller cancels ends the call before anything is sent.
func TestCallsTakeTheCallersContext(t *testing.T) {
	c, calls := fake(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListUsers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if _, err := c.ListGroups(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if err := c.SetDisabled(ctx, "u1", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("a cancelled call sent %+v", *calls)
	}
}

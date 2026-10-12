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

	"github.com/paisans-software/paisans-invite/internal/idp"
)

type call struct {
	Method, Path, Query, Key string
	Body                     map[string]any
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
	return New(srv.URL, "KEY", srv.Client()), &calls
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
	c.AdminEmails(context.Background())
	if len(*calls) != 3 {
		t.Fatalf("want 3 requests, got %d", len(*calls))
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
}

func TestAdminEmails(t *testing.T) {
	c, _ := fake(t, []map[string]any{
		{"id": "1", "username": "a", "email": "a@x.org", "isAdmin": true},
		{"id": "2", "username": "b", "email": "b@x.org", "isAdmin": false},
		{"id": "3", "username": "c", "isAdmin": true},
	})
	got, err := c.AdminEmails(context.Background())
	if err != nil || len(got) != 1 || got[0] != "a@x.org" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestErrorsDoNotEchoBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, "SECRET-IN-BODY")
	}))
	defer srv.Close()
	_, err := New(srv.URL, "KEY", srv.Client()).CreateUser(context.Background(), idp.NewUser{Username: "x", Email: "x@x.org"})
	if err == nil || strings.Contains(err.Error(), "SECRET-IN-BODY") || strings.Contains(err.Error(), "KEY") {
		t.Fatalf("error must carry status and path only: %v", err)
	}
}

func TestTransportErrorsDoNotEchoQuery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	c := New(base, "KEY", nil)
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
	check(New("://bad", "KEY", nil).do(context.Background(), http.MethodGet, "/api/users?search=secret%40example.org", nil, nil))
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
	if _, err := New(srv.URL, "KEY", srv.Client()).EmailTaken(context.Background(), "a@x.org"); err == nil {
		t.Fatal("a 302 must be an error")
	}
	if followed {
		t.Fatal("the redirect target was requested")
	}
}

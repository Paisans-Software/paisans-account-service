package pocketid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-invite/internal/idp"
)

type call struct {
	Method, Path, Query, Key string
	Body                     map[string]any
}

func fake(t *testing.T, users []map[string]any) (*Client, *[]call) {
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Key: r.Header.Get("X-API-Key")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &c.Body)
		}
		calls = append(calls, c)
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/users":
			json.NewEncoder(w).Encode(map[string]any{"data": users, "pagination": map[string]any{"totalPages": 1, "currentPage": 1}})
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

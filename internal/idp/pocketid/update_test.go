package pocketid

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/paisans-software/paisans-account-service/internal/idp"
)

// stored is the user the update server holds, as GET /api/users/:id answers.
func stored() map[string]any {
	return map[string]any{
		"id": "u1", "username": "bob", "email": "bob@x.org", "emailVerified": false,
		"firstName": "Bob", "lastName": "Smith", "displayName": "Bob S", "isAdmin": true,
		"locale": "it", "disabled": false, "customClaims": []any{}, "userGroups": []any{map[string]any{"id": "g"}},
		"ldapId": nil,
	}
}

// updateServer serves one user, answering GET and PUT on /api/users/u1 and 404
// for anything else, and records every call.
func updateServer(t *testing.T) (*Client, *[]call) {
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{Method: r.Method, Path: r.URL.Path}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			c.Raw = string(b)
			json.Unmarshal(b, &c.Body)
		}
		calls = append(calls, c)
		if r.URL.Path != "/api/users/u1" {
			w.WriteHeader(404)
			return
		}
		switch r.Method {
		case "GET":
			json.NewEncoder(w).Encode(stored())
		case "PUT":
			json.NewEncoder(w).Encode(c.Body)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(srv.Close)
	return mustNew(t, srv.URL, srv.Client(), week), &calls
}

// sentBack is what an update sends for the stored user with nothing changed:
// every field of the update body, as read.
func sentBack() map[string]any {
	return map[string]any{
		"username": "bob", "email": "bob@x.org", "emailVerified": false,
		"firstName": "Bob", "lastName": "Smith", "displayName": "Bob S", "isAdmin": true,
		"locale": "it", "disabled": false,
	}
}

func checkUpdate(t *testing.T, calls []call, change map[string]any) {
	t.Helper()
	if len(calls) != 2 || calls[0].Method != "GET" || calls[1].Method != "PUT" || calls[1].Path != "/api/users/u1" {
		t.Fatalf("want GET then PUT on /api/users/u1, got %+v", calls)
	}
	want := sentBack()
	for k, v := range change {
		want[k] = v
	}
	if !reflect.DeepEqual(calls[1].Body, want) {
		t.Fatalf("PUT body\n got %v\nwant %v", calls[1].Body, want)
	}
}

func TestSetDisabledSendsEveryFieldBack(t *testing.T) {
	for _, d := range []bool{true, false} {
		c, calls := updateServer(t)
		if err := c.SetDisabled(context.Background(), "u1", d); err != nil {
			t.Fatal(err)
		}
		checkUpdate(t, *calls, map[string]any{"disabled": d})
	}
}

func TestSetEmailIsVerified(t *testing.T) {
	c, calls := updateServer(t)
	if err := c.SetEmail(context.Background(), "u1", " new@x.org "); err != nil {
		t.Fatal(err)
	}
	checkUpdate(t, *calls, map[string]any{"email": "new@x.org", "emailVerified": true})
}

func TestSetNameNeverTheUsername(t *testing.T) {
	c, calls := updateServer(t)
	if err := c.SetName(context.Background(), "u1", idp.Name{DisplayName: "Robert", FirstName: "Robert", LastName: ""}); err != nil {
		t.Fatal(err)
	}
	checkUpdate(t, *calls, map[string]any{"displayName": "Robert", "firstName": "Robert", "lastName": ""})
}

// A user with no last name or email reads as null; the update sends them back
// as the same absence, never as a made-up value.
func TestUpdateSendsNullsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			u := stored()
			u["lastName"], u["email"], u["locale"] = nil, nil, nil
			json.NewEncoder(w).Encode(u)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["lastName"] != "" || body["email"] != nil || body["locale"] != nil {
			t.Errorf("nulls not sent back: %v", body)
		}
	}))
	defer srv.Close()
	if err := mustNew(t, srv.URL, srv.Client(), week).SetDisabled(context.Background(), "u1", true); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateOfMissingUserWritesNothing(t *testing.T) {
	c, calls := updateServer(t)
	err := c.SetDisabled(context.Background(), "nobody", true)
	if !errors.Is(err, idp.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if len(*calls) != 1 || (*calls)[0].Method != "GET" {
		t.Fatalf("a failed read must not be followed by a write: %+v", *calls)
	}
}

// The update sends the admin flag back as read: an admin's update never
// demotes them, and anyone else's never promotes them.
func TestUpdateKeepsAdmin(t *testing.T) {
	for _, admin := range []bool{true, false} {
		var put map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				u := stored()
				u["isAdmin"] = admin
				json.NewEncoder(w).Encode(u)
				return
			}
			json.NewDecoder(r.Body).Decode(&put)
		}))
		c := mustNew(t, srv.URL, srv.Client(), week)
		for _, set := range []func() error{
			func() error { return c.SetDisabled(context.Background(), "u1", true) },
			func() error { return c.SetEmail(context.Background(), "u1", "n@x.org") },
			func() error { return c.SetName(context.Background(), "u1", idp.Name{DisplayName: "N"}) },
		} {
			put = nil
			if err := set(); err != nil || put["isAdmin"] != admin {
				t.Errorf("admin %v: sent isAdmin %v, %v", admin, put["isAdmin"], err)
			}
		}
		srv.Close()
	}
}

func TestGetUser(t *testing.T) {
	c, _ := updateServer(t)
	u, err := c.GetUser(context.Background(), "u1")
	want := idp.User{ID: "u1", Username: "bob", Email: "bob@x.org", DisplayName: "Bob S", FirstName: "Bob", LastName: "Smith"}
	if err != nil || u != want {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := c.GetUser(context.Background(), "../x"); !errors.Is(err, idp.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListUsersCarriesNamesAndState(t *testing.T) {
	c, _ := fake(t, []map[string]any{{"id": "1", "username": "a", "email": "a@x.org", "displayName": "Ann", "disabled": true}})
	users, err := c.ListUsers(context.Background())
	if err != nil || len(users) != 1 || users[0].DisplayName != "Ann" || !users[0].Disabled {
		t.Fatalf("%+v %v", users, err)
	}
}

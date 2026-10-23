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
	pid "github.com/paisans-software/pocketid-go"
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
	u, err := c.CreateUser(context.Background(), idp.NewUser{Username: "bob", Email: "b@x.org", DisplayName: "Bob", FirstName: "Robert", LastName: "Ross"})
	if err != nil || u.ID != "new-id" {
		t.Fatalf("%v %v", u, err)
	}
	got := (*calls)[0]
	if got.Key != "KEY" {
		t.Fatal("API key header missing")
	}
	// pocketid-go's NewUser always sends the admin flag, here false. The
	// address goes as verified: Pocket ID stores emailVerified as sent, and
	// left out it would be false in every ID token.
	want := map[string]any{"username": "bob", "email": "b@x.org", "emailVerified": true, "displayName": "Bob", "firstName": "Robert", "lastName": "Ross", "isAdmin": false}
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
	members, err := c.GroupMembers(context.Background(), "admins")
	var ids []string
	for _, u := range members {
		ids = append(ids, u.ID)
	}
	if err != nil || strings.Join(ids, ",") != "1,2,3" || !members[2].Disabled {
		t.Fatalf("members: %+v %v", members, err)
	}
	if _, err := c.GroupMembers(context.Background(), "nobody"); err == nil {
		t.Fatal("a missing group must be an error")
	}
	for _, cl := range *calls {
		if cl.Method != "GET" {
			t.Fatalf("a read made a %s", cl.Method)
		}
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
	_, err = c.GroupMembers(context.Background(), "secret")
	check(err)
}

// Every exported method, answered with a redirect of any kind, fails with
// pocketid-go's *APIError carrying the 3xx, and nothing is sent to the
// redirect's target: following it would take the key to an address the
// private check never saw.
func TestRedirectIsAnErrorAndNotFollowed(t *testing.T) {
	var followed []string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = append(followed, r.Method+" "+r.URL.Path+" key="+r.Header.Get("X-API-Key"))
	}))
	defer elsewhere.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		var sent []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sent = append(sent, r.Method+" "+r.URL.Path)
			http.Redirect(w, r, elsewhere.URL+r.URL.Path, status)
		}))
		c := mustNew(t, srv.URL, srv.Client(), week)
		ctx := context.Background()
		for _, m := range []struct {
			name string
			run  func() error
		}{
			{"UsernameTaken", func() error { _, err := c.UsernameTaken(ctx, "bob"); return err }},
			{"EmailTaken", func() error { _, err := c.EmailTaken(ctx, "bob@x.org"); return err }},
			{"FindUser", func() error { _, _, err := c.FindUser(ctx, "bob"); return err }},
			{"AdminGroupEmails", func() error { _, err := c.AdminGroupEmails(ctx, "admins"); return err }},
			{"GroupMembers", func() error { _, err := c.GroupMembers(ctx, "admins"); return err }},
			{"ListUsers", func() error { _, err := c.ListUsers(ctx); return err }},
			{"GetUser", func() error { _, err := c.GetUser(ctx, "u1"); return err }},
			{"ListGroups", func() error { _, err := c.ListGroups(ctx); return err }},
			{"UserGroups", func() error { _, err := c.UserGroups(ctx, "u1"); return err }},
			{"CreateUser", func() error {
				_, err := c.CreateUser(ctx, idp.NewUser{Username: "ann", Email: "ann@x.org", DisplayName: "Ann"})
				return err
			}},
			{"SendLoginLink", func() error { return c.SendLoginLink(ctx, "u1") }},
			{"SetDisabled", func() error { return c.SetDisabled(ctx, "u1", true) }},
			{"SetEmail", func() error { return c.SetEmail(ctx, "u1", "new@x.org") }},
			{"SetName", func() error { return c.SetName(ctx, "u1", idp.Name{DisplayName: "B"}) }},
			{"ChangeGroups", func() error { _, err := c.ChangeGroups(ctx, "u1", []string{"g"}, nil); return err }},
		} {
			sent = nil
			err := m.run()
			var ae *pid.APIError
			if !errors.As(err, &ae) || ae.Status != status {
				t.Errorf("%d %s: want an *APIError with status %d, got %v", status, m.name, status, err)
			}
			if errors.Is(err, idp.ErrNotFound) {
				t.Errorf("%d %s: a redirect reads as not found", status, m.name)
			}
			if len(sent) != 1 {
				t.Errorf("%d %s: want the one request that was redirected, sent %v", status, m.name, sent)
			}
		}
		srv.Close()
	}
	if len(followed) != 0 {
		t.Fatalf("the redirect target was requested: %v", followed)
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

// ListClients lists every client and reads each restricted one with a launch
// URL by ID for its allowed groups, which a list entry may leave out; a
// client gone between the two reads is skipped, and nothing is written.
func TestListClientsReadsGroupsByID(t *testing.T) {
	launch := func(s string) any { return s }
	list := []map[string]any{
		{"id": "open", "name": "Open", "launchURL": launch("https://open.example.org"), "hasLogo": true},
		{"id": "night", "name": "Night", "launchURL": launch("https://night.example.org"), "hasDarkLogo": true},
		{"id": "wiki", "name": "Wiki", "launchURL": launch("https://wiki.example.org"), "isGroupRestricted": true},
		{"id": "gone", "name": "Gone", "launchURL": launch("https://gone.example.org"), "isGroupRestricted": true},
		{"id": "hidden", "name": "Hidden", "launchURL": nil, "isGroupRestricted": true},
		{"id": "blank", "name": "Blank", "launchURL": launch(""), "isGroupRestricted": true},
	}
	byID := map[string]map[string]any{
		"wiki": {"id": "wiki", "name": "Wiki", "launchURL": "https://wiki.example.org", "isGroupRestricted": true,
			"allowedUserGroups": []any{map[string]any{"id": "g-members", "name": "members"}, map[string]any{"id": "g-wiki", "name": "wiki"}}},
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, call{Method: r.Method, Path: r.URL.Path, Key: r.Header.Get("X-API-Key")})
		id, one := strings.CutPrefix(r.URL.Path, "/api/oidc/clients/")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/oidc/clients":
			page := 1
			fmt.Sscan(r.URL.Query().Get("pagination[page]"), &page)
			data := list[:4]
			if page == 2 {
				data = list[4:]
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data, "pagination": map[string]any{"totalPages": 2, "currentPage": page}})
		case r.Method == "GET" && one && byID[id] != nil:
			json.NewEncoder(w).Encode(byID[id])
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	c := mustNew(t, srv.URL, srv.Client(), week)
	got, err := c.ListClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []idp.Client{
		{ID: "open", Name: "Open", LaunchURL: "https://open.example.org", HasLogo: true},
		{ID: "night", Name: "Night", LaunchURL: "https://night.example.org", HasDarkLogo: true},
		{ID: "wiki", Name: "Wiki", LaunchURL: "https://wiki.example.org", Restricted: true, AllowedGroupIDs: []string{"g-members", "g-wiki"}},
		{ID: "hidden", Name: "Hidden", Restricted: true},
		{ID: "blank", Name: "Blank", Restricted: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	var reads []string
	for _, k := range calls {
		if k.Method != "GET" || k.Key != "KEY" {
			t.Errorf("request %s %s, key %q", k.Method, k.Path, k.Key)
		}
		if k.Path != "/api/oidc/clients" {
			reads = append(reads, k.Path)
		}
	}
	if strings.Join(reads, ",") != "/api/oidc/clients/wiki,/api/oidc/clients/gone" {
		t.Fatalf("read by ID %v", reads)
	}
}

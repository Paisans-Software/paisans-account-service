//go:build contract

package pocketid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/idp"
)

func TestContract(t *testing.T) {
	base, key := os.Getenv("CONTRACT_IDP_URL"), os.Getenv("CONTRACT_API_KEY")
	c, err := New(base, key, &http.Client{Timeout: 10 * time.Second}, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name := fmt.Sprintf("contract%d", time.Now().UnixNano()%1e9)

	if taken, err := c.UsernameTaken(ctx, name); err != nil || taken {
		t.Fatalf("fresh name: %v %v", taken, err)
	}
	u, err := c.CreateUser(ctx, idp.NewUser{Username: name, Email: name + "@example.org", DisplayName: "Contract"})
	if err != nil {
		t.Fatal(err)
	}
	if taken, err := c.UsernameTaken(ctx, name); err != nil || !taken {
		t.Fatalf("created user not found by search: %v %v", taken, err)
	}
	if taken, err := c.EmailTaken(ctx, name+"@EXAMPLE.org"); err != nil || !taken {
		t.Fatalf("email search must ignore case: %v %v", taken, err)
	}

	// The user landed in the signup default group, with no groups sent.
	var groups []struct {
		ID string `json:"id"`
	}
	api(t, base, key, http.MethodGet, "/api/users/"+u.ID+"/groups", nil, &groups)
	if len(groups) != 1 || groups[0].ID != os.Getenv("CONTRACT_GROUP_ID") {
		t.Fatalf("want exactly the default group, got %+v", groups)
	}

	// The admin notice reads the admin group's members, by exact group name:
	// "superadmins" also matches Pocket ID's substring search and must not count.
	other, err := c.CreateUser(ctx, idp.NewUser{Username: name + "x", Email: name + "x@example.org", DisplayName: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	for group, member := range map[string]string{"admins": u.ID, "superadmins": other.ID} {
		var g struct {
			ID string `json:"id"`
		}
		api(t, base, key, http.MethodPost, "/api/user-groups", map[string]any{"name": group, "friendlyName": group}, &g)
		api(t, base, key, http.MethodPut, "/api/user-groups/"+g.ID+"/users", map[string]any{"userIds": []string{member}}, nil)
	}
	admins, err := c.AdminGroupEmails(ctx, "admins")
	if err != nil || len(admins) != 1 || admins[0] != name+"@example.org" {
		t.Fatalf("admin group emails: %v %v", admins, err)
	}

	if err := c.SendLoginLink(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	// Mailpit received a message addressed to the new user, and Pocket ID
	// read the 168h lifetime: its text template says "It expires in
	// {{.Data.ExpirationString}}.", and utils.DurationToString(168h) is "7 days".
	mailpit := os.Getenv("CONTRACT_MAILPIT_URL")
	var msgID string
	for i := 0; i < 20 && msgID == ""; i++ {
		var res struct {
			Messages []struct {
				ID string `json:"ID"`
			} `json:"messages"`
		}
		get(t, mailpit+"/api/v1/search?query="+url.QueryEscape("to:"+name+"@example.org"), &res)
		if len(res.Messages) > 0 {
			msgID = res.Messages[0].ID
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if msgID == "" {
		t.Fatal("no login email reached Mailpit")
	}
	var msg struct {
		Text string `json:"Text"`
	}
	get(t, mailpit+"/api/v1/message/"+url.PathEscape(msgID), &msg)
	if !strings.Contains(msg.Text, "It expires in 7 days.") {
		t.Fatalf("login email does not carry the 168h lifetime:\n%s", msg.Text)
	}
}

// pocketid-go's UpdateUser sends every field back as read: disabling and
// re-enabling a user, and setting its address or name, change nothing else,
// and Pocket ID accepts the body. The admin flag, the username and the groups
// are unchanged after every update, for an administrator and for anyone else.
func TestContractUpdate(t *testing.T) {
	base, key := os.Getenv("CONTRACT_IDP_URL"), os.Getenv("CONTRACT_API_KEY")
	c, err := New(base, key, &http.Client{Timeout: 10 * time.Second}, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name := fmt.Sprintf("update%d", time.Now().UnixNano()%1e9)
	u, err := c.CreateUser(ctx, idp.NewUser{Username: name, Email: name + "@example.org", DisplayName: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		t.Helper()
		var m map[string]any
		api(t, base, key, http.MethodGet, "/api/users/"+u.ID, nil, &m)
		return m
	}
	// A second group besides the signup default, so that a write which
	// dropped or reset the groups would show.
	var extra struct {
		ID string `json:"id"`
	}
	api(t, base, key, http.MethodPost, "/api/user-groups", map[string]any{"name": name + "-extra", "friendlyName": "Extra"}, &extra)
	api(t, base, key, http.MethodPut, "/api/users/"+u.ID+"/user-groups", map[string]any{"userGroupIds": []string{os.Getenv("CONTRACT_GROUP_ID"), extra.ID}}, nil)
	before := read()
	if gs, _ := before["userGroups"].([]any); len(gs) != 2 {
		t.Fatalf("want two groups before the updates, got %v", before["userGroups"])
	}
	// kept checks what no update may change, against the user as first read.
	first := before
	kept := func(after map[string]any) {
		t.Helper()
		for _, k := range []string{"isAdmin", "username", "userGroups"} {
			if fmt.Sprint(after[k]) != fmt.Sprint(first[k]) {
				t.Errorf("an update changed %s: %v -> %v", k, first[k], after[k])
			}
		}
	}
	same := func(after map[string]any, changed ...string) {
		t.Helper()
		kept(after)
		skip := map[string]bool{"userGroups": true, "customClaims": true}
		for _, k := range changed {
			skip[k] = true
		}
		for k, v := range before {
			if !skip[k] && fmt.Sprint(after[k]) != fmt.Sprint(v) {
				t.Errorf("%s changed: %v -> %v", k, v, after[k])
			}
		}
		if fmt.Sprint(after["userGroups"]) != fmt.Sprint(before["userGroups"]) {
			t.Errorf("groups changed: %v -> %v", before["userGroups"], after["userGroups"])
		}
	}

	if err := c.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after["disabled"] != true {
		t.Fatalf("not disabled: %v", after)
	}
	same(after, "disabled")
	if got, err := c.GetUser(ctx, u.ID); err != nil || !got.Disabled {
		t.Fatalf("GetUser after disable: %+v %v", got, err)
	}
	if err := c.SetDisabled(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	after = read()
	if after["disabled"] != false {
		t.Fatalf("not re-enabled: %v", after)
	}
	same(after)

	if err := c.SetEmail(ctx, u.ID, name+".new@example.org"); err != nil {
		t.Fatal(err)
	}
	after = read()
	if after["email"] != name+".new@example.org" || after["emailVerified"] != true {
		t.Fatalf("email not set and verified: %v", after)
	}
	same(after, "email", "emailVerified")
	before = after

	if err := c.SetName(ctx, u.ID, idp.Name{DisplayName: "After", FirstName: "Af", LastName: "Ter"}); err != nil {
		t.Fatal(err)
	}
	after = read()
	if after["displayName"] != "After" || after["firstName"] != "Af" || after["lastName"] != "Ter" || after["username"] != name {
		t.Fatalf("name not set: %v", after)
	}
	same(after, "displayName", "firstName", "lastName")

	if after["isAdmin"] != false {
		t.Fatalf("a non-admin's update promoted them: %v", after)
	}

	// An administrator's update keeps them one: isAdmin goes back as read.
	promoted := read()
	promoted["isAdmin"] = true
	api(t, base, key, http.MethodPut, "/api/users/"+u.ID, promoted, nil)
	before = read()
	if before["isAdmin"] != true {
		t.Fatalf("not promoted for the test: %v", before)
	}
	if err := c.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	after = read()
	if after["isAdmin"] != true || after["disabled"] != true {
		t.Fatalf("an admin's update changed the admin flag: %v", after)
	}
	first = before
	same(after, "disabled")
	for _, set := range []func() error{
		func() error { return c.SetEmail(ctx, u.ID, name+".admin@example.org") },
		func() error {
			return c.SetName(ctx, u.ID, idp.Name{DisplayName: "Admin", FirstName: "Ad", LastName: "Min"})
		},
		func() error { return c.SetDisabled(ctx, u.ID, false) },
	} {
		if err := set(); err != nil {
			t.Fatal(err)
		}
		if after = read(); after["isAdmin"] != true {
			t.Fatalf("an admin's update changed the admin flag: %v", after)
		}
		kept(after)
	}

	if _, err := c.GetUser(ctx, "00000000-0000-0000-0000-00000000dead"); !errors.Is(err, idp.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
}

// The group write changes exactly the groups it names: a group added behind
// its back is kept, operators membership is refused both ways, and no other
// field of the user, nor another user's groups, changes.
func TestContractGroups(t *testing.T) {
	base, key := os.Getenv("CONTRACT_IDP_URL"), os.Getenv("CONTRACT_API_KEY")
	c, err := New(base, key, &http.Client{Timeout: 10 * time.Second}, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name := fmt.Sprintf("groups%d", time.Now().UnixNano()%1e9)
	u, err := c.CreateUser(ctx, idp.NewUser{Username: name, Email: name + "@example.org", DisplayName: "Groups"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.CreateUser(ctx, idp.NewUser{Username: name + "o", Email: name + "o@example.org", DisplayName: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	group := func(n string) string {
		t.Helper()
		var found struct {
			Data []struct{ ID, Name string } `json:"data"`
		}
		api(t, base, key, http.MethodGet, "/api/user-groups?search="+url.QueryEscape(n), nil, &found)
		for _, g := range found.Data {
			if g.Name == n {
				return g.ID
			}
		}
		var g struct {
			ID string `json:"id"`
		}
		api(t, base, key, http.MethodPost, "/api/user-groups", map[string]any{"name": n, "friendlyName": n}, &g)
		return g.ID
	}
	g1, g2, ops := group(name+"-one"), group(name+"-two"), group(idp.OperatorsGroup)
	members := os.Getenv("CONTRACT_GROUP_ID")
	ids := func(id string) string {
		t.Helper()
		gs, err := c.UserGroups(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, g := range gs {
			out = append(out, g.ID)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	set := func(xs ...string) string { sort.Strings(xs); return strings.Join(xs, ",") }
	read := func(id string) map[string]any {
		var m map[string]any
		api(t, base, key, http.MethodGet, "/api/users/"+id, nil, &m)
		delete(m, "userGroups")
		return m
	}
	// ListGroups is every group, one nobody is in included.
	all, err := c.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, g := range all {
		listed[g.ID] = true
	}
	if !listed[g1] || !listed[g2] || !listed[ops] || !listed[members] {
		t.Fatalf("ListGroups leaves out a group: %+v", all)
	}

	before, otherBefore := read(u.ID), ids(other.ID)
	if ids(u.ID) != members {
		t.Fatalf("want the default group only, got %s", ids(u.ID))
	}

	ch, err := c.ChangeGroups(ctx, u.ID, []string{g1}, nil)
	if err != nil || len(ch.Added) != 1 || ch.Added[0].ID != g1 || len(ch.Removed) != 0 {
		t.Fatalf("add: %+v %v", ch, err)
	}
	if got := ids(u.ID); got != set(members, g1) {
		t.Fatalf("after add: %s", got)
	}

	// Another admin adds g2 directly; removing g1 keeps it.
	api(t, base, key, http.MethodPut, "/api/users/"+u.ID+"/user-groups", map[string]any{"userGroupIds": []string{members, g1, g2}}, nil)
	ch, err = c.ChangeGroups(ctx, u.ID, nil, []string{g1})
	if err != nil || len(ch.Removed) != 1 || ch.Removed[0].ID != g1 || len(ch.Added) != 0 {
		t.Fatalf("remove: %+v %v", ch, err)
	}
	if got := ids(u.ID); got != set(members, g2) {
		t.Fatalf("after remove: %s", got)
	}

	if _, err := c.ChangeGroups(ctx, u.ID, []string{ops}, nil); !errors.Is(err, idp.ErrOperatorsGroup) {
		t.Fatalf("adding operators: %v", err)
	}
	api(t, base, key, http.MethodPut, "/api/users/"+u.ID+"/user-groups", map[string]any{"userGroupIds": []string{members, g2, ops}}, nil)
	if _, err := c.ChangeGroups(ctx, u.ID, nil, []string{ops}); !errors.Is(err, idp.ErrOperatorsGroup) {
		t.Fatalf("removing operators: %v", err)
	}
	// An operator's other groups change, and operators stays.
	if _, err := c.ChangeGroups(ctx, u.ID, []string{g1}, []string{g2}); err != nil {
		t.Fatal(err)
	}
	if got := ids(u.ID); got != set(members, g1, ops) {
		t.Fatalf("operator's groups: %s", got)
	}
	if _, err := c.ChangeGroups(ctx, u.ID, []string{"00000000-0000-0000-0000-00000000dead"}, nil); !errors.Is(err, idp.ErrNoSuchGroup) {
		t.Fatalf("unknown group: %v", err)
	}

	after := read(u.ID)
	for k, v := range before {
		if fmt.Sprint(after[k]) != fmt.Sprint(v) {
			t.Errorf("%s changed: %v -> %v", k, v, after[k])
		}
	}
	if len(after) != len(before) {
		t.Errorf("fields changed: %v -> %v", before, after)
	}
	if got := ids(other.ID); got != otherBefore {
		t.Errorf("another user's groups changed: %s -> %s", otherBefore, got)
	}
}

// raw is the client for the test's own calls, outside the adapter whose
// surface is pinned; its timeout keeps a hung server from hanging the job.
var raw = &http.Client{Timeout: 10 * time.Second}

// api is the test's own call to Pocket ID's API.
func api(t *testing.T, base, key, method, path string, body, out any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	do(t, req, out)
}

// get fetches a JSON document, as from Mailpit's API.
func get(t *testing.T, u string, out any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	do(t, req, out)
}

func do(t *testing.T, req *http.Request, out any) {
	t.Helper()
	resp, err := raw.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
		}
	}
}

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

// The update write sends every field back as read: disabling and re-enabling
// a user, and setting its address or name, change nothing else, and Pocket ID
// accepts the body.
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
	before := read()
	same := func(after map[string]any, changed ...string) {
		t.Helper()
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

	if _, err := c.GetUser(ctx, "00000000-0000-0000-0000-00000000dead"); !errors.Is(err, idp.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
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

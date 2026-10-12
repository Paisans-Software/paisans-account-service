//go:build contract

package pocketid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/idp"
)

func TestContract(t *testing.T) {
	base, key := os.Getenv("CONTRACT_IDP_URL"), os.Getenv("CONTRACT_API_KEY")
	c := New(base, key, &http.Client{Timeout: 10 * time.Second})
	ctx := context.Background()
	name := fmt.Sprintf("contract%d", time.Now().UnixNano()%1e9)

	if taken, err := c.UsernameTaken(ctx, name); err != nil || taken {
		t.Fatalf("fresh name: %v %v", taken, err)
	}
	u, err := c.CreateUser(ctx, idp.NewUser{Username: name, Email: name + "@example.org", DisplayName: "Contract"})
	if err != nil {
		t.Fatal(err)
	}
	if taken, _ := c.UsernameTaken(ctx, name); !taken {
		t.Fatal("created user not found by search")
	}
	if taken, _ := c.EmailTaken(ctx, name+"@EXAMPLE.org"); !taken {
		t.Fatal("email search must ignore case")
	}

	// The user landed in the signup default group, with no groups sent.
	req, _ := http.NewRequest(http.MethodGet, base+"/api/users/"+u.ID+"/groups", nil)
	req.Header.Set("X-API-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var groups []struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&groups)
	resp.Body.Close()
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
	// Mailpit received a message addressed to the new user.
	var found bool
	for i := 0; i < 20 && !found; i++ {
		resp, err := http.Get(os.Getenv("CONTRACT_MAILPIT_URL") + "/api/v1/search?query=to:" + name + "@example.org")
		if err == nil {
			var res struct {
				Total int `json:"total"`
			}
			json.NewDecoder(resp.Body).Decode(&res)
			resp.Body.Close()
			found = res.Total > 0
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !found {
		t.Fatal("no login email reached Mailpit")
	}
}

// api is the test's own setup call, outside the adapter whose surface is pinned.
func api(t *testing.T, base, key, method, path string, body, out any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, base+path, bytes.NewReader(b))
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
	}
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
}

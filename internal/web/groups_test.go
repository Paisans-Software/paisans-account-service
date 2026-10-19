package web

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-account-service/internal/idp/idptest"
)

// withGroups gives the harness the groups a deployment has: everyone in
// members, root in admins, opal (an operator) in operators and admins, and
// an empty wiki group.
func withGroups(h harness) harness {
	h.fake.Groups["members"] = []string{"root", "alice", "opal"}
	h.fake.Groups["admins"] = []string{"root", "opal"}
	h.fake.Groups["operators"] = []string{"opal"}
	h.fake.Groups["wiki"] = nil
	h.fake.FriendlyNames = map[string]string{"admins": "Admins", "members": "Members", "operators": "Operators", "wiki": "Wiki"}
	return h
}

var gid = idptest.GroupID

// groupsForm is the form the groups page sends: checked groups as group,
// those checked when the page loaded as was.
func groupsForm(checked, was []string) url.Values {
	f := url.Values{"csrf": {"tok"}}
	for _, c := range checked {
		f.Add("group", gid(c))
	}
	for _, w := range was {
		f.Add("was", gid(w))
	}
	return f
}

func TestGroupsPage(t *testing.T) {
	h := withGroups(newHarness(t, true))
	rec := h.as(admin, "GET", "/admin/members/s-alice/groups", nil)
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, body)
	}
	for _, want := range []string{
		`name="group" value="g-members" checked`, `name="was" value="g-members"`,
		`name="group" value="g-admins">`, `name="group" value="g-wiki">`, "Wiki", "wiki",
		`name="csrf" value="tok"`, "next sign-in",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// operators is shown, never offered: no field carries its ID.
	if !strings.Contains(body, "Operators") || strings.Contains(body, `value="g-operators"`) {
		t.Errorf("operators offered:\n%s", body)
	}
	if strings.Contains(body, "next paisans deployment apply") {
		t.Error("operator note on a member's page")
	}
	op := h.as(admin, "GET", "/admin/members/s-opal/groups", nil).Body.String()
	if !strings.Contains(op, "next paisans deployment apply") || !regexp.MustCompile(`disabled checked`).MatchString(op) || strings.Contains(op, `value="g-operators"`) ||
		!strings.Contains(op, "operators · set in the Paisan Stack deployment configuration") {
		t.Fatalf("operator page:\n%s", op)
	}
	if rec := h.as(member, "GET", "/admin/members/s-alice/groups", nil); rec.Code != 403 {
		t.Fatalf("member: %d", rec.Code)
	}
	if rec := h.as(admin, "GET", "/admin/members/nobody/groups", nil); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	if body := h.as(admin, "GET", "/admin/members?q=alice", nil).Body.String(); !strings.Contains(body, `href="/admin/members/s-alice/groups"`) {
		t.Fatalf("no groups link on the members page:\n%s", body)
	}
}

func TestGroupsSave(t *testing.T) {
	h := withGroups(newHarness(t, true))
	// Add admins and wiki, take out members.
	rec := h.as(admin, "POST", "/admin/members/s-alice/groups", groupsForm([]string{"admins", "wiki"}, []string{"members"}))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Added: Admins, Wiki.") || !strings.Contains(body, "Removed: Members.") || !strings.Contains(body, sessionGroupsNotice) {
		t.Fatalf("%d %s", rec.Code, body)
	}
	if !h.fake.In("alice", "admins") || !h.fake.In("alice", "wiki") || h.fake.In("alice", "members") {
		t.Fatal("groups not changed")
	}
	// An operator's admins comes out; operators stays.
	rec = h.as(admin, "POST", "/admin/members/s-opal/groups", groupsForm([]string{"members"}, []string{"members", "admins"}))
	if rec.Code != 200 || h.fake.In("opal", "admins") || !h.fake.In("opal", "operators") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// An admin removes their own admins.
	rec = h.as(admin, "POST", "/admin/members/s-root/groups", groupsForm([]string{"members"}, []string{"members", "admins"}))
	if rec.Code != 200 || h.fake.In("root", "admins") {
		t.Fatalf("self: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.as(admin, "POST", "/admin/members/s-alice/groups", groupsForm([]string{"wiki", "admins"}, []string{"wiki", "admins"})); rec.Code != 422 || !strings.Contains(rec.Body.String(), "No group was changed.") {
		t.Fatalf("no change: %d %s", rec.Code, rec.Body.String())
	}
}

// A crafted form cannot touch operators, add an unknown group, or skip CSRF.
func TestGroupsTampering(t *testing.T) {
	h := withGroups(newHarness(t, true))
	for name, f := range map[string]url.Values{
		"add operators":    groupsForm([]string{"members", "operators"}, []string{"members"}),
		"remove operators": groupsForm(nil, []string{"operators"}),
		"unknown":          groupsForm([]string{"members", "nope"}, []string{"members"}),
	} {
		for _, user := range []string{"s-alice", "s-opal"} {
			rec := h.as(admin, "POST", "/admin/members/"+user+"/groups", f)
			if rec.Code != 422 || !strings.Contains(rec.Body.String(), `class="alert error"`) {
				t.Errorf("%s %s: %d %s", name, user, rec.Code, rec.Body.String())
			}
		}
	}
	if h.fake.GroupWrites != 0 || h.fake.In("alice", "operators") || !h.fake.In("opal", "operators") {
		t.Fatal("a refused form wrote")
	}
	bad := groupsForm([]string{"admins"}, nil)
	bad.Set("csrf", "wrong")
	if rec := h.as(admin, "POST", "/admin/members/s-alice/groups", bad); rec.Code != 403 {
		t.Fatalf("csrf: %d", rec.Code)
	}
	if rec := h.as(member, "POST", "/admin/members/s-alice/groups", groupsForm([]string{"admins"}, nil)); rec.Code != 403 {
		t.Fatalf("member: %d", rec.Code)
	}
	if h.fake.GroupWrites != 0 || h.fake.In("alice", "admins") {
		t.Fatal("a forbidden post wrote")
	}
}

package flow_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/idp/idptest"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

// groupsSetup is people with the groups a deployment has: everyone in
// members, ann and root in admins, opal in operators and admins, and an empty
// wiki group.
func groupsSetup(t *testing.T) (*flow.Service, *idptest.Fake, *store.Store) {
	s, f, st := setupFull(t)
	people(f)
	f.Groups["members"] = []string{"root", "bob", "opal", "ann"}
	f.Groups["operators"] = []string{"opal"}
	f.Groups["admins"] = append(f.Groups["admins"], "opal")
	f.Groups["wiki"] = nil
	f.FriendlyNames = map[string]string{"wiki": "Wiki members"}
	return s, f, st
}

var g = idptest.GroupID

func groupNames(gs []idp.Group) string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Name)
	}
	return strings.Join(out, ",")
}

func audits(t *testing.T, st *store.Store, action string) []string {
	t.Helper()
	got, err := st.AuditSubjects(ctx, action)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMemberGroupsPage(t *testing.T) {
	s, _, _ := groupsSetup(t)
	p, err := s.MemberGroups(ctx, root, "o")
	if err != nil {
		t.Fatal(err)
	}
	if p.Member.Username != "opal" || !p.Operator || len(p.Groups) != 4 {
		t.Fatalf("%+v", p)
	}
	want := map[string][2]bool{"admins": {true, false}, "members": {true, false}, "operators": {true, true}, "wiki": {false, false}}
	for _, r := range p.Groups {
		if w := want[r.Name]; r.In != w[0] || r.Locked != w[1] {
			t.Errorf("%s: in %v locked %v", r.Name, r.In, r.Locked)
		}
		if r.Name == "wiki" && r.FriendlyName != "Wiki members" {
			t.Errorf("friendly name %q", r.FriendlyName)
		}
	}
	if p, err := s.MemberGroups(ctx, root, "b"); err != nil || p.Operator {
		t.Fatalf("bob: %+v %v", p, err)
	}
	if _, err := s.MemberGroups(ctx, alice, "b"); !errors.Is(err, flow.ErrForbidden) {
		t.Fatalf("member: %v", err)
	}
	if _, err := s.MemberGroups(ctx, root, "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestChangeGroups(t *testing.T) {
	s, f, st := groupsSetup(t)
	for _, c := range []struct {
		name        string
		admin       flow.Member
		user, who   string
		add, remove []string
		added, gone string
		after       map[string]bool
	}{
		{"add", root, "b", "bob", []string{g("admins")}, nil, "admins", "", map[string]bool{"admins": true, "members": true}},
		{"remove", root, "b", "bob", nil, []string{g("admins")}, "", "admins", map[string]bool{"admins": false, "members": true}},
		{"both", root, "b", "bob", []string{g("wiki"), g("admins")}, []string{g("members")}, "admins,wiki", "members", map[string]bool{"wiki": true, "admins": true, "members": false}},
		{"another admin", root, "a", "ann", nil, []string{g("admins")}, "", "admins", map[string]bool{"admins": false}},
		{"self", rootR, "r", "root", nil, []string{g("admins")}, "", "admins", map[string]bool{"admins": false}},
		{"an operator's admins", ann, "o", "opal", nil, []string{g("admins")}, "", "admins", map[string]bool{"admins": false, "operators": true}},
	} {
		ch, err := s.ChangeGroups(ctx, c.admin, c.user, c.add, c.remove)
		if err != nil || groupNames(ch.Added) != c.added || groupNames(ch.Removed) != c.gone {
			t.Fatalf("%s: %+v %v", c.name, ch, err)
		}
		for grp, in := range c.after {
			if f.In(c.who, grp) != in {
				t.Errorf("%s: %s in %s is %v", c.name, c.who, grp, !in)
			}
		}
	}
	got := audits(t, st, "groups.change")
	want := []string{
		"user=b added=admins removed=",
		"user=b added= removed=admins",
		"user=b added=admins,wiki removed=members",
		"user=a added= removed=admins",
		"user=r added= removed=admins",
		"user=o added= removed=admins",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("audit:\n%s", strings.Join(got, "\n"))
	}
}

// operators cannot be added or removed, whoever asks and however the form
// was made, and nothing is written.
func TestChangeGroupsRefusals(t *testing.T) {
	s, f, st := groupsSetup(t)
	for _, c := range []struct {
		name        string
		admin       flow.Member
		user        string
		add, remove []string
		err         error
	}{
		{"add operators", root, "b", []string{g("operators")}, nil, flow.ErrOperatorsGroup},
		{"remove operators", root, "o", nil, []string{g("operators")}, flow.ErrOperatorsGroup},
		{"operators with others", root, "o", []string{g("wiki")}, []string{g("operators")}, flow.ErrOperatorsGroup},
		{"unknown group", root, "b", []string{"g-nope"}, nil, flow.ErrNoSuchGroup},
		{"nothing", root, "b", nil, nil, flow.ErrNoGroupChange},
		{"blank", root, "b", []string{""}, []string{" "}, flow.ErrNoGroupChange},
		{"a member", alice, "b", []string{g("admins")}, nil, flow.ErrForbidden},
		{"missing user", root, "nobody", []string{g("admins")}, nil, store.ErrNotFound},
	} {
		if _, err := s.ChangeGroups(ctx, c.admin, c.user, c.add, c.remove); !errors.Is(err, c.err) {
			t.Errorf("%s: %v, want %v", c.name, err, c.err)
		}
	}
	if f.GroupWrites != 0 || !f.In("opal", "operators") || f.In("bob", "operators") || f.In("bob", "admins") {
		t.Fatal("a refused change wrote")
	}
	if got := audits(t, st, "groups.change"); len(got) != 0 {
		t.Fatalf("audited: %v", got)
	}
}

// A group someone else changed between the admin's page and their save is
// kept as the fresh read found it; the admin's own change is applied.
func TestChangeGroupsKeepsConcurrentChange(t *testing.T) {
	s, f, st := groupsSetup(t)
	f.OnChangeGroups = func() { f.SetIn("bob", "wiki", true) }
	ch, err := s.ChangeGroups(ctx, root, "b", []string{g("admins")}, nil)
	if err != nil || groupNames(ch.Added) != "admins" {
		t.Fatalf("%+v %v", ch, err)
	}
	if !f.In("bob", "wiki") || !f.In("bob", "admins") || !f.In("bob", "members") {
		t.Fatal("the other admin's change was undone")
	}
	// Removing a group someone else already removed changes nothing else.
	f.OnChangeGroups = func() { f.SetIn("bob", "admins", false) }
	ch, err = s.ChangeGroups(ctx, root, "b", nil, []string{g("admins")})
	if err != nil || len(ch.Added)+len(ch.Removed) != 0 || !f.In("bob", "wiki") {
		t.Fatalf("%+v %v", ch, err)
	}
	if got := audits(t, st, "groups.change"); len(got) != 2 || got[1] != "user=b added= removed=" {
		t.Fatalf("audit records what actually changed: %v", got)
	}
}

// A write that could not be read back is audited with what was sent.
func TestChangeGroupsUnconfirmed(t *testing.T) {
	s, f, st := groupsSetup(t)
	f.FailGroupReadBack = errors.New("timeout")
	if _, err := s.ChangeGroups(ctx, root, "b", []string{g("wiki")}, nil); !errors.Is(err, idp.ErrUnconfirmed) {
		t.Fatalf("%v", err)
	}
	if got := audits(t, st, "groups.change_unconfirmed"); len(got) != 1 || got[0] != "user=b added=wiki removed=" {
		t.Fatalf("%v", got)
	}
}

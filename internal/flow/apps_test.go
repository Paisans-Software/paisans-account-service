package flow_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/idp/idptest"
)

// appsSetup is bob, in members, at a provider with no clients yet.
func appsSetup(t *testing.T) (*flow.Service, *idptest.Fake) {
	s, f, _ := setupFull(t)
	people(f)
	f.Groups["members"] = []string{"bob"}
	f.Groups["operators"] = []string{"opal"}
	return s, f
}

var bobApps = flow.Member{Sub: "b", Username: "bob"}

func appIDs(as []flow.App) string {
	var out []string
	for _, a := range as {
		out = append(out, a.ID)
	}
	return strings.Join(out, ",")
}

func apps(t *testing.T, s *flow.Service, m flow.Member) []flow.App {
	t.Helper()
	got, err := s.Apps(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// An app is listed as Pocket ID's dashboard lists it: it has a launch URL, and
// it is not group-restricted or allows one of the user's groups.
func TestAppsFollowPocketIDsRule(t *testing.T) {
	s, f := appsSetup(t)
	f.Clients = []idp.Client{
		{ID: "open", Name: "Open", LaunchURL: "https://open.example.org"},
		{ID: "mine", Name: "Mine", LaunchURL: "https://mine.example.org", Restricted: true, AllowedGroupIDs: []string{idptest.GroupID("members")}},
		{ID: "ops", Name: "Ops", LaunchURL: "https://ops.example.org", Restricted: true, AllowedGroupIDs: []string{idptest.GroupID("operators")}},
		{ID: "nobody", Name: "Nobody", LaunchURL: "https://nobody.example.org", Restricted: true},
		{ID: "nolaunch", Name: "No launch"},
		{ID: "empty", Name: "Empty", LaunchURL: ""},
		{ID: "mine-nolaunch", Name: "Mine, no launch", Restricted: true, AllowedGroupIDs: []string{idptest.GroupID("members")}},
	}
	if got := appIDs(apps(t, s, bobApps)); got != "mine,open" {
		t.Fatalf("bob sees %q", got)
	}
	opal := flow.Member{Sub: "o", Username: "opal"}
	if got := appIDs(apps(t, s, opal)); got != "open,ops" {
		t.Fatalf("opal sees %q", got)
	}
}

// An app whose launch URL is not an absolute https URL with a host is left
// off, and a warning names it.
func TestAppsDropBadLaunchURLs(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	s, f := appsSetup(t)
	bad := map[string]string{
		"plain":    "http://plain.example.org",
		"script":   "javascript:alert(1)",
		"relative": "/relative",
		"nohost":   "https://",
	}
	f.Clients = []idp.Client{{ID: "good", Name: "Good", LaunchURL: "https://good.example.org/"}}
	for id, u := range bad {
		f.Clients = append(f.Clients, idp.Client{ID: id, Name: "Name " + id, LaunchURL: u})
	}
	if got := appIDs(apps(t, s, bobApps)); got != "good" {
		t.Fatalf("listed %q", got)
	}
	out := buf.String()
	for id := range bad {
		line := ""
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "client_id="+id+" ") {
				line = l
			}
		}
		if !strings.Contains(line, "level=WARN") || !strings.Contains(line, `client_name="Name `+id+`"`) {
			t.Errorf("%s: no warning naming it:\n%s", id, out)
		}
	}
	if strings.Contains(out, "client_id=good") {
		t.Errorf("a good app is warned about:\n%s", out)
	}
}

// Apps are sorted by name, ignoring case, then by ID.
func TestAppsSortedByName(t *testing.T) {
	s, f := appsSetup(t)
	for _, c := range [][2]string{{"z", "zebra"}, {"b2", "Beta"}, {"a", "alpha"}, {"b1", "beta"}, {"c", "Charlie"}} {
		f.Clients = append(f.Clients, idp.Client{ID: c[0], Name: c[1], LaunchURL: "https://" + c[0] + ".example.org"})
	}
	if got := appIDs(apps(t, s, bobApps)); got != "a,b1,b2,c,z" {
		t.Fatalf("order %q", got)
	}
}

// An app with a light or a dark logo has Pocket ID's public logo URL, which
// serves either; one with neither has none and a letter, the first of its
// name, upper-cased.
func TestAppsLogoAndLetter(t *testing.T) {
	s, f := appsSetup(t)
	f.Clients = []idp.Client{
		{ID: "a b/c", Name: "Logo", LaunchURL: "https://logo.example.org", HasLogo: true},
		{ID: "d", Name: "Night", LaunchURL: "https://night.example.org", HasDarkLogo: true},
		{ID: "e", Name: "émile", LaunchURL: "https://emile.example.org"},
	}
	got := apps(t, s, bobApps)
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if got[0].LogoURL != "https://id.example.org/api/oidc/clients/a%20b%2Fc/logo" || got[0].Letter != "" {
		t.Errorf("logo tile %+v", got[0])
	}
	if got[1].LogoURL != "https://id.example.org/api/oidc/clients/d/logo" || got[1].Letter != "" {
		t.Errorf("dark-only logo tile %+v", got[1])
	}
	if got[2].LogoURL != "" || got[2].Letter != "É" {
		t.Errorf("letter tile %+v", got[2])
	}
	if got[0].Name != "Logo" || got[0].URL != "https://logo.example.org" {
		t.Errorf("%+v", got[0])
	}
}

// The host under an app's name is its launch URL's.
func TestAppsHostIsLaunchHost(t *testing.T) {
	s, f := appsSetup(t)
	f.Clients = []idp.Client{{ID: "w", Name: "Wiki", LaunchURL: "https://wiki.example.org:8443/start?x=1"}}
	got := apps(t, s, bobApps)
	if len(got) != 1 || got[0].Host != "wiki.example.org" || got[0].URL != "https://wiki.example.org:8443/start?x=1" {
		t.Fatalf("%+v", got)
	}
}

// A provider that cannot list clients, or the user's groups, fails the list.
func TestAppsFailWhenProviderFails(t *testing.T) {
	s, f := appsSetup(t)
	f.Clients = []idp.Client{{ID: "open", Name: "Open", LaunchURL: "https://open.example.org"}}
	f.FailClients = true
	if got, err := s.Apps(ctx, bobApps); err == nil || got != nil {
		t.Fatalf("clients failing: %v %v", got, err)
	}
	f.FailClients = false
	f.FailLookup = errors.New("down")
	if got, err := s.Apps(ctx, bobApps); err == nil || got != nil {
		t.Fatalf("groups failing: %v %v", got, err)
	}
}

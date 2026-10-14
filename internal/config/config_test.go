package config

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

func env(over map[string]string) func(string) string {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	base := map[string]string{
		"ACCOUNT_PUBLIC_URL": "https://account.example.org", "ACCOUNT_SIGNUP": "invite", "ACCOUNT_JOIN_PUBLIC_URL": "https://join.example.org", "ACCOUNT_DATABASE_URL": "postgres://x",
		"ACCOUNT_SESSION_KEY": key, "ACCOUNT_EMAIL_HASH_KEY": key,
		"ACCOUNT_OIDC_ISSUER": "https://id.example.org", "ACCOUNT_OIDC_CLIENT_ID": "c", "ACCOUNT_OIDC_CLIENT_SECRET": "s",
		"ACCOUNT_MEMBER_GROUP": "members", "ACCOUNT_ADMIN_GROUP": "admins",
		"ACCOUNT_IDP_URL": "http://10.44.0.1:1413", "ACCOUNT_IDP_API_KEY": "k",
		"ACCOUNT_SMTP_HOST": "mail", "ACCOUNT_SMTP_PORT": "587", "ACCOUNT_SMTP_FROM": "a@example.org", "ACCOUNT_SMTP_TLS": "starttls",
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestDefaults(t *testing.T) {
	// The optional variables are unset, including the SMTP port and TLS mode
	// the base environment otherwise carries.
	c, err := Load(env(map[string]string{"ACCOUNT_SMTP_PORT": "", "ACCOUNT_SMTP_TLS": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustForwarded || c.SMTP.Port != 587 || c.SMTP.TLS != "starttls" {
		t.Fatalf("defaults wrong: trust_forwarded=%v port=%d tls=%q", c.TrustForwarded, c.SMTP.Port, c.SMTP.TLS)
	}
	if c.ListenAddr != ":8080" || c.Quota != 3 || !c.RequireApproval ||
		c.InviteLifetime != 14*24*time.Hour || c.ApplicationWait != 14*24*time.Hour ||
		c.DeclineRetention != 183*24*time.Hour || c.LoginLinkDays != 7 || c.CoCPath != "/etc/paisans-account-service/code-of-conduct.md" {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestRefusals(t *testing.T) {
	for name, over := range map[string]map[string]string{
		"short session key":  {"ACCOUNT_SESSION_KEY": base64.RawURLEncoding.EncodeToString(make([]byte, 16))},
		"padded session key": {"ACCOUNT_SESSION_KEY": base64.URLEncoding.EncodeToString(make([]byte, 32))},
		"padded hash key":    {"ACCOUNT_EMAIL_HASH_KEY": base64.URLEncoding.EncodeToString(make([]byte, 32))},
		"quota not a number": {"ACCOUNT_INVITE_QUOTA": "three"},
		"port zero":          {"ACCOUNT_SMTP_PORT": "0"},
		"port too big":       {"ACCOUNT_SMTP_PORT": "65536"},
		"missing database":   {"ACCOUNT_DATABASE_URL": ""},
		"missing smtp host":  {"ACCOUNT_SMTP_HOST": ""},
		"missing oidc id":    {"ACCOUNT_OIDC_CLIENT_ID": ""},
		"quota zero":         {"ACCOUNT_INVITE_QUOTA": "0"},
		"quota too big":      {"ACCOUNT_INVITE_QUOTA": "101"},
		"lifetime too long":  {"ACCOUNT_INVITE_LIFETIME_DAYS": "366"},
		"retention too long": {"ACCOUNT_DECLINE_RETENTION_DAYS": "3651"},
		"link days zero":     {"ACCOUNT_LOGIN_LINK_DAYS": "0"},
		"link days too long": {"ACCOUNT_LOGIN_LINK_DAYS": "32"},
		"link days word":     {"ACCOUNT_LOGIN_LINK_DAYS": "week"},
		"bad tls":            {"ACCOUNT_SMTP_TLS": "ssl"},
		"bad bool":           {"ACCOUNT_REQUIRE_APPROVAL": "yes"},
		"missing api key":    {"ACCOUNT_IDP_API_KEY": ""},
		"trailing slash":     {"ACCOUNT_PUBLIC_URL": "https://account.example.org/"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(env(over))
			for k := range over {
				if err == nil || !strings.Contains(err.Error(), k) {
					t.Fatalf("want an error naming %s, got %v", k, err)
				}
			}
		})
	}
}

func TestErrorsNeverEchoSecrets(t *testing.T) {
	_, err := Load(env(map[string]string{"ACCOUNT_SESSION_KEY": "not-base64-SECRETVALUE"}))
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("error must name the variable, not the value: %v", err)
	}
}

func TestLoginLinkDays(t *testing.T) {
	for _, n := range []string{"1", "31"} {
		c, err := Load(env(map[string]string{"ACCOUNT_LOGIN_LINK_DAYS": n}))
		if err != nil || fmt.Sprint(c.LoginLinkDays) != n {
			t.Fatalf("%s: %d %v", n, c.LoginLinkDays, err)
		}
	}
}

func TestSignupAndJoinURL(t *testing.T) {
	cases := []struct {
		name    string
		set     map[string]string
		wantErr string
	}{
		{"invite", nil, ""},
		{"open", map[string]string{"ACCOUNT_SIGNUP": "open"}, ""},
		{"signup unset", map[string]string{"ACCOUNT_SIGNUP": ""}, "ACCOUNT_SIGNUP"},
		{"signup not a mode", map[string]string{"ACCOUNT_SIGNUP": "true"}, "ACCOUNT_SIGNUP must be invite or open"},
		{"join url unset, invite", map[string]string{"ACCOUNT_JOIN_PUBLIC_URL": ""}, "ACCOUNT_JOIN_PUBLIC_URL is required"},
		{"join url unset, open", map[string]string{"ACCOUNT_SIGNUP": "open", "ACCOUNT_JOIN_PUBLIC_URL": ""}, "ACCOUNT_JOIN_PUBLIC_URL is required"},
		{"join url trailing slash", map[string]string{"ACCOUNT_JOIN_PUBLIC_URL": "https://join.example.org/"}, "ACCOUNT_JOIN_PUBLIC_URL must not end in /"},
		{"account url trailing slash", map[string]string{"ACCOUNT_PUBLIC_URL": "https://account.example.org/"}, "ACCOUNT_PUBLIC_URL must not end in /"},
		{"same host", map[string]string{"ACCOUNT_JOIN_PUBLIC_URL": "https://ACCOUNT.example.org:443"}, "must be different hosts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(env(c.set))
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestOperators(t *testing.T) {
	for in, want := range map[string]string{"": "[]", " alice, Bob ,": "[alice Bob]", "carol": "[carol]"} {
		c, err := Load(env(map[string]string{"ACCOUNT_OPERATORS": in}))
		if err != nil || fmt.Sprint(c.Operators) != want {
			t.Errorf("%q: %v %v", in, c.Operators, err)
		}
	}
	for _, bad := range []string{"bad name!", "alice,,-bob"} {
		if _, err := Load(env(map[string]string{"ACCOUNT_OPERATORS": bad})); err == nil || !strings.Contains(err.Error(), "ACCOUNT_OPERATORS") || strings.Contains(err.Error(), "bad name") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestHosts(t *testing.T) {
	cfg, err := Load(env(map[string]string{"ACCOUNT_PUBLIC_URL": "https://Account.Example.org:8443"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccountHost() != "account.example.org" || cfg.JoinHost() != "join.example.org" {
		t.Fatalf("hosts: %q %q", cfg.AccountHost(), cfg.JoinHost())
	}
}

// ACCOUNT_IDP_URL carries the API key, which goes only to a private address:
// a public one stops the service at startup, naming the variable.
func TestIDPURLMustBePrivate(t *testing.T) {
	for _, c := range []struct {
		url, networks string
		ok            bool
	}{
		{"http://10.44.0.1:1413", "", true},
		{"http://100.70.0.1:1413", "", true},
		{"http://[fd00::1]:1413", "", true},
		{"http://127.0.0.1:1411", "", true},
		{"http://localhost:1411", "", true},
		{"http://pocket-id:1411", "", true},
		{"http://203.0.113.5:1413", "203.0.113.0/24", true},
		{"http://203.0.113.5:1413", "198.51.100.0/24, 203.0.113.0/24", true},
		{"https://id.example.org", "", false},
		{"http://203.0.113.5:1413", "", false},
		{"http://203.0.113.5:1413", "198.51.100.0/24", false},
		{"http://pocket-id.internal:1411", "", false},
		{"http://[2001:db8::1]:1413", "", false},
		{"not a url", "", false},
		{"/api", "", false},
	} {
		cfg, err := Load(env(map[string]string{"ACCOUNT_IDP_URL": c.url, "ACCOUNT_IDP_PRIVATE_NETWORKS": c.networks}))
		if c.ok && err != nil {
			t.Errorf("%s (%s): %v", c.url, c.networks, err)
		}
		if !c.ok && (err == nil || !strings.Contains(err.Error(), "ACCOUNT_IDP_URL")) {
			t.Errorf("%s (%s): want an error naming ACCOUNT_IDP_URL, got %v", c.url, c.networks, err)
		}
		if c.ok && c.networks != "" && len(cfg.IDPNetworks) == 0 {
			t.Errorf("%s: networks not kept", c.networks)
		}
	}
}

func TestIDPPrivateNetworks(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil || cfg.IDPNetworks != nil {
		t.Fatalf("unset: %v %v", cfg.IDPNetworks, err)
	}
	cfg, err = Load(env(map[string]string{"ACCOUNT_IDP_PRIVATE_NETWORKS": " 203.0.113.0/24 ,198.51.100.0/24,"}))
	if err != nil || fmt.Sprint(cfg.IDPNetworks) != "[203.0.113.0/24 198.51.100.0/24]" {
		t.Fatalf("%v %v", cfg.IDPNetworks, err)
	}
	for _, bad := range []string{"203.0.113.5", "203.0.113.0/33", "mesh"} {
		if _, err := Load(env(map[string]string{"ACCOUNT_IDP_PRIVATE_NETWORKS": bad})); err == nil || !strings.Contains(err.Error(), "ACCOUNT_IDP_PRIVATE_NETWORKS") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// Members are sent to Pocket ID at the issuer, its public URL: ACCOUNT_IDP_URL
// is a private address no browser can reach.
func TestIDPPublicURL(t *testing.T) {
	cfg, err := Load(env(map[string]string{"ACCOUNT_OIDC_ISSUER": "https://id.example.org/"}))
	if err != nil || cfg.IDPPublicURL() != "https://id.example.org" {
		t.Fatalf("%q %v", cfg.IDPPublicURL(), err)
	}
}

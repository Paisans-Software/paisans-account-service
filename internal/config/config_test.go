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
		"ACCOUNT_PUBLIC_URL": "https://account.example.org", "ACCOUNT_INVITES": "true", "ACCOUNT_INVITE_PUBLIC_URL": "https://invite.example.org", "ACCOUNT_DATABASE_URL": "postgres://x",
		"ACCOUNT_SESSION_KEY": key, "ACCOUNT_EMAIL_HASH_KEY": key,
		"ACCOUNT_OIDC_ISSUER": "https://id.example.org", "ACCOUNT_OIDC_CLIENT_ID": "c", "ACCOUNT_OIDC_CLIENT_SECRET": "s",
		"ACCOUNT_MEMBER_GROUP": "members", "ACCOUNT_ADMIN_GROUP": "admins",
		"ACCOUNT_IDP_URL": "https://id.example.org", "ACCOUNT_IDP_API_KEY": "k",
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

func TestInvitesURLs(t *testing.T) {
	cases := []struct {
		name    string
		set     map[string]string
		wantErr string
	}{
		{"invites on, both urls", nil, ""},
		{"invites unset", map[string]string{"ACCOUNT_INVITES": ""}, "ACCOUNT_INVITES"},
		{"invites not a bool", map[string]string{"ACCOUNT_INVITES": "yes"}, "ACCOUNT_INVITES"},
		{"invites on, no invite url", map[string]string{"ACCOUNT_INVITE_PUBLIC_URL": ""}, "ACCOUNT_INVITE_PUBLIC_URL"},
		{"invites off, invite url set", map[string]string{"ACCOUNT_INVITES": "false"}, "ACCOUNT_INVITE_PUBLIC_URL"},
		{"invites off, no invite url", map[string]string{"ACCOUNT_INVITES": "false", "ACCOUNT_INVITE_PUBLIC_URL": ""}, ""},
		{"invite url trailing slash", map[string]string{"ACCOUNT_INVITE_PUBLIC_URL": "https://invite.example.org/"}, "ACCOUNT_INVITE_PUBLIC_URL must not end in /"},
		{"account url trailing slash", map[string]string{"ACCOUNT_PUBLIC_URL": "https://account.example.org/"}, "ACCOUNT_PUBLIC_URL must not end in /"},
		{"same host", map[string]string{"ACCOUNT_INVITE_PUBLIC_URL": "https://ACCOUNT.example.org:443"}, "must be different hosts"},
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

func TestHosts(t *testing.T) {
	cfg, err := Load(env(map[string]string{"ACCOUNT_PUBLIC_URL": "https://Account.Example.org:8443"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccountHost() != "account.example.org" || cfg.InviteHost() != "invite.example.org" {
		t.Fatalf("hosts: %q %q", cfg.AccountHost(), cfg.InviteHost())
	}
	off, err := Load(env(map[string]string{"ACCOUNT_INVITES": "false", "ACCOUNT_INVITE_PUBLIC_URL": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if off.InviteHost() != "" || off.InviteURL != "" || off.Invites {
		t.Fatalf("invites off: %+v", off)
	}
}

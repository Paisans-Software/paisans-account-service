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
		"INVITE_PUBLIC_URL": "https://invite.example.org", "INVITE_DATABASE_URL": "postgres://x",
		"INVITE_SESSION_KEY": key, "INVITE_EMAIL_HASH_KEY": key,
		"INVITE_OIDC_ISSUER": "https://id.example.org", "INVITE_OIDC_CLIENT_ID": "c", "INVITE_OIDC_CLIENT_SECRET": "s",
		"INVITE_MEMBER_GROUP": "members", "INVITE_ADMIN_GROUP": "admins",
		"INVITE_IDP_URL": "https://id.example.org", "INVITE_IDP_API_KEY": "k",
		"INVITE_SMTP_HOST": "mail", "INVITE_SMTP_PORT": "587", "INVITE_SMTP_FROM": "a@example.org", "INVITE_SMTP_TLS": "starttls",
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestDefaults(t *testing.T) {
	// The optional variables are unset, including the SMTP port and TLS mode
	// the base environment otherwise carries.
	c, err := Load(env(map[string]string{"INVITE_SMTP_PORT": "", "INVITE_SMTP_TLS": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustForwarded || c.SMTP.Port != 587 || c.SMTP.TLS != "starttls" {
		t.Fatalf("defaults wrong: trust_forwarded=%v port=%d tls=%q", c.TrustForwarded, c.SMTP.Port, c.SMTP.TLS)
	}
	if c.ListenAddr != ":8080" || c.Quota != 3 || !c.RequireApproval ||
		c.InviteLifetime != 14*24*time.Hour || c.ApplicationWait != 14*24*time.Hour ||
		c.DeclineRetention != 183*24*time.Hour || c.LoginLinkDays != 7 || c.CoCPath != "/etc/paisans-invite/code-of-conduct.md" {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestRefusals(t *testing.T) {
	for name, over := range map[string]map[string]string{
		"short session key":  {"INVITE_SESSION_KEY": base64.RawURLEncoding.EncodeToString(make([]byte, 16))},
		"padded session key": {"INVITE_SESSION_KEY": base64.URLEncoding.EncodeToString(make([]byte, 32))},
		"padded hash key":    {"INVITE_EMAIL_HASH_KEY": base64.URLEncoding.EncodeToString(make([]byte, 32))},
		"quota not a number": {"INVITE_QUOTA": "three"},
		"port zero":          {"INVITE_SMTP_PORT": "0"},
		"port too big":       {"INVITE_SMTP_PORT": "65536"},
		"missing database":   {"INVITE_DATABASE_URL": ""},
		"missing smtp host":  {"INVITE_SMTP_HOST": ""},
		"missing oidc id":    {"INVITE_OIDC_CLIENT_ID": ""},
		"quota zero":         {"INVITE_QUOTA": "0"},
		"quota too big":      {"INVITE_QUOTA": "101"},
		"lifetime too long":  {"INVITE_LIFETIME_DAYS": "366"},
		"retention too long": {"INVITE_DECLINE_RETENTION_DAYS": "3651"},
		"link days zero":     {"INVITE_LOGIN_LINK_DAYS": "0"},
		"link days too long": {"INVITE_LOGIN_LINK_DAYS": "32"},
		"link days word":     {"INVITE_LOGIN_LINK_DAYS": "week"},
		"bad tls":            {"INVITE_SMTP_TLS": "ssl"},
		"bad bool":           {"INVITE_REQUIRE_APPROVAL": "yes"},
		"missing api key":    {"INVITE_IDP_API_KEY": ""},
		"trailing slash":     {"INVITE_PUBLIC_URL": "https://invite.example.org/"},
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
	_, err := Load(env(map[string]string{"INVITE_SESSION_KEY": "not-base64-SECRETVALUE"}))
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("error must name the variable, not the value: %v", err)
	}
}

func TestLoginLinkDays(t *testing.T) {
	for _, n := range []string{"1", "31"} {
		c, err := Load(env(map[string]string{"INVITE_LOGIN_LINK_DAYS": n}))
		if err != nil || fmt.Sprint(c.LoginLinkDays) != n {
			t.Fatalf("%s: %d %v", n, c.LoginLinkDays, err)
		}
	}
}

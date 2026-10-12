// Package config reads the service's environment. The variable names are the
// contract with paisans-stack's invite kind.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	TLS      string
}

type Config struct {
	ListenAddr, PublicURL, DatabaseURL         string
	SessionKey, EmailHashKey                   []byte
	OIDCIssuer, OIDCClientID, OIDCClientSecret string
	MemberGroup, AdminGroup                    string
	IDPURL, IDPAPIKey                          string
	SMTP                                       SMTP
	AccentColor                                string
	Quota                                      int
	RequireApproval                            bool
	InviteLifetime, ApplicationWait            time.Duration
	DeclineRetention                           time.Duration
	// LoginLinkDays is how long the identity provider's sign-in link lasts.
	// Pocket ID accepts at most 744h, which is 31 days.
	LoginLinkDays  int
	CoCPath        string
	TrustForwarded bool
}

func Load(getenv func(string) string) (Config, error) {
	var errs []error
	req := func(k string) string {
		v := getenv(k)
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", k))
		}
		return v
	}
	opt := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	key := func(k string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(req(k))
		if err != nil || len(b) != 32 {
			errs = append(errs, fmt.Errorf("%s must be 32 bytes as unpadded base64url", k))
			return nil
		}
		return b
	}
	intIn := func(k, def string, lo, hi int) int {
		n, err := strconv.Atoi(opt(k, def))
		if err != nil || n < lo || n > hi {
			errs = append(errs, fmt.Errorf("%s must be a whole number from %d to %d", k, lo, hi))
		}
		return n
	}
	boolean := func(k, def string) bool {
		switch opt(k, def) {
		case "true":
			return true
		case "false":
			return false
		}
		errs = append(errs, fmt.Errorf("%s must be true or false", k))
		return false
	}
	days := func(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

	c := Config{
		ListenAddr:       opt("INVITE_LISTEN_ADDR", ":8080"),
		PublicURL:        req("INVITE_PUBLIC_URL"),
		DatabaseURL:      req("INVITE_DATABASE_URL"),
		SessionKey:       key("INVITE_SESSION_KEY"),
		EmailHashKey:     key("INVITE_EMAIL_HASH_KEY"),
		OIDCIssuer:       req("INVITE_OIDC_ISSUER"),
		OIDCClientID:     req("INVITE_OIDC_CLIENT_ID"),
		OIDCClientSecret: req("INVITE_OIDC_CLIENT_SECRET"),
		MemberGroup:      req("INVITE_MEMBER_GROUP"),
		AdminGroup:       req("INVITE_ADMIN_GROUP"),
		IDPURL:           strings.TrimRight(req("INVITE_IDP_URL"), "/"),
		IDPAPIKey:        req("INVITE_IDP_API_KEY"),
		SMTP: SMTP{
			Host: req("INVITE_SMTP_HOST"), Port: intIn("INVITE_SMTP_PORT", "587", 1, 65535),
			User: getenv("INVITE_SMTP_USER"), Password: getenv("INVITE_SMTP_PASSWORD"),
			From: req("INVITE_SMTP_FROM"), TLS: opt("INVITE_SMTP_TLS", "starttls"),
		},
		AccentColor:      getenv("INVITE_ACCENT_COLOR"),
		Quota:            intIn("INVITE_QUOTA", "3", 1, 100),
		RequireApproval:  boolean("INVITE_REQUIRE_APPROVAL", "true"),
		InviteLifetime:   days(intIn("INVITE_LIFETIME_DAYS", "14", 1, 365)),
		ApplicationWait:  days(intIn("INVITE_APPLICATION_WAIT_DAYS", "14", 1, 365)),
		DeclineRetention: days(intIn("INVITE_DECLINE_RETENTION_DAYS", "183", 1, 3650)),
		LoginLinkDays:    intIn("INVITE_LOGIN_LINK_DAYS", "7", 1, 31),
		CoCPath:          opt("INVITE_COC_PATH", "/etc/paisans-invite/code-of-conduct.md"),
		TrustForwarded:   boolean("INVITE_TRUST_FORWARDED", "false"),
	}
	switch c.SMTP.TLS {
	case "none", "starttls", "tls":
	default:
		errs = append(errs, errors.New("INVITE_SMTP_TLS must be none, starttls or tls"))
	}
	if strings.HasSuffix(c.PublicURL, "/") {
		errs = append(errs, errors.New("INVITE_PUBLIC_URL must not end in /"))
	}
	return c, errors.Join(errs...)
}

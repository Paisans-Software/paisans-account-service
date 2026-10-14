// Package config reads the service's environment. The variable names are the
// contract with the Paisans toolkit's `account` blueprint.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/idp"
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
	ListenAddr, DatabaseURL string
	// AccountURL is where members and admins sign in, from ACCOUNT_PUBLIC_URL.
	AccountURL string
	// Signup is how people join: "invite" (a member's invite link) or
	// "open" (a registration page).
	Signup string
	// JoinURL is where people without an account sign up, from
	// ACCOUNT_JOIN_PUBLIC_URL.
	JoinURL string
	// Operators are the usernames declared as operators in the deployment.
	// The service refuses to ban them or change their address or name.
	Operators                                  []string
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
		ListenAddr:       opt("ACCOUNT_LISTEN_ADDR", ":8080"),
		AccountURL:       req("ACCOUNT_PUBLIC_URL"),
		DatabaseURL:      req("ACCOUNT_DATABASE_URL"),
		SessionKey:       key("ACCOUNT_SESSION_KEY"),
		EmailHashKey:     key("ACCOUNT_EMAIL_HASH_KEY"),
		OIDCIssuer:       req("ACCOUNT_OIDC_ISSUER"),
		OIDCClientID:     req("ACCOUNT_OIDC_CLIENT_ID"),
		OIDCClientSecret: req("ACCOUNT_OIDC_CLIENT_SECRET"),
		MemberGroup:      req("ACCOUNT_MEMBER_GROUP"),
		AdminGroup:       req("ACCOUNT_ADMIN_GROUP"),
		IDPURL:           strings.TrimRight(req("ACCOUNT_IDP_URL"), "/"),
		IDPAPIKey:        req("ACCOUNT_IDP_API_KEY"),
		SMTP: SMTP{
			Host: req("ACCOUNT_SMTP_HOST"), Port: intIn("ACCOUNT_SMTP_PORT", "587", 1, 65535),
			User: getenv("ACCOUNT_SMTP_USER"), Password: getenv("ACCOUNT_SMTP_PASSWORD"),
			From: req("ACCOUNT_SMTP_FROM"), TLS: opt("ACCOUNT_SMTP_TLS", "starttls"),
		},
		AccentColor:      getenv("ACCOUNT_ACCENT_COLOR"),
		Quota:            intIn("ACCOUNT_INVITE_QUOTA", "3", 1, 100),
		RequireApproval:  boolean("ACCOUNT_REQUIRE_APPROVAL", "true"),
		InviteLifetime:   days(intIn("ACCOUNT_INVITE_LIFETIME_DAYS", "14", 1, 365)),
		ApplicationWait:  days(intIn("ACCOUNT_APPLICATION_WAIT_DAYS", "14", 1, 365)),
		DeclineRetention: days(intIn("ACCOUNT_DECLINE_RETENTION_DAYS", "183", 1, 3650)),
		LoginLinkDays:    intIn("ACCOUNT_LOGIN_LINK_DAYS", "7", 1, 31),
		CoCPath:          opt("ACCOUNT_COC_PATH", "/etc/paisans-account-service/code-of-conduct.md"),
		TrustForwarded:   boolean("ACCOUNT_TRUST_FORWARDED", "false"),
	}
	switch c.SMTP.TLS {
	case "none", "starttls", "tls":
	default:
		errs = append(errs, errors.New("ACCOUNT_SMTP_TLS must be none, starttls or tls"))
	}
	c.Signup = getenv("ACCOUNT_SIGNUP")
	switch c.Signup {
	case SignupInvite, SignupOpen:
	case "":
		errs = append(errs, errors.New("ACCOUNT_SIGNUP is required"))
	default:
		errs = append(errs, errors.New("ACCOUNT_SIGNUP must be invite or open"))
	}
	c.JoinURL = req("ACCOUNT_JOIN_PUBLIC_URL")
	for _, u := range []struct{ name, value string }{
		{"ACCOUNT_PUBLIC_URL", c.AccountURL},
		{"ACCOUNT_JOIN_PUBLIC_URL", c.JoinURL},
	} {
		if strings.HasSuffix(u.value, "/") {
			errs = append(errs, fmt.Errorf("%s must not end in /", u.name))
		}
	}
	// Every route is registered on one of the two hosts, so one host for
	// both would make them collide.
	if c.JoinURL != "" && c.AccountHost() == c.JoinHost() {
		errs = append(errs, errors.New("ACCOUNT_PUBLIC_URL and ACCOUNT_JOIN_PUBLIC_URL must be different hosts"))
	}
	for _, name := range strings.Split(getenv("ACCOUNT_OPERATORS"), ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if len(name) > idp.MaxUsername || !idp.UsernameRule.MatchString(name) {
			errs = append(errs, errors.New("ACCOUNT_OPERATORS must be usernames separated by commas"))
			break
		}
		c.Operators = append(c.Operators, name)
	}
	return c, errors.Join(errs...)
}

// AccountHost is the host members and admins use, lowercase and without a port.
func (c Config) AccountHost() string { return hostOf(c.AccountURL) }

// JoinHost is the host people without an account sign up on.
func (c Config) JoinHost() string { return hostOf(c.JoinURL) }

// The values of ACCOUNT_SIGNUP.
const (
	SignupInvite = "invite"
	SignupOpen   = "open"
)

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

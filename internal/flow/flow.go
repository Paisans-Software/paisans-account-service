// Package flow is the service's rules: the only package that uses the store,
// the identity provider and mail together.
package flow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/paisans-software/paisans-invite/internal/coc"
	"github.com/paisans-software/paisans-invite/internal/emailhash"
	"github.com/paisans-software/paisans-invite/internal/idp"
	imail "github.com/paisans-software/paisans-invite/internal/mail"
	"github.com/paisans-software/paisans-invite/internal/store"
)

var (
	ErrInvalidUsername = errors.New("username must be 1 to 50 letters, numbers, dots, underscores, hyphens or @, starting and ending with a letter or number")
	ErrInvalidEmail    = errors.New("that does not look like an email address")
	ErrInvalidName     = errors.New("display name must be 1 to 100 characters")
	ErrUsernameTaken   = errors.New("that username is taken")
	ErrEmailTaken      = errors.New("an account already uses that email address")
	ErrNotAgreed       = errors.New("you need to agree to the code of conduct")
	ErrForbidden       = errors.New("forbidden")
	// ErrBadNonce is a create form without a well-formed one-time nonce.
	ErrBadNonce = errors.New("the form is missing its one-time value")
)

type Member struct {
	Sub, Username string
	Admin         bool
}

type Settings struct {
	Quota           int
	RequireApproval bool
	InviteLifetime  time.Duration
	PublicURL       string
	EmailHashKey    []byte
	// AdminGroup is the provider group whose members are the service's admins.
	AdminGroup string
	// LoginLinkDays is the sign-in link's lifetime, the one the provider
	// client was built with, for the pages that state it.
	LoginLinkDays int
}

type Applicant struct {
	Username, Email, DisplayName string
	Agreed                       bool
}

type Pending struct {
	store.Application
	Declines store.DeclineHistory
}

type Service struct {
	st  *store.Store
	p   idp.Provider
	m   imail.Sender
	coc coc.CoC
	s   Settings
	now func() time.Time
}

func New(st *store.Store, p idp.Provider, m imail.Sender, c coc.CoC, s Settings, now func() time.Time) *Service {
	return &Service{st: st, p: p, m: m, coc: c, s: s, now: now}
}

func (s *Service) CoC() coc.CoC { return s.coc }

// Quota is the number of invites a non-admin member may have in use.
func (s *Service) Quota() int { return s.s.Quota }

// LoginLinkDays is how long the provider's sign-in link lasts.
func (s *Service) LoginLinkDays() int { return s.s.LoginLinkDays }

func TokenHash(token string) ([]byte, bool) {
	if len(token) != 43 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	h := sha256.Sum256([]byte(token))
	return h[:], true
}

// NewNonce returns a fresh one-time value for an invite create form: 16
// random bytes as unpadded base64url.
func NewNonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func validNonce(n string) bool {
	b, err := base64.RawURLEncoding.DecodeString(n)
	return err == nil && len(b) == 16
}

// CreateInvite makes an invite for the form that carried nonce. The same nonce
// a second time makes nothing and returns store.ErrDuplicateCreate.
func (s *Service) CreateInvite(ctx context.Context, m Member, nonce string) (string, store.Invite, error) {
	if !validNonce(nonce) {
		return "", store.Invite{}, ErrBadNonce
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", store.Invite{}, err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	h, _ := TokenHash(tok)
	quota := s.s.Quota
	if m.Admin {
		quota = 0
	}
	inv, err := s.st.CreateInvite(ctx, h, m.Sub, m.Username, nonce, s.now().Add(s.s.InviteLifetime), quota)
	if err != nil {
		return "", store.Invite{}, err
	}
	slog.Info("write", "action", "invite.create", "invite_id", inv.ID)
	return s.s.PublicURL + "/i/" + tok, inv, nil
}

func (s *Service) Invites(ctx context.Context, m Member) ([]store.Invite, error) {
	return s.st.ListInvites(ctx, m.Sub)
}

func (s *Service) Revoke(ctx context.Context, m Member, id int64) error {
	if err := s.st.RevokeInvite(ctx, id, m.Sub); err != nil {
		return err
	}
	slog.Info("write", "action", "invite.revoke", "invite_id", id)
	return nil
}

func (s *Service) OpenInvite(ctx context.Context, token string) (store.Invite, error) {
	h, ok := TokenHash(token)
	if !ok {
		return store.Invite{}, store.ErrNotUsable
	}
	return s.st.OpenInvite(ctx, h, s.now())
}

func (a Applicant) check() (Applicant, error) {
	a.Username = strings.TrimSpace(a.Username)
	a.Email = strings.TrimSpace(a.Email)
	a.DisplayName = strings.TrimSpace(a.DisplayName)
	if !a.Agreed {
		return a, ErrNotAgreed
	}
	if len(a.Username) > idp.MaxUsername || !idp.UsernameRule.MatchString(a.Username) {
		return a, ErrInvalidUsername
	}
	if addr, err := mail.ParseAddress(a.Email); err != nil || addr.Address != a.Email {
		return a, ErrInvalidEmail
	}
	if n := utf8.RuneCountInString(a.DisplayName); n == 0 || n > idp.MaxDisplayName {
		return a, ErrInvalidName
	}
	return a, nil
}

// available is checked at application and again at approval. A provider
// outage at application time defers the check rather than refusing.
func (s *Service) available(ctx context.Context, username, email string, strict bool) error {
	taken, err := s.p.UsernameTaken(ctx, username)
	if err != nil && strict {
		return err
	}
	if taken {
		return ErrUsernameTaken
	}
	taken, err = s.p.EmailTaken(ctx, email)
	if err != nil && strict {
		return err
	}
	if taken {
		return ErrEmailTaken
	}
	return nil
}

func (s *Service) Apply(ctx context.Context, token string, in Applicant) (bool, error) {
	h, ok := TokenHash(token)
	if !ok {
		return false, store.ErrNotUsable
	}
	a, err := in.check()
	if err != nil {
		return false, err
	}
	if taken, err := s.st.PendingUsernameTaken(ctx, a.Username); err != nil {
		return false, err
	} else if taken {
		return false, ErrUsernameTaken
	}
	if err := s.available(ctx, a.Username, a.Email, false); err != nil {
		return false, err
	}
	app, err := s.st.Apply(ctx, h, s.now(), store.NewApplication{Username: a.Username, Email: a.Email, DisplayName: a.DisplayName, CoCVersion: s.coc.Version})
	if errors.Is(err, store.ErrUsernameTaken) {
		return false, ErrUsernameTaken
	}
	if err != nil {
		return false, err
	}
	slog.Info("write", "action", "application.create", "application_id", app.ID)
	if !s.s.RequireApproval {
		err := s.approve(ctx, "", app)
		if err == nil {
			return true, nil
		}
		// The invite is spent, so the applicant is told to wait and the admins
		// can retry from the pending list.
		slog.Warn("automatic approval failed; left for an admin", "application_id", app.ID, "error", err)
	}
	if err := s.m.Send(ctx, []string{app.Email}, "We received your application",
		"Thank you for agreeing to the code of conduct. An admin will review your application, and you will get another email when they have.\n"); err != nil {
		slog.Warn("could not acknowledge an application", "error", err)
	}
	s.notifyAdmins(ctx)
	return false, nil
}

func (s *Service) notifyAdmins(ctx context.Context) {
	admins, err := s.p.AdminGroupEmails(ctx, s.s.AdminGroup)
	if err == nil && len(admins) > 0 {
		err = s.m.Send(ctx, admins, "An invite application is waiting",
			"Someone has accepted an invite and is waiting for approval.\n\n"+s.s.PublicURL+"/admin\n")
	}
	if err != nil {
		slog.Warn("could not notify admins of a waiting application", "error", err)
	}
}

func (s *Service) Pending(ctx context.Context) ([]Pending, error) {
	apps, err := s.st.PendingApplications(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Pending, 0, len(apps))
	for _, a := range apps {
		d, err := s.st.DeclineHistory(ctx, emailhash.Sum(s.s.EmailHashKey, a.Email))
		if err != nil {
			return nil, err
		}
		out = append(out, Pending{Application: a, Declines: d})
	}
	return out, nil
}

func (s *Service) Approve(ctx context.Context, admin Member, id int64) error {
	if !admin.Admin {
		return ErrForbidden
	}
	app, err := s.st.Application(ctx, id)
	if err != nil {
		return err
	}
	return s.approve(ctx, admin.Sub, app)
}

func (s *Service) approve(ctx context.Context, adminSub string, app store.Application) error {
	userID := app.IDPUserID
	if app.State == "link_unsent" {
		u, found, err := s.p.FindUser(ctx, app.Username)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("account %d was created but cannot be found at the identity provider", app.ID)
		}
		userID = u.ID
	} else {
		// The claim makes this approval the only one that may create the
		// account; a second admin, or a double submit, is refused as busy.
		claimed, err := s.st.ClaimApplication(ctx, app.ID, adminSub, s.now())
		if err != nil {
			return err
		}
		slog.Info("write", "action", "application.claim", "application_id", app.ID)
		if userID, err = s.createAccount(ctx, adminSub, claimed); err != nil {
			s.release(ctx, adminSub, app.ID)
			return err
		}
	}
	if err := s.p.SendLoginLink(ctx, userID); err != nil {
		return fmt.Errorf("the account exists but its sign-in link was not sent; retry: %w", err)
	}
	s.audit(ctx, adminSub, "idp.login_link", app.ID, "")
	slog.Info("write", "action", "idp.login_link", "application_id", app.ID)
	if err := s.st.CompleteApplication(ctx, app.ID, adminSub); err != nil {
		return err
	}
	slog.Info("write", "action", "application.complete", "application_id", app.ID)
	return nil
}

// createAccount creates the claimed application's account, or adopts the one
// a previous attempt created, and records it as link_unsent.
func (s *Service) createAccount(ctx context.Context, adminSub string, app store.Application) (string, error) {
	// A previous attempt may have created the account and then failed to
	// record it. An account with this username and this applicant's email is
	// that account, not a clash.
	u, found, err := s.p.FindUser(ctx, app.Username)
	if err != nil {
		return "", err
	}
	userID := u.ID
	if !found || !strings.EqualFold(strings.TrimSpace(u.Email), app.Email) {
		if err := s.available(ctx, app.Username, app.Email, true); err != nil {
			return "", err
		}
		u, err := s.p.CreateUser(ctx, idp.NewUser{Username: app.Username, Email: app.Email, DisplayName: app.DisplayName})
		if err != nil {
			return "", err
		}
		userID = u.ID
		s.audit(ctx, adminSub, "idp.user_create", app.ID, " idp="+userID)
		slog.Info("write", "action", "idp.user_create", "application_id", app.ID)
	}
	if err := s.st.MarkLinkUnsent(ctx, app.ID, userID, adminSub); err != nil {
		return "", err
	}
	slog.Info("write", "action", "application.link_unsent", "application_id", app.ID)
	return userID, nil
}

// release returns a claim after a failed approval. If it cannot, the claim
// lapses on its own after the store's timeout.
func (s *Service) release(ctx context.Context, adminSub string, id int64) {
	if err := s.st.ReleaseClaim(context.WithoutCancel(ctx), id, adminSub); err != nil {
		slog.Warn("could not release an approval claim", "application_id", id, "error", err)
		return
	}
	slog.Info("write", "action", "application.release", "application_id", id)
}

func (s *Service) Decline(ctx context.Context, admin Member, id int64) error {
	if !admin.Admin {
		return ErrForbidden
	}
	app, err := s.st.Application(ctx, id)
	if err != nil {
		return err
	}
	if err := s.st.DeclineApplication(ctx, id, admin.Sub, emailhash.Sum(s.s.EmailHashKey, app.Email)); err != nil {
		return err
	}
	slog.Info("write", "action", "application.decline", "application_id", id)
	if err := s.m.Send(ctx, []string{app.Email}, "About your invitation",
		"Thank you for your interest. Your application to join was not accepted.\n"); err != nil {
		slog.Warn("could not tell the invitee of a decline", "error", err)
	}
	inviter, found, err := s.p.FindUser(ctx, app.InviterUsername)
	if err != nil {
		slog.Warn("could not look up the inviter of a declined application", "application_id", id, "error", err)
	} else if found && inviter.Email != "" {
		if err := s.m.Send(ctx, []string{inviter.Email}, "An invite you sent was declined",
			"An application made with one of your invites was declined. Your invite slot is free again.\n"); err != nil {
			slog.Warn("could not tell the inviter of a decline", "error", err)
		}
	}
	return nil
}

// audit records a provider write that has already happened, so a failure to
// record it is logged rather than allowed to abort the flow. The subject is the
// application ID followed by extra, which may only hold further IDs.
func (s *Service) audit(ctx context.Context, actorSub, action string, id int64, extra string) {
	if err := s.st.Audit(ctx, actorSub, action, fmt.Sprint(id)+extra); err != nil {
		slog.Warn("could not audit a provider write", "action", action, "application_id", id, "error", err)
	}
}

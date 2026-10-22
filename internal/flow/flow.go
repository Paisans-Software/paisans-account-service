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

	"github.com/paisans-software/paisans-account-service/internal/coc"
	"github.com/paisans-software/paisans-account-service/internal/emailhash"
	"github.com/paisans-software/paisans-account-service/internal/idp"
	imail "github.com/paisans-software/paisans-account-service/internal/mail"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

var (
	ErrInvalidUsername = errors.New("username must be 1 to 50 lowercase letters, numbers, dots, underscores, hyphens or @, starting and ending with a letter or number")
	ErrInvalidEmail    = errors.New("that does not look like an email address")
	ErrInvalidName     = errors.New("display name must be 1 to 100 characters")
	ErrInvalidFirst    = errors.New("first name must be 1 to 50 characters")
	ErrInvalidLast     = errors.New("last name must be at most 50 characters")
	ErrUsernameTaken   = errors.New("that username is taken")
	ErrEmailTaken      = errors.New("an account already uses that email address")
	ErrNotAgreed       = errors.New("you need to agree to the code of conduct")
	ErrForbidden       = errors.New("forbidden")
	// ErrBanned is an application whose address matches a current ban.
	ErrBanned = errors.New("that address is banned")
	// ErrBadNonce is a create form without a well-formed one-time nonce.
	ErrBadNonce = errors.New("the form is missing its one-time value")
)

type Member struct {
	Sub, Username string
	Admin         bool
}

// IsAdmin reports whether the user is in the admin group at the identity
// provider now. A user it no longer has is not an admin.
func (s *Service) IsAdmin(ctx context.Context, sub string) (bool, error) {
	gs, err := s.p.UserGroups(ctx, sub)
	if errors.Is(err, idp.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, g := range gs {
		if g.Name == s.s.AdminGroup {
			return true, nil
		}
	}
	return false, nil
}

type Settings struct {
	Quota           int
	RequireApproval bool
	InviteLifetime  time.Duration
	// AccountURL is the base of links to the account host, such as /admin.
	AccountURL string
	// JoinURL is the base of invite links.
	JoinURL      string
	EmailHashKey []byte
	// AdminGroup is the provider group whose members are the service's admins.
	AdminGroup string
	// LoginLinkDays is the sign-in link's lifetime, the one the provider
	// client was built with, for the pages that state it.
	LoginLinkDays int
	// IDPURL is the identity provider as members reach it, its public URL
	// (config.Config.IDPPublicURL), for mail that sends someone there to
	// sign in. It is never ACCOUNT_IDP_URL, which is a private address.
	IDPURL string
	// Operators are the usernames the deployment declares as operators: the
	// service never bans them or changes their address or name.
	Operators []string
}

type Applicant struct {
	Username, Email, DisplayName string
	FirstName, LastName          string
	Agreed                       bool
}

type Pending struct {
	store.Application
	Declines store.DeclineHistory
	// Stale is an approving application whose claim has outlived the
	// takeover window: it may be approved again, or dismissed.
	Stale bool
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
	return s.s.JoinURL + "/i/" + tok, inv, nil
}

// Invite is one of a member's invites. Invitee is the account a used invite
// made, or nil for an invite not used, or used before the service recorded it.
type Invite struct {
	store.Invite
	Invitee *Invitee
}

// Invitee is the account an invite made, as the provider has it now. Gone is
// an account the provider no longer has; Unavailable is one it could not be
// asked about. Either way the names are empty.
type Invitee struct {
	Username, DisplayName string
	Gone, Unavailable     bool
}

// Invites returns the member's invites, each used one with its invitee read
// from the provider. A lookup that fails marks that invitee Unavailable and
// fails nothing else.
func (s *Service) Invites(ctx context.Context, m Member) ([]Invite, error) {
	invites, err := s.st.ListInvites(ctx, m.Sub)
	if err != nil {
		return nil, err
	}
	out := make([]Invite, len(invites))
	for n, i := range invites {
		out[n] = Invite{Invite: i}
		if i.InviteeSub == "" {
			continue
		}
		u, err := s.p.GetUser(ctx, i.InviteeSub)
		switch {
		case errors.Is(err, idp.ErrNotFound):
			out[n].Invitee = &Invitee{Gone: true}
		case err != nil:
			slog.Warn("could not look up an invitee", "invite_id", i.ID, "error", err)
			out[n].Invitee = &Invitee{Unavailable: true}
		default:
			out[n].Invitee = &Invitee{Username: u.Username, DisplayName: u.DisplayName}
		}
	}
	return out, nil
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
	a.Username = strings.ToLower(strings.TrimSpace(a.Username))
	a.Email = strings.TrimSpace(a.Email)
	a.DisplayName = strings.TrimSpace(a.DisplayName)
	a.FirstName = strings.TrimSpace(a.FirstName)
	a.LastName = strings.TrimSpace(a.LastName)
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
	if n := utf8.RuneCountInString(a.FirstName); n == 0 || n > idp.MaxFirstName {
		return a, ErrInvalidFirst
	}
	if utf8.RuneCountInString(a.LastName) > idp.MaxLastName {
		return a, ErrInvalidLast
	}
	return a, nil
}

// available is checked at approval, just before the account is created.
func (s *Service) available(ctx context.Context, username, email string) error {
	if taken, err := s.p.UsernameTaken(ctx, username); err != nil {
		return err
	} else if taken {
		return ErrUsernameTaken
	}
	if taken, err := s.p.EmailTaken(ctx, email); err != nil {
		return err
	} else if taken {
		return ErrEmailTaken
	}
	return nil
}

// Apply records an application made with an invite. It returns true when
// the account was created at once, as with approval off.
func (s *Service) Apply(ctx context.Context, token string, in Applicant) (bool, error) {
	h, ok := TokenHash(token)
	if !ok {
		return false, store.ErrNotUsable
	}
	// A refused address is answered as an accepted one, so the invite must be
	// known usable first: a dead invite is never answered as accepted.
	if _, err := s.st.OpenInvite(ctx, h, s.now()); err != nil {
		return false, err
	}
	return s.submit(ctx, in, func(n store.NewApplication) (store.Application, error) {
		return s.st.Apply(ctx, h, s.now(), n)
	})
}

// Register records an application made through open registration, with no
// invite. Everything after the form is Apply's.
func (s *Service) Register(ctx context.Context, in Applicant) (bool, error) {
	return s.submit(ctx, in, func(n store.NewApplication) (store.Application, error) {
		return s.st.Register(ctx, s.now(), n)
	})
}

func (s *Service) submit(ctx context.Context, in Applicant, record func(store.NewApplication) (store.Application, error)) (bool, error) {
	a, err := in.check()
	if err != nil {
		return false, err
	}
	if taken, err := s.st.PendingUsernameTaken(ctx, a.Username); err != nil {
		return false, err
	} else if taken {
		return false, ErrUsernameTaken
	}
	// A provider outage defers the username check to approval.
	if taken, err := s.p.UsernameTaken(ctx, a.Username); err == nil && taken {
		return false, ErrUsernameTaken
	}
	refused, err := s.addressRefused(ctx, a.Email)
	if err != nil {
		return false, err
	}
	if refused != "" {
		// The page says what it says for an accepted application, so it never
		// tells anyone whether an address has an account or a ban. Only the
		// address's own inbox learns why.
		s.tellRefused(ctx, a.Email, refused)
		return !s.s.RequireApproval, nil
	}
	app, err := record(store.NewApplication{Username: a.Username, Email: a.Email, DisplayName: a.DisplayName,
		FirstName: a.FirstName, LastName: a.LastName, CoCVersion: s.coc.Version})
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
		// The application is recorded, so the applicant is told to wait and
		// the admins can retry from the pending list.
		slog.Warn("automatic approval failed; left for an admin", "application_id", app.ID, "error", err)
	}
	if err := s.m.Send(ctx, []string{app.Email}, "We received your request to join",
		"Thank you for agreeing to the code of conduct. An admin will review your request, and you will get another email when they have approved it.\n"); err != nil {
		slog.Warn("could not acknowledge an application", "error", err)
	}
	s.notifyAdmins(ctx, "An invite request is waiting", "Someone has asked to join and is waiting for approval.\n\n"+s.s.AccountURL+"/admin\n")
	return false, nil
}

// The reasons addressRefused gives.
const (
	refusedBanned = "banned"
	refusedInUse  = "in_use"
)

// addressRefused says why a signup with email cannot go ahead, or "" if it
// can. A ban is always checked; an account already using the address is
// checked when the provider answers, and otherwise again at approval.
func (s *Service) addressRefused(ctx context.Context, email string) (string, error) {
	banned, err := s.banned(ctx, email)
	if err != nil {
		return "", err
	}
	if banned {
		return refusedBanned, nil
	}
	if taken, err := s.p.EmailTaken(ctx, email); err == nil && taken {
		return refusedInUse, nil
	}
	return "", nil
}

func (s *Service) banned(ctx context.Context, email string) (bool, error) {
	return s.st.EmailBanned(ctx, emailhash.Sum(s.s.EmailHashKey, email))
}

// tellRefused mails a refused signup's address the reason, which no page
// shows.
func (s *Service) tellRefused(ctx context.Context, email, reason string) {
	subject, body := "About your request to join", "Your request to join can't be accepted.\n"
	if reason == refusedInUse {
		subject = "You already have an account"
		body = "Someone asked to join with this email address, which already has an account. If it was you, go to " +
			s.s.IDPURL + ", choose sign in with an email code, and enter the code from the email you get.\n\nIf it wasn't you, you can ignore this email.\n"
	}
	slog.Info("signup refused", "reason", reason)
	if err := s.m.Send(ctx, []string{email}, subject, body); err != nil {
		slog.Warn("could not tell a refused signup", "error", err)
	}
}

// notifyAdmins mails every member of the admin group.
func (s *Service) notifyAdmins(ctx context.Context, subject, body string) {
	admins, err := s.p.AdminGroupEmails(ctx, s.s.AdminGroup)
	if err == nil && len(admins) > 0 {
		err = s.m.Send(ctx, admins, subject, body)
	}
	if err != nil {
		slog.Warn("could not notify the admins", "subject", subject, "error", err)
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
		out = append(out, Pending{Application: a, Declines: d, Stale: a.State == "approving" && a.ClaimStale(s.now())})
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
	// The claim makes this approval the only one that may create the account
	// and the only one that may send its link; a second admin, a double
	// submit or a second retry is refused as busy.
	claimed, claim, err := s.st.ClaimApplication(ctx, app.ID, adminSub, s.now())
	if err != nil {
		return err
	}
	slog.Info("write", "action", "application.claim", "application_id", app.ID)
	userID, err := s.accountFor(ctx, adminSub, claimed, claim)
	if err != nil {
		// An account that exists but is not recorded keeps the claim: the
		// application stays approving, to be adopted by a later approval once
		// the claim is stale, or dismissed; never declined or swept.
		if !lostClaim(err) && !errors.Is(err, errAccountUnrecorded) {
			s.release(ctx, adminSub, app.ID, claim)
		}
		return err
	}
	if err := s.p.SendLoginLink(ctx, userID); err != nil {
		// The application is link_unsent now; releasing the claim leaves it
		// ready for a retry.
		s.release(ctx, adminSub, app.ID, claim)
		return fmt.Errorf("the account exists but its sign-in link was not sent; retry: %w", err)
	}
	// The link is sent: what follows records it, and must not be cut short by
	// the request going away, or the row would stay link_unsent and claimed.
	ctx = context.WithoutCancel(ctx)
	s.audit(ctx, adminSub, "idp.login_link", app.ID, "")
	slog.Info("write", "action", "idp.login_link", "application_id", app.ID)
	if err := s.st.CompleteApplication(ctx, app.ID, claim, adminSub); err != nil {
		return err
	}
	slog.Info("write", "action", "application.complete", "application_id", app.ID)
	return nil
}

// accountFor returns the provider account of a claimed application: the one
// already recorded for a retry, or one created (or adopted) now.
func (s *Service) accountFor(ctx context.Context, adminSub string, app store.Application, claim string) (string, error) {
	if app.State != "link_unsent" {
		return s.createAccount(ctx, adminSub, app, claim)
	}
	u, found, err := s.p.FindUser(ctx, app.Username)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("account %d was created but cannot be found at the identity provider", app.ID)
	}
	return u.ID, nil
}

// createAccount creates the claimed application's account, or adopts the one
// a previous attempt created, and records it as link_unsent.
func (s *Service) createAccount(ctx context.Context, adminSub string, app store.Application, claim string) (string, error) {
	// A previous attempt may have created the account and then failed to
	// record it. An account with this username and this applicant's email is
	// that account, not a clash.
	u, found, err := s.p.FindUser(ctx, app.Username)
	if err != nil {
		return "", err
	}
	userID := u.ID
	if !found || !strings.EqualFold(strings.TrimSpace(u.Email), app.Email) {
		// The address may have been banned since the application was made.
		if banned, err := s.banned(ctx, app.Email); err != nil {
			return "", err
		} else if banned {
			return "", ErrBanned
		}
		if err := s.available(ctx, app.Username, app.Email); err != nil {
			return "", err
		}
		u, err := s.p.CreateUser(ctx, idp.NewUser{Username: app.Username, Email: app.Email, DisplayName: app.DisplayName,
			FirstName: app.FirstName, LastName: app.LastName})
		if err != nil {
			return "", err
		}
		userID = u.ID
		s.audit(ctx, adminSub, "idp.user_create", app.ID, " idp="+userID)
		slog.Info("write", "action", "idp.user_create", "application_id", app.ID)
	}
	if err := s.st.MarkLinkUnsent(ctx, app.ID, claim, userID, adminSub); err != nil {
		if lostClaim(err) {
			// Another approval took the claim over while this one was at the
			// provider. It owns the application now; this one sends nothing.
			slog.Warn("approval lost its claim; stopping without sending a link", "application_id", app.ID)
			return "", store.ErrBusy
		}
		slog.Error("account exists at the identity provider but could not be recorded; the claim is kept",
			"application_id", app.ID, "error", err)
		return "", fmt.Errorf("%w: %w", errAccountUnrecorded, err)
	}
	slog.Info("write", "action", "application.link_unsent", "application_id", app.ID)
	return userID, nil
}

// errAccountUnrecorded is an account created (or adopted) at the provider that
// the store could not record.
var errAccountUnrecorded = errors.New("the account was created but could not be recorded; approve again in ten minutes, or dismiss")

// lostClaim reports a claim-guarded write that found the claim no longer held:
// taken over by another approval, or the application gone.
func lostClaim(err error) bool {
	return errors.Is(err, store.ErrBusy) || errors.Is(err, store.ErrNotFound)
}

// release returns a claim after a failed approval. If it cannot, the claim
// lapses on its own after the store's timeout.
func (s *Service) release(ctx context.Context, adminSub string, id int64, claim string) {
	if err := s.st.ReleaseClaim(context.WithoutCancel(ctx), id, claim, adminSub); err != nil {
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
	subject := "About your invitation"
	if app.InviterUsername == "" {
		subject = "About your request to join"
	}
	if err := s.m.Send(ctx, []string{app.Email}, subject,
		"Thank you for your interest. Your request to join was not accepted.\n"); err != nil {
		slog.Warn("could not tell the applicant of a decline", "error", err)
	}
	if app.InviterUsername == "" {
		return nil
	}
	inviter, found, err := s.p.FindUser(ctx, app.InviterUsername)
	if err != nil {
		slog.Warn("could not look up the inviter of a declined application", "application_id", id, "error", err)
	} else if found && inviter.Email != "" {
		if err := s.m.Send(ctx, []string{inviter.Email}, "An invite you sent was declined",
			"A request to join made with one of your invites was declined. Your invite slot is free again.\n"); err != nil {
			slog.Warn("could not tell the inviter of a decline", "error", err)
		}
	}
	return nil
}

// Dismiss stops tracking an application whose account was created but whose
// sign-in link was never sent. Anything at the identity provider is left for
// an admin to deal with there.
func (s *Service) Dismiss(ctx context.Context, admin Member, id int64) error {
	if !admin.Admin {
		return ErrForbidden
	}
	if err := s.st.DismissApplication(ctx, id, admin.Sub, s.now()); err != nil {
		return err
	}
	slog.Info("write", "action", "application.dismiss", "application_id", id)
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

package flow

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

var (
	ErrCodeWrong   = errors.New("that code is not right")
	ErrCodeExpired = errors.New("that code has expired or has been tried too often; ask for a new one")
	// ErrAddressRefused is an address in use by another account or matching a
	// current ban. It is told only to someone who has shown they hold the
	// address, so it reveals nothing to anyone else.
	ErrAddressRefused = errors.New("that address can't be used for your account; choose another")
	ErrSameAddress    = errors.New("that is already your address")
	ErrInvalidNames   = errors.New("display name must be 1 to 100 characters, and first and last name at most 50 each")
)

const (
	// CodeLifetime is how long an address change's code lasts.
	CodeLifetime = 15 * time.Minute
	// MaxCodeAttempts is how many codes may be tried against one change.
	MaxCodeAttempts = 5
)

// Profile is the member's own account as the provider holds it, and whether
// they are an operator, whose address and name the service does not change.
func (s *Service) Profile(ctx context.Context, m Member) (idp.User, bool, error) {
	u, err := s.p.GetUser(ctx, m.Sub)
	if err != nil {
		return idp.User{}, false, err
	}
	return u, s.IsOperator(u.Username), nil
}

// self is the member's own account, refused for an operator.
func (s *Service) self(ctx context.Context, m Member) (idp.User, error) {
	u, op, err := s.Profile(ctx, m)
	if err != nil {
		return idp.User{}, err
	}
	if op {
		return idp.User{}, ErrOperator
	}
	return u, nil
}

func validEmail(e string) (string, bool) {
	e = strings.TrimSpace(e)
	addr, err := mail.ParseAddress(e)
	return e, err == nil && addr.Address == e
}

// codeHash keys a code to the member and the address it is for, so a copy of
// the database cannot test codes, and a code cannot be moved to another
// address.
func (s *Service) codeHash(userID, email, code string) []byte {
	m := hmac.New(sha256.New, s.s.EmailHashKey)
	m.Write([]byte("email-change-code\x00" + userID + "\x00" + strings.ToLower(strings.TrimSpace(email)) + "\x00" + code))
	return m.Sum(nil)
}

func sixDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// RequestEmailChange mails a six-digit code to the new address, replacing any
// earlier request. Whether the address is free is checked once the code shows
// the member holds it.
func (s *Service) RequestEmailChange(ctx context.Context, m Member, email string) error {
	email, ok := validEmail(email)
	if !ok {
		return ErrInvalidEmail
	}
	u, err := s.self(ctx, m)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(u.Email), email) {
		return ErrSameAddress
	}
	code, err := sixDigits()
	if err != nil {
		return err
	}
	if err := s.st.StartEmailChange(ctx, m.Sub, email, s.codeHash(m.Sub, email, code), s.now().Add(CodeLifetime)); err != nil {
		return err
	}
	slog.Info("write", "action", "email_change.request", "user_id", m.Sub)
	return s.m.Send(ctx, []string{email}, "Your code to change your email address",
		"Enter this code on the page where you asked to change your email address:\n\n"+code+
			"\n\nIt expires in 15 minutes. If you didn't ask for it, you can ignore this email.\n")
}

// PendingEmailChange is the address a member's unexpired change is for.
func (s *Service) PendingEmailChange(ctx context.Context, m Member) (string, bool, error) {
	return s.st.PendingEmailChange(ctx, m.Sub, s.now())
}

// ConfirmEmailChange checks code against the member's pending change and, if
// it matches, sets the new address at the provider, marked verified, and tells
// the old address.
func (s *Service) ConfirmEmailChange(ctx context.Context, m Member, code string) error {
	u, err := s.self(ctx, m)
	if err != nil {
		return err
	}
	c, err := s.st.TakeEmailChangeAttempt(ctx, m.Sub, s.now(), MaxCodeAttempts)
	if errors.Is(err, store.ErrNotFound) {
		return ErrCodeExpired
	}
	if err != nil {
		return err
	}
	if !hmac.Equal(c.CodeHash, s.codeHash(m.Sub, c.NewEmail, strings.TrimSpace(code))) {
		return ErrCodeWrong
	}
	if err := s.st.FinishEmailChange(ctx, m.Sub); errors.Is(err, store.ErrNotFound) {
		return ErrCodeExpired
	} else if err != nil {
		return err
	}
	if refused, err := s.addressRefusedStrict(ctx, c.NewEmail); err != nil {
		return err
	} else if refused {
		return ErrAddressRefused
	}
	if err := s.p.SetEmail(ctx, m.Sub, c.NewEmail); err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	s.auditSubject(ctx, m.Sub, "idp.user_email", m.Sub)
	slog.Info("write", "action", "idp.user_email", "user_id", m.Sub)
	if old := strings.TrimSpace(u.Email); old != "" {
		if err := s.m.Send(ctx, []string{old}, "Your email address was changed",
			"The email address of your account "+u.Username+" was just changed, and this address no longer signs you in.\n\n"+
				"If you didn't do this, contact an admin of the community.\n"); err != nil {
			slog.Warn("could not tell the old address of a change", "user_id", m.Sub, "error", err)
		}
	}
	return nil
}

// addressRefusedStrict reports an address used by any account or matching a
// current ban; a provider that cannot answer is an error, never a pass.
func (s *Service) addressRefusedStrict(ctx context.Context, email string) (bool, error) {
	if banned, err := s.banned(ctx, email); err != nil || banned {
		return banned, err
	}
	return s.p.EmailTaken(ctx, email)
}

// SetName sets the member's display, first and last name. The username never
// changes.
func (s *Service) SetName(ctx context.Context, m Member, n idp.Name) error {
	n = idp.Name{DisplayName: strings.TrimSpace(n.DisplayName), FirstName: strings.TrimSpace(n.FirstName), LastName: strings.TrimSpace(n.LastName)}
	if d := utf8.RuneCountInString(n.DisplayName); d == 0 || d > idp.MaxDisplayName ||
		utf8.RuneCountInString(n.FirstName) > idp.MaxFirstName || utf8.RuneCountInString(n.LastName) > idp.MaxLastName {
		return ErrInvalidNames
	}
	if _, err := s.self(ctx, m); err != nil {
		return err
	}
	if err := s.p.SetName(ctx, m.Sub, n); err != nil {
		return err
	}
	s.auditSubject(context.WithoutCancel(ctx), m.Sub, "idp.user_name", m.Sub)
	slog.Info("write", "action", "idp.user_name", "user_id", m.Sub)
	return nil
}

// auditSubject records a provider write that has already happened; a failure
// to record it is logged rather than allowed to abort the flow.
func (s *Service) auditSubject(ctx context.Context, actorSub, action, subject string) {
	if err := s.st.Audit(ctx, actorSub, action, subject); err != nil {
		slog.Warn("could not audit a provider write", "action", action, "error", err)
	}
}

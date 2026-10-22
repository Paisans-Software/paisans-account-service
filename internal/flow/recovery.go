package flow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

// RecoveryLifetime is how long a recovery request waits for its second admin.
const RecoveryLifetime = 48 * time.Hour

// ErrNoSuchMember is a recovery for a username the provider does not have.
var ErrNoSuchMember = errors.New("no member has that username")

// RecoveryRow is one recovery request as the admins' page shows it.
type RecoveryRow struct {
	store.Recovery
	// Expired is an open request past its time, before the sweep marks it.
	Expired bool
}

// Live reports whether the request is waiting for its second admin.
func (r RecoveryRow) Live() bool { return r.State == "open" && !r.Expired }

// OpenRecovery opens a request to give a member who has lost both their
// passkey and their address a new address and a sign-in link. A different
// admin approves it, unless the opener is the sole operator-admin
// (SoleOperatorAdmin): theirs is recovered at once and returned approved.
func (s *Service) OpenRecovery(ctx context.Context, admin Member, username, email, why string) (store.Recovery, error) {
	if !admin.Admin {
		return store.Recovery{}, ErrForbidden
	}
	why, err := reason(why)
	if err != nil {
		return store.Recovery{}, err
	}
	email, ok := validEmail(email)
	if !ok {
		return store.Recovery{}, ErrInvalidEmail
	}
	u, found, err := s.p.FindUser(ctx, strings.TrimSpace(username))
	if err != nil {
		return store.Recovery{}, err
	}
	if !found {
		return store.Recovery{}, ErrNoSuchMember
	}
	if err := s.recoverable(admin, u); err != nil {
		return store.Recovery{}, err
	}
	now := s.now()
	req := store.Recovery{UserID: u.ID, Username: u.Username, NewEmail: email, Reason: why,
		OpenedBySub: admin.Sub, OpenedBy: admin.Username, OpenedAt: now, ExpiresAt: now.Add(RecoveryLifetime)}
	sole, err := s.SoleOperatorAdmin(ctx, admin)
	if err != nil {
		// Unsure there is no other admin, open the request for one: it
		// waits, and the opener can cancel it.
		slog.Warn("could not read the admin group; opening a request for a second admin", "error", err)
	}
	if sole {
		return s.recoverAlone(ctx, admin, req)
	}
	r, err := s.st.OpenRecovery(ctx, req)
	if err != nil {
		return store.Recovery{}, err
	}
	slog.Info("write", "action", "recovery.open", "recovery_id", r.ID, "user_id", u.ID)
	s.notifyAdmins(ctx, "A recovery needs a second admin",
		"An admin has asked to recover a member's account. Another admin must approve it within 48 hours.\n\n"+s.s.AccountURL+"/admin/recovery\n")
	return r, nil
}

// SoleOperatorAdmin reports whether admin is an operator and, at the provider
// now, the admin group's only member. Such an admin recovers a member without
// a second admin's approval: an operator holds the host and the provider's
// API key, so a second admin would protect nothing.
func (s *Service) SoleOperatorAdmin(ctx context.Context, admin Member) (bool, error) {
	if !admin.Admin || !s.IsOperator(admin.Username) {
		return false, nil
	}
	members, err := s.p.GroupMembers(ctx, s.s.AdminGroup)
	if err != nil {
		return false, err
	}
	return len(members) == 1 && members[0].ID == admin.Sub, nil
}

// recoverAlone is the sole operator-admin's recovery: the request is opened
// already claimed by its opener and completed by them, recorded as theirs
// alone. One that fails is abandoned, freeing the member.
func (s *Service) recoverAlone(ctx context.Context, admin Member, req store.Recovery) (store.Recovery, error) {
	r, err := s.st.OpenSoleOperatorRecovery(ctx, req)
	if err != nil {
		return store.Recovery{}, err
	}
	slog.Info("write", "action", "recovery.open_sole_operator", "recovery_id", r.ID, "user_id", r.UserID)
	if err := s.recover(ctx, admin, r); err != nil {
		if aerr := s.st.AbandonSoleOperatorRecovery(context.WithoutCancel(ctx), r.ID); aerr != nil {
			slog.Warn("could not abandon a recovery", "recovery_id", r.ID, "error", aerr)
		}
		return store.Recovery{}, err
	}
	if err := s.st.CompleteRecovery(context.WithoutCancel(ctx), r.ID, admin.Sub, admin.Username); err != nil {
		return store.Recovery{}, fmt.Errorf("the sign-in link was sent but the recovery was not recorded: %w", err)
	}
	slog.Info("write", "action", "recovery.approve_sole_operator", "recovery_id", r.ID, "user_id", r.UserID)
	r.State, r.ApprovedBySub, r.ApprovedBy = "approved", admin.Sub, admin.Username
	return r, nil
}

// recoverable refuses a recovery of an operator, of the admin themselves, or
// of a banned member.
func (s *Service) recoverable(admin Member, u idp.User) error {
	switch {
	case s.IsOperator(u.Username):
		return ErrOperator
	case u.ID == admin.Sub:
		return ErrSelf
	case u.Disabled:
		return ErrMemberBanned
	}
	return nil
}

// ApproveRecovery is the second admin's approval: it sets the new address at
// the provider, marked verified, and has the provider email a sign-in link to
// it. The admin who opened the request cannot approve it.
func (s *Service) ApproveRecovery(ctx context.Context, admin Member, id int64) error {
	if !admin.Admin {
		return ErrForbidden
	}
	r, err := s.st.Recovery(ctx, id)
	if err != nil {
		return err
	}
	if r.UserID == admin.Sub {
		return ErrSelf
	}
	r, err = s.st.ClaimRecovery(ctx, id, admin.Sub, s.now())
	if err != nil {
		return err
	}
	if err := s.recover(ctx, admin, r); err != nil {
		if rerr := s.st.ReleaseRecovery(context.WithoutCancel(ctx), id); rerr != nil {
			slog.Warn("could not release a recovery claim", "recovery_id", id, "error", rerr)
		}
		return err
	}
	if err := s.st.CompleteRecovery(context.WithoutCancel(ctx), id, admin.Sub, admin.Username); err != nil {
		return fmt.Errorf("the sign-in link was sent but the approval was not recorded: %w", err)
	}
	slog.Info("write", "action", "recovery.approve", "recovery_id", id, "user_id", r.UserID)
	return nil
}

func (s *Service) recover(ctx context.Context, admin Member, r store.Recovery) error {
	u, err := s.p.GetUser(ctx, r.UserID)
	if errors.Is(err, idp.ErrNotFound) {
		return ErrNoSuchMember
	}
	if err != nil {
		return err
	}
	if err := s.recoverable(admin, u); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(u.Email), r.NewEmail) {
		if refused, err := s.addressRefusedStrict(ctx, r.NewEmail); err != nil {
			return err
		} else if refused {
			return ErrAddressRefused
		}
		if err := s.p.SetEmail(ctx, r.UserID, r.NewEmail); err != nil {
			return err
		}
		s.auditSubject(context.WithoutCancel(ctx), admin.Sub, "idp.user_email", fmt.Sprintf("recovery=%d user=%s", r.ID, r.UserID))
	}
	if err := s.p.SendLoginLink(ctx, r.UserID); err != nil {
		return fmt.Errorf("the address is set but the sign-in link was not sent; approve again: %w", err)
	}
	s.auditSubject(context.WithoutCancel(ctx), admin.Sub, "idp.login_link", fmt.Sprintf("recovery=%d user=%s", r.ID, r.UserID))
	return nil
}

// CancelRecovery cancels an open request. Any admin may.
func (s *Service) CancelRecovery(ctx context.Context, admin Member, id int64) error {
	if !admin.Admin {
		return ErrForbidden
	}
	if err := s.st.CancelRecovery(ctx, id, admin.Sub); err != nil {
		return err
	}
	slog.Info("write", "action", "recovery.cancel", "recovery_id", id)
	return nil
}

// Recoveries lists recent requests, newest first.
func (s *Service) Recoveries(ctx context.Context, admin Member) ([]RecoveryRow, error) {
	if !admin.Admin {
		return nil, ErrForbidden
	}
	list, err := s.st.Recoveries(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]RecoveryRow, 0, len(list))
	for _, r := range list {
		out = append(out, RecoveryRow{Recovery: r, Expired: r.State == "open" && !r.ExpiresAt.After(now)})
	}
	return out, nil
}

package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrQuota         = errors.New("invite quota reached")
	ErrNotUsable     = errors.New("invite cannot be used")
	ErrUsernameTaken = errors.New("username taken")
	ErrNotFound      = errors.New("not found")
	// ErrDuplicateCreate is a create form submitted again: its nonce has
	// already made an invite, so nothing more is made.
	ErrDuplicateCreate = errors.New("invite already made from this form")
	// ErrBusy is an application another admin (or the automatic approval) has
	// claimed and is still deciding.
	ErrBusy = errors.New("application is being decided")
)

type Invite struct {
	ID                          int64
	InviterSub, InviterUsername string
	CreatedAt, ExpiresAt        time.Time
	State                       string
	// InviteeSub is the Pocket ID user a used invite made, or empty for an
	// invite not used, or used before the service recorded it.
	InviteeSub string
}

func audit(ctx context.Context, tx pgx.Tx, actor, action string, id int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit (actor_sub, action, subject) VALUES (NULLIF($1,''), $2, $3)`,
		actor, action, strconv.FormatInt(id, 10))
	return err
}

// CreateInvite makes an invite for the form that carried nonce. A nonce the
// member has already used makes nothing and returns ErrDuplicateCreate, before
// the quota is counted, so a resubmitted form is never reported as over quota.
func (s *Store) CreateInvite(ctx context.Context, tokenHash []byte, inviterSub, inviterUsername, nonce string, expires time.Time, quota int) (Invite, error) {
	var inv Invite
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialises one member's creates across every site, so the count
		// and the insert cannot interleave.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('invite-quota:' || $1, 0))`, inviterSub); err != nil {
			return err
		}
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM invites WHERE inviter_sub=$1 AND create_nonce=$2)`, inviterSub, nonce).Scan(&used); err != nil {
			return err
		}
		if used {
			return ErrDuplicateCreate
		}
		if quota > 0 {
			var live int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM invites WHERE inviter_sub=$1 AND state IN ('open','applied')`, inviterSub).Scan(&live); err != nil {
				return err
			}
			if live >= quota {
				return ErrQuota
			}
		}
		err := tx.QueryRow(ctx, `INSERT INTO invites (token_hash, inviter_sub, inviter_username, expires_at, state, create_nonce)
			VALUES ($1,$2,$3,$4,'open',$5) RETURNING id, inviter_sub, inviter_username, created_at, expires_at, state`,
			tokenHash, inviterSub, inviterUsername, expires, nonce).
			Scan(&inv.ID, &inv.InviterSub, &inv.InviterUsername, &inv.CreatedAt, &inv.ExpiresAt, &inv.State)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "invites_create_nonce" {
			return ErrDuplicateCreate
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, inviterSub, "invite.create", inv.ID)
	})
	return inv, err
}

func (s *Store) ListInvites(ctx context.Context, inviterSub string) ([]Invite, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, inviter_sub, inviter_username, created_at, expires_at, state, COALESCE(invitee_sub,'')
		FROM invites WHERE inviter_sub=$1 ORDER BY created_at DESC, id DESC`, inviterSub)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invite, error) {
		var i Invite
		err := r.Scan(&i.ID, &i.InviterSub, &i.InviterUsername, &i.CreatedAt, &i.ExpiresAt, &i.State, &i.InviteeSub)
		return i, err
	})
}

func (s *Store) RevokeInvite(ctx context.Context, id int64, inviterSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE invites SET state='revoked' WHERE id=$1 AND inviter_sub=$2 AND state='open'`, id, inviterSub)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, inviterSub, "invite.revoke", id)
	})
}

func (s *Store) OpenInvite(ctx context.Context, tokenHash []byte, now time.Time) (Invite, error) {
	var i Invite
	err := s.pool.QueryRow(ctx, `SELECT id, inviter_sub, inviter_username, created_at, expires_at, state
		FROM invites WHERE token_hash=$1 AND state='open' AND expires_at > $2`, tokenHash, now).
		Scan(&i.ID, &i.InviterSub, &i.InviterUsername, &i.CreatedAt, &i.ExpiresAt, &i.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrNotUsable
	}
	return i, err
}

package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrSameAdmin is a recovery request approved by the admin who opened it.
var ErrSameAdmin = errors.New("the admin who opened a recovery cannot approve it")

type EmailChange struct {
	UserID, NewEmail string
	CodeHash         []byte
	ExpiresAt        time.Time
	Attempts         int
}

// StartEmailChange records a member's pending address change, replacing any
// earlier one, with no attempts made.
func (s *Store) StartEmailChange(ctx context.Context, userID, newEmail string, codeHash []byte, expires time.Time) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO email_changes (idp_user_id, new_email, code_hash, expires_at)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (idp_user_id) DO UPDATE SET new_email=$2, code_hash=$3, expires_at=$4, attempts=0, created_at=now()`,
			userID, newEmail, codeHash, expires); err != nil {
			return err
		}
		return auditTx(ctx, tx, userID, "email_change.request", userID)
	})
}

// TakeEmailChangeAttempt counts one attempt at a member's pending change and
// returns it. There is nothing to attempt, ErrNotFound, when no change is
// pending, it has expired, or max attempts have been made.
func (s *Store) TakeEmailChangeAttempt(ctx context.Context, userID string, now time.Time, max int) (EmailChange, error) {
	var c EmailChange
	err := s.pool.QueryRow(ctx, `UPDATE email_changes SET attempts=attempts+1
		WHERE idp_user_id=$1 AND expires_at > $2 AND attempts < $3
		RETURNING idp_user_id, new_email, code_hash, expires_at, attempts`, userID, now, max).
		Scan(&c.UserID, &c.NewEmail, &c.CodeHash, &c.ExpiresAt, &c.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailChange{}, ErrNotFound
	}
	return c, err
}

// PendingEmailChange reports whether a member has a change waiting for its
// code, and the address it is for.
func (s *Store) PendingEmailChange(ctx context.Context, userID string, now time.Time) (string, bool, error) {
	var addr string
	err := s.pool.QueryRow(ctx, `SELECT new_email FROM email_changes WHERE idp_user_id=$1 AND expires_at > $2`, userID, now).Scan(&addr)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return addr, err == nil, err
}

// FinishEmailChange consumes a member's pending change once its code matched.
// Only one caller can: a second is ErrNotFound.
func (s *Store) FinishEmailChange(ctx context.Context, userID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM email_changes WHERE idp_user_id=$1`, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return auditTx(ctx, tx, userID, "email_change.verified", userID)
	})
}

// DropEmailChange discards a member's pending change, as after too many
// wrong codes.
func (s *Store) DropEmailChange(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM email_changes WHERE idp_user_id=$1`, userID)
	return err
}

type Recovery struct {
	ID                         int64
	UserID, Username, NewEmail string
	Reason                     string
	OpenedBySub, OpenedBy      string
	ApprovedBySub, ApprovedBy  string
	State                      string
	OpenedAt, ExpiresAt        time.Time
}

const recoveryColumns = `id, idp_user_id, username, new_email, reason, opened_by_sub, opened_by_username,
	COALESCE(approved_by_sub,''), COALESCE(approved_by_username,''), state, opened_at, expires_at`

func scanRecovery(r pgx.Row) (Recovery, error) {
	var x Recovery
	err := r.Scan(&x.ID, &x.UserID, &x.Username, &x.NewEmail, &x.Reason, &x.OpenedBySub, &x.OpenedBy,
		&x.ApprovedBySub, &x.ApprovedBy, &x.State, &x.OpenedAt, &x.ExpiresAt)
	return x, err
}

func recoverySubject(r Recovery) string { return itoa(r.ID) + " user=" + r.UserID }

// OpenRecovery records a recovery request. A user who already has one open is
// ErrBusy.
func (s *Store) OpenRecovery(ctx context.Context, r Recovery) (Recovery, error) {
	var out Recovery
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanRecovery(tx.QueryRow(ctx, `INSERT INTO recovery_requests
			(idp_user_id, username, new_email, reason, opened_by_sub, opened_by_username, opened_at, expires_at, state)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'open') RETURNING `+recoveryColumns,
			r.UserID, r.Username, r.NewEmail, r.Reason, r.OpenedBySub, r.OpenedBy, r.OpenedAt, r.ExpiresAt))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "recovery_live_user" {
			return ErrBusy
		}
		if err != nil {
			return err
		}
		return auditTx(ctx, tx, r.OpenedBySub, "recovery.open", recoverySubject(out))
	})
	return out, err
}

// ClaimRecovery takes an open, unexpired request for one approval by an admin
// other than its opener. Its opener is ErrSameAdmin; a request being approved
// by another is ErrBusy until that claim is ten minutes old; anything else
// (expired, cancelled, approved, gone) is ErrNotFound.
func (s *Store) ClaimRecovery(ctx context.Context, id int64, approverSub string, now time.Time) (Recovery, error) {
	var r Recovery
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		r, err = scanRecovery(tx.QueryRow(ctx, `UPDATE recovery_requests SET state='approving', claimed_at=$3
			WHERE id=$1 AND opened_by_sub<>$2 AND expires_at > $3
			AND (state='open' OR (state='approving' AND claimed_at < $4))
			RETURNING `+recoveryColumns, id, approverSub, now, now.Add(-claimTimeout)))
		if errors.Is(err, pgx.ErrNoRows) {
			var opener, state string
			var expires time.Time
			err := tx.QueryRow(ctx, `SELECT opened_by_sub, state, expires_at FROM recovery_requests WHERE id=$1`, id).Scan(&opener, &state, &expires)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				return ErrNotFound
			case err != nil:
				return err
			case opener == approverSub:
				return ErrSameAdmin
			case state == "approving" && expires.After(now):
				return ErrBusy
			}
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return auditTx(ctx, tx, approverSub, "recovery.claim", recoverySubject(r))
	})
	return r, err
}

// ReleaseRecovery returns a claimed request to open after a failed approval.
func (s *Store) ReleaseRecovery(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE recovery_requests SET state='open', claimed_at=NULL WHERE id=$1 AND state='approving'`, id)
	return err
}

// CompleteRecovery records a claimed request as approved.
func (s *Store) CompleteRecovery(ctx context.Context, id int64, approverSub, approver string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := scanRecovery(tx.QueryRow(ctx, `UPDATE recovery_requests
			SET state='approved', approved_by_sub=$2, approved_by_username=$3, closed_at=now(), closed_by_sub=$2
			WHERE id=$1 AND state='approving' RETURNING `+recoveryColumns, id, approverSub, approver))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return auditTx(ctx, tx, approverSub, "recovery.approve", recoverySubject(r))
	})
}

// CancelRecovery cancels an open request. One that is being approved, or is
// no longer open, is ErrNotFound.
func (s *Store) CancelRecovery(ctx context.Context, id int64, actorSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := scanRecovery(tx.QueryRow(ctx, `UPDATE recovery_requests SET state='cancelled', closed_at=now(), closed_by_sub=$2
			WHERE id=$1 AND state='open' RETURNING `+recoveryColumns, id, actorSub))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return auditTx(ctx, tx, actorSub, "recovery.cancel", recoverySubject(r))
	})
}

// Recovery returns one request by ID.
func (s *Store) Recovery(ctx context.Context, id int64) (Recovery, error) {
	r, err := scanRecovery(s.pool.QueryRow(ctx, `SELECT `+recoveryColumns+` FROM recovery_requests WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Recovery{}, ErrNotFound
	}
	return r, err
}

// Recoveries lists the most recent requests, newest first.
func (s *Store) Recoveries(ctx context.Context) ([]Recovery, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+recoveryColumns+` FROM recovery_requests ORDER BY opened_at DESC, id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Recovery, error) { return scanRecovery(r) })
}

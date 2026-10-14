package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Application struct {
	ID, InviteID                             int64
	Username, Email, DisplayName, CoCVersion string
	AgreedAt                                 time.Time
	State, IDPUserID                         string
	InviterSub, InviterUsername              string
	// ClaimedAt is when the current claim was taken, or zero if none is held.
	ClaimedAt time.Time
}

type NewApplication struct{ Username, Email, DisplayName, CoCVersion string }

// appColumns are read FROM appFrom. A registration has no invite: its
// InviteID is 0 and its inviter is empty.
const appColumns = `a.id, COALESCE(a.invite_id,0), a.username, a.email, a.display_name, a.coc_version, a.agreed_at,
	a.state, COALESCE(a.idp_user_id,''), COALESCE(i.inviter_sub,''), COALESCE(i.inviter_username,''), a.claimed_at`

const appFrom = ` FROM applications a LEFT JOIN invites i ON i.id=a.invite_id`

func scanApp(r pgx.Row) (Application, error) {
	var a Application
	var claimed *time.Time
	err := r.Scan(&a.ID, &a.InviteID, &a.Username, &a.Email, &a.DisplayName, &a.CoCVersion, &a.AgreedAt,
		&a.State, &a.IDPUserID, &a.InviterSub, &a.InviterUsername, &claimed)
	if claimed != nil {
		a.ClaimedAt = *claimed
	}
	return a, err
}

func (s *Store) Apply(ctx context.Context, tokenHash []byte, now time.Time, n NewApplication) (Application, error) {
	var a Application
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID int64
		err := tx.QueryRow(ctx, `UPDATE invites SET state='applied'
			WHERE token_hash=$1 AND state='open' AND expires_at > $2 RETURNING id`, tokenHash, now).Scan(&inviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotUsable
		}
		if err != nil {
			return err
		}
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO applications (invite_id, username, email, display_name, coc_version, agreed_at, state)
			VALUES ($1,$2,$3,$4,$5,$6,'pending') RETURNING id`,
			inviteID, n.Username, n.Email, n.DisplayName, n.CoCVersion, now).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "applications_username" {
			return ErrUsernameTaken // the whole transaction rolls back, so the invite stays open
		}
		if err != nil {
			return err
		}
		if err := audit(ctx, tx, "", "application.create", id); err != nil {
			return err
		}
		a, err = scanApp(tx.QueryRow(ctx, `SELECT `+appColumns+appFrom+` WHERE a.id=$1`, id))
		return err
	})
	return a, err
}

// Register records an application made through open registration: the same
// as Apply, with no invite.
func (s *Store) Register(ctx context.Context, now time.Time, n NewApplication) (Application, error) {
	var a Application
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO applications (username, email, display_name, coc_version, agreed_at, state)
			VALUES ($1,$2,$3,$4,$5,'pending') RETURNING id`,
			n.Username, n.Email, n.DisplayName, n.CoCVersion, now).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "applications_username" {
			return ErrUsernameTaken
		}
		if err != nil {
			return err
		}
		if err := audit(ctx, tx, "", "application.create", id); err != nil {
			return err
		}
		a, err = scanApp(tx.QueryRow(ctx, `SELECT `+appColumns+appFrom+` WHERE a.id=$1`, id))
		return err
	})
	return a, err
}

func (s *Store) PendingUsernameTaken(ctx context.Context, username string) (bool, error) {
	var taken bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM applications WHERE lower(username)=lower($1))`, username).Scan(&taken)
	return taken, err
}

func (s *Store) Application(ctx context.Context, id int64) (Application, error) {
	a, err := scanApp(s.pool.QueryRow(ctx, `SELECT `+appColumns+appFrom+` WHERE a.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Application{}, ErrNotFound
	}
	return a, err
}

func (s *Store) PendingApplications(ctx context.Context) ([]Application, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+appColumns+appFrom+` ORDER BY a.agreed_at, a.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Application, error) { return scanApp(r) })
}

// ClaimTimeout is how long a claim holds before another approval may take it
// over, as after a crash between claiming and recording the account.
const ClaimTimeout = 10 * time.Minute

const claimTimeout = ClaimTimeout

// ClaimStale reports whether a's claim has outlived the takeover window at now.
func (a Application) ClaimStale(now time.Time) bool {
	return !a.ClaimedAt.IsZero() && a.ClaimedAt.Before(now.Add(-ClaimTimeout))
}

// ClaimApplication claims an application for one approval, so that only one
// creates its account and only one sends its sign-in link. A pending
// application moves to approving; a link_unsent one keeps its state and is
// claimed for a retry. Either may be claimed while it holds no claim or its
// claim has gone stale. It returns the claim token that MarkLinkUnsent,
// ReleaseClaim and CompleteApplication require; a takeover issues a new token,
// so the approval it replaced can write nothing more. It returns ErrBusy while
// another claim holds and ErrNotFound when there is no application to claim.
func (s *Store) ClaimApplication(ctx context.Context, id int64, adminSub string, now time.Time) (Application, string, error) {
	var a Application
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Application{}, "", err
	}
	claim := base64.RawURLEncoding.EncodeToString(raw)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE applications
			SET state=CASE WHEN state='pending' THEN 'approving' ELSE state END, claimed_at=$2, claim_token=$4
			WHERE id=$1 AND (state='pending'
				OR (state IN ('approving','link_unsent') AND (claim_token IS NULL OR claimed_at < $3)))`,
			id, now, now.Add(-claimTimeout), claim)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFoundOrBusy(ctx, tx, id)
		}
		if err := audit(ctx, tx, adminSub, "application.claim", id); err != nil {
			return err
		}
		a, err = scanApp(tx.QueryRow(ctx, `SELECT `+appColumns+appFrom+` WHERE a.id=$1`, id))
		return err
	})
	if err != nil {
		return Application{}, "", err
	}
	return a, claim, nil
}

// ReleaseClaim gives up a claim after a failed approval: an approving
// application returns to pending, a link_unsent one stays link_unsent with no
// claim, ready for a retry. Only the holder of claim may.
func (s *Store) ReleaseClaim(ctx context.Context, id int64, claim, adminSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE applications
			SET state=CASE WHEN state='approving' THEN 'pending' ELSE state END, claimed_at=NULL, claim_token=NULL
			WHERE id=$1 AND state IN ('approving','link_unsent') AND claim_token=$2`, id, claim)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return claimLost(ctx, tx, id)
		}
		return audit(ctx, tx, adminSub, "application.release", id)
	})
}

// notFoundOrBusy explains a statement that matched no application: ErrBusy if
// it holds a claim, ErrNotFound otherwise.
func notFoundOrBusy(ctx context.Context, tx pgx.Tx, id int64) error {
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM applications WHERE id=$1 AND claim_token IS NOT NULL)`, id).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrBusy
	}
	return ErrNotFound
}

// claimLost explains a claim-guarded write that matched nothing: ErrBusy if
// the application still exists (its claim is not, or no longer, this one),
// ErrNotFound if it is gone.
func claimLost(ctx context.Context, tx pgx.Tx, id int64) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM applications WHERE id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrBusy
	}
	return ErrNotFound
}

// MarkLinkUnsent records the account created for an application its caller
// holds claim on.
func (s *Store) MarkLinkUnsent(ctx context.Context, id int64, claim, idpUserID, adminSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE applications SET state='link_unsent', idp_user_id=$2, decided_by_sub=NULLIF($3,''), decided_at=now()
			WHERE id=$1 AND state='approving' AND claim_token=$4`, id, idpUserID, adminSub, claim)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return claimLost(ctx, tx, id)
		}
		return audit(ctx, tx, adminSub, "application.link_unsent", id)
	})
}

// CompleteApplication finishes an application whose sign-in link was sent by
// the holder of claim.
func (s *Store) CompleteApplication(ctx context.Context, id int64, claim, adminSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID *int64
		var username string
		err := tx.QueryRow(ctx, `DELETE FROM applications
			WHERE id=$1 AND state IN ('approving','link_unsent') AND claim_token=$2
			RETURNING invite_id, username`, id, claim).Scan(&inviteID, &username)
		if errors.Is(err, pgx.ErrNoRows) {
			return claimLost(ctx, tx, id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invites SET state='used' WHERE id=$1`, inviteID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO introductions (username, inviter_sub, inviter_username)
			SELECT $1, inviter_sub, inviter_username FROM invites WHERE id=$2`, username, inviteID); err != nil {
			return err
		}
		return audit(ctx, tx, adminSub, "application.complete", id)
	})
}

// DismissApplication stops tracking an application whose account may exist
// but whose sign-in link was never sent: one in link_unsent, or one in
// approving, that holds no live claim. It deletes the application and expires
// its invite, which frees the inviter's slot. A pending application is
// ErrNotFound (it is declined, not dismissed); a live claim is ErrBusy.
func (s *Store) DismissApplication(ctx context.Context, id int64, adminSub string, now time.Time) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID *int64
		err := tx.QueryRow(ctx, `DELETE FROM applications
			WHERE id=$1 AND state IN ('link_unsent','approving') AND (claim_token IS NULL OR claimed_at < $2)
			RETURNING invite_id`, id, now.Add(-claimTimeout)).Scan(&inviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFoundOrBusy(ctx, tx, id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invites SET state='expired' WHERE id=$1`, inviteID); err != nil {
			return err
		}
		return audit(ctx, tx, adminSub, "application.dismiss", id)
	})
}

func (s *Store) DeclineApplication(ctx context.Context, id int64, adminSub string, emailHMAC []byte) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID *int64
		err := tx.QueryRow(ctx, `DELETE FROM applications WHERE id=$1 AND state='pending' RETURNING invite_id`, id).Scan(&inviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFoundOrBusy(ctx, tx, id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invites SET state='declined' WHERE id=$1`, inviteID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO declines (email_hmac) VALUES ($1)`, emailHMAC); err != nil {
			return err
		}
		return audit(ctx, tx, adminSub, "application.decline", id)
	})
}

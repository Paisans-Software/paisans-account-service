package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const sweepLock = 0x696e7669746502

type SweepResult struct {
	InvitesExpired, ApplicationsExpired, DeclinesDeleted int64
	RecoveriesExpired, EmailChangesDeleted               int64
	Ran                                                  bool
}

func (s *Store) Sweep(ctx context.Context, now time.Time, applicationWait, declineRetention time.Duration) (SweepResult, error) {
	var r SweepResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, sweepLock).Scan(&r.Ran); err != nil || !r.Ran {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE invites SET state='expired' WHERE state='open' AND expires_at <= $1`, now)
		if err != nil {
			return err
		}
		r.InvitesExpired = tag.RowsAffected()
		// A registration has no invite to expire, so the count is of the
		// applications themselves.
		err = tx.QueryRow(ctx, `WITH gone AS (
			DELETE FROM applications WHERE state='pending' AND agreed_at <= $1 RETURNING invite_id),
			spent AS (UPDATE invites SET state='expired' WHERE id IN (SELECT invite_id FROM gone))
			SELECT count(*) FROM gone`, now.Add(-applicationWait)).Scan(&r.ApplicationsExpired)
		if err != nil {
			return err
		}
		tag, err = tx.Exec(ctx, `DELETE FROM declines WHERE declined_at <= $1`, now.Add(-declineRetention))
		if err != nil {
			return err
		}
		r.DeclinesDeleted = tag.RowsAffected()
		// A sole operator's request is never open: one its recovery left
		// claimed, as after a crash, expires once the claim is stale.
		tag, err = tx.Exec(ctx, `UPDATE recovery_requests SET state='expired', closed_at=$1
			WHERE (state='open' AND expires_at <= $1)
			OR (state='approving' AND sole_operator AND claimed_at < $2)`, now, now.Add(-claimTimeout))
		if err != nil {
			return err
		}
		r.RecoveriesExpired = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `DELETE FROM email_changes WHERE expires_at <= $1`, now)
		if err != nil {
			return err
		}
		r.EmailChangesDeleted = tag.RowsAffected()
		if r.InvitesExpired+r.ApplicationsExpired+r.DeclinesDeleted+r.RecoveriesExpired+r.EmailChangesDeleted > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO audit (action, subject) VALUES ('sweep', $1)`,
				fmtCounts(r))
		}
		return err
	})
	return r, err
}

func fmtCounts(r SweepResult) string {
	return "invites=" + itoa(r.InvitesExpired) + " applications=" + itoa(r.ApplicationsExpired) + " declines=" + itoa(r.DeclinesDeleted) +
		" recoveries=" + itoa(r.RecoveriesExpired) + " email_changes=" + itoa(r.EmailChangesDeleted)
}

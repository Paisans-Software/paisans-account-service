package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const sweepLock = 0x696e7669746502

type SweepResult struct {
	InvitesExpired, ApplicationsExpired, DeclinesDeleted int64
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
		tag, err = tx.Exec(ctx, `WITH gone AS (
			DELETE FROM applications WHERE state='pending' AND agreed_at <= $1 RETURNING invite_id)
			UPDATE invites SET state='expired' WHERE id IN (SELECT invite_id FROM gone)`, now.Add(-applicationWait))
		if err != nil {
			return err
		}
		r.ApplicationsExpired = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `DELETE FROM declines WHERE declined_at <= $1`, now.Add(-declineRetention))
		if err != nil {
			return err
		}
		r.DeclinesDeleted = tag.RowsAffected()
		if r.InvitesExpired+r.ApplicationsExpired+r.DeclinesDeleted > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO audit (action, subject) VALUES ('sweep', $1)`,
				fmtCounts(r))
		}
		return err
	})
	return r, err
}

func fmtCounts(r SweepResult) string {
	return "invites=" + itoa(r.InvitesExpired) + " applications=" + itoa(r.ApplicationsExpired) + " declines=" + itoa(r.DeclinesDeleted)
}

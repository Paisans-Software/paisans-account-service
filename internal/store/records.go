package store

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type DeclineHistory struct {
	Count int
	Last  time.Time
}

func (s *Store) DeclineHistory(ctx context.Context, emailHMAC []byte) (DeclineHistory, error) {
	var d DeclineHistory
	var last *time.Time
	err := s.pool.QueryRow(ctx, `SELECT count(*), max(declined_at) FROM declines WHERE email_hmac=$1`, emailHMAC).Scan(&d.Count, &last)
	if last != nil {
		d.Last = *last
	}
	return d, err
}

func (s *Store) Audit(ctx context.Context, actorSub, action, subject string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit (actor_sub, action, subject) VALUES (NULLIF($1,''), $2, $3)`, actorSub, action, subject)
	return err
}

// AuditSubjects returns the subjects of the audit rows for action, oldest first.
func (s *Store) AuditSubjects(ctx context.Context, action string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT subject FROM audit WHERE action=$1 ORDER BY id`, action)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

package store

import (
	"context"
	"time"
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

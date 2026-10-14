package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrAlreadyBanned is a ban of a user who has a current one.
var ErrAlreadyBanned = errors.New("already banned")

type Ban struct {
	ID                    int64
	UserID, Username      string
	Reason                string
	BannedBySub, BannedBy string
	BannedAt              time.Time
	LiftedBy              string
	// LiftedAt is zero while the ban is current.
	LiftedAt time.Time
}

func (b Ban) Current() bool { return b.LiftedAt.IsZero() }

const banColumns = `id, idp_user_id, username, reason, banned_by_sub, banned_by_username, banned_at,
	COALESCE(lifted_by_username,''), lifted_at`

func scanBan(r pgx.Row) (Ban, error) {
	var b Ban
	var lifted *time.Time
	err := r.Scan(&b.ID, &b.UserID, &b.Username, &b.Reason, &b.BannedBySub, &b.BannedBy, &b.BannedAt, &b.LiftedBy, &lifted)
	if lifted != nil {
		b.LiftedAt = *lifted
	}
	return b, err
}

// auditTx records action on subject inside tx.
func auditTx(ctx context.Context, tx pgx.Tx, actor, action, subject string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit (actor_sub, action, subject) VALUES (NULLIF($1,''), $2, $3)`, actor, action, subject)
	return err
}

// RecordBan records a ban already made at the identity provider, and its
// audit entry, in one transaction. A user with a current ban is
// ErrAlreadyBanned. emailHMAC may be nil for a user with no address.
func (s *Store) RecordBan(ctx context.Context, userID, username string, emailHMAC []byte, reason, actorSub, actor string) (Ban, error) {
	var b Ban
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		b, err = scanBan(tx.QueryRow(ctx, `INSERT INTO bans (idp_user_id, username, email_hmac, reason, banned_by_sub, banned_by_username)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+banColumns, userID, username, emailHMAC, reason, actorSub, actor))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "bans_current_user" {
			return ErrAlreadyBanned
		}
		if err != nil {
			return err
		}
		return auditTx(ctx, tx, actorSub, "ban.create", itoa(b.ID)+" user="+userID)
	})
	return b, err
}

// LiftBan marks a current ban lifted. A ban that is not current, or does not
// exist, is ErrNotFound.
func (s *Store) LiftBan(ctx context.Context, id int64, actorSub, actor string) (Ban, error) {
	var b Ban
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		b, err = scanBan(tx.QueryRow(ctx, `UPDATE bans SET lifted_by_sub=$2, lifted_by_username=$3, lifted_at=now()
			WHERE id=$1 AND lifted_at IS NULL RETURNING `+banColumns, id, actorSub, actor))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return auditTx(ctx, tx, actorSub, "ban.lift", itoa(b.ID)+" user="+b.UserID)
	})
	return b, err
}

// Ban returns one ban by ID.
func (s *Store) Ban(ctx context.Context, id int64) (Ban, error) {
	b, err := scanBan(s.pool.QueryRow(ctx, `SELECT `+banColumns+` FROM bans WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Ban{}, ErrNotFound
	}
	return b, err
}

// CurrentBans maps each currently banned user's ID to their ban.
func (s *Store) CurrentBans(ctx context.Context) (map[string]Ban, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+banColumns+` FROM bans WHERE lifted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Ban, error) { return scanBan(r) })
	if err != nil {
		return nil, err
	}
	out := make(map[string]Ban, len(list))
	for _, b := range list {
		out[b.UserID] = b
	}
	return out, nil
}

// EmailBanned reports whether a current ban carries emailHMAC.
func (s *Store) EmailBanned(ctx context.Context, emailHMAC []byte) (bool, error) {
	var banned bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bans WHERE email_hmac=$1 AND lifted_at IS NULL)`, emailHMAC).Scan(&banned)
	return banned, err
}

// Bans lists every ban, current and lifted, newest first.
func (s *Store) Bans(ctx context.Context) ([]Ban, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+banColumns+` FROM bans ORDER BY banned_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Ban, error) { return scanBan(r) })
}

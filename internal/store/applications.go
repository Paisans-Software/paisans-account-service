package store

import (
	"context"
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
}

type NewApplication struct{ Username, Email, DisplayName, CoCVersion string }

const appColumns = `a.id, a.invite_id, a.username, a.email, a.display_name, a.coc_version, a.agreed_at,
	a.state, COALESCE(a.idp_user_id,''), i.inviter_sub, i.inviter_username`

func scanApp(r pgx.Row) (Application, error) {
	var a Application
	err := r.Scan(&a.ID, &a.InviteID, &a.Username, &a.Email, &a.DisplayName, &a.CoCVersion, &a.AgreedAt,
		&a.State, &a.IDPUserID, &a.InviterSub, &a.InviterUsername)
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
		a, err = scanApp(tx.QueryRow(ctx, `SELECT `+appColumns+` FROM applications a JOIN invites i ON i.id=a.invite_id WHERE a.id=$1`, id))
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
	a, err := scanApp(s.pool.QueryRow(ctx, `SELECT `+appColumns+` FROM applications a JOIN invites i ON i.id=a.invite_id WHERE a.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Application{}, ErrNotFound
	}
	return a, err
}

func (s *Store) PendingApplications(ctx context.Context) ([]Application, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+appColumns+` FROM applications a JOIN invites i ON i.id=a.invite_id ORDER BY a.agreed_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Application, error) { return scanApp(r) })
}

func (s *Store) MarkLinkUnsent(ctx context.Context, id int64, idpUserID, adminSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE applications SET state='link_unsent', idp_user_id=$2, decided_by_sub=NULLIF($3,''), decided_at=now() WHERE id=$1`, id, idpUserID, adminSub)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, adminSub, "application.link_unsent", id)
	})
}

func (s *Store) CompleteApplication(ctx context.Context, id int64, adminSub string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID int64
		var username string
		err := tx.QueryRow(ctx, `DELETE FROM applications WHERE id=$1 RETURNING invite_id, username`, id).Scan(&inviteID, &username)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
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

func (s *Store) DeclineApplication(ctx context.Context, id int64, adminSub string, emailHMAC []byte) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID int64
		err := tx.QueryRow(ctx, `DELETE FROM applications WHERE id=$1 AND state='pending' RETURNING invite_id`, id).Scan(&inviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
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

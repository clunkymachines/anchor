package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"anchor/internal/domain"
)

func (s *Store) CreateSession(ctx context.Context, session domain.Session) error {
	switch s.dialect {
	case DialectSQLite:
		_, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`,
			session.ID,
			session.UserID,
			session.ExpiresAt,
		)
		return err
	case DialectPostgres, DialectPostgreSQL:
		_, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3)`,
			session.ID,
			session.UserID,
			session.ExpiresAt,
		)
		return err
	default:
		return fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) UserBySession(ctx context.Context, sessionID string, now time.Time) (domain.User, error) {
	query := `
		SELECT u.id, u.email, u.name, u.password_hash, u.is_admin
		FROM sessions s
		JOIN app_users u ON u.id = s.user_id
		WHERE s.id = ? AND s.expires_at > ?
	`
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT u.id, u.email, u.name, u.password_hash, u.is_admin
			FROM sessions s
			JOIN app_users u ON u.id = s.user_id
			WHERE s.id = $1 AND s.expires_at > $2
		`
	}

	var user domain.User
	err := s.readDB.QueryRowContext(ctx, query, sessionID, formatTime(now)).Scan(
		&user.ID,
		&user.Email,
		&user.Name,
		&user.PasswordHash,
		&user.IsAdmin,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	return user, err
}

func (s *Store) DeleteSession(ctx context.Context, sessionID string) error {
	query := `DELETE FROM sessions WHERE id = ?`
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `DELETE FROM sessions WHERE id = $1`
	}

	_, err := s.writeDB.ExecContext(ctx, query, sessionID)
	return err
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

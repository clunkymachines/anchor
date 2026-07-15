package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"anchor/internal/domain"
)

// ErrNotFound reports that no row matched the requested identity and scope.
var ErrNotFound = errors.New("not found")

// ErrConflict reports that a uniqueness or ownership constraint was violated.
var ErrConflict = errors.New("conflict")

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var count int
	if err := s.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM app_users").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) CreateUser(ctx context.Context, user domain.User) (int64, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	user.Email = strings.TrimSpace(strings.ToLower(user.Email))
	userID, err := s.createUserTx(ctx, tx, user)
	if err != nil {
		if isUniqueConstraintError(err) {
			return 0, ErrConflict
		}
		return 0, err
	}

	organisationID, err := s.createPersonalOrganisationTx(ctx, tx, user.Name)
	if err != nil {
		return 0, err
	}
	if err := s.addUserToOrganisationTx(ctx, tx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
		Role:           OrganisationRoleAdmin,
	}); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return userID, nil
}

func (s *Store) createUserTx(ctx context.Context, tx *sql.Tx, user domain.User) (int64, error) {
	switch s.dialect {
	case DialectSQLite:
		result, err := tx.ExecContext(ctx,
			`INSERT INTO app_users (email, name, password_hash, is_admin) VALUES (?, ?, ?, ?)`,
			user.Email,
			user.Name,
			user.PasswordHash,
			user.IsAdmin,
		)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	case DialectPostgres, DialectPostgreSQL:
		var id int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO app_users (email, name, password_hash, is_admin) VALUES ($1, $2, $3, $4) RETURNING id`,
			user.Email,
			user.Name,
			user.PasswordHash,
			user.IsAdmin,
		).Scan(&id)
		return id, err
	default:
		return 0, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) FindUserByEmail(ctx context.Context, email string) (domain.User, error) {
	query := `SELECT id, email, name, password_hash, is_admin FROM app_users WHERE email = ?`
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT id, email, name, password_hash, is_admin FROM app_users WHERE email = $1`
	}
	email = strings.TrimSpace(strings.ToLower(email))

	var user domain.User
	err := s.readDB.QueryRowContext(ctx, query, email).Scan(
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

// UpdateUserProfile changes the user's display name and login email.
func (s *Store) UpdateUserProfile(ctx context.Context, userID int64, name string, email string) error {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(strings.ToLower(email))
	query := `UPDATE app_users SET name = ?, email = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	args := []any{name, email, userID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `UPDATE app_users SET name = $1, email = $2, updated_at = NOW() WHERE id = $3`
	}

	result, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrConflict
		}
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserPassword changes the password and revokes every session except the
// one identified by keepSessionID. Both changes happen atomically.
func (s *Store) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string, keepSessionID string) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	updateQuery := `UPDATE app_users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	updateArgs := []any{passwordHash, userID}
	deleteQuery := `DELETE FROM sessions WHERE user_id = ? AND id <> ?`
	deleteArgs := []any{userID, keepSessionID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		updateQuery = `UPDATE app_users SET password_hash = $1, updated_at = NOW() WHERE id = $2`
		deleteQuery = `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`
	}

	result, err := tx.ExecContext(ctx, updateQuery, updateArgs...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, deleteQuery, deleteArgs...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetUserAdmin(ctx context.Context, userID int64, isAdmin bool) error {
	query := `UPDATE app_users SET is_admin = ? WHERE id = ?`
	args := []any{isAdmin, userID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `UPDATE app_users SET is_admin = $1 WHERE id = $2`
	}

	_, err := s.writeDB.ExecContext(ctx, query, args...)
	return err
}

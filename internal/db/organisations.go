package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"anchor/internal/domain"
)

const (
	// OrganisationRoleAdmin grants organisation management permissions.
	OrganisationRoleAdmin = "admin"
	// OrganisationRoleMember grants standard organisation access.
	OrganisationRoleMember = "member"
	// InvitationTTL is the validity period of a new organisation invitation.
	InvitationTTL = 7 * 24 * time.Hour
)

// ErrLastOrganisationAdmin prevents removing an organisation's final administrator.
var ErrLastOrganisationAdmin = errors.New("organisation must have at least one admin")

// ErrInvalidInvitation reports an absent, expired, accepted, or otherwise unusable invitation.
var ErrInvalidInvitation = errors.New("invalid invitation")

// OrganisationInviteResult describes either an existing-user membership change
// or a new invitation token.
type OrganisationInviteResult struct {
	ExistingUser bool
	Token        string
	Email        string
}

// InvitationAcceptance identifies the user and organisation joined through an invitation.
type InvitationAcceptance struct {
	UserID         int64
	OrganisationID int64
}

func (s *Store) OrganisationCount(ctx context.Context) (int, error) {
	var count int
	if err := s.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM organisations").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) CreateOrganisation(ctx context.Context, organisation domain.Organisation) (int64, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	id, err := s.createOrganisationTx(ctx, tx, organisation.Name)
	if err != nil {
		if isUniqueConstraintError(err) {
			return 0, ErrConflict
		}
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) CreateOrganisationForUser(ctx context.Context, organisation domain.Organisation, creatorUserID int64) (int64, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	organisationID, err := s.createOrganisationTx(ctx, tx, organisation.Name)
	if err != nil {
		if isUniqueConstraintError(err) {
			return 0, ErrConflict
		}
		return 0, err
	}
	if err := s.addUserToOrganisationTx(ctx, tx, domain.OrganisationMembership{
		UserID:         creatorUserID,
		OrganisationID: organisationID,
		Role:           OrganisationRoleAdmin,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return organisationID, nil
}

func (s *Store) AddUserToOrganisation(ctx context.Context, membership domain.OrganisationMembership) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := s.addUserToOrganisationTx(ctx, tx, membership); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RenameOrganisation(ctx context.Context, organisationID int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrConflict
	}

	query := `UPDATE organisations SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	args := []any{name, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `UPDATE organisations SET name = $1, updated_at = NOW() WHERE id = $2`
	}

	result, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrConflict
		}
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) IsOrganisationAdmin(ctx context.Context, userID int64, organisationID int64) (bool, error) {
	query := `
		SELECT COUNT(*)
		FROM organisation_users
		WHERE user_id = ? AND organisation_id = ? AND role = ?
	`
	args := []any{userID, organisationID, OrganisationRoleAdmin}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT COUNT(*)
			FROM organisation_users
			WHERE user_id = $1 AND organisation_id = $2 AND role = $3
		`
	}

	var count int
	if err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) ListOrganisationMembers(ctx context.Context, organisationID int64) ([]domain.OrganisationMember, error) {
	query := `
		SELECT u.id, ou.organisation_id, u.email, u.name, ou.role
		FROM organisation_users ou
		JOIN app_users u ON u.id = ou.user_id
		WHERE ou.organisation_id = ?
		ORDER BY CASE ou.role WHEN 'admin' THEN 0 ELSE 1 END, u.name, u.email
	`
	args := []any{organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT u.id, ou.organisation_id, u.email, u.name, ou.role
			FROM organisation_users ou
			JOIN app_users u ON u.id = ou.user_id
			WHERE ou.organisation_id = $1
			ORDER BY CASE ou.role WHEN 'admin' THEN 0 ELSE 1 END, u.name, u.email
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []domain.OrganisationMember
	for rows.Next() {
		var member domain.OrganisationMember
		if err := rows.Scan(&member.UserID, &member.OrganisationID, &member.Email, &member.Name, &member.Role); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return members, nil
}

func (s *Store) InviteUserToOrganisation(ctx context.Context, organisationID int64, inviterUserID int64, email string, now time.Time) (OrganisationInviteResult, error) {
	email = normalizeEmail(email)
	if email == "" {
		return OrganisationInviteResult{}, ErrNotFound
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return OrganisationInviteResult{}, err
	}
	defer tx.Rollback()

	existingUserID, err := s.findUserIDByEmailTx(ctx, tx, email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return OrganisationInviteResult{}, err
	}
	if err == nil {
		if err := s.addUserToOrganisationTx(ctx, tx, domain.OrganisationMembership{
			UserID:         existingUserID,
			OrganisationID: organisationID,
			Role:           OrganisationRoleMember,
		}); err != nil {
			return OrganisationInviteResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return OrganisationInviteResult{}, err
		}
		return OrganisationInviteResult{ExistingUser: true, Email: email}, nil
	}

	token, err := randomInvitationToken()
	if err != nil {
		return OrganisationInviteResult{}, err
	}
	tokenHash := invitationTokenHash(token)
	expiresAt := now.UTC().Add(InvitationTTL).Format(time.RFC3339)

	if err := s.deletePendingInvitationsTx(ctx, tx, organisationID, email); err != nil {
		return OrganisationInviteResult{}, err
	}
	if err := s.createInvitationTx(ctx, tx, organisationID, inviterUserID, email, tokenHash, expiresAt); err != nil {
		return OrganisationInviteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return OrganisationInviteResult{}, err
	}

	return OrganisationInviteResult{Token: token, Email: email}, nil
}

func (s *Store) InvitationByToken(ctx context.Context, token string, now time.Time) (domain.OrganisationInvitation, error) {
	tokenHash := invitationTokenHash(token)
	query := `
		SELECT i.id, i.organisation_id, o.name, i.email, i.token_hash, i.expires_at, COALESCE(i.accepted_at, ''), i.inviter_user_id, i.created_at
		FROM organisation_invitations i
		JOIN organisations o ON o.id = i.organisation_id
		WHERE i.token_hash = ? AND i.accepted_at IS NULL AND i.expires_at > ?
	`
	args := []any{tokenHash, now.UTC().Format(time.RFC3339)}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT i.id, i.organisation_id, o.name, i.email, i.token_hash, i.expires_at::text, COALESCE(i.accepted_at::text, ''), i.inviter_user_id, i.created_at::text
			FROM organisation_invitations i
			JOIN organisations o ON o.id = i.organisation_id
			WHERE i.token_hash = $1 AND i.accepted_at IS NULL AND i.expires_at > $2
		`
	}

	var invitation domain.OrganisationInvitation
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(
		&invitation.ID,
		&invitation.OrganisationID,
		&invitation.OrganisationName,
		&invitation.Email,
		&invitation.TokenHash,
		&invitation.ExpiresAt,
		&invitation.AcceptedAt,
		&invitation.InviterUserID,
		&invitation.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OrganisationInvitation{}, ErrInvalidInvitation
	}
	return invitation, err
}

func (s *Store) AcceptInvitation(ctx context.Context, token string, user domain.User, now time.Time) (InvitationAcceptance, error) {
	tokenHash := invitationTokenHash(token)
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return InvitationAcceptance{}, err
	}
	defer tx.Rollback()

	invitation, err := s.invitationByHashTx(ctx, tx, tokenHash, now)
	if err != nil {
		return InvitationAcceptance{}, err
	}
	user.Email = invitation.Email
	user.IsAdmin = false

	userID, err := s.createUserTx(ctx, tx, user)
	if err != nil {
		if isUniqueConstraintError(err) {
			return InvitationAcceptance{}, ErrConflict
		}
		return InvitationAcceptance{}, err
	}
	personalOrganisationID, err := s.createPersonalOrganisationTx(ctx, tx, user.Name)
	if err != nil {
		return InvitationAcceptance{}, err
	}
	if err := s.addUserToOrganisationTx(ctx, tx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: personalOrganisationID,
		Role:           OrganisationRoleAdmin,
	}); err != nil {
		return InvitationAcceptance{}, err
	}
	if err := s.addUserToOrganisationTx(ctx, tx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: invitation.OrganisationID,
		Role:           OrganisationRoleMember,
	}); err != nil {
		return InvitationAcceptance{}, err
	}
	if err := s.markInvitationAcceptedTx(ctx, tx, invitation.ID, now); err != nil {
		return InvitationAcceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return InvitationAcceptance{}, err
	}

	return InvitationAcceptance{UserID: userID, OrganisationID: invitation.OrganisationID}, nil
}

func (s *Store) RemoveOrganisationMember(ctx context.Context, organisationID int64, userID int64) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	role, err := s.membershipRoleTx(ctx, tx, organisationID, userID)
	if err != nil {
		return err
	}
	if role == OrganisationRoleAdmin {
		count, err := s.adminCountTx(ctx, tx, organisationID)
		if err != nil {
			return err
		}
		if count <= 1 {
			return ErrLastOrganisationAdmin
		}
	}

	query := `DELETE FROM organisation_users WHERE organisation_id = ? AND user_id = ?`
	args := []any{organisationID, userID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `DELETE FROM organisation_users WHERE organisation_id = $1 AND user_id = $2`
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListOrganisations(ctx context.Context) ([]domain.Organisation, error) {
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT id, name
		FROM organisations
		ORDER BY name, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var organisations []domain.Organisation
	for rows.Next() {
		var organisation domain.Organisation
		if err := rows.Scan(&organisation.ID, &organisation.Name); err != nil {
			return nil, err
		}
		organisations = append(organisations, organisation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return organisations, nil
}

func (s *Store) Organisation(ctx context.Context, organisationID int64) (domain.Organisation, error) {
	query := `SELECT id, name FROM organisations WHERE id = ?`
	args := []any{organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT id, name FROM organisations WHERE id = $1`
	}

	var organisation domain.Organisation
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&organisation.ID, &organisation.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Organisation{}, ErrNotFound
	}
	return organisation, err
}

func (s *Store) ListOrganisationsForUser(ctx context.Context, user domain.User) ([]domain.Organisation, error) {
	if user.IsAdmin {
		return s.ListOrganisations(ctx)
	}

	query := `
		SELECT o.id, o.name
		FROM organisations o
		JOIN organisation_users ou ON ou.organisation_id = o.id
		WHERE ou.user_id = ?
		ORDER BY o.name, o.id
	`
	args := []any{user.ID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT o.id, o.name
			FROM organisations o
			JOIN organisation_users ou ON ou.organisation_id = o.id
			WHERE ou.user_id = $1
			ORDER BY o.name, o.id
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var organisations []domain.Organisation
	for rows.Next() {
		var organisation domain.Organisation
		if err := rows.Scan(&organisation.ID, &organisation.Name); err != nil {
			return nil, err
		}
		organisations = append(organisations, organisation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return organisations, nil
}

func (s *Store) createPersonalOrganisationTx(ctx context.Context, tx *sql.Tx, displayName string) (int64, error) {
	base := strings.TrimSpace(displayName)
	if base == "" {
		base = "Personal"
	}
	base += "'s organisation"

	for suffix := 0; suffix < 1000; suffix++ {
		name := base
		if suffix > 0 {
			name = fmt.Sprintf("%s %d", base, suffix+1)
		}
		id, err := s.createOrganisationTx(ctx, tx, name)
		if err == nil {
			return id, nil
		}
		if !isUniqueConstraintError(err) {
			return 0, err
		}
	}

	return 0, ErrConflict
}

func (s *Store) createOrganisationTx(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	name = strings.TrimSpace(name)
	switch s.dialect {
	case DialectSQLite:
		result, err := tx.ExecContext(ctx, `INSERT INTO organisations (name) VALUES (?)`, name)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	case DialectPostgres, DialectPostgreSQL:
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO organisations (name) VALUES ($1) RETURNING id`, name).Scan(&id)
		return id, err
	default:
		return 0, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) addUserToOrganisationTx(ctx context.Context, tx *sql.Tx, membership domain.OrganisationMembership) error {
	role := membership.Role
	if role == "" {
		role = OrganisationRoleMember
	}

	switch s.dialect {
	case DialectSQLite:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO organisation_users (organisation_id, user_id, role) VALUES (?, ?, ?)
			ON CONFLICT (organisation_id, user_id) DO UPDATE SET role = CASE WHEN excluded.role = 'admin' THEN 'admin' ELSE organisation_users.role END`,
			membership.OrganisationID,
			membership.UserID,
			role,
		)
		return err
	case DialectPostgres, DialectPostgreSQL:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO organisation_users (organisation_id, user_id, role) VALUES ($1, $2, $3)
			ON CONFLICT (organisation_id, user_id) DO UPDATE SET role = CASE WHEN excluded.role = 'admin' THEN 'admin' ELSE organisation_users.role END`,
			membership.OrganisationID,
			membership.UserID,
			role,
		)
		return err
	default:
		return fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) findUserIDByEmailTx(ctx context.Context, tx *sql.Tx, email string) (int64, error) {
	query := `SELECT id FROM app_users WHERE email = ?`
	args := []any{email}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT id FROM app_users WHERE email = $1`
	}

	var userID int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return userID, err
}

func (s *Store) deletePendingInvitationsTx(ctx context.Context, tx *sql.Tx, organisationID int64, email string) error {
	query := `DELETE FROM organisation_invitations WHERE organisation_id = ? AND email = ? AND accepted_at IS NULL`
	args := []any{organisationID, email}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `DELETE FROM organisation_invitations WHERE organisation_id = $1 AND email = $2 AND accepted_at IS NULL`
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func (s *Store) createInvitationTx(ctx context.Context, tx *sql.Tx, organisationID int64, inviterUserID int64, email string, tokenHash string, expiresAt string) error {
	switch s.dialect {
	case DialectSQLite:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO organisation_invitations (organisation_id, email, token_hash, expires_at, inviter_user_id) VALUES (?, ?, ?, ?, ?)`,
			organisationID,
			email,
			tokenHash,
			expiresAt,
			inviterUserID,
		)
		return err
	case DialectPostgres, DialectPostgreSQL:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO organisation_invitations (organisation_id, email, token_hash, expires_at, inviter_user_id) VALUES ($1, $2, $3, $4, $5)`,
			organisationID,
			email,
			tokenHash,
			expiresAt,
			inviterUserID,
		)
		return err
	default:
		return fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) invitationByHashTx(ctx context.Context, tx *sql.Tx, tokenHash string, now time.Time) (domain.OrganisationInvitation, error) {
	query := `
		SELECT id, organisation_id, email, token_hash, expires_at, COALESCE(accepted_at, ''), inviter_user_id, created_at
		FROM organisation_invitations
		WHERE token_hash = ? AND accepted_at IS NULL AND expires_at > ?
	`
	args := []any{tokenHash, now.UTC().Format(time.RFC3339)}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT id, organisation_id, email, token_hash, expires_at::text, COALESCE(accepted_at::text, ''), inviter_user_id, created_at::text
			FROM organisation_invitations
			WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > $2
		`
	}

	var invitation domain.OrganisationInvitation
	err := tx.QueryRowContext(ctx, query, args...).Scan(
		&invitation.ID,
		&invitation.OrganisationID,
		&invitation.Email,
		&invitation.TokenHash,
		&invitation.ExpiresAt,
		&invitation.AcceptedAt,
		&invitation.InviterUserID,
		&invitation.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OrganisationInvitation{}, ErrInvalidInvitation
	}
	return invitation, err
}

func (s *Store) markInvitationAcceptedTx(ctx context.Context, tx *sql.Tx, invitationID int64, now time.Time) error {
	query := `UPDATE organisation_invitations SET accepted_at = ? WHERE id = ? AND accepted_at IS NULL`
	args := []any{now.UTC().Format(time.RFC3339), invitationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `UPDATE organisation_invitations SET accepted_at = $1 WHERE id = $2 AND accepted_at IS NULL`
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return ErrInvalidInvitation
	}
	return err
}

func (s *Store) membershipRoleTx(ctx context.Context, tx *sql.Tx, organisationID int64, userID int64) (string, error) {
	query := `SELECT role FROM organisation_users WHERE organisation_id = ? AND user_id = ?`
	args := []any{organisationID, userID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT role FROM organisation_users WHERE organisation_id = $1 AND user_id = $2`
	}
	var role string
	err := tx.QueryRowContext(ctx, query, args...).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

func (s *Store) adminCountTx(ctx context.Context, tx *sql.Tx, organisationID int64) (int, error) {
	query := `SELECT COUNT(*) FROM organisation_users WHERE organisation_id = ? AND role = ?`
	args := []any{organisationID, OrganisationRoleAdmin}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT COUNT(*) FROM organisation_users WHERE organisation_id = $1 AND role = $2`
	}
	var count int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

func randomInvitationToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}

func invitationTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func normalizeEmail(email string) string {
	return strings.TrimSpace(strings.ToLower(email))
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "duplicate key") ||
		strings.Contains(message, "constraint failed")
}

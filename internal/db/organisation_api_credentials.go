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

const organisationAPITokenPrefix = "anc_org_"

type OrganisationAPICredentialCreateResult struct {
	Credential domain.OrganisationAPICredential
	Token      string
}

func (s *Store) CreateOrganisationAPICredential(ctx context.Context, organisationID int64, name string) (OrganisationAPICredentialCreateResult, error) {
	name = strings.TrimSpace(name)
	if organisationID <= 0 || name == "" {
		return OrganisationAPICredentialCreateResult{}, ErrNotFound
	}

	token, err := randomOrganisationAPIToken()
	if err != nil {
		return OrganisationAPICredentialCreateResult{}, err
	}
	tokenHash := organisationAPITokenHash(token)

	var id int64
	if s.isPostgres() {
		err = s.writeDB.QueryRowContext(ctx,
			`INSERT INTO organisation_api_credentials (organisation_id, name, token_hash) VALUES ($1, $2, $3) RETURNING id`,
			organisationID, name, tokenHash,
		).Scan(&id)
	} else {
		var result sql.Result
		result, err = s.writeDB.ExecContext(ctx,
			`INSERT INTO organisation_api_credentials (organisation_id, name, token_hash) VALUES (?, ?, ?)`,
			organisationID, name, tokenHash,
		)
		if err == nil {
			id, err = result.LastInsertId()
		}
	}
	if err != nil {
		if isUniqueConstraintError(err) {
			return OrganisationAPICredentialCreateResult{}, ErrConflict
		}
		return OrganisationAPICredentialCreateResult{}, err
	}

	credential, err := s.OrganisationAPICredential(ctx, organisationID, id)
	if err != nil {
		return OrganisationAPICredentialCreateResult{}, err
	}
	return OrganisationAPICredentialCreateResult{Credential: credential, Token: token}, nil
}

func (s *Store) ListOrganisationAPICredentials(ctx context.Context, organisationID int64) ([]domain.OrganisationAPICredential, error) {
	query := `
		SELECT id, organisation_id, name, token_hash, enabled, COALESCE(last_used_at, ''), created_at, updated_at
		FROM organisation_api_credentials
		WHERE organisation_id = ?
		ORDER BY enabled DESC, created_at DESC, id DESC
	`
	args := []any{organisationID}
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, name, token_hash, enabled, COALESCE(last_used_at::text, ''), created_at::text, updated_at::text
			FROM organisation_api_credentials
			WHERE organisation_id = $1
			ORDER BY enabled DESC, created_at DESC, id DESC
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	credentials := []domain.OrganisationAPICredential{}
	for rows.Next() {
		credential, err := scanOrganisationAPICredential(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return credentials, nil
}

func (s *Store) OrganisationAPICredential(ctx context.Context, organisationID int64, credentialID int64) (domain.OrganisationAPICredential, error) {
	query := `
		SELECT id, organisation_id, name, token_hash, enabled, COALESCE(last_used_at, ''), created_at, updated_at
		FROM organisation_api_credentials
		WHERE organisation_id = ? AND id = ?
	`
	args := []any{organisationID, credentialID}
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, name, token_hash, enabled, COALESCE(last_used_at::text, ''), created_at::text, updated_at::text
			FROM organisation_api_credentials
			WHERE organisation_id = $1 AND id = $2
		`
	}

	credential, err := scanOrganisationAPICredential(s.readDB.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OrganisationAPICredential{}, ErrNotFound
	}
	return credential, err
}

func (s *Store) AuthenticateOrganisationAPIToken(ctx context.Context, token string, now time.Time) (domain.OrganisationAPICredential, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, organisationAPITokenPrefix) {
		return domain.OrganisationAPICredential{}, ErrNotFound
	}
	tokenHash := organisationAPITokenHash(token)

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return domain.OrganisationAPICredential{}, err
	}
	defer tx.Rollback()

	selectQuery := `
		SELECT id, organisation_id, name, token_hash, enabled, COALESCE(last_used_at, ''), created_at, updated_at
		FROM organisation_api_credentials
		WHERE token_hash = ?
	`
	args := []any{tokenHash}
	if s.isPostgres() {
		selectQuery = `
			SELECT id, organisation_id, name, token_hash, enabled, COALESCE(last_used_at::text, ''), created_at::text, updated_at::text
			FROM organisation_api_credentials
			WHERE token_hash = $1
		`
	}
	credential, err := scanOrganisationAPICredential(tx.QueryRowContext(ctx, selectQuery, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OrganisationAPICredential{}, ErrNotFound
	}
	if err != nil {
		return domain.OrganisationAPICredential{}, err
	}
	if !credential.Enabled {
		return domain.OrganisationAPICredential{}, ErrNotFound
	}

	usedAt := now.UTC().Format(time.RFC3339)
	updateQuery := `UPDATE organisation_api_credentials SET last_used_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	updateArgs := []any{usedAt, credential.ID}
	if s.isPostgres() {
		updateQuery = `UPDATE organisation_api_credentials SET last_used_at = $1, updated_at = NOW() WHERE id = $2`
	}
	if _, err := tx.ExecContext(ctx, updateQuery, updateArgs...); err != nil {
		return domain.OrganisationAPICredential{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.OrganisationAPICredential{}, err
	}
	credential.LastUsedAt = usedAt
	return credential, nil
}

func (s *Store) DisableOrganisationAPICredential(ctx context.Context, organisationID int64, credentialID int64) error {
	query := `UPDATE organisation_api_credentials SET enabled = 0, updated_at = CURRENT_TIMESTAMP WHERE organisation_id = ? AND id = ?`
	args := []any{organisationID, credentialID}
	if s.isPostgres() {
		query = `UPDATE organisation_api_credentials SET enabled = FALSE, updated_at = NOW() WHERE organisation_id = $1 AND id = $2`
	}
	return s.execOne(ctx, query, args...)
}

func (s *Store) RotateOrganisationAPICredential(ctx context.Context, organisationID int64, credentialID int64) (OrganisationAPICredentialCreateResult, error) {
	token, err := randomOrganisationAPIToken()
	if err != nil {
		return OrganisationAPICredentialCreateResult{}, err
	}
	tokenHash := organisationAPITokenHash(token)

	query := `UPDATE organisation_api_credentials SET token_hash = ?, enabled = 1, updated_at = CURRENT_TIMESTAMP WHERE organisation_id = ? AND id = ?`
	args := []any{tokenHash, organisationID, credentialID}
	if s.isPostgres() {
		query = `UPDATE organisation_api_credentials SET token_hash = $1, enabled = TRUE, updated_at = NOW() WHERE organisation_id = $2 AND id = $3`
	}
	if err := s.execOne(ctx, query, args...); err != nil {
		if isUniqueConstraintError(err) {
			return OrganisationAPICredentialCreateResult{}, ErrConflict
		}
		return OrganisationAPICredentialCreateResult{}, err
	}

	credential, err := s.OrganisationAPICredential(ctx, organisationID, credentialID)
	if err != nil {
		return OrganisationAPICredentialCreateResult{}, err
	}
	return OrganisationAPICredentialCreateResult{Credential: credential, Token: token}, nil
}

type organisationAPICredentialScanner interface {
	Scan(dest ...any) error
}

func scanOrganisationAPICredential(row organisationAPICredentialScanner) (domain.OrganisationAPICredential, error) {
	var credential domain.OrganisationAPICredential
	if err := row.Scan(
		&credential.ID,
		&credential.OrganisationID,
		&credential.Name,
		&credential.TokenHash,
		&credential.Enabled,
		&credential.LastUsedAt,
		&credential.CreatedAt,
		&credential.UpdatedAt,
	); err != nil {
		return domain.OrganisationAPICredential{}, err
	}
	return credential, nil
}

func randomOrganisationAPIToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate organisation api token: %w", err)
	}
	return organisationAPITokenPrefix + hex.EncodeToString(bytes), nil
}

func organisationAPITokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

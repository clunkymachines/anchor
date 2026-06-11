package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"anchor/internal/domain"
)

func (s *Store) CreateSoftwareRelease(ctx context.Context, release domain.SoftwareRelease) (int64, error) {
	switch s.dialect {
	case DialectSQLite:
		result, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO software_releases (
				organisation_id, name, version, artifact_path, artifact_filename, artifact_content_type, artifact_size_bytes
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			release.OrganisationID,
			release.Name,
			release.Version,
			release.ArtifactPath,
			release.ArtifactFilename,
			release.ArtifactContentType,
			release.ArtifactSizeBytes,
		)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	case DialectPostgres, DialectPostgreSQL:
		var id int64
		err := s.writeDB.QueryRowContext(ctx,
			`INSERT INTO software_releases (
				organisation_id, name, version, artifact_path, artifact_filename, artifact_content_type, artifact_size_bytes
			) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			release.OrganisationID,
			release.Name,
			release.Version,
			release.ArtifactPath,
			release.ArtifactFilename,
			release.ArtifactContentType,
			release.ArtifactSizeBytes,
		).Scan(&id)
		return id, err
	default:
		return 0, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) ListSoftwareReleases(ctx context.Context, organisationID int64) ([]domain.SoftwareRelease, error) {
	query := `
		SELECT id, organisation_id, name, version, artifact_path, artifact_filename, artifact_content_type, artifact_size_bytes, created_at
		FROM software_releases
		WHERE organisation_id = ?
		ORDER BY name, version
	`
	args := []any{organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT id, organisation_id, name, version, artifact_path, artifact_filename, artifact_content_type, artifact_size_bytes, created_at
			FROM software_releases
			WHERE organisation_id = $1
			ORDER BY name, version
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var releases []domain.SoftwareRelease
	for rows.Next() {
		release, err := scanSoftwareRelease(rows)
		if err != nil {
			return nil, err
		}
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return releases, nil
}

func (s *Store) SoftwareRelease(ctx context.Context, releaseID int64, organisationID int64) (domain.SoftwareRelease, error) {
	query := `
		SELECT id, organisation_id, name, version, artifact_path, artifact_filename, artifact_content_type, artifact_size_bytes, created_at
		FROM software_releases
		WHERE id = ? AND organisation_id = ?
	`
	args := []any{releaseID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT id, organisation_id, name, version, artifact_path, artifact_filename, artifact_content_type, artifact_size_bytes, created_at
			FROM software_releases
			WHERE id = $1 AND organisation_id = $2
		`
	}

	var release domain.SoftwareRelease
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(
		&release.ID,
		&release.OrganisationID,
		&release.Name,
		&release.Version,
		&release.ArtifactPath,
		&release.ArtifactFilename,
		&release.ArtifactContentType,
		&release.ArtifactSizeBytes,
		&release.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SoftwareRelease{}, ErrNotFound
	}
	return release, err
}

func (s *Store) ListOngoingOTADeployments(ctx context.Context, organisationID int64) ([]domain.OTADeployment, error) {
	query := `
		SELECT d.id, d.organisation_id, d.release_id, r.name, r.version, d.target, d.status, d.created_at
		FROM ota_deployments d
		JOIN software_releases r ON r.id = d.release_id AND r.organisation_id = d.organisation_id
		WHERE d.organisation_id = ?
			AND d.status NOT IN ('completed', 'failed', 'cancelled')
		ORDER BY d.created_at DESC, d.id DESC
	`
	args := []any{organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT d.id, d.organisation_id, d.release_id, r.name, r.version, d.target, d.status, d.created_at
			FROM ota_deployments d
			JOIN software_releases r ON r.id = d.release_id AND r.organisation_id = d.organisation_id
			WHERE d.organisation_id = $1
				AND d.status NOT IN ('completed', 'failed', 'cancelled')
			ORDER BY d.created_at DESC, d.id DESC
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deployments []domain.OTADeployment
	for rows.Next() {
		var deployment domain.OTADeployment
		if err := rows.Scan(
			&deployment.ID,
			&deployment.OrganisationID,
			&deployment.ReleaseID,
			&deployment.ReleaseName,
			&deployment.ReleaseVersion,
			&deployment.Target,
			&deployment.Status,
			&deployment.CreatedAt,
		); err != nil {
			return nil, err
		}
		deployments = append(deployments, deployment)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return deployments, nil
}

func scanSoftwareRelease(rows *sql.Rows) (domain.SoftwareRelease, error) {
	var release domain.SoftwareRelease
	if err := rows.Scan(
		&release.ID,
		&release.OrganisationID,
		&release.Name,
		&release.Version,
		&release.ArtifactPath,
		&release.ArtifactFilename,
		&release.ArtifactContentType,
		&release.ArtifactSizeBytes,
		&release.CreatedAt,
	); err != nil {
		return domain.SoftwareRelease{}, err
	}
	return release, nil
}

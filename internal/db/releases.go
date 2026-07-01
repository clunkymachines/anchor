package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"anchor/internal/domain"
)

func (s *Store) CreateSoftwareRelease(ctx context.Context, release domain.SoftwareRelease) (int64, error) {
	if _, err := s.DeviceModel(ctx, release.DeviceModelID, release.OrganisationID); err != nil {
		return 0, err
	}

	switch s.dialect {
	case DialectSQLite:
		result, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO software_releases (
				organisation_id, device_model_id, version, artifact_path, artifact_filename, artifact_content_type, artifact_size_bytes
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			release.OrganisationID,
			release.DeviceModelID,
			release.Version,
			release.ArtifactPath,
			release.ArtifactFilename,
			release.ArtifactContentType,
			release.ArtifactSizeBytes,
		)
		if err != nil {
			if isUniqueConstraintError(err) {
				return 0, ErrConflict
			}
			return 0, err
		}
		return result.LastInsertId()
	case DialectPostgres, DialectPostgreSQL:
		var id int64
		err := s.writeDB.QueryRowContext(ctx,
			`INSERT INTO software_releases (
				organisation_id, device_model_id, version, artifact_path, artifact_filename, artifact_content_type, artifact_size_bytes
			) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			release.OrganisationID,
			release.DeviceModelID,
			release.Version,
			release.ArtifactPath,
			release.ArtifactFilename,
			release.ArtifactContentType,
			release.ArtifactSizeBytes,
		).Scan(&id)
		if err != nil {
			if isUniqueConstraintError(err) {
				return 0, ErrConflict
			}
			return 0, err
		}
		return id, err
	default:
		return 0, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) ListSoftwareReleases(ctx context.Context, organisationID int64) ([]domain.SoftwareRelease, error) {
	query := `
		SELECT r.id, r.organisation_id, r.device_model_id, m.name, r.version,
			r.artifact_path, r.artifact_filename, r.artifact_content_type, r.artifact_size_bytes, r.created_at
		FROM software_releases r
		JOIN device_models m ON m.id = r.device_model_id AND m.organisation_id = r.organisation_id
		WHERE r.organisation_id = ?
		ORDER BY m.name, r.version
	`
	args := []any{organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT r.id, r.organisation_id, r.device_model_id, m.name, r.version,
				r.artifact_path, r.artifact_filename, r.artifact_content_type, r.artifact_size_bytes, r.created_at
			FROM software_releases r
			JOIN device_models m ON m.id = r.device_model_id AND m.organisation_id = r.organisation_id
			WHERE r.organisation_id = $1
			ORDER BY m.name, r.version
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
		SELECT r.id, r.organisation_id, r.device_model_id, m.name, r.version,
			r.artifact_path, r.artifact_filename, r.artifact_content_type, r.artifact_size_bytes, r.created_at
		FROM software_releases r
		JOIN device_models m ON m.id = r.device_model_id AND m.organisation_id = r.organisation_id
		WHERE r.id = ? AND r.organisation_id = ?
	`
	args := []any{releaseID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT r.id, r.organisation_id, r.device_model_id, m.name, r.version,
				r.artifact_path, r.artifact_filename, r.artifact_content_type, r.artifact_size_bytes, r.created_at
			FROM software_releases r
			JOIN device_models m ON m.id = r.device_model_id AND m.organisation_id = r.organisation_id
			WHERE r.id = $1 AND r.organisation_id = $2
		`
	}

	release, err := scanSoftwareRelease(s.readDB.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SoftwareRelease{}, ErrNotFound
	}
	return release, err
}

func (s *Store) SoftwareReleaseByDeviceModelAndVersion(ctx context.Context, organisationID int64, deviceModelID int64, version string) (domain.SoftwareRelease, error) {
	query := `
		SELECT r.id, r.organisation_id, r.device_model_id, m.name, r.version,
			r.artifact_path, r.artifact_filename, r.artifact_content_type, r.artifact_size_bytes, r.created_at
		FROM software_releases r
		JOIN device_models m ON m.id = r.device_model_id AND m.organisation_id = r.organisation_id
		WHERE r.organisation_id = ? AND r.device_model_id = ? AND r.version = ?
	`
	args := []any{organisationID, deviceModelID, version}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT r.id, r.organisation_id, r.device_model_id, m.name, r.version,
				r.artifact_path, r.artifact_filename, r.artifact_content_type, r.artifact_size_bytes, r.created_at
			FROM software_releases r
			JOIN device_models m ON m.id = r.device_model_id AND m.organisation_id = r.organisation_id
			WHERE r.organisation_id = $1 AND r.device_model_id = $2 AND r.version = $3
		`
	}

	release, err := scanSoftwareRelease(s.readDB.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SoftwareRelease{}, ErrNotFound
	}
	return release, err
}

func (s *Store) DeleteSoftwareRelease(ctx context.Context, releaseID int64, organisationID int64) error {
	query := `DELETE FROM software_releases WHERE id = ? AND organisation_id = ?`
	args := []any{releaseID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `DELETE FROM software_releases WHERE id = $1 AND organisation_id = $2`
	}

	result, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListOngoingOTADeployments(ctx context.Context, organisationID int64) ([]domain.OTADeployment, error) {
	query := `
		SELECT d.id, d.organisation_id, d.release_id, m.name, r.version, d.target, d.status, d.created_at
		FROM ota_deployments d
		JOIN software_releases r ON r.id = d.release_id AND r.organisation_id = d.organisation_id
		JOIN device_models m ON m.id = r.device_model_id AND m.organisation_id = r.organisation_id
		WHERE d.organisation_id = ?
			AND d.status NOT IN ('completed', 'failed', 'cancelled')
		ORDER BY d.created_at DESC, d.id DESC
	`
	args := []any{organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT d.id, d.organisation_id, d.release_id, m.name, r.version, d.target, d.status, d.created_at
			FROM ota_deployments d
			JOIN software_releases r ON r.id = d.release_id AND r.organisation_id = d.organisation_id
			JOIN device_models m ON m.id = r.device_model_id AND m.organisation_id = r.organisation_id
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
			&deployment.ReleaseModelName,
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

type softwareReleaseScanner interface {
	Scan(dest ...any) error
}

func scanSoftwareRelease(rows softwareReleaseScanner) (domain.SoftwareRelease, error) {
	var release domain.SoftwareRelease
	if err := rows.Scan(
		&release.ID,
		&release.OrganisationID,
		&release.DeviceModelID,
		&release.DeviceModelName,
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

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"anchor/internal/domain"
)

func (s *Store) CreateDeviceModel(ctx context.Context, model domain.DeviceModel) (int64, error) {
	var expectedReleaseID any
	if model.ExpectedReleaseID != nil {
		expectedReleaseID = *model.ExpectedReleaseID
	}

	switch s.dialect {
	case DialectSQLite:
		result, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO device_models (
				organisation_id, name, expected_heartbeat_seconds, expected_protocol, expected_release_id
			) VALUES (?, ?, ?, ?, ?)`,
			model.OrganisationID,
			model.Name,
			model.ExpectedHeartbeatSeconds,
			model.ExpectedProtocol,
			expectedReleaseID,
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
			`INSERT INTO device_models (
				organisation_id, name, expected_heartbeat_seconds, expected_protocol, expected_release_id
			) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			model.OrganisationID,
			model.Name,
			model.ExpectedHeartbeatSeconds,
			model.ExpectedProtocol,
			expectedReleaseID,
		).Scan(&id)
		if err != nil {
			if isUniqueConstraintError(err) {
				return 0, ErrConflict
			}
			return 0, err
		}
		return id, nil
	default:
		return 0, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) ListDeviceModels(ctx context.Context, organisationID int64) ([]domain.DeviceModel, error) {
	query := `
		SELECT m.id, m.organisation_id, m.name, m.expected_heartbeat_seconds, m.expected_protocol,
			m.expected_release_id, COALESCE(rm.name, ''), COALESCE(r.version, ''), m.created_at
		FROM device_models m
		LEFT JOIN software_releases r ON r.id = m.expected_release_id AND r.organisation_id = m.organisation_id
		LEFT JOIN device_models rm ON rm.id = r.device_model_id AND rm.organisation_id = r.organisation_id
		WHERE m.organisation_id = ?
		ORDER BY m.name
	`
	args := []any{organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT m.id, m.organisation_id, m.name, m.expected_heartbeat_seconds, m.expected_protocol,
				m.expected_release_id, COALESCE(rm.name, ''), COALESCE(r.version, ''), m.created_at
			FROM device_models m
			LEFT JOIN software_releases r ON r.id = m.expected_release_id AND r.organisation_id = m.organisation_id
			LEFT JOIN device_models rm ON rm.id = r.device_model_id AND rm.organisation_id = r.organisation_id
			WHERE m.organisation_id = $1
			ORDER BY m.name
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []domain.DeviceModel
	for rows.Next() {
		model, err := scanDeviceModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return models, nil
}

func (s *Store) DeviceModel(ctx context.Context, modelID int64, organisationID int64) (domain.DeviceModel, error) {
	query := `
		SELECT m.id, m.organisation_id, m.name, m.expected_heartbeat_seconds, m.expected_protocol,
			m.expected_release_id, COALESCE(rm.name, ''), COALESCE(r.version, ''), m.created_at
		FROM device_models m
		LEFT JOIN software_releases r ON r.id = m.expected_release_id AND r.organisation_id = m.organisation_id
		LEFT JOIN device_models rm ON rm.id = r.device_model_id AND rm.organisation_id = r.organisation_id
		WHERE m.id = ? AND m.organisation_id = ?
	`
	args := []any{modelID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT m.id, m.organisation_id, m.name, m.expected_heartbeat_seconds, m.expected_protocol,
				m.expected_release_id, COALESCE(rm.name, ''), COALESCE(r.version, ''), m.created_at
			FROM device_models m
			LEFT JOIN software_releases r ON r.id = m.expected_release_id AND r.organisation_id = m.organisation_id
			LEFT JOIN device_models rm ON rm.id = r.device_model_id AND rm.organisation_id = r.organisation_id
			WHERE m.id = $1 AND m.organisation_id = $2
		`
	}

	row := s.readDB.QueryRowContext(ctx, query, args...)
	model, err := scanDeviceModel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DeviceModel{}, ErrNotFound
	}
	return model, err
}

func (s *Store) UpdateDeviceModelExpectedRelease(ctx context.Context, organisationID int64, modelID int64, expectedReleaseID *int64) error {
	var releaseID any
	if expectedReleaseID != nil {
		if _, err := s.SoftwareRelease(ctx, *expectedReleaseID, organisationID); err != nil {
			return err
		}
		releaseID = *expectedReleaseID
	}

	query := `
		UPDATE device_models
		SET expected_release_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE organisation_id = ? AND id = ?
	`
	args := []any{releaseID, organisationID, modelID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			UPDATE device_models
			SET expected_release_id = $1, updated_at = NOW()
			WHERE organisation_id = $2 AND id = $3
		`
	}
	return s.execOne(ctx, query, args...)
}

type deviceModelScanner interface {
	Scan(dest ...any) error
}

func scanDeviceModel(row deviceModelScanner) (domain.DeviceModel, error) {
	var (
		model             domain.DeviceModel
		expectedReleaseID nullableInt64
	)
	if err := row.Scan(
		&model.ID,
		&model.OrganisationID,
		&model.Name,
		&model.ExpectedHeartbeatSeconds,
		&model.ExpectedProtocol,
		&expectedReleaseID,
		&model.ExpectedReleaseModelName,
		&model.ExpectedReleaseVersion,
		&model.CreatedAt,
	); err != nil {
		return domain.DeviceModel{}, err
	}
	if expectedReleaseID.Valid {
		model.ExpectedReleaseID = &expectedReleaseID.Int64
	}
	return model, nil
}

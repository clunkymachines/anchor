package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"anchor/internal/domain"
)

func (s *Store) ListDevices(ctx context.Context) ([]domain.Device, error) {
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds,
			COALESCE(last_event.last_received_ms, 0), d.software_versions, d.is_gateway
		FROM devices d
		JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
		LEFT JOIN (
			SELECT device_id, MAX(ts_received_ms) AS last_received_ms
			FROM device_events
			GROUP BY device_id
		) last_event ON last_event.device_id = d.id
		ORDER BY m.name, d.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []domain.Device
	for rows.Next() {
		var (
			device   domain.Device
			versions softwareVersionsValue
		)
		if err := rows.Scan(
			&device.ID,
			&device.OrganisationID,
			&device.DeviceModelID,
			&device.ModelName,
			&device.ExpectedHeartbeatSeconds,
			&device.LastEventReceivedMS,
			&versions,
			&device.IsGateway,
		); err != nil {
			return nil, err
		}
		device.SoftwareVersions = domain.SoftwareVersions(versions)
		devices = append(devices, device)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return devices, nil
}

func (s *Store) ListDevicesWithMQTT(ctx context.Context, organisationID int64) ([]domain.DeviceWithMQTT, error) {
	query := `
		SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds,
			COALESCE(last_event.last_received_ms, 0), d.software_versions, d.is_gateway, mc.username, mc.enabled
		FROM devices d
		JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
		LEFT JOIN (
			SELECT device_id, MAX(ts_received_ms) AS last_received_ms
			FROM device_events
			GROUP BY device_id
		) last_event ON last_event.device_id = d.id
		LEFT JOIN mqtt_credentials mc ON mc.device_id = d.id
		WHERE d.organisation_id = ?
		ORDER BY m.name, d.id
	`
	args := []any{organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds,
				COALESCE(last_event.last_received_ms, 0), d.software_versions, d.is_gateway, mc.username, mc.enabled
			FROM devices d
			JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
			LEFT JOIN (
				SELECT device_id, MAX(ts_received_ms) AS last_received_ms
				FROM device_events
				GROUP BY device_id
			) last_event ON last_event.device_id = d.id
			LEFT JOIN mqtt_credentials mc ON mc.device_id = d.id
			WHERE d.organisation_id = $1
			ORDER BY m.name, d.id
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []domain.DeviceWithMQTT
	for rows.Next() {
		var (
			device   domain.Device
			versions softwareVersionsValue
			username nullableString
			enabled  nullableBool
		)
		if err := rows.Scan(
			&device.ID,
			&device.OrganisationID,
			&device.DeviceModelID,
			&device.ModelName,
			&device.ExpectedHeartbeatSeconds,
			&device.LastEventReceivedMS,
			&versions,
			&device.IsGateway,
			&username,
			&enabled,
		); err != nil {
			return nil, err
		}
		device.SoftwareVersions = domain.SoftwareVersions(versions)

		deviceWithMQTT := domain.DeviceWithMQTT{Device: device}
		if username.Valid {
			deviceWithMQTT.MQTTCredential = &domain.DeviceMQTTCredential{
				DeviceID: device.ID,
				Username: username.String,
				Enabled:  enabled.Bool,
			}
		}
		devices = append(devices, deviceWithMQTT)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return devices, nil
}

func (s *Store) DeviceDetail(ctx context.Context, deviceID string, organisationID int64) (domain.DeviceDetail, error) {
	var (
		device   domain.Device
		versions softwareVersionsValue
		username nullableString
		enabled  nullableBool
	)

	query := `
		SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds,
			COALESCE(last_event.last_received_ms, 0), d.software_versions, d.is_gateway, mc.username, mc.enabled
		FROM devices d
		JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
		LEFT JOIN (
			SELECT device_id, MAX(ts_received_ms) AS last_received_ms
			FROM device_events
			GROUP BY device_id
		) last_event ON last_event.device_id = d.id
		LEFT JOIN mqtt_credentials mc ON mc.device_id = d.id
		WHERE d.id = ? AND d.organisation_id = ?
	`
	args := []any{deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds,
				COALESCE(last_event.last_received_ms, 0), d.software_versions, d.is_gateway, mc.username, mc.enabled
			FROM devices d
			JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
			LEFT JOIN (
				SELECT device_id, MAX(ts_received_ms) AS last_received_ms
				FROM device_events
				GROUP BY device_id
			) last_event ON last_event.device_id = d.id
			LEFT JOIN mqtt_credentials mc ON mc.device_id = d.id
			WHERE d.id = $1 AND d.organisation_id = $2
		`
	}

	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(
		&device.ID,
		&device.OrganisationID,
		&device.DeviceModelID,
		&device.ModelName,
		&device.ExpectedHeartbeatSeconds,
		&device.LastEventReceivedMS,
		&versions,
		&device.IsGateway,
		&username,
		&enabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DeviceDetail{}, ErrNotFound
	}
	if err != nil {
		return domain.DeviceDetail{}, err
	}
	device.SoftwareVersions = domain.SoftwareVersions(versions)

	detail := domain.DeviceDetail{Device: device}
	if username.Valid {
		detail.MQTTCredential = &domain.DeviceMQTTCredential{
			DeviceID: device.ID,
			Username: username.String,
			Enabled:  enabled.Bool,
		}
	}

	return detail, nil
}

func (s *Store) SaveDeviceWithMQTTCredential(ctx context.Context, cfg domain.DeviceWithMQTTCredential) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := s.upsertDevice(ctx, tx, cfg.Device); err != nil {
		return err
	}
	if err := s.upsertMQTTCredential(ctx, tx, cfg.Credential); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) DeleteDevice(ctx context.Context, deviceID string, organisationID int64) error {
	query := `DELETE FROM devices WHERE id = ? AND organisation_id = ?`
	args := []any{deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `DELETE FROM devices WHERE id = $1 AND organisation_id = $2`
	}

	_, err := s.writeDB.ExecContext(ctx, query, args...)
	return err
}

func (s *Store) FindMQTTCredentialByUsername(ctx context.Context, username string) (domain.DeviceMQTTCredential, error) {
	query := `SELECT device_id, username, password_hash, enabled FROM mqtt_credentials WHERE username = ?`
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT device_id, username, password_hash, enabled FROM mqtt_credentials WHERE username = $1`
	}

	var credential domain.DeviceMQTTCredential
	err := s.readDB.QueryRowContext(ctx, query, username).Scan(
		&credential.DeviceID,
		&credential.Username,
		&credential.PasswordHash,
		&credential.Enabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DeviceMQTTCredential{}, ErrNotFound
	}
	return credential, err
}

func (s *Store) FindMQTTPrincipalByUsername(ctx context.Context, username string) (domain.MQTTPrincipal, error) {
	query := `
		SELECT d.id, d.organisation_id, d.is_gateway, c.enabled
		FROM mqtt_credentials c
		JOIN devices d ON d.id = c.device_id
		WHERE c.username = ?
	`
	args := []any{username}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT d.id, d.organisation_id, d.is_gateway, c.enabled
			FROM mqtt_credentials c
			JOIN devices d ON d.id = c.device_id
			WHERE c.username = $1
		`
	}

	var principal domain.MQTTPrincipal
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(
		&principal.DeviceID,
		&principal.OrganisationID,
		&principal.IsGateway,
		&principal.Enabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MQTTPrincipal{}, ErrNotFound
	}
	return principal, err
}

func (s *Store) DeviceExistsInOrganisation(ctx context.Context, deviceID string, organisationID int64) (bool, error) {
	query := `SELECT 1 FROM devices WHERE id = ? AND organisation_id = ?`
	args := []any{deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT 1 FROM devices WHERE id = $1 AND organisation_id = $2`
	}

	var found int
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) upsertDevice(ctx context.Context, tx txRunner, device domain.Device) error {
	switch s.dialect {
	case DialectSQLite:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO devices (id, organisation_id, device_model_id, software_versions, is_gateway) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET organisation_id = excluded.organisation_id, device_model_id = excluded.device_model_id, software_versions = excluded.software_versions, is_gateway = excluded.is_gateway, updated_at = CURRENT_TIMESTAMP`,
			device.ID,
			device.OrganisationID,
			device.DeviceModelID,
			softwareVersionsValue(device.SoftwareVersions),
			device.IsGateway,
		)
		return err
	case DialectPostgres, DialectPostgreSQL:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO devices (id, organisation_id, device_model_id, software_versions, is_gateway) VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT(id) DO UPDATE SET organisation_id = excluded.organisation_id, device_model_id = excluded.device_model_id, software_versions = excluded.software_versions, is_gateway = excluded.is_gateway, updated_at = NOW()`,
			device.ID,
			device.OrganisationID,
			device.DeviceModelID,
			softwareVersionsValue(device.SoftwareVersions),
			device.IsGateway,
		)
		return err
	default:
		return fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) upsertMQTTCredential(ctx context.Context, tx txRunner, credential domain.DeviceMQTTCredential) error {
	switch s.dialect {
	case DialectSQLite:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO mqtt_credentials (device_id, username, password_hash, enabled) VALUES (?, ?, ?, ?)
			 ON CONFLICT(device_id) DO UPDATE SET username = excluded.username, password_hash = excluded.password_hash, enabled = excluded.enabled, updated_at = CURRENT_TIMESTAMP`,
			credential.DeviceID,
			credential.Username,
			credential.PasswordHash,
			credential.Enabled,
		)
		return err
	case DialectPostgres, DialectPostgreSQL:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO mqtt_credentials (device_id, username, password_hash, enabled) VALUES ($1, $2, $3, $4)
			 ON CONFLICT(device_id) DO UPDATE SET username = excluded.username, password_hash = excluded.password_hash, enabled = excluded.enabled, updated_at = NOW()`,
			credential.DeviceID,
			credential.Username,
			credential.PasswordHash,
			credential.Enabled,
		)
		return err
	default:
		return fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

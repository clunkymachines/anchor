package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"anchor/internal/domain"
)

type queryRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) InsertDeviceEvent(ctx context.Context, event domain.DeviceEvent) (int64, error) {
	id, err := insertDeviceEvent(ctx, s.writeDB, s.dialect, event)
	if err != nil {
		return 0, err
	}
	s.events.publish(event.DeviceID)
	return id, nil
}

func (s *Store) UpsertDeviceTwinProperty(ctx context.Context, property domain.DeviceTwinProperty) error {
	if err := upsertDeviceTwinProperty(ctx, s.writeDB, s.dialect, property); err != nil {
		return err
	}
	s.events.publish(property.DeviceID)
	return nil
}

func (s *Store) RecordDeviceEvent(ctx context.Context, event domain.DeviceEvent, properties []domain.DeviceTwinProperty) (int64, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	eventID, err := insertDeviceEvent(ctx, tx, s.dialect, event)
	if err != nil {
		return 0, err
	}

	for _, property := range properties {
		if property.DeviceID == "" {
			property.DeviceID = event.DeviceID
		}
		if property.SourceEventID == nil {
			property.SourceEventID = &eventID
		}
		if property.TSReceivedMS == 0 {
			property.TSReceivedMS = event.TSReceivedMS
		}
		if property.Protocol == "" {
			property.Protocol = event.Protocol
		}
		if property.SourcePath == "" {
			property.SourcePath = event.Topic
			if property.SourcePath == "" {
				property.SourcePath = event.CoAPPath
			}
		}
		if err := upsertDeviceTwinProperty(ctx, tx, s.dialect, property); err != nil {
			return 0, err
		}
		if firmwareVersion, ok := firmwareVersionFromTelemetryProperty(property); ok {
			if err := updateDeviceFirmwareVersion(ctx, tx, s.dialect, event.DeviceID, firmwareVersion); err != nil {
				return 0, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.events.publish(event.DeviceID)
	return eventID, nil
}

func firmwareVersionFromTelemetryProperty(property domain.DeviceTwinProperty) (string, bool) {
	if property.ValueType != "string" || (property.Path != "firmware" && property.Path != "firmware.version") {
		return "", false
	}
	var version string
	if err := json.Unmarshal([]byte(property.ValueJSON), &version); err != nil {
		return "", false
	}
	return strings.TrimSpace(version), true
}

func (s *Store) ListDeviceTwinProperties(ctx context.Context, deviceID string, organisationID int64) ([]domain.DeviceTwinProperty, error) {
	query := `
		SELECT p.device_id, p.path, p.value_json, p.value_type, p.source_event_id,
			p.ts_observed_ms, p.ts_received_ms, p.protocol, p.source_path
		FROM device_twin_properties p
		JOIN devices d ON d.id = p.device_id
		WHERE p.device_id = ? AND d.organisation_id = ?
		ORDER BY p.path
	`
	args := []any{deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT p.device_id, p.path, p.value_json, p.value_type, p.source_event_id,
				p.ts_observed_ms, p.ts_received_ms, p.protocol, p.source_path
			FROM device_twin_properties p
			JOIN devices d ON d.id = p.device_id
			WHERE p.device_id = $1 AND d.organisation_id = $2
			ORDER BY p.path
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var properties []domain.DeviceTwinProperty
	for rows.Next() {
		property, err := scanDeviceTwinProperty(rows)
		if err != nil {
			return nil, err
		}
		properties = append(properties, property)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return properties, nil
}

func (s *Store) ListRecentDeviceEvents(ctx context.Context, deviceID string, organisationID int64, limit int) ([]domain.DeviceEvent, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT e.id, e.device_id, e.ts_received_ms, e.protocol, e.direction, e.operation,
			e.topic, e.coap_path, e.method, e.code, e.content_format, e.payload_raw,
			e.payload_json, e.correlation_id, e.schema_hint, e.source, e.retained
		FROM device_events e
		JOIN devices d ON d.id = e.device_id
		WHERE e.device_id = ? AND d.organisation_id = ?
		ORDER BY e.ts_received_ms DESC, e.id DESC
		LIMIT ?
	`
	args := []any{deviceID, organisationID, limit}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT e.id, e.device_id, e.ts_received_ms, e.protocol, e.direction, e.operation,
				e.topic, e.coap_path, e.method, e.code, e.content_format, e.payload_raw,
				e.payload_json, e.correlation_id, e.schema_hint, e.source, e.retained
			FROM device_events e
			JOIN devices d ON d.id = e.device_id
			WHERE e.device_id = $1 AND d.organisation_id = $2
			ORDER BY e.ts_received_ms DESC, e.id DESC
			LIMIT $3
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []domain.DeviceEvent
	for rows.Next() {
		event, err := scanDeviceEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return events, nil
}

func insertDeviceEvent(ctx context.Context, runner queryRunner, dialect Dialect, event domain.DeviceEvent) (int64, error) {
	switch dialect {
	case DialectSQLite:
		result, err := runner.ExecContext(ctx,
			`INSERT INTO device_events (
				device_id, ts_received_ms, protocol, direction, operation, topic, coap_path,
				method, code, content_format, payload_raw, payload_json, correlation_id,
				schema_hint, source, retained
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			event.DeviceID,
			event.TSReceivedMS,
			event.Protocol,
			event.Direction,
			event.Operation,
			event.Topic,
			event.CoAPPath,
			event.Method,
			event.Code,
			event.ContentFormat,
			event.PayloadRaw,
			event.PayloadJSON,
			event.CorrelationID,
			event.SchemaHint,
			event.Source,
			event.Retained,
		)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	case DialectPostgres, DialectPostgreSQL:
		var id int64
		err := runner.QueryRowContext(ctx,
			`INSERT INTO device_events (
				device_id, ts_received_ms, protocol, direction, operation, topic, coap_path,
				method, code, content_format, payload_raw, payload_json, correlation_id,
				schema_hint, source, retained
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			RETURNING id`,
			event.DeviceID,
			event.TSReceivedMS,
			event.Protocol,
			event.Direction,
			event.Operation,
			event.Topic,
			event.CoAPPath,
			event.Method,
			event.Code,
			event.ContentFormat,
			event.PayloadRaw,
			event.PayloadJSON,
			event.CorrelationID,
			event.SchemaHint,
			event.Source,
			event.Retained,
		).Scan(&id)
		return id, err
	default:
		return 0, fmt.Errorf("unsupported db dialect %q", dialect)
	}
}

func upsertDeviceTwinProperty(ctx context.Context, runner queryRunner, dialect Dialect, property domain.DeviceTwinProperty) error {
	sourceEventID := nullableSourceEventID(property.SourceEventID)

	switch dialect {
	case DialectSQLite:
		_, err := runner.ExecContext(ctx,
			`INSERT INTO device_twin_properties (
				device_id, path, value_json, value_type, source_event_id, ts_observed_ms,
				ts_received_ms, protocol, source_path
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(device_id, path) DO UPDATE SET
				value_json = excluded.value_json,
				value_type = excluded.value_type,
				source_event_id = excluded.source_event_id,
				ts_observed_ms = excluded.ts_observed_ms,
				ts_received_ms = excluded.ts_received_ms,
				protocol = excluded.protocol,
				source_path = excluded.source_path`,
			property.DeviceID,
			property.Path,
			property.ValueJSON,
			property.ValueType,
			sourceEventID,
			property.TSObservedMS,
			property.TSReceivedMS,
			property.Protocol,
			property.SourcePath,
		)
		return err
	case DialectPostgres, DialectPostgreSQL:
		_, err := runner.ExecContext(ctx,
			`INSERT INTO device_twin_properties (
				device_id, path, value_json, value_type, source_event_id, ts_observed_ms,
				ts_received_ms, protocol, source_path
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT(device_id, path) DO UPDATE SET
				value_json = excluded.value_json,
				value_type = excluded.value_type,
				source_event_id = excluded.source_event_id,
				ts_observed_ms = excluded.ts_observed_ms,
				ts_received_ms = excluded.ts_received_ms,
				protocol = excluded.protocol,
				source_path = excluded.source_path`,
			property.DeviceID,
			property.Path,
			property.ValueJSON,
			property.ValueType,
			sourceEventID,
			property.TSObservedMS,
			property.TSReceivedMS,
			property.Protocol,
			property.SourcePath,
		)
		return err
	default:
		return fmt.Errorf("unsupported db dialect %q", dialect)
	}
}

func updateDeviceFirmwareVersion(ctx context.Context, runner queryRunner, dialect Dialect, deviceID string, version string) error {
	switch dialect {
	case DialectSQLite:
		_, err := runner.ExecContext(ctx,
			`UPDATE devices
			SET software_versions = json_set(software_versions, '$.firmware', ?),
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			version,
			deviceID,
		)
		return err
	case DialectPostgres, DialectPostgreSQL:
		_, err := runner.ExecContext(ctx,
			`UPDATE devices
			SET software_versions = jsonb_set(software_versions, '{firmware}', to_jsonb($1::text), true),
				updated_at = NOW()
			WHERE id = $2`,
			version,
			deviceID,
		)
		return err
	default:
		return fmt.Errorf("unsupported db dialect %q", dialect)
	}
}

func nullableSourceEventID(sourceEventID *int64) any {
	if sourceEventID == nil {
		return nil
	}
	return *sourceEventID
}

func scanDeviceEvent(rows *sql.Rows) (domain.DeviceEvent, error) {
	var (
		event         domain.DeviceEvent
		topic         nullableString
		coapPath      nullableString
		method        nullableString
		code          nullableString
		contentFormat nullableString
		payloadRaw    []byte
		payloadJSON   nullableString
		correlationID nullableString
		schemaHint    nullableString
		source        nullableString
	)
	if err := rows.Scan(
		&event.ID,
		&event.DeviceID,
		&event.TSReceivedMS,
		&event.Protocol,
		&event.Direction,
		&event.Operation,
		&topic,
		&coapPath,
		&method,
		&code,
		&contentFormat,
		&payloadRaw,
		&payloadJSON,
		&correlationID,
		&schemaHint,
		&source,
		&event.Retained,
	); err != nil {
		return domain.DeviceEvent{}, err
	}

	event.Topic = topic.String
	event.CoAPPath = coapPath.String
	event.Method = method.String
	event.Code = code.String
	event.ContentFormat = contentFormat.String
	event.PayloadRaw = payloadRaw
	event.PayloadJSON = payloadJSON.String
	event.CorrelationID = correlationID.String
	event.SchemaHint = schemaHint.String
	event.Source = source.String

	return event, nil
}

func scanDeviceTwinProperty(rows *sql.Rows) (domain.DeviceTwinProperty, error) {
	var (
		property      domain.DeviceTwinProperty
		sourceEventID nullableInt64
		valueJSON     nullableString
		sourcePath    nullableString
	)
	if err := rows.Scan(
		&property.DeviceID,
		&property.Path,
		&valueJSON,
		&property.ValueType,
		&sourceEventID,
		&property.TSObservedMS,
		&property.TSReceivedMS,
		&property.Protocol,
		&sourcePath,
	); err != nil {
		return domain.DeviceTwinProperty{}, err
	}

	property.ValueJSON = valueJSON.String
	property.SourcePath = sourcePath.String
	if sourceEventID.Valid {
		property.SourceEventID = &sourceEventID.Int64
	}

	return property, nil
}

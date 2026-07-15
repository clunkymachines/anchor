package db

import (
	"context"
	"database/sql"
	"errors"

	"anchor/internal/domain"
)

// MQTTIntegration returns the singleton application-level MQTT integration configuration.
func (s *Store) MQTTIntegration(ctx context.Context) (domain.MQTTIntegrationConfig, error) {
	query := `
		SELECT enabled, broker_url, client_id, username, password, qos, updated_at
		FROM mqtt_integration
		WHERE id = 1
	`
	if s.isPostgres() {
		query = `
			SELECT enabled, broker_url, client_id, username, password, qos, updated_at::text
			FROM mqtt_integration
			WHERE id = 1
		`
	}

	var config domain.MQTTIntegrationConfig
	if err := s.readDB.QueryRowContext(ctx, query).Scan(
		&config.Enabled,
		&config.BrokerURL,
		&config.ClientID,
		&config.Username,
		&config.Password,
		&config.QoS,
		&config.UpdatedAt,
	); errors.Is(err, sql.ErrNoRows) {
		return domain.MQTTIntegrationConfig{}, ErrNotFound
	} else if err != nil {
		return domain.MQTTIntegrationConfig{}, err
	}
	config.Configured = true
	return config, nil
}

// SaveMQTTIntegration replaces the singleton application-level MQTT integration configuration.
func (s *Store) SaveMQTTIntegration(ctx context.Context, config domain.MQTTIntegrationConfig) error {
	query := `
		INSERT INTO mqtt_integration (id, enabled, broker_url, client_id, username, password, qos)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			enabled = excluded.enabled,
			broker_url = excluded.broker_url,
			client_id = excluded.client_id,
			username = excluded.username,
			password = excluded.password,
			qos = excluded.qos,
			updated_at = CURRENT_TIMESTAMP
	`
	args := []any{config.Enabled, config.BrokerURL, config.ClientID, config.Username, config.Password, config.QoS}
	if s.isPostgres() {
		query = `
			INSERT INTO mqtt_integration (id, enabled, broker_url, client_id, username, password, qos)
			VALUES (1, $1, $2, $3, $4, $5, $6)
			ON CONFLICT (id) DO UPDATE SET
				enabled = excluded.enabled,
				broker_url = excluded.broker_url,
				client_id = excluded.client_id,
				username = excluded.username,
				password = excluded.password,
				qos = excluded.qos,
				updated_at = NOW()
		`
	}
	_, err := s.writeDB.ExecContext(ctx, query, args...)
	return err
}

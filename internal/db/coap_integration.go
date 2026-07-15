package db

import (
	"context"
	"database/sql"
	"errors"

	"anchor/internal/domain"
)

func (s *Store) CoAPIntegration(ctx context.Context) (domain.CoAPIntegrationConfig, error) {
	query := `SELECT enabled, frontend_url, bearer_token, updated_at FROM coap_integration WHERE id = 1`
	if s.isPostgres() {
		query = `SELECT enabled, frontend_url, bearer_token, updated_at::text FROM coap_integration WHERE id = 1`
	}
	var config domain.CoAPIntegrationConfig
	err := s.readDB.QueryRowContext(ctx, query).Scan(&config.Enabled, &config.FrontendURL, &config.BearerToken, &config.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CoAPIntegrationConfig{}, ErrNotFound
	}
	config.Configured = true
	return config, err
}

func (s *Store) SaveCoAPIntegration(ctx context.Context, config domain.CoAPIntegrationConfig) error {
	if config.BearerToken == "" {
		if current, err := s.CoAPIntegration(ctx); err == nil {
			config.BearerToken = current.BearerToken
		}
	}
	if config.Enabled {
		if err := domain.ValidateCoAPFrontendURL(config.FrontendURL); err != nil {
			return err
		}
		if config.BearerToken == "" {
			return errors.New("CoAP bearer token is required when enabled")
		}
	}
	query := `INSERT INTO coap_integration (id, enabled, frontend_url, bearer_token) VALUES (1, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled, frontend_url = excluded.frontend_url, bearer_token = excluded.bearer_token, updated_at = CURRENT_TIMESTAMP`
	args := []any{config.Enabled, config.FrontendURL, config.BearerToken}
	if s.isPostgres() {
		query = `INSERT INTO coap_integration (id, enabled, frontend_url, bearer_token) VALUES (1, $1, $2, $3) ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled, frontend_url = excluded.frontend_url, bearer_token = excluded.bearer_token, updated_at = NOW()`
	}
	_, err := s.writeDB.ExecContext(ctx, query, args...)
	return err
}

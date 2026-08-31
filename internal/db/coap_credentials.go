package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"anchor/internal/domain"
)

const generatedCoAPPSKLength = 16

func (s *Store) CreateCoAPCredential(ctx context.Context, credential domain.CoAPCredential, organisationID int64) (domain.CoAPCredential, error) {
	credential, err := prepareNewCoAPCredential(credential)
	if err != nil {
		return domain.CoAPCredential{}, err
	}
	if _, err := s.deviceOrganisation(ctx, credential.DeviceID, organisationID); err != nil {
		return domain.CoAPCredential{}, err
	}
	if err := s.insertCoAPCredential(ctx, s.writeDB, credential); err != nil {
		return domain.CoAPCredential{}, err
	}
	return s.loadCoAPCredential(ctx, credential.DeviceID, credential.PSK)
}

func (s *Store) SaveDeviceWithCoAPCredential(ctx context.Context, cfg domain.DeviceWithCoAPCredential) (domain.CoAPCredential, error) {
	var normalizedTags []string
	if cfg.Device.Tags != nil {
		var err error
		normalizedTags, err = NormalizeTags(cfg.Device.Tags)
		if err != nil {
			return domain.CoAPCredential{}, err
		}
	}
	credential, err := prepareNewCoAPCredential(cfg.Credential)
	if err != nil {
		return domain.CoAPCredential{}, err
	}
	if credential.DeviceID != cfg.Device.ID {
		return domain.CoAPCredential{}, errors.New("credential device id must match device id")
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return domain.CoAPCredential{}, err
	}
	defer tx.Rollback()

	if err := s.upsertDevice(ctx, tx, cfg.Device); err != nil {
		return domain.CoAPCredential{}, err
	}
	if err := s.upsertCoAPCredential(ctx, tx, credential); err != nil {
		return domain.CoAPCredential{}, err
	}
	if err := deleteDeviceCredentialTx(ctx, tx, s.dialect, "mqtt_credentials", cfg.Device.ID); err != nil {
		return domain.CoAPCredential{}, err
	}
	if err := s.refreshDeviceSearchTextTx(ctx, tx, cfg.Device.OrganisationID, cfg.Device.ID); err != nil {
		return domain.CoAPCredential{}, err
	}
	if cfg.Device.Tags != nil {
		if err := s.replaceDeviceTagsTx(ctx, tx, cfg.Device.ID, cfg.Device.OrganisationID, normalizedTags); err != nil {
			return domain.CoAPCredential{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.CoAPCredential{}, err
	}
	return s.loadCoAPCredential(ctx, credential.DeviceID, credential.PSK)
}

func (s *Store) upsertCoAPCredential(ctx context.Context, runner txRunner, credential domain.CoAPCredential) error {
	query := `INSERT INTO coap_credentials (device_id, psk_identity, psk, revision, enabled) VALUES (?, ?, ?, ?, ?) ON CONFLICT(device_id) DO UPDATE SET psk_identity=excluded.psk_identity, psk=excluded.psk, revision=coap_credentials.revision+1, enabled=excluded.enabled, updated_at=CURRENT_TIMESTAMP`
	if s.isPostgres() {
		query = `INSERT INTO coap_credentials (device_id, psk_identity, psk, revision, enabled) VALUES ($1,$2,$3,$4,$5) ON CONFLICT(device_id) DO UPDATE SET psk_identity=excluded.psk_identity, psk=excluded.psk, revision=coap_credentials.revision+1, enabled=excluded.enabled, updated_at=NOW()`
	}
	_, err := runner.ExecContext(ctx, query, credential.DeviceID, credential.PSKIdentity, credential.PSK, credential.Revision, credential.Enabled)
	return err
}

func prepareNewCoAPCredential(credential domain.CoAPCredential) (domain.CoAPCredential, error) {
	if credential.DeviceID == "" {
		return domain.CoAPCredential{}, errors.New("device id is required")
	}
	if credential.PSKIdentity == "" {
		credential.PSKIdentity = credential.DeviceID
	}
	if err := domain.ValidatePSKIdentity(credential.PSKIdentity); err != nil {
		return domain.CoAPCredential{}, err
	}
	if len(credential.PSK) == 0 {
		credential.PSK = make([]byte, generatedCoAPPSKLength)
		if _, err := rand.Read(credential.PSK); err != nil {
			return domain.CoAPCredential{}, fmt.Errorf("generate PSK: %w", err)
		}
	}
	if len(credential.PSK) < 16 || len(credential.PSK) > 64 {
		return domain.CoAPCredential{}, errors.New("PSK must be between 16 and 64 bytes")
	}
	if credential.Revision <= 0 {
		credential.Revision = 1
	}
	if credential.Revision != 1 {
		return domain.CoAPCredential{}, errors.New("new PSK credential revision must be 1")
	}
	return credential, nil
}

func (s *Store) insertCoAPCredential(ctx context.Context, runner txRunner, credential domain.CoAPCredential) error {
	query := `INSERT INTO coap_credentials (device_id, psk_identity, psk, revision, enabled) VALUES (?, ?, ?, ?, ?)`
	args := []any{credential.DeviceID, credential.PSKIdentity, credential.PSK, credential.Revision, credential.Enabled}
	if s.isPostgres() {
		query = `INSERT INTO coap_credentials (device_id, psk_identity, psk, revision, enabled) VALUES ($1, $2, $3, $4, $5)`
	}
	_, err := runner.ExecContext(ctx, query, args...)
	return err
}

func (s *Store) ReplaceCoAPCredential(ctx context.Context, credential domain.CoAPCredential, organisationID int64) (domain.CoAPCredential, error) {
	if err := domain.ValidatePSKIdentity(credential.PSKIdentity); err != nil {
		return domain.CoAPCredential{}, err
	}
	if len(credential.PSK) < 16 || len(credential.PSK) > 64 {
		return domain.CoAPCredential{}, errors.New("PSK must be between 16 and 64 bytes")
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return domain.CoAPCredential{}, err
	}
	defer tx.Rollback()
	if _, err := s.deviceOrganisationTx(ctx, tx, credential.DeviceID, organisationID); err != nil {
		return domain.CoAPCredential{}, err
	}
	query := `UPDATE coap_credentials SET psk_identity = ?, psk = ?, revision = revision + 1, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE device_id = ?`
	args := []any{credential.PSKIdentity, credential.PSK, credential.Enabled, credential.DeviceID}
	if s.isPostgres() {
		query = `UPDATE coap_credentials SET psk_identity = $1, psk = $2, revision = revision + 1, enabled = $3, updated_at = NOW() WHERE device_id = $4`
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return domain.CoAPCredential{}, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return domain.CoAPCredential{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return domain.CoAPCredential{}, err
	}
	return s.loadCoAPCredential(ctx, credential.DeviceID, credential.PSK)
}

func (s *Store) EnableCoAPCredential(ctx context.Context, deviceID string, organisationID int64, enabled bool) error {
	if _, err := s.deviceOrganisation(ctx, deviceID, organisationID); err != nil {
		return err
	}
	query := `UPDATE coap_credentials SET enabled = ?, revision = revision + 1, updated_at = CURRENT_TIMESTAMP WHERE device_id = ?`
	args := []any{enabled, deviceID}
	if s.isPostgres() {
		query = `UPDATE coap_credentials SET enabled = $1, revision = revision + 1, updated_at = NOW() WHERE device_id = $2`
	}
	result, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) LoadCoAPCredentialSummary(ctx context.Context, deviceID string, organisationID int64) (domain.CoAPCredentialSummary, error) {
	query := `SELECT c.device_id, c.psk_identity, c.revision, c.enabled, c.created_at, c.updated_at FROM coap_credentials c JOIN devices d ON d.id = c.device_id WHERE c.device_id = ? AND d.organisation_id = ?`
	args := []any{deviceID, organisationID}
	if s.isPostgres() {
		query = `SELECT c.device_id, c.psk_identity, c.revision, c.enabled, c.created_at::text, c.updated_at::text FROM coap_credentials c JOIN devices d ON d.id = c.device_id WHERE c.device_id = $1 AND d.organisation_id = $2`
	}
	var out domain.CoAPCredentialSummary
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&out.DeviceID, &out.PSKIdentity, &out.Revision, &out.Enabled, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CoAPCredentialSummary{}, ErrNotFound
	}
	return out, err
}

func (s *Store) ResolveCoAPCredential(ctx context.Context, identity string) (domain.CoAPResolvedCredential, error) {
	query := `SELECT c.device_id, d.organisation_id, c.psk_identity, c.psk, c.revision, c.enabled, c.created_at, c.updated_at, m.expected_heartbeat_seconds, m.expected_protocol FROM coap_credentials c JOIN devices d ON d.id = c.device_id JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id WHERE c.psk_identity = ? AND c.enabled = 1`
	args := []any{identity}
	if s.isPostgres() {
		query = `SELECT c.device_id, d.organisation_id, c.psk_identity, c.psk, c.revision, c.enabled, c.created_at::text, c.updated_at::text, m.expected_heartbeat_seconds, m.expected_protocol FROM coap_credentials c JOIN devices d ON d.id = c.device_id JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id WHERE c.psk_identity = $1 AND c.enabled = TRUE`
	}
	var out domain.CoAPResolvedCredential
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&out.DeviceID, &out.OrganisationID, &out.PSKIdentity, &out.PSK, &out.Revision, &out.Enabled, &out.CreatedAt, &out.UpdatedAt, &out.ExpectedHeartbeatSeconds, &out.ExpectedProtocol)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CoAPResolvedCredential{}, ErrNotFound
	}
	return out, err
}

func (s *Store) loadCoAPCredential(ctx context.Context, deviceID string, psk []byte) (domain.CoAPCredential, error) {
	summary, err := s.LoadCoAPCredentialSummary(ctx, deviceID, 0)
	if err != nil {
		query := `SELECT device_id, psk_identity, revision, enabled, created_at, updated_at FROM coap_credentials WHERE device_id = ?`
		if s.isPostgres() {
			query = `SELECT device_id, psk_identity, revision, enabled, created_at::text, updated_at::text FROM coap_credentials WHERE device_id = $1`
		}
		var c domain.CoAPCredential
		err = s.readDB.QueryRowContext(ctx, query, deviceID).Scan(&c.DeviceID, &c.PSKIdentity, &c.Revision, &c.Enabled, &c.CreatedAt, &c.UpdatedAt)
		if err != nil {
			return domain.CoAPCredential{}, err
		}
		c.PSK = append([]byte(nil), psk...)
		return c, nil
	}
	return domain.CoAPCredential{DeviceID: summary.DeviceID, PSKIdentity: summary.PSKIdentity, PSK: append([]byte(nil), psk...), Revision: summary.Revision, Enabled: summary.Enabled, CreatedAt: summary.CreatedAt, UpdatedAt: summary.UpdatedAt}, nil
}

func (s *Store) deviceOrganisation(ctx context.Context, deviceID string, organisationID int64) (int64, error) {
	var found int64
	query := `SELECT organisation_id FROM devices WHERE id = ? AND organisation_id = ?`
	args := []any{deviceID, organisationID}
	if s.isPostgres() {
		query = `SELECT organisation_id FROM devices WHERE id = $1 AND organisation_id = $2`
	}
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return found, err
}
func (s *Store) deviceOrganisationTx(ctx context.Context, tx *sql.Tx, deviceID string, organisationID int64) (int64, error) {
	var found int64
	query := `SELECT organisation_id FROM devices WHERE id = ? AND organisation_id = ?`
	args := []any{deviceID, organisationID}
	if s.isPostgres() {
		query = `SELECT organisation_id FROM devices WHERE id = $1 AND organisation_id = $2 FOR UPDATE`
	}
	err := tx.QueryRowContext(ctx, query, args...).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return found, err
}

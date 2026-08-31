package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const MaxDeviceTags = 32

func NormalizeTags(tags []string) ([]string, error) {
	if len(tags) > MaxDeviceTags {
		return nil, fmt.Errorf("a device can have at most %d tags", MaxDeviceTags)
	}
	seen := make(map[string]struct{}, len(tags))
	normalized := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" || len(tag) > 64 {
			return nil, errors.New("tags must be 1 to 64 characters")
		}
		for i := 0; i < len(tag); i++ {
			char := tag[i]
			if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.') {
				return nil, errors.New("tags may contain only ASCII letters, numbers, hyphens, underscores, and periods")
			}
		}
		if _, exists := seen[tag]; exists {
			return nil, fmt.Errorf("duplicate tag %q", tag)
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func NormalizeTag(tag string) (string, error) {
	tags, err := NormalizeTags([]string{tag})
	if err != nil {
		return "", err
	}
	return tags[0], nil
}

func (s *Store) DeviceTags(ctx context.Context, deviceID string, organisationID int64) ([]string, error) {
	query := `SELECT tag FROM device_tags WHERE device_id = ? AND organisation_id = ? ORDER BY tag`
	if s.isPostgres() {
		query = numberedPlaceholders(query)
	}
	rows, err := s.readDB.QueryContext(ctx, query, deviceID, organisationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make([]string, 0)
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

func (s *Store) ReplaceDeviceTags(ctx context.Context, deviceID string, organisationID int64, tags []string) error {
	normalized, err := NormalizeTags(tags)
	if err != nil {
		return err
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.replaceDeviceTagsTx(ctx, tx, deviceID, organisationID, normalized); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) replaceDeviceTagsTx(ctx context.Context, tx *sql.Tx, deviceID string, organisationID int64, normalized []string) error {
	query := `SELECT 1 FROM devices WHERE id = ? AND organisation_id = ?`
	if s.isPostgres() {
		query += ` FOR UPDATE`
		query = numberedPlaceholders(query)
	}
	var one int
	if err := tx.QueryRowContext(ctx, query, deviceID, organisationID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	deleteQuery := `DELETE FROM device_tags WHERE device_id = ? AND organisation_id = ?`
	insertQuery := `INSERT INTO device_tags (device_id, organisation_id, tag) VALUES (?, ?, ?)`
	if s.isPostgres() {
		deleteQuery = numberedPlaceholders(deleteQuery)
		insertQuery = numberedPlaceholders(insertQuery)
	}
	if _, err := tx.ExecContext(ctx, deleteQuery, deviceID, organisationID); err != nil {
		return err
	}
	for _, tag := range normalized {
		if err := s.ensureTagTx(ctx, tx, organisationID, tag); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, insertQuery, deviceID, organisationID, tag); err != nil {
			return err
		}
	}
	return s.cleanupUnusedTagsTx(ctx, tx, organisationID)
}

func (s *Store) ensureTagTx(ctx context.Context, tx *sql.Tx, organisationID int64, tag string) error {
	query := `INSERT OR IGNORE INTO tags (organisation_id, name) VALUES (?, ?)`
	if s.isPostgres() {
		query = `INSERT INTO tags (organisation_id, name) VALUES ($1, $2) ON CONFLICT DO NOTHING`
	}
	_, err := tx.ExecContext(ctx, query, organisationID, tag)
	return err
}

func (s *Store) cleanupUnusedTagsTx(ctx context.Context, tx *sql.Tx, organisationID int64) error {
	query := `DELETE FROM tags WHERE organisation_id = ? AND NOT EXISTS (SELECT 1 FROM device_tags dt WHERE dt.organisation_id = tags.organisation_id AND dt.tag = tags.name)`
	if s.isPostgres() {
		query = numberedPlaceholders(query)
	}
	_, err := tx.ExecContext(ctx, query, organisationID)
	return err
}

func (s *Store) UpdateDeviceTagsBulk(ctx context.Context, organisationID int64, deviceIDs []string, rawTag string, add bool) error {
	tag, err := NormalizeTag(rawTag)
	if err != nil {
		return err
	}
	if err := validateCampaignDeviceIDs(deviceIDs); err != nil {
		return err
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids := append([]string(nil), deviceIDs...)
	sort.Strings(ids)
	for _, deviceID := range ids {
		lockQuery := `SELECT 1 FROM devices WHERE id = ? AND organisation_id = ?`
		if s.isPostgres() {
			lockQuery += ` FOR UPDATE`
			lockQuery = numberedPlaceholders(lockQuery)
		}
		var existsDevice int
		if err := tx.QueryRowContext(ctx, lockQuery, deviceID, organisationID).Scan(&existsDevice); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		query := `SELECT COUNT(*) FROM device_tags WHERE device_id = ? AND organisation_id = ?`
		if s.isPostgres() {
			query = numberedPlaceholders(query)
		}
		var count int
		if err := tx.QueryRowContext(ctx, query, deviceID, organisationID).Scan(&count); err != nil {
			return err
		}
		if add && count >= MaxDeviceTags {
			existsQuery := `SELECT COUNT(*) FROM device_tags WHERE device_id = ? AND organisation_id = ? AND tag = ?`
			if s.isPostgres() {
				existsQuery = numberedPlaceholders(existsQuery)
			}
			var exists int
			if err := tx.QueryRowContext(ctx, existsQuery, deviceID, organisationID, tag).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return fmt.Errorf("device %q already has %d tags", deviceID, MaxDeviceTags)
			}
		}
	}
	if add {
		if err := s.ensureTagTx(ctx, tx, organisationID, tag); err != nil {
			return err
		}
	}
	for _, deviceID := range ids {
		var query string
		if add {
			query = `INSERT OR IGNORE INTO device_tags (device_id, organisation_id, tag) VALUES (?, ?, ?)`
			if s.isPostgres() {
				query = `INSERT INTO device_tags (device_id, organisation_id, tag) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`
			}
		} else {
			query = `DELETE FROM device_tags WHERE device_id = ? AND organisation_id = ? AND tag = ?`
			if s.isPostgres() {
				query = numberedPlaceholders(query)
			}
		}
		if _, err := tx.ExecContext(ctx, query, deviceID, organisationID, tag); err != nil {
			return err
		}
	}
	if !add {
		if err := s.cleanupUnusedTagsTx(ctx, tx, organisationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListTagSuggestions(ctx context.Context, organisationID int64, prefix string) ([]string, error) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	prefix = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
	query := `SELECT name FROM tags WHERE organisation_id = ? AND name LIKE ? ESCAPE '\' AND EXISTS (SELECT 1 FROM device_tags dt WHERE dt.organisation_id = tags.organisation_id AND dt.tag = tags.name) ORDER BY name LIMIT 100`
	if s.isPostgres() {
		query = numberedPlaceholders(query)
	}
	rows, err := s.readDB.QueryContext(ctx, query, organisationID, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make([]string, 0)
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

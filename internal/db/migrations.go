package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const createSchemaMigrationsTableSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	id INTEGER PRIMARY KEY,
	applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

// migration is an ordered list of SQL statements applied atomically.
type migration struct {
	id         int
	statements []string
}

// Migrate applies every migration that has not already been recorded.
func (s *Store) Migrate(ctx context.Context) error {
	migrations, err := s.migrationsForDialect()
	if err != nil {
		return err
	}

	return s.applyMigrations(ctx, migrations)
}

func (s *Store) migrationsForDialect() ([]migration, error) {
	initialSchema, err := s.dialect.initialSchemaStatements()
	if err != nil {
		return nil, err
	}

	migration2 := []string{
		`CREATE TABLE IF NOT EXISTS device_tags (device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE, organisation_id INTEGER NOT NULL REFERENCES organisations(id) ON DELETE CASCADE, tag TEXT NOT NULL, PRIMARY KEY (device_id, tag));`,
		`CREATE INDEX IF NOT EXISTS idx_device_tags_organisation_tag ON device_tags(organisation_id, tag, device_id);`,
		`CREATE INDEX IF NOT EXISTS idx_device_tags_device ON device_tags(device_id, tag);`,
		`ALTER TABLE campaigns ADD COLUMN target_type TEXT NOT NULL DEFAULT 'explicit';`,
		`ALTER TABLE campaigns ADD COLUMN target_device_ids TEXT NOT NULL DEFAULT '[]';`,
		`ALTER TABLE campaigns ADD COLUMN target_tag TEXT;`,
		`ALTER TABLE campaigns ADD COLUMN target_model_id INTEGER;`,
		`ALTER TABLE campaigns ADD COLUMN target_model_name TEXT;`,
		`UPDATE campaigns SET target_type = 'explicit', target_device_ids = COALESCE((SELECT json_group_array(device_id) FROM (SELECT device_id FROM device_tasks WHERE campaign_id = campaigns.id ORDER BY device_id)), '[]');`,
	}
	if s.isPostgres() {
		migration2 = []string{
			`CREATE TABLE IF NOT EXISTS device_tags (device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE, organisation_id BIGINT NOT NULL REFERENCES organisations(id) ON DELETE CASCADE, tag TEXT NOT NULL, PRIMARY KEY (device_id, tag));`,
			`CREATE INDEX IF NOT EXISTS idx_device_tags_organisation_tag ON device_tags(organisation_id, tag, device_id);`,
			`CREATE INDEX IF NOT EXISTS idx_device_tags_device ON device_tags(device_id, tag);`,
			`ALTER TABLE campaigns ADD COLUMN target_type TEXT NOT NULL DEFAULT 'explicit';`,
			`ALTER TABLE campaigns ADD COLUMN target_device_ids JSONB NOT NULL DEFAULT '[]'::jsonb;`,
			`ALTER TABLE campaigns ADD COLUMN target_tag TEXT;`,
			`ALTER TABLE campaigns ADD COLUMN target_model_id BIGINT;`,
			`ALTER TABLE campaigns ADD COLUMN target_model_name TEXT;`,
			`UPDATE campaigns SET target_type = 'explicit', target_device_ids = COALESCE((SELECT jsonb_agg(device_id ORDER BY device_id) FROM device_tasks WHERE campaign_id = campaigns.id), '[]'::jsonb);`,
		}
	}
	migration3 := []string{
		`CREATE TABLE IF NOT EXISTS tags (organisation_id INTEGER NOT NULL REFERENCES organisations(id) ON DELETE CASCADE, name TEXT NOT NULL, PRIMARY KEY (organisation_id, name));`,
		`INSERT OR IGNORE INTO tags (organisation_id, name) SELECT DISTINCT organisation_id, tag FROM device_tags;`,
		`CREATE INDEX IF NOT EXISTS idx_tags_organisation_name ON tags(organisation_id, name);`,
	}
	if s.isPostgres() {
		migration3 = []string{
			`CREATE TABLE IF NOT EXISTS tags (organisation_id BIGINT NOT NULL REFERENCES organisations(id) ON DELETE CASCADE, name TEXT NOT NULL, PRIMARY KEY (organisation_id, name));`,
			`INSERT INTO tags (organisation_id, name) SELECT DISTINCT organisation_id, tag FROM device_tags ON CONFLICT DO NOTHING;`,
			`CREATE INDEX IF NOT EXISTS idx_tags_organisation_name ON tags(organisation_id, name);`,
		}
	}
	return []migration{
		{
			id:         1,
			statements: initialSchema,
		},
		{id: 2, statements: migration2},
		{id: 3, statements: migration3},
	}, nil
}

func (s *Store) applyMigrations(ctx context.Context, migrations []migration) error {
	if err := validateMigrations(migrations); err != nil {
		return err
	}

	if _, err := s.writeDB.ExecContext(ctx, createSchemaMigrationsTableSQL); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	for _, migration := range migrations {
		if err := s.applyMigration(ctx, migration); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) applyMigration(ctx context.Context, migration migration) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", migration.id, err)
	}
	defer tx.Rollback()

	applied, err := s.migrationApplied(ctx, tx, migration.id)
	if err != nil {
		return fmt.Errorf("check migration %d: %w", migration.id, err)
	}
	if applied {
		return tx.Commit()
	}

	for statementIndex, statement := range migration.statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply migration %d statement %d: %w", migration.id, statementIndex+1, err)
		}
	}

	insertSQL := `INSERT INTO schema_migrations (id) VALUES (?)`
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		insertSQL = `INSERT INTO schema_migrations (id) VALUES ($1)`
	}
	if _, err := tx.ExecContext(ctx, insertSQL, migration.id); err != nil {
		return fmt.Errorf("record migration %d: %w", migration.id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", migration.id, err)
	}
	return nil
}

func (s *Store) migrationApplied(ctx context.Context, tx *sql.Tx, id int) (bool, error) {
	query := `SELECT id FROM schema_migrations WHERE id = ?`
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT id FROM schema_migrations WHERE id = $1`
	}

	var appliedID int
	err := tx.QueryRowContext(ctx, query, id).Scan(&appliedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func validateMigrations(migrations []migration) error {
	previousID := 0
	for _, migration := range migrations {
		if migration.id <= previousID {
			return fmt.Errorf("migrations must have unique positive IDs in ascending order: %d follows %d", migration.id, previousID)
		}
		if len(migration.statements) == 0 {
			return fmt.Errorf("migration %d has no statements", migration.id)
		}
		previousID = migration.id
	}
	return nil
}

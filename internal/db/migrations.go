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

	return []migration{
		{
			id:         1,
			statements: initialSchema,
		},
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

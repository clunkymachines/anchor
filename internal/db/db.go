// Package db owns database connection setup, schema creation, and persistence helpers.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const sqliteReadMaxOpenConns = 8

// Dialect identifies the SQL backend used by the store.
type Dialect string

const (
	// DialectSQLite uses the modernc SQLite driver.
	DialectSQLite Dialect = "sqlite"
	// DialectPostgres uses the pgx PostgreSQL driver.
	DialectPostgres Dialect = "postgres"
	// DialectPostgreSQL is accepted as an alias for DialectPostgres.
	DialectPostgreSQL Dialect = "postgresql"
)

// Config describes the database backend to open.
type Config struct {
	Dialect Dialect // Database dialect (e.g., "sqlite", "postgres"). Defaults to "sqlite" if not specified.
	DSN     string  // Data Source Name
}

// Store wraps the database handles used by repository methods.
type Store struct {
	readDB  *sql.DB
	writeDB *sql.DB
	dialect Dialect
	events  *deviceEventNotifier
	tasks   *deviceEventNotifier
}

// Open creates a store, configures the database connection, verifies it, and applies the schema.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	// Normalize config before selecting the driver or opening any handles.
	if cfg.Dialect == "" {
		cfg.Dialect = DialectSQLite
	}
	if cfg.DSN == "" {
		return nil, errors.New("db dsn is required")
	}

	driverName, err := cfg.Dialect.driverName()
	if err != nil {
		return nil, err
	}

	// The write handle is the primary connection pool. PostgreSQL uses it for all queries.
	writeDB, err := sql.Open(driverName, cfg.DSN)
	if err != nil {
		return nil, err
	}

	store := &Store{
		readDB:  writeDB,
		writeDB: writeDB,
		dialect: cfg.Dialect,
		events:  newDeviceEventNotifier(),
		tasks:   newDeviceEventNotifier(),
	}

	if cfg.Dialect == DialectSQLite {
		// SQLite gets a second pool so reads can proceed while the single writer is busy.
		readDB, err := sql.Open(driverName, cfg.DSN)
		if err != nil {
			writeDB.Close()
			return nil, err
		}

		store.readDB = readDB

		// SQLite allows many readers in WAL mode, but still only one writer at a time.
		store.readDB.SetMaxOpenConns(sqliteReadMaxOpenConns)
		store.writeDB.SetMaxOpenConns(1)

		// Pragmas are connection-local, so apply them to both pools.
		if err := configureSQLite(ctx, store.writeDB); err != nil {
			store.Close()
			return nil, err
		}
		if store.readDB != store.writeDB {
			if err := configureSQLite(ctx, store.readDB); err != nil {
				store.Close()
				return nil, err
			}
		}
	}

	// sql.Open validates arguments lazily. Ping forces the actual connection attempt.
	if err := store.writeDB.PingContext(ctx); err != nil {
		store.Close()
		return nil, err
	}
	if store.readDB != store.writeDB {
		if err := store.readDB.PingContext(ctx); err != nil {
			store.Close()
			return nil, err
		}
	}

	// Apply the current schema. Migrations will replace this once schema history matters.
	if err := store.InitSchema(ctx); err != nil {
		store.Close()
		return nil, err
	}

	return store, nil
}

// Close releases all database handles owned by the store.
func (s *Store) Close() error {
	if s.readDB == s.writeDB {
		return s.writeDB.Close()
	}

	return errors.Join(s.readDB.Close(), s.writeDB.Close())
}

// InitSchema creates the database schema.
func (s *Store) InitSchema(ctx context.Context) error {
	statements, err := s.dialect.schemaStatements()
	if err != nil {
		return err
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema statement: %w", err)
		}
	}

	return tx.Commit()
}

func configureSQLite(ctx context.Context, db *sql.DB) error {
	pragmas := []string{
		// SQLite leaves foreign key checks off by default and applies this per connection.
		"PRAGMA foreign_keys = ON;",
		// WAL gives better web-server concurrency because readers do not block the writer.
		"PRAGMA journal_mode = WAL;",
		// NORMAL is the usual durability/performance tradeoff when WAL is enabled.
		"PRAGMA synchronous = NORMAL;",
		// Wait briefly for a locked database instead of failing immediately under load.
		"PRAGMA busy_timeout = 5000;",
	}

	for _, pragma := range pragmas {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}

	return nil
}

func (d Dialect) driverName() (string, error) {
	switch d {
	case DialectSQLite:
		return "sqlite", nil
	case DialectPostgres, DialectPostgreSQL:
		return "pgx", nil
	default:
		return "", fmt.Errorf("unsupported db dialect %q", d)
	}
}

func (d Dialect) schemaStatements() ([]string, error) {
	switch d {
	case DialectSQLite:
		return []string{
			`CREATE TABLE IF NOT EXISTS app_users (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				email TEXT NOT NULL UNIQUE,
				name TEXT NOT NULL,
				password_hash TEXT NOT NULL,
				is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE TABLE IF NOT EXISTS organisations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL UNIQUE,
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE TABLE IF NOT EXISTS organisation_users (
				organisation_id INTEGER NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				user_id INTEGER NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
				role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (organisation_id, user_id)
			);`,
			`CREATE TABLE IF NOT EXISTS organisation_invitations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				organisation_id INTEGER NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				email TEXT NOT NULL,
				token_hash TEXT NOT NULL UNIQUE,
				expires_at TEXT NOT NULL,
				accepted_at TEXT,
				inviter_user_id INTEGER NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE TABLE IF NOT EXISTS software_releases (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				organisation_id INTEGER NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				version TEXT NOT NULL,
				artifact_path TEXT NOT NULL,
				artifact_filename TEXT NOT NULL,
				artifact_content_type TEXT NOT NULL,
				artifact_size_bytes INTEGER NOT NULL,
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE (organisation_id, id),
				UNIQUE (organisation_id, name, version)
			);`,
			`CREATE TABLE IF NOT EXISTS device_models (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				organisation_id INTEGER NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				expected_heartbeat_seconds INTEGER NOT NULL CHECK (expected_heartbeat_seconds > 0),
				expected_protocol TEXT NOT NULL,
				expected_release_id INTEGER,
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE (organisation_id, id),
				UNIQUE (organisation_id, name),
				FOREIGN KEY (organisation_id, expected_release_id) REFERENCES software_releases(organisation_id, id)
			);`,
			`CREATE TABLE IF NOT EXISTS devices (
				id TEXT PRIMARY KEY,
				organisation_id INTEGER NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				device_model_id INTEGER NOT NULL,
				software_versions TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(software_versions)),
				is_gateway INTEGER NOT NULL DEFAULT 0 CHECK (is_gateway IN (0, 1)),
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (organisation_id, device_model_id) REFERENCES device_models(organisation_id, id)
			);`,
			`CREATE TABLE IF NOT EXISTS mqtt_credentials (
				device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
				username TEXT NOT NULL UNIQUE,
				password_hash TEXT NOT NULL,
				enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE TABLE IF NOT EXISTS device_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
				ts_received_ms INTEGER NOT NULL,
				protocol TEXT NOT NULL,
				direction TEXT NOT NULL,
				operation TEXT NOT NULL,
				topic TEXT,
				coap_path TEXT,
				method TEXT,
				code TEXT,
				content_format TEXT,
				payload_raw BLOB,
				payload_json TEXT,
				correlation_id TEXT,
				schema_hint TEXT,
				source TEXT,
				retained INTEGER NOT NULL DEFAULT 0 CHECK (retained IN (0, 1))
			);`,
			`CREATE TABLE IF NOT EXISTS device_twin_properties (
				device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
				path TEXT NOT NULL,
				value_json TEXT,
				value_type TEXT NOT NULL,
				source_event_id INTEGER REFERENCES device_events(id) ON DELETE SET NULL,
				ts_observed_ms INTEGER NOT NULL,
				ts_received_ms INTEGER NOT NULL,
				protocol TEXT NOT NULL,
				source_path TEXT,
				PRIMARY KEY (device_id, path)
			);`,
			`CREATE TABLE IF NOT EXISTS device_tasks (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
				task_type TEXT NOT NULL CHECK (task_type IN ('read', 'write', 'exec', 'fota')),
				parameter TEXT CHECK (parameter IS NULL OR length(parameter) <= 256),
				status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'success', 'failure', 'canceled')),
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				completed_at TEXT
			);`,
			`CREATE TABLE IF NOT EXISTS ota_deployments (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				organisation_id INTEGER NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				release_id INTEGER NOT NULL,
				target TEXT NOT NULL,
				status TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (organisation_id, release_id) REFERENCES software_releases(organisation_id, id) ON DELETE CASCADE
			);`,
			`CREATE TABLE IF NOT EXISTS sessions (
				id TEXT PRIMARY KEY,
				user_id INTEGER NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
				expires_at TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE INDEX IF NOT EXISTS organisation_users_user_id_idx ON organisation_users(user_id);`,
			`CREATE INDEX IF NOT EXISTS organisation_users_role_idx ON organisation_users(organisation_id, role);`,
			`CREATE INDEX IF NOT EXISTS organisation_invitations_lookup_idx ON organisation_invitations(organisation_id, email, accepted_at);`,
			`CREATE INDEX IF NOT EXISTS organisation_invitations_expires_at_idx ON organisation_invitations(expires_at);`,
			`CREATE INDEX IF NOT EXISTS devices_organisation_id_idx ON devices(organisation_id);`,
			`CREATE INDEX IF NOT EXISTS device_models_organisation_id_idx ON device_models(organisation_id);`,
			`CREATE INDEX IF NOT EXISTS device_events_device_time_idx ON device_events(device_id, ts_received_ms DESC);`,
			`CREATE INDEX IF NOT EXISTS device_events_path_time_idx ON device_events(device_id, coap_path, ts_received_ms DESC);`,
			`CREATE INDEX IF NOT EXISTS device_events_topic_time_idx ON device_events(device_id, topic, ts_received_ms DESC);`,
			`CREATE INDEX IF NOT EXISTS device_twin_properties_device_idx ON device_twin_properties(device_id);`,
			`CREATE INDEX IF NOT EXISTS device_twin_properties_path_idx ON device_twin_properties(path);`,
			`CREATE INDEX IF NOT EXISTS device_tasks_device_status_created_idx ON device_tasks(device_id, status, created_at DESC);`,
			`CREATE INDEX IF NOT EXISTS software_releases_organisation_id_idx ON software_releases(organisation_id);`,
			`CREATE INDEX IF NOT EXISTS ota_deployments_organisation_status_idx ON ota_deployments(organisation_id, status);`,
			`CREATE INDEX IF NOT EXISTS ota_deployments_status_idx ON ota_deployments(status);`,
			`CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions(user_id);`,
			`CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at);`,
		}, nil
	case DialectPostgres, DialectPostgreSQL:
		return []string{
			`CREATE TABLE IF NOT EXISTS app_users (
				id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
				email TEXT NOT NULL UNIQUE,
				name TEXT NOT NULL,
				password_hash TEXT NOT NULL,
				is_admin BOOLEAN NOT NULL DEFAULT FALSE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);`,
			`CREATE TABLE IF NOT EXISTS organisations (
				id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
				name TEXT NOT NULL UNIQUE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);`,
			`CREATE TABLE IF NOT EXISTS organisation_users (
				organisation_id BIGINT NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
				role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				PRIMARY KEY (organisation_id, user_id)
			);`,
			`CREATE TABLE IF NOT EXISTS organisation_invitations (
				id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
				organisation_id BIGINT NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				email TEXT NOT NULL,
				token_hash TEXT NOT NULL UNIQUE,
				expires_at TIMESTAMPTZ NOT NULL,
				accepted_at TIMESTAMPTZ,
				inviter_user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);`,
			`CREATE TABLE IF NOT EXISTS software_releases (
				id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
				organisation_id BIGINT NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				version TEXT NOT NULL,
				artifact_path TEXT NOT NULL,
				artifact_filename TEXT NOT NULL,
				artifact_content_type TEXT NOT NULL,
				artifact_size_bytes BIGINT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				UNIQUE (organisation_id, id),
				UNIQUE (organisation_id, name, version)
			);`,
			`CREATE TABLE IF NOT EXISTS device_models (
				id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
				organisation_id BIGINT NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				expected_heartbeat_seconds BIGINT NOT NULL CHECK (expected_heartbeat_seconds > 0),
				expected_protocol TEXT NOT NULL,
				expected_release_id BIGINT,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				UNIQUE (organisation_id, id),
				UNIQUE (organisation_id, name),
				FOREIGN KEY (organisation_id, expected_release_id) REFERENCES software_releases(organisation_id, id)
			);`,
			`CREATE TABLE IF NOT EXISTS devices (
				id TEXT PRIMARY KEY,
				organisation_id BIGINT NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				device_model_id BIGINT NOT NULL,
				software_versions JSONB NOT NULL DEFAULT '{}'::jsonb,
				is_gateway BOOLEAN NOT NULL DEFAULT FALSE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				FOREIGN KEY (organisation_id, device_model_id) REFERENCES device_models(organisation_id, id)
			);`,
			`CREATE TABLE IF NOT EXISTS mqtt_credentials (
				device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
				username TEXT NOT NULL UNIQUE,
				password_hash TEXT NOT NULL,
				enabled BOOLEAN NOT NULL DEFAULT TRUE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);`,
			`CREATE TABLE IF NOT EXISTS device_events (
				id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
				device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
				ts_received_ms BIGINT NOT NULL,
				protocol TEXT NOT NULL,
				direction TEXT NOT NULL,
				operation TEXT NOT NULL,
				topic TEXT,
				coap_path TEXT,
				method TEXT,
				code TEXT,
				content_format TEXT,
				payload_raw BYTEA,
				payload_json TEXT,
				correlation_id TEXT,
				schema_hint TEXT,
				source TEXT,
				retained BOOLEAN NOT NULL DEFAULT FALSE
			);`,
			`CREATE TABLE IF NOT EXISTS device_twin_properties (
				device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
				path TEXT NOT NULL,
				value_json TEXT,
				value_type TEXT NOT NULL,
				source_event_id BIGINT REFERENCES device_events(id) ON DELETE SET NULL,
				ts_observed_ms BIGINT NOT NULL,
				ts_received_ms BIGINT NOT NULL,
				protocol TEXT NOT NULL,
				source_path TEXT,
				PRIMARY KEY (device_id, path)
			);`,
			`CREATE TABLE IF NOT EXISTS device_tasks (
				id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
				device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
				task_type TEXT NOT NULL CHECK (task_type IN ('read', 'write', 'exec', 'fota')),
				parameter TEXT CHECK (parameter IS NULL OR length(parameter) <= 256),
				status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'success', 'failure', 'canceled')),
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				completed_at TIMESTAMPTZ
			);`,
			`CREATE TABLE IF NOT EXISTS ota_deployments (
				id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
				organisation_id BIGINT NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
				release_id BIGINT NOT NULL,
				target TEXT NOT NULL,
				status TEXT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				FOREIGN KEY (organisation_id, release_id) REFERENCES software_releases(organisation_id, id) ON DELETE CASCADE
			);`,
			`CREATE TABLE IF NOT EXISTS sessions (
				id TEXT PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
				expires_at TIMESTAMPTZ NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);`,
			`CREATE INDEX IF NOT EXISTS organisation_users_user_id_idx ON organisation_users(user_id);`,
			`CREATE INDEX IF NOT EXISTS organisation_users_role_idx ON organisation_users(organisation_id, role);`,
			`CREATE INDEX IF NOT EXISTS organisation_invitations_lookup_idx ON organisation_invitations(organisation_id, email, accepted_at);`,
			`CREATE INDEX IF NOT EXISTS organisation_invitations_expires_at_idx ON organisation_invitations(expires_at);`,
			`CREATE INDEX IF NOT EXISTS devices_organisation_id_idx ON devices(organisation_id);`,
			`CREATE INDEX IF NOT EXISTS device_models_organisation_id_idx ON device_models(organisation_id);`,
			`CREATE INDEX IF NOT EXISTS device_events_device_time_idx ON device_events(device_id, ts_received_ms DESC);`,
			`CREATE INDEX IF NOT EXISTS device_events_path_time_idx ON device_events(device_id, coap_path, ts_received_ms DESC);`,
			`CREATE INDEX IF NOT EXISTS device_events_topic_time_idx ON device_events(device_id, topic, ts_received_ms DESC);`,
			`CREATE INDEX IF NOT EXISTS device_twin_properties_device_idx ON device_twin_properties(device_id);`,
			`CREATE INDEX IF NOT EXISTS device_twin_properties_path_idx ON device_twin_properties(path);`,
			`CREATE INDEX IF NOT EXISTS device_tasks_device_status_created_idx ON device_tasks(device_id, status, created_at DESC);`,
			`CREATE INDEX IF NOT EXISTS software_releases_organisation_id_idx ON software_releases(organisation_id);`,
			`CREATE INDEX IF NOT EXISTS ota_deployments_organisation_status_idx ON ota_deployments(organisation_id, status);`,
			`CREATE INDEX IF NOT EXISTS ota_deployments_status_idx ON ota_deployments(status);`,
			`CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions(user_id);`,
			`CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at);`,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported db dialect %q", d)
	}
}

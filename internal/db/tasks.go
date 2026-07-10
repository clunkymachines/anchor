package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"anchor/internal/domain"
)

const (
	DeviceTaskStatusPending    = "pending"
	DeviceTaskStatusInProgress = "in_progress"
	DeviceTaskStatusSuccess    = "success"
	DeviceTaskStatusFailure    = "failure"
	DeviceTaskStatusCanceled   = "canceled"
)

func (s *Store) CreateDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) (int64, error) {
	if task.Status == "" {
		task.Status = DeviceTaskStatusPending
	}
	if strings.TrimSpace(task.ParametersJSON) == "" {
		return 0, fmt.Errorf("device task parameters_json is required")
	}

	switch s.dialect {
	case DialectSQLite:
		result, err := s.writeDB.ExecContext(ctx, `
			INSERT INTO device_tasks (device_id, task_type, parameters_json, status, status_message, created_at)
			SELECT d.id, ?, ?, ?, ?, ?
			FROM devices d
			WHERE d.id = ? AND d.organisation_id = ?
		`,
			task.Type,
			task.ParametersJSON,
			task.Status,
			nullableStatusMessage(task.StatusMessage),
			task.CreatedAt,
			task.DeviceID,
			organisationID,
		)
		if err != nil {
			return 0, err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if rowsAffected == 0 {
			return 0, ErrNotFound
		}
		id, err := result.LastInsertId()
		if err != nil {
			return 0, err
		}
		s.tasks.publish(task.DeviceID)
		return id, nil
	case DialectPostgres, DialectPostgreSQL:
		var id int64
		err := s.writeDB.QueryRowContext(ctx, `
			INSERT INTO device_tasks (device_id, task_type, parameters_json, status, status_message, created_at)
			SELECT d.id, $1, $2, $3, $4, $5
			FROM devices d
			WHERE d.id = $6 AND d.organisation_id = $7
			RETURNING id
		`,
			task.Type,
			task.ParametersJSON,
			task.Status,
			nullableStatusMessage(task.StatusMessage),
			task.CreatedAt,
			task.DeviceID,
			organisationID,
		).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, err
		}
		s.tasks.publish(task.DeviceID)
		return id, nil
	default:
		return 0, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) ListOngoingDeviceTasks(ctx context.Context, deviceID string, organisationID int64) ([]domain.DeviceTask, error) {
	query := `
		SELECT t.id, t.device_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.completed_at
		FROM device_tasks t
		JOIN devices d ON d.id = t.device_id
		WHERE t.device_id = ?
			AND d.organisation_id = ?
			AND t.status IN ('pending', 'in_progress')
		ORDER BY t.created_at DESC, t.id DESC
	`
	args := []any{deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT t.id, t.device_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.completed_at
			FROM device_tasks t
			JOIN devices d ON d.id = t.device_id
			WHERE t.device_id = $1
				AND d.organisation_id = $2
				AND t.status IN ('pending', 'in_progress')
			ORDER BY t.created_at DESC, t.id DESC
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []domain.DeviceTask
	for rows.Next() {
		task, err := scanDeviceTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tasks, nil
}

func (s *Store) ListPendingDeviceTasks(ctx context.Context, deviceID string, organisationID int64) ([]domain.DeviceTask, error) {
	query := `
		SELECT t.id, t.device_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.completed_at
		FROM device_tasks t
		JOIN devices d ON d.id = t.device_id
		WHERE t.device_id = ?
			AND d.organisation_id = ?
			AND t.status = 'pending'
		ORDER BY t.created_at DESC, t.id DESC
	`
	args := []any{deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT t.id, t.device_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.completed_at
			FROM device_tasks t
			JOIN devices d ON d.id = t.device_id
			WHERE t.device_id = $1
				AND d.organisation_id = $2
				AND t.status = 'pending'
			ORDER BY t.created_at DESC, t.id DESC
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []domain.DeviceTask
	for rows.Next() {
		task, err := scanDeviceTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tasks, nil
}

func (s *Store) ListActiveAndRecentDeviceTasks(ctx context.Context, deviceID string, organisationID int64, completedLimit int) ([]domain.DeviceTask, error) {
	if completedLimit <= 0 {
		completedLimit = 3
	}

	query := `
		SELECT t.id, t.device_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.completed_at
		FROM device_tasks t
		JOIN devices d ON d.id = t.device_id
		WHERE t.device_id = ?
			AND d.organisation_id = ?
			AND (
				t.status IN ('pending', 'in_progress')
				OR t.id IN (
					SELECT finished.id
					FROM device_tasks finished
					WHERE finished.device_id = ?
						AND finished.status IN ('success', 'failure', 'canceled')
					ORDER BY finished.completed_at DESC, finished.created_at DESC, finished.id DESC
					LIMIT ?
				)
			)
		ORDER BY
			CASE WHEN t.status IN ('pending', 'in_progress') THEN 0 ELSE 1 END,
			CASE WHEN t.status IN ('pending', 'in_progress') THEN t.created_at ELSE COALESCE(t.completed_at, t.created_at) END DESC,
			t.id DESC
	`
	args := []any{deviceID, organisationID, deviceID, completedLimit}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT t.id, t.device_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.completed_at
			FROM device_tasks t
			JOIN devices d ON d.id = t.device_id
			WHERE t.device_id = $1
				AND d.organisation_id = $2
				AND (
					t.status IN ('pending', 'in_progress')
					OR t.id IN (
						SELECT finished.id
						FROM device_tasks finished
						WHERE finished.device_id = $3
							AND finished.status IN ('success', 'failure', 'canceled')
						ORDER BY finished.completed_at DESC, finished.created_at DESC, finished.id DESC
						LIMIT $4
					)
				)
			ORDER BY
				CASE WHEN t.status IN ('pending', 'in_progress') THEN 0 ELSE 1 END,
				CASE WHEN t.status IN ('pending', 'in_progress') THEN t.created_at ELSE COALESCE(t.completed_at, t.created_at) END DESC,
				t.id DESC
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []domain.DeviceTask
	for rows.Next() {
		task, err := scanDeviceTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tasks, nil
}

func (s *Store) UpdateDeviceTaskStatus(ctx context.Context, taskID int64, deviceID string, organisationID int64, status string, completedAt string, statusMessage string) error {
	query := `
		UPDATE device_tasks
		SET status = ?, completed_at = ?, status_message = ?
		WHERE id = ?
			AND device_id = ?
			AND status NOT IN ('success', 'failure', 'canceled')
			AND EXISTS (
				SELECT 1
				FROM devices d
				WHERE d.id = device_tasks.device_id AND d.organisation_id = ?
			)
	`
	args := []any{status, nullableCompletedAt(completedAt), nullableStatusMessage(statusMessage), taskID, deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			UPDATE device_tasks
			SET status = $1, completed_at = $2, status_message = $3
			WHERE id = $4
				AND device_id = $5
				AND status NOT IN ('success', 'failure', 'canceled')
				AND EXISTS (
					SELECT 1
					FROM devices d
					WHERE d.id = device_tasks.device_id AND d.organisation_id = $6
				)
		`
	}

	result, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	s.tasks.publish(deviceID)
	return nil
}

func scanDeviceTask(rows *sql.Rows) (domain.DeviceTask, error) {
	var (
		task          domain.DeviceTask
		statusMessage nullableString
		completedAt   nullableString
	)
	if err := rows.Scan(
		&task.ID,
		&task.DeviceID,
		&task.Type,
		&task.ParametersJSON,
		&task.Status,
		&statusMessage,
		&task.CreatedAt,
		&completedAt,
	); err != nil {
		return domain.DeviceTask{}, err
	}
	if statusMessage.Valid {
		task.StatusMessage = statusMessage.String
	}
	if completedAt.Valid {
		task.CompletedAt = completedAt.String
	}
	return task, nil
}

func nullableStatusMessage(message string) any {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	runes := []rune(message)
	if len(runes) > 512 {
		return string(runes[:512])
	}
	return message
}

func nullableCompletedAt(completedAt string) any {
	if completedAt == "" {
		return nil
	}
	return completedAt
}

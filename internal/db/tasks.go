package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"anchor/internal/domain"
)

const (
	DeviceTaskStatusQueued     = domain.TaskStatusQueued
	DeviceTaskStatusPending    = domain.TaskStatusPending
	DeviceTaskStatusInProgress = domain.TaskStatusInProgress
	DeviceTaskStatusSuccess    = domain.TaskStatusSuccess
	DeviceTaskStatusFailure    = domain.TaskStatusFailure
	DeviceTaskStatusExpired    = domain.TaskStatusExpired
	DeviceTaskStatusCanceled   = domain.TaskStatusCanceled
)

type TaskTransitionOutcome string

const (
	TaskTransitionChanged  TaskTransitionOutcome = "changed"
	TaskTransitionIgnored  TaskTransitionOutcome = "ignored"
	TaskTransitionNotFound TaskTransitionOutcome = "not_found"
)

type CreateDeviceTaskOptions struct {
	Task        domain.DeviceTask
	TTLSeconds  int64
	CampaignID  *int64
	CreatedTime time.Time
}

type PromotedTask struct {
	Task           domain.DeviceTask
	OrganisationID int64
}

func (s *Store) CreateDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) (int64, error) {
	createdAt, err := parseOptionalRFC3339(task.CreatedAt, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	ttlSeconds := int64(domain.DefaultTaskTTLDays * domain.SecondsPerDay)
	created, err := s.CreateQueuedDeviceTask(ctx, organisationID, CreateDeviceTaskOptions{
		Task:        task,
		TTLSeconds:  ttlSeconds,
		CampaignID:  task.CampaignID,
		CreatedTime: createdAt,
	})
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

func (s *Store) CreateQueuedDeviceTask(ctx context.Context, organisationID int64, opts CreateDeviceTaskOptions) (domain.DeviceTask, error) {
	if opts.CreatedTime.IsZero() {
		opts.CreatedTime = time.Now().UTC()
	}
	if opts.TTLSeconds <= 0 {
		opts.TTLSeconds = int64(domain.DefaultTaskTTLDays * domain.SecondsPerDay)
	}
	expiresAt, err := domain.TaskExpiresAt(opts.CreatedTime, opts.TTLSeconds)
	if err != nil {
		return domain.DeviceTask{}, err
	}
	task := opts.Task
	task.Status = ""
	task.CampaignID = opts.CampaignID
	task.CreatedAt = formatTime(opts.CreatedTime)
	task.ExpiresAt = formatTime(expiresAt)

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return domain.DeviceTask{}, err
	}
	defer tx.Rollback()

	created, err := s.createDeviceTaskTx(ctx, tx, organisationID, task)
	if err != nil {
		return domain.DeviceTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.DeviceTask{}, err
	}
	s.tasks.publish(created.DeviceID)
	return created, nil
}

func (s *Store) createDeviceTaskTx(ctx context.Context, tx *sql.Tx, organisationID int64, task domain.DeviceTask) (domain.DeviceTask, error) {
	if strings.TrimSpace(task.ParametersJSON) == "" {
		return domain.DeviceTask{}, fmt.Errorf("device task parameters_json is required")
	}
	if strings.TrimSpace(task.Type) == "" {
		return domain.DeviceTask{}, fmt.Errorf("device task type is required")
	}
	if strings.TrimSpace(task.CreatedAt) == "" || strings.TrimSpace(task.ExpiresAt) == "" {
		return domain.DeviceTask{}, fmt.Errorf("device task created_at and expires_at are required")
	}

	if err := s.lockDeviceForTask(ctx, tx, task.DeviceID, organisationID); err != nil {
		return domain.DeviceTask{}, err
	}
	status, err := s.nextTaskStatusTx(ctx, tx, task.DeviceID)
	if err != nil {
		return domain.DeviceTask{}, err
	}
	task.Status = status

	switch s.dialect {
	case DialectSQLite:
		result, err := tx.ExecContext(ctx, `
			INSERT INTO device_tasks (device_id, campaign_id, task_type, parameters_json, status, status_message, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`,
			task.DeviceID,
			nullableInt64Ptr(task.CampaignID),
			task.Type,
			task.ParametersJSON,
			task.Status,
			nullableStatusMessage(task.StatusMessage),
			task.CreatedAt,
			task.ExpiresAt,
		)
		if err != nil {
			if isUniqueConstraintError(err) {
				return domain.DeviceTask{}, ErrConflict
			}
			return domain.DeviceTask{}, err
		}
		task.ID, err = result.LastInsertId()
		return task, err
	case DialectPostgres, DialectPostgreSQL:
		err := tx.QueryRowContext(ctx, `
			INSERT INTO device_tasks (device_id, campaign_id, task_type, parameters_json, status, status_message, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id
		`,
			task.DeviceID,
			nullableInt64Ptr(task.CampaignID),
			task.Type,
			task.ParametersJSON,
			task.Status,
			nullableStatusMessage(task.StatusMessage),
			task.CreatedAt,
			task.ExpiresAt,
		).Scan(&task.ID)
		if err != nil {
			if isUniqueConstraintError(err) {
				return domain.DeviceTask{}, ErrConflict
			}
			return domain.DeviceTask{}, err
		}
		return task, nil
	default:
		return domain.DeviceTask{}, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) lockDeviceForTask(ctx context.Context, tx *sql.Tx, deviceID string, organisationID int64) error {
	query := `SELECT id FROM devices WHERE id = ? AND organisation_id = ?`
	args := []any{deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT id FROM devices WHERE id = $1 AND organisation_id = $2 FOR UPDATE`
	}
	var found string
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Store) nextTaskStatusTx(ctx context.Context, tx *sql.Tx, deviceID string) (string, error) {
	query := `SELECT COUNT(*) FROM device_tasks WHERE device_id = ? AND status IN ('pending', 'in_progress')`
	args := []any{deviceID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `SELECT COUNT(*) FROM device_tasks WHERE device_id = $1 AND status IN ('pending', 'in_progress')`
	}
	var active int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&active); err != nil {
		return "", err
	}
	if active == 0 {
		return DeviceTaskStatusPending, nil
	}
	return DeviceTaskStatusQueued, nil
}

func (s *Store) ListOngoingDeviceTasks(ctx context.Context, deviceID string, organisationID int64) ([]domain.DeviceTask, error) {
	return s.listDeviceTasks(ctx, deviceID, organisationID, "t.status IN ('queued', 'pending', 'in_progress')", "t.created_at DESC, t.id DESC", 0)
}

func (s *Store) ListPendingDeviceTasks(ctx context.Context, deviceID string, organisationID int64) ([]domain.DeviceTask, error) {
	return s.listDeviceTasks(ctx, deviceID, organisationID, "t.status = 'pending'", "t.created_at ASC, t.id ASC", 0)
}

func (s *Store) ListActiveAndRecentDeviceTasks(ctx context.Context, deviceID string, organisationID int64, completedLimit int) ([]domain.DeviceTask, error) {
	if completedLimit <= 0 {
		completedLimit = 3
	}
	query := `
		SELECT t.id, t.device_id, t.campaign_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.expires_at, t.completed_at
		FROM device_tasks t
		JOIN devices d ON d.id = t.device_id
		WHERE t.device_id = ?
			AND d.organisation_id = ?
			AND (
				t.status IN ('queued', 'pending', 'in_progress')
				OR t.id IN (
					SELECT finished.id
					FROM device_tasks finished
					WHERE finished.device_id = ?
						AND finished.status IN ('success', 'failure', 'expired', 'canceled')
					ORDER BY finished.completed_at DESC, finished.created_at DESC, finished.id DESC
					LIMIT ?
				)
			)
		ORDER BY
			CASE WHEN t.status IN ('queued', 'pending', 'in_progress') THEN 0 ELSE 1 END,
			CASE WHEN t.status IN ('queued', 'pending', 'in_progress') THEN t.created_at ELSE COALESCE(t.completed_at, t.created_at) END DESC,
			t.id DESC
	`
	args := []any{deviceID, organisationID, deviceID, completedLimit}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = numberedPlaceholders(query)
	}
	return s.queryDeviceTasks(ctx, query, args...)
}

func (s *Store) listDeviceTasks(ctx context.Context, deviceID string, organisationID int64, predicate string, order string, limit int) ([]domain.DeviceTask, error) {
	query := fmt.Sprintf(`
		SELECT t.id, t.device_id, t.campaign_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.expires_at, t.completed_at
		FROM device_tasks t
		JOIN devices d ON d.id = t.device_id
		WHERE t.device_id = ?
			AND d.organisation_id = ?
			AND %s
		ORDER BY %s
	`, predicate, order)
	args := []any{deviceID, organisationID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = numberedPlaceholders(query)
	}
	return s.queryDeviceTasks(ctx, query, args...)
}

func (s *Store) queryDeviceTasks(ctx context.Context, query string, args ...any) ([]domain.DeviceTask, error) {
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
	return tasks, rows.Err()
}

func (s *Store) UpdateDeviceTaskStatus(ctx context.Context, taskID int64, deviceID string, organisationID int64, status string, completedAt string, statusMessage string) error {
	outcome, err := s.ApplyDeviceTaskReport(ctx, taskID, deviceID, organisationID, status, completedAt, statusMessage)
	if err != nil {
		return err
	}
	if outcome == TaskTransitionNotFound {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ApplyDeviceTaskReport(ctx context.Context, taskID int64, deviceID string, organisationID int64, status string, completedAt string, statusMessage string) (TaskTransitionOutcome, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return TaskTransitionNotFound, err
	}
	defer tx.Rollback()

	task, err := s.deviceTaskForUpdate(ctx, tx, taskID, deviceID, organisationID)
	if errors.Is(err, ErrNotFound) {
		return TaskTransitionNotFound, nil
	}
	if err != nil {
		return TaskTransitionNotFound, err
	}
	if !domain.DeviceReportAllowed(task.Status, status) {
		return TaskTransitionIgnored, tx.Commit()
	}
	if status == DeviceTaskStatusSuccess || status == DeviceTaskStatusFailure {
		if completedAt == "" {
			completedAt = formatTime(time.Now().UTC())
		}
	} else {
		completedAt = ""
	}

	query := `UPDATE device_tasks SET status = ?, completed_at = ?, status_message = ? WHERE id = ? AND status = ?`
	args := []any{status, nullableCompletedAt(completedAt), nullableStatusMessage(statusMessage), taskID, task.Status}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `UPDATE device_tasks SET status = $1, completed_at = $2, status_message = $3 WHERE id = $4 AND status = $5`
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return TaskTransitionNotFound, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return TaskTransitionNotFound, err
	}
	if rows == 0 {
		return TaskTransitionIgnored, tx.Commit()
	}
	if err := tx.Commit(); err != nil {
		return TaskTransitionNotFound, err
	}
	s.tasks.publish(deviceID)
	return TaskTransitionChanged, nil
}

func (s *Store) CancelDeviceTask(ctx context.Context, taskID int64, deviceID string, organisationID int64, completedAt time.Time, message string) (TaskTransitionOutcome, error) {
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	query := `
		UPDATE device_tasks
		SET status = ?, completed_at = ?, status_message = ?
		WHERE id = ?
			AND device_id = ?
			AND status IN ('queued', 'pending', 'in_progress')
			AND EXISTS (
				SELECT 1 FROM devices d WHERE d.id = device_tasks.device_id AND d.organisation_id = ?
			)
	`
	args := []any{DeviceTaskStatusCanceled, formatTime(completedAt), nullableStatusMessage(message), taskID, deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			UPDATE device_tasks
			SET status = $1, completed_at = $2, status_message = $3
			WHERE id = $4
				AND device_id = $5
				AND status IN ('queued', 'pending', 'in_progress')
				AND EXISTS (
					SELECT 1 FROM devices d WHERE d.id = device_tasks.device_id AND d.organisation_id = $6
				)
		`
	}
	result, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		return TaskTransitionNotFound, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return TaskTransitionNotFound, err
	}
	if rows == 0 {
		return TaskTransitionNotFound, nil
	}
	s.tasks.publish(deviceID)
	return TaskTransitionChanged, nil
}

func (s *Store) ExpireOverdueDeviceTasks(ctx context.Context, now time.Time) (int64, error) {
	query := `
		UPDATE device_tasks
		SET status = 'expired',
			completed_at = ?,
			status_message = 'Task expired without terminal device status.'
		WHERE status IN ('queued', 'pending', 'in_progress') AND expires_at <= ?
	`
	args := []any{formatTime(now), formatTime(now)}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			UPDATE device_tasks
			SET status = 'expired',
				completed_at = $1,
				status_message = 'Task expired without terminal device status.'
			WHERE status IN ('queued', 'pending', 'in_progress') AND expires_at <= $2
		`
	}
	result, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) PromoteQueuedDeviceTasks(ctx context.Context, now time.Time) ([]PromotedTask, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	query := `
		SELECT q.id, q.device_id
		FROM device_tasks q
		JOIN devices d ON d.id = q.device_id
		WHERE q.status = 'queued'
			AND q.expires_at > ?
			AND NOT EXISTS (
				SELECT 1 FROM device_tasks active
				WHERE active.device_id = q.device_id AND active.status IN ('pending', 'in_progress')
			)
			AND q.id = (
				SELECT q2.id
				FROM device_tasks q2
				WHERE q2.device_id = q.device_id AND q2.status = 'queued' AND q2.expires_at > ?
				ORDER BY q2.created_at ASC, q2.id ASC
				LIMIT 1
			)
		ORDER BY q.device_id
	`
	args := []any{formatTime(now), formatTime(now)}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = numberedPlaceholders(query)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var deviceID string
		if err := rows.Scan(&id, &deviceID); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	promoted := make([]PromotedTask, 0, len(ids))
	for _, id := range ids {
		update := `UPDATE device_tasks SET status = 'pending' WHERE id = ? AND status = 'queued'`
		args := []any{id}
		if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
			update = `UPDATE device_tasks SET status = 'pending' WHERE id = $1 AND status = 'queued'`
		}
		result, err := tx.ExecContext(ctx, update, args...)
		if err != nil {
			return nil, err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rowsAffected == 0 {
			continue
		}
		task, organisationID, err := s.deviceTaskWithOrganisationTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		promoted = append(promoted, PromotedTask{Task: task, OrganisationID: organisationID})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, task := range promoted {
		s.tasks.publish(task.Task.DeviceID)
	}
	return promoted, nil
}

func (s *Store) deviceTaskForUpdate(ctx context.Context, tx *sql.Tx, taskID int64, deviceID string, organisationID int64) (domain.DeviceTask, error) {
	query := `
		SELECT t.id, t.device_id, t.campaign_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.expires_at, t.completed_at
		FROM device_tasks t
		JOIN devices d ON d.id = t.device_id
		WHERE t.id = ? AND t.device_id = ? AND d.organisation_id = ?
	`
	args := []any{taskID, deviceID, organisationID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT t.id, t.device_id, t.campaign_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at::text, t.expires_at::text, t.completed_at::text
			FROM device_tasks t
			JOIN devices d ON d.id = t.device_id
			WHERE t.id = $1 AND t.device_id = $2 AND d.organisation_id = $3
			FOR UPDATE
		`
	}
	task, err := scanDeviceTask(tx.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DeviceTask{}, ErrNotFound
	}
	return task, err
}

func (s *Store) deviceTaskWithOrganisationTx(ctx context.Context, tx *sql.Tx, taskID int64) (domain.DeviceTask, int64, error) {
	query := `
		SELECT t.id, t.device_id, t.campaign_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.expires_at, t.completed_at, d.organisation_id
		FROM device_tasks t
		JOIN devices d ON d.id = t.device_id
		WHERE t.id = ?
	`
	args := []any{taskID}
	if s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL {
		query = `
			SELECT t.id, t.device_id, t.campaign_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at::text, t.expires_at::text, t.completed_at::text, d.organisation_id
			FROM device_tasks t
			JOIN devices d ON d.id = t.device_id
			WHERE t.id = $1
		`
	}
	var organisationID int64
	task, err := scanDeviceTaskWithOrganisation(tx.QueryRowContext(ctx, query, args...), &organisationID)
	return task, organisationID, err
}

type deviceTaskScanner interface {
	Scan(dest ...any) error
}

func scanDeviceTask(row deviceTaskScanner) (domain.DeviceTask, error) {
	var (
		task          domain.DeviceTask
		campaignID    nullableInt64Value
		statusMessage nullableString
		completedAt   nullableString
	)
	if err := row.Scan(
		&task.ID,
		&task.DeviceID,
		&campaignID,
		&task.Type,
		&task.ParametersJSON,
		&task.Status,
		&statusMessage,
		&task.CreatedAt,
		&task.ExpiresAt,
		&completedAt,
	); err != nil {
		return domain.DeviceTask{}, err
	}
	if campaignID.Valid {
		task.CampaignID = &campaignID.Int64
	}
	if statusMessage.Valid {
		task.StatusMessage = statusMessage.String
	}
	if completedAt.Valid {
		task.CompletedAt = completedAt.String
	}
	return task, nil
}

func scanDeviceTaskWithOrganisation(row deviceTaskScanner, organisationID *int64) (domain.DeviceTask, error) {
	var (
		task          domain.DeviceTask
		campaignID    nullableInt64Value
		statusMessage nullableString
		completedAt   nullableString
	)
	if err := row.Scan(
		&task.ID,
		&task.DeviceID,
		&campaignID,
		&task.Type,
		&task.ParametersJSON,
		&task.Status,
		&statusMessage,
		&task.CreatedAt,
		&task.ExpiresAt,
		&completedAt,
		organisationID,
	); err != nil {
		return domain.DeviceTask{}, err
	}
	if campaignID.Valid {
		task.CampaignID = &campaignID.Int64
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

func nullableInt64Ptr(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

type nullableInt64Value struct {
	Int64 int64
	Valid bool
}

func (n *nullableInt64Value) Scan(value any) error {
	if value == nil {
		n.Int64 = 0
		n.Valid = false
		return nil
	}
	switch typed := value.(type) {
	case int64:
		n.Int64 = typed
	case int:
		n.Int64 = int64(typed)
	case []byte:
		parsed, err := parseInt64Bytes(typed)
		if err != nil {
			return err
		}
		n.Int64 = parsed
	case string:
		parsed, err := parseInt64Bytes([]byte(typed))
		if err != nil {
			return err
		}
		n.Int64 = parsed
	default:
		return fmt.Errorf("cannot scan %T into nullable int64", value)
	}
	n.Valid = true
	return nil
}

func parseInt64Bytes(value []byte) (int64, error) {
	var parsed int64
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid int64 %q", string(value))
		}
		parsed = parsed*10 + int64(ch-'0')
	}
	return parsed, nil
}

func parseOptionalRFC3339(value string, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func numberedPlaceholders(query string) string {
	var builder strings.Builder
	arg := 1
	for _, ch := range query {
		if ch == '?' {
			builder.WriteString(fmt.Sprintf("$%d", arg))
			arg++
			continue
		}
		builder.WriteRune(ch)
	}
	return builder.String()
}

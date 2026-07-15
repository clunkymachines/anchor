package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"anchor/internal/domain"
)

const (
	// CampaignStatusRunning aliases the domain running status for persistence callers.
	CampaignStatusRunning = domain.CampaignStatusRunning
	// CampaignStatusFinished aliases the domain finished status for persistence callers.
	CampaignStatusFinished = domain.CampaignStatusFinished
	// CampaignStatusCanceled aliases the domain canceled status for persistence callers.
	CampaignStatusCanceled = domain.CampaignStatusCanceled
	// MaxCampaignTargets limits devices selected for one campaign.
	MaxCampaignTargets = 100
)

// CampaignCreate contains campaign metadata and target device IDs to validate and persist.
type CampaignCreate struct {
	OrganisationID int64
	Name           string
	TaskType       string
	ParametersJSON string
	TTLSeconds     int64
	DeviceIDs      []string
	CreatedAt      time.Time
}

// CampaignCreateResult contains the created campaign and tasks immediately ready
// for publication; remaining tasks stay queued per device.
type CampaignCreateResult struct {
	Campaign     domain.Campaign
	PendingTasks []domain.DeviceTask
}

// CampaignTaskQuery filters and paginates tasks belonging to one campaign.
type CampaignTaskQuery struct {
	OrganisationID int64
	CampaignID     int64
	Status         string
	Page           int
	PageSize       int
}

// CampaignTaskPage contains one page of campaign tasks and its pagination metadata.
type CampaignTaskPage struct {
	Rows       []domain.CampaignTaskRow
	Pagination Pagination
}

// CampaignTargetDevices validates that every requested device belongs to the
// organisation and returns devices in the same order as deviceIDs.
func (s *Store) CampaignTargetDevices(ctx context.Context, organisationID int64, deviceIDs []string) ([]domain.Device, error) {
	if err := validateCampaignDeviceIDs(deviceIDs); err != nil {
		return nil, err
	}
	placeholders := make([]string, len(deviceIDs))
	args := make([]any, 0, len(deviceIDs)+1)
	args = append(args, organisationID)
	for i, id := range deviceIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf(`
		SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds, d.software_versions, d.is_gateway
		FROM devices d
		JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
		WHERE d.organisation_id = ? AND d.id IN (%s)
	`, strings.Join(placeholders, ","))
	if s.isPostgres() {
		query = numberedPlaceholders(query)
	}
	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[string]domain.Device, len(deviceIDs))
	for rows.Next() {
		var device domain.Device
		var versions softwareVersionsValue
		if err := rows.Scan(&device.ID, &device.OrganisationID, &device.DeviceModelID, &device.ModelName, &device.ExpectedHeartbeatSeconds, &versions, &device.IsGateway); err != nil {
			return nil, err
		}
		device.SoftwareVersions = domain.SoftwareVersions(versions)
		byID[device.ID] = device
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(byID) != len(deviceIDs) {
		return nil, ErrNotFound
	}
	devices := make([]domain.Device, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		devices = append(devices, byID[id])
	}
	return devices, nil
}

// CreateCampaign atomically creates a running campaign and one scheduled task
// per target device, then notifies task subscribers.
func (s *Store) CreateCampaign(ctx context.Context, input CampaignCreate) (CampaignCreateResult, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return CampaignCreateResult{}, errors.New("campaign name is required")
	}
	if strings.TrimSpace(input.ParametersJSON) == "" {
		return CampaignCreateResult{}, errors.New("campaign parameters are required")
	}
	if input.TTLSeconds <= 0 {
		return CampaignCreateResult{}, errors.New("campaign task TTL is required")
	}
	if err := validateCampaignDeviceIDs(input.DeviceIDs); err != nil {
		return CampaignCreateResult{}, err
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC()
	}
	expiresAt, err := domain.TaskExpiresAt(input.CreatedAt, input.TTLSeconds)
	if err != nil {
		return CampaignCreateResult{}, err
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return CampaignCreateResult{}, err
	}
	defer tx.Rollback()

	deviceIDs := append([]string(nil), input.DeviceIDs...)
	sort.Strings(deviceIDs)
	for _, deviceID := range deviceIDs {
		if err := s.lockDeviceForTask(ctx, tx, deviceID, input.OrganisationID); err != nil {
			return CampaignCreateResult{}, err
		}
	}
	devices, err := s.campaignTargetDevicesTx(ctx, tx, input.OrganisationID, input.DeviceIDs)
	if err != nil {
		return CampaignCreateResult{}, err
	}

	campaign := domain.Campaign{
		OrganisationID: input.OrganisationID,
		Name:           input.Name,
		TaskType:       input.TaskType,
		ParametersJSON: input.ParametersJSON,
		TaskTTLSeconds: input.TTLSeconds,
		Status:         CampaignStatusRunning,
		CreatedAt:      formatTime(input.CreatedAt),
	}
	campaign.ID, err = s.insertCampaignTx(ctx, tx, campaign)
	if err != nil {
		return CampaignCreateResult{}, err
	}

	pending := make([]domain.DeviceTask, 0, len(input.DeviceIDs))
	for _, device := range devices {
		campaignID := campaign.ID
		task, err := s.createDeviceTaskTx(ctx, tx, input.OrganisationID, domain.DeviceTask{
			DeviceID:       device.ID,
			CampaignID:     &campaignID,
			Type:           input.TaskType,
			ParametersJSON: input.ParametersJSON,
			CreatedAt:      formatTime(input.CreatedAt),
			ExpiresAt:      formatTime(expiresAt),
		})
		if err != nil {
			return CampaignCreateResult{}, err
		}
		if task.Status == DeviceTaskStatusPending {
			pending = append(pending, task)
		}
	}
	if err := tx.Commit(); err != nil {
		return CampaignCreateResult{}, err
	}
	for _, deviceID := range input.DeviceIDs {
		s.tasks.publish(deviceID)
	}
	campaign.TargetCount = len(input.DeviceIDs)
	return CampaignCreateResult{Campaign: campaign, PendingTasks: pending}, nil
}

func (s *Store) insertCampaignTx(ctx context.Context, tx *sql.Tx, campaign domain.Campaign) (int64, error) {
	switch s.dialect {
	case DialectSQLite:
		result, err := tx.ExecContext(ctx, `
			INSERT INTO campaigns (organisation_id, name, task_type, parameters_json, task_ttl_seconds, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, campaign.OrganisationID, campaign.Name, campaign.TaskType, campaign.ParametersJSON, campaign.TaskTTLSeconds, campaign.Status, campaign.CreatedAt)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	case DialectPostgres, DialectPostgreSQL:
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO campaigns (organisation_id, name, task_type, parameters_json, task_ttl_seconds, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id
		`, campaign.OrganisationID, campaign.Name, campaign.TaskType, campaign.ParametersJSON, campaign.TaskTTLSeconds, campaign.Status, campaign.CreatedAt).Scan(&id)
		return id, err
	default:
		return 0, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) campaignTargetDevicesTx(ctx context.Context, tx *sql.Tx, organisationID int64, deviceIDs []string) ([]domain.Device, error) {
	placeholders := make([]string, len(deviceIDs))
	args := make([]any, 0, len(deviceIDs)+1)
	args = append(args, organisationID)
	for i, id := range deviceIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf(`
		SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds, d.software_versions, d.is_gateway
		FROM devices d
		JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
		WHERE d.organisation_id = ? AND d.id IN (%s)
	`, strings.Join(placeholders, ","))
	if s.isPostgres() {
		query = numberedPlaceholders(query)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string]domain.Device, len(deviceIDs))
	for rows.Next() {
		var device domain.Device
		var versions softwareVersionsValue
		if err := rows.Scan(&device.ID, &device.OrganisationID, &device.DeviceModelID, &device.ModelName, &device.ExpectedHeartbeatSeconds, &versions, &device.IsGateway); err != nil {
			return nil, err
		}
		device.SoftwareVersions = domain.SoftwareVersions(versions)
		byID[device.ID] = device
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(byID) != len(deviceIDs) {
		return nil, ErrNotFound
	}
	devices := make([]domain.Device, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		devices = append(devices, byID[id])
	}
	return devices, nil
}

func validateCampaignDeviceIDs(deviceIDs []string) error {
	if len(deviceIDs) == 0 {
		return errors.New("select at least one device")
	}
	if len(deviceIDs) > MaxCampaignTargets {
		return errors.New("campaigns can target at most 100 devices")
	}
	seen := make(map[string]struct{}, len(deviceIDs))
	for _, id := range deviceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return errors.New("device id is required")
		}
		if _, ok := seen[id]; ok {
			return errors.New("duplicate selected device")
		}
		seen[id] = struct{}{}
	}
	return nil
}

// ListCampaigns returns an organisation's campaigns newest first with task counts.
func (s *Store) ListCampaigns(ctx context.Context, organisationID int64) ([]domain.Campaign, error) {
	query := `
		SELECT c.id, c.organisation_id, c.name, c.task_type, c.parameters_json, c.task_ttl_seconds, c.status, c.created_at, c.finished_at, c.canceled_at,
			COUNT(t.id),
			COALESCE(SUM(CASE WHEN t.status = 'queued' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'in_progress' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'success' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'failure' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'expired' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'canceled' THEN 1 ELSE 0 END), 0)
		FROM campaigns c
		LEFT JOIN device_tasks t ON t.campaign_id = c.id
		WHERE c.organisation_id = ?
		GROUP BY c.id, c.organisation_id, c.name, c.task_type, c.parameters_json, c.task_ttl_seconds, c.status, c.created_at, c.finished_at, c.canceled_at
		ORDER BY c.created_at DESC, c.id DESC
	`
	args := []any{organisationID}
	if s.isPostgres() {
		query = numberedPlaceholders(query)
	}
	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var campaigns []domain.Campaign
	for rows.Next() {
		campaign, err := scanCampaignWithCounts(rows)
		if err != nil {
			return nil, err
		}
		campaigns = append(campaigns, campaign)
	}
	return campaigns, rows.Err()
}

// Campaign returns one organisation-scoped campaign with its task counts.
func (s *Store) Campaign(ctx context.Context, organisationID int64, campaignID int64) (domain.Campaign, error) {
	query := `
		SELECT c.id, c.organisation_id, c.name, c.task_type, c.parameters_json, c.task_ttl_seconds, c.status, c.created_at, c.finished_at, c.canceled_at,
			COUNT(t.id),
			COALESCE(SUM(CASE WHEN t.status = 'queued' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'in_progress' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'success' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'failure' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'expired' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.status = 'canceled' THEN 1 ELSE 0 END), 0)
		FROM campaigns c
		LEFT JOIN device_tasks t ON t.campaign_id = c.id
		WHERE c.organisation_id = ? AND c.id = ?
		GROUP BY c.id, c.organisation_id, c.name, c.task_type, c.parameters_json, c.task_ttl_seconds, c.status, c.created_at, c.finished_at, c.canceled_at
	`
	args := []any{organisationID, campaignID}
	if s.isPostgres() {
		query = numberedPlaceholders(query)
	}
	campaign, err := scanCampaignWithCounts(s.readDB.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Campaign{}, ErrNotFound
	}
	return campaign, err
}

// ListCampaignTasks returns a filtered page of tasks for one organisation-scoped campaign.
func (s *Store) ListCampaignTasks(ctx context.Context, query CampaignTaskQuery) (CampaignTaskPage, error) {
	countQuery := `SELECT COUNT(*) FROM device_tasks t JOIN devices d ON d.id = t.device_id WHERE t.campaign_id = ? AND d.organisation_id = ?`
	args := []any{query.CampaignID, query.OrganisationID}
	status := strings.TrimSpace(query.Status)
	if status != "" {
		countQuery += ` AND t.status = ?`
		args = append(args, status)
	}
	if s.isPostgres() {
		countQuery = numberedPlaceholders(countQuery)
	}
	var count int
	if err := s.readDB.QueryRowContext(ctx, countQuery, args...).Scan(&count); err != nil {
		return CampaignTaskPage{}, err
	}
	pagination := NormalizeDeviceListPagination(query.Page, query.PageSize, count)

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pagination.PageSize, pagination.Offset)
	listQuery := `
		SELECT t.id, t.device_id, t.campaign_id, t.task_type, t.parameters_json, t.status, t.status_message, t.created_at, t.expires_at, t.completed_at,
			m.id, m.name
		FROM device_tasks t
		JOIN devices d ON d.id = t.device_id
		JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
		WHERE t.campaign_id = ? AND d.organisation_id = ?
	`
	if status != "" {
		listQuery += ` AND t.status = ?`
	}
	listQuery += ` ORDER BY t.created_at ASC, t.id ASC LIMIT ? OFFSET ?`
	if s.isPostgres() {
		listQuery = numberedPlaceholders(listQuery)
	}
	rows, err := s.readDB.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return CampaignTaskPage{}, err
	}
	defer rows.Close()
	var result []domain.CampaignTaskRow
	for rows.Next() {
		var (
			row           domain.CampaignTaskRow
			campaignID    nullableInt64Value
			statusMessage nullableString
			completedAt   nullableString
		)
		if err := rows.Scan(
			&row.Task.ID,
			&row.Task.DeviceID,
			&campaignID,
			&row.Task.Type,
			&row.Task.ParametersJSON,
			&row.Task.Status,
			&statusMessage,
			&row.Task.CreatedAt,
			&row.Task.ExpiresAt,
			&completedAt,
			&row.DeviceModelID,
			&row.DeviceModelName,
		); err != nil {
			return CampaignTaskPage{}, err
		}
		if campaignID.Valid {
			row.Task.CampaignID = &campaignID.Int64
		}
		if statusMessage.Valid {
			row.Task.StatusMessage = statusMessage.String
		}
		if completedAt.Valid {
			row.Task.CompletedAt = completedAt.String
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return CampaignTaskPage{}, err
	}
	return CampaignTaskPage{Rows: result, Pagination: pagination}, nil
}

// CancelCampaign atomically cancels a running campaign and all its non-terminal
// tasks. It returns the affected device IDs after notifying their subscribers.
func (s *Store) CancelCampaign(ctx context.Context, organisationID int64, campaignID int64, now time.Time) ([]string, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	query := `UPDATE campaigns SET status = 'canceled', canceled_at = ? WHERE id = ? AND organisation_id = ? AND status = 'running'`
	args := []any{formatTime(now), campaignID, organisationID}
	if s.isPostgres() {
		query = `UPDATE campaigns SET status = 'canceled', canceled_at = $1 WHERE id = $2 AND organisation_id = $3 AND status = 'running'`
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, ErrNotFound
	}

	devicesQuery := `SELECT DISTINCT device_id FROM device_tasks WHERE campaign_id = ? AND status IN ('queued', 'pending', 'in_progress')`
	devicesArgs := []any{campaignID}
	if s.isPostgres() {
		devicesQuery = `SELECT DISTINCT device_id FROM device_tasks WHERE campaign_id = $1 AND status IN ('queued', 'pending', 'in_progress')`
	}
	deviceRows, err := tx.QueryContext(ctx, devicesQuery, devicesArgs...)
	if err != nil {
		return nil, err
	}
	var deviceIDs []string
	for deviceRows.Next() {
		var deviceID string
		if err := deviceRows.Scan(&deviceID); err != nil {
			deviceRows.Close()
			return nil, err
		}
		deviceIDs = append(deviceIDs, deviceID)
	}
	if err := deviceRows.Close(); err != nil {
		return nil, err
	}
	if err := deviceRows.Err(); err != nil {
		return nil, err
	}

	updateTasks := `UPDATE device_tasks SET status = 'canceled', completed_at = ?, status_message = ? WHERE campaign_id = ? AND status IN ('queued', 'pending', 'in_progress')`
	updateArgs := []any{formatTime(now), nullableStatusMessage("Campaign canceled."), campaignID}
	if s.isPostgres() {
		updateTasks = `UPDATE device_tasks SET status = 'canceled', completed_at = $1, status_message = $2 WHERE campaign_id = $3 AND status IN ('queued', 'pending', 'in_progress')`
	}
	if _, err := tx.ExecContext(ctx, updateTasks, updateArgs...); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, deviceID := range deviceIDs {
		s.tasks.publish(deviceID)
	}
	return deviceIDs, nil
}

// FinalizeFinishedCampaigns marks running campaigns as finished when none of
// their tasks remain non-terminal and returns the number changed.
func (s *Store) FinalizeFinishedCampaigns(ctx context.Context, now time.Time) (int64, error) {
	query := `
		UPDATE campaigns
		SET status = 'finished', finished_at = ?
		WHERE status = 'running'
			AND NOT EXISTS (
				SELECT 1 FROM device_tasks t
				WHERE t.campaign_id = campaigns.id AND t.status IN ('queued', 'pending', 'in_progress')
			)
	`
	args := []any{formatTime(now)}
	if s.isPostgres() {
		query = `
			UPDATE campaigns
			SET status = 'finished', finished_at = $1
			WHERE status = 'running'
				AND NOT EXISTS (
					SELECT 1 FROM device_tasks t
					WHERE t.campaign_id = campaigns.id AND t.status IN ('queued', 'pending', 'in_progress')
				)
		`
	}
	result, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func scanCampaignWithCounts(row deviceTaskScanner) (domain.Campaign, error) {
	var (
		campaign   domain.Campaign
		finishedAt nullableString
		canceledAt nullableString
	)
	if err := row.Scan(
		&campaign.ID,
		&campaign.OrganisationID,
		&campaign.Name,
		&campaign.TaskType,
		&campaign.ParametersJSON,
		&campaign.TaskTTLSeconds,
		&campaign.Status,
		&campaign.CreatedAt,
		&finishedAt,
		&canceledAt,
		&campaign.TargetCount,
		&campaign.Counts.Queued,
		&campaign.Counts.Pending,
		&campaign.Counts.InProgress,
		&campaign.Counts.Success,
		&campaign.Counts.Failure,
		&campaign.Counts.Expired,
		&campaign.Counts.Canceled,
	); err != nil {
		return domain.Campaign{}, err
	}
	if finishedAt.Valid {
		campaign.FinishedAt = finishedAt.String
	}
	if canceledAt.Valid {
		campaign.CanceledAt = canceledAt.String
	}
	return campaign, nil
}

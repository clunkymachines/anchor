package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"anchor/internal/domain"
)

func mustJSON(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }
func nullableTargetModelID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}
func nullableTargetText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

const (
	CampaignTargetExplicit = "explicit"
	CampaignTargetModel    = "model"
	CampaignTargetTag      = "tag"
	CampaignTargetTagModel = "tag_model"
	// CampaignStatusRunning aliases the domain running status for persistence callers.
	CampaignStatusRunning = domain.CampaignStatusRunning
	// CampaignStatusFinished aliases the domain finished status for persistence callers.
	CampaignStatusFinished = domain.CampaignStatusFinished
	// CampaignStatusCanceled aliases the domain canceled status for persistence callers.
	CampaignStatusCanceled = domain.CampaignStatusCanceled
)

var ErrNoCampaignTargets = errors.New("campaign target resolves to no devices")

// CampaignCreate contains campaign metadata and target device IDs to validate and persist.
type CampaignCreate struct {
	OrganisationID int64
	Name           string
	TaskType       string
	ParametersJSON string
	TTLSeconds     int64
	DeviceIDs      []string
	TargetType     string
	TargetTag      string
	TargetModelID  int64
	CreatedAt      time.Time
}

type CampaignTargetSelector struct {
	OrganisationID int64
	TargetType     string
	DeviceIDs      []string
	Tag            string
	ModelID        int64
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

type campaignQueryRunner interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateCampaignSelector(selector *CampaignTargetSelector) error {
	selector.TargetType = strings.TrimSpace(selector.TargetType)
	selector.Tag = strings.TrimSpace(selector.Tag)
	switch selector.TargetType {
	case CampaignTargetExplicit:
		if selector.Tag != "" || selector.ModelID != 0 {
			return errors.New("explicit device targets cannot be combined with a tag or model")
		}
		if err := validateCampaignDeviceIDs(selector.DeviceIDs); err != nil {
			return err
		}
		selector.DeviceIDs = append([]string(nil), selector.DeviceIDs...)
		for i := range selector.DeviceIDs {
			selector.DeviceIDs[i] = strings.TrimSpace(selector.DeviceIDs[i])
		}
		sort.Strings(selector.DeviceIDs)
	case CampaignTargetTag:
		if len(selector.DeviceIDs) != 0 || selector.ModelID != 0 {
			return errors.New("tag targets cannot be combined with explicit devices or a model")
		}
		tag, err := NormalizeTag(selector.Tag)
		if err != nil {
			return err
		}
		selector.Tag = tag
	case CampaignTargetModel:
		if len(selector.DeviceIDs) != 0 || selector.Tag != "" || selector.ModelID <= 0 {
			return errors.New("model targeting requires exactly one model")
		}
	case CampaignTargetTagModel:
		if len(selector.DeviceIDs) != 0 || selector.ModelID <= 0 {
			return errors.New("tag and model targeting requires exactly one tag and model")
		}
		tag, err := NormalizeTag(selector.Tag)
		if err != nil {
			return err
		}
		selector.Tag = tag
	default:
		return errors.New("choose exactly one campaign targeting mode")
	}
	return nil
}

func (s *Store) EstimateCampaignTargets(ctx context.Context, selector CampaignTargetSelector) (int, error) {
	if err := validateCampaignSelector(&selector); err != nil {
		return 0, err
	}
	devices, _, err := s.resolveCampaignTargets(ctx, s.readDB, selector)
	if errors.Is(err, ErrNoCampaignTargets) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return len(devices), nil
}

func (s *Store) CampaignSelectorDevices(ctx context.Context, selector CampaignTargetSelector) ([]domain.Device, error) {
	if err := validateCampaignSelector(&selector); err != nil {
		return nil, err
	}
	devices, _, err := s.resolveCampaignTargets(ctx, s.readDB, selector)
	return devices, err
}

func (s *Store) resolveCampaignTargets(ctx context.Context, runner campaignQueryRunner, selector CampaignTargetSelector) ([]domain.Device, string, error) {
	query := `SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds, m.expected_protocol, d.software_versions, d.is_gateway FROM devices d JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id WHERE d.organisation_id = ?`
	args := []any{selector.OrganisationID}
	switch selector.TargetType {
	case CampaignTargetExplicit:
		placeholders := make([]string, len(selector.DeviceIDs))
		for i, id := range selector.DeviceIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query += ` AND d.id IN (` + strings.Join(placeholders, ",") + `)`
	case CampaignTargetModel:
		query += ` AND d.device_model_id = ?`
		args = append(args, selector.ModelID)
	case CampaignTargetTag:
		query += ` AND EXISTS (SELECT 1 FROM device_tags dt WHERE dt.device_id = d.id AND dt.organisation_id = d.organisation_id AND dt.tag = ?)`
		args = append(args, selector.Tag)
	case CampaignTargetTagModel:
		query += ` AND d.device_model_id = ? AND EXISTS (SELECT 1 FROM device_tags dt WHERE dt.device_id = d.id AND dt.organisation_id = d.organisation_id AND dt.tag = ?)`
		args = append(args, selector.ModelID, selector.Tag)
	}
	query += ` ORDER BY d.id`
	if s.isPostgres() {
		query = numberedPlaceholders(query)
	}
	rows, err := runner.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	devices := make([]domain.Device, 0)
	modelName := ""
	for rows.Next() {
		var device domain.Device
		var versions softwareVersionsValue
		if err := rows.Scan(&device.ID, &device.OrganisationID, &device.DeviceModelID, &device.ModelName, &device.ExpectedHeartbeatSeconds, &device.ExpectedProtocol, &versions, &device.IsGateway); err != nil {
			return nil, "", err
		}
		device.SoftwareVersions = domain.SoftwareVersions(versions)
		devices = append(devices, device)
		if selector.ModelID > 0 {
			modelName = device.ModelName
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if selector.TargetType == CampaignTargetExplicit && len(devices) != len(selector.DeviceIDs) {
		return nil, "", ErrNotFound
	}
	if len(devices) == 0 {
		return nil, "", ErrNoCampaignTargets
	}
	return devices, modelName, nil
}

func (s *Store) validateCampaignTaskTargetsTx(ctx context.Context, tx *sql.Tx, input CampaignCreate, devices []domain.Device) error {
	if input.TaskType == domain.TaskTypeFOTA {
		params, err := domain.ParseFOTATaskParameters(input.ParametersJSON)
		if err != nil {
			return err
		}
		query := `SELECT device_model_id FROM software_releases WHERE id = ? AND organisation_id = ?`
		if s.isPostgres() {
			query = numberedPlaceholders(query)
		}
		var releaseModelID int64
		if err := tx.QueryRowContext(ctx, query, params.ReleaseID, input.OrganisationID).Scan(&releaseModelID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("choose a release from this organisation")
			}
			return err
		}
		for _, device := range devices {
			if device.DeviceModelID != releaseModelID {
				return fmt.Errorf("device %q is incompatible with the selected FOTA release", device.ID)
			}
		}
	}
	for _, device := range devices {
		if !strings.EqualFold(device.ExpectedProtocol, "coap") {
			continue
		}
		switch input.TaskType {
		case domain.TaskTypeRead:
			var params domain.ReadTaskParameters
			if err := json.Unmarshal([]byte(input.ParametersJSON), &params); err != nil {
				return err
			}
			for _, path := range params.Paths {
				if err := domain.ValidateCoAPResourcePath(path); err != nil {
					return err
				}
			}
		case domain.TaskTypeWrite:
			var params domain.WriteTaskParameters
			if err := json.Unmarshal([]byte(input.ParametersJSON), &params); err != nil {
				return err
			}
			for _, value := range params.Values {
				if err := domain.ValidateCoAPResourcePath(value.Path); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
	if input.TargetType == "" {
		input.TargetType = CampaignTargetExplicit
	}
	selector := CampaignTargetSelector{OrganisationID: input.OrganisationID, TargetType: input.TargetType, DeviceIDs: input.DeviceIDs, Tag: input.TargetTag, ModelID: input.TargetModelID}
	if err := validateCampaignSelector(&selector); err != nil {
		return CampaignCreateResult{}, err
	}
	input.TargetType, input.TargetTag, input.TargetModelID = selector.TargetType, selector.Tag, selector.ModelID
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

	devices, modelName, err := s.resolveCampaignTargets(ctx, tx, selector)
	if err != nil {
		return CampaignCreateResult{}, err
	}
	deviceIDs := make([]string, 0, len(devices))
	for _, device := range devices {
		deviceIDs = append(deviceIDs, device.ID)
	}
	for _, deviceID := range deviceIDs {
		if err := s.lockDeviceForTask(ctx, tx, deviceID, input.OrganisationID); err != nil {
			return CampaignCreateResult{}, err
		}
	}
	if err := s.validateCampaignTaskTargetsTx(ctx, tx, input, devices); err != nil {
		return CampaignCreateResult{}, err
	}

	campaign := domain.Campaign{
		OrganisationID:  input.OrganisationID,
		Name:            input.Name,
		TaskType:        input.TaskType,
		ParametersJSON:  input.ParametersJSON,
		TaskTTLSeconds:  input.TTLSeconds,
		Status:          CampaignStatusRunning,
		CreatedAt:       formatTime(input.CreatedAt),
		TargetType:      input.TargetType,
		TargetTag:       selector.Tag,
		TargetModelID:   selector.ModelID,
		TargetModelName: modelName,
	}
	if input.TargetType == CampaignTargetExplicit {
		campaign.TargetDeviceIDs = append([]string(nil), deviceIDs...)
	}
	campaign.ID, err = s.insertCampaignTx(ctx, tx, campaign)
	if err != nil {
		return CampaignCreateResult{}, err
	}

	pending := make([]domain.DeviceTask, 0, len(devices))
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
	for _, deviceID := range deviceIDs {
		s.tasks.publish(deviceID)
	}
	campaign.TargetCount = len(deviceIDs)
	return CampaignCreateResult{Campaign: campaign, PendingTasks: pending}, nil
}

func (s *Store) insertCampaignTx(ctx context.Context, tx *sql.Tx, campaign domain.Campaign) (int64, error) {
	switch s.dialect {
	case DialectSQLite:
		result, err := tx.ExecContext(ctx, `
			INSERT INTO campaigns (organisation_id, name, task_type, parameters_json, task_ttl_seconds, status, created_at, target_type, target_device_ids, target_tag, target_model_id, target_model_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, campaign.OrganisationID, campaign.Name, campaign.TaskType, campaign.ParametersJSON, campaign.TaskTTLSeconds, campaign.Status, campaign.CreatedAt, campaign.TargetType, mustJSON(campaign.TargetDeviceIDs), campaign.TargetTag, nullableTargetModelID(campaign.TargetModelID), nullableTargetText(campaign.TargetModelName))
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	case DialectPostgres, DialectPostgreSQL:
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO campaigns (organisation_id, name, task_type, parameters_json, task_ttl_seconds, status, created_at, target_type, target_device_ids, target_tag, target_model_id, target_model_name)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING id
		`, campaign.OrganisationID, campaign.Name, campaign.TaskType, campaign.ParametersJSON, campaign.TaskTTLSeconds, campaign.Status, campaign.CreatedAt, campaign.TargetType, mustJSON(campaign.TargetDeviceIDs), campaign.TargetTag, nullableTargetModelID(campaign.TargetModelID), nullableTargetText(campaign.TargetModelName)).Scan(&id)
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
		if err := s.loadCampaignTarget(ctx, &campaign); err != nil {
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
	if err != nil {
		return campaign, err
	}
	if err := s.loadCampaignTarget(ctx, &campaign); err != nil {
		return domain.Campaign{}, err
	}
	return campaign, nil
}

func (s *Store) loadCampaignTarget(ctx context.Context, campaign *domain.Campaign) error {
	q := `SELECT target_type, target_device_ids, target_tag, target_model_id, target_model_name FROM campaigns WHERE id = ? AND organisation_id = ?`
	if s.isPostgres() {
		q = `SELECT target_type, target_device_ids, target_tag, target_model_id, target_model_name FROM campaigns WHERE id = $1 AND organisation_id = $2`
	}
	var ids string
	var tag, name nullableString
	var model nullableInt64
	if err := s.readDB.QueryRowContext(ctx, q, campaign.ID, campaign.OrganisationID).Scan(&campaign.TargetType, &ids, &tag, &model, &name); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(ids), &campaign.TargetDeviceIDs); err != nil {
		return fmt.Errorf("decode campaign target ids: %w", err)
	}
	if tag.Valid {
		campaign.TargetTag = tag.String
	}
	if model.Valid {
		campaign.TargetModelID = model.Int64
	}
	if name.Valid {
		campaign.TargetModelName = name.String
	}
	return nil
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

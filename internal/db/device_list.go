package db

import (
	"context"
	"math"
	"strings"

	"anchor/internal/domain"
)

const (
	DefaultDevicePageSize = 50
)

var allowedDevicePageSizes = map[int]struct{}{
	25:  {},
	50:  {},
	100: {},
}

type DeviceListQuery struct {
	OrganisationID int64
	Query          string
	Page           int
	PageSize       int
}

type DeviceListPage struct {
	Rows          []domain.DeviceListRow
	FilteredCount int
	Pagination    Pagination
}

type DeviceFleetMetrics struct {
	TotalDevices     int
	OnlineDevices    int
	DeviceModelCount int
	UpdatesAvailable int
}

type Pagination struct {
	Page       int
	PageSize   int
	TotalRows  int
	TotalPages int
	Offset     int
}

func NormalizeDeviceListPagination(page int, pageSize int, totalRows int) Pagination {
	if page < 1 {
		page = 1
	}
	if _, ok := allowedDevicePageSizes[pageSize]; !ok {
		pageSize = DefaultDevicePageSize
	}
	totalPages := 1
	if totalRows > 0 {
		totalPages = int(math.Ceil(float64(totalRows) / float64(pageSize)))
	}
	if page > totalPages {
		page = totalPages
	}
	return Pagination{
		Page:       page,
		PageSize:   pageSize,
		TotalRows:  totalRows,
		TotalPages: totalPages,
		Offset:     (page - 1) * pageSize,
	}
}

func (s *Store) ListDevicePage(ctx context.Context, query DeviceListQuery) (DeviceListPage, error) {
	query.Query = strings.TrimSpace(query.Query)
	filteredCount, err := s.countDevicePageRows(ctx, query)
	if err != nil {
		return DeviceListPage{}, err
	}
	pagination := NormalizeDeviceListPagination(query.Page, query.PageSize, filteredCount)

	rows, err := s.listDevicePageRows(ctx, query, pagination)
	if err != nil {
		return DeviceListPage{}, err
	}
	return DeviceListPage{
		Rows:          rows,
		FilteredCount: filteredCount,
		Pagination:    pagination,
	}, nil
}

func (s *Store) DeviceFleetMetrics(ctx context.Context, organisationID int64, nowMS int64) (DeviceFleetMetrics, error) {
	query := `
		SELECT
			COUNT(d.id),
			COALESCE(SUM(CASE
				WHEN last_event.last_received_ms IS NOT NULL
					AND ? - last_event.last_received_ms <= m.expected_heartbeat_seconds * 1000
				THEN 1 ELSE 0
			END), 0),
			(SELECT COUNT(*) FROM device_models WHERE organisation_id = ?)
		FROM devices d
		JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
		LEFT JOIN (
			SELECT device_id, MAX(ts_received_ms) AS last_received_ms
			FROM device_events
			GROUP BY device_id
		) last_event ON last_event.device_id = d.id
		WHERE d.organisation_id = ?
	`
	args := []any{nowMS, organisationID, organisationID}
	if s.isPostgres() {
		query = `
			SELECT
				COUNT(d.id),
				COALESCE(SUM(CASE
					WHEN last_event.last_received_ms IS NOT NULL
						AND $1 - last_event.last_received_ms <= m.expected_heartbeat_seconds * 1000
					THEN 1 ELSE 0
				END), 0),
				(SELECT COUNT(*) FROM device_models WHERE organisation_id = $2)
			FROM devices d
			JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
			LEFT JOIN (
				SELECT device_id, MAX(ts_received_ms) AS last_received_ms
				FROM device_events
				GROUP BY device_id
			) last_event ON last_event.device_id = d.id
			WHERE d.organisation_id = $3
		`
	}

	var metrics DeviceFleetMetrics
	if err := s.readDB.QueryRowContext(ctx, query, args...).Scan(
		&metrics.TotalDevices,
		&metrics.OnlineDevices,
		&metrics.DeviceModelCount,
	); err != nil {
		return DeviceFleetMetrics{}, err
	}
	return metrics, nil
}

func (s *Store) countDevicePageRows(ctx context.Context, query DeviceListQuery) (int, error) {
	sqlQuery := `
		SELECT COUNT(*)
		FROM devices d
		WHERE d.organisation_id = ?
			AND (? = '' OR LOWER(d.device_search_text) LIKE '%' || LOWER(?) || '%')
	`
	args := []any{query.OrganisationID, query.Query, query.Query}
	if s.isPostgres() {
		sqlQuery = `
			SELECT COUNT(*)
			FROM devices d
			WHERE d.organisation_id = $1
				AND ($2 = '' OR d.device_search_text ILIKE '%' || $3 || '%')
		`
	}
	var count int
	if err := s.readDB.QueryRowContext(ctx, sqlQuery, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) listDevicePageRows(ctx context.Context, query DeviceListQuery, pagination Pagination) ([]domain.DeviceListRow, error) {
	sqlQuery := `
		SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds,
			COALESCE((
				SELECT e.ts_received_ms
				FROM device_events e
				WHERE e.device_id = d.id
				ORDER BY e.ts_received_ms DESC
				LIMIT 1
			), 0),
			d.software_versions,
			d.is_gateway,
			CASE WHEN mc.device_id IS NULL THEN 0 ELSE 1 END,
			r.id,
			r.cve_status,
			r.active_cve_count,
			r.highest_active_severity,
			r.latest_attempted_scan_id,
			r.latest_successful_scan_id,
			r.latest_scan_warning
		FROM devices d
		JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
		LEFT JOIN mqtt_credentials mc ON mc.device_id = d.id
		LEFT JOIN software_releases r ON r.organisation_id = d.organisation_id
			AND r.device_model_id = d.device_model_id
			AND r.version = json_extract(d.software_versions, '$.firmware')
		WHERE d.organisation_id = ?
			AND (? = '' OR LOWER(d.device_search_text) LIKE '%' || LOWER(?) || '%')
		ORDER BY m.name ASC, d.id ASC
		LIMIT ? OFFSET ?
	`
	args := []any{query.OrganisationID, query.Query, query.Query, pagination.PageSize, pagination.Offset}
	if s.isPostgres() {
		sqlQuery = `
			SELECT d.id, d.organisation_id, d.device_model_id, m.name, m.expected_heartbeat_seconds,
				COALESCE((
					SELECT e.ts_received_ms
					FROM device_events e
					WHERE e.device_id = d.id
					ORDER BY e.ts_received_ms DESC
					LIMIT 1
				), 0),
				d.software_versions,
				d.is_gateway,
				CASE WHEN mc.device_id IS NULL THEN FALSE ELSE TRUE END,
				r.id,
				r.cve_status,
				r.active_cve_count,
				r.highest_active_severity,
				r.latest_attempted_scan_id,
				r.latest_successful_scan_id,
				r.latest_scan_warning
			FROM devices d
			JOIN device_models m ON m.id = d.device_model_id AND m.organisation_id = d.organisation_id
			LEFT JOIN mqtt_credentials mc ON mc.device_id = d.id
			LEFT JOIN software_releases r ON r.organisation_id = d.organisation_id
				AND r.device_model_id = d.device_model_id
				AND r.version = d.software_versions ->> 'firmware'
			WHERE d.organisation_id = $1
				AND ($2 = '' OR d.device_search_text ILIKE '%' || $3 || '%')
			ORDER BY m.name ASC, d.id ASC
			LIMIT $4 OFFSET $5
		`
	}

	rows, err := s.readDB.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	deviceRows := make([]domain.DeviceListRow, 0, pagination.PageSize)
	for rows.Next() {
		var (
			device             domain.Device
			versions           softwareVersionsValue
			hasMQTTCredential  bool
			releaseID          nullableInt64
			statusValue        nullableString
			activeCount        nullableInt64
			highestSeverity    nullableString
			latestAttemptedID  nullableInt64
			latestSuccessfulID nullableInt64
			latestScanWarning  nullableString
		)
		if err := rows.Scan(
			&device.ID,
			&device.OrganisationID,
			&device.DeviceModelID,
			&device.ModelName,
			&device.ExpectedHeartbeatSeconds,
			&device.LastEventReceivedMS,
			&versions,
			&device.IsGateway,
			&hasMQTTCredential,
			&releaseID,
			&statusValue,
			&activeCount,
			&highestSeverity,
			&latestAttemptedID,
			&latestSuccessfulID,
			&latestScanWarning,
		); err != nil {
			return nil, err
		}
		device.SoftwareVersions = domain.SoftwareVersions(versions)
		status := domain.CalculateDeviceCVEStatus(false, domain.CVEImpactStatus{})
		if releaseID.Valid {
			status = domain.CalculateDeviceCVEStatus(true, scanCVEImpactStatusFields(
				statusValue,
				activeCount,
				highestSeverity,
				latestAttemptedID,
				latestSuccessfulID,
				latestScanWarning,
			))
			status.MatchedReleaseID = releaseID.Int64
		}
		deviceRows = append(deviceRows, domain.DeviceListRow{
			Device:            device,
			HasMQTTCredential: hasMQTTCredential,
			CVEStatus:         status,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return deviceRows, nil
}

func scanCVEImpactStatusFields(statusValue nullableString, activeCount nullableInt64, highestSeverity nullableString, latestAttemptedID nullableInt64, latestSuccessfulID nullableInt64, latestScanWarning nullableString) domain.CVEImpactStatus {
	status := domain.CVEImpactStatus{
		Status: domain.CVEStatusNoSBOM,
	}
	if statusValue.Valid && strings.TrimSpace(statusValue.String) != "" {
		status.Status = domain.CVEImpactStatusValue(statusValue.String)
	}
	if activeCount.Valid {
		status.ActiveCVECount = int(activeCount.Int64)
	}
	if highestSeverity.Valid {
		status.HighestActiveSeverity = highestSeverity.String
	}
	if latestAttemptedID.Valid {
		status.LatestAttemptedScanID = latestAttemptedID.Int64
	}
	if latestSuccessfulID.Valid {
		status.LatestSuccessfulScanID = latestSuccessfulID.Int64
	}
	if latestScanWarning.Valid {
		status.LatestScanWarning = latestScanWarning.String
		status.HasLatestScanWarning = strings.TrimSpace(latestScanWarning.String) != ""
	}
	return status
}

func nullableCVEStatus(status domain.CVEImpactStatus) (string, int, string, int64, int64, string) {
	return string(status.Status),
		status.ActiveCVECount,
		status.HighestActiveSeverity,
		status.LatestAttemptedScanID,
		status.LatestSuccessfulScanID,
		status.LatestScanWarning
}

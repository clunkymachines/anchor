package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"anchor/internal/domain"
)

func (s *Store) ReplaceReleaseSBOM(ctx context.Context, organisationID int64, releaseID int64, fileCount int, totalSizeBytes int64) (domain.ReleaseSBOM, error) {
	if fileCount <= 0 {
		return domain.ReleaseSBOM{}, errors.New("sbom file count must be positive")
	}
	if totalSizeBytes < 0 {
		return domain.ReleaseSBOM{}, errors.New("sbom total size must not be negative")
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return domain.ReleaseSBOM{}, err
	}
	defer tx.Rollback()

	if err := s.ensureReleaseExistsTx(ctx, tx, organisationID, releaseID); err != nil {
		return domain.ReleaseSBOM{}, err
	}

	if _, err := tx.ExecContext(ctx, s.placeholderSQL(
		`DELETE FROM software_release_sboms WHERE organisation_id = ? AND release_id = ?`,
		`DELETE FROM software_release_sboms WHERE organisation_id = $1 AND release_id = $2`,
	), organisationID, releaseID); err != nil {
		return domain.ReleaseSBOM{}, err
	}

	var sbom domain.ReleaseSBOM
	switch s.dialect {
	case DialectSQLite:
		result, err := tx.ExecContext(ctx,
			`INSERT INTO software_release_sboms (organisation_id, release_id, file_count, total_size_bytes)
			VALUES (?, ?, ?, ?)`,
			organisationID,
			releaseID,
			fileCount,
			totalSizeBytes,
		)
		if err != nil {
			return domain.ReleaseSBOM{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return domain.ReleaseSBOM{}, err
		}
		sbom, err = s.releaseSBOMTx(ctx, tx, organisationID, id)
		if err != nil {
			return domain.ReleaseSBOM{}, err
		}
	case DialectPostgres, DialectPostgreSQL:
		err := tx.QueryRowContext(ctx,
			`INSERT INTO software_release_sboms (organisation_id, release_id, file_count, total_size_bytes)
			VALUES ($1, $2, $3, $4)
			RETURNING id, organisation_id, release_id, file_count, total_size_bytes, created_at::text, updated_at::text`,
			organisationID,
			releaseID,
			fileCount,
			totalSizeBytes,
		).Scan(&sbom.ID, &sbom.OrganisationID, &sbom.ReleaseID, &sbom.FileCount, &sbom.TotalSizeBytes, &sbom.CreatedAt, &sbom.UpdatedAt)
		if err != nil {
			return domain.ReleaseSBOM{}, err
		}
	default:
		return domain.ReleaseSBOM{}, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}

	if err := tx.Commit(); err != nil {
		return domain.ReleaseSBOM{}, err
	}
	s.publishReleaseCVEScan(organisationID, releaseID)
	return sbom, nil
}

func (s *Store) ClearReleaseSBOM(ctx context.Context, organisationID int64, releaseID int64) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := s.ensureReleaseExistsTx(ctx, tx, organisationID, releaseID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.placeholderSQL(
		`DELETE FROM software_release_sboms WHERE organisation_id = ? AND release_id = ?`,
		`DELETE FROM software_release_sboms WHERE organisation_id = $1 AND release_id = $2`,
	), organisationID, releaseID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.publishReleaseCVEScan(organisationID, releaseID)
	return nil
}

func (s *Store) CurrentReleaseSBOM(ctx context.Context, organisationID int64, releaseID int64) (domain.ReleaseSBOM, error) {
	query := `
		SELECT id, organisation_id, release_id, file_count, total_size_bytes, created_at, updated_at
		FROM software_release_sboms
		WHERE organisation_id = ? AND release_id = ?
	`
	args := []any{organisationID, releaseID}
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, release_id, file_count, total_size_bytes, created_at::text, updated_at::text
			FROM software_release_sboms
			WHERE organisation_id = $1 AND release_id = $2
		`
	}

	sbom, err := scanReleaseSBOM(s.readDB.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReleaseSBOM{}, ErrNotFound
	}
	return sbom, err
}

func (s *Store) EnqueueCVEScan(ctx context.Context, organisationID int64, releaseID int64, trigger string) (domain.CVEScanRun, error) {
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		trigger = "auto"
	}

	var run domain.CVEScanRun
	switch s.dialect {
	case DialectSQLite:
		result, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO cve_scan_runs (organisation_id, release_id, release_sbom_id, trigger_source, status)
			SELECT ?, ?, b.id, ?, 'pending'
			FROM software_release_sboms b
			WHERE b.organisation_id = ? AND b.release_id = ?`,
			organisationID,
			releaseID,
			trigger,
			organisationID,
			releaseID,
		)
		if err != nil {
			if isUniqueConstraintError(err) {
				return domain.CVEScanRun{}, ErrConflict
			}
			return domain.CVEScanRun{}, err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return domain.CVEScanRun{}, err
		}
		if rowsAffected == 0 {
			return domain.CVEScanRun{}, ErrNotFound
		}
		id, err := result.LastInsertId()
		if err != nil {
			return domain.CVEScanRun{}, err
		}
		run, err = s.CVEScanRun(ctx, organisationID, id)
		if err == nil {
			s.publishReleaseCVEScan(organisationID, releaseID)
		}
		return run, err
	case DialectPostgres, DialectPostgreSQL:
		err := s.writeDB.QueryRowContext(ctx,
			`INSERT INTO cve_scan_runs (organisation_id, release_id, release_sbom_id, trigger_source, status)
			SELECT $1, $2, b.id, $3, 'pending'
			FROM software_release_sboms b
			WHERE b.organisation_id = $4 AND b.release_id = $5
			RETURNING id, organisation_id, release_id, release_sbom_id, trigger_source, status, error_message, created_at::text, COALESCE(started_at::text, ''), COALESCE(finished_at::text, '')`,
			organisationID,
			releaseID,
			trigger,
			organisationID,
			releaseID,
		).Scan(&run.ID, &run.OrganisationID, &run.ReleaseID, &run.ReleaseSBOMID, &run.Trigger, &run.Status, &run.ErrorMessage, &run.CreatedAt, &run.StartedAt, &run.FinishedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.CVEScanRun{}, ErrNotFound
		}
		if err != nil {
			if isUniqueConstraintError(err) {
				return domain.CVEScanRun{}, ErrConflict
			}
			return domain.CVEScanRun{}, err
		}
		s.publishReleaseCVEScan(organisationID, releaseID)
		return run, nil
	default:
		return domain.CVEScanRun{}, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
}

func (s *Store) CVEScanRun(ctx context.Context, organisationID int64, scanRunID int64) (domain.CVEScanRun, error) {
	query := `
		SELECT id, organisation_id, release_id, release_sbom_id, trigger_source, status, error_message, created_at, started_at, finished_at
		FROM cve_scan_runs
		WHERE organisation_id = ? AND id = ?
	`
	args := []any{organisationID, scanRunID}
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, release_id, release_sbom_id, trigger_source, status, error_message, created_at::text, started_at::text, finished_at::text
			FROM cve_scan_runs
			WHERE organisation_id = $1 AND id = $2
		`
	}

	run, err := scanCVEScanRun(s.readDB.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CVEScanRun{}, ErrNotFound
	}
	return run, err
}

func (s *Store) ListPendingCVEScanRuns(ctx context.Context) ([]domain.CVEScanRun, error) {
	query := `
		SELECT id, organisation_id, release_id, release_sbom_id, trigger_source, status, error_message, created_at, started_at, finished_at
		FROM cve_scan_runs
		WHERE status = 'pending'
		ORDER BY created_at, id
	`
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, release_id, release_sbom_id, trigger_source, status, error_message, created_at::text, started_at::text, finished_at::text
			FROM cve_scan_runs
			WHERE status = 'pending'
			ORDER BY created_at, id
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []domain.CVEScanRun
	for rows.Next() {
		run, err := scanCVEScanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Store) StartCVEScanRun(ctx context.Context, organisationID int64, scanRunID int64, startedAt string) error {
	query := `
		UPDATE cve_scan_runs
		SET status = 'running', started_at = ?
		WHERE organisation_id = ? AND id = ? AND status = 'pending'
	`
	args := []any{nullableTime(startedAt), organisationID, scanRunID}
	if s.isPostgres() {
		query = `
			UPDATE cve_scan_runs
			SET status = 'running', started_at = $1
			WHERE organisation_id = $2 AND id = $3 AND status = 'pending'
		`
	}
	if err := s.execOne(ctx, query, args...); err != nil {
		return err
	}
	run, err := s.CVEScanRun(ctx, organisationID, scanRunID)
	if err == nil {
		s.publishReleaseCVEScan(organisationID, run.ReleaseID)
	}
	return nil
}

func (s *Store) MarkRunningCVEScansFailed(ctx context.Context, finishedAt string, errorMessage string) (int64, error) {
	query := `
		UPDATE cve_scan_runs
		SET status = 'failed', error_message = ?, finished_at = ?
		WHERE status = 'running'
	`
	args := []any{strings.TrimSpace(errorMessage), nullableTime(finishedAt)}
	if s.isPostgres() {
		query = `
			UPDATE cve_scan_runs
			SET status = 'failed', error_message = $1, finished_at = $2
			WHERE status = 'running'
		`
	}

	result, err := s.writeDB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) CompleteCVEScanRun(ctx context.Context, organisationID int64, scanRunID int64, finishedAt string, findings []domain.CVEScanFinding) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var releaseID int64
	err = tx.QueryRowContext(ctx, s.placeholderSQL(
		`SELECT release_id FROM cve_scan_runs WHERE organisation_id = ? AND id = ? AND status IN ('pending', 'running')`,
		`SELECT release_id FROM cve_scan_runs WHERE organisation_id = $1 AND id = $2 AND status IN ('pending', 'running')`,
	), organisationID, scanRunID).Scan(&releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, s.placeholderSQL(
		`DELETE FROM cve_scan_findings WHERE organisation_id = ? AND scan_run_id = ?`,
		`DELETE FROM cve_scan_findings WHERE organisation_id = $1 AND scan_run_id = $2`,
	), organisationID, scanRunID); err != nil {
		return err
	}

	for _, finding := range dedupeCVEScanFindings(findings) {
		if _, err := tx.ExecContext(ctx, s.insertFindingSQL(),
			organisationID,
			releaseID,
			scanRunID,
			strings.TrimSpace(finding.CVEID),
			strings.TrimSpace(finding.Severity),
			strings.TrimSpace(finding.PackageName),
			strings.TrimSpace(finding.InstalledVersion),
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, s.placeholderSQL(
		`UPDATE cve_scan_runs SET status = 'success', error_message = '', finished_at = ? WHERE organisation_id = ? AND id = ?`,
		`UPDATE cve_scan_runs SET status = 'success', error_message = '', finished_at = $1 WHERE organisation_id = $2 AND id = $3`,
	), nullableTime(finishedAt), organisationID, scanRunID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.publishReleaseCVEScan(organisationID, releaseID)
	return nil
}

func (s *Store) FailCVEScanRun(ctx context.Context, organisationID int64, scanRunID int64, finishedAt string, errorMessage string) error {
	query := `
		UPDATE cve_scan_runs
		SET status = 'failed', error_message = ?, finished_at = ?
		WHERE organisation_id = ? AND id = ? AND status IN ('pending', 'running')
	`
	args := []any{strings.TrimSpace(errorMessage), nullableTime(finishedAt), organisationID, scanRunID}
	if s.isPostgres() {
		query = `
			UPDATE cve_scan_runs
			SET status = 'failed', error_message = $1, finished_at = $2
			WHERE organisation_id = $3 AND id = $4 AND status IN ('pending', 'running')
		`
	}
	if err := s.execOne(ctx, query, args...); err != nil {
		return err
	}
	run, err := s.CVEScanRun(ctx, organisationID, scanRunID)
	if err == nil {
		s.publishReleaseCVEScan(organisationID, run.ReleaseID)
	}
	return nil
}

func (s *Store) ListCVEScanRuns(ctx context.Context, organisationID int64, releaseID int64) ([]domain.CVEScanRun, error) {
	query := `
		SELECT id, organisation_id, release_id, release_sbom_id, trigger_source, status, error_message, created_at, started_at, finished_at
		FROM cve_scan_runs
		WHERE organisation_id = ? AND release_id = ?
		ORDER BY created_at DESC, id DESC
	`
	args := []any{organisationID, releaseID}
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, release_id, release_sbom_id, trigger_source, status, error_message, created_at::text, started_at::text, finished_at::text
			FROM cve_scan_runs
			WHERE organisation_id = $1 AND release_id = $2
			ORDER BY created_at DESC, id DESC
		`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []domain.CVEScanRun
	for rows.Next() {
		run, err := scanCVEScanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Store) LatestSuccessfulCVEScanRun(ctx context.Context, organisationID int64, releaseID int64) (domain.CVEScanRun, error) {
	query := `
		SELECT id, organisation_id, release_id, release_sbom_id, trigger_source, status, error_message, created_at, started_at, finished_at
		FROM cve_scan_runs
		WHERE organisation_id = ? AND release_id = ? AND status = 'success'
		ORDER BY COALESCE(finished_at, created_at) DESC, id DESC
		LIMIT 1
	`
	args := []any{organisationID, releaseID}
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, release_id, release_sbom_id, trigger_source, status, error_message, created_at::text, started_at::text, finished_at::text
			FROM cve_scan_runs
			WHERE organisation_id = $1 AND release_id = $2 AND status = 'success'
			ORDER BY COALESCE(finished_at, created_at) DESC, id DESC
			LIMIT 1
		`
	}
	run, err := scanCVEScanRun(s.readDB.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CVEScanRun{}, ErrNotFound
	}
	return run, err
}

func (s *Store) ListCurrentCVEFindings(ctx context.Context, organisationID int64, releaseID int64) ([]domain.CVEScanFinding, error) {
	run, err := s.LatestSuccessfulCVEScanRun(ctx, organisationID, releaseID)
	if err != nil {
		return nil, err
	}
	return s.listCVEFindingsForRun(ctx, organisationID, releaseID, run.ID, false)
}

func (s *Store) ListActiveCVEFindings(ctx context.Context, organisationID int64, releaseID int64) ([]domain.CVEScanFinding, error) {
	run, err := s.LatestSuccessfulCVEScanRun(ctx, organisationID, releaseID)
	if err != nil {
		return nil, err
	}
	return s.listCVEFindingsForRun(ctx, organisationID, releaseID, run.ID, true)
}

func (s *Store) ReleaseCVEImpactStatus(ctx context.Context, organisationID int64, releaseID int64) (domain.CVEImpactStatus, error) {
	if _, err := s.CurrentReleaseSBOM(ctx, organisationID, releaseID); errors.Is(err, ErrNotFound) {
		if _, releaseErr := s.SoftwareRelease(ctx, releaseID, organisationID); releaseErr != nil {
			return domain.CVEImpactStatus{}, releaseErr
		}
		return domain.CalculateReleaseCVEStatus(false, nil, nil), nil
	} else if err != nil {
		return domain.CVEImpactStatus{}, err
	}

	scanRuns, err := s.ListCVEScanRuns(ctx, organisationID, releaseID)
	if err != nil {
		return domain.CVEImpactStatus{}, err
	}
	activeFindings, err := s.ListActiveCVEFindings(ctx, organisationID, releaseID)
	if errors.Is(err, ErrNotFound) {
		activeFindings = nil
	} else if err != nil {
		return domain.CVEImpactStatus{}, err
	}
	return domain.CalculateReleaseCVEStatus(true, scanRuns, activeFindings), nil
}

func (s *Store) DeviceCVEImpactStatus(ctx context.Context, organisationID int64, deviceID string) (domain.CVEImpactStatus, error) {
	detail, err := s.DeviceDetail(ctx, deviceID, organisationID)
	if err != nil {
		return domain.CVEImpactStatus{}, err
	}

	firmwareVersion := strings.TrimSpace(detail.Device.SoftwareVersions["firmware"])
	if firmwareVersion == "" {
		return domain.CalculateDeviceCVEStatus(false, domain.CVEImpactStatus{}), nil
	}

	release, err := s.SoftwareReleaseByDeviceModelAndVersion(ctx, organisationID, detail.Device.DeviceModelID, firmwareVersion)
	if errors.Is(err, ErrNotFound) {
		return domain.CalculateDeviceCVEStatus(false, domain.CVEImpactStatus{}), nil
	}
	if err != nil {
		return domain.CVEImpactStatus{}, err
	}

	status, err := s.ReleaseCVEImpactStatus(ctx, organisationID, release.ID)
	if err != nil {
		return domain.CVEImpactStatus{}, err
	}
	status = domain.CalculateDeviceCVEStatus(true, status)
	status.MatchedReleaseID = release.ID
	return status, nil
}

func (s *Store) UpsertReleaseCVEWaiver(ctx context.Context, waiver domain.ReleaseCVEWaiver) (domain.ReleaseCVEWaiver, error) {
	waiver.CVEID = strings.TrimSpace(waiver.CVEID)
	waiver.Note = strings.TrimSpace(waiver.Note)
	if waiver.CVEID == "" {
		return domain.ReleaseCVEWaiver{}, errors.New("cve id is required")
	}
	if _, err := s.SoftwareRelease(ctx, waiver.ReleaseID, waiver.OrganisationID); err != nil {
		return domain.ReleaseCVEWaiver{}, err
	}

	switch s.dialect {
	case DialectSQLite:
		_, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO release_cve_waivers (organisation_id, release_id, cve_id, note, user_id)
			VALUES (?, ?, ?, ?, NULLIF(?, 0))
			ON CONFLICT (organisation_id, release_id, cve_id)
			DO UPDATE SET note = excluded.note, user_id = excluded.user_id, updated_at = CURRENT_TIMESTAMP`,
			waiver.OrganisationID,
			waiver.ReleaseID,
			waiver.CVEID,
			waiver.Note,
			waiver.UserID,
		)
		if err != nil {
			return domain.ReleaseCVEWaiver{}, err
		}
	case DialectPostgres, DialectPostgreSQL:
		_, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO release_cve_waivers (organisation_id, release_id, cve_id, note, user_id)
			VALUES ($1, $2, $3, $4, NULLIF($5, 0))
			ON CONFLICT (organisation_id, release_id, cve_id)
			DO UPDATE SET note = excluded.note, user_id = excluded.user_id, updated_at = NOW()`,
			waiver.OrganisationID,
			waiver.ReleaseID,
			waiver.CVEID,
			waiver.Note,
			waiver.UserID,
		)
		if err != nil {
			return domain.ReleaseCVEWaiver{}, err
		}
	default:
		return domain.ReleaseCVEWaiver{}, fmt.Errorf("unsupported db dialect %q", s.dialect)
	}
	return s.ReleaseCVEWaiver(ctx, waiver.OrganisationID, waiver.ReleaseID, waiver.CVEID)
}

func (s *Store) DeleteReleaseCVEWaiver(ctx context.Context, organisationID int64, releaseID int64, cveID string) error {
	query := `DELETE FROM release_cve_waivers WHERE organisation_id = ? AND release_id = ? AND cve_id = ?`
	args := []any{organisationID, releaseID, strings.TrimSpace(cveID)}
	if s.isPostgres() {
		query = `DELETE FROM release_cve_waivers WHERE organisation_id = $1 AND release_id = $2 AND cve_id = $3`
	}
	return s.execOne(ctx, query, args...)
}

func (s *Store) ReleaseCVEWaiver(ctx context.Context, organisationID int64, releaseID int64, cveID string) (domain.ReleaseCVEWaiver, error) {
	query := `
		SELECT id, organisation_id, release_id, cve_id, note, COALESCE(user_id, 0), created_at, updated_at
		FROM release_cve_waivers
		WHERE organisation_id = ? AND release_id = ? AND cve_id = ?
	`
	args := []any{organisationID, releaseID, strings.TrimSpace(cveID)}
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, release_id, cve_id, note, COALESCE(user_id, 0), created_at::text, updated_at::text
			FROM release_cve_waivers
			WHERE organisation_id = $1 AND release_id = $2 AND cve_id = $3
		`
	}
	waiver, err := scanReleaseCVEWaiver(s.readDB.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReleaseCVEWaiver{}, ErrNotFound
	}
	return waiver, err
}

func (s *Store) ListReleaseCVEWaivers(ctx context.Context, organisationID int64, releaseID int64) ([]domain.ReleaseCVEWaiver, error) {
	query := `
		SELECT id, organisation_id, release_id, cve_id, note, COALESCE(user_id, 0), created_at, updated_at
		FROM release_cve_waivers
		WHERE organisation_id = ? AND release_id = ?
		ORDER BY cve_id
	`
	args := []any{organisationID, releaseID}
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, release_id, cve_id, note, COALESCE(user_id, 0), created_at::text, updated_at::text
			FROM release_cve_waivers
			WHERE organisation_id = $1 AND release_id = $2
			ORDER BY cve_id
		`
	}
	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var waivers []domain.ReleaseCVEWaiver
	for rows.Next() {
		waiver, err := scanReleaseCVEWaiver(rows)
		if err != nil {
			return nil, err
		}
		waivers = append(waivers, waiver)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return waivers, nil
}

func (s *Store) listCVEFindingsForRun(ctx context.Context, organisationID int64, releaseID int64, scanRunID int64, activeOnly bool) ([]domain.CVEScanFinding, error) {
	query := `
		SELECT f.id, f.organisation_id, f.release_id, f.scan_run_id, f.cve_id, f.severity, f.package_name, f.installed_version, f.created_at
		FROM cve_scan_findings f
		WHERE f.organisation_id = ? AND f.release_id = ? AND f.scan_run_id = ?
	`
	args := []any{organisationID, releaseID, scanRunID}
	if activeOnly {
		query += ` AND NOT EXISTS (
			SELECT 1
			FROM release_cve_waivers w
			WHERE w.organisation_id = f.organisation_id AND w.release_id = f.release_id AND w.cve_id = f.cve_id
		)`
	}
	query += ` ORDER BY f.cve_id, f.package_name, f.installed_version`

	if s.isPostgres() {
		query = `
			SELECT f.id, f.organisation_id, f.release_id, f.scan_run_id, f.cve_id, f.severity, f.package_name, f.installed_version, f.created_at::text
			FROM cve_scan_findings f
			WHERE f.organisation_id = $1 AND f.release_id = $2 AND f.scan_run_id = $3
		`
		if activeOnly {
			query += ` AND NOT EXISTS (
				SELECT 1
				FROM release_cve_waivers w
				WHERE w.organisation_id = f.organisation_id AND w.release_id = f.release_id AND w.cve_id = f.cve_id
			)`
		}
		query += ` ORDER BY f.cve_id, f.package_name, f.installed_version`
	}

	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var findings []domain.CVEScanFinding
	for rows.Next() {
		finding, err := scanCVEScanFinding(rows)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return findings, nil
}

func (s *Store) releaseSBOMTx(ctx context.Context, tx *sql.Tx, organisationID int64, sbomID int64) (domain.ReleaseSBOM, error) {
	query := `
		SELECT id, organisation_id, release_id, file_count, total_size_bytes, created_at, updated_at
		FROM software_release_sboms
		WHERE organisation_id = ? AND id = ?
	`
	args := []any{organisationID, sbomID}
	if s.isPostgres() {
		query = `
			SELECT id, organisation_id, release_id, file_count, total_size_bytes, created_at::text, updated_at::text
			FROM software_release_sboms
			WHERE organisation_id = $1 AND id = $2
		`
	}
	return scanReleaseSBOM(tx.QueryRowContext(ctx, query, args...))
}

func (s *Store) ensureReleaseExistsTx(ctx context.Context, tx *sql.Tx, organisationID int64, releaseID int64) error {
	var found int
	err := tx.QueryRowContext(ctx, s.placeholderSQL(
		`SELECT 1 FROM software_releases WHERE organisation_id = ? AND id = ?`,
		`SELECT 1 FROM software_releases WHERE organisation_id = $1 AND id = $2`,
	), organisationID, releaseID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *Store) insertFindingSQL() string {
	if s.isPostgres() {
		return `INSERT INTO cve_scan_findings (
			organisation_id, release_id, scan_run_id, cve_id, severity, package_name, installed_version
		) VALUES ($1, $2, $3, $4, $5, $6, $7)`
	}
	return `INSERT INTO cve_scan_findings (
		organisation_id, release_id, scan_run_id, cve_id, severity, package_name, installed_version
	) VALUES (?, ?, ?, ?, ?, ?, ?)`
}

func scanReleaseSBOM(row interface{ Scan(dest ...any) error }) (domain.ReleaseSBOM, error) {
	var sbom domain.ReleaseSBOM
	if err := row.Scan(
		&sbom.ID,
		&sbom.OrganisationID,
		&sbom.ReleaseID,
		&sbom.FileCount,
		&sbom.TotalSizeBytes,
		&sbom.CreatedAt,
		&sbom.UpdatedAt,
	); err != nil {
		return domain.ReleaseSBOM{}, err
	}
	return sbom, nil
}

func scanCVEScanRun(row interface{ Scan(dest ...any) error }) (domain.CVEScanRun, error) {
	var (
		run        domain.CVEScanRun
		startedAt  nullableString
		finishedAt nullableString
	)
	if err := row.Scan(
		&run.ID,
		&run.OrganisationID,
		&run.ReleaseID,
		&run.ReleaseSBOMID,
		&run.Trigger,
		&run.Status,
		&run.ErrorMessage,
		&run.CreatedAt,
		&startedAt,
		&finishedAt,
	); err != nil {
		return domain.CVEScanRun{}, err
	}
	if startedAt.Valid {
		run.StartedAt = startedAt.String
	}
	if finishedAt.Valid {
		run.FinishedAt = finishedAt.String
	}
	return run, nil
}

func scanCVEScanFinding(row interface{ Scan(dest ...any) error }) (domain.CVEScanFinding, error) {
	var finding domain.CVEScanFinding
	if err := row.Scan(
		&finding.ID,
		&finding.OrganisationID,
		&finding.ReleaseID,
		&finding.ScanRunID,
		&finding.CVEID,
		&finding.Severity,
		&finding.PackageName,
		&finding.InstalledVersion,
		&finding.CreatedAt,
	); err != nil {
		return domain.CVEScanFinding{}, err
	}
	return finding, nil
}

func scanReleaseCVEWaiver(row interface{ Scan(dest ...any) error }) (domain.ReleaseCVEWaiver, error) {
	var waiver domain.ReleaseCVEWaiver
	if err := row.Scan(
		&waiver.ID,
		&waiver.OrganisationID,
		&waiver.ReleaseID,
		&waiver.CVEID,
		&waiver.Note,
		&waiver.UserID,
		&waiver.CreatedAt,
		&waiver.UpdatedAt,
	); err != nil {
		return domain.ReleaseCVEWaiver{}, err
	}
	return waiver, nil
}

func dedupeCVEScanFindings(findings []domain.CVEScanFinding) []domain.CVEScanFinding {
	deduped := make([]domain.CVEScanFinding, 0, len(findings))
	seen := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		finding.CVEID = strings.TrimSpace(finding.CVEID)
		finding.PackageName = strings.TrimSpace(finding.PackageName)
		finding.InstalledVersion = strings.TrimSpace(finding.InstalledVersion)
		if finding.CVEID == "" {
			continue
		}
		key := finding.CVEID + "\x00" + finding.PackageName + "\x00" + finding.InstalledVersion
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, finding)
	}
	return deduped
}

func (s *Store) execOne(ctx context.Context, query string, args ...any) error {
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
	return nil
}

func (s *Store) placeholderSQL(sqliteQuery string, postgresQuery string) string {
	if s.isPostgres() {
		return postgresQuery
	}
	return sqliteQuery
}

func (s *Store) isPostgres() bool {
	return s.dialect == DialectPostgres || s.dialect == DialectPostgreSQL
}

func nullableTime(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

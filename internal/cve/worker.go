package cve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"
)

const (
	// DefaultScannerPath is the executable name used when no scanner path is configured.
	DefaultScannerPath = "grype"
	staleRunningError  = "scan interrupted by process restart"
)

// Store contains the persistence operations required by Worker.
type Store interface {
	ListPendingCVEScanRuns(ctx context.Context) ([]domain.CVEScanRun, error)
	MarkRunningCVEScansFailed(ctx context.Context, finishedAt string, errorMessage string) (int64, error)
	StartCVEScanRun(ctx context.Context, organisationID int64, scanRunID int64, startedAt string) error
	CompleteCVEScanRun(ctx context.Context, organisationID int64, scanRunID int64, finishedAt string, findings []domain.CVEScanFinding) error
	FailCVEScanRun(ctx context.Context, organisationID int64, scanRunID int64, finishedAt string, errorMessage string) error
	SoftwareRelease(ctx context.Context, releaseID int64, organisationID int64) (domain.SoftwareRelease, error)
}

// Scanner scans one SPDX file and returns vulnerability results as Grype JSON.
type Scanner interface {
	ScanSPDX(ctx context.Context, path string) ([]byte, error)
}

// Worker drains queued CVE scans and persists their results.
type Worker struct {
	store             Store
	scanner           Scanner
	releaseStorageDir string
	now               func() time.Time
	logger            *slog.Logger
	notify            chan struct{}
}

// Config supplies Worker dependencies. Scanner, Logger, and Now receive
// production defaults when omitted; Store is required. ReleaseStorageDir is the
// root used to resolve stored release and SBOM paths.
type Config struct {
	Store             Store
	Scanner           Scanner
	ScannerPath       string
	ReleaseStorageDir string
	Logger            *slog.Logger
	Now               func() time.Time
}

// NewWorker constructs a stopped Worker. Call Start to recover interrupted work
// and begin asynchronous queue processing.
func NewWorker(config Config) *Worker {
	scanner := config.Scanner
	if scanner == nil {
		scannerPath := strings.TrimSpace(config.ScannerPath)
		if scannerPath == "" {
			scannerPath = DefaultScannerPath
		}
		scanner = ExternalScanner{Path: scannerPath}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Worker{
		store:             config.Store,
		scanner:           scanner,
		releaseStorageDir: config.ReleaseStorageDir,
		now:               now,
		logger:            logger,
		notify:            make(chan struct{}, 1),
	}
}

// Start marks scans interrupted by an earlier process as failed, starts the
// processing loop, and schedules an initial queue check. The loop stops when ctx
// is canceled.
func (w *Worker) Start(ctx context.Context) error {
	if w.store == nil {
		return errors.New("cve worker store is required")
	}
	if w.scanner == nil {
		return errors.New("cve worker scanner is required")
	}
	if _, err := w.store.MarkRunningCVEScansFailed(ctx, formatTime(w.now()), staleRunningError); err != nil {
		return err
	}

	go w.loop(ctx)
	w.Notify()
	return nil
}

// Notify schedules a queue check without blocking. Multiple notifications may
// be coalesced while a check is already pending.
func (w *Worker) Notify() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// ProcessPending drains queued scans until none remain. Failures for individual
// scans are logged and do not prevent later scans from being attempted; errors
// reading the queue are returned.
func (w *Worker) ProcessPending(ctx context.Context) error {
	for {
		runs, err := w.store.ListPendingCVEScanRuns(ctx)
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			return nil
		}
		for _, run := range runs {
			if err := w.processRun(ctx, run); err != nil {
				w.logger.Warn("cve scan worker failed", "scan_run_id", run.ID, "organisation_id", run.OrganisationID, "release_id", run.ReleaseID, "err", err)
			}
		}
	}
}

func (w *Worker) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
			if err := w.ProcessPending(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.Warn("cve scan worker queue processing failed", "err", err)
			}
		}
	}
}

func (w *Worker) processRun(ctx context.Context, run domain.CVEScanRun) error {
	startedAt := formatTime(w.now())
	if err := w.store.StartCVEScanRun(ctx, run.OrganisationID, run.ID, startedAt); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		return err
	}

	findings, err := w.scanRelease(ctx, run)
	if err != nil {
		if failErr := w.store.FailCVEScanRun(ctx, run.OrganisationID, run.ID, formatTime(w.now()), err.Error()); failErr != nil && !errors.Is(failErr, db.ErrNotFound) {
			return errors.Join(err, failErr)
		}
		return err
	}

	if err := w.store.CompleteCVEScanRun(ctx, run.OrganisationID, run.ID, formatTime(w.now()), findings); err != nil {
		return err
	}
	return nil
}

func (w *Worker) scanRelease(ctx context.Context, run domain.CVEScanRun) ([]domain.CVEScanFinding, error) {
	release, err := w.store.SoftwareRelease(ctx, run.ReleaseID, run.OrganisationID)
	if err != nil {
		return nil, err
	}
	sbomFiles, err := w.releaseSBOMFiles(release.ArtifactPath)
	if err != nil {
		return nil, err
	}
	if len(sbomFiles) == 0 {
		return nil, errors.New("no SPDX files found for release SBOM")
	}
	var findings []domain.CVEScanFinding
	for _, path := range sbomFiles {
		output, err := w.scanner.ScanSPDX(ctx, path)
		if err != nil {
			return nil, err
		}
		fileFindings, err := grypeFindings(output)
		if err != nil {
			return nil, fmt.Errorf("parse grype output for %s: %w", filepath.Base(path), err)
		}
		findings = append(findings, fileFindings...)
	}
	return findings, nil
}

func (w *Worker) releaseSBOMFiles(artifactPath string) ([]string, error) {
	fullDir, ok := releaseArtifactFullPath(w.releaseStorageDir, releaseSBOMRelativeDir(artifactPath))
	if !ok {
		return nil, errors.New("release SBOM storage path is invalid")
	}
	entries, err := os.ReadDir(fullDir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".spdx") {
			files = append(files, filepath.Join(fullDir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

// ExternalScanner invokes a Grype executable to scan SPDX files.
type ExternalScanner struct {
	Path string
}

// ScanSPDX scans path with Grype and returns its JSON output. If Grype rejects
// external SPDX document references, it retries with a temporary sanitized copy.
func (s ExternalScanner) ScanSPDX(ctx context.Context, path string) ([]byte, error) {
	output, message, err := s.runGrype(ctx, path)
	if err == nil {
		return output, nil
	}
	if ctx.Err() == nil && shouldRetrySPDXWithoutExternalDocumentRefs(message) {
		if output, retryErr := s.scanSPDXWithoutExternalDocumentRefs(ctx, path); retryErr == nil {
			return output, nil
		}
	}
	if message == "" {
		message = err.Error()
	}
	return nil, fmt.Errorf("grype scan failed for %s: %s", filepath.Base(path), message)
}

func (s ExternalScanner) runGrype(ctx context.Context, path string) ([]byte, string, error) {
	scannerPath := strings.TrimSpace(s.Path)
	if scannerPath == "" {
		scannerPath = DefaultScannerPath
	}
	cmd := exec.CommandContext(ctx, scannerPath, "sbom:"+path, "-o", "json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		return nil, message, err
	}
	return output, "", nil
}

func (s ExternalScanner) scanSPDXWithoutExternalDocumentRefs(ctx context.Context, path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sanitized, changed := stripSPDXExternalDocumentRefs(content)
	if !changed {
		return nil, errors.New("no ExternalDocumentRef tags found")
	}

	file, err := os.CreateTemp("", "anchor-spdx-*.spdx")
	if err != nil {
		return nil, err
	}
	tmpPath := file.Name()
	defer os.Remove(tmpPath)

	if _, err := file.Write(sanitized); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}

	output, _, err := s.runGrype(ctx, tmpPath)
	return output, err
}

func shouldRetrySPDXWithoutExternalDocumentRefs(message string) bool {
	return strings.Contains(message, "unable to decode spdx tag-value") &&
		strings.Contains(message, "ExternalDocumentRef")
}

func stripSPDXExternalDocumentRefs(content []byte) ([]byte, bool) {
	lines := strings.SplitAfter(string(content), "\n")
	var builder strings.Builder
	changed := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "ExternalDocumentRef:") {
			changed = true
			continue
		}
		builder.WriteString(line)
	}
	return []byte(builder.String()), changed
}

type grypeDocument struct {
	Matches []grypeMatch `json:"matches"`
}

type grypeMatch struct {
	Vulnerability          grypeVulnerability   `json:"vulnerability"`
	RelatedVulnerabilities []grypeVulnerability `json:"relatedVulnerabilities"`
	Artifact               grypeArtifact        `json:"artifact"`
}

type grypeVulnerability struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
}

type grypeArtifact struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func grypeFindings(output []byte) ([]domain.CVEScanFinding, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return nil, nil
	}

	var document grypeDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return nil, err
	}

	var findings []domain.CVEScanFinding
	for _, match := range document.Matches {
		packageName := strings.TrimSpace(match.Artifact.Name)
		installedVersion := strings.TrimSpace(match.Artifact.Version)
		vulnerabilities := append([]grypeVulnerability{match.Vulnerability}, match.RelatedVulnerabilities...)
		for _, vulnerability := range vulnerabilities {
			cveID := normalizedCVEID(vulnerability.ID)
			if cveID == "" {
				continue
			}
			severity := strings.TrimSpace(vulnerability.Severity)
			if severity == "" {
				severity = strings.TrimSpace(match.Vulnerability.Severity)
			}
			findings = append(findings, domain.CVEScanFinding{
				CVEID:            cveID,
				Severity:         severity,
				PackageName:      packageName,
				InstalledVersion: installedVersion,
			})
		}
	}
	return findings, nil
}

func normalizedCVEID(id string) string {
	id = strings.TrimSpace(strings.ToUpper(id))
	if !strings.HasPrefix(id, "CVE-") {
		return ""
	}
	return id
}

func releaseSBOMRelativeDir(artifactPath string) string {
	return artifactPath + ".sbom"
}

func releaseArtifactFullPath(storageDir string, relativePath string) (string, bool) {
	cleanPath := filepath.Clean(relativePath)
	if cleanPath == "." || filepath.IsAbs(cleanPath) || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) || cleanPath == ".." {
		return "", false
	}
	return filepath.Join(storageDir, cleanPath), true
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

package cve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"
)

func TestWorkerProcessesPendingScansAsynchronously(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, organisationID, releaseID, storageDir := testWorkerRelease(t)
	defer store.Close()
	if _, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "auto"); err != nil {
		t.Fatalf("enqueue scan: %v", err)
	}

	scanner := &recordingScanner{}
	worker := NewWorker(Config{
		Store:             store,
		Scanner:           scanner,
		ReleaseStorageDir: storageDir,
		Now:               fixedWorkerTime,
	})
	if err := worker.Start(ctx); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	waitFor(t, func() bool {
		runs, err := store.ListCVEScanRuns(ctx, organisationID, releaseID)
		if err != nil || len(runs) != 1 {
			return false
		}
		return runs[0].Status == "success"
	})
	if got := scanner.paths(); len(got) != 2 {
		t.Fatalf("expected scanner to receive two SPDX files, got %#v", got)
	}
}

func TestWorkerPersistsGrypeFindings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, organisationID, releaseID, storageDir := testWorkerRelease(t)
	defer store.Close()
	if _, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "auto"); err != nil {
		t.Fatalf("enqueue scan: %v", err)
	}

	scanner := &recordingScanner{
		outputs: map[string][]byte{
			"app.spdx": []byte(`{
				"matches": [
					{
						"vulnerability": {"id": "CVE-2021-43666", "severity": "High"},
						"artifact": {"name": "mbedtls", "version": "2.16.0"}
					},
					{
						"vulnerability": {"id": "GHSA-test", "severity": "Medium"},
						"relatedVulnerabilities": [{"id": "CVE-2020-16150", "severity": "Critical"}],
						"artifact": {"name": "mbedtls", "version": "2.16.0"}
					}
				]
			}`),
			"build.spdx": []byte(`{"matches":[]}`),
		},
	}
	worker := NewWorker(Config{
		Store:             store,
		Scanner:           scanner,
		ReleaseStorageDir: storageDir,
		Now:               fixedWorkerTime,
	})
	if err := worker.ProcessPending(ctx); err != nil {
		t.Fatalf("process pending: %v", err)
	}

	findings, err := store.ListCurrentCVEFindings(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected two CVE findings, got %#v", findings)
	}
	if findings[0].CVEID != "CVE-2020-16150" || findings[0].Severity != "Critical" || findings[0].PackageName != "mbedtls" || findings[0].InstalledVersion != "2.16.0" {
		t.Fatalf("unexpected first finding: %#v", findings[0])
	}
	if findings[1].CVEID != "CVE-2021-43666" || findings[1].Severity != "High" || findings[1].PackageName != "mbedtls" || findings[1].InstalledVersion != "2.16.0" {
		t.Fatalf("unexpected second finding: %#v", findings[1])
	}
}

func TestWorkerPersistsScannerFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, organisationID, releaseID, storageDir := testWorkerRelease(t)
	defer store.Close()
	run, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "auto")
	if err != nil {
		t.Fatalf("enqueue scan: %v", err)
	}

	worker := NewWorker(Config{
		Store:             store,
		Scanner:           failingScanner{err: errors.New("scanner unavailable")},
		ReleaseStorageDir: storageDir,
		Now:               fixedWorkerTime,
	})
	if err := worker.ProcessPending(ctx); err != nil {
		t.Fatalf("process pending: %v", err)
	}

	updated, err := store.CVEScanRun(ctx, organisationID, run.ID)
	if err != nil {
		t.Fatalf("load scan run: %v", err)
	}
	if updated.Status != "failed" || updated.ErrorMessage != "scanner unavailable" {
		t.Fatalf("expected failed scan run with scanner error, got %#v", updated)
	}
}

func TestWorkerTreatsMissingGrypeAsFailedScan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, organisationID, releaseID, storageDir := testWorkerRelease(t)
	defer store.Close()
	run, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "auto")
	if err != nil {
		t.Fatalf("enqueue scan: %v", err)
	}

	worker := NewWorker(Config{
		Store:             store,
		ScannerPath:       filepath.Join(t.TempDir(), "missing-grype"),
		ReleaseStorageDir: storageDir,
		Now:               fixedWorkerTime,
	})
	if err := worker.ProcessPending(ctx); err != nil {
		t.Fatalf("process pending: %v", err)
	}

	updated, err := store.CVEScanRun(ctx, organisationID, run.ID)
	if err != nil {
		t.Fatalf("load scan run: %v", err)
	}
	if updated.Status != "failed" || updated.ErrorMessage == "" {
		t.Fatalf("expected missing scanner to be persisted as failed scan, got %#v", updated)
	}
}

func TestExternalScannerRetriesSPDXWithoutExternalDocumentRefs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	grypePath := filepath.Join(dir, "grype")
	grypeScript := `#!/bin/sh
file=${1#sbom:}
if grep -q '^ExternalDocumentRef:' "$file"; then
  echo 'failed to catalog: unable to decode sbom: unable to decode spdx tag-value: received unknown tag ExternalDocumentRef in CreationInfo section' >&2
  exit 1
fi
printf '{"matches":[]}'
`
	if err := os.WriteFile(grypePath, []byte(grypeScript), 0o755); err != nil {
		t.Fatalf("write fake grype: %v", err)
	}
	sbomPath := filepath.Join(dir, "build.spdx")
	sbomContent := "SPDXVersion: SPDX-2.3\nDataLicense: CC0-1.0\nSPDXID: SPDXRef-DOCUMENT\nExternalDocumentRef: DocumentRef-zephyr SPDXRef-DOCUMENT SHA1: 0000000000000000000000000000000000000000\nPackageName: app\n"
	if err := os.WriteFile(sbomPath, []byte(sbomContent), 0o644); err != nil {
		t.Fatalf("write sbom: %v", err)
	}

	output, err := (ExternalScanner{Path: grypePath}).ScanSPDX(context.Background(), sbomPath)
	if err != nil {
		t.Fatalf("scan spdx: %v", err)
	}
	if string(output) != `{"matches":[]}` {
		t.Fatalf("unexpected grype output: %q", output)
	}
	stored, err := os.ReadFile(sbomPath)
	if err != nil {
		t.Fatalf("read stored sbom: %v", err)
	}
	if !strings.Contains(string(stored), "ExternalDocumentRef:") {
		t.Fatalf("expected stored sbom to remain unchanged, got %q", stored)
	}
}

func TestWorkerStartupMarksRunningScansFailedAndContinuesPending(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, organisationID, runningReleaseID, storageDir := testWorkerRelease(t)
	defer store.Close()
	runningRun, err := store.EnqueueCVEScan(ctx, organisationID, runningReleaseID, "auto")
	if err != nil {
		t.Fatalf("enqueue running scan: %v", err)
	}
	if err := store.StartCVEScanRun(ctx, organisationID, runningRun.ID, "2026-06-29T10:00:00Z"); err != nil {
		t.Fatalf("start running scan: %v", err)
	}

	pendingReleaseID := testSoftwareReleaseID(t, store, organisationID, "2.0.0")
	if _, err := store.ReplaceReleaseSBOM(ctx, organisationID, pendingReleaseID, 1, 64); err != nil {
		t.Fatalf("replace pending release sbom: %v", err)
	}
	writeReleaseSBOMFiles(t, storageDir, "1/firmware-2.bin", "app.spdx")
	pendingRun, err := store.EnqueueCVEScan(ctx, organisationID, pendingReleaseID, "auto")
	if err != nil {
		t.Fatalf("enqueue pending scan: %v", err)
	}

	worker := NewWorker(Config{
		Store:             store,
		Scanner:           &recordingScanner{},
		ReleaseStorageDir: storageDir,
		Now:               fixedWorkerTime,
	})
	if err := worker.Start(ctx); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	waitFor(t, func() bool {
		updated, err := store.CVEScanRun(ctx, organisationID, pendingRun.ID)
		return err == nil && updated.Status == "success"
	})

	stale, err := store.CVEScanRun(ctx, organisationID, runningRun.ID)
	if err != nil {
		t.Fatalf("load stale scan: %v", err)
	}
	if stale.Status != "failed" || stale.ErrorMessage != staleRunningError {
		t.Fatalf("expected stale running scan to be failed deterministically, got %#v", stale)
	}
}

func testWorkerRelease(t *testing.T) (*db.Store, int64, int64, string) {
	t.Helper()

	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{
		Dialect: db.DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: t.Name()})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	releaseID := testSoftwareReleaseID(t, store, organisationID, "1.2.3")
	if _, err := store.ReplaceReleaseSBOM(ctx, organisationID, releaseID, 2, 128); err != nil {
		t.Fatalf("replace release sbom: %v", err)
	}
	storageDir := t.TempDir()
	writeReleaseSBOMFiles(t, storageDir, "1/firmware.bin", "app.spdx", "build.spdx")
	return store, organisationID, releaseID, storageDir
}

func testSoftwareReleaseID(t *testing.T, store *db.Store, organisationID int64, version string) int64 {
	t.Helper()

	ctx := context.Background()
	modelID, err := store.CreateDeviceModel(ctx, domain.DeviceModel{
		OrganisationID:           organisationID,
		Name:                     "Gateway " + version,
		ExpectedHeartbeatSeconds: 60,
		ExpectedProtocol:         "mqtt",
	})
	if err != nil {
		t.Fatalf("create device model: %v", err)
	}
	artifactPath := "1/firmware.bin"
	if version == "2.0.0" {
		artifactPath = "1/firmware-2.bin"
	}
	releaseID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		DeviceModelID:       modelID,
		Version:             version,
		ArtifactPath:        artifactPath,
		ArtifactFilename:    filepath.Base(artifactPath),
		ArtifactContentType: "application/octet-stream",
		ArtifactSizeBytes:   4,
	})
	if err != nil {
		t.Fatalf("create software release: %v", err)
	}
	return releaseID
}

func writeReleaseSBOMFiles(t *testing.T, storageDir string, artifactPath string, filenames ...string) {
	t.Helper()

	sbomDir, ok := releaseArtifactFullPath(storageDir, releaseSBOMRelativeDir(artifactPath))
	if !ok {
		t.Fatalf("invalid sbom path for %q", artifactPath)
	}
	if err := os.MkdirAll(sbomDir, 0o755); err != nil {
		t.Fatalf("create sbom dir: %v", err)
	}
	for _, filename := range filenames {
		if err := os.WriteFile(filepath.Join(sbomDir, filename), []byte("SPDXVersion: SPDX-2.3\n"), 0o644); err != nil {
			t.Fatalf("write sbom file: %v", err)
		}
	}
}

type recordingScanner struct {
	mu       sync.Mutex
	scanned  []string
	scanErrs map[string]error
	outputs  map[string][]byte
}

func (s *recordingScanner) ScanSPDX(_ context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.scanned = append(s.scanned, filepath.Base(path))
	if s.scanErrs != nil {
		if err := s.scanErrs[filepath.Base(path)]; err != nil {
			return nil, err
		}
	}
	if s.outputs != nil {
		if output := s.outputs[filepath.Base(path)]; output != nil {
			return output, nil
		}
	}
	return []byte(`{"matches":[]}`), nil
}

func (s *recordingScanner) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	paths := make([]string, len(s.scanned))
	copy(paths, s.scanned)
	return paths
}

type failingScanner struct {
	err error
}

func (s failingScanner) ScanSPDX(context.Context, string) ([]byte, error) {
	return nil, s.err
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func fixedWorkerTime() time.Time {
	return time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
}

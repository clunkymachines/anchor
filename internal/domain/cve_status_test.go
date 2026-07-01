package domain

import "testing"

func TestCalculateReleaseCVEStatusCoversReleaseStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		hasSBOM               bool
		runs                  []CVEScanRun
		findings              []CVEScanFinding
		wantStatus            CVEImpactStatusValue
		wantCount             int
		wantSeverity          string
		wantWarning           bool
		wantLatestSuccessful  int64
		wantLatestAttemptedID int64
	}{
		{
			name:       "no sbom",
			hasSBOM:    false,
			wantStatus: CVEStatusNoSBOM,
		},
		{
			name:       "not scanned",
			hasSBOM:    true,
			wantStatus: CVEStatusNotScanned,
		},
		{
			name:                  "scan pending",
			hasSBOM:               true,
			runs:                  []CVEScanRun{{ID: 1, Status: "pending"}},
			wantStatus:            CVEStatusScanPending,
			wantLatestAttemptedID: 1,
		},
		{
			name:                  "running scan is pending status",
			hasSBOM:               true,
			runs:                  []CVEScanRun{{ID: 1, Status: "running"}},
			wantStatus:            CVEStatusScanPending,
			wantLatestAttemptedID: 1,
		},
		{
			name:                  "scan failed without previous success",
			hasSBOM:               true,
			runs:                  []CVEScanRun{{ID: 1, Status: "failed"}},
			wantStatus:            CVEStatusScanFailed,
			wantLatestAttemptedID: 1,
		},
		{
			name:                  "impacted",
			hasSBOM:               true,
			runs:                  []CVEScanRun{{ID: 1, Status: "success"}},
			findings:              []CVEScanFinding{{CVEID: "CVE-2026-0001", Severity: "high"}},
			wantStatus:            CVEStatusImpacted,
			wantCount:             1,
			wantSeverity:          "high",
			wantLatestSuccessful:  1,
			wantLatestAttemptedID: 1,
		},
		{
			name:                  "not impacted",
			hasSBOM:               true,
			runs:                  []CVEScanRun{{ID: 1, Status: "success"}},
			wantStatus:            CVEStatusNotImpacted,
			wantLatestSuccessful:  1,
			wantLatestAttemptedID: 1,
		},
		{
			name:    "failed latest keeps previous successful impact with warning",
			hasSBOM: true,
			runs: []CVEScanRun{
				{ID: 1, Status: "success"},
				{ID: 2, Status: "failed"},
			},
			findings:              []CVEScanFinding{{CVEID: "CVE-2026-0001", Severity: "medium"}},
			wantStatus:            CVEStatusImpacted,
			wantCount:             1,
			wantSeverity:          "medium",
			wantWarning:           true,
			wantLatestSuccessful:  1,
			wantLatestAttemptedID: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := CalculateReleaseCVEStatus(tt.hasSBOM, tt.runs, tt.findings)
			if got.Status != tt.wantStatus {
				t.Fatalf("status: got %q want %q", got.Status, tt.wantStatus)
			}
			if got.ActiveCVECount != tt.wantCount {
				t.Fatalf("active count: got %d want %d", got.ActiveCVECount, tt.wantCount)
			}
			if got.HighestActiveSeverity != tt.wantSeverity {
				t.Fatalf("highest severity: got %q want %q", got.HighestActiveSeverity, tt.wantSeverity)
			}
			if got.HasLatestScanWarning != tt.wantWarning {
				t.Fatalf("warning: got %t want %t", got.HasLatestScanWarning, tt.wantWarning)
			}
			if got.LatestSuccessfulScanID != tt.wantLatestSuccessful {
				t.Fatalf("latest successful: got %d want %d", got.LatestSuccessfulScanID, tt.wantLatestSuccessful)
			}
			if got.LatestAttemptedScanID != tt.wantLatestAttemptedID {
				t.Fatalf("latest attempted: got %d want %d", got.LatestAttemptedScanID, tt.wantLatestAttemptedID)
			}
		})
	}
}

func TestCalculateReleaseCVEStatusCountsUniqueActiveCVEsAndHighestSeverity(t *testing.T) {
	t.Parallel()

	got := CalculateReleaseCVEStatus(true, []CVEScanRun{{ID: 1, Status: "success"}}, []CVEScanFinding{
		{CVEID: "CVE-2026-0001", Severity: "low", PackageName: "lib-a", InstalledVersion: "1.0.0"},
		{CVEID: "CVE-2026-0001", Severity: "critical", PackageName: "lib-b", InstalledVersion: "2.0.0"},
		{CVEID: "CVE-2026-0002", Severity: "medium", PackageName: "lib-c", InstalledVersion: "3.0.0"},
		{CVEID: "CVE-2026-0003", Severity: "unexpected", PackageName: "lib-d", InstalledVersion: "4.0.0"},
	})
	if got.Status != CVEStatusImpacted {
		t.Fatalf("status: got %q want %q", got.Status, CVEStatusImpacted)
	}
	if got.ActiveCVECount != 3 {
		t.Fatalf("active count: got %d want 3", got.ActiveCVECount)
	}
	if got.HighestActiveSeverity != "critical" {
		t.Fatalf("highest severity: got %q want critical", got.HighestActiveSeverity)
	}
}

func TestCalculateReleaseCVEStatusUsesWaiverFilteredFindings(t *testing.T) {
	t.Parallel()

	got := CalculateReleaseCVEStatus(true, []CVEScanRun{{ID: 1, Status: "success"}}, nil)
	if got.Status != CVEStatusNotImpacted {
		t.Fatalf("status: got %q want %q", got.Status, CVEStatusNotImpacted)
	}
	if got.ActiveCVECount != 0 || got.HighestActiveSeverity != "" {
		t.Fatalf("expected waived findings to be excluded from active state, got %#v", got)
	}
}

func TestCalculateDeviceCVEStatusUnknownRelease(t *testing.T) {
	t.Parallel()

	got := CalculateDeviceCVEStatus(false, CVEImpactStatus{Status: CVEStatusImpacted})
	if got.Status != CVEStatusUnknownRelease {
		t.Fatalf("status: got %q want %q", got.Status, CVEStatusUnknownRelease)
	}

	matched := CalculateDeviceCVEStatus(true, CVEImpactStatus{Status: CVEStatusImpacted, ActiveCVECount: 1})
	if matched.Status != CVEStatusImpacted || matched.ActiveCVECount != 1 {
		t.Fatalf("expected matched device to use release status, got %#v", matched)
	}
}

func TestHighestSeverityOrdering(t *testing.T) {
	t.Parallel()

	got := highestSeverity([]string{"unknown", "negligible", "low", "medium", "high", "critical"})
	if got != "critical" {
		t.Fatalf("highest severity: got %q want critical", got)
	}

	got = highestSeverity([]string{"unknown", "negligible"})
	if got != "negligible" {
		t.Fatalf("highest severity: got %q want negligible", got)
	}
}

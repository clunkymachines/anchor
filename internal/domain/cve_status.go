package domain

import "strings"

type CVEImpactStatusValue string

const (
	CVEStatusNoSBOM         CVEImpactStatusValue = "no_sbom"
	CVEStatusScanPending    CVEImpactStatusValue = "scan_pending"
	CVEStatusNotScanned     CVEImpactStatusValue = "not_scanned"
	CVEStatusImpacted       CVEImpactStatusValue = "impacted"
	CVEStatusNotImpacted    CVEImpactStatusValue = "not_impacted"
	CVEStatusScanFailed     CVEImpactStatusValue = "scan_failed"
	CVEStatusUnknownRelease CVEImpactStatusValue = "unknown_release"
)

type CVEImpactStatus struct {
	Status                 CVEImpactStatusValue
	ActiveCVECount         int
	HighestActiveSeverity  string
	HasLatestScanWarning   bool
	LatestScanWarning      string
	LatestAttemptedScanID  int64
	LatestSuccessfulScanID int64
	MatchedReleaseID       int64
}

func CalculateReleaseCVEStatus(hasSBOM bool, scanRuns []CVEScanRun, activeFindings []CVEScanFinding) CVEImpactStatus {
	if !hasSBOM {
		return CVEImpactStatus{Status: CVEStatusNoSBOM}
	}

	latestAttempt := latestCVEScanRun(scanRuns, "")
	if latestAttempt == nil {
		return CVEImpactStatus{Status: CVEStatusNotScanned}
	}

	status := CVEImpactStatus{
		LatestAttemptedScanID: latestAttempt.ID,
	}
	switch latestAttempt.Status {
	case "pending", "running":
		status.Status = CVEStatusScanPending
		return status
	}

	latestSuccess := latestCVEScanRun(scanRuns, "success")
	if latestSuccess == nil {
		if latestAttempt.Status == "failed" {
			status.Status = CVEStatusScanFailed
			return status
		}
		status.Status = CVEStatusNotScanned
		return status
	}

	status.LatestSuccessfulScanID = latestSuccess.ID
	status.ActiveCVECount, status.HighestActiveSeverity = activeCVECountAndHighestSeverity(activeFindings)
	if status.ActiveCVECount > 0 {
		status.Status = CVEStatusImpacted
	} else {
		status.Status = CVEStatusNotImpacted
	}
	if latestAttempt.Status == "failed" && latestAttempt.ID != latestSuccess.ID {
		status.HasLatestScanWarning = true
		status.LatestScanWarning = "Latest scan failed; showing latest successful scan."
	}
	return status
}

func CalculateDeviceCVEStatus(releaseMatched bool, releaseStatus CVEImpactStatus) CVEImpactStatus {
	if !releaseMatched {
		return CVEImpactStatus{Status: CVEStatusUnknownRelease}
	}
	return releaseStatus
}

func activeCVECountAndHighestSeverity(findings []CVEScanFinding) (int, string) {
	cves := make(map[string]struct{})
	highest := ""
	for _, finding := range findings {
		cveID := strings.TrimSpace(finding.CVEID)
		if cveID == "" {
			continue
		}
		cves[cveID] = struct{}{}
		highest = higherSeverity(highest, finding.Severity)
	}
	return len(cves), highest
}

func highestSeverity(severities []string) string {
	highest := ""
	for _, severity := range severities {
		highest = higherSeverity(highest, severity)
	}
	return highest
}

func higherSeverity(current string, candidate string) string {
	candidate = normalizeSeverity(candidate)
	if candidate == "" {
		return current
	}
	if current == "" || severityRank(candidate) > severityRank(current) {
		return candidate
	}
	return current
}

func normalizeSeverity(severity string) string {
	severity = strings.TrimSpace(strings.ToLower(severity))
	if severity == "" {
		return ""
	}
	switch severity {
	case "critical", "high", "medium", "low", "negligible", "unknown":
		return severity
	default:
		return "unknown"
	}
}

func severityRank(severity string) int {
	switch normalizeSeverity(severity) {
	case "critical":
		return 6
	case "high":
		return 5
	case "medium":
		return 4
	case "low":
		return 3
	case "negligible":
		return 2
	case "unknown":
		return 1
	default:
		return 0
	}
}

func latestCVEScanRun(scanRuns []CVEScanRun, status string) *CVEScanRun {
	var latest *CVEScanRun
	for i := range scanRuns {
		if status != "" && scanRuns[i].Status != status {
			continue
		}
		if latest == nil || scanRuns[i].ID > latest.ID {
			latest = &scanRuns[i]
		}
	}
	return latest
}

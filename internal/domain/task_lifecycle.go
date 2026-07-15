package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// TaskStatusQueued means the task is waiting behind another task for its device.
	TaskStatusQueued = "queued"
	// TaskStatusPending means the task is ready and waiting for the device.
	TaskStatusPending = "pending"
	// TaskStatusInProgress means the device has started the task.
	TaskStatusInProgress = "in_progress"
	// TaskStatusSuccess means the device completed the task successfully.
	TaskStatusSuccess = "success"
	// TaskStatusFailure means the device completed the task unsuccessfully.
	TaskStatusFailure = "failure"
	// TaskStatusExpired means the server deadline elapsed before completion.
	TaskStatusExpired = "expired"
	// TaskStatusCanceled means a user or campaign canceled the task.
	TaskStatusCanceled = "canceled"

	// CampaignStatusRunning means at least one campaign task remains non-terminal.
	CampaignStatusRunning = "running"
	// CampaignStatusFinished means every campaign task reached a terminal state.
	CampaignStatusFinished = "finished"
	// CampaignStatusCanceled means the campaign was explicitly canceled.
	CampaignStatusCanceled = "canceled"

	// DefaultTaskTTLDays is applied when no explicit task lifetime is supplied.
	DefaultTaskTTLDays = 7
	// SecondsPerDay converts whole-day task lifetimes to seconds.
	SecondsPerDay = 24 * 60 * 60
)

// IsTaskActiveStatus reports whether a task has been offered to or started by a device.
func IsTaskActiveStatus(status string) bool {
	return status == TaskStatusPending || status == TaskStatusInProgress
}

// IsTaskNonTerminalStatus reports whether a task may still change state.
func IsTaskNonTerminalStatus(status string) bool {
	return status == TaskStatusQueued || status == TaskStatusPending || status == TaskStatusInProgress
}

// IsTaskTerminalStatus reports whether a task has reached a final state.
func IsTaskTerminalStatus(status string) bool {
	switch status {
	case TaskStatusSuccess, TaskStatusFailure, TaskStatusExpired, TaskStatusCanceled:
		return true
	default:
		return false
	}
}

// DeviceReportAllowed reports whether a device may move a task from
// currentStatus to nextStatus. Server-owned queued, expired, and canceled states
// cannot be changed by device reports.
func DeviceReportAllowed(currentStatus string, nextStatus string) bool {
	switch currentStatus {
	case TaskStatusPending:
		return nextStatus == TaskStatusInProgress || nextStatus == TaskStatusSuccess || nextStatus == TaskStatusFailure
	case TaskStatusInProgress:
		return nextStatus == TaskStatusInProgress || nextStatus == TaskStatusSuccess || nextStatus == TaskStatusFailure
	default:
		return false
	}
}

// ParseTaskTTLDays parses a positive whole-day lifetime and returns both its day
// count and equivalent seconds.
func ParseTaskTTLDays(input string) (int, int64, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, 0, errors.New("task TTL is required")
	}
	days64, err := strconv.ParseInt(input, 10, 32)
	if err != nil || days64 <= 0 {
		return 0, 0, errors.New("task TTL must be a positive whole number of days")
	}
	if days64 > int64((1<<31-1)/SecondsPerDay) {
		return 0, 0, errors.New("task TTL is too large")
	}
	return int(days64), days64 * SecondsPerDay, nil
}

// TaskExpiresAt adds a validated positive lifetime in seconds to createdAt.
func TaskExpiresAt(createdAt time.Time, ttlSeconds int64) (time.Time, error) {
	if ttlSeconds <= 0 {
		return time.Time{}, errors.New("task TTL must be positive")
	}
	duration := time.Duration(ttlSeconds) * time.Second
	if duration/time.Second != time.Duration(ttlSeconds) {
		return time.Time{}, errors.New("task TTL is too large")
	}
	return createdAt.Add(duration), nil
}

// TaskExpiryMessage returns a human-readable explanation for an expired task.
func TaskExpiryMessage(ttlSeconds int64) string {
	if ttlSeconds%SecondsPerDay == 0 {
		days := ttlSeconds / SecondsPerDay
		if days == 1 {
			return "Task expired after 1 day without terminal device status."
		}
		return fmt.Sprintf("Task expired after %d days without terminal device status.", days)
	}
	return fmt.Sprintf("Task expired after %d seconds without terminal device status.", ttlSeconds)
}

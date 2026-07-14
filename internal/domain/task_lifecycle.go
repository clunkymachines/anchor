package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	TaskStatusQueued     = "queued"
	TaskStatusPending    = "pending"
	TaskStatusInProgress = "in_progress"
	TaskStatusSuccess    = "success"
	TaskStatusFailure    = "failure"
	TaskStatusExpired    = "expired"
	TaskStatusCanceled   = "canceled"

	CampaignStatusRunning  = "running"
	CampaignStatusFinished = "finished"
	CampaignStatusCanceled = "canceled"

	DefaultTaskTTLDays = 7
	SecondsPerDay      = 24 * 60 * 60
)

func IsTaskActiveStatus(status string) bool {
	return status == TaskStatusPending || status == TaskStatusInProgress
}

func IsTaskNonTerminalStatus(status string) bool {
	return status == TaskStatusQueued || status == TaskStatusPending || status == TaskStatusInProgress
}

func IsTaskTerminalStatus(status string) bool {
	switch status {
	case TaskStatusSuccess, TaskStatusFailure, TaskStatusExpired, TaskStatusCanceled:
		return true
	default:
		return false
	}
}

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

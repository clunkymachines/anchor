package mqtt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"anchor/internal/domain"

	"github.com/eclipse/paho.golang/paho"
	"github.com/fxamacker/cbor/v2"
)

// PublishDeviceTask publishes one device task to the device's MQTT task topic.
func (c *Client) PublishDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) error {
	publish, err := deviceTaskPublish(organisationID, task, c.config.FOTADownloadBaseURL, c.config.QoS)
	if err != nil {
		return err
	}

	c.mu.RLock()
	manager := c.mqtt
	c.mu.RUnlock()
	if manager == nil {
		return errors.New("mqtt publisher is not connected")
	}

	_, err = manager.Publish(ctx, publish)
	return err
}

// PublishPendingDeviceTasks republishes pending tasks for a device that is ready to receive them.
func (c *Client) PublishPendingDeviceTasks(ctx context.Context, deviceID string, organisationID int64) error {
	tasks, err := c.store.ListPendingDeviceTasks(ctx, deviceID, organisationID)
	if err != nil {
		return err
	}

	for _, task := range tasks {
		if err := c.PublishDeviceTask(ctx, task, organisationID); err != nil {
			return err
		}
	}

	return nil
}

// deviceTaskPublish builds the MQTT publish packet for an Anchor device task.
func deviceTaskPublish(organisationID int64, task domain.DeviceTask, fotaDownloadBaseURL string, qos byte) (*paho.Publish, error) {
	parameters, err := taskParametersForMQTT(organisationID, task, fotaDownloadBaseURL)
	if err != nil {
		return nil, err
	}
	payload, err := cbor.Marshal(map[string]any{
		"task":       task.ID,
		"type":       task.Type,
		"parameters": parameters,
		"status":     task.Status,
		"created_at": task.CreatedAt,
	})
	if err != nil {
		return nil, err
	}

	return &paho.Publish{
		Topic:   taskTopic(organisationID, task.DeviceID),
		QoS:     qos,
		Payload: payload,
		Properties: &paho.PublishProperties{
			ContentType: "application/cbor",
		},
	}, nil
}

func taskParametersForMQTT(organisationID int64, task domain.DeviceTask, fotaDownloadBaseURL string) (any, error) {
	switch task.Type {
	case domain.TaskTypeRead, domain.TaskTypeWrite:
		return decodeTaskParametersJSON(task.ParametersJSON)
	case domain.TaskTypeFOTA:
		params, err := domain.ParseFOTATaskParameters(task.ParametersJSON)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"url": fotaDownloadURL(params.ReleaseID, organisationID, fotaDownloadBaseURL),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported task type %q", task.Type)
	}
}

func decodeTaskParametersJSON(parametersJSON string) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(parametersJSON)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return normalizeJSONNumbers(value), nil
}

func normalizeJSONNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized[key] = normalizeJSONNumbers(child)
		}
		return normalized
	case []any:
		normalized := make([]any, 0, len(typed))
		for _, child := range typed {
			normalized = append(normalized, normalizeJSONNumbers(child))
		}
		return normalized
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		floating, _ := typed.Float64()
		return floating
	default:
		return value
	}
}

func fotaDownloadURL(releaseID int64, organisationID int64, baseURL string) string {
	path := "/org/" + strconv.FormatInt(organisationID, 10) + "/releases/" + strconv.FormatInt(releaseID, 10) + "/binary"
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return path
	}
	return baseURL + path
}

// taskTopic formats the per-device topic used for Anchor-to-device task messages.
func taskTopic(organisationID int64, deviceID string) string {
	return fmt.Sprintf("dev/%d/%s/task", organisationID, deviceID)
}

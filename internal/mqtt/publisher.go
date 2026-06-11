package mqtt

import (
	"context"
	"errors"
	"fmt"

	"anchor/internal/domain"

	"github.com/eclipse/paho.golang/paho"
	"github.com/fxamacker/cbor/v2"
)

// PublishDeviceTask publishes one device task to the device's MQTT task topic.
func (c *Client) PublishDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) error {
	publish, err := deviceTaskPublish(organisationID, task, c.config.QoS)
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
func deviceTaskPublish(organisationID int64, task domain.DeviceTask, qos byte) (*paho.Publish, error) {
	payload, err := cbor.Marshal(map[string]any{
		"task":       task.ID,
		"type":       task.Type,
		"parameter":  task.Parameter,
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

// taskTopic formats the per-device topic used for Anchor-to-device task messages.
func taskTopic(organisationID int64, deviceID string) string {
	return fmt.Sprintf("dev/%d/%s/task", organisationID, deviceID)
}

package mqtt

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

// DataTopicFilter is the MQTT subscription filter used to receive device telemetry.
const DataTopicFilter = "dev/+/+/data"

// Config contains the broker connection settings for Anchor's internal MQTT client.
type Config struct {
	BrokerURL           string
	ClientID            string
	Username            string
	Password            string
	QoS                 byte
	FOTADownloadBaseURL string
	OnConnected         func()
	OnDisconnected      func()
	OnConnectError      func(error)
}

// Client owns Anchor's internal MQTT connection.
//
// It subscribes to device data topics for ingestion and publishes device task
// messages on the same broker connection.
type Client struct {
	store  *db.Store
	config Config
	logger *slog.Logger
	mu     sync.RWMutex
	mqtt   *autopaho.ConnectionManager
}

// NewClient builds an internal MQTT client with default identity values.
func NewClient(store *db.Store, config Config, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	if config.ClientID == "" {
		config.ClientID = "anchor-ingest"
	}
	if config.Username == "" {
		config.Username = config.ClientID
	}

	return &Client{
		store:  store,
		config: config,
		logger: logger,
	}
}

// Start connects to the broker and subscribes to device telemetry after each connection.
func (c *Client) Start(ctx context.Context) (*autopaho.ConnectionManager, error) {
	brokerURL, err := url.Parse(c.config.BrokerURL)
	if err != nil {
		return nil, err
	}

	manager, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{brokerURL},
		KeepAlive:                     30,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         0,
		ConnectTimeout:                10 * time.Second,
		ReconnectBackoff:              mqttReconnectBackoff(),
		ConnectUsername:               c.config.Username,
		ConnectPassword:               []byte(c.config.Password),
		OnConnectionUp: func(manager *autopaho.ConnectionManager, _ *paho.Connack) {
			c.logger.Info("mqtt client connected", "broker", c.config.BrokerURL, "client_id", c.config.ClientID)
			if c.config.OnConnected != nil {
				c.config.OnConnected()
			}
			go c.subscribe(ctx, manager)
		},
		OnConnectError: func(err error) {
			c.logger.Warn("mqtt client connect failed", "err", err)
			if c.config.OnConnectError != nil {
				c.config.OnConnectError(err)
			}
		},
		OnConnectionDown: func() bool {
			c.logger.Warn("mqtt client connection down")
			if c.config.OnDisconnected != nil {
				c.config.OnDisconnected()
			}
			return true
		},
		ClientConfig: paho.ClientConfig{
			ClientID: c.config.ClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(received paho.PublishReceived) (bool, error) {
					if received.Packet != nil {
						c.handlePublish(ctx, received.Packet)
					}
					return true, nil
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.mqtt = manager
	c.mu.Unlock()

	return manager, nil
}

// subscribe registers the telemetry topic filter on an active broker connection.
func (c *Client) subscribe(ctx context.Context, manager *autopaho.ConnectionManager) {
	if _, err := manager.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{
			{
				Topic: DataTopicFilter,
				QoS:   c.config.QoS,
			},
		},
	}); err != nil {
		c.logger.Error("mqtt client subscribe failed", "topic", DataTopicFilter, "err", err)
		return
	}

	c.logger.Info("mqtt client subscribed", "topic", DataTopicFilter)
}

// handlePublish records a device data publish and materializes decoded properties.
func (c *Client) handlePublish(ctx context.Context, publish *paho.Publish) {
	organisationID, deviceID, ok := parseDataTopic(publish.Topic)
	if !ok {
		c.logger.Warn("mqtt client ignored unexpected topic", "topic", publish.Topic)
		return
	}

	exists, err := c.store.DeviceExistsInOrganisation(ctx, deviceID, organisationID)
	if err != nil {
		c.logger.Error("mqtt client device lookup failed", "device_id", deviceID, "organisation_id", organisationID, "err", err)
		return
	}
	if !exists {
		c.logger.Warn("mqtt client ignored unknown device", "device_id", deviceID, "organisation_id", organisationID)
		return
	}

	nowMS := time.Now().UnixMilli()
	contentType := ""
	correlationID := ""
	if publish.Properties != nil {
		contentType = publish.Properties.ContentType
		if len(publish.Properties.CorrelationData) > 0 {
			correlationID = hex.EncodeToString(publish.Properties.CorrelationData)
		}
	}

	event := domain.DeviceEvent{
		DeviceID:      deviceID,
		TSReceivedMS:  nowMS,
		Protocol:      "mqtt",
		Direction:     "inbound",
		Operation:     "publish",
		Topic:         publish.Topic,
		ContentFormat: contentType,
		PayloadRaw:    publish.Payload,
		CorrelationID: correlationID,
		Source:        "broker",
		Retained:      publish.Retain,
	}

	var properties []domain.DeviceTwinProperty
	decoded, err := decodePayload(publish.Payload, contentType)
	if err != nil {
		c.logger.Warn("mqtt client payload decode failed", "topic", publish.Topic, "content_type", contentType, "err", err)
	} else {
		event.PayloadJSON = decoded.payloadJSON
		if event.ContentFormat == "" {
			event.ContentFormat = decoded.format
		}
		if !c.updateTaskStatus(ctx, deviceID, organisationID, decoded.value) {
			updates, err := flattenPayload(decoded.value)
			if err != nil {
				c.logger.Warn("mqtt client payload flatten failed", "topic", publish.Topic, "err", err)
			} else {
				properties = make([]domain.DeviceTwinProperty, 0, len(updates))
				for _, update := range updates {
					properties = append(properties, domain.DeviceTwinProperty{
						DeviceID:     deviceID,
						Path:         update.path,
						ValueJSON:    update.valueJSON,
						ValueType:    update.valueType,
						TSObservedMS: nowMS,
						TSReceivedMS: nowMS,
						Protocol:     "mqtt",
						SourcePath:   publish.Topic,
					})
				}
			}
		}
	}

	if _, err := c.store.RecordDeviceEvent(ctx, event, properties); err != nil {
		c.logger.Error("mqtt client record failed", "topic", publish.Topic, "device_id", deviceID, "err", err)
	}
}

// updateTaskStatus applies device-reported task progress from a decoded payload.
func (c *Client) updateTaskStatus(ctx context.Context, deviceID string, organisationID int64, value any) bool {
	update, ok := taskStatusFromPayload(value)
	if !ok {
		return false
	}

	completedAt := ""
	if update.status == db.DeviceTaskStatusSuccess || update.status == db.DeviceTaskStatusFailure {
		completedAt = time.Now().UTC().Format(time.RFC3339)
	}

	if err := c.store.UpdateDeviceTaskStatus(ctx, update.taskID, deviceID, organisationID, update.status, completedAt, update.message); err != nil {
		c.logger.Warn("mqtt client task status update failed", "device_id", deviceID, "organisation_id", organisationID, "task", update.taskID, "status", update.status, "err", err)
		return true
	}
	if update.status == db.DeviceTaskStatusSuccess || update.status == db.DeviceTaskStatusFailure {
		c.processTaskQueue(ctx)
	}

	if update.message != "" {
		c.logger.Info("mqtt client task status updated", "device_id", deviceID, "organisation_id", organisationID, "task", update.taskID, "status", update.status, "msg", update.message)
	}
	return true
}

func (c *Client) processTaskQueue(ctx context.Context) {
	now := time.Now().UTC()
	if _, err := c.store.ExpireOverdueDeviceTasks(ctx, now); err != nil {
		c.logger.Warn("mqtt client task expiry failed", "err", err)
	}
	promoted, err := c.store.PromoteQueuedDeviceTasks(ctx, now)
	if err != nil {
		c.logger.Warn("mqtt client task promotion failed", "err", err)
		return
	}
	for _, task := range promoted {
		if err := c.PublishDeviceTask(ctx, task.Task, task.OrganisationID); err != nil {
			c.logger.Warn("mqtt client promoted task publish failed", "device_id", task.Task.DeviceID, "organisation_id", task.OrganisationID, "task", task.Task.ID, "err", err)
		}
	}
	if _, err := c.store.FinalizeFinishedCampaigns(ctx, now); err != nil {
		c.logger.Warn("mqtt client campaign finalization failed", "err", err)
	}
}

type taskStatusUpdate struct {
	taskID  int64
	status  string
	message string
}

// taskStatusFromPayload extracts a task status update from a decoded device payload.
func taskStatusFromPayload(value any) (taskStatusUpdate, bool) {
	object, ok := normalizeDecodedValue(value).(map[string]any)
	if !ok {
		return taskStatusUpdate{}, false
	}

	taskID, ok := parseTaskID(object["task"])
	if !ok {
		return taskStatusUpdate{}, false
	}

	status, ok := object["status"].(string)
	if !ok || !validTaskStatus(status) {
		return taskStatusUpdate{}, false
	}

	update := taskStatusUpdate{
		taskID: taskID,
		status: status,
	}
	if msg, ok := object["msg"].(string); ok {
		update.message = msg
	}

	return update, true
}

// parseTaskID converts supported payload scalar values into a positive task ID.
func parseTaskID(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, typed > 0
	case int:
		return int64(typed), typed > 0
	case float64:
		if typed <= 0 || typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil && parsed > 0
	default:
		return 0, false
	}
}

// validTaskStatus reports whether a device may set the supplied task status.
func validTaskStatus(status string) bool {
	switch status {
	case db.DeviceTaskStatusInProgress, db.DeviceTaskStatusSuccess, db.DeviceTaskStatusFailure:
		return true
	default:
		return false
	}
}

// parseDataTopic extracts organisation and device identifiers from a data topic.
func parseDataTopic(topic string) (int64, string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 || parts[0] != "dev" || parts[3] != "data" {
		return 0, "", false
	}

	organisationID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || organisationID <= 0 || parts[2] == "" {
		return 0, "", false
	}

	return organisationID, parts[2], true
}

// mqttReconnectBackoff returns the reconnect policy shared by the internal client.
func mqttReconnectBackoff() autopaho.Backoff {
	return autopaho.NewExponentialBackoff(time.Second, 30*time.Second, 2*time.Second, 2)
}

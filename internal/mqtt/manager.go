package mqtt

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"

	"github.com/eclipse/paho.golang/autopaho"
)

// Manager owns the currently configured MQTT client and applies configuration changes at runtime.
type Manager struct {
	store               *db.Store
	logger              *slog.Logger
	fotaDownloadBaseURL string

	applyMu    sync.Mutex
	mu         sync.RWMutex
	config     domain.MQTTIntegrationConfig
	status     domain.MQTTIntegrationStatus
	generation uint64
	client     *Client
	connection *autopaho.ConnectionManager
	cancel     context.CancelFunc
}

func NewManager(store *db.Store, fotaDownloadBaseURL string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		store:               store,
		fotaDownloadBaseURL: fotaDownloadBaseURL,
		logger:              logger,
		status: domain.MQTTIntegrationStatus{
			State:     domain.MQTTConnectionDisabled,
			Reason:    "The integration is inactive.",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
}

// Start loads and applies the persisted MQTT integration configuration.
func (m *Manager) Start(ctx context.Context) error {
	config, err := m.store.MQTTIntegration(ctx)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return m.ApplyMQTTIntegration(ctx, config)
}

// ApplyMQTTIntegration switches the active MQTT connection to config.
func (m *Manager) ApplyMQTTIntegration(ctx context.Context, config domain.MQTTIntegrationConfig) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	m.generation++
	generation := m.generation
	m.config = config
	if config.Enabled {
		m.status = mqttIntegrationStatus(domain.MQTTConnectionConnecting, "Waiting for the broker connection.")
	} else {
		m.status = mqttIntegrationStatus(domain.MQTTConnectionDisabled, "The integration is inactive.")
	}
	m.mu.Unlock()

	m.stopCurrent()
	if !config.Enabled {
		m.logger.Info("mqtt integration disabled")
		return nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	client := NewClient(m.store, Config{
		BrokerURL:           config.BrokerURL,
		ClientID:            config.ClientID,
		Username:            config.Username,
		Password:            config.Password,
		QoS:                 config.QoS,
		FOTADownloadBaseURL: m.fotaDownloadBaseURL,
		OnConnected: func() {
			m.setStatus(generation, domain.MQTTConnectionConnected, "Broker connection established.")
		},
		OnDisconnected: func() {
			m.setStatus(generation, domain.MQTTConnectionDisconnected, "The broker connection was lost. Anchor is reconnecting.")
		},
		OnConnectError: func(err error) {
			m.setStatus(generation, domain.MQTTConnectionFailed, err.Error())
		},
	}, m.logger)
	connection, err := client.Start(runCtx)
	if err != nil {
		cancel()
		m.setStatus(generation, domain.MQTTConnectionFailed, err.Error())
		return err
	}

	m.mu.Lock()
	m.client = client
	m.connection = connection
	m.cancel = cancel
	m.mu.Unlock()
	m.logger.Info("mqtt integration started", "broker", config.BrokerURL, "topic", DataTopicFilter)
	return nil
}

// InternalMQTTCredentials returns the enabled internal client's broker credentials.
func (m *Manager) InternalMQTTCredentials() (string, string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Username, m.config.Password, m.config.Enabled
}

// MQTTIntegrationStatus returns the latest connection state reported by the MQTT client.
func (m *Manager) MQTTIntegrationStatus() domain.MQTTIntegrationStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// PublishDeviceTask publishes through the active client and is a no-op while disabled.
func (m *Manager) PublishDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		return nil
	}
	return client.PublishDeviceTask(ctx, task, organisationID)
}

// PublishPendingDeviceTasks republishes through the active client and is a no-op while disabled.
func (m *Manager) PublishPendingDeviceTasks(ctx context.Context, deviceID string, organisationID int64) error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		return nil
	}
	return client.PublishPendingDeviceTasks(ctx, deviceID, organisationID)
}

// Close disconnects the current MQTT client.
func (m *Manager) Close() {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.stopCurrent()
}

func (m *Manager) stopCurrent() {
	m.mu.Lock()
	connection := m.connection
	cancel := m.cancel
	m.client = nil
	m.connection = nil
	m.cancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if connection != nil {
		ctx, cancelDisconnect := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelDisconnect()
		_ = connection.Disconnect(ctx)
	}
}

func (m *Manager) setStatus(generation uint64, state string, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.generation {
		return
	}
	m.status = mqttIntegrationStatus(state, reason)
}

func mqttIntegrationStatus(state string, reason string) domain.MQTTIntegrationStatus {
	return domain.MQTTIntegrationStatus{
		State:     state,
		Reason:    reason,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

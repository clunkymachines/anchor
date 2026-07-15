package sim

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"anchor/internal/domain"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/fxamacker/cbor/v2"
)

const (
	// DefaultFleetSize is the number of devices generated when FleetSize is zero.
	DefaultFleetSize = 1000
	// DefaultTelemetryInterval is the publish interval used when none is configured.
	DefaultTelemetryInterval = 60 * time.Second
	// DefaultDevicePrefix prefixes generated device IDs.
	DefaultDevicePrefix = "sim-"
	// DefaultUsernamePrefix prefixes generated MQTT usernames.
	DefaultUsernamePrefix = "sim-"
	// DefaultFirmwareVersion is reported by generated devices.
	DefaultFirmwareVersion = "sim-1.0.0"
	// DefaultLogInterval controls periodic simulator metric logging.
	DefaultLogInterval = 30 * time.Second
	// DefaultProvisionTimeout bounds fleet provisioning requests.
	DefaultProvisionTimeout   = 10 * time.Minute
	defaultConnectTimeout     = 15 * time.Second
	defaultConnectConcurrency = 25
)

// Config describes the Anchor API, MQTT broker, fleet identity, and pacing used
// by a simulator runtime. NormalizeConfig fills optional zero values and
// Validate checks required connection and fleet settings.
type Config struct {
	AnchorBaseURL      string
	APIToken           string
	MQTTBrokerURL      string
	DeviceModelID      int64
	FleetSize          int
	DevicePrefix       string
	StartIndex         int
	MQTTUsernamePrefix string
	Secret             string
	FirmwareVersion    string
	TelemetryInterval  time.Duration
	QoS                byte
	ConnectConcurrency int
	LogInterval        time.Duration
	ProvisionTimeout   time.Duration
	HTTPClient         *http.Client
	Logger             *slog.Logger
}

// DeviceDefinition contains the stable identity, credentials, and MQTT topics
// assigned to one simulated device.
type DeviceDefinition struct {
	ID             string
	MQTTUsername   string
	MQTTPassword   string
	Firmware       string
	DeviceModelID  int64
	DataTopic      string
	TaskTopic      string
	OrganisationID int64
}

// Metrics contains concurrency-safe counters accumulated by a running fleet.
type Metrics struct {
	Provisioned         atomic.Int64
	Connected           atomic.Int64
	PublishAttempts     atomic.Int64
	PublishSuccesses    atomic.Int64
	PublishFailures     atomic.Int64
	TaskMessages        atomic.Int64
	TaskSuccesses       atomic.Int64
	TaskFailures        atomic.Int64
	PublishLatencyNanos atomic.Int64
}

type provisioningRequest struct {
	Devices []provisioningDeviceRequest `json:"devices"`
}

type provisioningDeviceRequest struct {
	ID               string                  `json:"id"`
	DeviceModelID    int64                   `json:"device_model_id"`
	MQTTUsername     string                  `json:"mqtt_username"`
	MQTTPassword     string                  `json:"mqtt_password"`
	SoftwareVersions domain.SoftwareVersions `json:"software_versions"`
	IsGateway        bool                    `json:"is_gateway"`
}

type provisioningResponse struct {
	Results []provisioningResult `json:"results"`
}

type provisioningResult struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	MQTTUsername string `json:"mqtt_username"`
	DataTopic    string `json:"data_topic"`
	TaskTopic    string `json:"task_topic"`
	Error        *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Runtime owns a normalized simulated fleet and its live MQTT connections.
type Runtime struct {
	cfg     Config
	devices []*deviceRuntime
	metrics Metrics
}

type deviceRuntime struct {
	def      DeviceDefinition
	cfg      Config
	manager  *autopaho.ConnectionManager
	stateMu  sync.Mutex
	state    map[string]any
	started  time.Time
	sequence uint64
	metrics  *Metrics
}

type taskEnvelope struct {
	Task       int64          `cbor:"task"`
	Type       string         `cbor:"type"`
	Parameters map[string]any `cbor:"parameters"`
}

// NormalizeConfig returns cfg with defaults applied to optional zero-valued
// settings. Explicit non-zero values are preserved.
func NormalizeConfig(cfg Config) Config {
	if cfg.FleetSize == 0 {
		cfg.FleetSize = DefaultFleetSize
	}
	if cfg.DevicePrefix == "" {
		cfg.DevicePrefix = DefaultDevicePrefix
	}
	if cfg.MQTTUsernamePrefix == "" {
		cfg.MQTTUsernamePrefix = DefaultUsernamePrefix
	}
	if cfg.FirmwareVersion == "" {
		cfg.FirmwareVersion = DefaultFirmwareVersion
	}
	if cfg.TelemetryInterval == 0 {
		cfg.TelemetryInterval = DefaultTelemetryInterval
	}
	if cfg.ConnectConcurrency == 0 {
		cfg.ConnectConcurrency = defaultConnectConcurrency
	}
	if cfg.LogInterval == 0 {
		cfg.LogInterval = DefaultLogInterval
	}
	if cfg.ProvisionTimeout == 0 {
		cfg.ProvisionTimeout = DefaultProvisionTimeout
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.ProvisionTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return cfg
}

// Validate reports whether cfg contains the required endpoints, credentials,
// model, and supported fleet sizing values.
func (cfg Config) Validate() error {
	if strings.TrimSpace(cfg.AnchorBaseURL) == "" {
		return errors.New("anchor base URL is required")
	}
	if strings.TrimSpace(cfg.APIToken) == "" {
		return errors.New("API token is required")
	}
	if strings.TrimSpace(cfg.MQTTBrokerURL) == "" {
		return errors.New("MQTT broker URL is required")
	}
	if cfg.DeviceModelID <= 0 {
		return errors.New("device model ID is required")
	}
	if cfg.FleetSize <= 0 {
		return errors.New("fleet size must be positive")
	}
	if cfg.FleetSize > 2000 {
		return errors.New("fleet size cannot exceed 2000")
	}
	if cfg.Secret == "" {
		return errors.New("simulator secret is required")
	}
	if cfg.ConnectConcurrency <= 0 {
		return errors.New("connect concurrency must be positive")
	}
	return nil
}

// GenerateFleet deterministically derives cfg.FleetSize device identities and
// credentials after applying configuration defaults.
func GenerateFleet(cfg Config) []DeviceDefinition {
	cfg = NormalizeConfig(cfg)
	devices := make([]DeviceDefinition, 0, cfg.FleetSize)
	for i := 0; i < cfg.FleetSize; i++ {
		index := cfg.StartIndex + i
		id := cfg.DevicePrefix + strconv.Itoa(index)
		devices = append(devices, DeviceDefinition{
			ID:            id,
			MQTTUsername:  cfg.MQTTUsernamePrefix + strconv.Itoa(index),
			MQTTPassword:  DeterministicPassword(cfg.Secret, id),
			Firmware:      cfg.FirmwareVersion,
			DeviceModelID: cfg.DeviceModelID,
		})
	}
	return devices
}

// DeterministicPassword derives a stable per-device password using HMAC-SHA256.
func DeterministicPassword(secret string, deviceID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(deviceID))
	return hex.EncodeToString(mac.Sum(nil))
}

// NewRuntime normalizes and validates cfg, then constructs a stopped simulated
// fleet. Call Run to provision and connect its devices.
func NewRuntime(cfg Config) (*Runtime, error) {
	cfg = NormalizeConfig(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	defs := GenerateFleet(cfg)
	runtime := &Runtime{
		cfg:     cfg,
		devices: make([]*deviceRuntime, 0, len(defs)),
	}
	for _, def := range defs {
		runtime.devices = append(runtime.devices, &deviceRuntime{
			def:     def,
			cfg:     cfg,
			state:   defaultWritableState(),
			started: time.Now(),
			metrics: &runtime.metrics,
		})
	}
	return runtime, nil
}

// Run provisions the fleet, connects its devices to MQTT, and blocks until ctx
// is canceled. It disconnects all devices before returning ctx.Err.
func (r *Runtime) Run(ctx context.Context) error {
	if err := r.provision(ctx); err != nil {
		return err
	}
	r.startMetricsLogger(ctx)
	if err := r.connectAll(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	r.disconnectAll()
	return ctx.Err()
}

func (r *Runtime) provision(ctx context.Context) error {
	req := provisioningRequest{Devices: make([]provisioningDeviceRequest, 0, len(r.devices))}
	for _, device := range r.devices {
		req.Devices = append(req.Devices, provisioningDeviceRequest{
			ID:            device.def.ID,
			DeviceModelID: device.def.DeviceModelID,
			MQTTUsername:  device.def.MQTTUsername,
			MQTTPassword:  device.def.MQTTPassword,
			SoftwareVersions: domain.SoftwareVersions{
				"firmware": device.def.Firmware,
			},
		})
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	endpoint := strings.TrimRight(r.cfg.AnchorBaseURL, "/") + "/api/v1/devices/bulk-upsert"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+r.cfg.APIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus {
		return fmt.Errorf("provisioning failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var decoded provisioningResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return err
	}
	byID := make(map[string]provisioningResult, len(decoded.Results))
	for _, result := range decoded.Results {
		byID[result.ID] = result
	}
	kept := r.devices[:0]
	for _, device := range r.devices {
		result, ok := byID[device.def.ID]
		if !ok || result.Error != nil {
			if ok && result.Error != nil {
				r.cfg.Logger.Warn("device provisioning failed", "device_id", device.def.ID, "code", result.Error.Code, "message", result.Error.Message)
			} else {
				r.cfg.Logger.Warn("device provisioning result missing", "device_id", device.def.ID)
			}
			continue
		}
		device.def.DataTopic = result.DataTopic
		device.def.TaskTopic = result.TaskTopic
		device.def.MQTTUsername = result.MQTTUsername
		device.def.OrganisationID = organisationIDFromDataTopic(result.DataTopic)
		kept = append(kept, device)
	}
	r.devices = kept
	r.metrics.Provisioned.Store(int64(len(r.devices)))
	if len(r.devices) == 0 {
		return errors.New("no devices provisioned successfully")
	}
	r.cfg.Logger.Info("fleet provisioned", "devices", len(r.devices))
	return nil
}

func (r *Runtime) connectAll(ctx context.Context) error {
	sem := make(chan struct{}, r.cfg.ConnectConcurrency)
	errCh := make(chan error, len(r.devices))
	var wg sync.WaitGroup
	for _, device := range r.devices {
		device := device
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := device.connect(ctx); err != nil {
				errCh <- err
				return
			}
			r.metrics.Connected.Add(1)
			go device.telemetryLoop(ctx)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	r.cfg.Logger.Info("fleet connected", "devices", len(r.devices))
	return nil
}

func (r *Runtime) disconnectAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, device := range r.devices {
		if device.manager != nil {
			_ = device.manager.Disconnect(ctx)
		}
	}
}

func (r *Runtime) startMetricsLogger(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(r.cfg.LogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				attempts := r.metrics.PublishAttempts.Load()
				avgLatency := int64(0)
				if attempts > 0 {
					avgLatency = r.metrics.PublishLatencyNanos.Load() / attempts
				}
				r.cfg.Logger.Info("fleet metrics",
					"provisioned", r.metrics.Provisioned.Load(),
					"connected", r.metrics.Connected.Load(),
					"publish_attempts", attempts,
					"publish_successes", r.metrics.PublishSuccesses.Load(),
					"publish_failures", r.metrics.PublishFailures.Load(),
					"task_messages", r.metrics.TaskMessages.Load(),
					"task_successes", r.metrics.TaskSuccesses.Load(),
					"task_failures", r.metrics.TaskFailures.Load(),
					"avg_publish_latency_ms", time.Duration(avgLatency).Milliseconds(),
				)
			}
		}
	}()
}

func (d *deviceRuntime) connect(ctx context.Context) error {
	brokerURL, err := url.Parse(d.cfg.MQTTBrokerURL)
	if err != nil {
		return err
	}
	subscribed := make(chan error, 1)
	manager, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{brokerURL},
		KeepAlive:                     30,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         0,
		ConnectTimeout:                defaultConnectTimeout,
		ReconnectBackoff:              func(int) time.Duration { return time.Second },
		ConnectUsername:               d.def.MQTTUsername,
		ConnectPassword:               []byte(d.def.MQTTPassword),
		OnConnectionUp: func(manager *autopaho.ConnectionManager, _ *paho.Connack) {
			_, err := manager.Subscribe(ctx, &paho.Subscribe{
				Subscriptions: []paho.SubscribeOptions{{Topic: d.def.TaskTopic, QoS: d.cfg.QoS}},
			})
			select {
			case subscribed <- err:
			default:
			}
		},
		OnConnectError: func(err error) {
			d.cfg.Logger.Warn("sim device mqtt connect failed", "device_id", d.def.ID, "err", err)
		},
		OnConnectionDown: func() bool { return true },
		ClientConfig: paho.ClientConfig{
			ClientID: "fleet-sim-" + d.def.ID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(received paho.PublishReceived) (bool, error) {
					if received.Packet != nil {
						d.metrics.TaskMessages.Add(1)
						go d.handleTask(ctx, received.Packet.Payload)
					}
					return true, nil
				},
			},
		},
	})
	if err != nil {
		return err
	}
	d.manager = manager
	connectCtx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()
	if err := manager.AwaitConnection(connectCtx); err != nil {
		return err
	}
	select {
	case err := <-subscribed:
		return err
	case <-connectCtx.Done():
		return connectCtx.Err()
	}
}

func (d *deviceRuntime) telemetryLoop(ctx context.Context) {
	jitter := deterministicJitter(d.def.ID, d.cfg.TelemetryInterval)
	timer := time.NewTimer(jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	ticker := time.NewTicker(d.cfg.TelemetryInterval)
	defer ticker.Stop()
	for {
		d.publishTelemetry(ctx, d.telemetryPayload())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *deviceRuntime) telemetryPayload() map[string]any {
	sequence := atomic.AddUint64(&d.sequence, 1)
	rnd := rand.New(rand.NewSource(int64(stableHash(d.def.ID)) + int64(sequence)))
	payload := map[string]any{
		"firmware":       d.def.Firmware,
		"battery":        map[string]any{"percent": 40 + rnd.Intn(60)},
		"signal":         map[string]any{"rssi": -95 + rnd.Intn(45)},
		"uptime_seconds": int64(time.Since(d.started).Seconds()),
		"sim":            map[string]any{"sequence": int64(sequence), "profile": "default"},
	}
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	for path, value := range d.state {
		setNested(payload, path, value)
	}
	return payload
}

func (d *deviceRuntime) publishTelemetry(ctx context.Context, payload map[string]any) {
	d.metrics.PublishAttempts.Add(1)
	start := time.Now()
	encoded, err := cbor.Marshal(payload)
	if err != nil {
		d.metrics.PublishFailures.Add(1)
		return
	}
	_, err = d.manager.Publish(ctx, &paho.Publish{
		Topic:   d.def.DataTopic,
		QoS:     d.cfg.QoS,
		Payload: encoded,
		Properties: &paho.PublishProperties{
			ContentType: "application/cbor",
		},
	})
	d.metrics.PublishLatencyNanos.Add(time.Since(start).Nanoseconds())
	if err != nil {
		d.metrics.PublishFailures.Add(1)
		return
	}
	d.metrics.PublishSuccesses.Add(1)
}

func (d *deviceRuntime) handleTask(ctx context.Context, payload []byte) {
	var task taskEnvelope
	if err := cbor.Unmarshal(payload, &task); err != nil || task.Task <= 0 {
		return
	}
	_ = d.publishTaskStatus(ctx, task.Task, "in_progress", "task received")
	var err error
	switch task.Type {
	case domain.TaskTypeRead:
		err = d.handleReadTask(ctx, task)
	case domain.TaskTypeWrite:
		err = d.handleWriteTask(ctx, task)
	case domain.TaskTypeFOTA:
		err = d.handleFOTATask(ctx, task)
	default:
		err = fmt.Errorf("unsupported task type %q", task.Type)
	}
	if err != nil {
		d.metrics.TaskFailures.Add(1)
		_ = d.publishTaskStatus(ctx, task.Task, "failure", err.Error())
		return
	}
	d.metrics.TaskSuccesses.Add(1)
	_ = d.publishTaskStatus(ctx, task.Task, "success", "")
}

func (d *deviceRuntime) handleReadTask(ctx context.Context, task taskEnvelope) error {
	paths, err := stringList(task.Parameters["paths"])
	if err != nil {
		return err
	}
	values := make(map[string]any)
	for _, path := range paths {
		value, ok := d.valueForPath(path)
		if !ok {
			return fmt.Errorf("unsupported read path %q", path)
		}
		setNested(values, path, value)
	}
	d.publishTelemetry(ctx, values)
	return nil
}

func (d *deviceRuntime) handleWriteTask(ctx context.Context, task taskEnvelope) error {
	values, ok := task.Parameters["values"].([]any)
	if !ok || len(values) == 0 {
		return errors.New("write task values are required")
	}
	for _, raw := range values {
		entry, ok := stringMap(raw)
		if !ok {
			return errors.New("write value entry is invalid")
		}
		path, _ := entry["path"].(string)
		if !supportedWritePath(path) {
			return fmt.Errorf("unsupported write path %q", path)
		}
		d.stateMu.Lock()
		d.state[path] = entry["value"]
		d.stateMu.Unlock()
	}
	return nil
}

func stringMap(value any) (map[string]any, bool) {
	if typed, ok := value.(map[string]any); ok {
		return typed, true
	}
	typed, ok := value.(map[interface{}]interface{})
	if !ok {
		return nil, false
	}
	out := make(map[string]any, len(typed))
	for key, value := range typed {
		keyString, ok := key.(string)
		if !ok {
			return nil, false
		}
		out[keyString] = value
	}
	return out, true
}

func (d *deviceRuntime) handleFOTATask(ctx context.Context, task taskEnvelope) error {
	rawURL, _ := task.Parameters["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("fota url is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := d.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("fota download returned status %d", resp.StatusCode)
	}
	return nil
}

func (d *deviceRuntime) publishTaskStatus(ctx context.Context, taskID int64, status string, message string) error {
	payload := map[string]any{"task": taskID, "status": status}
	if message != "" {
		payload["msg"] = message
	}
	d.publishTelemetry(ctx, payload)
	return nil
}

func (d *deviceRuntime) valueForPath(path string) (any, bool) {
	payload := d.telemetryPayload()
	if value, ok := nestedValue(payload, path); ok {
		return value, true
	}
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	value, ok := d.state[path]
	return value, ok
}

// TelemetryCBOR encodes the simulator's standard telemetry payload, overlaying
// writable values addressed by dotted property paths.
func TelemetryCBOR(device DeviceDefinition, sequence uint64, writable map[string]any) ([]byte, error) {
	payload := map[string]any{
		"firmware":       device.Firmware,
		"battery":        map[string]any{"percent": 50},
		"signal":         map[string]any{"rssi": -70},
		"uptime_seconds": int64(0),
		"sim":            map[string]any{"sequence": int64(sequence), "profile": "default"},
	}
	for path, value := range writable {
		setNested(payload, path, value)
	}
	return cbor.Marshal(payload)
}

func defaultWritableState() map[string]any {
	return map[string]any{
		"config.sample_rate_seconds": int64(60),
		"config.mode":                "normal",
		"config.enabled":             true,
	}
}

func supportedWritePath(path string) bool {
	_, ok := defaultWritableState()[path]
	return ok
}

func stringList(value any) ([]string, error) {
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 {
		return nil, errors.New("paths are required")
	}
	paths := make([]string, 0, len(raw))
	for _, item := range raw {
		path, ok := item.(string)
		if !ok || strings.TrimSpace(path) == "" {
			return nil, errors.New("path must be a string")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func setNested(root map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := root
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
			return
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[part] = next
		}
		current = next
	}
}

func nestedValue(root map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = root
	for _, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func deterministicJitter(deviceID string, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	max := interval / 2
	if max <= 0 {
		return 0
	}
	return time.Duration(stableHash(deviceID) % uint64(max))
}

func stableHash(value string) uint64 {
	sum := sha256.Sum256([]byte(value))
	return uint64(sum[0])<<56 | uint64(sum[1])<<48 | uint64(sum[2])<<40 | uint64(sum[3])<<32 |
		uint64(sum[4])<<24 | uint64(sum[5])<<16 | uint64(sum[6])<<8 | uint64(sum[7])
}

func organisationIDFromDataTopic(topic string) int64 {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 {
		return 0
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	return id
}

// SupportedWritePaths returns the writable simulator property paths in sorted order.
func SupportedWritePaths() []string {
	state := defaultWritableState()
	paths := make([]string, 0, len(state))
	for path := range state {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

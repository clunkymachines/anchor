package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"anchor/internal/sim"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := configFromFlags()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(2)
	}

	runtime, err := sim.NewRuntime(cfg)
	if err != nil {
		slog.Error("simulator setup", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runtime.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("simulator stopped", "err", err)
		os.Exit(1)
	}
}

func configFromFlags() (sim.Config, error) {
	var (
		anchorBaseURL      = flag.String("anchor-url", envOrDefault("ANCHOR_SIM_ANCHOR_URL", "http://localhost:8080"), "Anchor base URL")
		apiToken           = flag.String("api-token", os.Getenv("ANCHOR_SIM_API_TOKEN"), "organisation API bearer token")
		mqttBrokerURL      = flag.String("mqtt-url", envOrDefault("ANCHOR_SIM_MQTT_URL", "mqtt://localhost:1883"), "MQTT broker URL")
		modelID            = flag.Int64("model-id", envInt64("ANCHOR_SIM_MODEL_ID", 0), "existing device model ID")
		organisationID     = flag.Int64("organisation-id", envInt64("ANCHOR_SIM_ORGANISATION_ID", 0), "organisation ID")
		fleetSize          = flag.Int("fleet-size", envInt("ANCHOR_SIM_FLEET_SIZE", sim.DefaultFleetSize), "number of devices")
		devicePrefix       = flag.String("device-prefix", envOrDefault("ANCHOR_SIM_DEVICE_PREFIX", sim.DefaultDevicePrefix), "device ID prefix")
		startIndex         = flag.Int("start-index", envInt("ANCHOR_SIM_START_INDEX", 1), "first device index")
		usernamePrefix     = flag.String("username-prefix", envOrDefault("ANCHOR_SIM_USERNAME_PREFIX", sim.DefaultUsernamePrefix), "MQTT username prefix")
		secret             = flag.String("secret", os.Getenv("ANCHOR_SIM_SECRET"), "secret for deterministic MQTT passwords")
		firmware           = flag.String("firmware", envOrDefault("ANCHOR_SIM_FIRMWARE", sim.DefaultFirmwareVersion), "reported firmware version")
		telemetryInterval  = flag.Duration("telemetry-interval", envDuration("ANCHOR_SIM_TELEMETRY_INTERVAL", sim.DefaultTelemetryInterval), "telemetry interval")
		qos                = flag.Int("qos", envInt("ANCHOR_SIM_QOS", 0), "MQTT QoS 0, 1, or 2")
		connectConcurrency = flag.Int("connect-concurrency", envInt("ANCHOR_SIM_CONNECT_CONCURRENCY", 25), "maximum concurrent MQTT connects")
		logInterval        = flag.Duration("log-interval", envDuration("ANCHOR_SIM_LOG_INTERVAL", sim.DefaultLogInterval), "aggregate metrics log interval")
		provisionTimeout   = flag.Duration("provision-timeout", envDuration("ANCHOR_SIM_PROVISION_TIMEOUT", sim.DefaultProvisionTimeout), "bulk provisioning HTTP timeout")
		taskProfile        = flag.String("task-profile", envOrDefault("ANCHOR_SIM_TASK_PROFILE", sim.TaskProfileNormal), "task behavior profile: normal or demo-rollout")
		taskStartDelay     = flag.Duration("task-start-delay", envDuration("ANCHOR_SIM_TASK_START_DELAY", 0), "delay before reporting a task in progress")
		taskDurationMin    = flag.Duration("task-duration-min", envDuration("ANCHOR_SIM_TASK_DURATION_MIN", 0), "minimum visible task execution duration")
		taskDurationMax    = flag.Duration("task-duration-max", envDuration("ANCHOR_SIM_TASK_DURATION_MAX", 0), "maximum visible task execution duration")
	)
	flag.Parse()

	if *qos < 0 || *qos > 2 {
		return sim.Config{}, fmt.Errorf("qos must be 0, 1, or 2")
	}
	return sim.Config{
		AnchorBaseURL:      *anchorBaseURL,
		APIToken:           *apiToken,
		MQTTBrokerURL:      *mqttBrokerURL,
		DeviceModelID:      *modelID,
		OrganisationID:     *organisationID,
		FleetSize:          *fleetSize,
		DevicePrefix:       *devicePrefix,
		StartIndex:         *startIndex,
		MQTTUsernamePrefix: *usernamePrefix,
		Secret:             *secret,
		FirmwareVersion:    *firmware,
		TelemetryInterval:  *telemetryInterval,
		QoS:                byte(*qos),
		ConnectConcurrency: *connectConcurrency,
		LogInterval:        *logInterval,
		ProvisionTimeout:   *provisionTimeout,
		TaskProfile:        *taskProfile,
		TaskStartDelay:     *taskStartDelay,
		TaskDurationMin:    *taskDurationMin,
		TaskDurationMax:    *taskDurationMax,
		Logger:             slog.Default(),
	}, nil
}

func envOrDefault(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"anchor/internal/db"
	"anchor/internal/mqtt"
	"anchor/internal/web"
)

const defaultHTTPAddr = ":8080"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ctx := context.Background()

	store, err := db.Open(ctx, dbConfigFromEnv())
	if err != nil {
		slog.Error("init db", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := ensureBootstrapData(ctx, store); err != nil {
		slog.Error("bootstrap data", "err", err)
		os.Exit(1)
	}

	mqttConfig, mqttEnabled, err := mqttConfigFromEnv()
	if err != nil {
		slog.Error("mqtt config", "err", err)
		os.Exit(1)
	}
	var taskPublisher web.DeviceTaskPublisher
	if mqttEnabled {
		mqttClient := mqtt.NewClient(store, mqttConfig, slog.Default())
		mqttConn, err := mqttClient.Start(ctx)
		if err != nil {
			slog.Error("start mqtt client", "err", err)
			os.Exit(1)
		}
		taskPublisher = mqttClient
		defer mqttConn.Disconnect(context.Background())
		slog.Info("mqtt client started", "broker", mqttConfig.BrokerURL, "topic", mqtt.DataTopicFilter)
	}

	server := &http.Server{
		Addr:              envOrDefault("ANCHOR_HTTP_ADDR", defaultHTTPAddr),
		Handler:           web.NewServer(store, webConfigFromMQTT(mqttConfig, mqttEnabled, taskPublisher)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("app started", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server stopped unexpectedly", "err", err)
		os.Exit(1)
	}
}

func webConfigFromMQTT(mqttConfig mqtt.Config, mqttEnabled bool, taskPublisher web.DeviceTaskPublisher) web.ServerConfig {
	config := web.ServerConfig{
		FOTADownloadBaseURL: os.Getenv("ANCHOR_FOTA_DOWNLOAD_BASE_URL"),
	}
	if !mqttEnabled {
		return config
	}

	config.TaskPublisher = taskPublisher
	config.InternalMQTTClientAuth = web.InternalMQTTClientAuthConfig{
		Username: mqttConfig.Username,
		Password: mqttConfig.Password,
	}
	return config
}

func mqttConfigFromEnv() (mqtt.Config, bool, error) {
	brokerURL := os.Getenv("ANCHOR_MQTT_BROKER_URL")
	if brokerURL == "" {
		return mqtt.Config{}, false, nil
	}

	clientID := envOrDefault("ANCHOR_MQTT_CLIENT_ID", "anchor-ingest")
	username := envOrDefault("ANCHOR_MQTT_USERNAME", clientID)
	password := os.Getenv("ANCHOR_MQTT_PASSWORD")
	if password == "" {
		generatedPassword, err := randomMQTTPassword()
		if err != nil {
			return mqtt.Config{}, false, err
		}
		password = generatedPassword
	}

	qos := byte(0)
	if value := os.Getenv("ANCHOR_MQTT_QOS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 2 {
			return mqtt.Config{}, false, fmt.Errorf("ANCHOR_MQTT_QOS must be 0, 1, or 2")
		}
		qos = byte(parsed)
	}

	return mqtt.Config{
		BrokerURL: brokerURL,
		ClientID:  clientID,
		Username:  username,
		Password:  password,
		QoS:       qos,
	}, true, nil
}

func randomMQTTPassword() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate mqtt password: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func dbConfigFromEnv() db.Config {
	dialect := db.Dialect(envOrDefault("ANCHOR_DB_DIALECT", string(db.DialectSQLite)))
	dsn := os.Getenv("ANCHOR_DB_DSN")
	if dsn == "" && dialect == db.DialectSQLite {
		dsn = envOrDefault("ANCHOR_DB_PATH", "anchor.db")
	}

	return db.Config{
		Dialect: dialect,
		DSN:     dsn,
	}
}

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

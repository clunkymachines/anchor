package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"anchor/internal/buildinfo"
	"anchor/internal/coapcontrol"
	"anchor/internal/db"
	"anchor/internal/domain"
	"anchor/internal/mqtt"
	"anchor/internal/web"
)

const defaultHTTPAddr = ":8080"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	slog.Info("starting app", "version", buildinfo.Version)

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

	mqttManager := mqtt.NewManager(store, os.Getenv("ANCHOR_FOTA_DOWNLOAD_BASE_URL"), slog.Default())
	if err := mqttManager.Start(ctx); err != nil {
		slog.Error("start mqtt integration", "err", err)
	}
	defer mqttManager.Close()

	if err := configureCoAPIntegration(ctx, store); err != nil {
		slog.Error("configure CoAP integration", "err", err)
		os.Exit(1)
	}
	coapManager := coapcontrol.NewSwitcher(os.Getenv("ANCHOR_FOTA_DOWNLOAD_BASE_URL"))
	if coapConfig, err := store.CoAPIntegration(ctx); err == nil {
		if err = coapManager.ApplyCoAPIntegration(ctx, coapConfig); err != nil {
			slog.Error("start CoAP integration", "err", err)
			os.Exit(1)
		}
	}

	server := &http.Server{
		Addr: envOrDefault("ANCHOR_HTTP_ADDR", defaultHTTPAddr),
		Handler: web.NewServer(store, web.ServerConfig{
			FOTADownloadBaseURL:    os.Getenv("ANCHOR_FOTA_DOWNLOAD_BASE_URL"),
			CVEScanWorkerEnabled:   true,
			CVEScannerPath:         os.Getenv("ANCHOR_GRYPE_PATH"),
			MQTTIntegrationRuntime: mqttManager,
			CoAPTaskPublisher:      coapManager,
			CoAPIntegrationRuntime: coapManager,
			CoAPInvalidator:        coapManager,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("app started", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server stopped unexpectedly", "err", err)
		os.Exit(1)
	}
}

func configureCoAPIntegration(ctx context.Context, store *db.Store) error {
	enabledValue, hasEnabled := os.LookupEnv("ANCHOR_COAP_ENABLED")
	frontendURL, hasURL := os.LookupEnv("ANCHOR_COAP_FRONTEND_URL")
	token, hasToken := os.LookupEnv("COAP_INTERNAL_BEARER_TOKEN")
	if !hasEnabled && !hasURL && !hasToken {
		return nil
	}

	config, err := store.CoAPIntegration(ctx)
	if errors.Is(err, db.ErrNotFound) {
		config = domain.CoAPIntegrationConfig{}
	} else if err != nil {
		return err
	}
	if hasEnabled {
		parsed, err := strconv.ParseBool(enabledValue)
		if err != nil {
			return fmt.Errorf("ANCHOR_COAP_ENABLED must be true or false: %w", err)
		}
		config.Enabled = parsed
	}
	if hasURL {
		config.FrontendURL = frontendURL
	}
	if hasToken && token != "" {
		config.BearerToken = token
	}
	return store.SaveCoAPIntegration(ctx, config)
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

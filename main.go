package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
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

	mqttManager := mqtt.NewManager(store, os.Getenv("ANCHOR_FOTA_DOWNLOAD_BASE_URL"), slog.Default())
	if err := mqttManager.Start(ctx); err != nil {
		slog.Error("start mqtt integration", "err", err)
	}
	defer mqttManager.Close()

	server := &http.Server{
		Addr: envOrDefault("ANCHOR_HTTP_ADDR", defaultHTTPAddr),
		Handler: web.NewServer(store, web.ServerConfig{
			FOTADownloadBaseURL:    os.Getenv("ANCHOR_FOTA_DOWNLOAD_BASE_URL"),
			CVEScanWorkerEnabled:   true,
			CVEScannerPath:         os.Getenv("ANCHOR_GRYPE_PATH"),
			MQTTIntegrationRuntime: mqttManager,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("app started", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server stopped unexpectedly", "err", err)
		os.Exit(1)
	}
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

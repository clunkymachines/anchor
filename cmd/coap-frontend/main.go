package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"anchor/internal/coapfrontend"
)

func main() {
	logger := slog.Default()
	config, err := coapfrontend.LoadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid CoAP frontend configuration:\n%s\n", err)
		os.Exit(1)
	}
	if err := run(logger, config); err != nil {
		logger.Error("CoAP frontend stopped", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, config coapfrontend.Config) error {
	client, err := coapfrontend.NewHTTPAnchorClient(config.AnchorURL, config.BearerToken, &http.Client{Timeout: config.HTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	if err != nil {
		return err
	}
	runtime, err := coapfrontend.NewRuntime(config, client, logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runtime.Serve(ctx)
}

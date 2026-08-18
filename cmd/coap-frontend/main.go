package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"anchor/internal/coapfrontend"
)

func main() {
	showHelp, err := parseCommandLine(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	if showHelp {
		writeHelp(os.Stdout)
		return
	}

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

func parseCommandLine(args []string, stderr io.Writer) (bool, error) {
	flags := flag.NewFlagSet("coap-frontend", flag.ContinueOnError)
	flags.SetOutput(stderr)
	help := flags.Bool("help", false, "show this help and exit")
	shortHelp := flags.Bool("h", false, "show this help and exit")
	flags.Usage = func() { writeHelp(stderr) }
	if err := flags.Parse(args); err != nil {
		return false, err
	}
	if flags.NArg() != 0 {
		err := fmt.Errorf("unexpected argument %q", flags.Arg(0))
		fmt.Fprintf(stderr, "coap-frontend: %s\n", err)
		flags.Usage()
		return false, err
	}
	return *help || *shortHelp, nil
}

func writeHelp(w io.Writer) {
	fmt.Fprintf(w, `Usage: coap-frontend [options]

Runs Anchor's CoAP/DTLS frontend. Configuration is read from environment
variables.

Options:
  -h, -help, --help                     Show this help and exit.

Environment:
  COAP_UDP_LISTEN_ADDR                  DTLS/CoAP UDP address (default ":5684")
  COAP_CONTROL_LISTEN_ADDR              Private control HTTP address (default ":8081")
  ANCHOR_INTERNAL_URL                   Anchor private HTTP base URL
                                        (default "http://localhost:8080")
  COAP_INTERNAL_BEARER_TOKEN            Shared internal bearer token (required)
  COAP_HTTP_TIMEOUT                     Frontend-to-Anchor request timeout (default "10s")
  COAP_EXCHANGE_TIMEOUT                 Device CoAP exchange timeout (default "15s")
  COAP_CID_LENGTH                       Fixed CID length; 0 disables CIDs (default %d)
  COAP_IDLE_SWEEP_INTERVAL              Inactive-association sweep interval (default "1m")
  COAP_MAX_ASSOCIATIONS                 Active association limit (default 1000)
  COAP_MAX_CONCURRENT_HANDSHAKES        Concurrent handshake limit (default 128)
  COAP_MAX_BODY_BYTES                   Assembled body limit, at most 65536
                                        (default 65536)
`, coapfrontend.DefaultCIDLength)
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

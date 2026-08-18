package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseCommandLineHelp(t *testing.T) {
	for _, argument := range []string{"-h", "-help", "--help"} {
		t.Run(argument, func(t *testing.T) {
			var stderr bytes.Buffer
			showHelp, err := parseCommandLine([]string{argument}, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			if !showHelp {
				t.Fatal("help was not requested")
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %s", stderr.String())
			}
		})
	}
}

func TestWriteHelpDocumentsConfiguration(t *testing.T) {
	var output bytes.Buffer
	writeHelp(&output)

	for _, expected := range []string{
		"Usage: coap-frontend [options]",
		"-h, -help, --help",
		"COAP_UDP_LISTEN_ADDR",
		"COAP_CONTROL_LISTEN_ADDR",
		"ANCHOR_INTERNAL_URL",
		"COAP_INTERNAL_BEARER_TOKEN",
		"COAP_HTTP_TIMEOUT",
		"COAP_EXCHANGE_TIMEOUT",
		"COAP_CID_LENGTH",
		"COAP_IDLE_SWEEP_INTERVAL",
		"COAP_MAX_ASSOCIATIONS",
		"COAP_MAX_CONCURRENT_HANDSHAKES",
		"COAP_MAX_BODY_BYTES",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("help output does not contain %q", expected)
		}
	}
}

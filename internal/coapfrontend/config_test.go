package coapfrontend

import "testing"

func TestLoadConfigDefaultsControlListenerTo8081(t *testing.T) {
	values := map[string]string{
		"COAP_INTERNAL_BEARER_TOKEN": "test-secret",
	}
	config, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.ControlListenAddr != ":8081" {
		t.Fatalf("control listen address = %q, want :8081", config.ControlListenAddr)
	}
	if config.AnchorURL != "http://localhost:8080" {
		t.Fatalf("Anchor URL = %q, want http://localhost:8080", config.AnchorURL)
	}
}

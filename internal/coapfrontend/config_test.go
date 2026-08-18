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
	if config.CIDLength != DefaultCIDLength {
		t.Fatalf("CID length = %d, want %d", config.CIDLength, DefaultCIDLength)
	}
}

func TestCIDLengthAllowsOnlyFixedLengthOrDisabled(t *testing.T) {
	for _, test := range []struct {
		value   string
		wantErr bool
	}{
		{value: "0"},
		{value: "8"},
		{value: "4", wantErr: true},
		{value: "9", wantErr: true},
	} {
		t.Run(test.value, func(t *testing.T) {
			values := map[string]string{
				"COAP_INTERNAL_BEARER_TOKEN": "test-secret",
				"COAP_CID_LENGTH":            test.value,
			}
			_, err := LoadConfig(func(key string) string { return values[key] })
			if (err != nil) != test.wantErr {
				t.Fatalf("LoadConfig(COAP_CID_LENGTH=%s) error = %v, wantErr %v", test.value, err, test.wantErr)
			}
		})
	}
}

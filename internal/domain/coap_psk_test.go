package domain

import "testing"

func TestGenerateCoAPPSKUsesMinimumSupportedLength(t *testing.T) {
	t.Parallel()

	psk, err := GenerateCoAPPSK()
	if err != nil {
		t.Fatalf("generate CoAP PSK: %v", err)
	}
	if len(psk) != 16 {
		t.Fatalf("expected a 16-byte generated PSK, got %d bytes", len(psk))
	}
}

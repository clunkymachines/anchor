package domain

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
)

func GenerateCoAPPSK() ([]byte, error) {
	psk := make([]byte, 16)
	if _, err := rand.Read(psk); err != nil {
		return nil, err
	}
	return psk, nil
}

func EncodeCoAPPSK(psk []byte) string { return base64.RawURLEncoding.EncodeToString(psk) }

func DecodeCoAPPSK(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("PSK is required")
	}
	psk, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(psk) < 16 || len(psk) > 64 {
		return nil, errors.New("PSK must be unpadded Base64URL encoding of 16 to 64 bytes")
	}
	return psk, nil
}

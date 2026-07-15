// Package coapfrontend contains the non-durable CoAP/DTLS frontend runtime.
package coapfrontend

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	UDPListenAddr           string
	ControlListenAddr       string
	AnchorURL               string
	BearerToken             string
	HTTPTimeout             time.Duration
	CoAPExchangeTimeout     time.Duration
	CIDLength               int
	IdleSweepInterval       time.Duration
	MaxAssociations         int
	MaxConcurrentHandshakes int
	MaxBodyBytes            int64
}

func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := Config{UDPListenAddr: valueOr(getenv("COAP_UDP_LISTEN_ADDR"), ":5684"), ControlListenAddr: valueOr(getenv("COAP_CONTROL_LISTEN_ADDR"), ":8081"), AnchorURL: valueOr(getenv("ANCHOR_INTERNAL_URL"), "http://localhost:8080"), BearerToken: getenv("COAP_INTERNAL_BEARER_TOKEN"), HTTPTimeout: 10 * time.Second, CoAPExchangeTimeout: 15 * time.Second, CIDLength: 8, IdleSweepInterval: time.Minute, MaxAssociations: 1000, MaxConcurrentHandshakes: 128, MaxBodyBytes: 64 << 10}
	var (
		err  error
		errs []error
	)
	if c.BearerToken == "" {
		errs = append(errs, errors.New("COAP_INTERNAL_BEARER_TOKEN is required"))
	}
	if c.CIDLength, err = parseInt(getenv, "COAP_CID_LENGTH", c.CIDLength); err != nil || c.CIDLength < 0 || c.CIDLength > 32 {
		errs = append(errs, errors.New("COAP_CID_LENGTH must be between 0 and 32"))
		c.CIDLength = 8
	}
	if c.MaxAssociations, err = parseInt(getenv, "COAP_MAX_ASSOCIATIONS", c.MaxAssociations); err != nil || c.MaxAssociations <= 0 {
		errs = append(errs, errors.New("COAP_MAX_ASSOCIATIONS must be positive"))
		c.MaxAssociations = 1000
	}
	if c.MaxConcurrentHandshakes, err = parseInt(getenv, "COAP_MAX_CONCURRENT_HANDSHAKES", c.MaxConcurrentHandshakes); err != nil || c.MaxConcurrentHandshakes <= 0 {
		errs = append(errs, errors.New("COAP_MAX_CONCURRENT_HANDSHAKES must be positive"))
		c.MaxConcurrentHandshakes = 128
	}
	if c.HTTPTimeout, err = parseDuration(getenv, "COAP_HTTP_TIMEOUT", c.HTTPTimeout); err != nil || c.HTTPTimeout <= 0 {
		errs = append(errs, errors.New("COAP_HTTP_TIMEOUT must be a positive duration"))
		c.HTTPTimeout = 10 * time.Second
	}
	if c.CoAPExchangeTimeout, err = parseDuration(getenv, "COAP_EXCHANGE_TIMEOUT", c.CoAPExchangeTimeout); err != nil || c.CoAPExchangeTimeout <= 0 {
		errs = append(errs, errors.New("COAP_EXCHANGE_TIMEOUT must be a positive duration"))
		c.CoAPExchangeTimeout = 15 * time.Second
	}
	if c.IdleSweepInterval, err = parseDuration(getenv, "COAP_IDLE_SWEEP_INTERVAL", c.IdleSweepInterval); err != nil || c.IdleSweepInterval <= 0 {
		errs = append(errs, errors.New("COAP_IDLE_SWEEP_INTERVAL must be a positive duration"))
		c.IdleSweepInterval = time.Minute
	}
	if raw := getenv("COAP_MAX_BODY_BYTES"); raw != "" {
		c.MaxBodyBytes, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || c.MaxBodyBytes <= 0 || c.MaxBodyBytes > 64<<10 {
			errs = append(errs, errors.New("COAP_MAX_BODY_BYTES must be between 1 and 65536"))
			c.MaxBodyBytes = 64 << 10
		}
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}
func parseInt(getenv func(string) string, key string, fallback int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}
func parseDuration(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	return time.ParseDuration(raw)
}
func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func (c Config) Validate() error {
	if c.UDPListenAddr == "" || c.ControlListenAddr == "" || c.AnchorURL == "" || c.BearerToken == "" {
		return errors.New("frontend listen addresses, Anchor URL, and bearer token are required")
	}
	if c.CIDLength < 0 || c.CIDLength > 32 {
		return fmt.Errorf("invalid CID length %d", c.CIDLength)
	}
	if c.MaxAssociations <= 0 || c.MaxConcurrentHandshakes <= 0 || c.MaxBodyBytes <= 0 || c.MaxBodyBytes > 64<<10 {
		return errors.New("frontend resource limits are invalid")
	}
	if c.HTTPTimeout <= 0 || c.CoAPExchangeTimeout <= 0 || c.IdleSweepInterval <= 0 {
		return errors.New("frontend timeouts and sweep interval must be positive")
	}
	return nil
}

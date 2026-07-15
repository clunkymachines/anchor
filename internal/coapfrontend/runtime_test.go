package coapfrontend

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"anchor/internal/coapapi"
	"anchor/internal/domain"
	"github.com/fxamacker/cbor/v2"
	piondtls "github.com/pion/dtls/v3"
	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapnet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/client"
)

type fakeAnchor struct {
	credential coapapi.CredentialResolveResponse
	mu         sync.Mutex
	telemetry  int
	operations []coapapi.OperationResultRequest
	statuses   []coapapi.TaskStatusRequest
}

func (f *fakeAnchor) ResolveCredentials(context.Context, string) (coapapi.CredentialResolveResponse, error) {
	return f.credential, nil
}
func (f *fakeAnchor) Activity(context.Context, string, coapapi.ActivityRequest) error { return nil }
func (f *fakeAnchor) Telemetry(context.Context, string, coapapi.TelemetryRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.telemetry++
	return nil
}
func (f *fakeAnchor) Operation(_ context.Context, _ string, _ int64, in coapapi.OperationResultRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.operations = append(f.operations, in)
	return nil
}
func (f *fakeAnchor) TaskStatus(_ context.Context, _ string, _ int64, in coapapi.TaskStatusRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, in)
	return nil
}
func (f *fakeAnchor) PendingTask(context.Context, string) (coapapi.TaskProjection, bool, error) {
	return coapapi.TaskProjection{}, false, nil
}

func TestLoopbackDTLSTelemetryAndBidirectionalRead(t *testing.T) {
	psk := []byte("0123456789abcdef")
	anchor := &fakeAnchor{credential: coapapi.CredentialResolveResponse{DeviceID: "device-1", OrganisationID: 7, PSK: coapapi.EncodePSK(psk), Revision: 1, ExpectedHeartbeatSeconds: 60, ExpectedProtocol: "coap"}}
	config := Config{UDPListenAddr: "127.0.0.1:0", ControlListenAddr: "127.0.0.1:0", AnchorURL: "http://anchor.invalid", BearerToken: "test-secret", HTTPTimeout: time.Second, CoAPExchangeTimeout: time.Second, IdleSweepInterval: time.Minute, MaxAssociations: 4, MaxConcurrentHandshakes: 4, MaxBodyBytes: 64 << 10}
	runtime, err := NewRuntime(config, anchor, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := coapnet.NewDTLSListener("udp4", config.UDPListenAddr, runtime.dtlsConfig())
	if err != nil {
		t.Fatal(err)
	}
	server := coapdtls.NewServer(options.WithHandlerFunc(runtime.handleDeviceRequest), options.WithOnNewConn(runtime.onNewConnection), options.WithMaxMessageSize(uint32(config.MaxBodyBytes)), options.WithBlockwise(true, blockwise.SZX1024, time.Second))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })

	deviceHandler := func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
		path, _ := req.Path()
		if req.Code() == codes.GET && path == "/sensor" {
			body, _ := cbor.Marshal(map[string]any{"temperature": 21})
			_ = w.SetResponse(codes.Content, message.AppCBOR, bytes.NewReader(body))
			return
		}
		_ = w.SetResponse(codes.NotFound, message.TextPlain, nil)
	}
	device, err := coapdtls.Dial(listener.Addr().String(), &piondtls.Config{PSK: func([]byte) ([]byte, error) { return psk, nil }, PSKIdentityHint: []byte("device-1"), CipherSuites: []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8}, ExtendedMasterSecret: piondtls.RequireExtendedMasterSecret}, options.WithHandlerFunc(deviceHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = device.Close() })

	payload, _ := cbor.Marshal(map[string]any{"temperature": 20})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := device.Post(ctx, "/dp", message.AppCBOR, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if response.Code() != codes.Changed {
		t.Fatalf("telemetry code = %v", response.Code())
	}

	task := coapapi.TaskProjection{ID: 42, DeviceID: "device-1", Type: domain.TaskTypeRead, ReadPaths: []string{"/sensor"}}
	body, _ := json.Marshal(task)
	req := httptest.NewRequest(http.MethodPost, coapapi.VersionPrefix+"/tasks/42/dispatch", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-secret")
	res := httptest.NewRecorder()
	runtime.ControlHandler().ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("dispatch status=%d body=%s", res.Code, res.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		anchor.mu.Lock()
		complete := len(anchor.operations) == 1 && len(anchor.statuses) == 1
		if complete {
			op, status := anchor.operations[0], anchor.statuses[0]
			anchor.mu.Unlock()
			if op.Path != "/sensor" || op.ResponseCode != "2.05 Content" {
				t.Fatalf("unexpected operation: %#v", op)
			}
			if status.Status != domain.TaskStatusSuccess {
				t.Fatalf("unexpected status: %#v", status)
			}
			return
		}
		anchor.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for task completion")
}

func TestLoopbackDTLSNegotiatesCID(t *testing.T) {
	psk := []byte("0123456789abcdef")
	anchor := &fakeAnchor{credential: coapapi.CredentialResolveResponse{DeviceID: "cid-device", PSK: coapapi.EncodePSK(psk), Revision: 1, ExpectedHeartbeatSeconds: 60, ExpectedProtocol: "coap"}}
	config := Config{UDPListenAddr: "127.0.0.1:0", ControlListenAddr: "127.0.0.1:0", AnchorURL: "http://anchor.invalid", BearerToken: "test-secret", HTTPTimeout: time.Second, CoAPExchangeTimeout: time.Second, IdleSweepInterval: time.Minute, CIDLength: 8, MaxAssociations: 2, MaxConcurrentHandshakes: 2, MaxBodyBytes: 64 << 10}
	runtime, err := NewRuntime(config, anchor, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := coapnet.NewDTLSListener("udp4", config.UDPListenAddr, runtime.dtlsConfig())
	if err != nil {
		t.Fatal(err)
	}
	server := coapdtls.NewServer(options.WithHandlerFunc(runtime.handleDeviceRequest), options.WithOnNewConn(runtime.onNewConnection))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })
	device, err := coapdtls.Dial(listener.Addr().String(), &piondtls.Config{PSK: func([]byte) ([]byte, error) { return psk, nil }, PSKIdentityHint: []byte("cid-device"), CipherSuites: []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM}, ExtendedMasterSecret: piondtls.RequireExtendedMasterSecret, ConnectionIDGenerator: piondtls.RandomCIDGenerator(8)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = device.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := device.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	status := runtime.Registry().Status("cid-device")
	if !status.Connected || !status.CIDNegotiated {
		t.Fatalf("unexpected association status: %#v", status)
	}
}

func TestControlRequiresBearerScheme(t *testing.T) {
	runtime, err := NewRuntime(Config{UDPListenAddr: "127.0.0.1:0", ControlListenAddr: "127.0.0.1:0", AnchorURL: "http://anchor.invalid", BearerToken: "test-secret", HTTPTimeout: time.Second, CoAPExchangeTimeout: time.Second, IdleSweepInterval: time.Minute, MaxAssociations: 1, MaxConcurrentHandshakes: 1, MaxBodyBytes: 1024}, &fakeAnchor{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"", "test-secret", "Basic test-secret", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodGet, coapapi.VersionPrefix+"/status", nil)
		req.Header.Set("Authorization", header)
		res := httptest.NewRecorder()
		runtime.ControlHandler().ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("header %q returned %d", header, res.Code)
		}
	}
}

func TestLibcoapCLIHandshakeAndTelemetry(t *testing.T) {
	cli, err := exec.LookPath("coap-client")
	if err != nil {
		t.Skip("libcoap coap-client is not installed")
	}
	psk := []byte("0123456789abcdef")
	anchor := &fakeAnchor{credential: coapapi.CredentialResolveResponse{DeviceID: "libcoap-device", PSK: coapapi.EncodePSK(psk), Revision: 1, ExpectedHeartbeatSeconds: 60, ExpectedProtocol: "coap"}}
	config := Config{UDPListenAddr: "127.0.0.1:0", ControlListenAddr: "127.0.0.1:0", AnchorURL: "http://anchor.invalid", BearerToken: "test-secret", HTTPTimeout: time.Second, CoAPExchangeTimeout: 3 * time.Second, IdleSweepInterval: time.Minute, MaxAssociations: 2, MaxConcurrentHandshakes: 2, MaxBodyBytes: 64 << 10}
	runtime, err := NewRuntime(config, anchor, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := coapnet.NewDTLSListener("udp4", config.UDPListenAddr, runtime.dtlsConfig())
	if err != nil {
		t.Fatal(err)
	}
	server := coapdtls.NewServer(options.WithHandlerFunc(runtime.handleDeviceRequest), options.WithOnNewConn(runtime.onNewConnection), options.WithMaxMessageSize(uint32(config.MaxBodyBytes)), options.WithBlockwise(true, blockwise.SZX1024, config.CoAPExchangeTimeout))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })
	payload, _ := cbor.Marshal(map[string]any{"client": "libcoap", "temperature": 22})
	payloadPath := t.TempDir() + "/telemetry.cbor"
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	uri := "coaps://" + listener.Addr().String() + "/dp"
	command := exec.CommandContext(ctx, cli, "-m", "post", "-t", "application/cbor", "-f", payloadPath, "-u", "libcoap-device", "-k", "0x"+hex.EncodeToString(psk), "-B", "5", "-v", "7", uri)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("coap-client failed: %v\n%s", err, output)
	}
	anchor.mu.Lock()
	telemetry := anchor.telemetry
	anchor.mu.Unlock()
	if telemetry != 1 {
		t.Fatalf("expected one telemetry callback, got %d\n%s", telemetry, output)
	}
}

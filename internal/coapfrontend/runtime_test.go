package coapfrontend

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
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
	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/dtls/v3/pkg/protocol/extension"
	"github.com/pion/dtls/v3/pkg/protocol/handshake"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapnet "github.com/plgd-dev/go-coap/v3/net"
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
	server := runtime.newCoAPServer()
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
	config := Config{UDPListenAddr: "127.0.0.1:0", ControlListenAddr: "127.0.0.1:0", AnchorURL: "http://anchor.invalid", BearerToken: "test-secret", HTTPTimeout: time.Second, CoAPExchangeTimeout: time.Second, IdleSweepInterval: time.Minute, CIDLength: DefaultCIDLength, MaxAssociations: 2, MaxConcurrentHandshakes: 2, MaxBodyBytes: 64 << 10}
	runtime, err := NewRuntime(config, anchor, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := coapnet.NewDTLSListener("udp4", config.UDPListenAddr, runtime.dtlsConfig())
	if err != nil {
		t.Fatal(err)
	}
	server := runtime.newCoAPServer()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })
	device, err := coapdtls.Dial(listener.Addr().String(), &piondtls.Config{PSK: func([]byte) ([]byte, error) { return psk, nil }, PSKIdentityHint: []byte("cid-device"), CipherSuites: []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM}, ExtendedMasterSecret: piondtls.RequireExtendedMasterSecret, ConnectionIDGenerator: piondtls.RandomCIDGenerator(DefaultCIDLength)})
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

func TestLoopbackDTLSCIDMigrationPreservesAssociation(t *testing.T) {
	const deviceID = "cid-migration-device"

	psk := []byte("0123456789abcdef")
	anchor := &fakeAnchor{credential: coapapi.CredentialResolveResponse{DeviceID: deviceID, PSK: coapapi.EncodePSK(psk), Revision: 1, ExpectedHeartbeatSeconds: 60, ExpectedProtocol: "coap"}}
	config := Config{UDPListenAddr: "127.0.0.1:0", ControlListenAddr: "127.0.0.1:0", AnchorURL: "http://anchor.invalid", BearerToken: "test-secret", HTTPTimeout: time.Second, CoAPExchangeTimeout: time.Second, IdleSweepInterval: time.Minute, CIDLength: DefaultCIDLength, MaxAssociations: 2, MaxConcurrentHandshakes: 2, MaxBodyBytes: 64 << 10}
	runtime, err := NewRuntime(config, anchor, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := coapnet.NewDTLSListener("udp4", config.UDPListenAddr, runtime.dtlsConfig())
	if err != nil {
		t.Fatal(err)
	}
	server := runtime.newCoAPServer()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })

	serverAddr, err := net.ResolveUDPAddr("udp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	initialSocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	initialCapture := &recordingPacketConn{PacketConn: initialSocket}
	clientConfig := &piondtls.Config{
		PSK:                   func([]byte) ([]byte, error) { return psk, nil },
		PSKIdentityHint:       []byte(deviceID),
		CipherSuites:          []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM},
		ExtendedMasterSecret:  piondtls.RequireExtendedMasterSecret,
		ConnectionIDGenerator: piondtls.RandomCIDGenerator(DefaultCIDLength),
	}
	initialDTLS, err := piondtls.Client(initialCapture, serverAddr, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	initialClient := coapdtls.Client(initialDTLS)

	response := postHeartbeat(t, initialClient)
	if response.Code() != codes.Changed {
		t.Fatalf("initial heartbeat code = %v", response.Code())
	}
	initialClient.ReleaseMessage(response)
	initialStatus := runtime.Registry().Status(deviceID)
	if !initialStatus.Connected || !initialStatus.CIDNegotiated {
		t.Fatalf("unexpected initial association status: %#v", initialStatus)
	}
	if initialStatus.CIDLength != DefaultCIDLength {
		t.Fatalf("association CID length = %d, want %d", initialStatus.CIDLength, DefaultCIDLength)
	}
	initialPeer := initialSocket.LocalAddr().String()
	if initialStatus.PeerAddress != initialPeer {
		t.Fatalf("initial peer address = %q, want %q", initialStatus.PeerAddress, initialPeer)
	}

	// go-coap used to close the server-side DTLS connection after 16 seconds
	// of inactivity. Stay idle past that boundary before migrating so this test
	// proves that the retained association remains usable across a device cycle.
	const legacyGoCoAPInactivityTimeout = 16 * time.Second
	idleTimer := time.NewTimer(legacyGoCoAPInactivityTimeout + 2*time.Second)
	select {
	case <-initialClient.Done():
		idleTimer.Stop()
		t.Fatal("DTLS association closed during retained-session idle period")
	case <-idleTimer.C:
	}
	retainedStatus := runtime.Registry().Status(deviceID)
	if !retainedStatus.Connected || retainedStatus.Generation != initialStatus.Generation {
		t.Fatalf("association was not retained while idle: before=%#v after=%#v", initialStatus, retainedStatus)
	}

	state, ok := initialDTLS.ConnectionState()
	if !ok {
		t.Fatal("cannot export established DTLS state")
	}
	_, serverCIDFromState, ok := stateConnectionIDs(&state)
	if !ok || len(serverCIDFromState) != DefaultCIDLength {
		t.Fatalf("server CID from DTLS state = %x, want %d bytes", serverCIDFromState, DefaultCIDLength)
	}
	serverCIDFromHello, ok := serverCIDFromServerHello(initialCapture.receivedDatagrams())
	if !ok {
		t.Fatal("original ServerHello did not contain a CID")
	}
	if !bytes.Equal(serverCIDFromHello, serverCIDFromState) {
		t.Fatalf("ServerHello CID %x differs from established state CID %x", serverCIDFromHello, serverCIDFromState)
	}

	if err := initialSocket.Close(); err != nil {
		t.Fatal(err)
	}
	migratedSocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	migratedCapture := &recordingPacketConn{PacketConn: migratedSocket}
	migratedDTLS, err := piondtls.Resume(&state, migratedCapture, serverAddr, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	migratedClient := coapdtls.Client(migratedDTLS)
	t.Cleanup(func() { _ = migratedClient.Close() })

	response = postHeartbeat(t, migratedClient)
	if response.Code() != codes.Changed {
		t.Fatalf("migrated heartbeat code = %v", response.Code())
	}
	migratedClient.ReleaseMessage(response)

	migratedPeer := migratedSocket.LocalAddr().String()
	if migratedPeer == initialPeer {
		t.Fatalf("client UDP tuple did not change: %q", migratedPeer)
	}
	migratedStatus := runtime.Registry().Status(deviceID)
	if migratedStatus.Generation != initialStatus.Generation {
		t.Fatalf("association generation changed from %d to %d", initialStatus.Generation, migratedStatus.Generation)
	}
	if migratedStatus.PeerAddress != migratedPeer {
		t.Fatalf("migrated peer address = %q, want %q", migratedStatus.PeerAddress, migratedPeer)
	}

	cidRecord, ok := connectionIDRecord(migratedCapture.sentDatagrams(), DefaultCIDLength)
	if !ok {
		t.Fatal("migrated heartbeat did not emit a DTLS Connection ID record")
	}
	if cidRecord.source != migratedPeer || cidRecord.destination != serverAddr.String() {
		t.Fatalf("migrated CID packet tuple = %s -> %s, want %s -> %s", cidRecord.source, cidRecord.destination, migratedPeer, serverAddr)
	}
	if !bytes.Equal(cidRecord.cid, serverCIDFromHello) {
		t.Fatalf("migrated record CID %x differs from ServerHello CID %x", cidRecord.cid, serverCIDFromHello)
	}

	metrics := runtime.metricSnapshot()
	if metrics.CIDNegotiated != 1 || metrics.CIDLength != DefaultCIDLength || metrics.CIDPacketReceived != 2 || metrics.CIDPacketRouted != 2 || metrics.PeerAddressChanged != 1 || metrics.CoAPRequestReceived != 2 {
		t.Fatalf("unexpected CID migration diagnostics: %#v", metrics)
	}
}

func postHeartbeat(t *testing.T, cc *client.Conn) *pool.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := cc.AcquireMessage(ctx)
	token, err := cc.GetToken()
	if err != nil {
		cc.ReleaseMessage(req)
		t.Fatal(err)
	}
	if err := req.SetupPost("/hb", token, message.TextPlain, nil); err != nil {
		cc.ReleaseMessage(req)
		t.Fatal(err)
	}
	req.SetType(message.Confirmable)
	response, err := cc.Do(req)
	cc.ReleaseMessage(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

type capturedDatagram struct {
	payload     []byte
	source      string
	destination string
}

type recordingPacketConn struct {
	net.PacketConn
	mu       sync.Mutex
	received []capturedDatagram
	sent     []capturedDatagram
}

func (c *recordingPacketConn) ReadFrom(payload []byte) (int, net.Addr, error) {
	n, source, err := c.PacketConn.ReadFrom(payload)
	if n > 0 {
		c.mu.Lock()
		c.received = append(c.received, capturedDatagram{payload: bytes.Clone(payload[:n]), source: source.String(), destination: c.LocalAddr().String()})
		c.mu.Unlock()
	}
	return n, source, err
}

func (c *recordingPacketConn) WriteTo(payload []byte, destination net.Addr) (int, error) {
	c.mu.Lock()
	c.sent = append(c.sent, capturedDatagram{payload: bytes.Clone(payload), source: c.LocalAddr().String(), destination: destination.String()})
	c.mu.Unlock()
	return c.PacketConn.WriteTo(payload, destination)
}

func (c *recordingPacketConn) receivedDatagrams() []capturedDatagram {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedDatagram(nil), c.received...)
}

func (c *recordingPacketConn) sentDatagrams() []capturedDatagram {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedDatagram(nil), c.sent...)
}

func serverCIDFromServerHello(datagrams []capturedDatagram) ([]byte, bool) {
	for _, datagram := range datagrams {
		records, err := recordlayer.UnpackDatagram(datagram.payload)
		if err != nil {
			continue
		}
		for _, record := range records {
			var recordHeader recordlayer.Header
			if recordHeader.Unmarshal(record) != nil || recordHeader.ContentType != protocol.ContentTypeHandshake {
				continue
			}
			var handshakeHeader handshake.Header
			if handshakeHeader.Unmarshal(record[recordlayer.FixedHeaderSize:]) != nil || handshakeHeader.Type != handshake.TypeServerHello {
				continue
			}
			var hello handshake.MessageServerHello
			if hello.Unmarshal(record[recordlayer.FixedHeaderSize+handshake.HeaderLength:]) != nil {
				continue
			}
			for _, candidate := range hello.Extensions {
				if cid, ok := candidate.(*extension.ConnectionID); ok {
					return bytes.Clone(cid.CID), true
				}
			}
		}
	}
	return nil, false
}

func connectionIDRecord(datagrams []capturedDatagram, cidLength int) (capturedCIDRecord, bool) {
	for _, datagram := range datagrams {
		if len(datagram.payload) < recordlayer.FixedHeaderSize+cidLength || datagram.payload[0] != byte(protocol.ContentTypeConnectionID) {
			continue
		}
		payloadLengthOffset := 11 + cidLength
		payloadLength := int(binary.BigEndian.Uint16(datagram.payload[payloadLengthOffset : payloadLengthOffset+2]))
		if len(datagram.payload) < recordlayer.FixedHeaderSize+cidLength+payloadLength {
			continue
		}
		return capturedCIDRecord{cid: bytes.Clone(datagram.payload[11 : 11+cidLength]), source: datagram.source, destination: datagram.destination}, true
	}
	return capturedCIDRecord{}, false
}

type capturedCIDRecord struct {
	cid         []byte
	source      string
	destination string
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
	server := runtime.newCoAPServer()
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

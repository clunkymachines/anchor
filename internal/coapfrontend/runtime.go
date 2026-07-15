package coapfrontend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
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

type authenticatedDevice struct {
	DeviceID       string
	OrganisationID int64
	Revision       int64
	Heartbeat      time.Duration
	Protocol       string
}
type authContextKey struct{}

type Runtime struct {
	config     Config
	anchor     AnchorClient
	logger     *slog.Logger
	registry   *Registry
	server     *coapdtlsServer
	handshakes chan struct{}

	metrics struct {
		handshakeSuccess, handshakeFailure, requestsAccepted, requestsRejected atomic.Int64
		dispatchStarted, dispatchFailed, dispatchCompleted                     atomic.Int64
	}
}

// coapdtlsServer is the small server surface needed by Runtime.
type coapdtlsServer struct{ stop func() }

func NewRuntime(config Config, anchor AnchorClient, logger *slog.Logger) (*Runtime, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if anchor == nil {
		return nil, errors.New("Anchor client is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runtime{config: config, anchor: anchor, logger: logger, registry: NewRegistry(config.MaxAssociations), handshakes: make(chan struct{}, config.MaxConcurrentHandshakes)}, nil
}

func (r *Runtime) Registry() *Registry { return r.registry }

func (r *Runtime) dtlsConfig() *piondtls.Config {
	cfg := &piondtls.Config{
		PSKIdentityHint:        []byte("Anchor CoAP frontend"),
		CipherSuites:           []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8, piondtls.TLS_PSK_WITH_AES_128_CCM},
		ExtendedMasterSecret:   piondtls.RequireExtendedMasterSecret,
		ReplayProtectionWindow: 64,
		PSK: func(identity []byte) ([]byte, error) {
			ctx, cancel := context.WithTimeout(context.Background(), r.config.HTTPTimeout)
			defer cancel()
			resolved, err := r.anchor.ResolveCredentials(ctx, string(identity))
			if err != nil || resolved.ExpectedProtocol != "coap" {
				return nil, errors.New("credential rejected")
			}
			psk, err := coapapi.DecodePSK(resolved.PSK)
			if err != nil {
				return nil, errors.New("credential rejected")
			}
			return psk, nil
		},
	}
	if r.config.CIDLength > 0 {
		cfg.ConnectionIDGenerator = piondtls.RandomCIDGenerator(r.config.CIDLength)
	}
	return cfg
}

func (r *Runtime) Serve(ctx context.Context) error {
	listener, err := coapnet.NewDTLSListener("udp", r.config.UDPListenAddr, r.dtlsConfig())
	if err != nil {
		return err
	}
	defer listener.Close()

	srv := coapdtls.NewServer(
		options.WithHandlerFunc(r.handleDeviceRequest),
		options.WithOnNewConn(r.onNewConnection),
		options.WithMaxMessageSize(uint32(r.config.MaxBodyBytes)),
		options.WithBlockwise(true, blockwise.SZX1024, r.config.CoAPExchangeTimeout),
	)
	r.server = &coapdtlsServer{stop: srv.Stop}
	control := &http.Server{Addr: r.config.ControlListenAddr, Handler: r.ControlHandler(), ReadHeaderTimeout: 5 * time.Second}
	controlErr := make(chan error, 1)
	go func() {
		if err := control.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			controlErr <- err
		}
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
	ticker := time.NewTicker(r.config.IdleSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			srv.Stop()
			for _, a := range r.registry.Snapshot() {
				r.registry.Invalidate(a.DeviceID, 0, true)
			}
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return control.Shutdown(shutdown)
		case err := <-controlErr:
			return err
		case err := <-serveErr:
			if ctx.Err() != nil {
				return nil
			}
			return err
		case now := <-ticker.C:
			r.registry.Sweep(now)
		}
	}
}

func (r *Runtime) onNewConnection(cc *client.Conn) {
	select {
	case r.handshakes <- struct{}{}:
		defer func() { <-r.handshakes }()
	default:
		r.metrics.handshakeFailure.Add(1)
		_ = cc.Close()
		return
	}
	dtlsConn, ok := cc.NetConn().(*piondtls.Conn)
	if !ok {
		_ = cc.Close()
		return
	}
	ctx, cancelHandshake := context.WithTimeout(context.Background(), r.config.HTTPTimeout)
	if err := dtlsConn.HandshakeContext(ctx); err != nil {
		cancelHandshake()
		r.metrics.handshakeFailure.Add(1)
		r.logger.Warn("CoAP DTLS handshake failed", "error_class", "handshake")
		_ = cc.Close()
		return
	}
	cancelHandshake()
	state, ok := dtlsConn.ConnectionState()
	if !ok {
		r.metrics.handshakeFailure.Add(1)
		_ = cc.Close()
		return
	}
	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), r.config.HTTPTimeout)
	resolved, resolveErr := r.anchor.ResolveCredentials(resolveCtx, string(state.IdentityHint))
	resolveCancel()
	if resolveErr != nil || resolved.DeviceID == "" || resolved.ExpectedProtocol != "coap" {
		r.metrics.handshakeFailure.Add(1)
		_ = cc.Close()
		return
	}
	device := authenticatedDevice{DeviceID: resolved.DeviceID, OrganisationID: resolved.OrganisationID, Revision: resolved.Revision, Heartbeat: time.Duration(resolved.ExpectedHeartbeatSeconds) * time.Second, Protocol: resolved.ExpectedProtocol}
	cidNegotiated := stateHasCID(&state)
	connCtx, cancel := context.WithCancel(context.Background())
	cc.SetContextValue(authContextKey{}, device)
	if err := r.registry.Install(Association{DeviceID: device.DeviceID, CredentialRevision: device.Revision, CIDNegotiated: cidNegotiated, PeerAddress: cc.RemoteAddr(), ExpectedHeartbeat: device.Heartbeat, Cancel: cancel, Conn: cc}); err != nil {
		cancel()
		r.metrics.handshakeFailure.Add(1)
		_ = cc.Close()
		return
	}
	a, _ := r.registry.Get(device.DeviceID)
	cc.AddOnClose(func() { r.registry.Remove(device.DeviceID, a.Generation); cancel() })
	r.metrics.handshakeSuccess.Add(1)
	r.logger.Info("CoAP association authenticated", "device_id", device.DeviceID, "generation", a.Generation, "cid", cidNegotiated)
	go func() {
		activityCtx, stop := context.WithTimeout(connCtx, r.config.HTTPTimeout)
		defer stop()
		_ = r.anchor.Activity(activityCtx, device.DeviceID, coapapi.ActivityRequest{TimestampMS: time.Now().UnixMilli(), Reason: "authenticated"})
		r.reconcile(connCtx, device.DeviceID)
	}()
}

func stateHasCID(state *piondtls.State) bool {
	data, err := state.MarshalBinary()
	if err != nil {
		return false
	}
	var wire struct {
		LocalConnectionID  []byte
		RemoteConnectionID []byte
	}
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&wire); err != nil {
		return false
	}
	return len(wire.LocalConnectionID) > 0 || len(wire.RemoteConnectionID) > 0
}

func (r *Runtime) handleDeviceRequest(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
	device, ok := req.Context().Value(authContextKey{}).(authenticatedDevice)
	if !ok {
		r.respond(w, codes.Unauthorized)
		return
	}
	a, active := r.registry.Get(device.DeviceID)
	if active {
		r.registry.Touch(device.DeviceID, a.Generation, a.Conn.RemoteAddr())
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), r.config.HTTPTimeout)
		defer cancel()
		_ = r.anchor.Activity(ctx, device.DeviceID, coapapi.ActivityRequest{TimestampMS: time.Now().UnixMilli(), Reason: "request"})
	}()
	if req.Type() != message.Confirmable {
		r.metrics.requestsRejected.Add(1)
		r.respond(w, codes.BadRequest)
		return
	}
	path, err := req.Path()
	if err != nil {
		r.respond(w, codes.BadRequest)
		return
	}
	body, err := req.ReadBody()
	if err != nil || int64(len(body)) > r.config.MaxBodyBytes {
		r.metrics.requestsRejected.Add(1)
		r.respond(w, codes.RequestEntityTooLarge)
		return
	}
	r.metrics.requestsAccepted.Add(1)
	switch {
	case path == "/dp" && req.Code() == codes.POST:
		format, err := req.ContentFormat()
		if err != nil || format != message.AppCBOR || len(body) == 0 || !isCBORMap(body) {
			r.respond(w, codes.UnsupportedMediaType)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), r.config.HTTPTimeout)
		defer cancel()
		err = r.anchor.Telemetry(ctx, device.DeviceID, coapapi.TelemetryRequest{Method: "POST", Path: path, ContentFormat: "application/cbor", CorrelationID: correlationID(), TimestampMS: time.Now().UnixMilli(), Payload: body})
		if err != nil {
			r.respond(w, codes.ServiceUnavailable)
			return
		}
		r.respond(w, codes.Changed)
		go r.reconcile(context.Background(), device.DeviceID)
	case path == "/hb" && req.Code() == codes.POST:
		if len(body) != 0 || req.HasOption(message.ContentFormat) {
			r.respond(w, codes.BadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), r.config.HTTPTimeout)
		defer cancel()
		if err := r.anchor.Activity(ctx, device.DeviceID, coapapi.ActivityRequest{TimestampMS: time.Now().UnixMilli(), Reason: "heartbeat"}); err != nil {
			r.respond(w, codes.ServiceUnavailable)
			return
		}
		r.respond(w, codes.Changed)
		go r.reconcile(context.Background(), device.DeviceID)
	case strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/status") && req.Code() == codes.PUT:
		r.handleTaskStatus(w, req, device, path, body)
	default:
		if path == "/dp" || path == "/hb" || strings.HasPrefix(path, "/tasks/") {
			r.respond(w, codes.MethodNotAllowed)
		} else {
			r.respond(w, codes.NotFound)
		}
	}
}

func (r *Runtime) handleTaskStatus(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message, device authenticatedDevice, path string, body []byte) {
	format, err := req.ContentFormat()
	if err != nil || format != message.AppCBOR {
		r.respond(w, codes.UnsupportedMediaType)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 {
		r.respond(w, codes.NotFound)
		return
	}
	taskID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || taskID <= 0 {
		r.respond(w, codes.NotFound)
		return
	}
	var payload struct {
		Status  string `cbor:"status"`
		Message string `cbor:"msg"`
	}
	if err := cbor.Unmarshal(body, &payload); err != nil || (payload.Status != domain.TaskStatusInProgress && payload.Status != domain.TaskStatusSuccess && payload.Status != domain.TaskStatusFailure) || len([]rune(payload.Message)) > 512 {
		r.respond(w, codes.BadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), r.config.HTTPTimeout)
	defer cancel()
	if err := r.anchor.TaskStatus(ctx, device.DeviceID, taskID, coapapi.TaskStatusRequest{Status: payload.Status, Message: payload.Message}); err != nil {
		r.respond(w, codes.ServiceUnavailable)
		return
	}
	r.respond(w, codes.Changed)
	if payload.Status == domain.TaskStatusSuccess || payload.Status == domain.TaskStatusFailure {
		go r.reconcile(context.Background(), device.DeviceID)
	}
}

func (r *Runtime) respond(w *responsewriter.ResponseWriter[*client.Conn], code codes.Code) {
	_ = w.SetResponse(code, message.TextPlain, nil)
}
func isCBORMap(body []byte) bool {
	var v map[any]any
	return cbor.Unmarshal(body, &v) == nil && v != nil
}
func correlationID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func (r *Runtime) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			const prefix = "Bearer "
			header := req.Header.Get("Authorization")
			if !strings.HasPrefix(header, prefix) || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), []byte(r.config.BearerToken)) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, req)
		})
	}
	mux.Handle("GET "+coapapi.VersionPrefix+"/status", auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, coapapi.FrontendStatus{State: "healthy", ActiveAssociations: len(r.registry.Snapshot())})
	})))
	mux.Handle("GET "+coapapi.VersionPrefix+"/metrics", auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, r.metricSnapshot()) })))
	mux.Handle("GET "+coapapi.VersionPrefix+"/devices/{deviceID}/association", auth(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, r.registry.Status(req.PathValue("deviceID")))
	})))
	mux.Handle("POST "+coapapi.VersionPrefix+"/devices/{deviceID}/invalidate", auth(http.HandlerFunc(r.invalidateHandler)))
	mux.Handle("POST "+coapapi.VersionPrefix+"/tasks/{taskID}/dispatch", auth(http.HandlerFunc(r.dispatchHandler)))
	return mux
}

func (r *Runtime) invalidateHandler(w http.ResponseWriter, req *http.Request) {
	var in coapapi.InvalidateRequest
	if decodeStrict(req, r.config.MaxBodyBytes, &in) != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	r.registry.Invalidate(req.PathValue("deviceID"), in.Revision, in.Force)
	w.WriteHeader(http.StatusNoContent)
}
func (r *Runtime) dispatchHandler(w http.ResponseWriter, req *http.Request) {
	var task coapapi.TaskProjection
	if decodeStrict(req, r.config.MaxBodyBytes, &task) != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(req.PathValue("taskID"), 10, 64)
	if err != nil || id != task.ID {
		http.Error(w, "task ID mismatch", http.StatusBadRequest)
		return
	}
	a, ok := r.registry.Get(taskDeviceID(task))
	if !ok { // Older projections omit device ID; locate only when unambiguous in tests is forbidden.
		// Production projections carry DeviceID. Keep this explicit so dispatch never guesses an association.
		http.Error(w, "no active association", http.StatusConflict)
		return
	}
	if !a.Dispatch.TryLock() {
		http.Error(w, "device is busy", http.StatusConflict)
		return
	}
	r.metrics.dispatchStarted.Add(1)
	go r.execute(a, task)
	w.WriteHeader(http.StatusAccepted)
}

func taskDeviceID(task coapapi.TaskProjection) string { return task.DeviceID }
func decodeStrict(req *http.Request, limit int64, out any) error {
	dec := json.NewDecoder(io.LimitReader(req.Body, limit+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (r *Runtime) metricSnapshot() coapapi.Metrics {
	return coapapi.Metrics{ActiveAssociations: int64(len(r.registry.Snapshot())), HandshakeSuccess: r.metrics.handshakeSuccess.Load(), HandshakeFailure: r.metrics.handshakeFailure.Load(), RequestsAccepted: r.metrics.requestsAccepted.Load(), RequestsRejected: r.metrics.requestsRejected.Load(), DispatchStarted: r.metrics.dispatchStarted.Load(), DispatchFailed: r.metrics.dispatchFailed.Load(), DispatchCompleted: r.metrics.dispatchCompleted.Load()}
}

func (r *Runtime) reconcile(ctx context.Context, deviceID string) {
	a, ok := r.registry.Get(deviceID)
	if !ok || !a.Dispatch.TryLock() {
		return
	}
	queryCtx, cancel := context.WithTimeout(ctx, r.config.HTTPTimeout)
	task, found, err := r.anchor.PendingTask(queryCtx, deviceID)
	cancel()
	if err != nil || !found {
		a.Dispatch.Unlock()
		return
	}
	go r.executeLocked(a, task)
}
func (r *Runtime) execute(a *Association, task coapapi.TaskProjection) {
	status, msg, failed := r.executeTask(a, task)
	a.Dispatch.Unlock()
	r.setTaskStatus(a.DeviceID, task.ID, status, msg)
	r.recordDispatchOutcome(failed)
	r.logger.Info("CoAP task dispatch finished", "device_id", a.DeviceID, "task_id", task.ID, "generation", a.Generation, "failed_operations", failed)
}
func (r *Runtime) executeLocked(a *Association, task coapapi.TaskProjection) {
	status, msg, failed := r.executeTask(a, task)
	a.Dispatch.Unlock()
	r.setTaskStatus(a.DeviceID, task.ID, status, msg)
	r.recordDispatchOutcome(failed)
	r.logger.Info("CoAP reconciled task finished", "device_id", a.DeviceID, "task_id", task.ID, "generation", a.Generation, "failed_operations", failed)
}

func (r *Runtime) executeTask(a *Association, task coapapi.TaskProjection) (string, string, int) {
	failed := 0
	switch task.Type {
	case domain.TaskTypeRead:
		for _, path := range task.ReadPaths {
			if !r.readOperation(a, task.ID, path) {
				failed++
			}
		}
	case domain.TaskTypeWrite:
		for _, op := range task.WriteValues {
			if !r.writeOperation(a, task.ID, op.Path, op.Value) {
				failed++
			}
		}
	case domain.TaskTypeFOTA:
		body, _ := cbor.Marshal(map[string]any{"task": task.ID, "url": task.ArtifactURL})
		if !r.putOperation(a, task.ID, "/firmware", body, false) {
			failed++
		}
	default:
		failed++
	}
	if failed > 0 {
		return domain.TaskStatusFailure, fmt.Sprintf("%d CoAP operation(s) failed", failed), failed
	}
	if task.Type == domain.TaskTypeFOTA {
		return domain.TaskStatusInProgress, "", 0
	}
	return domain.TaskStatusSuccess, "", 0
}

func (r *Runtime) recordDispatchOutcome(failed int) {
	if failed > 0 {
		r.metrics.dispatchFailed.Add(1)
	} else {
		r.metrics.dispatchCompleted.Add(1)
	}
}

func (r *Runtime) readOperation(a *Association, taskID int64, path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), r.config.CoAPExchangeTimeout)
	defer cancel()
	corr := correlationID()
	res, err := a.Conn.Get(ctx, path, message.Option{ID: message.Accept, Value: []byte{byte(message.AppCBOR)}})
	report := coapapi.OperationResultRequest{TaskID: taskID, CorrelationID: corr, Method: "GET", Path: path, RequestCode: "0.01 GET", TimestampMS: time.Now().UnixMilli()}
	ok := false
	if err != nil {
		report.Error = err.Error()
	} else {
		report.ResponseCode = formatCode(res.Code())
		report.ResponsePayload, _ = res.ReadBody()
		if f, e := res.ContentFormat(); e == nil {
			report.ContentFormat = f.String()
		}
		ok = isSuccess(res.Code()) && len(report.ResponsePayload) > 0 && report.ContentFormat == "application/cbor"
	}
	return ok && r.reportOperation(a.DeviceID, taskID, report)
}
func (r *Runtime) writeOperation(a *Association, taskID int64, path string, value any) bool {
	body, err := cbor.Marshal(value)
	if err != nil {
		return false
	}
	return r.putOperation(a, taskID, path, body, true)
}
func (r *Runtime) putOperation(a *Association, taskID int64, path string, body []byte, allowResponse bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), r.config.CoAPExchangeTimeout)
	defer cancel()
	corr := correlationID()
	res, err := a.Conn.Put(ctx, path, message.AppCBOR, bytes.NewReader(body))
	report := coapapi.OperationResultRequest{TaskID: taskID, CorrelationID: corr, Method: "PUT", Path: path, RequestCode: "0.03 PUT", RequestPayload: body, TimestampMS: time.Now().UnixMilli()}
	ok := false
	if err != nil {
		report.Error = err.Error()
	} else {
		report.ResponseCode = formatCode(res.Code())
		report.ResponsePayload, _ = res.ReadBody()
		if f, e := res.ContentFormat(); e == nil {
			report.ContentFormat = f.String()
		}
		ok = isSuccess(res.Code()) && (!allowResponse || len(report.ResponsePayload) == 0 || report.ContentFormat == "application/cbor")
	}
	return ok && r.reportOperation(a.DeviceID, taskID, report)
}
func (r *Runtime) reportOperation(deviceID string, taskID int64, report coapapi.OperationResultRequest) bool {
	ctx, cancel := context.WithTimeout(context.Background(), r.config.HTTPTimeout)
	defer cancel()
	return r.anchor.Operation(ctx, deviceID, taskID, report) == nil
}
func (r *Runtime) setTaskStatus(deviceID string, taskID int64, status, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), r.config.HTTPTimeout)
	defer cancel()
	_ = r.anchor.TaskStatus(ctx, deviceID, taskID, coapapi.TaskStatusRequest{Status: status, Message: msg})
}
func isSuccess(code codes.Code) bool { return code >= 64 && code < 96 }
func formatCode(code codes.Code) string {
	return fmt.Sprintf("%d.%02d %s", int(code)/32, int(code)%32, code.String())
}

package web

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"anchor/internal/coapapi"
	"anchor/internal/coapcontrol"
	"anchor/internal/db"
	"anchor/internal/domain"
	"github.com/fxamacker/cbor/v2"
)

const coapInternalBodyLimit = 96 << 10

func (s *Server) requireCoAPInternalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.coAPIntegrationEnabled || s.coAPInternalToken == "" {
			writeAPIError(w, http.StatusServiceUnavailable, "coap_disabled", "CoAP integration is disabled.")
			return
		}
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Unauthorized.")
			return
		}
		value := strings.TrimPrefix(header, "Bearer ")
		if !constantTimeTokenEqual(value, s.coAPInternalToken) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Unauthorized.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func constantTimeTokenEqual(got, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func (s *Server) coAPInternalNotFound(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }

func decodeCoAPJSON(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, coapInternalBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON.")
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "Request body must contain one JSON value.")
		return errors.New("extra JSON value")
	}
	return nil
}

func (s *Server) coAPResolveCredentials(w http.ResponseWriter, r *http.Request) {
	var req coapapi.CredentialResolveRequest
	if decodeCoAPJSON(w, r, &req) != nil {
		return
	}
	credential, err := s.store.ResolveCoAPCredential(r.Context(), req.PSKIdentity)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Credential rejected.")
		return
	}
	writeAPIJSON(w, http.StatusOK, coapapi.CredentialResolveResponse{DeviceID: credential.DeviceID, OrganisationID: credential.OrganisationID, PSK: coapapi.EncodePSK(credential.PSK), Revision: credential.Revision, ExpectedHeartbeatSeconds: credential.ExpectedHeartbeatSeconds, ExpectedProtocol: credential.ExpectedProtocol})
}

func (s *Server) coAPDeviceOrganisation(r *http.Request) (int64, error) {
	return s.store.DeviceOrganisationID(r.Context(), r.PathValue("deviceID"))
}

func (s *Server) coAPActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req coapapi.ActivityRequest
	if decodeCoAPJSON(w, r, &req) != nil {
		return
	}
	if req.TimestampMS <= 0 {
		req.TimestampMS = time.Now().UnixMilli()
	}
	if err := s.store.TouchDeviceLastSeen(r.Context(), r.PathValue("deviceID"), req.TimestampMS); err != nil {
		writeAPIError(w, http.StatusNotFound, "device_not_found", "Device not found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeCoAPCBOR(payload []byte) (any, error) {
	var value any
	if len(payload) == 0 {
		return nil, errors.New("empty CBOR payload")
	}
	if err := cbor.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	return normalizeCoAPValue(value), nil
}
func normalizeCoAPValue(value any) any {
	switch v := value.(type) {
	case map[any]any:
		m := make(map[string]any, len(v))
		for k, child := range v {
			m[fmt.Sprint(k)] = normalizeCoAPValue(child)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, child := range v {
			m[k] = normalizeCoAPValue(child)
		}
		return m
	case []any:
		for i := range v {
			v[i] = normalizeCoAPValue(v[i])
		}
		return v
	case uint64:
		return int64(v)
	case uint32:
		return int64(v)
	case uint16:
		return int64(v)
	case uint8:
		return int64(v)
	default:
		return value
	}
}
func flattenCoAP(value any, path string, out *[]domain.DeviceTwinProperty, deviceID string, event *int64, ts int64, source string) {
	if object, ok := value.(map[string]any); ok {
		for key, child := range object {
			p := key
			if path != "" {
				p = path + "." + key
			}
			flattenCoAP(child, p, out, deviceID, event, ts, source)
		}
		return
	}
	if path == "" {
		path = "value"
	}
	encoded, _ := json.Marshal(value)
	valueType := "object"
	switch value.(type) {
	case nil:
		valueType = "null"
	case bool:
		valueType = "bool"
	case string:
		valueType = "string"
	case []byte:
		valueType = "bytes"
	case []any:
		valueType = "array"
	case int64, float64:
		valueType = "number"
	}
	*out = append(*out, domain.DeviceTwinProperty{DeviceID: deviceID, Path: path, ValueJSON: string(encoded), ValueType: valueType, SourceEventID: event, TSObservedMS: ts, TSReceivedMS: ts, Protocol: "coap", SourcePath: source})
}

func (s *Server) coAPTelemetry(w http.ResponseWriter, r *http.Request) {
	var req coapapi.TelemetryRequest
	if decodeCoAPJSON(w, r, &req) != nil {
		return
	}
	if req.Method != "POST" || req.Path != "/dp" || req.ContentFormat != "application/cbor" {
		writeAPIError(w, http.StatusBadRequest, "invalid_telemetry_metadata", "Telemetry must be a CBOR POST.")
		return
	}
	deviceID := r.PathValue("deviceID")
	if _, err := s.store.DeviceOrganisationID(r.Context(), deviceID); err != nil {
		writeAPIError(w, http.StatusNotFound, "device_not_found", "Device not found.")
		return
	}
	if err := domain.ValidateCoAPResourcePath(req.Path); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_path", "CoAP path is invalid.")
		return
	}
	value, err := decodeCoAPCBOR(req.Payload)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_cbor", "Telemetry must be valid CBOR.")
		return
	}
	if _, ok := value.(map[string]any); !ok {
		writeAPIError(w, http.StatusBadRequest, "invalid_telemetry", "Telemetry must be a CBOR map.")
		return
	}
	ts := req.TimestampMS
	if ts <= 0 {
		ts = time.Now().UnixMilli()
	}
	payloadJSON, _ := json.Marshal(value)
	event := domain.DeviceEvent{DeviceID: deviceID, TSReceivedMS: ts, Protocol: "coap", Direction: "inbound", Operation: "post", CoAPPath: req.Path, Method: req.Method, ContentFormat: req.ContentFormat, PayloadRaw: req.Payload, PayloadJSON: string(payloadJSON), CorrelationID: req.CorrelationID, Source: "coap"}
	properties := []domain.DeviceTwinProperty{}
	flattenCoAP(value, "", &properties, deviceID, nil, ts, req.Path)
	if _, err := s.store.RecordDeviceEvent(r.Context(), event, properties); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "telemetry_error", "Could not record telemetry.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) coAPOperation(w http.ResponseWriter, r *http.Request) {
	var req coapapi.OperationResultRequest
	if decodeCoAPJSON(w, r, &req) != nil {
		return
	}
	deviceID := r.PathValue("deviceID")
	taskID, err := strconv.ParseInt(r.PathValue("taskID"), 10, 64)
	if err != nil || taskID <= 0 || req.TaskID != taskID {
		writeAPIError(w, http.StatusNotFound, "task_not_found", "Task not found.")
		return
	}
	ts := req.TimestampMS
	if ts <= 0 {
		ts = time.Now().UnixMilli()
	}
	organisationID, err := s.store.DeviceOrganisationID(r.Context(), deviceID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "device_not_found", "Device not found.")
		return
	}
	if _, err := s.store.DeviceTaskForDevice(r.Context(), taskID, deviceID, organisationID); err != nil {
		writeAPIError(w, http.StatusNotFound, "task_not_found", "Task not found.")
		return
	}
	if err := domain.ValidateCoAPResourcePath(req.Path); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_path", "CoAP path is invalid.")
		return
	}
	out := domain.DeviceEvent{DeviceID: deviceID, TSReceivedMS: ts, Protocol: "coap", Direction: "outbound", Operation: "request", CoAPPath: req.Path, Method: req.Method, Code: req.RequestCode, PayloadRaw: req.RequestPayload, CorrelationID: req.CorrelationID, Source: "coap"}
	var response *domain.DeviceEvent
	var property *domain.DeviceTwinProperty
	if req.ResponseCode != "" || len(req.ResponsePayload) > 0 {
		response = &domain.DeviceEvent{DeviceID: deviceID, TSReceivedMS: ts, Protocol: "coap", Direction: "inbound", Operation: "response", CoAPPath: req.Path, Method: req.Method, Code: req.ResponseCode, ContentFormat: req.ContentFormat, PayloadRaw: req.ResponsePayload, CorrelationID: req.CorrelationID, Source: "coap"}
		if len(req.ResponsePayload) > 0 && req.Error == "" && strings.HasPrefix(req.ResponseCode, "2.") {
			if req.ContentFormat != "application/cbor" {
				writeAPIError(w, http.StatusBadRequest, "invalid_content_format", "Non-empty CoAP responses must be CBOR.")
				return
			}
			value, err := decodeCoAPCBOR(req.ResponsePayload)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_cbor", "Response must be valid CBOR.")
				return
			}
			encoded, _ := json.Marshal(value)
			response.PayloadJSON = string(encoded)
			property = &domain.DeviceTwinProperty{Path: req.Path, ValueJSON: string(encoded), ValueType: coAPValueType(value), TSObservedMS: ts, TSReceivedMS: ts, Protocol: "coap", SourcePath: req.Path}
		}
	}
	if err := s.store.RecordCoAPOperation(r.Context(), out, response, property); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "operation_error", "Could not record operation.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func coAPValueType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case []byte:
		return "bytes"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case int64, float64:
		return "number"
	default:
		return "object"
	}
}

func (s *Server) coAPTaskStatus(w http.ResponseWriter, r *http.Request) {
	var req coapapi.TaskStatusRequest
	if decodeCoAPJSON(w, r, &req) != nil {
		return
	}
	if req.Status != domain.TaskStatusInProgress && req.Status != domain.TaskStatusSuccess && req.Status != domain.TaskStatusFailure {
		writeAPIError(w, http.StatusBadRequest, "invalid_status", "Unsupported task status.")
		return
	}
	if len([]rune(req.Message)) > 512 {
		writeAPIError(w, http.StatusBadRequest, "message_too_long", "Task message is too long.")
		return
	}
	taskID, err := strconv.ParseInt(r.PathValue("taskID"), 10, 64)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "task_not_found", "Task not found.")
		return
	}
	org, err := s.coAPDeviceOrganisation(r)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "task_not_found", "Task not found.")
		return
	}
	if err := s.store.UpdateDeviceTaskStatus(r.Context(), taskID, r.PathValue("deviceID"), org, req.Status, "", req.Message); err != nil {
		writeAPIError(w, http.StatusNotFound, "task_not_found", "Task not found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) coAPPendingTask(w http.ResponseWriter, r *http.Request) {
	org, err := s.coAPDeviceOrganisation(r)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "device_not_found", "Device not found.")
		return
	}
	task, err := s.store.OldestPendingCoAPTask(r.Context(), r.PathValue("deviceID"), org)
	if errors.Is(err, db.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "task_error", "Could not load pending task.")
		return
	}
	artifactURL := ""
	if task.Type == domain.TaskTypeFOTA && s.fotaDownloadBaseURL != "" {
		params, parseErr := domain.ParseFOTATaskParameters(task.ParametersJSON)
		if parseErr != nil {
			writeAPIError(w, http.StatusInternalServerError, "task_projection_error", "Could not project pending task.")
			return
		}
		artifactURL = s.fotaDownloadBaseURL + "/org/" + strconv.FormatInt(org, 10) + "/releases/" + strconv.FormatInt(params.ReleaseID, 10) + "/binary"
	}
	projection, err := coapcontrol.ProjectTask(task, artifactURL)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "task_projection_error", "Could not project pending task.")
		return
	}
	writeAPIJSON(w, http.StatusOK, projection)
}

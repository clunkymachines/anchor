package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"anchor/internal/db"
	"anchor/internal/domain"
	"anchor/internal/telemetry"
)

const apiCheckInBodyLimit = 64 << 10

type apiCheckInEnvelope struct {
	ObservedAt json.RawMessage `json:"observed_at"`
	Data       json.RawMessage `json:"data"`
	TaskStatus json.RawMessage `json:"task_status"`
}

type apiTaskStatus struct {
	Task   int64  `json:"task"`
	Status string `json:"status"`
	Msg    string `json:"msg"`
}

type apiCheckInResponse struct {
	Task *apiTaskProjection `json:"task"`
}
type apiTaskProjection struct {
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	Parameters any    `json:"parameters"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at"`
}
type apiWriteParameters struct {
	Values []apiWriteValue `json:"values"`
}
type apiWriteValue struct {
	Path  string `json:"path"`
	Value any    `json:"value"`
}
type apiFOTAParameters struct {
	URL string `json:"url"`
}

type preparedTelemetry struct {
	present    bool
	payload    string
	properties []domain.DeviceTwinProperty
}
type componentError struct {
	status        int
	code, message string
}

func (s *Server) apiDeviceCheckIn(w http.ResponseWriter, r *http.Request) {
	received := time.Now().UTC()
	credential, ok := apiCredentialFromRequest(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Bearer token is required.")
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return
	}

	envelope, requestErr := decodeAPICheckInEnvelope(w, r)
	if requestErr != nil {
		if errors.As(requestErr, new(*http.MaxBytesError)) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body must not exceed 64 KiB.")
		} else {
			writeAPIError(w, http.StatusBadRequest, "invalid_json", "Request body must contain one valid JSON object with supported fields.")
		}
		return
	}

	deviceID := r.PathValue("deviceID")
	protocol, err := s.store.DeviceExpectedProtocol(r.Context(), deviceID, credential.OrganisationID)
	if errors.Is(err, db.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "device_not_found", "Device not found.")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "device_lookup_error", "Could not load device.")
		return
	}
	if protocol != "api" {
		writeAPIError(w, http.StatusConflict, "protocol_mismatch", "Device is not configured for API check-in.")
		return
	}

	if err := s.store.TouchDeviceLastSeen(r.Context(), deviceID, received.UnixMilli()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "connectivity_error", "Could not update device connectivity.")
		return
	}

	observed, observationErr := parseAPIObservedAt(envelope.ObservedAt, received)
	telemetryPart, telemetryErr := prepareAPITelemetry(envelope.Data, deviceID, observed, received)
	if observationErr != nil {
		telemetryErr = observationErr
	}
	statusPart, statusErr := prepareAPITaskStatus(envelope.TaskStatus)

	// Valid components commit independently, even when the other component is invalid.
	if telemetryErr == nil && telemetryPart.present {
		event := domain.DeviceEvent{DeviceID: deviceID, TSReceivedMS: received.UnixMilli(), Protocol: "api", Direction: "inbound", Operation: "check-in", PayloadJSON: telemetryPart.payload, Source: "application-backend"}
		if _, err := s.store.RecordDeviceEvent(r.Context(), event, telemetryPart.properties); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "telemetry_error", "Could not record telemetry.")
			return
		}
	}
	if statusErr == nil && statusPart != nil {
		completedAt := ""
		if statusPart.Status == domain.TaskStatusSuccess || statusPart.Status == domain.TaskStatusFailure {
			completedAt = received.Format(time.RFC3339Nano)
		}
		outcome, err := s.store.ApplyDeviceTaskReport(r.Context(), statusPart.Task, deviceID, credential.OrganisationID, statusPart.Status, completedAt, statusPart.Msg)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "task_status_error", "Could not apply task status.")
			return
		}
		if outcome == db.TaskTransitionNotFound {
			statusErr = &componentError{http.StatusNotFound, "task_not_found", "Task not found."}
		}
	}
	if telemetryErr != nil {
		writeAPIError(w, telemetryErr.status, telemetryErr.code, telemetryErr.message)
		return
	}
	if statusErr != nil {
		writeAPIError(w, statusErr.status, statusErr.code, statusErr.message)
		return
	}

	task, err := s.store.AdvanceAndLoadPendingAPITask(r.Context(), deviceID, credential.OrganisationID, received)
	if errors.Is(err, db.ErrNotFound) {
		writeAPIJSON(w, http.StatusOK, apiCheckInResponse{Task: nil})
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "task_queue_error", "Could not load pending task.")
		return
	}
	projection, err := s.projectAPITask(task, credential.OrganisationID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "task_projection_error", "Could not project pending task.")
		return
	}
	writeAPIJSON(w, http.StatusOK, apiCheckInResponse{Task: &projection})
}

func decodeAPICheckInEnvelope(w http.ResponseWriter, r *http.Request) (apiCheckInEnvelope, error) {
	limited := http.MaxBytesReader(w, r.Body, apiCheckInBodyLimit)
	body, err := io.ReadAll(limited)
	if err != nil {
		return apiCheckInEnvelope{}, err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return apiCheckInEnvelope{}, errors.New("top-level value is not an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var envelope apiCheckInEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return envelope, err
	}
	return envelope, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func parseAPIObservedAt(raw json.RawMessage, received time.Time) (time.Time, *componentError) {
	if raw == nil {
		return received, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return time.Time{}, &componentError{http.StatusBadRequest, "invalid_observed_at", "observed_at must be an RFC 3339 timestamp."}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, &componentError{http.StatusBadRequest, "invalid_observed_at", "observed_at must be an RFC 3339 timestamp."}
	}
	if parsed.After(received.Add(time.Hour)) {
		return time.Time{}, &componentError{http.StatusBadRequest, "observed_at_in_future", "observed_at must not be more than one hour in the future."}
	}
	return parsed.UTC(), nil
}

func prepareAPITelemetry(raw json.RawMessage, deviceID string, observed, received time.Time) (preparedTelemetry, *componentError) {
	if raw == nil {
		return preparedTelemetry{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil || requireJSONEOF(decoder) != nil {
		return preparedTelemetry{}, &componentError{http.StatusBadRequest, "invalid_data", "data must be a JSON object."}
	}
	if len(object) == 0 {
		return preparedTelemetry{}, nil
	}
	flat, err := telemetry.Flatten(object)
	if errors.Is(err, telemetry.ErrDuplicatePath) {
		return preparedTelemetry{}, &componentError{http.StatusBadRequest, "duplicate_path", "Telemetry contains colliding flattened paths."}
	}
	if err != nil {
		return preparedTelemetry{}, &componentError{http.StatusBadRequest, "invalid_data", "data contains an unsupported value."}
	}
	payload, err := json.Marshal(object)
	if err != nil {
		return preparedTelemetry{}, &componentError{http.StatusBadRequest, "invalid_data", "data contains an unsupported value."}
	}
	properties := make([]domain.DeviceTwinProperty, 0, len(flat))
	for _, update := range flat {
		properties = append(properties, domain.DeviceTwinProperty{DeviceID: deviceID, Path: update.Path, ValueJSON: update.ValueJSON, ValueType: update.ValueType, TSObservedMS: observed.UnixMilli(), TSReceivedMS: received.UnixMilli(), Protocol: "api", SourcePath: "api-check-in"})
	}
	return preparedTelemetry{present: true, payload: string(payload), properties: properties}, nil
}

func prepareAPITaskStatus(raw json.RawMessage) (*apiTaskStatus, *componentError) {
	if raw == nil {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var status apiTaskStatus
	if err := decoder.Decode(&status); err != nil || requireJSONEOF(decoder) != nil {
		return nil, &componentError{http.StatusBadRequest, "invalid_task_status", "task_status must be a valid object with supported fields."}
	}
	if status.Task <= 0 {
		return nil, &componentError{http.StatusBadRequest, "invalid_task_id", "task_status.task must be a positive task id."}
	}
	if status.Status != domain.TaskStatusInProgress && status.Status != domain.TaskStatusSuccess && status.Status != domain.TaskStatusFailure {
		return nil, &componentError{http.StatusBadRequest, "invalid_task_status_value", "task_status.status is unsupported."}
	}
	if !utf8.ValidString(status.Msg) || len([]rune(status.Msg)) > 512 {
		return nil, &componentError{http.StatusBadRequest, "task_message_too_long", "task_status.msg must be at most 512 characters."}
	}
	return &status, nil
}

func (s *Server) projectAPITask(task domain.DeviceTask, organisationID int64) (apiTaskProjection, error) {
	createdAt, err := normalizeAPITaskTime(task.CreatedAt)
	if err != nil {
		return apiTaskProjection{}, err
	}
	expiresAt, err := normalizeAPITaskTime(task.ExpiresAt)
	if err != nil {
		return apiTaskProjection{}, err
	}
	out := apiTaskProjection{ID: task.ID, Type: task.Type, CreatedAt: createdAt, ExpiresAt: expiresAt}
	switch task.Type {
	case domain.TaskTypeRead:
		var params domain.ReadTaskParameters
		if err := decodeStoredTaskParameters(task.ParametersJSON, &params); err != nil || len(params.Paths) == 0 {
			return out, errors.New("invalid read parameters")
		}
		out.Parameters = params
	case domain.TaskTypeWrite:
		var stored domain.WriteTaskParameters
		if err := decodeStoredTaskParameters(task.ParametersJSON, &stored); err != nil || len(stored.Values) == 0 {
			return out, errors.New("invalid write parameters")
		}
		params := apiWriteParameters{Values: make([]apiWriteValue, 0, len(stored.Values))}
		for _, item := range stored.Values {
			if strings.TrimSpace(item.Path) == "" {
				return out, errors.New("invalid write path")
			}
			decoder := json.NewDecoder(bytes.NewReader(item.Value))
			decoder.UseNumber()
			var value any
			if err := decoder.Decode(&value); err != nil || requireJSONEOF(decoder) != nil {
				return out, errors.New("invalid write value")
			}
			params.Values = append(params.Values, apiWriteValue{Path: item.Path, Value: value})
		}
		out.Parameters = params
	case domain.TaskTypeFOTA:
		params, err := domain.ParseFOTATaskParameters(task.ParametersJSON)
		if err != nil {
			return out, err
		}
		base, err := url.Parse(s.fotaDownloadBaseURL)
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
			return out, errors.New("absolute FOTA base URL is required")
		}
		artifact := strings.TrimRight(s.fotaDownloadBaseURL, "/") + "/org/" + strconv.FormatInt(organisationID, 10) + "/releases/" + strconv.FormatInt(params.ReleaseID, 10) + "/binary"
		out.Parameters = apiFOTAParameters{URL: artifact}
	default:
		return out, errors.New("unsupported task type")
	}
	return out, nil
}

func normalizeAPITaskTime(value string) (string, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05Z07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format(time.RFC3339Nano), nil
		}
	}
	return "", errors.New("invalid task timestamp")
}

func decodeStoredTaskParameters(input string, output any) error {
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

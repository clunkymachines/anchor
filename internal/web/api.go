package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

type apiErrorResponse struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiBulkUpsertRequest struct {
	Devices []apiBulkUpsertDeviceRequest `json:"devices"`
}

type apiBulkUpsertDeviceRequest struct {
	ID               string                  `json:"id"`
	DeviceModelID    int64                   `json:"device_model_id"`
	MQTTUsername     string                  `json:"mqtt_username"`
	MQTTPassword     string                  `json:"mqtt_password"`
	SoftwareVersions domain.SoftwareVersions `json:"software_versions"`
	IsGateway        bool                    `json:"is_gateway"`
}

type apiBulkUpsertResponse struct {
	Results []apiBulkUpsertDeviceResult `json:"results"`
}

type apiBulkUpsertDeviceResult struct {
	ID           string        `json:"id"`
	Status       string        `json:"status,omitempty"`
	Created      bool          `json:"created,omitempty"`
	Updated      bool          `json:"updated,omitempty"`
	MQTTUsername string        `json:"mqtt_username,omitempty"`
	DataTopic    string        `json:"data_topic,omitempty"`
	TaskTopic    string        `json:"task_topic,omitempty"`
	Error        *apiErrorBody `json:"error,omitempty"`
}

func (s *Server) requireAPIAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) || strings.TrimSpace(strings.TrimPrefix(auth, prefix)) == "" {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Bearer token is required.")
			return
		}

		credential, err := s.store.AuthenticateOrganisationAPIToken(r.Context(), strings.TrimSpace(strings.TrimPrefix(auth, prefix)), time.Now())
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Bearer token is invalid or disabled.")
			return
		}

		ctx := contextWithAPICredential(r, credential)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func contextWithAPICredential(r *http.Request, credential domain.OrganisationAPICredential) context.Context {
	return context.WithValue(r.Context(), apiCredentialContextKey, credential)
}

func apiCredentialFromRequest(r *http.Request) (domain.OrganisationAPICredential, bool) {
	credential, ok := r.Context().Value(apiCredentialContextKey).(domain.OrganisationAPICredential)
	return credential, ok
}

func (s *Server) apiDeviceBulkUpsert(w http.ResponseWriter, r *http.Request) {
	credential, ok := apiCredentialFromRequest(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Bearer token is required.")
		return
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	var req apiBulkUpsertRequest
	if err := decoder.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON.")
		return
	}
	if len(req.Devices) == 0 {
		writeAPIError(w, http.StatusBadRequest, "empty_devices", "At least one device is required.")
		return
	}
	if len(req.Devices) > maxBulkUpsertDevices {
		writeAPIError(w, http.StatusBadRequest, "too_many_devices", "At most 2000 devices can be upserted at once.")
		return
	}

	seen := make(map[string]struct{}, len(req.Devices))
	results := make([]apiBulkUpsertDeviceResult, 0, len(req.Devices))
	successes := 0
	failures := 0
	for _, device := range req.Devices {
		result := s.apiUpsertOneDevice(r, credential.OrganisationID, device, seen)
		if result.Error != nil {
			failures++
		} else {
			successes++
		}
		results = append(results, result)
	}

	status := http.StatusOK
	if successes > 0 && failures > 0 {
		status = http.StatusMultiStatus
	}
	writeAPIJSON(w, status, apiBulkUpsertResponse{Results: results})
}

func (s *Server) apiUpsertOneDevice(r *http.Request, organisationID int64, req apiBulkUpsertDeviceRequest, seen map[string]struct{}) apiBulkUpsertDeviceResult {
	req.ID = strings.TrimSpace(req.ID)
	req.MQTTUsername = strings.TrimSpace(req.MQTTUsername)
	result := apiBulkUpsertDeviceResult{ID: req.ID, MQTTUsername: req.MQTTUsername}

	if req.ID == "" {
		result.Error = &apiErrorBody{Code: "device_id_required", Message: "Device id is required."}
		return result
	}
	if _, ok := seen[req.ID]; ok {
		result.Error = &apiErrorBody{Code: "duplicate_device_id", Message: "Device id appears more than once in this request."}
		return result
	}
	seen[req.ID] = struct{}{}
	if req.DeviceModelID <= 0 {
		result.Error = &apiErrorBody{Code: "device_model_required", Message: "device_model_id is required."}
		return result
	}
	if req.MQTTUsername == "" {
		result.Error = &apiErrorBody{Code: "mqtt_username_required", Message: "mqtt_username is required."}
		return result
	}
	if req.MQTTPassword == "" {
		result.Error = &apiErrorBody{Code: "mqtt_password_required", Message: "mqtt_password is required."}
		return result
	}
	if _, err := s.store.DeviceModel(r.Context(), req.DeviceModelID, organisationID); errors.Is(err, db.ErrNotFound) {
		result.Error = &apiErrorBody{Code: "device_model_not_found", Message: "device_model_id does not belong to this organisation."}
		return result
	} else if err != nil {
		result.Error = &apiErrorBody{Code: "device_model_error", Message: "Could not validate device model."}
		return result
	}

	existingOrganisationID, err := s.store.DeviceOrganisationID(r.Context(), req.ID)
	created := errors.Is(err, db.ErrNotFound)
	if err != nil && !created {
		result.Error = &apiErrorBody{Code: "device_lookup_error", Message: "Could not validate device ownership."}
		return result
	}
	if !created && existingOrganisationID != organisationID {
		result.Error = &apiErrorBody{Code: "device_id_conflict", Message: "Device id already belongs to another organisation."}
		return result
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.MQTTPassword), bcrypt.DefaultCost)
	if err != nil {
		result.Error = &apiErrorBody{Code: "credential_error", Message: "Could not hash MQTT credential."}
		return result
	}
	if req.SoftwareVersions == nil {
		req.SoftwareVersions = domain.SoftwareVersions{}
	}
	if err := s.store.SaveDeviceWithMQTTCredential(r.Context(), domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               req.ID,
			OrganisationID:   organisationID,
			DeviceModelID:    req.DeviceModelID,
			SoftwareVersions: req.SoftwareVersions,
			IsGateway:        req.IsGateway,
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     req.ID,
			Username:     req.MQTTUsername,
			PasswordHash: string(passwordHash),
			Enabled:      true,
		},
	}); err != nil {
		result.Error = &apiErrorBody{Code: "device_upsert_error", Message: "Could not upsert device."}
		return result
	}

	result.Status = "updated"
	result.Updated = true
	if created {
		result.Status = "created"
		result.Created = true
		result.Updated = false
	}
	result.DataTopic = mqttDataTopic(organisationID, req.ID)
	result.TaskTopic = mqttTaskTopic(organisationID, req.ID)
	return result
}

func writeAPIError(w http.ResponseWriter, status int, code string, message string) {
	writeAPIJSON(w, status, apiErrorResponse{Error: apiErrorBody{Code: code, Message: message}})
}

func writeAPIJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

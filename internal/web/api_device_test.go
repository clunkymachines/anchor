package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"
)

func TestAPIDeviceCheckInTelemetryTasksAndPartialCommit(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{Dialect: db.DialectSQLite, DSN: filepath.Join(t.TempDir(), "anchor.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	orgID, _ := store.CreateOrganisation(ctx, domain.Organisation{Name: "API org"})
	modelID, err := store.CreateDeviceModel(ctx, domain.DeviceModel{OrganisationID: orgID, Name: "API model", ExpectedHeartbeatSeconds: 60, ExpectedProtocol: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDevice(ctx, domain.Device{ID: "relay-1", OrganisationID: orgID, DeviceModelID: modelID, SoftwareVersions: domain.SoftwareVersions{}}); err != nil {
		t.Fatal(err)
	}
	credential, err := store.CreateOrganisationAPICredential(ctx, orgID, "backend")
	if err != nil {
		t.Fatal(err)
	}
	handler := apiDeviceTestHandler(store)

	res := apiCheckInRequest(t, handler, credential.Token, "relay-1", `{"data":{"battery":{"percent":82},"firmware":{"version":"2.0"}},"task_status":{"task":1,"status":"success","extra":true}}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("partial check-in status=%d body=%s", res.Code, res.Body.String())
	}
	props, err := store.ListDeviceTwinProperties(ctx, "relay-1", orgID)
	if err != nil || len(props) != 2 {
		t.Fatalf("telemetry was not committed: %#v err=%v", props, err)
	}
	events, _ := store.ListRecentDeviceEvents(ctx, "relay-1", orgID, 10)
	if len(events) != 1 || events[0].Protocol != "api" || events[0].PayloadJSON == "" {
		t.Fatalf("unexpected events: %#v", events)
	}
	res = apiCheckInRequest(t, handler, credential.Token, "relay-1", `{"observed_at":"2000-01-01T00:00:00Z","data":{"firmware":{"version":"1.0"}}}`)
	if res.Code != http.StatusOK {
		t.Fatalf("stale telemetry status=%d body=%s", res.Code, res.Body.String())
	}
	props, _ = store.ListDeviceTwinProperties(ctx, "relay-1", orgID)
	for _, property := range props {
		if property.Path == "firmware.version" && property.ValueJSON != `"2.0"` {
			t.Fatalf("stale firmware replaced twin: %#v", property)
		}
	}
	detail, _ := store.DeviceDetail(ctx, "relay-1", orgID)
	if detail.Device.SoftwareVersions["firmware"] != "2.0" {
		t.Fatalf("stale firmware replaced denormalized value: %#v", detail.Device.SoftwareVersions)
	}

	readJSON, _ := domain.BuildReadTaskParameters([]string{"battery.percent"})
	first, err := store.CreateQueuedDeviceTask(ctx, orgID, db.CreateDeviceTaskOptions{Task: domain.DeviceTask{DeviceID: "relay-1", Type: domain.TaskTypeRead, ParametersJSON: readJSON}, CreatedTime: time.Now().UTC(), TTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	writeJSON, _ := domain.BuildWriteTaskParameters(`{"values":[{"path":"interval","value":60}]}`)
	second, err := store.CreateQueuedDeviceTask(ctx, orgID, db.CreateDeviceTaskOptions{Task: domain.DeviceTask{DeviceID: "relay-1", Type: domain.TaskTypeWrite, ParametersJSON: writeJSON}, CreatedTime: time.Now().UTC().Add(time.Millisecond), TTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}

	res = apiCheckInRequest(t, handler, credential.Token, "relay-1", `{}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":`+strconv.FormatInt(first.ID, 10)) {
		t.Fatalf("poll status=%d body=%s", res.Code, res.Body.String())
	}
	res = apiCheckInRequest(t, handler, credential.Token, "relay-1", `{"data":[],"task_status":{"task":`+strconv.FormatInt(first.ID, 10)+`,"status":"success","msg":"done"}}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("mixed status=%d body=%s", res.Code, res.Body.String())
	}
	finished, err := store.DeviceTaskForDevice(ctx, first.ID, "relay-1", orgID)
	if err != nil || finished.Status != domain.TaskStatusSuccess {
		t.Fatalf("valid task report not committed: %#v err=%v", finished, err)
	}

	res = apiCheckInRequest(t, handler, credential.Token, "relay-1", `{}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":`+strconv.FormatInt(second.ID, 10)) || !strings.Contains(res.Body.String(), `"value":60`) {
		t.Fatalf("next task status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAPIDeviceCheckInStrictEnvelopeAndProtocol(t *testing.T) {
	ctx := context.Background()
	store, orgID, mqttModelID := testAPIStore(t, ctx)
	defer store.Close()
	credential, _ := store.CreateOrganisationAPICredential(ctx, orgID, "backend")
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{Device: domain.Device{ID: "mqtt-1", OrganisationID: orgID, DeviceModelID: mqttModelID, SoftwareVersions: domain.SoftwareVersions{}}, Credential: domain.DeviceMQTTCredential{DeviceID: "mqtt-1", Username: "mqtt-1", PasswordHash: "hash", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	handler := apiDeviceTestHandler(store)

	res := apiCheckInRequest(t, handler, credential.Token, "mqtt-1", `{}`)
	if res.Code != http.StatusConflict {
		t.Fatalf("protocol mismatch=%d body=%s", res.Code, res.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/mqtt-1/check-in", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+credential.Token)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("media type=%d body=%s", res.Code, res.Body.String())
	}

	apiModelID, _ := store.CreateDeviceModel(ctx, domain.DeviceModel{OrganisationID: orgID, Name: "API", ExpectedHeartbeatSeconds: 60, ExpectedProtocol: "api"})
	_ = store.SaveDevice(ctx, domain.Device{ID: "api-1", OrganisationID: orgID, DeviceModelID: apiModelID, SoftwareVersions: domain.SoftwareVersions{}})
	res = apiCheckInRequest(t, handler, credential.Token, "api-1", `{"unknown":true}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unknown envelope=%d body=%s", res.Code, res.Body.String())
	}
	detail, _ := store.DeviceDetail(ctx, "api-1", orgID)
	if detail.Device.LastSeenMS != 0 {
		t.Fatalf("invalid envelope mutated last_seen: %d", detail.Device.LastSeenMS)
	}
}

func apiCheckInRequest(t *testing.T, handler http.Handler, token, deviceID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/check-in", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	var decoded any
	if strings.HasPrefix(res.Header().Get("Content-Type"), "application/json") && json.Unmarshal(res.Body.Bytes(), &decoded) != nil {
		t.Fatalf("invalid response JSON: %s", res.Body.String())
	}
	return res
}

func apiDeviceTestHandler(store *db.Store) http.Handler {
	server := &Server{store: store}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/devices/{deviceID}/check-in", server.requireAPIAuth(http.HandlerFunc(server.apiDeviceCheckIn)))
	return mux
}

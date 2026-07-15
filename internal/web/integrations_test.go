package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"anchor/internal/db"
	"anchor/internal/domain"
)

type recordingMQTTIntegrationRuntime struct {
	applied []domain.MQTTIntegrationConfig
	status  domain.MQTTIntegrationStatus
}

func (r *recordingMQTTIntegrationRuntime) ApplyMQTTIntegration(_ context.Context, config domain.MQTTIntegrationConfig) error {
	r.applied = append(r.applied, config)
	return nil
}

func (r *recordingMQTTIntegrationRuntime) InternalMQTTCredentials() (string, string, bool) {
	if len(r.applied) == 0 {
		return "", "", false
	}
	config := r.applied[len(r.applied)-1]
	return config.Username, config.Password, config.Enabled
}

func (r *recordingMQTTIntegrationRuntime) MQTTIntegrationStatus() domain.MQTTIntegrationStatus {
	if r.status.State != "" {
		return r.status
	}
	return domain.MQTTIntegrationStatus{State: domain.MQTTConnectionDisabled, Reason: "The integration is inactive."}
}

func (r *recordingMQTTIntegrationRuntime) PublishDeviceTask(context.Context, domain.DeviceTask, int64) error {
	return nil
}

func (r *recordingMQTTIntegrationRuntime) PublishPendingDeviceTasks(context.Context, string, int64) error {
	return nil
}

func TestAnchorAdminCanConfigureMQTTIntegration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{Dialect: db.DialectSQLite, DSN: filepath.Join(t.TempDir(), "anchor.db")})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	admin := domain.User{Email: "admin@example.com", Name: "Admin", PasswordHash: "hash", IsAdmin: true}
	admin.ID, err = store.CreateUser(ctx, admin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	organisationID, err := store.CreateOrganisationForUser(ctx, domain.Organisation{Name: "Test Org"}, admin.ID)
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}

	runtime := &recordingMQTTIntegrationRuntime{}
	server := testServerWithTemplates(t, store)
	server.mqttIntegrationRuntime = runtime

	values := url.Values{
		"enabled":    {"1"},
		"broker_url": {"mqtt://broker.example.com:1883"},
		"client_id":  {"anchor-ingest"},
		"username":   {"anchor-service"},
		"password":   {"secret"},
		"qos":        {"1"},
	}
	req := formRequest(http.MethodPost, "/integrations/mqtt?organisation_id="+strconv.FormatInt(organisationID, 10), values, admin)
	res := httptest.NewRecorder()
	server.mqttIntegrationPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after save, got %d body=%q", res.Code, res.Body.String())
	}
	if len(runtime.applied) != 1 {
		t.Fatalf("expected runtime apply, got %#v", runtime.applied)
	}

	config, err := store.MQTTIntegration(ctx)
	if err != nil {
		t.Fatalf("load mqtt integration: %v", err)
	}
	if !config.Enabled || config.BrokerURL != values.Get("broker_url") || config.Password != "secret" || config.QoS != 1 {
		t.Fatalf("unexpected mqtt integration: %#v", config)
	}

	values.Del("password")
	values.Del("enabled")
	values.Set("qos", "2")
	req = formRequest(http.MethodPost, "/integrations/mqtt?organisation_id="+strconv.FormatInt(organisationID, 10), values, admin)
	res = httptest.NewRecorder()
	server.mqttIntegrationPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after update, got %d body=%q", res.Code, res.Body.String())
	}
	config, err = store.MQTTIntegration(ctx)
	if err != nil {
		t.Fatalf("reload mqtt integration: %v", err)
	}
	if config.Enabled || config.Password != "secret" || config.QoS != 2 {
		t.Fatalf("expected disable with retained password, got %#v", config)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/integrations?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), userContextKey, admin))
	getRes := httptest.NewRecorder()
	server.integrations(getRes, getReq)
	if getRes.Code != http.StatusOK {
		t.Fatalf("expected integrations page, got %d body=%q", getRes.Code, getRes.Body.String())
	}
	body := getRes.Body.String()
	if !strings.Contains(body, "MQTT with Mosquitto") || !strings.Contains(body, "Integrations") {
		t.Fatalf("expected integrations content, got %q", body)
	}
	if strings.Contains(body, "secret") {
		t.Fatal("expected saved password not to be rendered")
	}
}

func TestMQTTIntegrationRequiresAnchorAdminAndValidConfiguration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{Dialect: db.DialectSQLite, DSN: filepath.Join(t.TempDir(), "anchor.db")})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	member := domain.User{Email: "member@example.com", Name: "Member", PasswordHash: "hash"}
	member.ID, err = store.CreateUser(ctx, member)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	runtime := &recordingMQTTIntegrationRuntime{}
	server := testServerWithTemplates(t, store)
	server.mqttIntegrationRuntime = runtime

	getReq := httptest.NewRequest(http.MethodGet, "/integrations", nil)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), userContextKey, member))
	getRes := httptest.NewRecorder()
	server.integrations(getRes, getReq)
	if getRes.Code != http.StatusForbidden {
		t.Fatalf("expected member to be forbidden, got %d", getRes.Code)
	}

	admin := member
	admin.IsAdmin = true
	invalidReq := formRequest(http.MethodPost, "/integrations/mqtt", url.Values{
		"broker_url": {"http://broker.example.com"},
		"client_id":  {"anchor-ingest"},
		"username":   {"anchor-ingest"},
		"password":   {"secret"},
		"qos":        {"0"},
	}, admin)
	invalidRes := httptest.NewRecorder()
	server.mqttIntegrationPost(invalidRes, invalidReq)
	if invalidRes.Code != http.StatusOK || !strings.Contains(invalidRes.Body.String(), "mqtt, mqtts, ws, or wss") {
		t.Fatalf("expected broker validation error, got %d body=%q", invalidRes.Code, invalidRes.Body.String())
	}
	if len(runtime.applied) != 0 {
		t.Fatalf("expected invalid configuration not to apply, got %#v", runtime.applied)
	}
}

func TestMQTTIntegrationStatusShowsBrokerFailureReason(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{Dialect: db.DialectSQLite, DSN: filepath.Join(t.TempDir(), "anchor.db")})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	admin := domain.User{Email: "admin@example.com", Name: "Admin", PasswordHash: "hash", IsAdmin: true}
	admin.ID, err = store.CreateUser(ctx, admin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := store.SaveMQTTIntegration(ctx, domain.MQTTIntegrationConfig{
		Enabled: true, BrokerURL: "mqtt://broker.example.com:1883", ClientID: "anchor-ingest",
		Username: "anchor-ingest", Password: "secret", QoS: 0,
	}); err != nil {
		t.Fatalf("save mqtt integration: %v", err)
	}

	runtime := &recordingMQTTIntegrationRuntime{status: domain.MQTTIntegrationStatus{
		State:     domain.MQTTConnectionFailed,
		Reason:    "connection refused",
		UpdatedAt: "2026-07-15T08:00:00Z",
	}}
	server := testServerWithTemplates(t, store)
	server.mqttIntegrationRuntime = runtime

	req := httptest.NewRequest(http.MethodGet, "/integrations/mqtt/status", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, admin))
	res := httptest.NewRecorder()
	server.mqttIntegrationStatus(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected status response, got %d body=%q", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, expected := range []string{"Connection failed", "connection refused", "mqtt-integration-status"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected status response to contain %q, got %q", expected, body)
		}
	}
}

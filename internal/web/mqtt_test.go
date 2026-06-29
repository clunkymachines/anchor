package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

func TestMQTTAuthAndACL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{
		Dialect: db.DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	passwordHash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}

	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-001",
			OrganisationID:   organisationID,
			DeviceModelID:    testDeviceModelID(t, store, organisationID, "Gateway"),
			ModelName:        "Gateway",
			SoftwareVersions: domain.SoftwareVersions{},
			IsGateway:        true,
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-001",
			Username:     "device-001",
			PasswordHash: string(passwordHash),
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save device with mqtt credential: %v", err)
	}
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-002",
			OrganisationID:   organisationID,
			DeviceModelID:    testDeviceModelID(t, store, organisationID, "Sensor"),
			ModelName:        "Sensor",
			SoftwareVersions: domain.SoftwareVersions{},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-002",
			Username:     "device-002",
			PasswordHash: string(passwordHash),
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save second device with mqtt credential: %v", err)
	}

	publisher := newRecordingTaskPublisher()
	server := &Server{store: store, taskPublisher: publisher}

	authReq := httptest.NewRequest(http.MethodPost, "/mqtt/auth", bytes.NewBufferString(`{"username":"device-001","password":"secret","clientid":"client-001"}`))
	authReq.Header.Set("Content-Type", "application/json")
	authRes := httptest.NewRecorder()
	server.mqttAuth(authRes, authReq)
	if authRes.Code != http.StatusNoContent {
		t.Fatalf("expected auth success, got %d", authRes.Code)
	}

	badAuthReq := httptest.NewRequest(http.MethodPost, "/mqtt/auth", bytes.NewBufferString(`{"username":"device-001","password":"wrong","clientid":"client-001"}`))
	badAuthReq.Header.Set("Content-Type", "application/json")
	badAuthRes := httptest.NewRecorder()
	server.mqttAuth(badAuthRes, badAuthReq)
	if badAuthRes.Code != http.StatusUnauthorized {
		t.Fatalf("expected auth failure, got %d", badAuthRes.Code)
	}

	writeACLReq := httptest.NewRequest(http.MethodPost, "/mqtt/acl", bytes.NewBufferString(`{"username":"device-001","clientid":"client-001","topic":"dev/`+strconv.FormatInt(organisationID, 10)+`/device-001/data","acc":2}`))
	writeACLReq.Header.Set("Content-Type", "application/json")
	writeACLRes := httptest.NewRecorder()
	server.mqttACL(writeACLRes, writeACLReq)
	if writeACLRes.Code != http.StatusNoContent {
		t.Fatalf("expected write acl success, got %d", writeACLRes.Code)
	}

	gatewayACLReq := httptest.NewRequest(http.MethodPost, "/mqtt/acl", bytes.NewBufferString(`{"username":"device-001","clientid":"client-001","topic":"dev/`+strconv.FormatInt(organisationID, 10)+`/device-002/data","acc":2}`))
	gatewayACLReq.Header.Set("Content-Type", "application/json")
	gatewayACLRes := httptest.NewRecorder()
	server.mqttACL(gatewayACLRes, gatewayACLReq)
	if gatewayACLRes.Code != http.StatusNoContent {
		t.Fatalf("expected gateway write acl success, got %d", gatewayACLRes.Code)
	}

	deniedACLReq := httptest.NewRequest(http.MethodPost, "/mqtt/acl", bytes.NewBufferString(`{"username":"device-002","clientid":"client-002","topic":"dev/`+strconv.FormatInt(organisationID, 10)+`/device-001/data","acc":2}`))
	deniedACLReq.Header.Set("Content-Type", "application/json")
	deniedACLRes := httptest.NewRecorder()
	server.mqttACL(deniedACLRes, deniedACLReq)
	if deniedACLRes.Code != http.StatusUnauthorized {
		t.Fatalf("expected acl failure, got %d", deniedACLRes.Code)
	}

	subscribeACLReq := httptest.NewRequest(http.MethodPost, "/mqtt/acl", bytes.NewBufferString(`username=device-001&clientid=client-001&topic=dev/`+strconv.FormatInt(organisationID, 10)+`/device-001/task&acc=4`))
	subscribeACLReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	subscribeACLRes := httptest.NewRecorder()
	server.mqttACL(subscribeACLRes, subscribeACLReq)
	if subscribeACLRes.Code != http.StatusNoContent {
		t.Fatalf("expected subscribe acl success, got %d", subscribeACLRes.Code)
	}

	select {
	case pending := <-publisher.pending:
		if pending.organisationID != organisationID || pending.deviceID != "device-001" {
			t.Fatalf("unexpected pending publish: %#v", pending)
		}
	case <-time.After(time.Second):
		t.Fatal("expected pending tasks to be published after task subscribe")
	}
}

func TestInternalMQTTClientAuthAndACL(t *testing.T) {
	t.Parallel()

	server := &Server{
		internalMQTTClientAuth: InternalMQTTClientAuthConfig{
			Username: "anchor-ingest",
			Password: "secret",
		},
	}

	authReq := httptest.NewRequest(http.MethodPost, "/mqtt/auth", bytes.NewBufferString(`{"username":"anchor-ingest","password":"secret","clientid":"anchor-ingest"}`))
	authReq.Header.Set("Content-Type", "application/json")
	authRes := httptest.NewRecorder()
	server.mqttAuth(authRes, authReq)
	if authRes.Code != http.StatusNoContent {
		t.Fatalf("expected mqtt client auth success, got %d", authRes.Code)
	}

	subscribeReq := httptest.NewRequest(http.MethodPost, "/mqtt/acl", bytes.NewBufferString(`{"username":"anchor-ingest","clientid":"anchor-ingest","topic":"dev/+/+/data","acc":4}`))
	subscribeReq.Header.Set("Content-Type", "application/json")
	subscribeRes := httptest.NewRecorder()
	server.mqttACL(subscribeRes, subscribeReq)
	if subscribeRes.Code != http.StatusNoContent {
		t.Fatalf("expected mqtt client subscribe success, got %d", subscribeRes.Code)
	}

	readReq := httptest.NewRequest(http.MethodPost, "/mqtt/acl", bytes.NewBufferString(`{"username":"anchor-ingest","clientid":"anchor-ingest","topic":"dev/42/device-001/data","acc":1}`))
	readReq.Header.Set("Content-Type", "application/json")
	readRes := httptest.NewRecorder()
	server.mqttACL(readRes, readReq)
	if readRes.Code != http.StatusNoContent {
		t.Fatalf("expected mqtt client read success, got %d", readRes.Code)
	}

	taskWriteReq := httptest.NewRequest(http.MethodPost, "/mqtt/acl", bytes.NewBufferString(`{"username":"anchor-ingest","clientid":"anchor-ingest","topic":"dev/42/device-001/task","acc":2}`))
	taskWriteReq.Header.Set("Content-Type", "application/json")
	taskWriteRes := httptest.NewRecorder()
	server.mqttACL(taskWriteRes, taskWriteReq)
	if taskWriteRes.Code != http.StatusNoContent {
		t.Fatalf("expected mqtt client task write success, got %d", taskWriteRes.Code)
	}

	dataWriteReq := httptest.NewRequest(http.MethodPost, "/mqtt/acl", bytes.NewBufferString(`{"username":"anchor-ingest","clientid":"anchor-ingest","topic":"dev/42/device-001/data","acc":2}`))
	dataWriteReq.Header.Set("Content-Type", "application/json")
	dataWriteRes := httptest.NewRecorder()
	server.mqttACL(dataWriteRes, dataWriteReq)
	if dataWriteRes.Code != http.StatusUnauthorized {
		t.Fatalf("expected mqtt client data write denial, got %d", dataWriteRes.Code)
	}
}

func TestParseMQTTTopics(t *testing.T) {
	t.Parallel()

	organisationID, deviceID, ok := parseMQTTDataTopic("dev/42/device-001/data")
	if !ok || organisationID != 42 || deviceID != "device-001" {
		t.Fatalf("unexpected data topic parse: org=%d device=%q ok=%v", organisationID, deviceID, ok)
	}
	if _, _, ok := parseMQTTDataTopic("dev/42/device-001/data/temp"); ok {
		t.Fatal("expected nested data topic to be rejected")
	}

	taskOrganisationID, taskDeviceID, ok := parseMQTTTaskTopic("dev/42/device-001/task")
	if !ok || taskOrganisationID != 42 || taskDeviceID != "device-001" {
		t.Fatalf("unexpected task topic parse: org=%d device=%q ok=%v", taskOrganisationID, taskDeviceID, ok)
	}
	if _, _, ok := parseMQTTTaskTopic("dev/device-001/task"); ok {
		t.Fatal("expected org-less task topic to be rejected")
	}
}

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"anchor/internal/db"
	"anchor/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

func TestAPIBulkUpsertAuthAndPartialSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, organisationID, modelID := testAPIStore(t, ctx)
	defer store.Close()

	credential, err := store.CreateOrganisationAPICredential(ctx, organisationID, "Simulator")
	if err != nil {
		t.Fatalf("create api credential: %v", err)
	}
	server := &Server{store: store}
	handler := server.requireAPIAuth(http.HandlerFunc(server.apiDeviceBulkUpsert))

	missingReq := httptest.NewRequest(http.MethodPost, "/api/v1/devices/bulk-upsert", strings.NewReader(`{"devices":[]}`))
	missingRes := httptest.NewRecorder()
	handler.ServeHTTP(missingRes, missingReq)
	if missingRes.Code != http.StatusUnauthorized {
		t.Fatalf("expected missing bearer to be unauthorized, got %d", missingRes.Code)
	}

	reqBody := `{"devices":[
		{"id":"sim-1","device_model_id":` + strconv.FormatInt(modelID, 10) + `,"mqtt_username":"sim-1","mqtt_password":"first","software_versions":{"firmware":"1.0.0"}},
		{"id":"sim-bad","device_model_id":999999,"mqtt_username":"sim-bad","mqtt_password":"bad"}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/bulk-upsert", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+credential.Token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusMultiStatus {
		t.Fatalf("expected mixed partial status 207, got %d body=%s", res.Code, res.Body.String())
	}

	var decoded apiBulkUpsertResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.Results) != 2 || decoded.Results[0].Status != "created" || decoded.Results[1].Error == nil {
		t.Fatalf("unexpected partial response: %#v", decoded.Results)
	}

	detail, err := store.DeviceDetail(ctx, "sim-1", organisationID)
	if err != nil {
		t.Fatalf("read upserted device: %v", err)
	}
	if detail.Device.SoftwareVersions["firmware"] != "1.0.0" {
		t.Fatalf("expected firmware version to be stored, got %#v", detail.Device.SoftwareVersions)
	}
}

func TestAPIBulkUpsertDuplicateAndMQTTPasswordUpdate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, organisationID, modelID := testAPIStore(t, ctx)
	defer store.Close()

	credential, err := store.CreateOrganisationAPICredential(ctx, organisationID, "Simulator")
	if err != nil {
		t.Fatalf("create api credential: %v", err)
	}
	server := &Server{store: store}
	handler := server.requireAPIAuth(http.HandlerFunc(server.apiDeviceBulkUpsert))

	first := `{"devices":[{"id":"sim-1","device_model_id":` + strconv.FormatInt(modelID, 10) + `,"mqtt_username":"sim-1","mqtt_password":"first"}]}`
	res := doAPIRequest(t, handler, credential.Token, first)
	if res.Code != http.StatusOK {
		t.Fatalf("expected first upsert OK, got %d body=%s", res.Code, res.Body.String())
	}

	second := `{"devices":[
		{"id":"sim-1","device_model_id":` + strconv.FormatInt(modelID, 10) + `,"mqtt_username":"sim-1","mqtt_password":"second"},
		{"id":"sim-1","device_model_id":` + strconv.FormatInt(modelID, 10) + `,"mqtt_username":"sim-dup","mqtt_password":"dup"}
	]}`
	res = doAPIRequest(t, handler, credential.Token, second)
	if res.Code != http.StatusMultiStatus {
		t.Fatalf("expected duplicate mixed status 207, got %d body=%s", res.Code, res.Body.String())
	}
	var decoded apiBulkUpsertResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Results[0].Status != "updated" || decoded.Results[1].Error == nil || decoded.Results[1].Error.Code != "duplicate_device_id" {
		t.Fatalf("unexpected duplicate response: %#v", decoded.Results)
	}

	mqttCredential, err := store.FindMQTTCredentialByUsername(ctx, "sim-1")
	if err != nil {
		t.Fatalf("find mqtt credential: %v", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(mqttCredential.PasswordHash), []byte("second")) != nil {
		t.Fatal("expected MQTT password hash to be replaced on upsert")
	}
}

func TestAPISingleUpsertTagsReplacePreserveClearAndNormalize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, organisationID, modelID := testAPIStore(t, ctx)
	defer store.Close()
	credential, err := store.CreateOrganisationAPICredential(ctx, organisationID, "Tags")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: store}
	handler := server.requireAPIAuth(http.HandlerFunc(server.apiDeviceUpsert))
	do := func(body string) (apiBulkUpsertDeviceResult, int) {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/devices/tagged", strings.NewReader(body))
		req.SetPathValue("deviceID", "tagged")
		req.Header.Set("Authorization", "Bearer "+credential.Token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var result apiBulkUpsertDeviceResult
		_ = json.NewDecoder(res.Body).Decode(&result)
		return result, res.Code
	}
	base := `"device_model_id":` + strconv.FormatInt(modelID, 10) + `,"mqtt_username":"tagged","mqtt_password":"secret"`
	result, status := do(`{` + base + `,"tags":[" Beta ","factory.floor"]}`)
	if status != http.StatusOK || result.Tags == nil || !reflect.DeepEqual(*result.Tags, []string{"beta", "factory.floor"}) {
		t.Fatalf("create status=%d result=%+v", status, result)
	}
	result, status = do(`{` + base + `}`)
	if status != http.StatusOK || result.Tags == nil || !reflect.DeepEqual(*result.Tags, []string{"beta", "factory.floor"}) {
		t.Fatalf("preserve status=%d result=%+v", status, result)
	}
	_, status = do(`{` + base + `,"tags":["beta","BETA"]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate status=%d", status)
	}
	got, _ := store.DeviceTags(ctx, "tagged", organisationID)
	if !reflect.DeepEqual(got, []string{"beta", "factory.floor"}) {
		t.Fatalf("invalid update changed tags: %v", got)
	}
	result, status = do(`{` + base + `,"tags":[]}`)
	if status != http.StatusOK || result.Tags == nil || len(*result.Tags) != 0 {
		t.Fatalf("clear status=%d result=%+v", status, result)
	}
}

func TestAPIBulkUpsertRejectsForeignDeviceID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, organisationID, modelID := testAPIStore(t, ctx)
	defer store.Close()
	otherOrgID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Other"})
	if err != nil {
		t.Fatalf("create other org: %v", err)
	}
	otherModelID := testDeviceModelID(t, store, otherOrgID, "Other Model")
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device:     domain.Device{ID: "shared-id", OrganisationID: otherOrgID, DeviceModelID: otherModelID, SoftwareVersions: domain.SoftwareVersions{}},
		Credential: domain.DeviceMQTTCredential{DeviceID: "shared-id", Username: "shared-id", PasswordHash: "hash", Enabled: true},
	}); err != nil {
		t.Fatalf("save foreign device: %v", err)
	}

	credential, err := store.CreateOrganisationAPICredential(ctx, organisationID, "Simulator")
	if err != nil {
		t.Fatalf("create api credential: %v", err)
	}
	server := &Server{store: store}
	handler := server.requireAPIAuth(http.HandlerFunc(server.apiDeviceBulkUpsert))
	body := `{"devices":[{"id":"shared-id","device_model_id":` + strconv.FormatInt(modelID, 10) + `,"mqtt_username":"shared-id-new","mqtt_password":"secret"}]}`
	res := doAPIRequest(t, handler, credential.Token, body)
	if res.Code != http.StatusOK {
		t.Fatalf("expected all-row validation response to be OK, got %d body=%s", res.Code, res.Body.String())
	}
	var decoded apiBulkUpsertResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.Results) != 1 || decoded.Results[0].Error == nil || decoded.Results[0].Error.Code != "device_id_conflict" {
		t.Fatalf("unexpected foreign device response: %#v", decoded.Results)
	}
}

func TestOrganisationAPICredentialUIRequiresAdmin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{Dialect: db.DialectSQLite, DSN: filepath.Join(t.TempDir(), "anchor.db")})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	adminID, err := store.CreateUser(ctx, domain.User{Email: "admin@example.test", Name: "Admin", PasswordHash: "hash"})
	if err != nil {
		t.Fatalf("create admin user: %v", err)
	}
	memberID, err := store.CreateUser(ctx, domain.User{Email: "member@example.test", Name: "Member", PasswordHash: "hash"})
	if err != nil {
		t.Fatalf("create member user: %v", err)
	}
	organisationID, err := store.CreateOrganisationForUser(ctx, domain.Organisation{Name: "Org"}, adminID)
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{UserID: memberID, OrganisationID: organisationID, Role: db.OrganisationRoleMember}); err != nil {
		t.Fatalf("add member: %v", err)
	}

	server := &Server{store: store}
	form := "organisation_id=" + strconv.FormatInt(organisationID, 10) + "&name=Simulator"
	req := httptest.NewRequest(http.MethodPost, "/organisations/api-credentials", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, domain.User{ID: memberID}))
	res := httptest.NewRecorder()
	server.organisationAPICredentialsPost(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("expected member to be forbidden, got %d", res.Code)
	}
}

func testAPIStore(t *testing.T, ctx context.Context) (*db.Store, int64, int64) {
	t.Helper()
	store, err := db.Open(ctx, db.Config{Dialect: db.DialectSQLite, DSN: filepath.Join(t.TempDir(), "anchor.db")})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	modelID := testDeviceModelID(t, store, organisationID, "Simulator")
	return store, organisationID, modelID
}

func doAPIRequest(t *testing.T, handler http.Handler, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/bulk-upsert", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

package web

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"
)

func TestDeviceHandlersRejectOrganisationsOutsideUserMembership(t *testing.T) {
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

	allowedOrgID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Allowed"})
	if err != nil {
		t.Fatalf("create allowed org: %v", err)
	}
	forbiddenOrgID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Forbidden"})
	if err != nil {
		t.Fatalf("create forbidden org: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: allowedOrgID,
	}); err != nil {
		t.Fatalf("add user to allowed org: %v", err)
	}
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "forbidden-device",
			OrganisationID:   forbiddenOrgID,
			DeviceModelID:    testDeviceModelID(t, store, forbiddenOrgID, "Sensor"),
			ModelName:        "Sensor",
			SoftwareVersions: domain.SoftwareVersions{},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "forbidden-device",
			Username:     "forbidden-device",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save forbidden device: %v", err)
	}

	server := &Server{store: store}
	user := domain.User{ID: userID, Email: "member@example.com"}

	detailReq := httptest.NewRequest(http.MethodGet, "/devices/forbidden-device?organisation_id="+strconv.FormatInt(forbiddenOrgID, 10), nil)
	detailReq.SetPathValue("deviceID", "forbidden-device")
	detailReq = detailReq.WithContext(context.WithValue(detailReq.Context(), userContextKey, user))
	detailRes := httptest.NewRecorder()
	server.deviceDetail(detailRes, detailReq)
	if detailRes.Code != http.StatusNotFound {
		t.Fatalf("expected forbidden device detail to be hidden, got %d", detailRes.Code)
	}

	form := url.Values{
		"organisation_id": {strconv.FormatInt(forbiddenOrgID, 10)},
		"task_type":       {"read"},
		"read_paths":      {"battery"},
	}
	taskReq := httptest.NewRequest(http.MethodPost, "/devices/forbidden-device/tasks", strings.NewReader(form.Encode()))
	taskReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	taskReq.SetPathValue("deviceID", "forbidden-device")
	taskReq = taskReq.WithContext(context.WithValue(taskReq.Context(), userContextKey, user))
	taskRes := httptest.NewRecorder()
	server.deviceTaskPost(taskRes, taskReq)
	if taskRes.Code != http.StatusBadRequest {
		t.Fatalf("expected forbidden task create to be rejected, got %d", taskRes.Code)
	}

	tasks, err := store.ListOngoingDeviceTasks(ctx, "forbidden-device", forbiddenOrgID)
	if err != nil {
		t.Fatalf("list forbidden device tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected no task to be created in forbidden org, got %#v", tasks)
	}

	releasesReq := httptest.NewRequest(http.MethodGet, "/releases?organisation_id="+strconv.FormatInt(forbiddenOrgID, 10), nil)
	releasesReq = releasesReq.WithContext(context.WithValue(releasesReq.Context(), userContextKey, user))
	releasesRes := httptest.NewRecorder()
	server.releases(releasesRes, releasesReq)
	if releasesRes.Code != http.StatusNotFound {
		t.Fatalf("expected forbidden releases page to be hidden, got %d", releasesRes.Code)
	}

	campaignReq := httptest.NewRequest(http.MethodGet, "/campaigns?organisation_id="+strconv.FormatInt(forbiddenOrgID, 10), nil)
	campaignReq = campaignReq.WithContext(context.WithValue(campaignReq.Context(), userContextKey, user))
	campaignRes := httptest.NewRecorder()
	server.campaigns(campaignRes, campaignReq)
	if campaignRes.Code != http.StatusNotFound {
		t.Fatalf("expected forbidden campaigns page to be hidden, got %d", campaignRes.Code)
	}
}

func TestDeviceTaskPostCreatesFOTATaskWithReleaseID(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
	}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}
	deviceModelID := testDeviceModelID(t, store, organisationID, "Sensor")
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-001",
			OrganisationID:   organisationID,
			DeviceModelID:    deviceModelID,
			ModelName:        "Sensor",
			SoftwareVersions: domain.SoftwareVersions{},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-001",
			Username:     "device-001",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save device: %v", err)
	}
	releaseID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		DeviceModelID:       deviceModelID,
		Version:             "1.2.3",
		ArtifactPath:        "1/firmware.bin",
		ArtifactFilename:    "firmware.bin",
		ArtifactContentType: "application/octet-stream",
		ArtifactSizeBytes:   4,
	})
	if err != nil {
		t.Fatalf("create release: %v", err)
	}

	publisher := newRecordingTaskPublisher()
	server := &Server{store: store, taskPublisher: publisher, fotaDownloadBaseURL: "https://firmware.example.com/downloads/"}
	form := url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"task_type":       {"fota"},
		"release_id":      {strconv.FormatInt(releaseID, 10)},
		"ttl_days":        {"7"},
	}
	req := httptest.NewRequest(http.MethodPost, "/devices/device-001/tasks", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("deviceID", "device-001")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, domain.User{ID: userID, Email: "member@example.com"}))
	res := httptest.NewRecorder()
	server.deviceTaskPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after task launch, got %d", res.Code)
	}

	tasks, err := store.ListOngoingDeviceTasks(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	expectedParameters := `{"release_id":` + strconv.FormatInt(releaseID, 10) + `}`
	if len(tasks) != 1 || tasks[0].Type != "fota" || tasks[0].ParametersJSON != expectedParameters {
		t.Fatalf("unexpected FOTA task: %#v", tasks)
	}

	select {
	case published := <-publisher.tasks:
		if published.organisationID != organisationID || published.task.ID != tasks[0].ID || published.task.Type != "fota" || published.task.ParametersJSON != expectedParameters {
			t.Fatalf("unexpected published task: %#v", published)
		}
	case <-time.After(time.Second):
		t.Fatal("expected task to be published")
	}
}

func TestDeviceModelsPostCreatesModelWithExpectedRelease(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
	}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}
	releaseModelID := testDeviceModelID(t, store, organisationID, "Release target")
	releaseID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		DeviceModelID:       releaseModelID,
		Version:             "1.2.3",
		ArtifactPath:        "1/firmware.bin",
		ArtifactFilename:    "firmware.bin",
		ArtifactContentType: "application/octet-stream",
		ArtifactSizeBytes:   4,
	})
	if err != nil {
		t.Fatalf("create release: %v", err)
	}

	server := &Server{store: store}
	req := formRequest(http.MethodPost, "/device-models", url.Values{
		"organisation_id":            {strconv.FormatInt(organisationID, 10)},
		"name":                       {"Gateway v1"},
		"expected_heartbeat_seconds": {"120"},
		"expected_protocol":          {"mqtt"},
		"expected_release_id":        {strconv.FormatInt(releaseID, 10)},
	}, domain.User{ID: userID, Email: "member@example.com"})
	res := httptest.NewRecorder()
	server.deviceModelsPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after model create, got %d body=%q", res.Code, res.Body.String())
	}

	models, err := store.ListDeviceModels(ctx, organisationID)
	if err != nil {
		t.Fatalf("list device models: %v", err)
	}
	var createdModel *domain.DeviceModel
	for i := range models {
		if models[i].Name == "Gateway v1" {
			createdModel = &models[i]
			break
		}
	}
	if createdModel == nil || createdModel.ExpectedHeartbeatSeconds != 120 || createdModel.ExpectedProtocol != "mqtt" {
		t.Fatalf("unexpected device model: %#v", models)
	}
	if createdModel.ExpectedReleaseID == nil || *createdModel.ExpectedReleaseID != releaseID || createdModel.ExpectedReleaseVersion != "1.2.3" {
		t.Fatalf("unexpected expected release on model: %#v", createdModel)
	}

	server = testServerWithTemplates(t, store)
	user := domain.User{ID: userID, Email: "member@example.com"}
	listReq := httptest.NewRequest(http.MethodGet, "/device-models?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), userContextKey, user))
	listRes := httptest.NewRecorder()
	server.deviceModels(listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("expected device models page, got %d body=%q", listRes.Code, listRes.Body.String())
	}
	listBody := listRes.Body.String()
	if !strings.Contains(listBody, `href="/device-models/new?organisation_id=`+strconv.FormatInt(organisationID, 10)+`"`) {
		t.Fatalf("expected device model list to link to create page, got %q", listBody)
	}
	if !strings.Contains(listBody, `href="/device-models/`+strconv.FormatInt(createdModel.ID, 10)+`?organisation_id=`+strconv.FormatInt(organisationID, 10)+`"`) {
		t.Fatalf("expected device model list to link to detail page, got %q", listBody)
	}
	if strings.Contains(listBody, `method="post" action="/device-models"`) || strings.Contains(listBody, `name="expected_heartbeat_seconds"`) {
		t.Fatalf("expected device model list to be list-only, got %q", listBody)
	}

	newReq := httptest.NewRequest(http.MethodGet, "/device-models/new?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	newReq = newReq.WithContext(context.WithValue(newReq.Context(), userContextKey, user))
	newRes := httptest.NewRecorder()
	server.deviceModelNew(newRes, newReq)
	if newRes.Code != http.StatusOK {
		t.Fatalf("expected device model create page, got %d body=%q", newRes.Code, newRes.Body.String())
	}
	newBody := newRes.Body.String()
	for _, expected := range []string{
		"Create device model",
		`method="post" action="/device-models"`,
		`name="expected_heartbeat_seconds"`,
		`name="expected_protocol"`,
		`name="expected_release_id"`,
		"Back to models",
	} {
		if !strings.Contains(newBody, expected) {
			t.Fatalf("expected device model create page to contain %q, got %q", expected, newBody)
		}
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/device-models/"+strconv.FormatInt(createdModel.ID, 10)+"?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	detailReq.SetPathValue("modelID", strconv.FormatInt(createdModel.ID, 10))
	detailReq = detailReq.WithContext(context.WithValue(detailReq.Context(), userContextKey, user))
	detailRes := httptest.NewRecorder()
	server.deviceModelDetail(detailRes, detailReq)
	if detailRes.Code != http.StatusOK {
		t.Fatalf("expected device model detail page, got %d body=%q", detailRes.Code, detailRes.Body.String())
	}
	detailBody := detailRes.Body.String()
	for _, expected := range []string{
		"Gateway v1",
		"Expected release",
		`method="post" action="/device-models/` + strconv.FormatInt(createdModel.ID, 10) + `/expected-release"`,
		`option value="` + strconv.FormatInt(releaseID, 10) + `" selected`,
	} {
		if !strings.Contains(detailBody, expected) {
			t.Fatalf("expected device model detail page to contain %q, got %q", expected, detailBody)
		}
	}

	clearReq := formRequest(http.MethodPost, "/device-models/"+strconv.FormatInt(createdModel.ID, 10)+"/expected-release", url.Values{
		"organisation_id":     {strconv.FormatInt(organisationID, 10)},
		"expected_release_id": {""},
	}, user)
	clearReq.SetPathValue("modelID", strconv.FormatInt(createdModel.ID, 10))
	clearRes := httptest.NewRecorder()
	server.deviceModelExpectedReleasePost(clearRes, clearReq)
	if clearRes.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after expected release clear, got %d body=%q", clearRes.Code, clearRes.Body.String())
	}
	if location := clearRes.Header().Get("Location"); location != "/device-models/"+strconv.FormatInt(createdModel.ID, 10)+"?organisation_id="+strconv.FormatInt(organisationID, 10) {
		t.Fatalf("unexpected expected release redirect location %q", location)
	}
	updatedModel, err := store.DeviceModel(ctx, createdModel.ID, organisationID)
	if err != nil {
		t.Fatalf("load updated device model: %v", err)
	}
	if updatedModel.ExpectedReleaseID != nil || updatedModel.ExpectedReleaseVersion != "" {
		t.Fatalf("expected release to be cleared, got %#v", updatedModel)
	}
}

func TestLocalTimeElementNormalizesUTCTimestamps(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "sqlite current timestamp", input: "2026-06-29 10:01:00", want: `datetime="2026-06-29T10:01:00Z"`},
		{name: "rfc3339", input: "2026-06-29T10:01:00Z", want: `datetime="2026-06-29T10:01:00Z"`},
		{name: "postgres offset", input: "2026-06-29 12:01:00+02", want: `datetime="2026-06-29T10:01:00Z"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := string(localTimeElement(tc.input))
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "data-local-time") {
				t.Fatalf("expected local time element with %q, got %q", tc.want, got)
			}
		})
	}
}

func TestDevicesPostCreatesDeviceWithSelectedModel(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
	}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}
	modelID := testDeviceModelID(t, store, organisationID, "Sensor")

	server := testServerWithTemplates(t, store)
	req := formRequest(http.MethodPost, "/devices", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"device_id":       {"device-001"},
		"device_model_id": {strconv.FormatInt(modelID, 10)},
		"mqtt_username":   {"device-001"},
		"mqtt_password":   {"secret"},
	}, domain.User{ID: userID, Email: "member@example.com"})
	res := httptest.NewRecorder()
	server.devicesPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after device create, got %d body=%q", res.Code, res.Body.String())
	}

	detail, err := store.DeviceDetail(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("load device detail: %v", err)
	}
	if detail.Device.DeviceModelID != modelID || detail.Device.ModelName != "Sensor" {
		t.Fatalf("unexpected device model link: %#v", detail.Device)
	}
	detailReq := httptest.NewRequest(http.MethodGet, "/devices/device-001?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	detailReq.SetPathValue("deviceID", "device-001")
	detailReq = detailReq.WithContext(context.WithValue(detailReq.Context(), userContextKey, domain.User{ID: userID, Email: "member@example.com"}))
	detailRes := httptest.NewRecorder()
	server.deviceDetail(detailRes, detailReq)
	if detailRes.Code != http.StatusOK || !strings.Contains(detailRes.Body.String(), "MQTT configuration") || !strings.Contains(detailRes.Body.String(), "Data publish topic") || strings.Contains(detailRes.Body.String(), "PSK identity") || strings.Contains(detailRes.Body.String(), `aria-label="Communication type"`) {
		t.Fatalf("expected MQTT-only detail without protocol tabs, got %d body=%q", detailRes.Code, detailRes.Body.String())
	}
}

func TestDevicesPostCreatesCoAPCredentialForCoAPModel(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{Email: "member@example.com", Name: "Member", PasswordHash: "hash"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{UserID: userID, OrganisationID: organisationID}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}
	modelID, err := store.CreateDeviceModel(ctx, domain.DeviceModel{
		OrganisationID:           organisationID,
		Name:                     "Constrained sensor",
		ExpectedHeartbeatSeconds: 60,
		ExpectedProtocol:         "coap",
	})
	if err != nil {
		t.Fatalf("create CoAP model: %v", err)
	}

	server := testServerWithTemplates(t, store)
	getReq := httptest.NewRequest(http.MethodGet, "/devices/new?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), userContextKey, domain.User{ID: userID, Email: "member@example.com"}))
	getRes := httptest.NewRecorder()
	server.deviceNew(getRes, getReq)
	if getRes.Code != http.StatusOK || !strings.Contains(getRes.Body.String(), `data-protocol="coap"`) || !strings.Contains(getRes.Body.String(), `data-device-protocol-config="mqtt"`) || !strings.Contains(getRes.Body.String(), `data-device-protocol-config="coap"`) {
		t.Fatalf("expected model-driven MQTT and CoAP form panels, got %d body=%q", getRes.Code, getRes.Body.String())
	}

	req := formRequest(http.MethodPost, "/devices", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"device_id":       {"coap-001"},
		"device_model_id": {strconv.FormatInt(modelID, 10)},
	}, domain.User{ID: userID, Email: "member@example.com"})
	res := httptest.NewRecorder()
	server.devicesPost(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected CoAP credential response, got %d body=%q", res.Code, res.Body.String())
	}

	credential, err := store.ResolveCoAPCredential(ctx, "coap-001")
	if err != nil {
		t.Fatalf("resolve generated CoAP credential: %v", err)
	}
	if len(credential.PSK) != 16 || credential.ExpectedProtocol != "coap" {
		t.Fatalf("unexpected generated CoAP credential: %#v", credential)
	}
	body := res.Body.String()
	if !strings.Contains(body, hex.EncodeToString(credential.PSK)) || !strings.Contains(body, "Anchor will not show it again") {
		t.Fatalf("expected one-time generated credential disclosure, got %q", body)
	}
	if _, err := store.FindMQTTCredentialByUsername(ctx, "coap-001"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("expected no MQTT credential for CoAP model, got %v", err)
	}
	detailReq := httptest.NewRequest(http.MethodGet, "/devices/coap-001?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	detailReq.SetPathValue("deviceID", "coap-001")
	detailReq = detailReq.WithContext(context.WithValue(detailReq.Context(), userContextKey, domain.User{ID: userID, Email: "member@example.com"}))
	detailRes := httptest.NewRecorder()
	server.deviceDetail(detailRes, detailReq)
	detailBody := detailRes.Body.String()
	if detailRes.Code != http.StatusOK || !strings.Contains(detailBody, "PSK identity") || !strings.Contains(detailBody, "Hidden after creation") || strings.Contains(detailBody, "MQTT username") || strings.Contains(detailBody, "Data publish topic") || strings.Contains(detailBody, `aria-label="Communication type"`) {
		t.Fatalf("expected CoAP-only detail without protocol tabs, got %d body=%q", detailRes.Code, detailBody)
	}

	importedPSK := bytes.Repeat([]byte{0xab}, 16)
	importedHex := hex.EncodeToString(importedPSK)
	req = formRequest(http.MethodPost, "/devices", url.Values{
		"organisation_id":   {strconv.FormatInt(organisationID, 10)},
		"device_id":         {"coap-imported"},
		"device_model_id":   {strconv.FormatInt(modelID, 10)},
		"coap_psk_identity": {"manufacturing-001"},
		"coap_psk":          {importedHex},
	}, domain.User{ID: userID, Email: "member@example.com"})
	res = httptest.NewRecorder()
	server.devicesPost(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected imported hexadecimal CoAP credential response, got %d body=%q", res.Code, res.Body.String())
	}
	importedCredential, err := store.ResolveCoAPCredential(ctx, "manufacturing-001")
	if err != nil {
		t.Fatalf("resolve imported CoAP credential: %v", err)
	}
	if !bytes.Equal(importedCredential.PSK, importedPSK) || !strings.Contains(res.Body.String(), importedHex) {
		t.Fatalf("expected hexadecimal PSK to round trip, got credential=%#v body=%q", importedCredential, res.Body.String())
	}
}

func TestDeviceCVEStatusShownOnListAndDetail(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
	}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}
	modelID := testDeviceModelID(t, store, organisationID, "Sensor")
	releaseID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		DeviceModelID:       modelID,
		Version:             "1.2.3",
		ArtifactPath:        "1/firmware.bin",
		ArtifactFilename:    "firmware.bin",
		ArtifactContentType: "application/octet-stream",
		ArtifactSizeBytes:   4,
	})
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if _, err := store.ReplaceReleaseSBOM(ctx, organisationID, releaseID, 1, 64); err != nil {
		t.Fatalf("replace sbom: %v", err)
	}
	run, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "auto")
	if err != nil {
		t.Fatalf("enqueue scan: %v", err)
	}
	if err := store.CompleteCVEScanRun(ctx, organisationID, run.ID, "2026-06-29T10:01:00Z", []domain.CVEScanFinding{
		{CVEID: "CVE-2026-0001", Severity: "high", PackageName: "lib-a", InstalledVersion: "1.0.0"},
	}); err != nil {
		t.Fatalf("complete scan: %v", err)
	}
	for _, device := range []domain.Device{
		{
			ID:               "device-impacted",
			OrganisationID:   organisationID,
			DeviceModelID:    modelID,
			SoftwareVersions: domain.SoftwareVersions{"firmware": "1.2.3"},
		},
		{
			ID:               "device-unknown",
			OrganisationID:   organisationID,
			DeviceModelID:    modelID,
			SoftwareVersions: domain.SoftwareVersions{"firmware": "9.9.9"},
		},
	} {
		if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
			Device: device,
			Credential: domain.DeviceMQTTCredential{
				DeviceID:     device.ID,
				Username:     device.ID,
				PasswordHash: "hash",
				Enabled:      true,
			},
		}); err != nil {
			t.Fatalf("save device %s: %v", device.ID, err)
		}
	}

	server := testServerWithTemplates(t, store)
	user := domain.User{ID: userID, Email: "member@example.com"}
	listReq := httptest.NewRequest(http.MethodGet, "/devices?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), userContextKey, user))
	listRes := httptest.NewRecorder()
	server.devices(listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("expected devices page, got %d body=%q", listRes.Code, listRes.Body.String())
	}
	listBody := listRes.Body.String()
	for _, expected := range []string{"CVE", "Impacted", "High", "Unknown release"} {
		if !strings.Contains(listBody, expected) {
			t.Fatalf("expected devices page to contain %q, got %q", expected, listBody)
		}
	}
	if strings.Contains(listBody, "CVE-2026-0001") || strings.Contains(listBody, "lib-a") {
		t.Fatalf("expected device list not to expose raw scanner findings, got %q", listBody)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/devices/device-impacted?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	detailReq.SetPathValue("deviceID", "device-impacted")
	detailReq = detailReq.WithContext(context.WithValue(detailReq.Context(), userContextKey, user))
	detailRes := httptest.NewRecorder()
	server.deviceDetail(detailRes, detailReq)
	if detailRes.Code != http.StatusOK {
		t.Fatalf("expected device detail, got %d body=%q", detailRes.Code, detailRes.Body.String())
	}
	detailBody := detailRes.Body.String()
	for _, expected := range []string{
		"Firmware",
		"1.2.3",
		"Impacted",
		"High",
		"Active CVEs",
		`href="/releases/` + strconv.FormatInt(releaseID, 10) + `?organisation_id=` + strconv.FormatInt(organisationID, 10) + `"`,
		"Sensor 1.2.3",
	} {
		if !strings.Contains(detailBody, expected) {
			t.Fatalf("expected device detail to contain %q, got %q", expected, detailBody)
		}
	}
}

func TestDevicesPagePaginationSearchAndMetrics(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
	}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}
	modelID := testDeviceModelID(t, store, organisationID, "Sensor")
	for i := 1; i <= 30; i++ {
		deviceID := fmt.Sprintf("device-%02d", i)
		if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
			Device: domain.Device{
				ID:               deviceID,
				OrganisationID:   organisationID,
				DeviceModelID:    modelID,
				SoftwareVersions: domain.SoftwareVersions{},
			},
			Credential: domain.DeviceMQTTCredential{
				DeviceID:     deviceID,
				Username:     fmt.Sprintf("field-%02d", i),
				PasswordHash: "hash",
				Enabled:      true,
			},
		}); err != nil {
			t.Fatalf("save device %s: %v", deviceID, err)
		}
	}

	server := testServerWithTemplates(t, store)
	user := domain.User{ID: userID, Email: "member@example.com"}
	req := httptest.NewRequest(http.MethodGet, "/devices?organisation_id="+strconv.FormatInt(organisationID, 10)+"&page_size=25", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	res := httptest.NewRecorder()
	server.devices(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected devices page, got %d body=%q", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, expected := range []string{
		"Total devices",
		">30<",
		"Showing <span class=\"value-mono\">1-25</span> of <span class=\"value-mono\">30</span>",
		"Page 1 of 2",
		"page=2&amp;page_size=25",
		`name="page_size" value="25"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected devices page to contain %q, got %q", expected, body)
		}
	}

	searchReq := httptest.NewRequest(http.MethodGet, "/devices?organisation_id="+strconv.FormatInt(organisationID, 10)+"&q=no-match&page=4&page_size=25", nil)
	searchReq = searchReq.WithContext(context.WithValue(searchReq.Context(), userContextKey, user))
	searchRes := httptest.NewRecorder()
	server.devices(searchRes, searchReq)
	if searchRes.Code != http.StatusOK {
		t.Fatalf("expected searched devices page, got %d body=%q", searchRes.Code, searchRes.Body.String())
	}
	searchBody := searchRes.Body.String()
	for _, expected := range []string{
		`value="no-match"`,
		"0 matching devices",
		"from 30 registered",
		"No devices match this search.",
		"Page 1 of 1",
	} {
		if !strings.Contains(searchBody, expected) {
			t.Fatalf("expected searched devices page to contain %q, got %q", expected, searchBody)
		}
	}
	if strings.Contains(searchBody, `name="page"`) {
		t.Fatalf("expected search form to reset page instead of preserving it, got %q", searchBody)
	}
}

func TestFOTADownloadURLDefaultsToReleaseBinaryPath(t *testing.T) {
	t.Parallel()

	server := &Server{}
	if got, want := server.fotaDownloadURL(7, 42), "/org/42/releases/7/binary"; got != want {
		t.Fatalf("unexpected default FOTA download URL: got %q want %q", got, want)
	}
}

func TestDeviceConnectivityUsesModelHeartbeatAndLatestEvent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	connected := deviceConnectivity(domain.Device{
		ExpectedHeartbeatSeconds: 60,
		LastEventReceivedMS:      now.Add(-30 * time.Second).UnixMilli(),
	}, now)
	if !connected.Connected || connected.Status != "Connected" || connected.StatusClass != "status-success" {
		t.Fatalf("expected connected status, got %#v", connected)
	}

	disconnected := deviceConnectivity(domain.Device{
		ExpectedHeartbeatSeconds: 60,
		LastEventReceivedMS:      now.Add(-61 * time.Second).UnixMilli(),
	}, now)
	if disconnected.Connected || disconnected.Status != "Disconnected" || disconnected.StatusClass != "status-danger" {
		t.Fatalf("expected disconnected status, got %#v", disconnected)
	}

	neverSeen := deviceConnectivity(domain.Device{ExpectedHeartbeatSeconds: 60}, now)
	if neverSeen.Connected || neverSeen.LastSeen != "Never" {
		t.Fatalf("expected never-seen disconnected status, got %#v", neverSeen)
	}
}

func TestOrganisationManagementRequiresOrganisationAdmin(t *testing.T) {
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

	adminID, err := store.CreateUser(ctx, domain.User{
		Email:        "admin@example.com",
		Name:         "Admin",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	memberID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	organisationID, err := store.CreateOrganisationForUser(ctx, domain.Organisation{Name: "Shared"}, adminID)
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         memberID,
		OrganisationID: organisationID,
		Role:           db.OrganisationRoleMember,
	}); err != nil {
		t.Fatalf("add member: %v", err)
	}

	server := testServerWithTemplates(t, store)
	memberUser := domain.User{ID: memberID, Email: "member@example.com"}

	renameReq := formRequest(http.MethodPost, "/organisations/rename", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"name":            {"Renamed"},
	}, memberUser)
	renameRes := httptest.NewRecorder()
	server.organisationRenamePost(renameRes, renameReq)
	if renameRes.Code != http.StatusForbidden {
		t.Fatalf("expected non-admin rename to be forbidden, got %d", renameRes.Code)
	}

	inviteReq := formRequest(http.MethodPost, "/organisations/invitations", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"email":           {"new@example.com"},
	}, memberUser)
	inviteRes := httptest.NewRecorder()
	server.organisationInvitationsPost(inviteRes, inviteReq)
	if inviteRes.Code != http.StatusForbidden {
		t.Fatalf("expected non-admin invite to be forbidden, got %d", inviteRes.Code)
	}

	removeReq := formRequest(http.MethodPost, "/organisations/members/remove", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"user_id":         {strconv.FormatInt(adminID, 10)},
	}, memberUser)
	removeRes := httptest.NewRecorder()
	server.organisationMemberRemovePost(removeRes, removeReq)
	if removeRes.Code != http.StatusForbidden {
		t.Fatalf("expected non-admin remove to be forbidden, got %d", removeRes.Code)
	}
}

func TestOrganisationAdminCanManageMembersAndInvitations(t *testing.T) {
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

	adminID, err := store.CreateUser(ctx, domain.User{
		Email:        "admin@example.com",
		Name:         "Admin",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	memberID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	organisationID, err := store.CreateOrganisationForUser(ctx, domain.Organisation{Name: "Shared"}, adminID)
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         memberID,
		OrganisationID: organisationID,
		Role:           db.OrganisationRoleMember,
	}); err != nil {
		t.Fatalf("add member: %v", err)
	}

	server := testServerWithTemplates(t, store)
	adminUser := domain.User{ID: adminID, Email: "admin@example.com"}

	renameReq := formRequest(http.MethodPost, "/organisations/rename", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"name":            {"Renamed"},
	}, adminUser)
	renameRes := httptest.NewRecorder()
	server.organisationRenamePost(renameRes, renameReq)
	if renameRes.Code != http.StatusSeeOther {
		t.Fatalf("expected admin rename redirect, got %d body=%q", renameRes.Code, renameRes.Body.String())
	}
	organisation, err := store.Organisation(ctx, organisationID)
	if err != nil {
		t.Fatalf("load organisation: %v", err)
	}
	if organisation.Name != "Renamed" {
		t.Fatalf("expected organisation rename, got %#v", organisation)
	}

	inviteReq := formRequest(http.MethodPost, "/organisations/invitations", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"email":           {"new@example.com"},
	}, adminUser)
	inviteRes := httptest.NewRecorder()
	server.organisationInvitationsPost(inviteRes, inviteReq)
	if inviteRes.Code != http.StatusOK {
		t.Fatalf("expected invite page render, got %d body=%q", inviteRes.Code, inviteRes.Body.String())
	}
	if !strings.Contains(inviteRes.Body.String(), "/invitations/") {
		t.Fatalf("expected generated invitation URL in response, got %q", inviteRes.Body.String())
	}

	removeReq := formRequest(http.MethodPost, "/organisations/members/remove", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"user_id":         {strconv.FormatInt(memberID, 10)},
	}, adminUser)
	removeRes := httptest.NewRecorder()
	server.organisationMemberRemovePost(removeRes, removeReq)
	if removeRes.Code != http.StatusSeeOther {
		t.Fatalf("expected member removal redirect, got %d body=%q", removeRes.Code, removeRes.Body.String())
	}
	members, err := store.ListOrganisationMembers(ctx, organisationID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 || members[0].UserID != adminID {
		t.Fatalf("expected member removed, got %#v", members)
	}
}

func TestInvitationSignupCreatesAccountAndSession(t *testing.T) {
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

	adminID, err := store.CreateUser(ctx, domain.User{
		Email:        "admin@example.com",
		Name:         "Admin",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	organisationID, err := store.CreateOrganisationForUser(ctx, domain.Organisation{Name: "Shared"}, adminID)
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	invite, err := store.InviteUserToOrganisation(ctx, organisationID, adminID, "new@example.com", time.Now())
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}

	server := testServerWithTemplates(t, store)
	req := httptest.NewRequest(http.MethodPost, "/invitations/"+invite.Token, strings.NewReader(url.Values{
		"name":     {"New User"},
		"password": {"secret"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("token", invite.Token)
	res := httptest.NewRecorder()
	server.invitationSignupPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected signup redirect, got %d body=%q", res.Code, res.Body.String())
	}
	if got := res.Header().Get("Location"); got != "/organisations?organisation_id="+strconv.FormatInt(organisationID, 10) {
		t.Fatalf("unexpected signup redirect %q", got)
	}
	cookies := res.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != sessionCookieName {
		t.Fatalf("expected session cookie, got %#v", cookies)
	}
	user, err := store.UserBySession(ctx, cookies[0].Value, time.Now())
	if err != nil {
		t.Fatalf("load signed-in user by session: %v", err)
	}
	if user.Email != "new@example.com" {
		t.Fatalf("unexpected signed-in user: %#v", user)
	}
	if organisations, err := store.ListOrganisationsForUser(ctx, user); err != nil {
		t.Fatalf("list new user organisations: %v", err)
	} else if !containsOrganisationID(organisations, organisationID) || len(organisations) != 2 {
		t.Fatalf("expected invited and personal organisations, got %#v", organisations)
	}
}

func TestDeviceTaskCancelPostCancelsOngoingTask(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
	}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-001",
			OrganisationID:   organisationID,
			DeviceModelID:    testDeviceModelID(t, store, organisationID, "Sensor"),
			ModelName:        "Sensor",
			SoftwareVersions: domain.SoftwareVersions{},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-001",
			Username:     "device-001",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save device: %v", err)
	}
	taskID, err := store.CreateDeviceTask(ctx, domain.DeviceTask{
		DeviceID:       "device-001",
		Type:           "fota",
		ParametersJSON: `{"release_id":1}`,
		Status:         db.DeviceTaskStatusPending,
		CreatedAt:      "2026-06-07T08:00:00Z",
	}, organisationID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	form := url.Values{"organisation_id": {strconv.FormatInt(organisationID, 10)}}
	req := httptest.NewRequest(http.MethodPost, "/devices/device-001/tasks/"+strconv.FormatInt(taskID, 10)+"/cancel", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("deviceID", "device-001")
	req.SetPathValue("taskID", strconv.FormatInt(taskID, 10))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, domain.User{ID: userID, Email: "member@example.com"}))
	res := httptest.NewRecorder()

	server := &Server{store: store}
	server.deviceTaskCancelPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after task cancel, got %d", res.Code)
	}

	tasks, err := store.ListOngoingDeviceTasks(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected canceled task to leave ongoing list, got %#v", tasks)
	}
}

func TestReleaseUploadStoresArtifactAndServesBinary(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
	}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("organisation_id", strconv.FormatInt(organisationID, 10)); err != nil {
		t.Fatalf("write org field: %v", err)
	}
	deviceModelID := testDeviceModelID(t, store, organisationID, "Gateway")
	if err := writer.WriteField("device_model_id", strconv.FormatInt(deviceModelID, 10)); err != nil {
		t.Fatalf("write device model field: %v", err)
	}
	if err := writer.WriteField("version", "1.2.3"); err != nil {
		t.Fatalf("write version field: %v", err)
	}
	part, err := writer.CreateFormFile("artifact", "firmware.bin")
	if err != nil {
		t.Fatalf("create artifact field: %v", err)
	}
	if _, err := part.Write([]byte("firmware-bytes")); err != nil {
		t.Fatalf("write artifact field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	storageDir := t.TempDir()
	server := &Server{store: store, releaseStorageDir: storageDir}
	user := domain.User{ID: userID, Email: "member@example.com"}
	req := httptest.NewRequest(http.MethodPost, "/releases", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	res := httptest.NewRecorder()
	server.releasesPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after release upload, got %d body=%q", res.Code, res.Body.String())
	}

	releases, err := store.ListSoftwareReleases(ctx, organisationID)
	if err != nil {
		t.Fatalf("list releases: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected one release, got %#v", releases)
	}
	release := releases[0]
	if release.ID <= 0 {
		t.Fatalf("expected release id to be populated, got %#v", release)
	}
	if release.DeviceModelID != deviceModelID || release.DeviceModelName != "Gateway" {
		t.Fatalf("unexpected release model metadata: %#v", release)
	}
	if release.ArtifactFilename != "firmware.bin" || release.ArtifactSizeBytes != int64(len("firmware-bytes")) {
		t.Fatalf("unexpected artifact metadata: %#v", release)
	}
	scanRuns, err := store.ListCVEScanRuns(ctx, organisationID, release.ID)
	if err != nil {
		t.Fatalf("list cve scan runs: %v", err)
	}
	if len(scanRuns) != 0 {
		t.Fatalf("expected firmware-only release not to enqueue a scan, got %#v", scanRuns)
	}
	artifactPath, ok := server.releaseArtifactFullPath(release.ArtifactPath)
	if !ok {
		t.Fatalf("artifact path rejected: %q", release.ArtifactPath)
	}
	if _, err := os.Stat(artifactPath); err != nil {
		t.Fatalf("expected artifact on disk: %v", err)
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/org/"+strconv.FormatInt(organisationID, 10)+"/releases/"+strconv.FormatInt(release.ID, 10)+"/binary", nil)
	downloadReq.SetPathValue("organisationID", strconv.FormatInt(organisationID, 10))
	downloadReq.SetPathValue("releaseID", strconv.FormatInt(release.ID, 10))
	downloadRes := httptest.NewRecorder()
	server.releaseBinary(downloadRes, downloadReq)
	if downloadRes.Code != http.StatusOK {
		t.Fatalf("expected release binary, got %d", downloadRes.Code)
	}
	if got := downloadRes.Body.String(); got != "firmware-bytes" {
		t.Fatalf("unexpected release binary body %q", got)
	}

}

func TestReleaseUploadStoresSPDXFilesAndShowsAggregateCount(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
	}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}
	deviceModelID := testDeviceModelID(t, store, organisationID, "Gateway")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("organisation_id", strconv.FormatInt(organisationID, 10)); err != nil {
		t.Fatalf("write org field: %v", err)
	}
	if err := writer.WriteField("device_model_id", strconv.FormatInt(deviceModelID, 10)); err != nil {
		t.Fatalf("write device model field: %v", err)
	}
	if err := writer.WriteField("version", "1.2.3"); err != nil {
		t.Fatalf("write version field: %v", err)
	}
	artifactPart, err := writer.CreateFormFile("artifact", "firmware.bin")
	if err != nil {
		t.Fatalf("create artifact field: %v", err)
	}
	if _, err := artifactPart.Write([]byte("firmware-bytes")); err != nil {
		t.Fatalf("write artifact field: %v", err)
	}
	appSPDX, err := writer.CreateFormFile("spdx_files", "app.spdx")
	if err != nil {
		t.Fatalf("create app spdx field: %v", err)
	}
	if _, err := appSPDX.Write([]byte("SPDXVersion: SPDX-2.3\nDataLicense: CC0-1.0\nSPDXID: SPDXRef-DOCUMENT\n")); err != nil {
		t.Fatalf("write app spdx field: %v", err)
	}
	buildSPDX, err := writer.CreateFormFile("spdx_files", "build.spdx")
	if err != nil {
		t.Fatalf("create build spdx field: %v", err)
	}
	if _, err := buildSPDX.Write([]byte(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT"}`)); err != nil {
		t.Fatalf("write build spdx field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	storageDir := t.TempDir()
	server := testServerWithTemplates(t, store)
	server.releaseStorageDir = storageDir
	user := domain.User{ID: userID, Email: "member@example.com"}
	req := httptest.NewRequest(http.MethodPost, "/releases", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	res := httptest.NewRecorder()
	server.releasesPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after release upload, got %d body=%q", res.Code, res.Body.String())
	}

	releases, err := store.ListSoftwareReleases(ctx, organisationID)
	if err != nil {
		t.Fatalf("list releases: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected one release, got %#v", releases)
	}
	sbomDir, ok := server.releaseArtifactFullPath(releaseSBOMRelativeDir(releases[0].ArtifactPath))
	if !ok {
		t.Fatalf("sbom path rejected for artifact %q", releases[0].ArtifactPath)
	}
	entries, err := os.ReadDir(sbomDir)
	if err != nil {
		t.Fatalf("read sbom dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two sbom files, got %#v", entries)
	}
	sbom, err := store.CurrentReleaseSBOM(ctx, organisationID, releases[0].ID)
	if err != nil {
		t.Fatalf("current release sbom: %v", err)
	}
	if sbom.FileCount != 2 {
		t.Fatalf("unexpected sbom metadata: %#v", sbom)
	}
	scanRuns, err := store.ListCVEScanRuns(ctx, organisationID, releases[0].ID)
	if err != nil {
		t.Fatalf("list cve scan runs: %v", err)
	}
	if len(scanRuns) != 1 || scanRuns[0].Status != "pending" || scanRuns[0].Trigger != "auto" {
		t.Fatalf("expected auto-enqueued pending scan, got %#v", scanRuns)
	}
	if err := store.CompleteCVEScanRun(ctx, organisationID, scanRuns[0].ID, "2026-06-29T10:01:00Z", []domain.CVEScanFinding{
		{CVEID: "CVE-2026-0001", Severity: "critical", PackageName: "lib-a", InstalledVersion: "1.0.0"},
		{CVEID: "CVE-2026-0001", Severity: "high", PackageName: "lib-b", InstalledVersion: "2.0.0"},
		{CVEID: "CVE-2026-0002", Severity: "high", PackageName: "lib-c", InstalledVersion: "3.0.0"},
		{CVEID: "CVE-2026-0003", Severity: "medium", PackageName: "lib-d", InstalledVersion: "4.0.0"},
		{CVEID: "CVE-2026-0004", Severity: "low", PackageName: "lib-e", InstalledVersion: "5.0.0"},
		{CVEID: "CVE-2026-0005", Severity: "unknown", PackageName: "lib-f", InstalledVersion: "6.0.0"},
	}); err != nil {
		t.Fatalf("complete cve scan: %v", err)
	}
	releaseViews, err := server.releaseViews(ctx, organisationID)
	if err != nil {
		t.Fatalf("release views: %v", err)
	}
	if len(releaseViews) != 1 {
		t.Fatalf("expected one release view, got %#v", releaseViews)
	}
	counts := releaseViews[0].CVECounts
	if !counts.HasData || counts.Total != 5 || counts.Critical != 1 || counts.High != 1 || counts.Medium != 1 || counts.Low != 1 || counts.Other != 1 {
		t.Fatalf("unexpected cve severity counts: %#v", counts)
	}
	if releaseViews[0].LastScanAt != "2026-06-29T10:01:00Z" {
		t.Fatalf("unexpected last scan time: %#v", releaseViews[0].LastScanAt)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/releases?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), userContextKey, user))
	listRes := httptest.NewRecorder()
	server.releases(listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("expected releases page, got %d body=%q", listRes.Code, listRes.Body.String())
	}
	bodyText := listRes.Body.String()
	if !strings.Contains(bodyText, `href="/releases/new?organisation_id=`+strconv.FormatInt(organisationID, 10)+`"`) {
		t.Fatalf("expected release list to link to create page, got %q", bodyText)
	}
	if strings.Contains(bodyText, `enctype="multipart/form-data"`) || strings.Contains(bodyText, `name="artifact"`) {
		t.Fatalf("expected release list to be list-only, got %q", bodyText)
	}
	if strings.Contains(bodyText, "SPDX files") || strings.Contains(bodyText, ">SBOM<") {
		t.Fatalf("expected release list not to show SBOM column, got %q", bodyText)
	}
	for _, expected := range []string{"CVE status", "Critical", "High", "Medium", "Low", "Other", "Last scan", "2026-06-29T10:01:00Z", "Impacted · Critical"} {
		if !strings.Contains(bodyText, expected) {
			t.Fatalf("expected release list to contain %q, got %q", expected, bodyText)
		}
	}
	if strings.Contains(bodyText, "app.spdx") || strings.Contains(bodyText, "build.spdx") {
		t.Fatalf("expected release list not to show individual SPDX filenames, got %q", bodyText)
	}

	newReq := httptest.NewRequest(http.MethodGet, "/releases/new?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	newReq = newReq.WithContext(context.WithValue(newReq.Context(), userContextKey, user))
	newRes := httptest.NewRecorder()
	server.releaseNew(newRes, newReq)
	if newRes.Code != http.StatusOK {
		t.Fatalf("expected release create page, got %d body=%q", newRes.Code, newRes.Body.String())
	}
	newBody := newRes.Body.String()
	for _, expected := range []string{
		"Create release",
		`method="post" action="/releases"`,
		`name="device_model_id"`,
		`name="artifact"`,
		`name="spdx_files"`,
		"Back to releases",
	} {
		if !strings.Contains(newBody, expected) {
			t.Fatalf("expected release create page to contain %q, got %q", expected, newBody)
		}
	}
}

func TestReleaseDetailShowsCVEsRescansAndReplacesSBOM(t *testing.T) {
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

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.com",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
	}); err != nil {
		t.Fatalf("add user to organisation: %v", err)
	}
	deviceModelID := testDeviceModelID(t, store, organisationID, "Gateway")
	releaseID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		DeviceModelID:       deviceModelID,
		Version:             "1.2.3",
		ArtifactPath:        "1/firmware.bin",
		ArtifactFilename:    "firmware.bin",
		ArtifactContentType: "application/octet-stream",
		ArtifactSizeBytes:   14,
	})
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if _, err := store.ReplaceReleaseSBOM(ctx, organisationID, releaseID, 2, 128); err != nil {
		t.Fatalf("replace sbom metadata: %v", err)
	}
	successRun, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "auto")
	if err != nil {
		t.Fatalf("enqueue scan: %v", err)
	}
	if err := store.StartCVEScanRun(ctx, organisationID, successRun.ID, "2026-06-29T10:00:00Z"); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	if err := store.CompleteCVEScanRun(ctx, organisationID, successRun.ID, "2026-06-29T10:01:00Z", []domain.CVEScanFinding{
		{CVEID: "CVE-2026-0001", Severity: "high", PackageName: "lib-a", InstalledVersion: "1.0.0"},
		{CVEID: "CVE-2026-0002", Severity: "medium", PackageName: "lib-b", InstalledVersion: "2.0.0"},
	}); err != nil {
		t.Fatalf("complete scan: %v", err)
	}
	failedRun, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "manual")
	if err != nil {
		t.Fatalf("enqueue failed scan: %v", err)
	}
	if err := store.FailCVEScanRun(ctx, organisationID, failedRun.ID, "2026-06-29T10:02:00Z", "scanner failed"); err != nil {
		t.Fatalf("fail scan: %v", err)
	}
	if _, err := store.UpsertReleaseCVEWaiver(ctx, domain.ReleaseCVEWaiver{
		OrganisationID: organisationID,
		ReleaseID:      releaseID,
		CVEID:          "CVE-2026-0001",
		Note:           "not shipped",
	}); err != nil {
		t.Fatalf("upsert waiver: %v", err)
	}

	storageDir := t.TempDir()
	oldSBOMDir := filepath.Join(storageDir, "1", "firmware.bin.sbom")
	if err := os.MkdirAll(oldSBOMDir, 0o755); err != nil {
		t.Fatalf("create old sbom dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldSBOMDir, "old.spdx"), []byte("SPDXVersion: SPDX-2.3\n"), 0o644); err != nil {
		t.Fatalf("write old sbom: %v", err)
	}
	worker := &recordingCVEWorker{}
	server := testServerWithTemplates(t, store)
	server.releaseStorageDir = storageDir
	server.cveScanWorker = worker
	user := domain.User{ID: userID, Email: "member@example.com"}

	detailReq := httptest.NewRequest(http.MethodGet, "/releases/"+strconv.FormatInt(releaseID, 10)+"?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	detailReq.SetPathValue("releaseID", strconv.FormatInt(releaseID, 10))
	detailReq = detailReq.WithContext(context.WithValue(detailReq.Context(), userContextKey, user))
	detailRes := httptest.NewRecorder()
	server.releaseDetail(detailRes, detailReq)
	if detailRes.Code != http.StatusOK {
		t.Fatalf("expected release detail, got %d body=%q", detailRes.Code, detailRes.Body.String())
	}
	body := detailRes.Body.String()
	for _, expected := range []string{
		"Gateway 1.2.3",
		"Latest scan failed; showing latest successful scan.",
		"CVE-2026-0002",
		"lib-b",
		"CVE-2026-0001",
		"not shipped",
		"https://nvd.nist.gov/vuln/detail/CVE-2026-0002",
		"Failed",
		"Mark not relevant",
		"Unmark",
		`data-release-events-url="/releases/`,
		`data-release-cves-url="/releases/`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected release detail to contain %q, got %q", expected, body)
		}
	}

	partialReq := httptest.NewRequest(http.MethodGet, "/releases/"+strconv.FormatInt(releaseID, 10)+"/cves?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	partialReq.SetPathValue("releaseID", strconv.FormatInt(releaseID, 10))
	partialReq = partialReq.WithContext(context.WithValue(partialReq.Context(), userContextKey, user))
	partialRes := httptest.NewRecorder()
	server.releaseCVEState(partialRes, partialReq)
	if partialRes.Code != http.StatusOK {
		t.Fatalf("expected release cve partial, got %d body=%q", partialRes.Code, partialRes.Body.String())
	}
	partialBody := partialRes.Body.String()
	if !strings.Contains(partialBody, `id="release-cve-state"`) || strings.Contains(partialBody, "<!doctype html>") {
		t.Fatalf("expected release cve partial only, got %q", partialBody)
	}

	waiveReq := formRequest(http.MethodPost, "/releases/"+strconv.FormatInt(releaseID, 10)+"/cves/CVE-2026-0002/waiver", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
		"note":            {"firmware-only path not linked"},
	}, user)
	waiveReq.SetPathValue("releaseID", strconv.FormatInt(releaseID, 10))
	waiveReq.SetPathValue("cveID", "CVE-2026-0002")
	waiveRes := httptest.NewRecorder()
	server.releaseCVEMarkNotRelevantPost(waiveRes, waiveReq)
	if waiveRes.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after waiver, got %d body=%q", waiveRes.Code, waiveRes.Body.String())
	}
	waivedStatus, err := store.ReleaseCVEImpactStatus(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("release cve status after waiver action: %v", err)
	}
	if waivedStatus.Status != domain.CVEStatusNotImpacted || waivedStatus.ActiveCVECount != 0 || !waivedStatus.HasLatestScanWarning {
		t.Fatalf("expected all CVEs waived to produce not impacted status with warning, got %#v", waivedStatus)
	}
	waivers, err := store.ListReleaseCVEWaivers(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list waivers after waiver action: %v", err)
	}
	if len(waivers) != 2 {
		t.Fatalf("expected two waivers after marking second CVE, got %#v", waivers)
	}
	var sawUserWaiver bool
	for _, waiver := range waivers {
		if waiver.CVEID == "CVE-2026-0002" && waiver.Note == "firmware-only path not linked" && waiver.UserID == userID {
			sawUserWaiver = true
		}
	}
	if !sawUserWaiver {
		t.Fatalf("expected waiver to record note and user id, got %#v", waivers)
	}

	waivedPartialReq := httptest.NewRequest(http.MethodGet, "/releases/"+strconv.FormatInt(releaseID, 10)+"/cves?organisation_id="+strconv.FormatInt(organisationID, 10), nil)
	waivedPartialReq.SetPathValue("releaseID", strconv.FormatInt(releaseID, 10))
	waivedPartialReq = waivedPartialReq.WithContext(context.WithValue(waivedPartialReq.Context(), userContextKey, user))
	waivedPartialRes := httptest.NewRecorder()
	server.releaseCVEState(waivedPartialRes, waivedPartialReq)
	waivedPartialBody := waivedPartialRes.Body.String()
	for _, expected := range []string{"Not impacted", "No active CVEs.", "firmware-only path not linked"} {
		if !strings.Contains(waivedPartialBody, expected) {
			t.Fatalf("expected waived cve partial to contain %q, got %q", expected, waivedPartialBody)
		}
	}

	unwaiveReq := formRequest(http.MethodPost, "/releases/"+strconv.FormatInt(releaseID, 10)+"/cves/CVE-2026-0002/waiver/delete", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
	}, user)
	unwaiveReq.SetPathValue("releaseID", strconv.FormatInt(releaseID, 10))
	unwaiveReq.SetPathValue("cveID", "CVE-2026-0002")
	unwaiveRes := httptest.NewRecorder()
	server.releaseCVEUnmarkNotRelevantPost(unwaiveRes, unwaiveReq)
	if unwaiveRes.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after waiver delete, got %d body=%q", unwaiveRes.Code, unwaiveRes.Body.String())
	}
	unwaivedStatus, err := store.ReleaseCVEImpactStatus(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("release cve status after waiver delete: %v", err)
	}
	if unwaivedStatus.Status != domain.CVEStatusImpacted || unwaivedStatus.ActiveCVECount != 1 {
		t.Fatalf("expected unmarked CVE to return to active impact, got %#v", unwaivedStatus)
	}

	rescanReq := formRequest(http.MethodPost, "/releases/"+strconv.FormatInt(releaseID, 10)+"/rescan", url.Values{
		"organisation_id": {strconv.FormatInt(organisationID, 10)},
	}, user)
	rescanReq.SetPathValue("releaseID", strconv.FormatInt(releaseID, 10))
	rescanRes := httptest.NewRecorder()
	server.releaseRescanPost(rescanRes, rescanReq)
	if rescanRes.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after rescan, got %d body=%q", rescanRes.Code, rescanRes.Body.String())
	}
	if worker.notifications != 1 {
		t.Fatalf("expected worker notification after rescan, got %d", worker.notifications)
	}
	scanRuns, err := store.ListCVEScanRuns(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list scan runs after rescan: %v", err)
	}
	if len(scanRuns) != 3 || scanRuns[0].Status != "pending" || scanRuns[0].Trigger != "manual" {
		t.Fatalf("expected manual pending scan after rescan, got %#v", scanRuns)
	}

	var replaceBody bytes.Buffer
	writer := multipart.NewWriter(&replaceBody)
	if err := writer.WriteField("organisation_id", strconv.FormatInt(organisationID, 10)); err != nil {
		t.Fatalf("write org field: %v", err)
	}
	replacement, err := writer.CreateFormFile("spdx_files", "replacement.spdx")
	if err != nil {
		t.Fatalf("create replacement spdx: %v", err)
	}
	if _, err := replacement.Write([]byte("SPDXVersion: SPDX-2.3\nDataLicense: CC0-1.0\nSPDXID: SPDXRef-DOCUMENT\n")); err != nil {
		t.Fatalf("write replacement spdx: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close replacement body: %v", err)
	}
	replaceReq := httptest.NewRequest(http.MethodPost, "/releases/"+strconv.FormatInt(releaseID, 10)+"/sbom", &replaceBody)
	replaceReq.Header.Set("Content-Type", writer.FormDataContentType())
	replaceReq.SetPathValue("releaseID", strconv.FormatInt(releaseID, 10))
	replaceReq = replaceReq.WithContext(context.WithValue(replaceReq.Context(), userContextKey, user))
	replaceRes := httptest.NewRecorder()
	server.releaseSBOMReplacePost(replaceRes, replaceReq)
	if replaceRes.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after sbom replace, got %d body=%q", replaceRes.Code, replaceRes.Body.String())
	}
	if worker.notifications != 2 {
		t.Fatalf("expected worker notification after sbom replace, got %d", worker.notifications)
	}
	if _, err := os.Stat(filepath.Join(oldSBOMDir, "old.spdx")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected old sbom file to be removed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldSBOMDir, "replacement.spdx")); err != nil {
		t.Fatalf("expected replacement sbom file: %v", err)
	}
	replacedSBOM, err := store.CurrentReleaseSBOM(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("current release sbom after replace: %v", err)
	}
	if replacedSBOM.FileCount != 1 {
		t.Fatalf("unexpected replacement sbom metadata: %#v", replacedSBOM)
	}
	scanRuns, err = store.ListCVEScanRuns(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list scan runs after replace: %v", err)
	}
	if len(scanRuns) != 1 || scanRuns[0].Status != "pending" || scanRuns[0].Trigger != "auto" {
		t.Fatalf("expected replacement to reset scan history and enqueue auto scan, got %#v", scanRuns)
	}
}

type recordingCVEWorker struct {
	notifications int
}

func (w *recordingCVEWorker) Notify() {
	w.notifications++
}

func testServerWithTemplates(t *testing.T, store *db.Store) *Server {
	t.Helper()

	templates := template.Must(template.New("").Funcs(template.FuncMap{"dict": templateDict, "localTime": localTimeElement}).ParseGlob("../../templates/*.html"))
	return &Server{store: store, templates: templates}
}

func formRequest(method string, target string, values url.Values, user domain.User) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req.WithContext(context.WithValue(req.Context(), userContextKey, user))
}

func testDeviceModelID(t *testing.T, store *db.Store, organisationID int64, name string) int64 {
	t.Helper()

	ctx := context.Background()
	models, err := store.ListDeviceModels(ctx, organisationID)
	if err != nil {
		t.Fatalf("list device models: %v", err)
	}
	for _, model := range models {
		if model.Name == name {
			return model.ID
		}
	}

	id, err := store.CreateDeviceModel(ctx, domain.DeviceModel{
		OrganisationID:           organisationID,
		Name:                     name,
		ExpectedHeartbeatSeconds: 60,
		ExpectedProtocol:         "mqtt",
	})
	if err != nil {
		t.Fatalf("create device model: %v", err)
	}
	return id
}

func containsOrganisationID(organisations []domain.Organisation, organisationID int64) bool {
	for _, organisation := range organisations {
		if organisation.ID == organisationID {
			return true
		}
	}
	return false
}

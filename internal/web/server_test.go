package web

import (
	"bytes"
	"context"
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
		"task_parameter":  {"battery"},
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

	otaReq := httptest.NewRequest(http.MethodGet, "/ota-updates?organisation_id="+strconv.FormatInt(forbiddenOrgID, 10), nil)
	otaReq = otaReq.WithContext(context.WithValue(otaReq.Context(), userContextKey, user))
	otaRes := httptest.NewRecorder()
	server.otaUpdates(otaRes, otaReq)
	if otaRes.Code != http.StatusNotFound {
		t.Fatalf("expected forbidden ota page to be hidden, got %d", otaRes.Code)
	}
}

func TestDeviceTaskPostCreatesFOTATaskWithReleaseBinaryPath(t *testing.T) {
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
	releaseID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		Name:                "firmware",
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
	expectedParameter := "https://firmware.example.com/downloads" + releaseBinaryURLPath(releaseID, organisationID)
	if len(tasks) != 1 || tasks[0].Type != "fota" || tasks[0].Parameter != expectedParameter {
		t.Fatalf("unexpected FOTA task: %#v", tasks)
	}

	select {
	case published := <-publisher.tasks:
		if published.organisationID != organisationID || published.task.ID != tasks[0].ID || published.task.Type != "fota" || published.task.Parameter != expectedParameter {
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
	releaseID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		Name:                "firmware",
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
	if len(models) != 1 || models[0].Name != "Gateway v1" || models[0].ExpectedHeartbeatSeconds != 120 || models[0].ExpectedProtocol != "mqtt" {
		t.Fatalf("unexpected device model: %#v", models)
	}
	if models[0].ExpectedReleaseID == nil || *models[0].ExpectedReleaseID != releaseID || models[0].ExpectedReleaseVersion != "1.2.3" {
		t.Fatalf("unexpected expected release on model: %#v", models[0])
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

	server := &Server{store: store}
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
		DeviceID:  "device-001",
		Type:      "fota",
		Parameter: "/org/1/releases/1/binary",
		Status:    db.DeviceTaskStatusPending,
		CreatedAt: "2026-06-07T08:00:00Z",
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
	if err := writer.WriteField("name", "firmware"); err != nil {
		t.Fatalf("write name field: %v", err)
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
	if release.ArtifactFilename != "firmware.bin" || release.ArtifactSizeBytes != int64(len("firmware-bytes")) {
		t.Fatalf("unexpected artifact metadata: %#v", release)
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

func testServerWithTemplates(t *testing.T, store *db.Store) *Server {
	t.Helper()

	templates := template.Must(template.New("").Funcs(template.FuncMap{"dict": templateDict}).ParseGlob("../../templates/*.html"))
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

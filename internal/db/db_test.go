package db

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"anchor/internal/domain"
)

func TestOpenSQLiteCreatesSchema(t *testing.T) {
	t.Parallel()

	store, err := Open(context.Background(), Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	if store.readDB == store.writeDB {
		t.Fatal("expected sqlite store to use separate read and write pools")
	}
	if got := store.readDB.Stats().MaxOpenConnections; got != sqliteReadMaxOpenConns {
		t.Fatalf("expected sqlite read pool max open connections to be %d, got %d", sqliteReadMaxOpenConns, got)
	}
	if got := store.writeDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("expected sqlite write pool max open connections to be 1, got %d", got)
	}

	assertColumns(t, store, "app_users", []string{
		"id",
		"email",
		"name",
		"password_hash",
		"is_admin",
		"created_at",
		"updated_at",
	})
	assertColumns(t, store, "organisations", []string{
		"id",
		"name",
		"created_at",
		"updated_at",
	})
	assertColumns(t, store, "organisation_users", []string{
		"organisation_id",
		"user_id",
		"role",
		"created_at",
	})
	assertColumns(t, store, "organisation_invitations", []string{
		"id",
		"organisation_id",
		"email",
		"token_hash",
		"expires_at",
		"accepted_at",
		"inviter_user_id",
		"created_at",
	})
	assertColumns(t, store, "devices", []string{
		"id",
		"organisation_id",
		"device_model_id",
		"software_versions",
		"is_gateway",
		"created_at",
		"updated_at",
	})
	assertColumns(t, store, "device_models", []string{
		"id",
		"organisation_id",
		"name",
		"expected_heartbeat_seconds",
		"expected_protocol",
		"expected_release_id",
		"created_at",
		"updated_at",
	})
	assertColumns(t, store, "mqtt_credentials", []string{
		"device_id",
		"username",
		"password_hash",
		"enabled",
		"created_at",
		"updated_at",
	})
	assertColumns(t, store, "device_events", []string{
		"id",
		"device_id",
		"ts_received_ms",
		"protocol",
		"direction",
		"operation",
		"topic",
		"coap_path",
		"method",
		"code",
		"content_format",
		"payload_raw",
		"payload_json",
		"correlation_id",
		"schema_hint",
		"source",
		"retained",
	})
	assertColumns(t, store, "device_twin_properties", []string{
		"device_id",
		"path",
		"value_json",
		"value_type",
		"source_event_id",
		"ts_observed_ms",
		"ts_received_ms",
		"protocol",
		"source_path",
	})
	assertColumns(t, store, "device_tasks", []string{
		"id",
		"device_id",
		"task_type",
		"parameters_json",
		"status",
		"status_message",
		"created_at",
		"completed_at",
	})
	assertColumns(t, store, "software_releases", []string{
		"id",
		"organisation_id",
		"device_model_id",
		"version",
		"artifact_path",
		"artifact_filename",
		"artifact_content_type",
		"artifact_size_bytes",
		"created_at",
	})
	assertColumns(t, store, "software_release_sboms", []string{
		"id",
		"organisation_id",
		"release_id",
		"file_count",
		"total_size_bytes",
		"created_at",
		"updated_at",
	})
	assertColumns(t, store, "ota_deployments", []string{
		"id",
		"organisation_id",
		"release_id",
		"target",
		"status",
		"created_at",
	})
	assertColumns(t, store, "cve_scan_runs", []string{
		"id",
		"organisation_id",
		"release_id",
		"release_sbom_id",
		"trigger_source",
		"status",
		"error_message",
		"created_at",
		"started_at",
		"finished_at",
	})
	assertColumns(t, store, "cve_scan_findings", []string{
		"id",
		"organisation_id",
		"release_id",
		"scan_run_id",
		"cve_id",
		"severity",
		"package_name",
		"installed_version",
		"created_at",
	})
	assertColumns(t, store, "release_cve_waivers", []string{
		"id",
		"organisation_id",
		"release_id",
		"cve_id",
		"note",
		"user_id",
		"created_at",
		"updated_at",
	})
	assertColumns(t, store, "sessions", []string{
		"id",
		"user_id",
		"expires_at",
		"created_at",
	})
}

func TestListOrganisationsForUser(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	alphaID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Alpha"})
	if err != nil {
		t.Fatalf("create alpha organisation: %v", err)
	}
	betaID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Beta"})
	if err != nil {
		t.Fatalf("create beta organisation: %v", err)
	}

	adminID, err := store.CreateUser(ctx, domain.User{
		Email:        "admin@example.test",
		Name:         "Admin",
		PasswordHash: "hash",
		IsAdmin:      true,
	})
	if err != nil {
		t.Fatalf("create admin user: %v", err)
	}
	memberID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.test",
		Name:         "Member",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create member user: %v", err)
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         memberID,
		OrganisationID: betaID,
	}); err != nil {
		t.Fatalf("add member to organisation: %v", err)
	}

	adminOrganisations, err := store.ListOrganisationsForUser(ctx, domain.User{ID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatalf("list admin organisations: %v", err)
	}
	if len(adminOrganisations) != 4 {
		t.Fatalf("expected admin to access 4 organisations, got %d", len(adminOrganisations))
	}

	memberOrganisations, err := store.ListOrganisationsForUser(ctx, domain.User{ID: memberID})
	if err != nil {
		t.Fatalf("list member organisations: %v", err)
	}
	if !containsOrganisationID(memberOrganisations, betaID) || len(memberOrganisations) != 2 {
		t.Fatalf("expected member to access beta and a personal organisation, got %#v", memberOrganisations)
	}

	if alphaID == betaID {
		t.Fatal("expected distinct organisation IDs")
	}
}

func TestCreateUserCreatesPersonalOrganisationWithAdminRole(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	firstID, err := store.CreateUser(ctx, domain.User{
		Email:        "first@example.test",
		Name:         "Alex",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create first user: %v", err)
	}
	secondID, err := store.CreateUser(ctx, domain.User{
		Email:        "second@example.test",
		Name:         "Alex",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create second user: %v", err)
	}

	firstOrganisations, err := store.ListOrganisationsForUser(ctx, domain.User{ID: firstID})
	if err != nil {
		t.Fatalf("list first organisations: %v", err)
	}
	secondOrganisations, err := store.ListOrganisationsForUser(ctx, domain.User{ID: secondID})
	if err != nil {
		t.Fatalf("list second organisations: %v", err)
	}
	if len(firstOrganisations) != 1 || firstOrganisations[0].Name != "Alex's organisation" {
		t.Fatalf("unexpected first personal organisation: %#v", firstOrganisations)
	}
	if len(secondOrganisations) != 1 || secondOrganisations[0].Name != "Alex's organisation 2" {
		t.Fatalf("unexpected second personal organisation: %#v", secondOrganisations)
	}

	isAdmin, err := store.IsOrganisationAdmin(ctx, firstID, firstOrganisations[0].ID)
	if err != nil {
		t.Fatalf("check personal admin: %v", err)
	}
	if !isAdmin {
		t.Fatal("expected user to be admin of personal organisation")
	}
}

func TestCreateOrganisationForUserCreatesAdminMembership(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	userID, err := store.CreateUser(ctx, domain.User{
		Email:        "creator@example.test",
		Name:         "Creator",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	organisationID, err := store.CreateOrganisationForUser(ctx, domain.Organisation{Name: "Shared"}, userID)
	if err != nil {
		t.Fatalf("create organisation for user: %v", err)
	}

	isAdmin, err := store.IsOrganisationAdmin(ctx, userID, organisationID)
	if err != nil {
		t.Fatalf("check organisation admin: %v", err)
	}
	if !isAdmin {
		t.Fatal("expected creator to be organisation admin")
	}
	if _, err := store.CreateOrganisationForUser(ctx, domain.Organisation{Name: "Shared"}, userID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate organisation name conflict, got %v", err)
	}
}

func TestRemoveOrganisationMemberRejectsLastAdmin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	adminID, err := store.CreateUser(ctx, domain.User{
		Email:        "admin@example.test",
		Name:         "Admin",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	memberID, err := store.CreateUser(ctx, domain.User{
		Email:        "member@example.test",
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
		Role:           OrganisationRoleMember,
	}); err != nil {
		t.Fatalf("add member: %v", err)
	}

	if err := store.RemoveOrganisationMember(ctx, organisationID, adminID); !errors.Is(err, ErrLastOrganisationAdmin) {
		t.Fatalf("expected last admin removal to be rejected, got %v", err)
	}
	if err := store.RemoveOrganisationMember(ctx, organisationID, memberID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	members, err := store.ListOrganisationMembers(ctx, organisationID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 || members[0].UserID != adminID {
		t.Fatalf("unexpected members after removal: %#v", members)
	}
}

func TestOrganisationInvitations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	adminID, err := store.CreateUser(ctx, domain.User{
		Email:        "admin@example.test",
		Name:         "Admin",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	existingID, err := store.CreateUser(ctx, domain.User{
		Email:        "existing@example.test",
		Name:         "Existing",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create existing user: %v", err)
	}
	organisationID, err := store.CreateOrganisationForUser(ctx, domain.Organisation{Name: "Shared"}, adminID)
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)

	existingInvite, err := store.InviteUserToOrganisation(ctx, organisationID, adminID, "existing@example.test", now)
	if err != nil {
		t.Fatalf("invite existing user: %v", err)
	}
	if !existingInvite.ExistingUser || existingInvite.Token != "" {
		t.Fatalf("expected existing user to be added without token, got %#v", existingInvite)
	}
	if organisations, err := store.ListOrganisationsForUser(ctx, domain.User{ID: existingID}); err != nil {
		t.Fatalf("list existing user organisations: %v", err)
	} else if !containsOrganisationID(organisations, organisationID) {
		t.Fatalf("expected existing user to be added to shared organisation, got %#v", organisations)
	}

	firstInvite, err := store.InviteUserToOrganisation(ctx, organisationID, adminID, "new@example.test", now)
	if err != nil {
		t.Fatalf("invite missing user: %v", err)
	}
	if firstInvite.ExistingUser || len(firstInvite.Token) != 64 {
		t.Fatalf("expected generated token for missing user, got %#v", firstInvite)
	}
	var tokenHash string
	var expiresAt string
	if err := store.readDB.QueryRowContext(ctx, `SELECT token_hash, expires_at FROM organisation_invitations WHERE email = ?`, "new@example.test").Scan(&tokenHash, &expiresAt); err != nil {
		t.Fatalf("read invitation: %v", err)
	}
	if tokenHash == firstInvite.Token || len(tokenHash) != 64 {
		t.Fatalf("expected hashed token to be stored, got %q", tokenHash)
	}
	if expiresAt != now.Add(InvitationTTL).Format(time.RFC3339) {
		t.Fatalf("unexpected expiry: got %q", expiresAt)
	}

	secondInvite, err := store.InviteUserToOrganisation(ctx, organisationID, adminID, "new@example.test", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("re-invite missing user: %v", err)
	}
	if secondInvite.Token == firstInvite.Token {
		t.Fatal("expected replacement invitation token")
	}
	if _, err := store.InvitationByToken(ctx, firstInvite.Token, now.Add(time.Hour)); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("expected old token to be invalid after replacement, got %v", err)
	}

	acceptance, err := store.AcceptInvitation(ctx, secondInvite.Token, domain.User{
		Name:         "New User",
		PasswordHash: "hash",
	}, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("accept invitation: %v", err)
	}
	if acceptance.OrganisationID != organisationID || acceptance.UserID <= 0 {
		t.Fatalf("unexpected acceptance: %#v", acceptance)
	}
	if organisations, err := store.ListOrganisationsForUser(ctx, domain.User{ID: acceptance.UserID}); err != nil {
		t.Fatalf("list accepted user organisations: %v", err)
	} else if len(organisations) != 2 || !containsOrganisationID(organisations, organisationID) {
		t.Fatalf("expected accepted user personal and invited orgs, got %#v", organisations)
	}
	if _, err := store.InvitationByToken(ctx, secondInvite.Token, now.Add(2*time.Hour)); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("expected accepted token to be invalid, got %v", err)
	}

	expiredInvite, err := store.InviteUserToOrganisation(ctx, organisationID, adminID, "expired@example.test", now)
	if err != nil {
		t.Fatalf("create expired invite: %v", err)
	}
	if _, err := store.InvitationByToken(ctx, expiredInvite.Token, now.Add(8*24*time.Hour)); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("expected expired token to be invalid, got %v", err)
	}
	if _, err := store.InvitationByToken(ctx, "not-a-token", now); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("expected invalid token to be rejected, got %v", err)
	}
}

func TestListSoftwareReleasesAndOngoingOTADeployments(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
	otherOrganisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: t.Name() + " other"})
	if err != nil {
		t.Fatalf("create other organisation: %v", err)
	}
	deviceModelID := testDeviceModelID(t, store, organisationID, "Gateway")
	otherDeviceModelID := testDeviceModelID(t, store, otherOrganisationID, "Gateway")

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
		t.Fatalf("create software release: %v", err)
	}
	if _, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		DeviceModelID:       deviceModelID,
		Version:             "1.2.3",
		ArtifactPath:        "1/firmware-copy.bin",
		ArtifactFilename:    "firmware-copy.bin",
		ArtifactContentType: "application/octet-stream",
		ArtifactSizeBytes:   4,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate model/version release conflict, got %v", err)
	}
	otherReleaseID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      otherOrganisationID,
		DeviceModelID:       otherDeviceModelID,
		Version:             "9.9.9",
		ArtifactPath:        "2/firmware.bin",
		ArtifactFilename:    "firmware.bin",
		ArtifactContentType: "application/octet-stream",
		ArtifactSizeBytes:   4,
	})
	if err != nil {
		t.Fatalf("create other software release: %v", err)
	}
	if _, err := store.writeDB.ExecContext(ctx, `INSERT INTO ota_deployments (organisation_id, release_id, target, status) VALUES (?, ?, 'all devices', 'running')`, organisationID, releaseID); err != nil {
		t.Fatalf("insert running ota deployment: %v", err)
	}
	if _, err := store.writeDB.ExecContext(ctx, `INSERT INTO ota_deployments (organisation_id, release_id, target, status) VALUES (?, ?, 'lab devices', 'completed')`, organisationID, releaseID); err != nil {
		t.Fatalf("insert completed ota deployment: %v", err)
	}
	if _, err := store.writeDB.ExecContext(ctx, `INSERT INTO ota_deployments (organisation_id, release_id, target, status) VALUES (?, ?, 'other devices', 'running')`, otherOrganisationID, otherReleaseID); err != nil {
		t.Fatalf("insert other ota deployment: %v", err)
	}

	releases, err := store.ListSoftwareReleases(ctx, organisationID)
	if err != nil {
		t.Fatalf("list software releases: %v", err)
	}
	if len(releases) != 1 || releases[0].OrganisationID != organisationID || releases[0].DeviceModelID != deviceModelID || releases[0].DeviceModelName != "Gateway" || releases[0].Version != "1.2.3" {
		t.Fatalf("unexpected releases: %#v", releases)
	}
	if releases[0].ArtifactFilename != "firmware.bin" || releases[0].ArtifactSizeBytes != 4 {
		t.Fatalf("unexpected release artifact metadata: %#v", releases[0])
	}

	release, err := store.SoftwareRelease(ctx, releaseID, organisationID)
	if err != nil {
		t.Fatalf("get software release: %v", err)
	}
	if release.ID != releaseID || release.DeviceModelID != deviceModelID || release.DeviceModelName != "Gateway" || release.ArtifactPath != "1/firmware.bin" {
		t.Fatalf("unexpected software release: %#v", release)
	}

	deployments, err := store.ListOngoingOTADeployments(ctx, organisationID)
	if err != nil {
		t.Fatalf("list ongoing ota deployments: %v", err)
	}
	if len(deployments) != 1 || deployments[0].OrganisationID != organisationID || deployments[0].Status != "running" || deployments[0].ReleaseVersion != "1.2.3" {
		t.Fatalf("unexpected deployments: %#v", deployments)
	}

	if _, err := store.writeDB.ExecContext(ctx, `INSERT INTO ota_deployments (organisation_id, release_id, target, status) VALUES (?, ?, 'bad link', 'running')`, organisationID, otherReleaseID); err == nil {
		t.Fatal("expected cross-organisation release deployment to fail")
	}
}

func TestUpdateDeviceModelExpectedRelease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
	otherOrganisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: t.Name() + " other"})
	if err != nil {
		t.Fatalf("create other organisation: %v", err)
	}
	modelID := testDeviceModelID(t, store, organisationID, "Gateway")
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
		t.Fatalf("create software release: %v", err)
	}

	if err := store.UpdateDeviceModelExpectedRelease(ctx, organisationID, modelID, &releaseID); err != nil {
		t.Fatalf("update expected release: %v", err)
	}
	model, err := store.DeviceModel(ctx, modelID, organisationID)
	if err != nil {
		t.Fatalf("load device model: %v", err)
	}
	if model.ExpectedReleaseID == nil || *model.ExpectedReleaseID != releaseID || model.ExpectedReleaseModelName != "Gateway" || model.ExpectedReleaseVersion != "1.2.3" {
		t.Fatalf("unexpected expected release after update: %#v", model)
	}

	if err := store.UpdateDeviceModelExpectedRelease(ctx, organisationID, modelID, nil); err != nil {
		t.Fatalf("clear expected release: %v", err)
	}
	model, err = store.DeviceModel(ctx, modelID, organisationID)
	if err != nil {
		t.Fatalf("load cleared device model: %v", err)
	}
	if model.ExpectedReleaseID != nil || model.ExpectedReleaseModelName != "" || model.ExpectedReleaseVersion != "" {
		t.Fatalf("expected release should be cleared: %#v", model)
	}

	if err := store.UpdateDeviceModelExpectedRelease(ctx, otherOrganisationID, modelID, &releaseID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected wrong organisation update to fail as not found, got %v", err)
	}
}

func TestCVEPersistenceTracksCurrentSBOMScansFindingsAndWaivers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
	deviceModelID := testDeviceModelID(t, store, organisationID, "Gateway")
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
		t.Fatalf("create software release: %v", err)
	}

	if _, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "auto"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected enqueue without sbom to fail as not found, got %v", err)
	}
	noSBOMStatus, err := store.ReleaseCVEImpactStatus(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("release cve status without sbom: %v", err)
	}
	if noSBOMStatus.Status != domain.CVEStatusNoSBOM {
		t.Fatalf("expected no sbom status, got %#v", noSBOMStatus)
	}

	sbom, err := store.ReplaceReleaseSBOM(ctx, organisationID, releaseID, 2, 128)
	if err != nil {
		t.Fatalf("replace release sbom: %v", err)
	}
	if sbom.ReleaseID != releaseID || sbom.FileCount != 2 || sbom.TotalSizeBytes != 128 {
		t.Fatalf("unexpected sbom metadata: %#v", sbom)
	}
	currentSBOM, err := store.CurrentReleaseSBOM(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("current release sbom: %v", err)
	}
	if currentSBOM.ID != sbom.ID {
		t.Fatalf("unexpected current sbom: %#v", currentSBOM)
	}

	run, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "auto")
	if err != nil {
		t.Fatalf("enqueue scan: %v", err)
	}
	if run.Status != "pending" || run.ReleaseSBOMID != sbom.ID {
		t.Fatalf("unexpected enqueued run: %#v", run)
	}
	if _, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "manual"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate active scan conflict, got %v", err)
	}
	if err := store.StartCVEScanRun(ctx, organisationID, run.ID, "2026-06-29T10:00:00Z"); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	if err := store.CompleteCVEScanRun(ctx, organisationID, run.ID, "2026-06-29T10:01:00Z", []domain.CVEScanFinding{
		{CVEID: " CVE-2026-0001 ", Severity: "high", PackageName: "lib-a", InstalledVersion: "1.0.0"},
		{CVEID: "CVE-2026-0001", Severity: "high", PackageName: "lib-a", InstalledVersion: "1.0.0"},
		{CVEID: "CVE-2026-0002", Severity: "medium", PackageName: "lib-b", InstalledVersion: "2.0.0"},
	}); err != nil {
		t.Fatalf("complete scan: %v", err)
	}

	latest, err := store.LatestSuccessfulCVEScanRun(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("latest successful scan: %v", err)
	}
	if latest.ID != run.ID || latest.Status != "success" {
		t.Fatalf("unexpected latest successful scan: %#v", latest)
	}
	findings, err := store.ListCurrentCVEFindings(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list current findings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected deduped current findings, got %#v", findings)
	}
	failedRun, err := store.EnqueueCVEScan(ctx, organisationID, releaseID, "manual")
	if err != nil {
		t.Fatalf("enqueue failed scan: %v", err)
	}
	if err := store.FailCVEScanRun(ctx, organisationID, failedRun.ID, "2026-06-29T10:02:00Z", "scanner failed"); err != nil {
		t.Fatalf("fail scan: %v", err)
	}
	latest, err = store.LatestSuccessfulCVEScanRun(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("latest successful scan after failed attempt: %v", err)
	}
	if latest.ID != run.ID {
		t.Fatalf("expected latest successful scan to ignore later failed scan, got %#v", latest)
	}
	runsBeforeReplacement, err := store.ListCVEScanRuns(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list scan runs: %v", err)
	}
	if len(runsBeforeReplacement) != 2 {
		t.Fatalf("expected successful and failed scan history, got %#v", runsBeforeReplacement)
	}
	status, err := store.ReleaseCVEImpactStatus(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("release cve status after failed latest: %v", err)
	}
	if status.Status != domain.CVEStatusImpacted || !status.HasLatestScanWarning || status.ActiveCVECount != 2 || status.HighestActiveSeverity != "high" {
		t.Fatalf("unexpected failed-latest release status: %#v", status)
	}

	waiver, err := store.UpsertReleaseCVEWaiver(ctx, domain.ReleaseCVEWaiver{
		OrganisationID: organisationID,
		ReleaseID:      releaseID,
		CVEID:          " CVE-2026-0001 ",
		Note:           " not relevant for this build ",
		UserID:         42,
	})
	if err != nil {
		t.Fatalf("upsert waiver: %v", err)
	}
	if waiver.CVEID != "CVE-2026-0001" || waiver.Note != "not relevant for this build" || waiver.UserID != 42 {
		t.Fatalf("unexpected waiver: %#v", waiver)
	}
	activeFindings, err := store.ListActiveCVEFindings(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list active findings: %v", err)
	}
	if len(activeFindings) != 1 || activeFindings[0].CVEID != "CVE-2026-0002" {
		t.Fatalf("expected waiver to filter active findings, got %#v", activeFindings)
	}
	waivers, err := store.ListReleaseCVEWaivers(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list waivers: %v", err)
	}
	if len(waivers) != 1 || waivers[0].CVEID != "CVE-2026-0001" {
		t.Fatalf("unexpected waivers: %#v", waivers)
	}
	status, err = store.ReleaseCVEImpactStatus(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("release cve status after waiver: %v", err)
	}
	if status.Status != domain.CVEStatusImpacted || status.ActiveCVECount != 1 || status.HighestActiveSeverity != "medium" || !status.HasLatestScanWarning {
		t.Fatalf("expected waiver-filtered impacted status with warning, got %#v", status)
	}
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-matched",
			OrganisationID:   organisationID,
			DeviceModelID:    deviceModelID,
			SoftwareVersions: domain.SoftwareVersions{"firmware": "1.2.3"},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-matched",
			Username:     "device-matched",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save matched device: %v", err)
	}
	deviceStatus, err := store.DeviceCVEImpactStatus(ctx, organisationID, "device-matched")
	if err != nil {
		t.Fatalf("matched device cve status: %v", err)
	}
	if deviceStatus.Status != domain.CVEStatusImpacted || deviceStatus.MatchedReleaseID != releaseID || deviceStatus.ActiveCVECount != 1 {
		t.Fatalf("unexpected matched device cve status: %#v", deviceStatus)
	}
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-unknown-release",
			OrganisationID:   organisationID,
			DeviceModelID:    deviceModelID,
			SoftwareVersions: domain.SoftwareVersions{"firmware": "9.9.9"},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-unknown-release",
			Username:     "device-unknown-release",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save unknown-release device: %v", err)
	}
	unknownDeviceStatus, err := store.DeviceCVEImpactStatus(ctx, organisationID, "device-unknown-release")
	if err != nil {
		t.Fatalf("unknown-release device cve status: %v", err)
	}
	if unknownDeviceStatus.Status != domain.CVEStatusUnknownRelease {
		t.Fatalf("expected unknown release device status, got %#v", unknownDeviceStatus)
	}

	if _, err := store.ReplaceReleaseSBOM(ctx, organisationID, releaseID, 1, 64); err != nil {
		t.Fatalf("replace release sbom again: %v", err)
	}
	runs, err := store.ListCVEScanRuns(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list scan runs after sbom replacement: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected sbom replacement to clear scan history, got %#v", runs)
	}
	if _, err := store.LatestSuccessfulCVEScanRun(ctx, organisationID, releaseID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected no latest successful scan after sbom replacement, got %v", err)
	}
	waivers, err = store.ListReleaseCVEWaivers(ctx, organisationID, releaseID)
	if err != nil {
		t.Fatalf("list waivers after sbom replacement: %v", err)
	}
	if len(waivers) != 1 {
		t.Fatalf("expected release-scoped waiver to survive sbom replacement, got %#v", waivers)
	}

	if err := store.ClearReleaseSBOM(ctx, organisationID, releaseID); err != nil {
		t.Fatalf("clear release sbom: %v", err)
	}
	if _, err := store.CurrentReleaseSBOM(ctx, organisationID, releaseID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected no current sbom after clear, got %v", err)
	}
}

func TestSaveDeviceWithMQTTCredential(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
	err = store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-001",
			OrganisationID:   organisationID,
			DeviceModelID:    testDeviceModelID(t, store, organisationID, "Gateway"),
			ModelName:        "Gateway",
			SoftwareVersions: domain.SoftwareVersions{"firmware": "1.0.0"},
			IsGateway:        true,
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-001",
			Username:     "device-001",
			PasswordHash: "hash",
			Enabled:      true,
		},
	})
	if err != nil {
		t.Fatalf("save device with mqtt credential: %v", err)
	}

	credential, err := store.FindMQTTCredentialByUsername(ctx, "device-001")
	if err != nil {
		t.Fatalf("find mqtt credential: %v", err)
	}
	if credential.DeviceID != "device-001" || !credential.Enabled {
		t.Fatalf("unexpected credential: %#v", credential)
	}

	principal, err := store.FindMQTTPrincipalByUsername(ctx, "device-001")
	if err != nil {
		t.Fatalf("find mqtt principal: %v", err)
	}
	if principal.DeviceID != "device-001" || principal.OrganisationID != organisationID || !principal.IsGateway || !principal.Enabled {
		t.Fatalf("unexpected mqtt principal: %#v", principal)
	}

	devices, err := store.ListDevicesWithMQTT(ctx, organisationID)
	if err != nil {
		t.Fatalf("list devices with mqtt: %v", err)
	}
	if len(devices) != 1 || devices[0].Device.OrganisationID != organisationID || !devices[0].Device.IsGateway {
		t.Fatalf("unexpected devices with mqtt: %#v", devices)
	}

	detail, err := store.DeviceDetail(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("load device detail: %v", err)
	}
	if detail.Device.ID != "device-001" || detail.MQTTCredential == nil || detail.MQTTCredential.Username != "device-001" {
		t.Fatalf("unexpected device detail: %#v", detail)
	}
	if detail.Device.OrganisationID != organisationID || !detail.Device.IsGateway {
		t.Fatalf("unexpected device detail organisation/gateway: %#v", detail.Device)
	}
}

func TestDeleteDeviceRemovesMQTTConfig(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-001",
			OrganisationID:   organisationID,
			DeviceModelID:    testDeviceModelID(t, store, organisationID, "Gateway"),
			ModelName:        "Gateway",
			SoftwareVersions: domain.SoftwareVersions{},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-001",
			Username:     "device-001",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save device with mqtt credential: %v", err)
	}

	if err := store.DeleteDevice(ctx, "device-001", organisationID); err != nil {
		t.Fatalf("delete device: %v", err)
	}

	if _, err := store.FindMQTTCredentialByUsername(ctx, "device-001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected credential to be removed, got %v", err)
	}
	exists, err := store.DeviceExistsInOrganisation(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("check deleted device: %v", err)
	}
	if exists {
		t.Fatal("expected device to be removed")
	}
}

func TestRecordDeviceEventUpdatesTwin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
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
		t.Fatalf("save device with mqtt credential: %v", err)
	}

	eventID, err := store.RecordDeviceEvent(ctx, domain.DeviceEvent{
		DeviceID:      "device-001",
		TSReceivedMS:  1234,
		Protocol:      "mqtt",
		Direction:     "inbound",
		Operation:     "publish",
		Topic:         "dev/1/device-001/data",
		ContentFormat: "application/json",
		PayloadJSON:   `{"battery":87}`,
		Source:        "broker",
	}, []domain.DeviceTwinProperty{
		{
			Path:         "battery",
			ValueJSON:    "87",
			ValueType:    "number",
			TSObservedMS: 1200,
		},
		{
			Path:         "firmware",
			ValueJSON:    `" 1.2.3 "`,
			ValueType:    "string",
			TSObservedMS: 1200,
		},
	})
	if err != nil {
		t.Fatalf("record device event: %v", err)
	}
	if eventID == 0 {
		t.Fatal("expected event id")
	}

	properties, err := store.ListDeviceTwinProperties(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("list twin properties: %v", err)
	}
	if len(properties) != 2 || properties[0].Path != "battery" || properties[0].ValueJSON != "87" || properties[1].Path != "firmware" {
		t.Fatalf("unexpected twin properties: %#v", properties)
	}
	if properties[0].SourceEventID == nil || *properties[0].SourceEventID != eventID {
		t.Fatalf("unexpected source event id: %#v", properties[0].SourceEventID)
	}

	events, err := store.ListRecentDeviceEvents(ctx, "device-001", organisationID, 10)
	if err != nil {
		t.Fatalf("list recent device events: %v", err)
	}
	if len(events) != 1 || events[0].ID != eventID || events[0].PayloadJSON != `{"battery":87}` {
		t.Fatalf("unexpected recent events: %#v", events)
	}

	detail, err := store.DeviceDetail(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("load device detail: %v", err)
	}
	if detail.Device.ExpectedHeartbeatSeconds != 60 || detail.Device.LastEventReceivedMS != 1234 {
		t.Fatalf("unexpected device heartbeat/event fields: %#v", detail.Device)
	}
	if detail.Device.SoftwareVersions["firmware"] != "1.2.3" {
		t.Fatalf("expected firmware software version to be trimmed and stored, got %#v", detail.Device.SoftwareVersions)
	}

	if _, err := store.RecordDeviceEvent(ctx, domain.DeviceEvent{
		DeviceID:      "device-001",
		TSReceivedMS:  1235,
		Protocol:      "mqtt",
		Direction:     "inbound",
		Operation:     "publish",
		Topic:         "dev/1/device-001/data",
		ContentFormat: "application/json",
		PayloadJSON:   `{"firmware":123}`,
		Source:        "broker",
	}, []domain.DeviceTwinProperty{
		{
			Path:         "firmware",
			ValueJSON:    "123",
			ValueType:    "number",
			TSObservedMS: 1235,
		},
	}); err != nil {
		t.Fatalf("record non-string firmware event: %v", err)
	}
	detail, err = store.DeviceDetail(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("load device detail after non-string firmware: %v", err)
	}
	if detail.Device.SoftwareVersions["firmware"] != "1.2.3" {
		t.Fatalf("expected non-string firmware telemetry not to update software versions, got %#v", detail.Device.SoftwareVersions)
	}
}

func TestDeviceEventSubscriptionsArePerDevice(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &Store{events: newDeviceEventNotifier()}
	deviceAEvents, unsubscribeA := store.SubscribeDeviceEvents(ctx, "device-a")
	defer unsubscribeA()
	deviceBEvents, unsubscribeB := store.SubscribeDeviceEvents(ctx, "device-b")
	defer unsubscribeB()

	store.events.publish("device-a")

	select {
	case <-deviceAEvents:
	case <-time.After(time.Second):
		t.Fatal("expected device-a notification")
	}

	select {
	case <-deviceBEvents:
		t.Fatal("did not expect device-b notification")
	default:
	}
}

func TestReleaseCVEScanSubscriptionsArePerRelease(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
	modelID := testDeviceModelID(t, store, organisationID, "Gateway")
	releaseAID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		DeviceModelID:       modelID,
		Version:             "1.0.0",
		ArtifactPath:        "1/firmware-a.bin",
		ArtifactFilename:    "firmware-a.bin",
		ArtifactContentType: "application/octet-stream",
		ArtifactSizeBytes:   4,
	})
	if err != nil {
		t.Fatalf("create release a: %v", err)
	}
	releaseBID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{
		OrganisationID:      organisationID,
		DeviceModelID:       modelID,
		Version:             "2.0.0",
		ArtifactPath:        "1/firmware-b.bin",
		ArtifactFilename:    "firmware-b.bin",
		ArtifactContentType: "application/octet-stream",
		ArtifactSizeBytes:   4,
	})
	if err != nil {
		t.Fatalf("create release b: %v", err)
	}
	if _, err := store.ReplaceReleaseSBOM(ctx, organisationID, releaseAID, 1, 64); err != nil {
		t.Fatalf("replace release a sbom: %v", err)
	}
	if _, err := store.ReplaceReleaseSBOM(ctx, organisationID, releaseBID, 1, 64); err != nil {
		t.Fatalf("replace release b sbom: %v", err)
	}

	releaseAEvents, unsubscribeA := store.SubscribeReleaseCVEScans(ctx, organisationID, releaseAID)
	defer unsubscribeA()
	releaseBEvents, unsubscribeB := store.SubscribeReleaseCVEScans(ctx, organisationID, releaseBID)
	defer unsubscribeB()

	if _, err := store.EnqueueCVEScan(ctx, organisationID, releaseAID, "manual"); err != nil {
		t.Fatalf("enqueue release a scan: %v", err)
	}

	select {
	case <-releaseAEvents:
	case <-time.After(time.Second):
		t.Fatal("expected release-a notification")
	}

	select {
	case <-releaseBEvents:
		t.Fatal("did not expect release-b notification")
	default:
	}
}

func TestDeviceTaskSubscriptionsArePerDevice(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-a",
			OrganisationID:   organisationID,
			DeviceModelID:    testDeviceModelID(t, store, organisationID, "Sensor"),
			ModelName:        "Sensor",
			SoftwareVersions: domain.SoftwareVersions{},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-a",
			Username:     "device-a",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save device-a: %v", err)
	}
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-b",
			OrganisationID:   organisationID,
			DeviceModelID:    testDeviceModelID(t, store, organisationID, "Sensor"),
			ModelName:        "Sensor",
			SoftwareVersions: domain.SoftwareVersions{},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-b",
			Username:     "device-b",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save device-b: %v", err)
	}

	deviceATasks, unsubscribeA := store.SubscribeDeviceTasks(ctx, "device-a")
	defer unsubscribeA()
	deviceBTasks, unsubscribeB := store.SubscribeDeviceTasks(ctx, "device-b")
	defer unsubscribeB()

	if _, err := store.CreateDeviceTask(ctx, domain.DeviceTask{
		DeviceID:       "device-a",
		Type:           "fota",
		ParametersJSON: `{"release_id":1}`,
		Status:         DeviceTaskStatusPending,
		CreatedAt:      "2026-06-06T08:00:00Z",
	}, organisationID); err != nil {
		t.Fatalf("create task: %v", err)
	}

	select {
	case <-deviceATasks:
	case <-time.After(time.Second):
		t.Fatal("expected device-a task notification")
	}

	select {
	case <-deviceBTasks:
		t.Fatal("did not expect device-b task notification")
	default:
	}
}

func TestCreateAndListOngoingDeviceTasks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
	otherOrganisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: t.Name() + " other"})
	if err != nil {
		t.Fatalf("create other organisation: %v", err)
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
		t.Fatalf("save device with mqtt credential: %v", err)
	}

	readID, err := store.CreateDeviceTask(ctx, domain.DeviceTask{
		DeviceID:       "device-001",
		Type:           "read",
		ParametersJSON: `{"paths":["battery"]}`,
		Status:         DeviceTaskStatusPending,
		CreatedAt:      "2026-06-04T08:00:00Z",
	}, organisationID)
	if err != nil {
		t.Fatalf("create read task: %v", err)
	}
	writeID, err := store.CreateDeviceTask(ctx, domain.DeviceTask{
		DeviceID:       "device-001",
		Type:           "write",
		ParametersJSON: `{"values":[{"path":"led","value":"on"}]}`,
		Status:         DeviceTaskStatusInProgress,
		CreatedAt:      "2026-06-04T08:01:00Z",
	}, organisationID)
	if err != nil {
		t.Fatalf("create write task: %v", err)
	}
	completedID, err := store.CreateDeviceTask(ctx, domain.DeviceTask{
		DeviceID:       "device-001",
		Type:           "fota",
		ParametersJSON: `{"release_id":1}`,
		Status:         DeviceTaskStatusPending,
		CreatedAt:      "2026-06-04T08:02:00Z",
	}, organisationID)
	if err != nil {
		t.Fatalf("create completed task: %v", err)
	}
	if err := store.UpdateDeviceTaskStatus(ctx, completedID, "device-001", organisationID, DeviceTaskStatusSuccess, "2026-06-04T08:03:00Z", "done"); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if err := store.UpdateDeviceTaskStatus(ctx, completedID, "device-001", organisationID, DeviceTaskStatusInProgress, "", "again"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected terminal task regression to be rejected, got %v", err)
	}

	tasks, err := store.ListOngoingDeviceTasks(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("list ongoing tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected two ongoing tasks, got %#v", tasks)
	}
	if tasks[0].ID != writeID || tasks[0].Status != DeviceTaskStatusInProgress {
		t.Fatalf("expected newest in-progress task first, got %#v", tasks[0])
	}
	if tasks[1].ID != readID || tasks[1].ParametersJSON != `{"paths":["battery"]}` {
		t.Fatalf("expected pending read task second, got %#v", tasks[1])
	}

	pendingTasks, err := store.ListPendingDeviceTasks(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("list pending tasks: %v", err)
	}
	if len(pendingTasks) != 1 || pendingTasks[0].ID != readID {
		t.Fatalf("expected only pending read task, got %#v", pendingTasks)
	}

	tasks, err = store.ListOngoingDeviceTasks(ctx, "device-001", otherOrganisationID)
	if err != nil {
		t.Fatalf("list tasks from other organisation: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected no cross-organisation tasks, got %#v", tasks)
	}
}

func TestListActiveAndRecentDeviceTasksIncludesLastThreeFinished(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
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
		t.Fatalf("save device with mqtt credential: %v", err)
	}

	activeID, err := store.CreateDeviceTask(ctx, domain.DeviceTask{
		DeviceID:       "device-001",
		Type:           "fota",
		ParametersJSON: `{"release_id":1}`,
		Status:         DeviceTaskStatusPending,
		CreatedAt:      "2026-06-07T08:10:00Z",
	}, organisationID)
	if err != nil {
		t.Fatalf("create active task: %v", err)
	}

	var finishedIDs []int64
	for i := 1; i <= 4; i++ {
		id, err := store.CreateDeviceTask(ctx, domain.DeviceTask{
			DeviceID:       "device-001",
			Type:           "fota",
			ParametersJSON: fmt.Sprintf(`{"release_id":%d}`, i),
			Status:         DeviceTaskStatusPending,
			CreatedAt:      fmt.Sprintf("2026-06-07T08:0%d:00Z", i),
		}, organisationID)
		if err != nil {
			t.Fatalf("create finished task %d: %v", i, err)
		}
		if err := store.UpdateDeviceTaskStatus(ctx, id, "device-001", organisationID, DeviceTaskStatusSuccess, fmt.Sprintf("2026-06-07T08:1%d:00Z", i), "done"); err != nil {
			t.Fatalf("finish task %d: %v", i, err)
		}
		finishedIDs = append(finishedIDs, id)
	}

	tasks, err := store.ListActiveAndRecentDeviceTasks(ctx, "device-001", organisationID, 3)
	if err != nil {
		t.Fatalf("list active and recent tasks: %v", err)
	}
	if len(tasks) != 4 {
		t.Fatalf("expected active plus three finished tasks, got %#v", tasks)
	}
	if tasks[0].ID != activeID || tasks[0].Status != DeviceTaskStatusPending {
		t.Fatalf("expected active task first, got %#v", tasks[0])
	}
	wantFinished := []int64{finishedIDs[3], finishedIDs[2], finishedIDs[1]}
	for i, wantID := range wantFinished {
		if tasks[i+1].ID != wantID || tasks[i+1].CompletedAt == "" {
			t.Fatalf("unexpected finished task at %d: got %#v want id %d", i+1, tasks[i+1], wantID)
		}
	}
}

func TestCreateDeviceTaskRequiresMatchingOrganisation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID := testOrganisationID(t, store)
	otherOrganisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: t.Name() + " other"})
	if err != nil {
		t.Fatalf("create other organisation: %v", err)
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
		t.Fatalf("save device with mqtt credential: %v", err)
	}

	_, err = store.CreateDeviceTask(ctx, domain.DeviceTask{
		DeviceID:       "device-001",
		Type:           "read",
		ParametersJSON: `{"paths":["battery"]}`,
		Status:         DeviceTaskStatusPending,
		CreatedAt:      "2026-06-04T08:00:00Z",
	}, otherOrganisationID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for wrong organisation, got %v", err)
	}
}

func testOrganisationID(t *testing.T, store *Store) int64 {
	t.Helper()

	id, err := store.CreateOrganisation(context.Background(), domain.Organisation{Name: t.Name()})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	return id
}

func testDeviceModelID(t *testing.T, store *Store, organisationID int64, name string) int64 {
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

func assertColumns(t *testing.T, store *Store, table string, expected []string) {
	t.Helper()

	rows, err := store.readDB.Query("PRAGMA table_info(" + table + ");")
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()

	found := make(map[string]bool)
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}

	for _, column := range expected {
		if !found[column] {
			t.Fatalf("expected %s.%s to exist", table, column)
		}
	}
}

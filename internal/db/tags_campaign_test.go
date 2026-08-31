package db

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"anchor/internal/domain"
)

func openTagTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), Config{Dialect: DialectSQLite, DSN: filepath.Join(t.TempDir(), "anchor.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestDeviceTagsNormalizeFilterAndRemainOrganisationScoped(t *testing.T) {
	ctx := context.Background()
	store := openTagTestStore(t)
	org := testOrganisationID(t, store)
	other, err := store.CreateOrganisation(ctx, domain.Organisation{Name: t.Name() + " other"})
	if err != nil {
		t.Fatal(err)
	}
	modelA := testDeviceModelID(t, store, org, "A")
	modelB := testDeviceModelID(t, store, org, "B")
	otherModel := testDeviceModelID(t, store, other, "Other")
	for _, device := range []domain.Device{
		{ID: "a-1", OrganisationID: org, DeviceModelID: modelA, SoftwareVersions: domain.SoftwareVersions{}, Tags: []string{" Beta ", "factory.floor"}},
		{ID: "a-2", OrganisationID: org, DeviceModelID: modelA, SoftwareVersions: domain.SoftwareVersions{}, Tags: []string{"beta", "factory_floor"}},
		{ID: "b-1", OrganisationID: org, DeviceModelID: modelB, SoftwareVersions: domain.SoftwareVersions{}, Tags: []string{"beta", "factory.floor"}},
		{ID: "other-1", OrganisationID: other, DeviceModelID: otherModel, SoftwareVersions: domain.SoftwareVersions{}, Tags: []string{"secret"}},
	} {
		if err := store.SaveDevice(ctx, device); err != nil {
			t.Fatalf("save %s: %v", device.ID, err)
		}
	}

	tags, err := store.DeviceTags(ctx, "a-1", org)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"beta", "factory.floor"}; !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
	page, err := store.ListDevicePage(ctx, DeviceListQuery{OrganisationID: org, Tags: []string{"BETA", "factory.floor"}, DeviceModelID: modelA, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Device.ID != "a-1" {
		t.Fatalf("unexpected filtered rows: %+v", page.Rows)
	}
	suggestions, err := store.ListTagSuggestions(ctx, org, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"beta", "factory.floor", "factory_floor"}; !reflect.DeepEqual(suggestions, want) {
		t.Fatalf("suggestions = %v, want %v", suggestions, want)
	}
	prefixed, err := store.ListTagSuggestions(ctx, org, "factory_")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prefixed, []string{"factory_floor"}) {
		t.Fatalf("literal prefix suggestions = %v", prefixed)
	}
	if err := store.ReplaceDeviceTags(ctx, "a-1", org, []string{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.DeviceTags(ctx, "a-1", org); len(got) != 0 {
		t.Fatalf("expected cleared tags, got %v", got)
	}
}

func TestBulkTagUpdateIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store := openTagTestStore(t)
	org := testOrganisationID(t, store)
	model := testDeviceModelID(t, store, org, "A")
	for _, id := range []string{"one", "two"} {
		if err := store.SaveDevice(ctx, domain.Device{ID: id, OrganisationID: org, DeviceModelID: model, SoftwareVersions: domain.SoftwareVersions{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateDeviceTagsBulk(ctx, org, []string{"one", "two"}, " Beta ", true); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDeviceTagsBulk(ctx, org, []string{"one", "two"}, "beta", true); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDeviceTagsBulk(ctx, org, []string{"one", "missing"}, "new", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	for _, id := range []string{"one", "two"} {
		got, _ := store.DeviceTags(ctx, id, org)
		if !reflect.DeepEqual(got, []string{"beta"}) {
			t.Fatalf("%s tags = %v", id, got)
		}
	}
	if err := store.UpdateDeviceTagsBulk(ctx, org, []string{"one", "two"}, "beta", false); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDeviceTagsBulk(ctx, org, []string{"one", "two"}, "beta", false); err != nil {
		t.Fatal(err)
	}
}

func TestCampaignSelectorsResolveAtomicallyAndPersistExpression(t *testing.T) {
	ctx := context.Background()
	store := openTagTestStore(t)
	org := testOrganisationID(t, store)
	modelA := testDeviceModelID(t, store, org, "A")
	modelB := testDeviceModelID(t, store, org, "B")
	for _, device := range []domain.Device{
		{ID: "a-1", OrganisationID: org, DeviceModelID: modelA, SoftwareVersions: domain.SoftwareVersions{}, Tags: []string{"beta"}},
		{ID: "a-2", OrganisationID: org, DeviceModelID: modelA, SoftwareVersions: domain.SoftwareVersions{}, Tags: []string{"stable"}},
		{ID: "b-1", OrganisationID: org, DeviceModelID: modelB, SoftwareVersions: domain.SoftwareVersions{}, Tags: []string{"beta"}},
	} {
		if err := store.SaveDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name     string
		selector CampaignTargetSelector
		count    int
	}{
		{"model", CampaignTargetSelector{OrganisationID: org, TargetType: CampaignTargetModel, ModelID: modelA}, 2},
		{"tag", CampaignTargetSelector{OrganisationID: org, TargetType: CampaignTargetTag, Tag: "BETA"}, 2},
		{"tag model", CampaignTargetSelector{OrganisationID: org, TargetType: CampaignTargetTagModel, Tag: "beta", ModelID: modelA}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.EstimateCampaignTargets(ctx, tc.selector)
			if err != nil || got != tc.count {
				t.Fatalf("count=%d err=%v", got, err)
			}
		})
	}

	result, err := store.CreateCampaign(ctx, CampaignCreate{OrganisationID: org, Name: "Beta A", TaskType: domain.TaskTypeRead, ParametersJSON: `{"paths":["battery"]}`, TTLSeconds: 60, TargetType: CampaignTargetTagModel, TargetTag: "Beta", TargetModelID: modelA, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Campaign.TargetCount != 1 {
		t.Fatalf("target count = %d", result.Campaign.TargetCount)
	}
	stored, err := store.Campaign(ctx, org, result.Campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TargetType != CampaignTargetTagModel || stored.TargetTag != "beta" || stored.TargetModelID != modelA || stored.TargetModelName != "A" || len(stored.TargetDeviceIDs) != 0 {
		t.Fatalf("unexpected snapshot: %+v", stored)
	}
	if err := store.ReplaceDeviceTags(ctx, "a-1", org, []string{"stable"}); err != nil {
		t.Fatal(err)
	}
	stored, err = store.Campaign(ctx, org, result.Campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TargetCount != 1 {
		t.Fatalf("campaign membership changed after tag update: %d", stored.TargetCount)
	}
	explicit, err := store.CreateCampaign(ctx, CampaignCreate{OrganisationID: org, Name: "Explicit", TaskType: domain.TaskTypeRead, ParametersJSON: `{"paths":["battery"]}`, TTLSeconds: 60, DeviceIDs: []string{"b-1", "a-2"}})
	if err != nil {
		t.Fatal(err)
	}
	explicitStored, err := store.Campaign(ctx, org, explicit.Campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(explicitStored.TargetDeviceIDs, []string{"a-2", "b-1"}) {
		t.Fatalf("explicit snapshot not sorted: %v", explicitStored.TargetDeviceIDs)
	}
}

func TestTagOnlyFOTARejectsAnyIncompatibleDeviceWithoutPartialCampaign(t *testing.T) {
	ctx := context.Background()
	store := openTagTestStore(t)
	org := testOrganisationID(t, store)
	modelA := testDeviceModelID(t, store, org, "A")
	modelB := testDeviceModelID(t, store, org, "B")
	for _, device := range []domain.Device{{ID: "a", OrganisationID: org, DeviceModelID: modelA, SoftwareVersions: domain.SoftwareVersions{}, Tags: []string{"beta"}}, {ID: "b", OrganisationID: org, DeviceModelID: modelB, SoftwareVersions: domain.SoftwareVersions{}, Tags: []string{"beta"}}} {
		if err := store.SaveDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
	}
	releaseID, err := store.CreateSoftwareRelease(ctx, domain.SoftwareRelease{OrganisationID: org, DeviceModelID: modelA, Version: "1.0", ArtifactPath: "a.bin", ArtifactFilename: "a.bin", ArtifactContentType: "application/octet-stream", ArtifactSizeBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	params, _ := domain.BuildFOTATaskParameters(releaseID)
	_, err = store.CreateCampaign(ctx, CampaignCreate{OrganisationID: org, Name: "bad", TaskType: domain.TaskTypeFOTA, ParametersJSON: params, TTLSeconds: 60, TargetType: CampaignTargetTag, TargetTag: "beta"})
	if err == nil {
		t.Fatal("expected incompatible FOTA selector to fail")
	}
	campaigns, loadErr := store.ListCampaigns(ctx, org)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(campaigns) != 0 {
		t.Fatalf("partial campaign created: %+v", campaigns)
	}
}

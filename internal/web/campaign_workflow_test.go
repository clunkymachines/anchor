package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"anchor/internal/db"
	"anchor/internal/domain"

	"golang.org/x/net/html"
)

func campaignWorkflowFixture(t *testing.T) (*Server, domain.User, int64, int64) {
	t.Helper()
	ctx := context.Background()
	store, orgID, modelID := testAPIStore(t, ctx)
	t.Cleanup(func() { store.Close() })
	userID, err := store.CreateUser(ctx, domain.User{Email: "campaign-test@example.test", Name: "Campaign Test", PasswordHash: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	user := domain.User{ID: userID, Email: "campaign-test@example.test", Name: "Campaign Test"}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{UserID: userID, OrganisationID: orgID}); err != nil {
		t.Fatal(err)
	}
	otherModel := testDeviceModelID(t, store, orgID, "Other model")
	for _, device := range []domain.Device{
		{ID: "beta-a", OrganisationID: orgID, DeviceModelID: modelID, Tags: []string{"beta"}},
		{ID: "prod-a", OrganisationID: orgID, DeviceModelID: modelID, Tags: []string{"production"}},
		{ID: "beta-b", OrganisationID: orgID, DeviceModelID: otherModel, Tags: []string{"beta"}},
	} {
		if err := store.SaveDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
	}
	foreignOrg, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Other organisation"})
	if err != nil {
		t.Fatal(err)
	}
	foreignModel := testDeviceModelID(t, store, foreignOrg, "Foreign model")
	if err := store.SaveDevice(ctx, domain.Device{ID: "foreign-device", OrganisationID: foreignOrg, DeviceModelID: foreignModel, Tags: []string{"beta"}}); err != nil {
		t.Fatal(err)
	}
	return testServerWithTemplates(t, store), user, orgID, modelID
}

// Parse the rendered DOM, not merely strings, to catch nested or externally
// associated controls that accidentally stop belonging to the creation form.
func campaignFormInputs(t *testing.T, body string) url.Values {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{}
	var visit func(*html.Node, bool)
	visit = func(node *html.Node, inForm bool) {
		attrs := map[string]string{}
		for _, attr := range node.Attr {
			attrs[attr.Key] = attr.Val
		}
		if node.Type == html.ElementNode && node.Data == "form" {
			inForm = attrs["id"] == "campaign-create-form"
		}
		if inForm && attrs["form"] != "" {
			t.Error("campaign controls should be inside the creation form")
		}
		if inForm && node.Data == "input" && attrs["name"] != "" {
			values.Add(attrs["name"], attrs["value"])
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child, inForm)
		}
	}
	visit(doc, false)
	return values
}

func TestCampaignCreationEntryPaths(t *testing.T) {
	server, user, orgID, _ := campaignWorkflowFixture(t)
	org := strconv.FormatInt(orgID, 10)
	for _, task := range []string{"read", "write", "fota"} {
		for _, selected := range []bool{false, true} {
			t.Run(task+"/selected="+strconv.FormatBool(selected), func(t *testing.T) {
				res := httptest.NewRecorder()
				if selected {
					server.campaignNewPost(res, formRequest(http.MethodPost, "/campaigns/new", url.Values{"organisation_id": {org}, "task_type": {task}, "device_id": {"beta-a"}}, user))
				} else {
					server.campaignNew(res, formRequest(http.MethodGet, "/campaigns/new?organisation_id="+org+"&task_type="+task, nil, user))
				}
				if res.Code != http.StatusOK {
					t.Fatalf("status %d: %s", res.Code, res.Body.String())
				}
				values := campaignFormInputs(t, res.Body.String())
				wantType := "filters"
				if selected {
					wantType = "explicit"
				}
				if values.Get("target_type") != wantType || values.Get("task_type") != task || values.Get("organisation_id") != org {
					t.Fatalf("wrong creation form: %#v", values)
				}
				if selected {
					if !reflect.DeepEqual(values["device_id"], []string{"beta-a"}) {
						t.Fatalf("wrong explicit selection: %#v", values)
					}
					if strings.Contains(res.Body.String(), `name="target_tag"`) || strings.Contains(res.Body.String(), `name="target_model_id"`) {
						t.Fatal("selected-device flow must not offer selector filters")
					}
				} else if len(values["device_id"]) != 0 || !strings.Contains(res.Body.String(), `name="target_model_id"`) {
					t.Fatal("filter flow must offer filters without a device selection")
				}
				if strings.Contains(res.Body.String(), `type="radio"`) {
					t.Fatal("targeting mode picker should not be rendered")
				}
			})
		}
	}
	res := httptest.NewRecorder()
	server.campaignNewPost(res, formRequest(http.MethodPost, "/campaigns/new", url.Values{"organisation_id": {org}, "task_type": {"read"}}, user))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("empty device selection should be rejected, got %d", res.Code)
	}
	res = httptest.NewRecorder()
	server.campaignNewPost(res, formRequest(http.MethodPost, "/campaigns/new", url.Values{"organisation_id": {org}, "task_type": {"read"}, "device_id": {"foreign-device"}}, user))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("foreign device selection should be rejected, got %d", res.Code)
	}
}

func TestCampaignWriteAndFOTACreation(t *testing.T) {
	for _, task := range []string{"write", "fota"} {
		for _, selected := range []bool{false, true} {
			t.Run(task+"/selected="+strconv.FormatBool(selected), func(t *testing.T) {
				server, user, orgID, modelID := campaignWorkflowFixture(t)
				values := url.Values{"organisation_id": {strconv.FormatInt(orgID, 10)}, "target_type": {"filters"}, "task_type": {task}, "name": {"Launch"}, "ttl_days": {"7"}}
				if selected {
					values.Set("target_type", "explicit")
					values.Add("device_id", "beta-a")
				} else {
					values.Set("target_model_id", strconv.FormatInt(modelID, 10))
				}
				if task == "write" {
					values.Set("write_values", `[{"path":"config.interval","value":15}]`)
				} else {
					releaseID, err := server.store.CreateSoftwareRelease(context.Background(), domain.SoftwareRelease{OrganisationID: orgID, DeviceModelID: modelID, Version: "1.2.3", ArtifactPath: "test.bin", ArtifactFilename: "test.bin", ArtifactContentType: "application/octet-stream"})
					if err != nil {
						t.Fatal(err)
					}
					values.Set("release_id", strconv.FormatInt(releaseID, 10))
				}
				res := httptest.NewRecorder()
				server.campaignsPost(res, formRequest(http.MethodPost, "/campaigns", values, user))
				if res.Code != http.StatusSeeOther {
					t.Fatalf("create %s: %d %s", task, res.Code, res.Body.String())
				}
			})
		}
	}
}

func TestCampaignFilterEstimateAndCreation(t *testing.T) {
	for _, mode := range []string{"explicit", "tag", "model", "tag_model"} {
		t.Run(mode, func(t *testing.T) {
			server, user, orgID, modelID := campaignWorkflowFixture(t)
			values := url.Values{"organisation_id": {strconv.FormatInt(orgID, 10)}, "target_type": {"filters"}, "task_type": {"read"}, "name": {"test campaign"}, "ttl_days": {"3"}, "read_paths": {"battery"}}
			wantIDs := []string{"beta-a"}
			switch mode {
			case "explicit":
				values.Set("target_type", "explicit")
				values.Add("device_id", "beta-a")
			case "tag":
				values.Set("target_tag", " BETA ")
				wantIDs = []string{"beta-a", "beta-b"}
			case "model":
				values.Set("target_model_id", strconv.FormatInt(modelID, 10))
				wantIDs = []string{"beta-a", "prod-a"}
			case "tag_model":
				values.Set("target_tag", "beta")
				values.Set("target_model_id", strconv.FormatInt(modelID, 10))
			}
			estimate := httptest.NewRecorder()
			server.campaignEstimate(estimate, formRequest(http.MethodGet, "/campaigns/estimate?"+values.Encode(), nil, user))
			var payload struct {
				Count int `json:"count"`
			}
			if err := json.Unmarshal(estimate.Body.Bytes(), &payload); err != nil || estimate.Code != http.StatusOK || payload.Count != len(wantIDs) {
				t.Fatalf("estimate: %d %s", estimate.Code, estimate.Body.String())
			}
			res := httptest.NewRecorder()
			server.campaignsPost(res, formRequest(http.MethodPost, "/campaigns", values, user))
			if res.Code != http.StatusSeeOther {
				t.Fatalf("create: %d %s", res.Code, res.Body.String())
			}
			campaigns, err := server.store.ListCampaigns(context.Background(), orgID)
			if err != nil || len(campaigns) != 1 {
				t.Fatalf("campaigns: %#v, %v", campaigns, err)
			}
			campaign := campaigns[0]
			if campaign.TargetType != mode || campaign.TargetCount != len(wantIDs) {
				t.Fatalf("wrong campaign target: %#v", campaign)
			}
			page, err := server.store.ListCampaignTasks(context.Background(), db.CampaignTaskQuery{OrganisationID: orgID, CampaignID: campaign.ID})
			if err != nil {
				t.Fatal(err)
			}
			gotIDs := []string{}
			for _, row := range page.Rows {
				gotIDs = append(gotIDs, row.Task.DeviceID)
			}
			slices.Sort(gotIDs)
			if !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Fatalf("task targets: %v, want %v", gotIDs, wantIDs)
			}
		})
	}
}

func TestCampaignInvalidFiltersPreserveFormAndCreateNothing(t *testing.T) {
	server, user, orgID, modelID := campaignWorkflowFixture(t)
	for _, tag := range []string{"", "absent"} {
		values := url.Values{"organisation_id": {strconv.FormatInt(orgID, 10)}, "target_type": {"filters"}, "target_tag": {tag}, "task_type": {"write"}, "name": {"Keep this name"}, "ttl_days": {"9"}, "write_values": {`[{"path":"config.interval","value":15}]`}}
		res := httptest.NewRecorder()
		server.campaignsPost(res, formRequest(http.MethodPost, "/campaigns", values, user))
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `class="form-error"`) {
			t.Fatalf("expected inline error, got %d %s", res.Code, res.Body.String())
		}
		form := campaignFormInputs(t, res.Body.String())
		if form.Get("name") != values.Get("name") || form.Get("ttl_days") != "9" || form.Get("target_tag") != tag || !strings.Contains(res.Body.String(), "config.interval") {
			t.Fatal("validation lost the submitted fields")
		}
	}
	for _, values := range []url.Values{
		{"target_type": {"filters"}, "target_model_id": {"invalid"}},
		{"target_type": {"filters"}, "target_model_id": {"-1"}},
		{"target_type": {"filters"}, "target_tag": {"beta"}, "device_id": {"beta-a"}},
		{"target_type": {"explicit"}, "target_model_id": {strconv.FormatInt(modelID, 10)}, "device_id": {"beta-a"}},
	} {
		values.Set("organisation_id", strconv.FormatInt(orgID, 10))
		res := httptest.NewRecorder()
		server.campaignEstimate(res, formRequest(http.MethodGet, "/campaigns/estimate?"+values.Encode(), nil, user))
		if res.Code != http.StatusBadRequest {
			t.Fatalf("invalid targeting accepted: %v", values)
		}
	}
	campaigns, err := server.store.ListCampaigns(context.Background(), orgID)
	if err != nil || len(campaigns) != 0 {
		t.Fatalf("invalid campaign persisted: %#v, %v", campaigns, err)
	}
}

func TestWriteTaskFormAcceptsArrayAndObject(t *testing.T) {
	server := &Server{}
	for _, input := range []string{`[{"path":"config.interval","value":15}]`, `{"values":[{"path":"config.interval","value":15}]}`} {
		req := formRequest(http.MethodPost, "/campaigns", url.Values{"write_values": {input}}, domain.User{})
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		encoded, err := server.taskParametersFromForm(req, domain.TaskTypeWrite, 0)
		if err != nil {
			t.Fatalf("valid editor payload rejected: %v", err)
		}
		var params domain.WriteTaskParameters
		if err := json.Unmarshal([]byte(encoded), &params); err != nil || len(params.Values) != 1 || params.Values[0].Path != "config.interval" || string(params.Values[0].Value) != "15" {
			t.Fatalf("wrong wire payload: %s (%v)", encoded, err)
		}
	}
}

func TestCampaignHistoryOffersDirectCreation(t *testing.T) {
	server := &Server{templates: parseTemplates()}
	res := httptest.NewRecorder()
	server.renderCampaigns(res, campaignsPageData{Shell: shellPageData{SelectedOrganisationID: 42}})
	if res.Code != http.StatusOK {
		t.Fatal(res.Body.String())
	}
	for _, task := range []string{"read", "write", "fota"} {
		if !strings.Contains(res.Body.String(), `/campaigns/new?organisation_id=42&task_type=`+task) {
			t.Errorf("missing direct creation entry for %s", task)
		}
	}
}

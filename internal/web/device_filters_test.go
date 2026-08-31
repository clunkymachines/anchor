package web

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDeviceTagFilterOptionsPreserveActiveTags(t *testing.T) {
	got := deviceTagFilterOptions([]string{"beta", "production"}, []string{"production", "retired"})
	want := []tagOptionView{{Name: "production", Selected: true}, {Name: "retired", Selected: true}, {Name: "beta"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter options = %#v, want %#v", got, want)
	}
}

func TestDeviceFilterToolbarRendering(t *testing.T) {
	server := &Server{templates: parseTemplates()}
	res := httptest.NewRecorder()
	server.renderDevices(res, httptest.NewRequest(http.MethodGet, "/devices", nil), devicesPageData{
		Shell:      shellPageData{SelectedOrganisationID: 42},
		Query:      "sensor & test",
		Pagination: paginationView{PageSize: 50},
		ActiveTags: []string{"beta", "production"},
		TagOptions: deviceTagFilterOptions([]string{"beta", "production", "test"}, []string{"beta", "production"}),
		HasFilters: true,
		ReturnURL:  "/devices?organisation_id=42&page=2&tag=beta&tag=production",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("render devices: status %d, body %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		`class="device-filters"`,
		`name="page_size" value="50"`,
		`name="q" value="sensor &amp; test"`,
		`type="checkbox" name="tag" value="beta" checked`,
		`type="checkbox" name="tag" value="production" checked`,
		`for="tag-filter-search">Search tags</label>`,
		`data-remove-filter-tag="beta"`,
		`data-bulk-tag-form hidden`,
		`data-device-selection-count role="status"`,
		`href="/devices?organisation_id=42&amp;page=2&amp;tag=beta&amp;tag=production"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("device toolbar is missing %q", want)
		}
	}
	if strings.Contains(body, "filter-multiselect") {
		t.Error("device toolbar still renders the native multi-select")
	}
	if strings.Index(body, `Device inventory`) > strings.Index(body, `class="device-filters"`) {
		t.Error("filters should appear below the inventory heading")
	}
}

func TestDeviceSearchSharesTheInventoryFilterForm(t *testing.T) {
	for _, query := range []string{"", "sensor & test"} {
		t.Run(query, func(t *testing.T) {
			server := &Server{templates: parseTemplates()}
			res := httptest.NewRecorder()
			server.renderDevices(res, httptest.NewRequest(http.MethodGet, "/devices", nil), devicesPageData{
				Shell:      shellPageData{SelectedOrganisationID: 42},
				Query:      query,
				Pagination: paginationView{PageSize: 50},
			})
			if res.Code != http.StatusOK {
				t.Fatalf("render devices: %d %s", res.Code, res.Body.String())
			}
			body := res.Body.String()
			topbar := strings.Split(strings.Split(body, `<header class="app-topbar">`)[1], `</header>`)[0]
			if strings.Contains(topbar, `type="search"`) {
				t.Error("device search must not be duplicated in the global top bar")
			}
			filters := strings.Split(strings.Split(body, `<form class="device-filters"`)[1], `</form>`)[0]
			if !strings.Contains(filters, `id="device-filter-search" type="search" name="q"`) || strings.Count(filters, `name="q"`) != 1 {
				t.Error("the inventory filter form must contain one editable search field")
			}
			if !strings.Contains(filters, `name="model_id"`) || !strings.Contains(filters, `type="submit">Apply filters`) {
				t.Error("search and model must share the apply action")
			}
			clear := `href="/devices?organisation_id=42&page_size=50">Clear filters</a>`
			if strings.Contains(filters, clear) != (query != "") {
				t.Error("clear filters should reset search-only filtering while preserving organisation and page size")
			}
		})
	}
}

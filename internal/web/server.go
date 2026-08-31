package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"anchor/internal/coapapi"
	"anchor/internal/coapcontrol"
	"anchor/internal/cve"
	"anchor/internal/db"
	"anchor/internal/domain"
	"anchor/internal/taskdispatch"
	staticassets "anchor/static"
	templateassets "anchor/templates"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName        = "anchor_session"
	sessionDuration          = 7 * 24 * time.Hour
	defaultReleaseDir        = "anchor-data/releases"
	maxFirmwareUpload        = 256 << 20
	maxReleaseSBOMFiles      = 10
	maxReleaseSBOMFileSize   = 25 << 20
	maxReleaseSBOMTotalSize  = 100 << 20
	maxReleaseUpload         = maxFirmwareUpload + maxReleaseSBOMTotalSize
	releaseSBOMFormFieldName = "spdx_files"
	maxBulkUpsertDevices     = 2000
)

type contextKey string

const userContextKey contextKey = "user"
const apiCredentialContextKey contextKey = "api_credential"

// Server owns the dependencies and configuration used by Anchor's HTTP handlers.
type Server struct {
	store                  *db.Store
	templates              *template.Template
	internalMQTTClientAuth InternalMQTTClientAuthConfig
	mqttIntegrationRuntime MQTTIntegrationRuntime
	taskPublisher          DeviceTaskPublisher
	cveScanWorker          CVEScanWorker
	releaseStorageDir      string
	fotaDownloadBaseURL    string
	coAPInternalToken      string
	coAPIntegrationEnabled bool
	coAPIntegrationRuntime CoAPIntegrationRuntime
	coAPInvalidator        CoAPCredentialInvalidator
}

// ServerConfig supplies the optional integrations and storage settings used by
// NewServer. Zero-valued optional dependencies disable their related runtime
// behavior.
type ServerConfig struct {
	InternalMQTTClientAuth InternalMQTTClientAuthConfig
	MQTTIntegrationRuntime MQTTIntegrationRuntime
	TaskPublisher          DeviceTaskPublisher
	CoAPTaskPublisher      DeviceTaskPublisher
	ReleaseStorageDir      string
	FOTADownloadBaseURL    string
	CoAPInternalToken      string
	CoAPIntegrationEnabled bool
	CoAPIntegrationRuntime CoAPIntegrationRuntime
	CoAPInvalidator        CoAPCredentialInvalidator
	CVEScanWorkerEnabled   bool
	CVEScannerPath         string
	CVEScanWorker          CVEScanWorker
}

// InternalMQTTClientAuthConfig authorizes Anchor's own MQTT broker client in MQTT auth callbacks.
type InternalMQTTClientAuthConfig struct {
	Username string
	Password string
}

// DeviceTaskPublisher sends newly created and pending tasks to devices.
type DeviceTaskPublisher interface {
	PublishDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) error
	PublishPendingDeviceTasks(ctx context.Context, deviceID string, organisationID int64) error
}

// MQTTIntegrationRuntime applies saved MQTT settings and exposes the active internal credentials.
type MQTTIntegrationRuntime interface {
	DeviceTaskPublisher
	ApplyMQTTIntegration(ctx context.Context, config domain.MQTTIntegrationConfig) error
	InternalMQTTCredentials() (username string, password string, enabled bool)
	MQTTIntegrationStatus() domain.MQTTIntegrationStatus
}

// CVEScanWorker wakes the asynchronous CVE scan processor after work is queued.
type CVEScanWorker interface {
	Notify()
}

// CoAPIntegrationRuntime probes the private frontend without exposing its
// bearer token to browser requests.
type CoAPIntegrationRuntime interface {
	IntegrationStatus(context.Context) domain.CoAPIntegrationStatus
}

type CoAPAssociationRuntime interface {
	Association(context.Context, string) (coapapi.AssociationStatus, error)
}

type CoAPConfigRuntime interface {
	ApplyCoAPIntegration(context.Context, domain.CoAPIntegrationConfig) error
}

type CoAPCredentialInvalidator interface {
	Invalidate(context.Context, string, int64, bool) error
}

type loginPageData struct {
	Error string
	Email string
}

type shellPageData struct {
	User                   domain.User
	Organisations          []domain.Organisation
	SelectedOrganisationID int64
}

type integrationsPageData struct {
	Shell                     shellPageData
	MQTT                      domain.MQTTIntegrationConfig
	MQTTFormError             string
	MQTTMessage               string
	MQTTStatus                string
	MQTTStatusClass           string
	MQTTConnectionStatus      string
	MQTTConnectionStatusClass string
	MQTTConnectionReason      string
	MQTTConnectionUpdatedAt   string
	PasswordConfigured        bool
	CoAP                      domain.CoAPIntegrationConfig
	CoAPFormError             string
	CoAPMessage               string
	CoAPStatus                string
	CoAPStatusClass           string
	CoAPFrontendStatus        string
	CoAPFrontendStatusClass   string
	CoAPFrontendReason        string
	CoAPTokenConfigured       bool
}

type devicesPageData struct {
	Shell                 shellPageData
	Devices               []deviceView
	Metrics               db.DeviceFleetMetrics
	FilteredCount         int
	Query                 string
	Pagination            paginationView
	HasQuery              bool
	IsEmptyUnfiltered     bool
	IsEmptyFiltered       bool
	DeviceInventoryLabel  string
	DeviceInventorySuffix string
	DeviceModels          []deviceModelOptionView
	TagSuggestions        []string
	TagOptions            []tagOptionView
	ActiveTags            []string
	ModelID               int64
	HasFilters            bool
	ReturnURL             string
}

type campaignSelectionPageData struct {
	Shell          shellPageData
	Devices        []campaignDevicePreviewView
	Releases       []releaseOptionView
	FormError      string
	Name           string
	TaskType       string
	TaskLabel      string
	TaskHelp       string
	ReadPaths      string
	WriteValues    string
	TTLDays        int
	ReleaseID      int64
	CanUseFOTA     bool
	FOTAHelpText   string
	DeviceModels   []deviceModelOptionView
	TagSuggestions []string
	TargetType     string
	TargetTag      string
	TargetModelID  int64
	EstimatedCount int
}

type campaignsPageData struct {
	Shell     shellPageData
	Campaigns []campaignView
}

type campaignDetailPageData struct {
	Shell        shellPageData
	Campaign     campaignView
	Tasks        []campaignTaskView
	StatusFilter string
	Pagination   paginationView
}

type deviceCreatePageData struct {
	Shell           shellPageData
	DeviceModels    []deviceModelOptionView
	FormError       string
	DeviceFormNote  string
	DeviceID        string
	MQTTUsername    string
	CoAPPSKIdentity string
	IsGateway       bool
	TagSuggestions  []string
	TagsInput       string
}

type deviceCreatedPageData struct {
	Shell       shellPageData
	DeviceID    string
	ModelName   string
	PSKIdentity string
	PSKHex      string
}

type coAPCredentialCreatedPageData struct {
	Shell       shellPageData
	DeviceID    string
	PSKIdentity string
	PSKHex      string
}

type deviceDetailPageData struct {
	Shell                shellPageData
	Device               deviceDetailView
	MQTTCredential       *mqttCredentialView
	CoAPCredential       *coAPCredentialView
	TwinProperties       []twinPropertyView
	RecentEvents         []deviceEventView
	ActiveAndRecentTasks []deviceTaskView
	TagSuggestions       []string
}

type deviceTaskLaunchPageData struct {
	Shell         shellPageData
	Device        deviceDetailView
	Releases      []releaseOptionView
	TaskType      string
	TaskLabel     string
	TaskHelp      string
	TaskFormError string
	ReadPaths     string
	WriteValues   string
	ReleaseID     int64
	TTLDays       string
}

type releasesPageData struct {
	Shell    shellPageData
	Releases []releaseView
}

type releaseCreatePageData struct {
	Shell            shellPageData
	DeviceModels     []deviceModelOptionView
	ReleaseFormError string
	ReleaseFormNote  string
}

type releaseDetailPageData struct {
	Shell            shellPageData
	Release          domain.SoftwareRelease
	SBOM             releaseSBOMView
	CVEStatus        cveStatusView
	ActiveCVEs       []cveGroupView
	WaivedCVEs       []cveGroupView
	ScanRuns         []scanRunView
	ReplaceFormError string
	RescanFormError  string
}

type deviceModelsPageData struct {
	Shell  shellPageData
	Models []deviceModelView
}

type deviceModelCreatePageData struct {
	Shell          shellPageData
	Releases       []releaseOptionView
	ModelFormError string
}

type deviceModelDetailPageData struct {
	Shell                    shellPageData
	Model                    deviceModelView
	Releases                 []releaseOptionView
	ExpectedReleaseFormError string
}

type organisationPageData struct {
	Shell               shellPageData
	Organisation        domain.Organisation
	Admins              []domain.OrganisationMember
	Members             []domain.OrganisationMember
	APICredentials      []apiCredentialView
	IsOrganisationAdmin bool
	RenameFormError     string
	InviteFormError     string
	RemoveFormError     string
	InviteURL           string
	InviteMessage       string
	APIToken            string
	APIFormError        string
	APIMessage          string
}

type invitationSignupPageData struct {
	Invitation domain.OrganisationInvitation
	Error      string
	Name       string
}

type deviceView struct {
	ID               string
	OrganisationID   int64
	ModelName        string
	SoftwareVersions string
	FirmwareVersion  string
	CVEStatus        cveStatusView
	IsGateway        bool
	Communication    []string
	Status           string
	StatusClass      string
	LastSeen         string
	Tags             []string
	TagOverflow      int
}

type tagOptionView struct {
	Name     string
	Selected bool
}

type paginationView struct {
	Page       int
	PageSize   int
	TotalRows  int
	TotalPages int
	RangeStart int
	RangeEnd   int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
	PageSizes  []int
	FormAction string
	Query      string
	Status     string
	Tags       []string
	ModelID    int64
}

type deviceDetailView struct {
	ID                  string
	OrganisationID      int64
	DeviceModelID       int64
	ModelName           string
	ExpectedProtocol    string
	ProtocolLabel       string
	SoftwareVersions    string
	FirmwareVersion     string
	CVEStatus           cveStatusView
	MatchedReleaseLabel string
	MatchedReleaseURL   string
	IsGateway           bool
	DataTopic           string
	TaskTopic           string
	GatewayPublishTopic string
	Status              string
	StatusClass         string
	LastSeen            string
	SupportNote         string
	Tags                []string
}

type mqttCredentialView struct {
	Username string
	Enabled  bool
}

type coAPCredentialView struct {
	PSKIdentity string
	Revision    int64
	Enabled     bool
	Association string
	CID         string
}

type twinPropertyView struct {
	Path       string
	Value      string
	ValueType  string
	Protocol   string
	SourcePath string
	TSObserved string
	TSReceived string
}

type deviceEventView struct {
	ID             int64
	TSReceived     string
	Protocol       string
	Direction      string
	Operation      string
	Topic          string
	CoAPPath       string
	ContentFormat  string
	Source         string
	PayloadJSON    string
	PayloadRawSize int
}

type deviceTaskView struct {
	ID            int64
	Type          string
	Summary       string
	Status        string
	StatusClass   string
	StatusMessage string
	CampaignID    int64
	CampaignURL   string
	CreatedAt     string
	ExpiresAt     string
	CompletedAt   string
}

type campaignDevicePreviewView struct {
	ID        string
	ModelName string
	ModelID   int64
}

type campaignView struct {
	ID             int64
	OrganisationID int64
	Name           string
	Type           string
	Summary        string
	Status         string
	StatusClass    string
	CreatedAt      string
	FinishedAt     string
	CanceledAt     string
	TTLDays        int64
	TargetCount    int
	Target         string
	Queued         int
	Pending        int
	InProgress     int
	Success        int
	Failure        int
	Expired        int
	Canceled       int
	DetailURL      string
	CancelAction   string
}

type campaignTaskView struct {
	ID            int64
	DeviceID      string
	DeviceURL     string
	ModelName     string
	Type          string
	Summary       string
	Status        string
	StatusClass   string
	StatusValue   string
	StatusMessage string
	CreatedAt     string
	ExpiresAt     string
	CompletedAt   string
	CancelAction  string
}

type releaseView struct {
	domain.SoftwareRelease
	CVEStatus  cveStatusView
	CVECounts  cveSeverityCountsView
	LastScanAt string
}

type releaseSBOMView struct {
	Present        bool
	FileCount      int
	TotalSizeBytes int64
	UpdatedAt      string
}

type cveStatusView struct {
	Status       string
	Label        string
	StatusClass  string
	ActiveCount  int
	HighestLabel string
	Warning      string
}

type cveSeverityCountsView struct {
	HasData  bool
	Total    int
	Critical int
	High     int
	Medium   int
	Low      int
	Other    int
}

type cveGroupView struct {
	CVEID         string
	Severity      string
	SeverityClass string
	NVDURL        string
	WaiverNote    string
	Evidence      []cveEvidenceView
}

type cveEvidenceView struct {
	PackageName      string
	InstalledVersion string
}

type scanRunView struct {
	ID           int64
	Trigger      string
	Status       string
	StatusClass  string
	ErrorMessage string
	CreatedAt    string
	StartedAt    string
	FinishedAt   string
}

type releaseOptionView struct {
	ID        int64
	ModelName string
	Version   string
	Label     string
	Selected  bool
}

type deviceModelOptionView struct {
	ID                       int64
	Name                     string
	ExpectedHeartbeatSeconds int64
	ExpectedProtocol         string
	ExpectedReleaseLabel     string
	Selected                 bool
}

type deviceModelView struct {
	OrganisationID           int64
	ID                       int64
	Name                     string
	ExpectedHeartbeatSeconds int64
	ExpectedProtocol         string
	ExpectedReleaseID        *int64
	ExpectedReleaseLabel     string
	CreatedAt                string
}

type apiCredentialView struct {
	ID          int64
	Name        string
	Enabled     bool
	Status      string
	StatusClass string
	LastUsedAt  string
	CreatedAt   string
}

type mqttAuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	ClientID string `json:"clientid"`
}

type mqttACLRequest struct {
	Username string `json:"username"`
	ClientID string `json:"clientid"`
	Topic    string `json:"topic"`
	Access   any    `json:"acc"`
}

// NewServer constructs Anchor's HTTP handler and starts configured background
// task scheduling and CVE scanning services. If config is omitted, optional
// integrations remain disabled and default storage settings are used.
func NewServer(store *db.Store, configs ...ServerConfig) http.Handler {
	var config ServerConfig
	if len(configs) > 0 {
		config = configs[0]
	}

	server := &Server{
		store:                  store,
		templates:              parseTemplates(),
		internalMQTTClientAuth: config.InternalMQTTClientAuth,
		mqttIntegrationRuntime: config.MQTTIntegrationRuntime,
		taskPublisher:          config.TaskPublisher,
		cveScanWorker:          config.CVEScanWorker,
		releaseStorageDir:      config.ReleaseStorageDir,
		fotaDownloadBaseURL:    strings.TrimRight(strings.TrimSpace(config.FOTADownloadBaseURL), "/"),
		coAPInternalToken:      config.CoAPInternalToken,
		coAPIntegrationEnabled: config.CoAPIntegrationEnabled,
		coAPInvalidator:        config.CoAPInvalidator,
	}
	if server.taskPublisher == nil && server.mqttIntegrationRuntime != nil {
		server.taskPublisher = server.mqttIntegrationRuntime
	}
	if config.CoAPTaskPublisher != nil && server.taskPublisher != nil {
		server.taskPublisher = taskdispatch.New(store, server.taskPublisher, config.CoAPTaskPublisher)
	}
	if saved, err := store.CoAPIntegration(context.Background()); err == nil {
		if server.coAPInternalToken == "" {
			server.coAPInternalToken = saved.BearerToken
		}
		if !config.CoAPIntegrationEnabled {
			server.coAPIntegrationEnabled = saved.Enabled
		}
		if config.CoAPIntegrationRuntime == nil && saved.Enabled {
			if runtime, err := coapcontrol.New(coapcontrol.Config{BaseURL: saved.FrontendURL, BearerToken: saved.BearerToken}); err == nil {
				server.coAPIntegrationRuntime = runtime
			}
		}
	}
	if config.CoAPIntegrationRuntime != nil {
		server.coAPIntegrationRuntime = config.CoAPIntegrationRuntime
	}
	if server.releaseStorageDir == "" {
		server.releaseStorageDir = defaultReleaseDir
	}
	if config.CVEScanWorkerEnabled && server.cveScanWorker == nil {
		worker := cve.NewWorker(cve.Config{
			Store:             store,
			ScannerPath:       config.CVEScannerPath,
			ReleaseStorageDir: server.releaseStorageDir,
		})
		if err := worker.Start(context.Background()); err == nil {
			server.cveScanWorker = worker
		}
	}
	if server.taskPublisher != nil {
		go server.runTaskScheduler(context.Background(), time.Minute)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /logo.png", server.logo)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticassets.Files))))
	mux.HandleFunc("GET /", server.home)
	mux.HandleFunc("GET /login", server.login)
	mux.HandleFunc("POST /login", server.loginPost)
	mux.HandleFunc("POST /logout", server.logout)
	mux.Handle("GET /settings", server.requireAuth(http.HandlerFunc(server.settings)))
	mux.Handle("POST /settings/profile", server.requireAuth(http.HandlerFunc(server.settingsProfilePost)))
	mux.Handle("POST /settings/password", server.requireAuth(http.HandlerFunc(server.settingsPasswordPost)))
	mux.Handle("GET /devices", server.requireAuth(http.HandlerFunc(server.devices)))
	mux.Handle("GET /devices/new", server.requireAuth(http.HandlerFunc(server.deviceNew)))
	mux.Handle("GET /devices/{deviceID}", server.requireAuth(http.HandlerFunc(server.deviceDetail)))
	mux.Handle("GET /devices/{deviceID}/events", server.requireAuth(http.HandlerFunc(server.deviceEvents)))
	mux.Handle("GET /devices/{deviceID}/telemetry", server.requireAuth(http.HandlerFunc(server.deviceTelemetry)))
	mux.Handle("GET /devices/{deviceID}/tasks", server.requireAuth(http.HandlerFunc(server.deviceTasks)))
	mux.Handle("GET /devices/{deviceID}/tasks/new/{taskType}", server.requireAuth(http.HandlerFunc(server.deviceTaskNew)))
	mux.Handle("POST /devices", server.requireAuth(http.HandlerFunc(server.devicesPost)))
	mux.Handle("POST /devices/tags", server.requireAuth(http.HandlerFunc(server.deviceTagsBulkPost)))
	mux.Handle("POST /devices/{deviceID}/tags", server.requireAuth(http.HandlerFunc(server.deviceTagsPost)))
	mux.Handle("POST /devices/{deviceID}/tasks", server.requireAuth(http.HandlerFunc(server.deviceTaskPost)))
	mux.Handle("POST /devices/{deviceID}/support-note", server.requireAuth(http.HandlerFunc(server.deviceSupportNotePost)))
	mux.Handle("POST /devices/{deviceID}/tasks/{taskID}/cancel", server.requireAuth(http.HandlerFunc(server.deviceTaskCancelPost)))
	mux.Handle("POST /devices/{deviceID}/coap/replace", server.requireAuth(http.HandlerFunc(server.deviceCoAPReplacePost)))
	mux.Handle("POST /devices/{deviceID}/coap/toggle", server.requireAuth(http.HandlerFunc(server.deviceCoAPTogglePost)))
	mux.Handle("POST /devices/delete", server.requireAuth(http.HandlerFunc(server.deviceDeletePost)))
	mux.Handle("GET /campaigns", server.requireAuth(http.HandlerFunc(server.campaigns)))
	mux.Handle("GET /campaigns/new", server.requireAuth(http.HandlerFunc(server.campaignNew)))
	mux.Handle("POST /campaigns/new", server.requireAuth(http.HandlerFunc(server.campaignNewPost)))
	mux.Handle("GET /campaigns/estimate", server.requireAuth(http.HandlerFunc(server.campaignEstimate)))
	mux.Handle("POST /campaigns", server.requireAuth(http.HandlerFunc(server.campaignsPost)))
	mux.Handle("GET /campaigns/{campaignID}", server.requireAuth(http.HandlerFunc(server.campaignDetail)))
	mux.Handle("POST /campaigns/{campaignID}/tasks/{taskID}/cancel", server.requireAuth(http.HandlerFunc(server.campaignTaskCancelPost)))
	mux.Handle("POST /campaigns/{campaignID}/cancel", server.requireAuth(http.HandlerFunc(server.campaignCancelPost)))
	mux.Handle("GET /device-models", server.requireAuth(http.HandlerFunc(server.deviceModels)))
	mux.Handle("GET /device-models/new", server.requireAuth(http.HandlerFunc(server.deviceModelNew)))
	mux.Handle("GET /device-models/{modelID}", server.requireAuth(http.HandlerFunc(server.deviceModelDetail)))
	mux.Handle("POST /device-models", server.requireAuth(http.HandlerFunc(server.deviceModelsPost)))
	mux.Handle("POST /device-models/{modelID}/expected-release", server.requireAuth(http.HandlerFunc(server.deviceModelExpectedReleasePost)))
	mux.Handle("GET /releases", server.requireAuth(http.HandlerFunc(server.releases)))
	mux.Handle("GET /releases/new", server.requireAuth(http.HandlerFunc(server.releaseNew)))
	mux.Handle("POST /releases", server.requireAuth(http.HandlerFunc(server.releasesPost)))
	mux.Handle("GET /releases/{releaseID}", server.requireAuth(http.HandlerFunc(server.releaseDetail)))
	mux.Handle("GET /releases/{releaseID}/cves", server.requireAuth(http.HandlerFunc(server.releaseCVEState)))
	mux.Handle("GET /releases/{releaseID}/events", server.requireAuth(http.HandlerFunc(server.releaseEvents)))
	mux.Handle("POST /releases/{releaseID}/sbom", server.requireAuth(http.HandlerFunc(server.releaseSBOMReplacePost)))
	mux.Handle("POST /releases/{releaseID}/rescan", server.requireAuth(http.HandlerFunc(server.releaseRescanPost)))
	mux.Handle("POST /releases/{releaseID}/cves/{cveID}/waiver", server.requireAuth(http.HandlerFunc(server.releaseCVEMarkNotRelevantPost)))
	mux.Handle("POST /releases/{releaseID}/cves/{cveID}/waiver/delete", server.requireAuth(http.HandlerFunc(server.releaseCVEUnmarkNotRelevantPost)))
	mux.HandleFunc("GET /org/{organisationID}/releases/{releaseID}/binary", server.releaseBinary)
	mux.Handle("GET /organisations", server.requireAuth(http.HandlerFunc(server.organisations)))
	mux.Handle("POST /organisations/rename", server.requireAuth(http.HandlerFunc(server.organisationRenamePost)))
	mux.Handle("POST /organisations/invitations", server.requireAuth(http.HandlerFunc(server.organisationInvitationsPost)))
	mux.Handle("POST /organisations/members/remove", server.requireAuth(http.HandlerFunc(server.organisationMemberRemovePost)))
	mux.Handle("POST /organisations/api-credentials", server.requireAuth(http.HandlerFunc(server.organisationAPICredentialsPost)))
	mux.Handle("POST /organisations/api-credentials/{credentialID}/disable", server.requireAuth(http.HandlerFunc(server.organisationAPICredentialDisablePost)))
	mux.Handle("POST /organisations/api-credentials/{credentialID}/rotate", server.requireAuth(http.HandlerFunc(server.organisationAPICredentialRotatePost)))
	mux.Handle("GET /integrations", server.requireAuth(http.HandlerFunc(server.integrations)))
	mux.Handle("GET /integrations/mqtt/status", server.requireAuth(http.HandlerFunc(server.mqttIntegrationStatus)))
	mux.Handle("POST /integrations/mqtt", server.requireAuth(http.HandlerFunc(server.mqttIntegrationPost)))
	mux.Handle("POST /integrations/coap", server.requireAuth(http.HandlerFunc(server.coAPIntegrationPost)))
	mux.Handle("GET /integrations/coap/status", server.requireAuth(http.HandlerFunc(server.coAPIntegrationStatus)))
	mux.HandleFunc("GET /invitations/{token}", server.invitationSignup)
	mux.HandleFunc("POST /invitations/{token}", server.invitationSignupPost)
	mux.Handle("POST /api/v1/devices/bulk-upsert", server.requireAPIAuth(http.HandlerFunc(server.apiDeviceBulkUpsert)))
	mux.Handle("PUT /api/v1/devices/{deviceID}", server.requireAPIAuth(http.HandlerFunc(server.apiDeviceUpsert)))
	mux.Handle("POST /api/v1/devices/{deviceID}", server.requireAPIAuth(http.HandlerFunc(server.apiDeviceUpsert)))
	mux.Handle("POST /api/v1/devices/{deviceID}/check-in", server.requireAPIAuth(http.HandlerFunc(server.apiDeviceCheckIn)))
	mux.Handle("POST /internal/coap/v1/credentials/resolve", server.requireCoAPInternalAuth(http.HandlerFunc(server.coAPResolveCredentials)))
	mux.Handle("POST /internal/coap/v1/devices/{deviceID}/activity", server.requireCoAPInternalAuth(http.HandlerFunc(server.coAPActivity)))
	mux.Handle("POST /internal/coap/v1/devices/{deviceID}/telemetry", server.requireCoAPInternalAuth(http.HandlerFunc(server.coAPTelemetry)))
	mux.Handle("POST /internal/coap/v1/devices/{deviceID}/tasks/{taskID}/operations", server.requireCoAPInternalAuth(http.HandlerFunc(server.coAPOperation)))
	mux.Handle("PUT /internal/coap/v1/devices/{deviceID}/tasks/{taskID}/status", server.requireCoAPInternalAuth(http.HandlerFunc(server.coAPTaskStatus)))
	mux.Handle("GET /internal/coap/v1/devices/{deviceID}/tasks/pending", server.requireCoAPInternalAuth(http.HandlerFunc(server.coAPPendingTask)))
	mux.HandleFunc("POST /mqtt/auth", server.mqttAuth)
	mux.HandleFunc("POST /mqtt/superuser", server.mqttSuperuser)
	mux.HandleFunc("POST /mqtt/acl", server.mqttACL)

	return mux
}

func parseTemplates() *template.Template {
	return template.Must(template.New("").Funcs(template.FuncMap{
		"dict":      templateDict,
		"localTime": localTimeElement,
	}).ParseFS(templateassets.Files, "*.html"))
}

func templateDict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, errors.New("dict requires an even number of arguments")
	}

	dict := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, errors.New("dict keys must be strings")
		}
		dict[key] = values[i+1]
	}
	return dict, nil
}

func (s *Server) logo(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, staticassets.Files, "logo.png")
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.userFromRequest(r); ok {
		http.Redirect(w, r, "/devices", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.userFromRequest(r); ok {
		http.Redirect(w, r, "/devices", http.StatusSeeOther)
		return
	}
	if err := s.render(w, http.StatusOK, loginPageData{}, "login.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	email := r.FormValue("email")
	password := r.FormValue("password")

	user, err := s.store.FindUserByEmail(r.Context(), email)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		if err := s.render(w, http.StatusOK, loginPageData{
			Error: "Invalid email or password.",
			Email: email,
		}, "login.html"); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
		}
		return
	}

	token, err := randomToken()
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(sessionDuration)
	if err := s.store.CreateSession(r.Context(), domain.Session{
		ID:        token,
		UserID:    user.ID,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, sessionCookie(token, expiresAt))
	http.Redirect(w, r, "/devices", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.store.DeleteSession(r.Context(), cookie.Value)
	}

	http.SetCookie(w, expiredSessionCookie())
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	tags := nonBlankStrings(r.URL.Query()["tag"])
	var tagErr error
	tags, tagErr = normalizeFilterTags(tags)
	if tagErr != nil {
		http.Error(w, tagErr.Error(), http.StatusBadRequest)
		return
	}
	modelID, _ := strconv.ParseInt(r.URL.Query().Get("model_id"), 10, 64)
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	pageSize := parsePositiveInt(r.URL.Query().Get("page_size"), db.DefaultDevicePageSize)
	devicePage, err := s.store.ListDevicePage(r.Context(), db.DeviceListQuery{
		OrganisationID: shell.SelectedOrganisationID,
		Query:          query,
		Tags:           tags,
		DeviceModelID:  modelID,
		Page:           page,
		PageSize:       pageSize,
	})
	if err != nil {
		http.Error(w, "device query error", http.StatusInternalServerError)
		return
	}
	metrics, err := s.store.DeviceFleetMetrics(r.Context(), shell.SelectedOrganisationID, time.Now().UnixMilli())
	if err != nil {
		http.Error(w, "device metrics query error", http.StatusInternalServerError)
		return
	}
	models, err := s.store.ListDeviceModels(r.Context(), shell.SelectedOrganisationID)
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	modelOptions := make([]deviceModelOptionView, 0, len(models))
	for _, model := range models {
		option := deviceModelOption(model)
		option.Selected = model.ID == modelID
		modelOptions = append(modelOptions, option)
	}
	tagSuggestions, err := s.store.ListTagSuggestions(r.Context(), shell.SelectedOrganisationID, "")
	if err != nil {
		http.Error(w, "tag query error", http.StatusInternalServerError)
		return
	}
	tagOptions := deviceTagFilterOptions(tagSuggestions, tags)

	views := make([]deviceView, 0, len(devicePage.Rows))
	now := time.Now()
	for _, row := range devicePage.Rows {
		communication := []string{}
		if strings.EqualFold(strings.TrimSpace(row.Device.ExpectedProtocol), "api") {
			communication = append(communication, "API")
		}
		if row.HasMQTTCredential {
			communication = append(communication, "MQTT")
		}
		if row.HasCoAPCredential {
			communication = append(communication, "CoAP over DTLS")
		}
		if row.Device.IsGateway {
			communication = append(communication, "Gateway")
		}
		connectivity := deviceConnectivity(row.Device, now)
		tagOverflow := 0
		if len(row.Device.Tags) > 3 {
			tagOverflow = len(row.Device.Tags) - 3
		}
		views = append(views, deviceView{
			ID:               row.Device.ID,
			OrganisationID:   row.Device.OrganisationID,
			ModelName:        row.Device.ModelName,
			SoftwareVersions: formatSoftwareVersions(row.Device.SoftwareVersions),
			FirmwareVersion:  firmwareVersion(row.Device.SoftwareVersions),
			CVEStatus:        cveStatusViewFor(row.CVEStatus),
			IsGateway:        row.Device.IsGateway,
			Communication:    communication,
			Status:           connectivity.Status,
			StatusClass:      connectivity.StatusClass,
			LastSeen:         connectivity.LastSeen,
			Tags:             row.Device.Tags,
			TagOverflow:      tagOverflow,
		})
	}

	hasQuery := query != ""
	hasFilters := len(tags) > 0 || modelID > 0
	inventoryLabel := strconv.Itoa(metrics.TotalDevices) + " devices registered"
	inventorySuffix := ""
	if hasQuery || hasFilters {
		inventoryLabel = strconv.Itoa(devicePage.FilteredCount) + " matching devices"
		inventorySuffix = "from " + strconv.Itoa(metrics.TotalDevices) + " registered"
	}
	s.renderDevices(w, r, devicesPageData{
		Shell:                 shell,
		Devices:               views,
		Metrics:               metrics,
		FilteredCount:         devicePage.FilteredCount,
		Query:                 query,
		Pagination:            devicePaginationView(r.URL.Path, shell.SelectedOrganisationID, query, tags, modelID, devicePage.Pagination),
		HasQuery:              hasQuery,
		IsEmptyUnfiltered:     !hasQuery && !hasFilters && metrics.TotalDevices == 0,
		IsEmptyFiltered:       (hasQuery || hasFilters) && devicePage.FilteredCount == 0,
		DeviceInventoryLabel:  inventoryLabel,
		DeviceInventorySuffix: inventorySuffix,
		DeviceModels:          modelOptions,
		TagSuggestions:        tagSuggestions,
		TagOptions:            tagOptions,
		ActiveTags:            tags,
		ModelID:               modelID,
		HasFilters:            hasFilters,
		ReturnURL:             devicePageURL(r.URL.Path, shell.SelectedOrganisationID, query, tags, modelID, devicePage.Pagination.Page, devicePage.Pagination.PageSize),
	})
}

func (s *Server) deviceNew(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}

	data, err := s.loadDeviceCreatePageData(r.Context(), shell, shell.SelectedOrganisationID, "")
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	s.renderDeviceNew(w, data)
}

func (s *Server) deviceDetail(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceID")
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}

	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}

	data, err := s.loadDeviceDetailPageData(r.Context(), shell, deviceID, organisationID)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "device detail query error", http.StatusInternalServerError)
		return
	}

	s.renderDeviceDetail(w, data)
}

func (s *Server) deviceTelemetry(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceID")
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.DeviceDetail(r.Context(), deviceID, organisationID); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "device detail query error", http.StatusInternalServerError)
		return
	}

	twinProperties, recentEvents, err := s.loadDeviceTelemetry(r.Context(), deviceID, organisationID)
	if err != nil {
		http.Error(w, "device telemetry query error", http.StatusInternalServerError)
		return
	}

	s.renderDeviceTelemetry(w, deviceDetailPageData{
		Device: deviceDetailView{
			ID:             deviceID,
			OrganisationID: organisationID,
		},
		TwinProperties: twinProperties,
		RecentEvents:   recentEvents,
	})
}

func (s *Server) deviceTasks(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceID")
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.DeviceDetail(r.Context(), deviceID, organisationID); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "device detail query error", http.StatusInternalServerError)
		return
	}

	s.renderDeviceTasksForDevice(w, r, deviceID, organisationID)
}

func (s *Server) renderDeviceTasksForDevice(w http.ResponseWriter, r *http.Request, deviceID string, organisationID int64) {
	tasks, err := s.loadActiveAndRecentDeviceTasks(r.Context(), deviceID, organisationID)
	if err != nil {
		http.Error(w, "device tasks query error", http.StatusInternalServerError)
		return
	}

	s.renderDeviceTasks(w, deviceDetailPageData{
		Device: deviceDetailView{
			ID:             deviceID,
			OrganisationID: organisationID,
		},
		ActiveAndRecentTasks: tasks,
	})
}

func (s *Server) deviceTaskNew(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceID")
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}
	taskType := r.PathValue("taskType")
	if _, _, ok := deviceTaskLaunchCopy(taskType); !ok {
		http.NotFound(w, r)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.Error(w, "missing organisation id", http.StatusBadRequest)
		return
	}

	data, err := s.loadDeviceTaskLaunchPageData(r.Context(), shell, deviceID, organisationID, taskType, "")
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "device task launch query error", http.StatusInternalServerError)
		return
	}

	s.renderDeviceTaskNew(w, data)
}

func (s *Server) deviceEvents(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceID")
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.DeviceDetail(r.Context(), deviceID, organisationID); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "device detail query error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	events, unsubscribe := s.store.SubscribeDeviceEvents(r.Context(), deviceID)
	defer unsubscribe()
	tasks, unsubscribeTasks := s.store.SubscribeDeviceTasks(r.Context(), deviceID)
	defer unsubscribeTasks()

	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-events:
			_, _ = w.Write([]byte("event: device-telemetry\n"))
			_, _ = w.Write([]byte("data: refresh\n\n"))
			flusher.Flush()
		case <-tasks:
			_, _ = w.Write([]byte("event: device-tasks\n"))
			_, _ = w.Write([]byte("data: refresh\n\n"))
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": heartbeat\n\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) devicesPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	deviceID := strings.TrimSpace(r.FormValue("device_id"))
	if deviceID == "" {
		s.renderDeviceNewWithError(w, r, "Device ID and model are required.")
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		s.renderDeviceNewWithError(w, r, "An organisation is required before creating a device.")
		return
	}
	deviceModelID, err := strconv.ParseInt(r.FormValue("device_model_id"), 10, 64)
	if err != nil || deviceModelID <= 0 {
		s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, "Choose a device model.")
		return
	}
	model, err := s.store.DeviceModel(r.Context(), deviceModelID, organisationID)
	if errors.Is(err, db.ErrNotFound) {
		s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, "Choose a device model from this organisation.")
		return
	} else if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}

	device := domain.Device{
		ID:               deviceID,
		OrganisationID:   organisationID,
		DeviceModelID:    deviceModelID,
		SoftwareVersions: domain.SoftwareVersions{},
	}
	tags := parseTagInput(r.FormValue("tags"))
	if _, err := db.NormalizeTags(tags); err != nil {
		s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, err.Error())
		return
	}
	device.Tags = tags

	switch strings.ToLower(strings.TrimSpace(model.ExpectedProtocol)) {
	case "mqtt":
		username := strings.TrimSpace(r.FormValue("mqtt_username"))
		password := r.FormValue("mqtt_password")
		if username == "" || password == "" {
			s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, "MQTT username and password are required for this model.")
			return
		}
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "credential error", http.StatusInternalServerError)
			return
		}
		device.IsGateway = r.FormValue("is_gateway") == "on"
		if err := s.store.SaveDeviceWithMQTTCredential(r.Context(), domain.DeviceWithMQTTCredential{
			Device: device,
			Credential: domain.DeviceMQTTCredential{
				DeviceID:     deviceID,
				Username:     username,
				PasswordHash: string(passwordHash),
				Enabled:      true,
			},
		}); err != nil {
			http.Error(w, "device with mqtt credential error", http.StatusInternalServerError)
			return
		}
	case "coap":
		var psk []byte
		pskHex := strings.TrimSpace(r.FormValue("coap_psk"))
		if pskHex != "" {
			psk, err = hex.DecodeString(pskHex)
			if err != nil || len(psk) < 16 || len(psk) > 64 {
				s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, "CoAP PSK must decode to 16 to 64 bytes (32 to 128 hexadecimal characters).")
				return
			}
		}
		credential, err := s.store.SaveDeviceWithCoAPCredential(r.Context(), domain.DeviceWithCoAPCredential{
			Device: device,
			Credential: domain.CoAPCredential{
				DeviceID:    deviceID,
				PSKIdentity: strings.TrimSpace(r.FormValue("coap_psk_identity")),
				PSK:         psk,
				Enabled:     true,
			},
		})
		if err != nil {
			s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, "Could not create the CoAP credential. Check that its identity is valid and unique.")
			return
		}
		s.renderDeviceCreated(w, deviceCreatedPageData{
			Shell:       shell,
			DeviceID:    deviceID,
			ModelName:   model.Name,
			PSKIdentity: credential.PSKIdentity,
			PSKHex:      hex.EncodeToString(credential.PSK),
		})
		return
	case "api":
		if strings.TrimSpace(r.FormValue("mqtt_username")) != "" || r.FormValue("mqtt_password") != "" || strings.TrimSpace(r.FormValue("coap_psk_identity")) != "" || strings.TrimSpace(r.FormValue("coap_psk")) != "" || r.FormValue("is_gateway") != "" {
			s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, "API devices cannot have MQTT, CoAP, or gateway settings.")
			return
		}
		if err := s.store.SaveDevice(r.Context(), device); err != nil {
			http.Error(w, "device save error", http.StatusInternalServerError)
			return
		}
	default:
		s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, "The selected model uses a protocol that device creation does not support yet.")
		return
	}

	http.Redirect(w, r, "/devices?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) deviceSupportNotePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	deviceID := r.PathValue("deviceID")
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	note := strings.TrimSpace(r.FormValue("support_note"))
	if len([]rune(note)) > 4000 {
		http.Error(w, "support note must be 4000 characters or fewer", http.StatusBadRequest)
		return
	}
	if err := s.store.UpdateDeviceSupportNote(r.Context(), deviceID, organisationID, note); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "support note update error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/devices/"+url.PathEscape(deviceID)+"?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) deviceTagsPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	deviceID := r.PathValue("deviceID")
	operation := r.FormValue("operation")
	if operation != "add" && operation != "remove" {
		http.Error(w, "choose add or remove", http.StatusBadRequest)
		return
	}
	if err := s.store.UpdateDeviceTagsBulk(r.Context(), organisationID, []string{deviceID}, r.FormValue("tag"), operation == "add"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/devices/"+url.PathEscape(deviceID)+"?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) deviceTagsBulkPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	operation := r.FormValue("operation")
	if operation != "add" && operation != "remove" {
		http.Error(w, "choose add or remove", http.StatusBadRequest)
		return
	}
	if err := s.store.UpdateDeviceTagsBulk(r.Context(), organisationID, r.Form["device_id"], r.FormValue("tag"), operation == "add"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target := r.FormValue("return_to")
	if !strings.HasPrefix(target, "/devices?") {
		target = "/devices?organisation_id=" + strconv.FormatInt(organisationID, 10)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) deviceCoAPReplacePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	deviceID := r.PathValue("deviceID")
	summary, err := s.store.LoadCoAPCredentialSummary(r.Context(), deviceID, organisationID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	identity := strings.TrimSpace(r.FormValue("coap_psk_identity"))
	if identity == "" {
		identity = summary.PSKIdentity
	}
	var psk []byte
	if encoded := strings.TrimSpace(r.FormValue("coap_psk")); encoded != "" {
		psk, err = hex.DecodeString(encoded)
		if err != nil || len(psk) < 16 || len(psk) > 64 {
			http.Error(w, "CoAP PSK must be 32 to 128 hexadecimal characters", http.StatusBadRequest)
			return
		}
	} else {
		psk, err = domain.GenerateCoAPPSK()
		if err != nil {
			http.Error(w, "credential generation failed", http.StatusInternalServerError)
			return
		}
	}
	replaced, err := s.store.ReplaceCoAPCredential(r.Context(), domain.CoAPCredential{DeviceID: deviceID, PSKIdentity: identity, PSK: psk, Enabled: summary.Enabled}, organisationID)
	if err != nil {
		http.Error(w, "could not replace CoAP credential", http.StatusBadRequest)
		return
	}
	if s.coAPInvalidator != nil {
		_ = s.coAPInvalidator.Invalidate(r.Context(), deviceID, replaced.Revision, false)
	}
	if err := s.render(w, http.StatusOK, coAPCredentialCreatedPageData{Shell: shell, DeviceID: deviceID, PSKIdentity: replaced.PSKIdentity, PSKHex: hex.EncodeToString(replaced.PSK)}, "coap_credential_created"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) deviceCoAPTogglePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	deviceID := r.PathValue("deviceID")
	summary, err := s.store.LoadCoAPCredentialSummary(r.Context(), deviceID, organisationID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.EnableCoAPCredential(r.Context(), deviceID, organisationID, !summary.Enabled); err != nil {
		http.Error(w, "could not update CoAP credential", http.StatusInternalServerError)
		return
	}
	updated, err := s.store.LoadCoAPCredentialSummary(r.Context(), deviceID, organisationID)
	if err == nil && s.coAPInvalidator != nil {
		_ = s.coAPInvalidator.Invalidate(r.Context(), deviceID, updated.Revision, !updated.Enabled)
	}
	http.Redirect(w, r, "/devices/"+url.PathEscape(deviceID)+"?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) deviceTaskPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	deviceID := r.PathValue("deviceID")
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.Error(w, "missing organisation id", http.StatusBadRequest)
		return
	}

	taskType := r.FormValue("task_type")
	if _, _, ok := deviceTaskLaunchCopy(taskType); !ok {
		http.Error(w, "choose a supported task type", http.StatusBadRequest)
		return
	}
	parametersJSON, err := s.taskParametersFromForm(r, taskType, organisationID)
	if err == nil {
		if protocol, protocolErr := s.store.DeviceExpectedProtocol(r.Context(), deviceID, organisationID); protocolErr != nil {
			err = protocolErr
		} else if protocol == "coap" {
			err = validateCoAPTaskParameters(taskType, parametersJSON)
		}
	}
	if err != nil {
		s.renderDeviceTaskNewWithError(w, r, shell, deviceID, organisationID, taskType, err.Error())
		return
	}
	if parametersJSON == "" {
		s.renderDeviceTaskNewWithError(w, r, shell, deviceID, organisationID, taskType, "Choose a supported task type.")
		return
	}

	task := domain.DeviceTask{
		DeviceID:       deviceID,
		Type:           taskType,
		ParametersJSON: parametersJSON,
	}
	_, ttlSeconds, err := domain.ParseTaskTTLDays(r.FormValue("ttl_days"))
	if err != nil {
		s.renderDeviceTaskNewWithError(w, r, shell, deviceID, organisationID, taskType, err.Error())
		return
	}
	task, err = s.store.CreateQueuedDeviceTask(r.Context(), organisationID, db.CreateDeviceTaskOptions{
		Task:        task,
		TTLSeconds:  ttlSeconds,
		CreatedTime: time.Now().UTC(),
	})
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "device task create error", http.StatusInternalServerError)
		return
	}
	if task.Status == db.DeviceTaskStatusPending {
		s.publishDeviceTask(r.Context(), task, organisationID)
	}
	s.processTaskQueue(r.Context())

	http.Redirect(w, r, "/devices/"+deviceID+"?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) taskParametersFromForm(r *http.Request, taskType string, organisationID int64) (string, error) {
	switch taskType {
	case domain.TaskTypeRead:
		return domain.BuildReadTaskParameters(readTaskPathsFromForm(r.FormValue("read_paths")))
	case domain.TaskTypeWrite:
		// The web editor presents an array; the task wire format wraps that
		// array in a values object. Continue accepting the full object too.
		input := strings.TrimSpace(r.FormValue("write_values"))
		if strings.HasPrefix(input, "[") {
			input = `{"values":` + input + `}`
		}
		return domain.BuildWriteTaskParameters(input)
	case domain.TaskTypeFOTA:
		releaseID, err := strconv.ParseInt(r.FormValue("release_id"), 10, 64)
		if err != nil || releaseID <= 0 {
			return "", errors.New("choose a release for the FOTA task")
		}
		if _, ok, err := s.findReleaseOption(r.Context(), organisationID, releaseID); err != nil {
			return "", fmt.Errorf("release query error")
		} else if !ok {
			return "", errors.New("choose a release from this organisation")
		}
		return domain.BuildFOTATaskParameters(releaseID)
	default:
		return "", nil
	}
}

func validateCoAPTaskParameters(taskType, parametersJSON string) error {
	switch taskType {
	case domain.TaskTypeRead:
		var params domain.ReadTaskParameters
		if err := json.Unmarshal([]byte(parametersJSON), &params); err != nil {
			return err
		}
		for _, path := range params.Paths {
			if err := domain.ValidateCoAPResourcePath(path); err != nil {
				return err
			}
		}
	case domain.TaskTypeWrite:
		var params domain.WriteTaskParameters
		if err := json.Unmarshal([]byte(parametersJSON), &params); err != nil {
			return err
		}
		for _, value := range params.Values {
			if err := domain.ValidateCoAPResourcePath(value.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func readTaskPathsFromForm(input string) []string {
	lines := strings.Split(input, "\n")
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

func (s *Server) deviceTaskCancelPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	deviceID := r.PathValue("deviceID")
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}
	taskID, err := strconv.ParseInt(r.PathValue("taskID"), 10, 64)
	if err != nil || taskID <= 0 {
		http.NotFound(w, r)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.Error(w, "missing organisation id", http.StatusBadRequest)
		return
	}

	outcome, err := s.store.CancelDeviceTask(r.Context(), taskID, deviceID, organisationID, time.Now().UTC(), "")
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "device task cancel error", http.StatusInternalServerError)
		return
	}
	if outcome == db.TaskTransitionNotFound {
		http.NotFound(w, r)
		return
	}
	s.processTaskQueue(r.Context())

	if isHTMXRequest(r) {
		s.renderDeviceTasksForDevice(w, r, deviceID, organisationID)
		return
	}

	redirect(w, r, "/devices/"+deviceID+"?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) renderDeviceTaskNewWithError(w http.ResponseWriter, r *http.Request, shell shellPageData, deviceID string, organisationID int64, taskType string, message string) {
	data, err := s.loadDeviceTaskLaunchPageData(r.Context(), shell, deviceID, organisationID, taskType, message)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "device task launch query error", http.StatusInternalServerError)
		return
	}
	data.ReadPaths = r.FormValue("read_paths")
	data.WriteValues = r.FormValue("write_values")
	data.TTLDays = r.FormValue("ttl_days")
	data.ReleaseID, _ = strconv.ParseInt(r.FormValue("release_id"), 10, 64)

	s.renderDeviceTaskNew(w, data)
}

func (s *Server) deviceDeletePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	deviceID := r.FormValue("device_id")
	if deviceID == "" {
		http.Error(w, "missing device id", http.StatusBadRequest)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.Error(w, "missing organisation id", http.StatusBadRequest)
		return
	}

	if err := s.store.DeleteDevice(r.Context(), deviceID, organisationID); err != nil {
		http.Error(w, "device delete error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/devices?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) deviceModels(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}

	data, err := s.loadDeviceModelsPageData(r.Context(), shell, organisationID)
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	s.renderDeviceModels(w, data)
}

func (s *Server) deviceModelNew(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}

	data, err := s.loadDeviceModelCreatePageData(r.Context(), shell, organisationID, "")
	if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}
	s.renderDeviceModelNew(w, data)
}

func (s *Server) deviceModelDetail(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	modelID, err := strconv.ParseInt(r.PathValue("modelID"), 10, 64)
	if err != nil || modelID <= 0 {
		http.NotFound(w, r)
		return
	}

	data, err := s.loadDeviceModelDetailPageData(r.Context(), shell, organisationID, modelID, "")
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	s.renderDeviceModelDetail(w, data)
}

func (s *Server) deviceModelsPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.Error(w, "missing organisation id", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	expectedProtocol := strings.TrimSpace(r.FormValue("expected_protocol"))
	expectedHeartbeatSeconds, err := strconv.ParseInt(r.FormValue("expected_heartbeat_seconds"), 10, 64)
	if name == "" || expectedProtocol == "" || expectedHeartbeatSeconds <= 0 || err != nil {
		s.renderDeviceModelNewForOrganisationWithError(w, r, shell, organisationID, "Name, heartbeat, and protocol are required.")
		return
	}
	expectedProtocol = strings.ToLower(expectedProtocol)
	if expectedProtocol != "mqtt" && expectedProtocol != "coap" && expectedProtocol != "api" {
		s.renderDeviceModelNewForOrganisationWithError(w, r, shell, organisationID, "Choose a supported protocol.")
		return
	}

	var expectedReleaseID *int64
	releaseValue := strings.TrimSpace(r.FormValue("expected_release_id"))
	if releaseValue != "" {
		releaseID, err := strconv.ParseInt(releaseValue, 10, 64)
		if err != nil || releaseID <= 0 {
			s.renderDeviceModelNewForOrganisationWithError(w, r, shell, organisationID, "Choose a valid expected release.")
			return
		}
		if _, err := s.store.SoftwareRelease(r.Context(), releaseID, organisationID); errors.Is(err, db.ErrNotFound) {
			s.renderDeviceModelNewForOrganisationWithError(w, r, shell, organisationID, "Choose a release from this organisation.")
			return
		} else if err != nil {
			http.Error(w, "release query error", http.StatusInternalServerError)
			return
		}
		expectedReleaseID = &releaseID
	}

	_, err = s.store.CreateDeviceModel(r.Context(), domain.DeviceModel{
		OrganisationID:           organisationID,
		Name:                     name,
		ExpectedHeartbeatSeconds: expectedHeartbeatSeconds,
		ExpectedProtocol:         expectedProtocol,
		ExpectedReleaseID:        expectedReleaseID,
	})
	if errors.Is(err, db.ErrConflict) {
		s.renderDeviceModelNewForOrganisationWithError(w, r, shell, organisationID, "A device model with this name already exists.")
		return
	}
	if err != nil {
		http.Error(w, "device model create error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/device-models?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) deviceModelExpectedReleasePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.Error(w, "missing organisation id", http.StatusBadRequest)
		return
	}
	modelID, err := strconv.ParseInt(r.PathValue("modelID"), 10, 64)
	if err != nil || modelID <= 0 {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.DeviceModel(r.Context(), modelID, organisationID); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}

	var expectedReleaseID *int64
	releaseValue := strings.TrimSpace(r.FormValue("expected_release_id"))
	if releaseValue != "" {
		releaseID, err := strconv.ParseInt(releaseValue, 10, 64)
		if err != nil || releaseID <= 0 {
			s.renderDeviceModelDetailForOrganisationWithError(w, r, shell, organisationID, modelID, "Choose a valid expected release.")
			return
		}
		if _, err := s.store.SoftwareRelease(r.Context(), releaseID, organisationID); errors.Is(err, db.ErrNotFound) {
			s.renderDeviceModelDetailForOrganisationWithError(w, r, shell, organisationID, modelID, "Choose a release from this organisation.")
			return
		} else if err != nil {
			http.Error(w, "release query error", http.StatusInternalServerError)
			return
		}
		expectedReleaseID = &releaseID
	}

	if err := s.store.UpdateDeviceModelExpectedRelease(r.Context(), organisationID, modelID, expectedReleaseID); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "device model update error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, deviceModelDetailURL(modelID, organisationID), http.StatusSeeOther)
}

func (s *Server) releases(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	shell.SelectedOrganisationID = organisationID

	releases, err := s.releaseViews(r.Context(), organisationID)
	if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}
	s.renderReleases(w, releasesPageData{
		Shell:    shell,
		Releases: releases,
	})
}

func (s *Server) releaseNew(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}

	data, err := s.loadReleaseCreatePageData(r.Context(), shell, organisationID, "")
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	s.renderReleaseNew(w, data)
}

func (s *Server) releasesPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReleaseUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.renderReleasesWithError(w, r, "Release upload is invalid or too large.")
		return
	}

	version := strings.TrimSpace(r.FormValue("version"))
	if version == "" {
		s.renderReleasesWithError(w, r, "Firmware version is required.")
		return
	}

	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.Error(w, "missing organisation id", http.StatusBadRequest)
		return
	}
	deviceModelID, err := strconv.ParseInt(r.FormValue("device_model_id"), 10, 64)
	if err != nil || deviceModelID <= 0 {
		s.renderReleaseNewForOrganisationWithError(w, r, shell, organisationID, "Choose a device model.")
		return
	}
	if _, err := s.store.DeviceModel(r.Context(), deviceModelID, organisationID); errors.Is(err, db.ErrNotFound) {
		s.renderReleaseNewForOrganisationWithError(w, r, shell, organisationID, "Choose a device model from this organisation.")
		return
	} else if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}

	artifact, cleanup, err := s.saveReleaseArtifact(r, organisationID)
	if err != nil {
		s.renderReleaseNewForOrganisationWithError(w, r, shell, organisationID, err.Error())
		return
	}
	sbom, sbomCleanup, err := s.saveReleaseSBOMFiles(r, artifact.Path)
	if err != nil {
		cleanup()
		s.renderReleaseNewForOrganisationWithError(w, r, shell, organisationID, err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			sbomCleanup()
			cleanup()
		}
	}()

	releaseID, err := s.store.CreateSoftwareRelease(r.Context(), domain.SoftwareRelease{
		OrganisationID:      organisationID,
		DeviceModelID:       deviceModelID,
		Version:             version,
		ArtifactPath:        artifact.Path,
		ArtifactFilename:    artifact.Filename,
		ArtifactContentType: artifact.ContentType,
		ArtifactSizeBytes:   artifact.SizeBytes,
	})
	if err != nil {
		if errors.Is(err, db.ErrConflict) {
			s.renderReleaseNewForOrganisationWithError(w, r, shell, organisationID, "A release already exists for this device model and version.")
			return
		}
		http.Error(w, "release create error", http.StatusInternalServerError)
		return
	}
	if sbom.Count > 0 {
		if _, err := s.store.ReplaceReleaseSBOM(r.Context(), organisationID, releaseID, sbom.Count, sbom.SizeBytes); err != nil {
			_ = s.store.DeleteSoftwareRelease(r.Context(), releaseID, organisationID)
			http.Error(w, "release sbom create error", http.StatusInternalServerError)
			return
		}
		if _, err := s.store.EnqueueCVEScan(r.Context(), organisationID, releaseID, "auto"); err != nil && !errors.Is(err, db.ErrConflict) {
			_ = s.store.DeleteSoftwareRelease(r.Context(), releaseID, organisationID)
			http.Error(w, "release scan enqueue error", http.StatusInternalServerError)
			return
		}
		if s.cveScanWorker != nil {
			s.cveScanWorker.Notify()
		}
	}
	committed = true

	http.Redirect(w, r, "/releases?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) releaseDetail(w http.ResponseWriter, r *http.Request) {
	shell, releaseID, organisationID, ok := s.releaseMutationTarget(w, r, r.URL.Query().Get("organisation_id"))
	if !ok {
		return
	}
	data, err := s.loadReleaseDetailPageData(r.Context(), shell, releaseID, organisationID, "", "")
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "release detail query error", http.StatusInternalServerError)
		return
	}
	s.renderReleaseDetail(w, data)
}

func (s *Server) releaseCVEState(w http.ResponseWriter, r *http.Request) {
	shell, releaseID, organisationID, ok := s.releaseMutationTarget(w, r, r.URL.Query().Get("organisation_id"))
	if !ok {
		return
	}
	data, err := s.loadReleaseDetailPageData(r.Context(), shell, releaseID, organisationID, "", "")
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "release detail query error", http.StatusInternalServerError)
		return
	}
	s.renderReleaseCVEState(w, data)
}

func (s *Server) releaseEvents(w http.ResponseWriter, r *http.Request) {
	_, releaseID, organisationID, ok := s.releaseMutationTarget(w, r, r.URL.Query().Get("organisation_id"))
	if !ok {
		return
	}
	if _, err := s.store.SoftwareRelease(r.Context(), releaseID, organisationID); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	scans, unsubscribe := s.store.SubscribeReleaseCVEScans(r.Context(), organisationID, releaseID)
	defer unsubscribe()

	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-scans:
			_, _ = w.Write([]byte("event: release-cves\n"))
			_, _ = w.Write([]byte("data: refresh\n\n"))
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": heartbeat\n\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) releaseSBOMReplacePost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReleaseSBOMTotalSize+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.renderReleaseDetailMutationError(w, r, "Replacement SBOM upload is invalid or too large.", "")
		return
	}
	shell, releaseID, organisationID, ok := s.releaseMutationTarget(w, r, r.FormValue("organisation_id"))
	if !ok {
		return
	}
	release, err := s.store.SoftwareRelease(r.Context(), releaseID, organisationID)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}

	sbom, err := s.replaceReleaseSBOMFiles(r, release.ArtifactPath)
	if err != nil {
		s.renderReleaseDetailForOrganisationWithError(w, r, shell, releaseID, organisationID, err.Error(), "")
		return
	}
	if _, err := s.store.ReplaceReleaseSBOM(r.Context(), organisationID, releaseID, sbom.Count, sbom.SizeBytes); err != nil {
		http.Error(w, "release sbom replace error", http.StatusInternalServerError)
		return
	}
	if _, err := s.store.EnqueueCVEScan(r.Context(), organisationID, releaseID, "auto"); err != nil && !errors.Is(err, db.ErrConflict) {
		http.Error(w, "release scan enqueue error", http.StatusInternalServerError)
		return
	}
	if s.cveScanWorker != nil {
		s.cveScanWorker.Notify()
	}
	http.Redirect(w, r, releaseDetailURL(releaseID, organisationID), http.StatusSeeOther)
}

func (s *Server) releaseRescanPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderReleaseDetailMutationError(w, r, "", "Rescan request is invalid.")
		return
	}
	shell, releaseID, organisationID, ok := s.releaseMutationTarget(w, r, r.FormValue("organisation_id"))
	if !ok {
		return
	}
	if _, err := s.store.SoftwareRelease(r.Context(), releaseID, organisationID); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}
	if _, err := s.store.EnqueueCVEScan(r.Context(), organisationID, releaseID, "manual"); errors.Is(err, db.ErrConflict) {
		s.renderReleaseDetailForOrganisationWithError(w, r, shell, releaseID, organisationID, "", "A scan is already pending or running.")
		return
	} else if errors.Is(err, db.ErrNotFound) {
		s.renderReleaseDetailForOrganisationWithError(w, r, shell, releaseID, organisationID, "", "Upload an SBOM before scanning.")
		return
	} else if err != nil {
		http.Error(w, "release scan enqueue error", http.StatusInternalServerError)
		return
	}
	if s.cveScanWorker != nil {
		s.cveScanWorker.Notify()
	}
	http.Redirect(w, r, releaseDetailURL(releaseID, organisationID), http.StatusSeeOther)
}

func (s *Server) releaseCVEMarkNotRelevantPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "waiver request is invalid", http.StatusBadRequest)
		return
	}
	shell, releaseID, organisationID, ok := s.releaseMutationTarget(w, r, r.FormValue("organisation_id"))
	if !ok {
		return
	}
	cveID := strings.TrimSpace(r.PathValue("cveID"))
	if cveID == "" {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.UpsertReleaseCVEWaiver(r.Context(), domain.ReleaseCVEWaiver{
		OrganisationID: organisationID,
		ReleaseID:      releaseID,
		CVEID:          cveID,
		Note:           r.FormValue("note"),
		UserID:         shell.User.ID,
	}); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "release cve waiver error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, releaseDetailURL(releaseID, organisationID), http.StatusSeeOther)
}

func (s *Server) releaseCVEUnmarkNotRelevantPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "waiver request is invalid", http.StatusBadRequest)
		return
	}
	_, releaseID, organisationID, ok := s.releaseMutationTarget(w, r, r.FormValue("organisation_id"))
	if !ok {
		return
	}
	cveID := strings.TrimSpace(r.PathValue("cveID"))
	if cveID == "" {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteReleaseCVEWaiver(r.Context(), organisationID, releaseID, cveID); err != nil {
		http.Error(w, "release cve waiver delete error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, releaseDetailURL(releaseID, organisationID), http.StatusSeeOther)
}

func (s *Server) releaseMutationTarget(w http.ResponseWriter, r *http.Request, organisationValue string) (shellPageData, int64, int64, bool) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return shellPageData{}, 0, 0, false
	}
	releaseID, err := strconv.ParseInt(r.PathValue("releaseID"), 10, 64)
	if err != nil || releaseID <= 0 {
		http.NotFound(w, r)
		return shellPageData{}, 0, 0, false
	}
	organisationID, ok := requestedOrganisationID(organisationValue, shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return shellPageData{}, 0, 0, false
	}
	shell.SelectedOrganisationID = organisationID
	return shell, releaseID, organisationID, true
}

func (s *Server) releaseBinary(w http.ResponseWriter, r *http.Request) {
	releaseID, err := strconv.ParseInt(r.PathValue("releaseID"), 10, 64)
	if err != nil || releaseID <= 0 {
		http.NotFound(w, r)
		return
	}

	organisationID, err := strconv.ParseInt(r.PathValue("organisationID"), 10, 64)
	if err != nil || organisationID <= 0 {
		http.NotFound(w, r)
		return
	}

	release, err := s.store.SoftwareRelease(r.Context(), releaseID, organisationID)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}

	path, ok := s.releaseArtifactFullPath(release.ArtifactPath)
	if !ok {
		http.Error(w, "release artifact path error", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "release artifact read error", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "release artifact stat error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", release.ArtifactContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", release.ArtifactFilename))
	http.ServeContent(w, r, release.ArtifactFilename, stat.ModTime(), file)
}

type releaseArtifactUpload struct {
	Path        string
	Filename    string
	ContentType string
	SizeBytes   int64
}

type releaseSBOMUpload struct {
	RelativeDir string
	Count       int
	SizeBytes   int64
}

func (s *Server) saveReleaseArtifact(r *http.Request, organisationID int64) (releaseArtifactUpload, func(), error) {
	file, header, err := r.FormFile("artifact")
	if err != nil {
		return releaseArtifactUpload{}, nil, errors.New("Release binary is required.")
	}
	defer file.Close()

	filename := cleanUploadFilename(header.Filename)
	token, err := randomToken()
	if err != nil {
		return releaseArtifactUpload{}, nil, fmt.Errorf("prepare release artifact: %w", err)
	}

	relativePath := filepath.Join(strconv.FormatInt(organisationID, 10), token+"-"+filename)
	fullPath, ok := s.releaseArtifactFullPath(relativePath)
	if !ok {
		return releaseArtifactUpload{}, nil, errors.New("Release artifact path is invalid.")
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return releaseArtifactUpload{}, nil, fmt.Errorf("prepare release artifact storage: %w", err)
	}

	destination, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return releaseArtifactUpload{}, nil, fmt.Errorf("create release artifact: %w", err)
	}
	written, copyErr := io.Copy(destination, file)
	closeErr := destination.Close()
	if copyErr != nil {
		_ = os.Remove(fullPath)
		return releaseArtifactUpload{}, nil, fmt.Errorf("save release artifact: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(fullPath)
		return releaseArtifactUpload{}, nil, fmt.Errorf("save release artifact: %w", closeErr)
	}
	if written == 0 {
		_ = os.Remove(fullPath)
		return releaseArtifactUpload{}, nil, errors.New("Release binary must not be empty.")
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return releaseArtifactUpload{
			Path:        relativePath,
			Filename:    filename,
			ContentType: contentType,
			SizeBytes:   written,
		}, func() {
			_ = os.Remove(fullPath)
		}, nil
}

func (s *Server) saveReleaseSBOMFiles(r *http.Request, artifactPath string) (releaseSBOMUpload, func(), error) {
	noopCleanup := func() {}
	selected, totalSize, err := selectedReleaseSBOMFiles(r)
	if err != nil {
		return releaseSBOMUpload{}, noopCleanup, err
	}
	if len(selected) == 0 {
		return releaseSBOMUpload{}, noopCleanup, nil
	}

	relativeDir := releaseSBOMRelativeDir(artifactPath)
	cleanup, err := s.writeSelectedReleaseSBOMFiles(selected, relativeDir)
	if err != nil {
		return releaseSBOMUpload{}, noopCleanup, err
	}
	return releaseSBOMUpload{
		RelativeDir: relativeDir,
		Count:       len(selected),
		SizeBytes:   totalSize,
	}, cleanup, nil
}

func (s *Server) replaceReleaseSBOMFiles(r *http.Request, artifactPath string) (releaseSBOMUpload, error) {
	selected, totalSize, err := selectedReleaseSBOMFiles(r)
	if err != nil {
		return releaseSBOMUpload{}, err
	}
	if len(selected) == 0 {
		return releaseSBOMUpload{}, errors.New("Replacement SBOM requires at least one .spdx file.")
	}

	token, err := randomToken()
	if err != nil {
		return releaseSBOMUpload{}, fmt.Errorf("prepare SBOM storage: %w", err)
	}
	relativeDir := releaseSBOMRelativeDir(artifactPath)
	stagedRelativeDir := relativeDir + ".upload-" + token
	stagedCleanup, err := s.writeSelectedReleaseSBOMFiles(selected, stagedRelativeDir)
	if err != nil {
		return releaseSBOMUpload{}, err
	}
	committed := false
	defer func() {
		if !committed {
			stagedCleanup()
		}
	}()

	fullDir, ok := s.releaseArtifactFullPath(relativeDir)
	if !ok {
		return releaseSBOMUpload{}, errors.New("SBOM storage path is invalid.")
	}
	stagedFullDir, ok := s.releaseArtifactFullPath(stagedRelativeDir)
	if !ok {
		return releaseSBOMUpload{}, errors.New("SBOM storage path is invalid.")
	}
	if err := os.RemoveAll(fullDir); err != nil {
		return releaseSBOMUpload{}, fmt.Errorf("replace SBOM storage: %w", err)
	}
	if err := os.Rename(stagedFullDir, fullDir); err != nil {
		return releaseSBOMUpload{}, fmt.Errorf("replace SBOM storage: %w", err)
	}
	committed = true

	return releaseSBOMUpload{
		RelativeDir: relativeDir,
		Count:       len(selected),
		SizeBytes:   totalSize,
	}, nil
}

func selectedReleaseSBOMFiles(r *http.Request) ([]*multipartFileHeader, int64, error) {
	if r.MultipartForm == nil {
		return nil, 0, nil
	}

	headers := r.MultipartForm.File[releaseSBOMFormFieldName]
	selected := make([]*multipartFileHeader, 0, len(headers))
	seenFilenames := make(map[string]struct{})
	var totalSize int64
	for _, header := range headers {
		if strings.TrimSpace(header.Filename) == "" {
			continue
		}
		filename := cleanUploadFilename(header.Filename)
		if !strings.EqualFold(filepath.Ext(filename), ".spdx") {
			return nil, 0, errors.New("SBOM files must use the .spdx extension.")
		}
		if header.Size <= 0 {
			return nil, 0, errors.New("SBOM files must not be empty.")
		}
		if header.Size > maxReleaseSBOMFileSize {
			return nil, 0, fmt.Errorf("Each SBOM file must be %d MB or smaller.", maxReleaseSBOMFileSize>>20)
		}
		totalSize += header.Size
		if totalSize > maxReleaseSBOMTotalSize {
			return nil, 0, fmt.Errorf("SBOM uploads must be %d MB total or smaller.", maxReleaseSBOMTotalSize>>20)
		}
		if _, ok := seenFilenames[filename]; ok {
			return nil, 0, errors.New("SBOM filenames must be unique after cleanup.")
		}
		file, err := header.Open()
		if err != nil {
			return nil, 0, fmt.Errorf("read SBOM file: %w", err)
		}
		shapeErr := validateSPDXShape(file)
		closeErr := file.Close()
		if shapeErr != nil {
			return nil, 0, shapeErr
		}
		if closeErr != nil {
			return nil, 0, fmt.Errorf("read SBOM file: %w", closeErr)
		}
		seenFilenames[filename] = struct{}{}
		selected = append(selected, &multipartFileHeader{
			header:   header,
			filename: filename,
		})
	}
	if len(selected) > maxReleaseSBOMFiles {
		return nil, 0, fmt.Errorf("A release can include up to %d SBOM files.", maxReleaseSBOMFiles)
	}
	return selected, totalSize, nil
}

func (s *Server) writeSelectedReleaseSBOMFiles(selected []*multipartFileHeader, relativeDir string) (func(), error) {
	fullDir, ok := s.releaseArtifactFullPath(relativeDir)
	if !ok {
		return nil, errors.New("SBOM storage path is invalid.")
	}
	if err := os.MkdirAll(fullDir, 0o755); err != nil {
		return nil, fmt.Errorf("prepare SBOM storage: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(fullDir)
	}

	for _, selectedFile := range selected {
		file, err := selectedFile.header.Open()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("read SBOM file: %w", err)
		}

		fullPath := filepath.Join(fullDir, selectedFile.filename)
		destination, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			_ = file.Close()
			cleanup()
			return nil, fmt.Errorf("create SBOM file: %w", err)
		}
		written, copyErr := io.Copy(destination, io.LimitReader(file, maxReleaseSBOMFileSize+1))
		closeDestinationErr := destination.Close()
		closeSourceErr := file.Close()
		if copyErr != nil {
			cleanup()
			return nil, fmt.Errorf("save SBOM file: %w", copyErr)
		}
		if closeDestinationErr != nil {
			cleanup()
			return nil, fmt.Errorf("save SBOM file: %w", closeDestinationErr)
		}
		if closeSourceErr != nil {
			cleanup()
			return nil, fmt.Errorf("read SBOM file: %w", closeSourceErr)
		}
		if written == 0 {
			cleanup()
			return nil, errors.New("SBOM files must not be empty.")
		}
		if written > maxReleaseSBOMFileSize {
			cleanup()
			return nil, fmt.Errorf("Each SBOM file must be %d MB or smaller.", maxReleaseSBOMFileSize>>20)
		}
	}
	return cleanup, nil
}

type multipartFileHeader struct {
	header   *multipart.FileHeader
	filename string
}

func validateSPDXShape(file multipart.File) error {
	probe := make([]byte, 64<<10)
	n, err := file.Read(probe)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read SBOM file: %w", err)
	}
	content := strings.TrimSpace(strings.TrimPrefix(string(probe[:n]), "\ufeff"))
	if content == "" {
		return errors.New("SBOM files must not be empty.")
	}
	if strings.HasPrefix(content, "SPDXVersion:") {
		return nil
	}
	if strings.HasPrefix(content, "{") && strings.Contains(content, "spdxVersion") {
		return nil
	}
	return errors.New("SBOM files must look like SPDX tag-value or SPDX JSON documents.")
}

func releaseSBOMRelativeDir(artifactPath string) string {
	return artifactPath + ".sbom"
}

func (s *Server) releaseViews(ctx context.Context, organisationID int64) ([]releaseView, error) {
	releases, err := s.store.ListSoftwareReleases(ctx, organisationID)
	if err != nil {
		return nil, err
	}

	views := make([]releaseView, 0, len(releases))
	for _, release := range releases {
		status, err := s.store.ReleaseCVESummary(ctx, organisationID, release.ID)
		if err != nil {
			return nil, err
		}
		scanRuns, err := s.store.ListCVEScanRuns(ctx, organisationID, release.ID)
		if err != nil {
			return nil, err
		}
		lastScanAt := ""
		if len(scanRuns) > 0 {
			lastScanAt = cveScanRunDisplayTime(scanRuns[0])
		}
		var counts cveSeverityCountsView
		if status.LatestSuccessfulScanID > 0 {
			activeFindings, err := s.store.ListActiveCVEFindings(ctx, organisationID, release.ID)
			if errors.Is(err, db.ErrNotFound) {
				activeFindings = nil
			} else if err != nil {
				return nil, err
			}
			counts = cveSeverityCounts(activeFindings)
			counts.HasData = true
		}
		views = append(views, releaseView{
			SoftwareRelease: release,
			CVEStatus:       cveStatusViewFor(status),
			CVECounts:       counts,
			LastScanAt:      lastScanAt,
		})
	}
	return views, nil
}

func (s *Server) releaseArtifactFullPath(relativePath string) (string, bool) {
	cleanPath := filepath.Clean(relativePath)
	if cleanPath == "." || filepath.IsAbs(cleanPath) || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) || cleanPath == ".." {
		return "", false
	}
	return filepath.Join(s.releaseStorageDir, cleanPath), true
}

func cleanUploadFilename(filename string) string {
	filename = filepath.Base(filename)
	filename = strings.TrimSpace(filename)
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		return "release.bin"
	}

	var builder strings.Builder
	for _, char := range filename {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= 'A' && char <= 'Z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '.', char == '-', char == '_':
			builder.WriteRune(char)
		default:
			builder.WriteRune('-')
		}
	}
	cleaned := strings.Trim(builder.String(), ".-")
	if cleaned == "" {
		return "release.bin"
	}
	return cleaned
}

func (s *Server) campaigns(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}

	campaigns, err := s.store.ListCampaigns(r.Context(), organisationID)
	if err != nil {
		http.Error(w, "campaign query error", http.StatusInternalServerError)
		return
	}

	views := make([]campaignView, 0, len(campaigns))
	for _, campaign := range campaigns {
		views = append(views, s.campaignView(campaign))
	}
	s.renderCampaigns(w, campaignsPageData{
		Shell:     shell,
		Campaigns: views,
	})
}

func (s *Server) campaignNew(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	taskType := r.URL.Query().Get("task_type")
	if taskType == "" {
		taskType = domain.TaskTypeRead
	}
	data, err := s.loadCampaignSelectionPageData(r.Context(), shell, organisationID, nil, taskType, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.renderCampaignNew(w, data)
}

func (s *Server) campaignNewPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	taskType := r.FormValue("task_type")
	_, _, ok = campaignTaskLaunchCopy(taskType)
	if !ok {
		http.Error(w, "choose a supported task type", http.StatusBadRequest)
		return
	}
	deviceIDs := nonBlankStrings(r.Form["device_id"])
	if len(deviceIDs) == 0 {
		http.Error(w, "Select at least one device from the device list before creating a campaign.", http.StatusBadRequest)
		return
	}
	data, err := s.loadCampaignSelectionPageData(r.Context(), shell, organisationID, deviceIDs, taskType, "")
	if err != nil {
		http.Error(w, "The selected devices are unavailable in this organisation. Return to the device list and select them again.", http.StatusBadRequest)
		return
	}
	s.renderCampaignNew(w, data)
}

func (s *Server) campaignEstimate(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "invalid_organisation", "Choose an organisation.")
		return
	}
	selector, err := campaignTargetFromValues(organisationID, r.URL.Query())
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_target", err.Error())
		return
	}
	count, err := s.store.EstimateCampaignTargets(r.Context(), selector)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_target", err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]int{"count": count})
}

func (s *Server) campaignsPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	selector, targetErr := campaignTargetFromValues(organisationID, r.Form)
	taskType := r.FormValue("task_type")
	if _, _, ok := deviceTaskLaunchCopy(taskType); !ok {
		http.Error(w, "choose a supported task type", http.StatusBadRequest)
		return
	}
	parametersJSON, err := s.taskParametersFromForm(r, taskType, organisationID)
	if targetErr != nil {
		err = targetErr
	}
	_, ttlSeconds, ttlErr := domain.ParseTaskTTLDays(r.FormValue("ttl_days"))
	if ttlErr != nil && err == nil {
		err = ttlErr
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" && err == nil {
		err = errors.New("campaign name is required")
	}
	if parametersJSON == "" && err == nil {
		err = errors.New("choose a supported task type")
	}
	if err != nil {
		s.renderCampaignSubmissionError(w, r, shell, organisationID, selector, err)
		return
	}
	result, err := s.store.CreateCampaign(r.Context(), db.CampaignCreate{
		OrganisationID: organisationID,
		Name:           name,
		TaskType:       taskType,
		ParametersJSON: parametersJSON,
		TTLSeconds:     ttlSeconds,
		DeviceIDs:      selector.DeviceIDs,
		TargetType:     selector.TargetType,
		TargetTag:      selector.Tag,
		TargetModelID:  selector.ModelID,
		CreatedAt:      time.Now().UTC(),
	})
	if err != nil {
		s.renderCampaignSubmissionError(w, r, shell, organisationID, selector, err)
		return
	}
	for _, task := range result.PendingTasks {
		s.publishDeviceTask(r.Context(), task, organisationID)
	}
	s.processTaskQueue(r.Context())
	http.Redirect(w, r, campaignDetailURL(result.Campaign.ID, organisationID), http.StatusSeeOther)
}

// The creation screen chooses one or both filters without exposing the storage
// targeting modes. Infer the selector on the server so no JavaScript is required.
func campaignTargetFromValues(organisationID int64, values url.Values) (db.CampaignTargetSelector, error) {
	selector := db.CampaignTargetSelector{
		OrganisationID: organisationID,
		TargetType:     strings.TrimSpace(values.Get("target_type")),
		DeviceIDs:      nonBlankStrings(values["device_id"]),
		Tag:            strings.TrimSpace(values.Get("target_tag")),
	}
	if selector.TargetType == "" {
		selector.TargetType = db.CampaignTargetExplicit
	}
	if rawModel := strings.TrimSpace(values.Get("target_model_id")); rawModel != "" {
		modelID, err := strconv.ParseInt(rawModel, 10, 64)
		if err != nil || modelID <= 0 {
			return selector, errors.New("Choose a valid device model.")
		}
		selector.ModelID = modelID
	}
	if selector.TargetType == "filters" {
		if len(selector.DeviceIDs) != 0 {
			return selector, errors.New("Choose tag/model filters or selected devices, not both.")
		}
		switch {
		case selector.Tag != "" && selector.ModelID > 0:
			selector.TargetType = db.CampaignTargetTagModel
		case selector.Tag != "":
			selector.TargetType = db.CampaignTargetTag
		case selector.ModelID > 0:
			selector.TargetType = db.CampaignTargetModel
		default:
			return selector, errors.New("Choose a tag, a model, or both to target devices.")
		}
	}
	return selector, nil
}

func (s *Server) renderCampaignSubmissionError(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, selector db.CampaignTargetSelector, submitErr error) {
	var deviceIDs []string
	if selector.TargetType == db.CampaignTargetExplicit {
		deviceIDs = selector.DeviceIDs
	}
	data, err := s.loadCampaignSelectionPageData(r.Context(), shell, organisationID, deviceIDs, r.FormValue("task_type"), submitErr.Error())
	if err != nil {
		http.Error(w, "The selected devices are unavailable. Return to the device list and select them again.", http.StatusBadRequest)
		return
	}
	data.Name = r.FormValue("name")
	data.ReadPaths = r.FormValue("read_paths")
	data.WriteValues = r.FormValue("write_values")
	data.ReleaseID, _ = strconv.ParseInt(r.FormValue("release_id"), 10, 64)
	data.TTLDays = parsePositiveInt(r.FormValue("ttl_days"), domain.DefaultTaskTTLDays)
	data.TargetType = selector.TargetType
	data.TargetTag = r.FormValue("target_tag")
	data.TargetModelID = selector.ModelID
	if count, estimateErr := s.store.EstimateCampaignTargets(r.Context(), selector); estimateErr == nil {
		data.EstimatedCount = count
	}
	s.renderCampaignNew(w, data)
}

func (s *Server) campaignDetail(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	campaignID, err := strconv.ParseInt(r.PathValue("campaignID"), 10, 64)
	if err != nil || campaignID <= 0 {
		http.NotFound(w, r)
		return
	}
	campaign, err := s.store.Campaign(r.Context(), organisationID, campaignID)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "campaign query error", http.StatusInternalServerError)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	pageSize := parsePositiveInt(r.URL.Query().Get("page_size"), db.DefaultDevicePageSize)
	taskPage, err := s.store.ListCampaignTasks(r.Context(), db.CampaignTaskQuery{OrganisationID: organisationID, CampaignID: campaignID, Status: status, Page: page, PageSize: pageSize})
	if err != nil {
		http.Error(w, "campaign task query error", http.StatusInternalServerError)
		return
	}
	tasks := make([]campaignTaskView, 0, len(taskPage.Rows))
	for _, row := range taskPage.Rows {
		tasks = append(tasks, s.campaignTaskView(row, organisationID, campaignID, r.URL.RawQuery))
	}
	s.renderCampaignDetail(w, campaignDetailPageData{
		Shell:        shell,
		Campaign:     s.campaignView(campaign),
		Tasks:        tasks,
		StatusFilter: status,
		Pagination:   campaignPaginationView(r.URL.Path, organisationID, campaignID, status, taskPage.Pagination),
	})
}

func (s *Server) campaignTaskCancelPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	campaignID, err := strconv.ParseInt(r.PathValue("campaignID"), 10, 64)
	if err != nil || campaignID <= 0 {
		http.NotFound(w, r)
		return
	}
	taskID, err := strconv.ParseInt(r.PathValue("taskID"), 10, 64)
	if err != nil || taskID <= 0 {
		http.NotFound(w, r)
		return
	}
	taskPage, err := s.store.ListCampaignTasks(r.Context(), db.CampaignTaskQuery{OrganisationID: organisationID, CampaignID: campaignID, Page: 1, PageSize: 100})
	if err != nil {
		http.Error(w, "campaign task query error", http.StatusInternalServerError)
		return
	}
	var deviceID string
	for _, row := range taskPage.Rows {
		if row.Task.ID == taskID {
			deviceID = row.Task.DeviceID
			break
		}
	}
	if deviceID == "" {
		http.NotFound(w, r)
		return
	}
	outcome, err := s.store.CancelDeviceTask(r.Context(), taskID, deviceID, organisationID, time.Now().UTC(), "")
	if err != nil {
		http.Error(w, "campaign task cancel error", http.StatusInternalServerError)
		return
	}
	if outcome == db.TaskTransitionNotFound {
		http.NotFound(w, r)
		return
	}
	_, _ = s.store.FinalizeFinishedCampaigns(r.Context(), time.Now().UTC())
	s.processTaskQueue(r.Context())
	http.Redirect(w, r, campaignDetailURL(campaignID, organisationID), http.StatusSeeOther)
}

func (s *Server) campaignCancelPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}
	campaignID, err := strconv.ParseInt(r.PathValue("campaignID"), 10, 64)
	if err != nil || campaignID <= 0 {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.CancelCampaign(r.Context(), organisationID, campaignID, time.Now().UTC()); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "campaign cancel error", http.StatusInternalServerError)
		return
	}
	s.processTaskQueue(r.Context())
	http.Redirect(w, r, campaignDetailURL(campaignID, organisationID), http.StatusSeeOther)
}

func (s *Server) organisations(w http.ResponseWriter, r *http.Request) {
	data, ok := s.loadOrganisationPageData(w, r, "", "", "", "", "")
	if !ok {
		return
	}
	s.renderOrganisation(w, data)
}

func (s *Server) organisationRenamePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	shell, organisationID, ok := s.organisationMutationTarget(w, r)
	if !ok {
		return
	}
	isAdmin, err := s.store.IsOrganisationAdmin(r.Context(), shell.User.ID, organisationID)
	if err != nil {
		http.Error(w, "organisation permission query error", http.StatusInternalServerError)
		return
	}
	if !isAdmin {
		http.Error(w, "organisation admin required", http.StatusForbidden)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderOrganisationForShell(w, r, shell, organisationID, "Organisation name is required.", "", "", "", "")
		return
	}
	if err := s.store.RenameOrganisation(r.Context(), organisationID, name); errors.Is(err, db.ErrConflict) {
		s.renderOrganisationForShell(w, r, shell, organisationID, "That organisation name is already in use.", "", "", "", "")
		return
	} else if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "organisation rename error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/organisations?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) organisationInvitationsPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	shell, organisationID, ok := s.organisationMutationTarget(w, r)
	if !ok {
		return
	}
	isAdmin, err := s.store.IsOrganisationAdmin(r.Context(), shell.User.ID, organisationID)
	if err != nil {
		http.Error(w, "organisation permission query error", http.StatusInternalServerError)
		return
	}
	if !isAdmin {
		http.Error(w, "organisation admin required", http.StatusForbidden)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		s.renderOrganisationForShell(w, r, shell, organisationID, "", "Email is required.", "", "", "")
		return
	}
	result, err := s.store.InviteUserToOrganisation(r.Context(), organisationID, shell.User.ID, email, time.Now())
	if err != nil {
		s.renderOrganisationForShell(w, r, shell, organisationID, "", "Could not create invitation.", "", "", "")
		return
	}

	if result.ExistingUser {
		s.renderOrganisationForShell(w, r, shell, organisationID, "", "", "", "", "Existing user added as a member.")
		return
	}
	inviteURL := "/invitations/" + result.Token
	s.renderOrganisationForShell(w, r, shell, organisationID, "", "", "", inviteURL, "Invitation URL created.")
}

func (s *Server) organisationMemberRemovePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	shell, organisationID, ok := s.organisationMutationTarget(w, r)
	if !ok {
		return
	}
	isAdmin, err := s.store.IsOrganisationAdmin(r.Context(), shell.User.ID, organisationID)
	if err != nil {
		http.Error(w, "organisation permission query error", http.StatusInternalServerError)
		return
	}
	if !isAdmin {
		http.Error(w, "organisation admin required", http.StatusForbidden)
		return
	}

	userID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		http.Error(w, "missing user id", http.StatusBadRequest)
		return
	}

	err = s.store.RemoveOrganisationMember(r.Context(), organisationID, userID)
	if errors.Is(err, db.ErrLastOrganisationAdmin) {
		s.renderOrganisationForShell(w, r, shell, organisationID, "", "", "An organisation must keep at least one admin.", "", "")
		return
	}
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "member remove error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/organisations?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) organisationAPICredentialsPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, organisationID, ok := s.organisationAdminMutationTarget(w, r)
	if !ok {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderOrganisationForShellWithAPI(w, r, shell, organisationID, "", "", "", "", "", "", "Credential name is required.", "")
		return
	}
	result, err := s.store.CreateOrganisationAPICredential(r.Context(), organisationID, name)
	if err != nil {
		s.renderOrganisationForShellWithAPI(w, r, shell, organisationID, "", "", "", "", "", "", "Could not create API credential.", "")
		return
	}
	s.renderOrganisationForShellWithAPI(w, r, shell, organisationID, "", "", "", "", "", result.Token, "", "API credential created.")
}

func (s *Server) organisationAPICredentialDisablePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	_, organisationID, ok := s.organisationAdminMutationTarget(w, r)
	if !ok {
		return
	}
	credentialID, ok := credentialIDFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.DisableOrganisationAPICredential(r.Context(), organisationID, credentialID); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "api credential disable error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/organisations?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) organisationAPICredentialRotatePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, organisationID, ok := s.organisationAdminMutationTarget(w, r)
	if !ok {
		return
	}
	credentialID, ok := credentialIDFromPath(w, r)
	if !ok {
		return
	}
	result, err := s.store.RotateOrganisationAPICredential(r.Context(), organisationID, credentialID)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "api credential rotate error", http.StatusInternalServerError)
		return
	}
	s.renderOrganisationForShellWithAPI(w, r, shell, organisationID, "", "", "", "", "", result.Token, "", "API credential rotated.")
}

func (s *Server) organisationAdminMutationTarget(w http.ResponseWriter, r *http.Request) (shellPageData, int64, bool) {
	shell, organisationID, ok := s.organisationMutationTarget(w, r)
	if !ok {
		return shellPageData{}, 0, false
	}
	isAdmin, err := s.store.IsOrganisationAdmin(r.Context(), shell.User.ID, organisationID)
	if err != nil {
		http.Error(w, "organisation permission query error", http.StatusInternalServerError)
		return shellPageData{}, 0, false
	}
	if !isAdmin {
		http.Error(w, "organisation admin required", http.StatusForbidden)
		return shellPageData{}, 0, false
	}
	return shell, organisationID, true
}

func credentialIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	credentialID, err := strconv.ParseInt(r.PathValue("credentialID"), 10, 64)
	if err != nil || credentialID <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return credentialID, true
}

func (s *Server) invitationSignup(w http.ResponseWriter, r *http.Request) {
	invitation, err := s.store.InvitationByToken(r.Context(), r.PathValue("token"), time.Now())
	if errors.Is(err, db.ErrInvalidInvitation) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "invitation query error", http.StatusInternalServerError)
		return
	}

	s.renderInvitationSignup(w, invitationSignupPageData{Invitation: invitation})
}

func (s *Server) invitationSignupPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	token := r.PathValue("token")
	invitation, err := s.store.InvitationByToken(r.Context(), token, time.Now())
	if errors.Is(err, db.ErrInvalidInvitation) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "invitation query error", http.StatusInternalServerError)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	password := r.FormValue("password")
	if name == "" || password == "" {
		s.renderInvitationSignup(w, invitationSignupPageData{
			Invitation: invitation,
			Error:      "Display name and password are required.",
			Name:       name,
		})
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "credential error", http.StatusInternalServerError)
		return
	}
	acceptance, err := s.store.AcceptInvitation(r.Context(), token, domain.User{
		Name:         name,
		PasswordHash: string(passwordHash),
	}, time.Now())
	if errors.Is(err, db.ErrInvalidInvitation) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, db.ErrConflict) {
		s.renderInvitationSignup(w, invitationSignupPageData{
			Invitation: invitation,
			Error:      "An account already exists for this email.",
			Name:       name,
		})
		return
	}
	if err != nil {
		http.Error(w, "invitation acceptance error", http.StatusInternalServerError)
		return
	}

	sessionID, err := randomToken()
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	expiresAt := time.Now().Add(sessionDuration)
	if err := s.store.CreateSession(r.Context(), domain.Session{
		ID:        sessionID,
		UserID:    acceptance.UserID,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, sessionCookie(sessionID, expiresAt))
	http.Redirect(w, r, "/organisations?organisation_id="+strconv.FormatInt(acceptance.OrganisationID, 10), http.StatusSeeOther)
}

func (s *Server) organisationMutationTarget(w http.ResponseWriter, r *http.Request) (shellPageData, int64, bool) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return shellPageData{}, 0, false
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.Error(w, "missing organisation id", http.StatusBadRequest)
		return shellPageData{}, 0, false
	}
	return shell, organisationID, true
}

func (s *Server) renderOrganisationForShell(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, renameError string, inviteError string, removeError string, inviteURL string, inviteMessage string) {
	s.renderOrganisationForShellWithAPI(w, r, shell, organisationID, renameError, inviteError, removeError, inviteURL, inviteMessage, "", "", "")
}

func (s *Server) renderOrganisationForShellWithAPI(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, renameError string, inviteError string, removeError string, inviteURL string, inviteMessage string, apiToken string, apiError string, apiMessage string) {
	data, ok := s.loadOrganisationPageDataForShell(w, r, shell, organisationID, renameError, inviteError, removeError, inviteURL, inviteMessage, apiToken, apiError, apiMessage)
	if !ok {
		return
	}
	s.renderOrganisation(w, data)
}

func (s *Server) loadOrganisationPageData(w http.ResponseWriter, r *http.Request, renameError string, inviteError string, removeError string, inviteURL string, inviteMessage string) (organisationPageData, bool) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return organisationPageData{}, false
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return organisationPageData{}, false
	}
	return s.loadOrganisationPageDataForShell(w, r, shell, organisationID, renameError, inviteError, removeError, inviteURL, inviteMessage, "", "", "")
}

func (s *Server) loadOrganisationPageDataForShell(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, renameError string, inviteError string, removeError string, inviteURL string, inviteMessage string, apiToken string, apiError string, apiMessage string) (organisationPageData, bool) {
	organisation, err := s.store.Organisation(r.Context(), organisationID)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return organisationPageData{}, false
	}
	if err != nil {
		http.Error(w, "organisation query error", http.StatusInternalServerError)
		return organisationPageData{}, false
	}

	isOrganisationAdmin, err := s.store.IsOrganisationAdmin(r.Context(), shell.User.ID, organisationID)
	if err != nil {
		http.Error(w, "organisation permission query error", http.StatusInternalServerError)
		return organisationPageData{}, false
	}
	members, err := s.store.ListOrganisationMembers(r.Context(), organisationID)
	if err != nil {
		http.Error(w, "organisation members query error", http.StatusInternalServerError)
		return organisationPageData{}, false
	}
	admins := make([]domain.OrganisationMember, 0)
	for _, member := range members {
		if member.Role == db.OrganisationRoleAdmin {
			admins = append(admins, member)
		}
	}
	apiCredentials := []apiCredentialView{}
	if isOrganisationAdmin {
		credentials, err := s.store.ListOrganisationAPICredentials(r.Context(), organisationID)
		if err != nil {
			http.Error(w, "api credential query error", http.StatusInternalServerError)
			return organisationPageData{}, false
		}
		apiCredentials = make([]apiCredentialView, 0, len(credentials))
		for _, credential := range credentials {
			apiCredentials = append(apiCredentials, apiCredentialViewFor(credential))
		}
	}

	shell.SelectedOrganisationID = organisationID
	return organisationPageData{
		Shell:               shell,
		Organisation:        organisation,
		Admins:              admins,
		Members:             members,
		APICredentials:      apiCredentials,
		IsOrganisationAdmin: isOrganisationAdmin,
		RenameFormError:     renameError,
		InviteFormError:     inviteError,
		RemoveFormError:     removeError,
		InviteURL:           inviteURL,
		InviteMessage:       inviteMessage,
		APIToken:            apiToken,
		APIFormError:        apiError,
		APIMessage:          apiMessage,
	}, true
}

func (s *Server) shellData(w http.ResponseWriter, r *http.Request) (shellPageData, bool) {
	user, _ := s.currentUser(r)
	organisations, err := s.store.ListOrganisationsForUser(r.Context(), user)
	if err != nil {
		http.Error(w, "organisation query error", http.StatusInternalServerError)
		return shellPageData{}, false
	}

	return shellPageData{
		User:                   user,
		Organisations:          organisations,
		SelectedOrganisationID: selectedOrganisationID(r, organisations),
	}, true
}

func (s *Server) renderDeviceNewWithError(w http.ResponseWriter, r *http.Request, message string) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		organisationID = shell.SelectedOrganisationID
	}

	s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, message)
}

func (s *Server) renderDeviceNewForOrganisationWithError(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, message string) {
	data, err := s.loadDeviceCreatePageData(r.Context(), shell, organisationID, message)
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	data.DeviceID = strings.TrimSpace(r.FormValue("device_id"))
	data.MQTTUsername = strings.TrimSpace(r.FormValue("mqtt_username"))
	data.CoAPPSKIdentity = strings.TrimSpace(r.FormValue("coap_psk_identity"))
	data.IsGateway = r.FormValue("is_gateway") == "on"
	data.TagsInput = r.FormValue("tags")
	if selectedModelID, err := strconv.ParseInt(r.FormValue("device_model_id"), 10, 64); err == nil {
		for index := range data.DeviceModels {
			data.DeviceModels[index].Selected = data.DeviceModels[index].ID == selectedModelID
		}
	}

	s.renderDeviceNew(w, data)
}

func (s *Server) renderReleasesWithError(w http.ResponseWriter, r *http.Request, message string) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.FormValue("organisation_id"), shell.Organisations)
	if !ok {
		http.Error(w, "missing organisation id", http.StatusBadRequest)
		return
	}

	s.renderReleaseNewForOrganisationWithError(w, r, shell, organisationID, message)
}

func (s *Server) renderReleaseDetailMutationError(w http.ResponseWriter, r *http.Request, replaceError string, rescanError string) {
	shell, releaseID, organisationID, ok := s.releaseMutationTarget(w, r, r.FormValue("organisation_id"))
	if !ok {
		return
	}
	s.renderReleaseDetailForOrganisationWithError(w, r, shell, releaseID, organisationID, replaceError, rescanError)
}

func (s *Server) renderReleaseDetailForOrganisationWithError(w http.ResponseWriter, r *http.Request, shell shellPageData, releaseID int64, organisationID int64, replaceError string, rescanError string) {
	data, err := s.loadReleaseDetailPageData(r.Context(), shell, releaseID, organisationID, replaceError, rescanError)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "release detail query error", http.StatusInternalServerError)
		return
	}
	s.renderReleaseDetail(w, data)
}

func (s *Server) renderReleaseNewForOrganisationWithError(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, message string) {
	data, err := s.loadReleaseCreatePageData(r.Context(), shell, organisationID, message)
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	s.renderReleaseNew(w, data)
}

func (s *Server) renderDeviceModelNewForOrganisationWithError(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, message string) {
	data, err := s.loadDeviceModelCreatePageData(r.Context(), shell, organisationID, message)
	if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}
	s.renderDeviceModelNew(w, data)
}

func (s *Server) renderDeviceModelDetailForOrganisationWithError(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, modelID int64, message string) {
	data, err := s.loadDeviceModelDetailPageData(r.Context(), shell, organisationID, modelID, message)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	s.renderDeviceModelDetail(w, data)
}

func (s *Server) renderDevices(w http.ResponseWriter, r *http.Request, data devicesPageData) {
	if err := s.render(w, http.StatusOK, data, "devices.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceNew(w http.ResponseWriter, data deviceCreatePageData) {
	if err := s.render(w, http.StatusOK, data, "device_new.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceCreated(w http.ResponseWriter, data deviceCreatedPageData) {
	if err := s.render(w, http.StatusCreated, data, "device_created.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceDetail(w http.ResponseWriter, data deviceDetailPageData) {
	if err := s.render(w, http.StatusOK, data, "device_detail.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceTelemetry(w http.ResponseWriter, data deviceDetailPageData) {
	if err := s.render(w, http.StatusOK, data, "device_telemetry"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceTasks(w http.ResponseWriter, data deviceDetailPageData) {
	if err := s.render(w, http.StatusOK, data, "device_tasks"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceTaskNew(w http.ResponseWriter, data deviceTaskLaunchPageData) {
	if err := s.render(w, http.StatusOK, data, "device_task_new.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderCampaignNew(w http.ResponseWriter, data campaignSelectionPageData) {
	if err := s.render(w, http.StatusOK, data, "campaign_new.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderCampaigns(w http.ResponseWriter, data campaignsPageData) {
	if err := s.render(w, http.StatusOK, data, "campaigns.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderCampaignDetail(w http.ResponseWriter, data campaignDetailPageData) {
	if err := s.render(w, http.StatusOK, data, "campaign_detail.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderReleases(w http.ResponseWriter, data releasesPageData) {
	if err := s.render(w, http.StatusOK, data, "releases.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderReleaseNew(w http.ResponseWriter, data releaseCreatePageData) {
	if err := s.render(w, http.StatusOK, data, "release_new.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderReleaseDetail(w http.ResponseWriter, data releaseDetailPageData) {
	if err := s.render(w, http.StatusOK, data, "release_detail.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderReleaseCVEState(w http.ResponseWriter, data releaseDetailPageData) {
	if err := s.render(w, http.StatusOK, data, "release_cve_state"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceModels(w http.ResponseWriter, data deviceModelsPageData) {
	if err := s.render(w, http.StatusOK, data, "device_models.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceModelNew(w http.ResponseWriter, data deviceModelCreatePageData) {
	if err := s.render(w, http.StatusOK, data, "device_model_new.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceModelDetail(w http.ResponseWriter, data deviceModelDetailPageData) {
	if err := s.render(w, http.StatusOK, data, "device_model_detail.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderOrganisation(w http.ResponseWriter, data organisationPageData) {
	if err := s.render(w, http.StatusOK, data, "organisations.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderInvitationSignup(w http.ResponseWriter, data invitationSignupPageData) {
	if err := s.render(w, http.StatusOK, data, "invitation_signup.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) mqttAuth(w http.ResponseWriter, r *http.Request) {
	req, err := parseMQTTAuthRequest(r)
	if err != nil || req.Username == "" || req.Password == "" {
		mqttUnauthorized(w)
		return
	}

	if s.internalMQTTClientAuthenticated(req.Username, req.Password) {
		mqttAuthorized(w)
		return
	}

	credential, err := s.store.FindMQTTCredentialByUsername(r.Context(), req.Username)
	if err != nil || !credential.Enabled {
		mqttUnauthorized(w)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(credential.PasswordHash), []byte(req.Password)) != nil {
		mqttUnauthorized(w)
		return
	}

	mqttAuthorized(w)
}

func (s *Server) mqttSuperuser(w http.ResponseWriter, r *http.Request) {
	mqttUnauthorized(w)
}

func (s *Server) mqttACL(w http.ResponseWriter, r *http.Request) {
	req, err := parseMQTTACLRequest(r)
	if err != nil || req.Username == "" || req.Topic == "" {
		mqttUnauthorized(w)
		return
	}

	actions := mqttActionsForAccess(req.Access)
	if len(actions) == 0 {
		mqttUnauthorized(w)
		return
	}

	if s.internalMQTTClientConfiguredFor(req.Username) {
		for _, action := range actions {
			if !s.internalMQTTClientTopicAllowed(action, req.Topic) {
				mqttUnauthorized(w)
				return
			}
		}
		mqttAuthorized(w)
		return
	}

	for _, action := range actions {
		allowed, err := s.mqttTopicAllowed(r.Context(), req.Username, action, req.Topic)
		if err != nil || !allowed {
			mqttUnauthorized(w)
			return
		}
	}

	s.publishPendingTasksAfterSubscribe(req.Topic, actions)
	mqttAuthorized(w)
}

func selectedOrganisationID(r *http.Request, organisations []domain.Organisation) int64 {
	return selectedOrganisationIDFromValue(r.URL.Query().Get("organisation_id"), organisations)
}

func selectedOrganisationIDFromValue(value string, organisations []domain.Organisation) int64 {
	if len(organisations) == 0 {
		return 0
	}

	requestedID, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		for _, organisation := range organisations {
			if organisation.ID == requestedID {
				return requestedID
			}
		}
	}

	return organisations[0].ID
}

func requestedOrganisationID(value string, organisations []domain.Organisation) (int64, bool) {
	if len(organisations) == 0 {
		return 0, false
	}
	if value == "" {
		return organisations[0].ID, true
	}

	requestedID, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		for _, organisation := range organisations {
			if organisation.ID == requestedID {
				return requestedID, true
			}
		}
	}

	return 0, false
}

func (s *Server) loadDeviceCreatePageData(ctx context.Context, shell shellPageData, organisationID int64, formError string) (deviceCreatePageData, error) {
	shell.SelectedOrganisationID = organisationID
	modelOptions, err := s.deviceModelOptions(ctx, organisationID)
	if err != nil {
		return deviceCreatePageData{}, err
	}
	tags, err := s.store.ListTagSuggestions(ctx, organisationID, "")
	if err != nil {
		return deviceCreatePageData{}, err
	}

	note := ""
	if len(modelOptions) == 0 {
		note = "Create a device model before registering devices."
	}
	return deviceCreatePageData{
		Shell:          shell,
		DeviceModels:   modelOptions,
		FormError:      formError,
		DeviceFormNote: note,
		TagSuggestions: tags,
	}, nil
}

func (s *Server) deviceModelOptions(ctx context.Context, organisationID int64) ([]deviceModelOptionView, error) {
	models, err := s.store.ListDeviceModels(ctx, organisationID)
	if err != nil {
		return nil, err
	}
	modelOptions := make([]deviceModelOptionView, 0, len(models))
	for _, model := range models {
		modelOptions = append(modelOptions, deviceModelOption(model))
	}
	return modelOptions, nil
}

func (s *Server) loadDeviceModelsPageData(ctx context.Context, shell shellPageData, organisationID int64) (deviceModelsPageData, error) {
	shell.SelectedOrganisationID = organisationID
	models, err := s.store.ListDeviceModels(ctx, organisationID)
	if err != nil {
		return deviceModelsPageData{}, err
	}
	modelViews := make([]deviceModelView, 0, len(models))
	for _, model := range models {
		modelViews = append(modelViews, deviceModelViewFor(model))
	}
	return deviceModelsPageData{
		Shell:  shell,
		Models: modelViews,
	}, nil
}

func (s *Server) loadDeviceModelCreatePageData(ctx context.Context, shell shellPageData, organisationID int64, formError string) (deviceModelCreatePageData, error) {
	shell.SelectedOrganisationID = organisationID
	releases, err := s.loadReleaseOptions(ctx, organisationID)
	if err != nil {
		return deviceModelCreatePageData{}, err
	}
	return deviceModelCreatePageData{
		Shell:          shell,
		Releases:       releases,
		ModelFormError: formError,
	}, nil
}

func (s *Server) loadDeviceModelDetailPageData(ctx context.Context, shell shellPageData, organisationID int64, modelID int64, formError string) (deviceModelDetailPageData, error) {
	shell.SelectedOrganisationID = organisationID
	model, err := s.store.DeviceModel(ctx, modelID, organisationID)
	if err != nil {
		return deviceModelDetailPageData{}, err
	}
	releases, err := s.loadReleaseOptions(ctx, organisationID)
	if err != nil {
		return deviceModelDetailPageData{}, err
	}
	if model.ExpectedReleaseID != nil {
		for i := range releases {
			releases[i].Selected = releases[i].ID == *model.ExpectedReleaseID
		}
	}
	return deviceModelDetailPageData{
		Shell:                    shell,
		Model:                    deviceModelViewFor(model),
		Releases:                 releases,
		ExpectedReleaseFormError: formError,
	}, nil
}

func (s *Server) loadReleaseCreatePageData(ctx context.Context, shell shellPageData, organisationID int64, formError string) (releaseCreatePageData, error) {
	shell.SelectedOrganisationID = organisationID
	models, err := s.deviceModelOptions(ctx, organisationID)
	if err != nil {
		return releaseCreatePageData{}, err
	}

	note := ""
	if len(models) == 0 {
		note = "Create a device model before creating releases."
	}
	return releaseCreatePageData{
		Shell:            shell,
		DeviceModels:     models,
		ReleaseFormError: formError,
		ReleaseFormNote:  note,
	}, nil
}

func (s *Server) loadReleaseDetailPageData(ctx context.Context, shell shellPageData, releaseID int64, organisationID int64, replaceError string, rescanError string) (releaseDetailPageData, error) {
	shell.SelectedOrganisationID = organisationID
	release, err := s.store.SoftwareRelease(ctx, releaseID, organisationID)
	if err != nil {
		return releaseDetailPageData{}, err
	}

	var sbomView releaseSBOMView
	sbom, err := s.store.CurrentReleaseSBOM(ctx, organisationID, releaseID)
	if errors.Is(err, db.ErrNotFound) {
		err = nil
	} else if err == nil {
		sbomView = releaseSBOMView{
			Present:        true,
			FileCount:      sbom.FileCount,
			TotalSizeBytes: sbom.TotalSizeBytes,
			UpdatedAt:      sbom.UpdatedAt,
		}
	}
	if err != nil {
		return releaseDetailPageData{}, err
	}

	status, err := s.store.ReleaseCVEImpactStatus(ctx, organisationID, releaseID)
	if err != nil {
		return releaseDetailPageData{}, err
	}
	scanRuns, err := s.store.ListCVEScanRuns(ctx, organisationID, releaseID)
	if err != nil {
		return releaseDetailPageData{}, err
	}
	currentFindings, err := s.store.ListCurrentCVEFindings(ctx, organisationID, releaseID)
	if errors.Is(err, db.ErrNotFound) {
		currentFindings = nil
	} else if err != nil {
		return releaseDetailPageData{}, err
	}
	waivers, err := s.store.ListReleaseCVEWaivers(ctx, organisationID, releaseID)
	if err != nil {
		return releaseDetailPageData{}, err
	}

	activeCVEs, waivedCVEs := groupedCVEFindings(currentFindings, waivers)
	scanRunViews := make([]scanRunView, 0, len(scanRuns))
	for _, run := range scanRuns {
		scanRunViews = append(scanRunViews, scanRunView{
			ID:           run.ID,
			Trigger:      run.Trigger,
			Status:       cveScanRunStatusLabel(run.Status),
			StatusClass:  cveScanRunStatusClass(run.Status),
			ErrorMessage: run.ErrorMessage,
			CreatedAt:    run.CreatedAt,
			StartedAt:    run.StartedAt,
			FinishedAt:   run.FinishedAt,
		})
	}

	return releaseDetailPageData{
		Shell:            shell,
		Release:          release,
		SBOM:             sbomView,
		CVEStatus:        cveStatusViewFor(status),
		ActiveCVEs:       activeCVEs,
		WaivedCVEs:       waivedCVEs,
		ScanRuns:         scanRunViews,
		ReplaceFormError: replaceError,
		RescanFormError:  rescanError,
	}, nil
}

func (s *Server) loadDeviceDetailPageData(ctx context.Context, shell shellPageData, deviceID string, organisationID int64) (deviceDetailPageData, error) {
	detail, err := s.store.DeviceDetail(ctx, deviceID, organisationID)
	if err != nil {
		return deviceDetailPageData{}, err
	}
	connectivity := deviceConnectivity(detail.Device, time.Now())
	cveStatus, err := s.store.DeviceCVEImpactStatus(ctx, organisationID, deviceID)
	if err != nil {
		return deviceDetailPageData{}, err
	}

	expectedProtocol := strings.ToLower(strings.TrimSpace(detail.Device.ExpectedProtocol))
	protocolLabel := strings.ToUpper(expectedProtocol)
	if expectedProtocol == "coap" {
		protocolLabel = "CoAP over DTLS"
	} else if expectedProtocol == "api" {
		protocolLabel = "API"
	}
	data := deviceDetailPageData{
		Shell: shell,
		Device: deviceDetailView{
			ID:                  detail.Device.ID,
			OrganisationID:      detail.Device.OrganisationID,
			DeviceModelID:       detail.Device.DeviceModelID,
			ModelName:           detail.Device.ModelName,
			ExpectedProtocol:    expectedProtocol,
			ProtocolLabel:       protocolLabel,
			SoftwareVersions:    formatSoftwareVersions(detail.Device.SoftwareVersions),
			FirmwareVersion:     firmwareVersion(detail.Device.SoftwareVersions),
			CVEStatus:           cveStatusViewFor(cveStatus),
			IsGateway:           detail.Device.IsGateway,
			DataTopic:           mqttDataTopic(detail.Device.OrganisationID, detail.Device.ID),
			TaskTopic:           mqttTaskTopic(detail.Device.OrganisationID, detail.Device.ID),
			GatewayPublishTopic: "dev/" + strconv.FormatInt(detail.Device.OrganisationID, 10) + "/{deviceID}/data",
			Status:              connectivity.Status,
			StatusClass:         connectivity.StatusClass,
			LastSeen:            connectivity.LastSeen,
			SupportNote:         detail.Device.SupportNote,
			Tags:                detail.Device.Tags,
		},
	}
	data.TagSuggestions, err = s.store.ListTagSuggestions(ctx, organisationID, "")
	if err != nil {
		return deviceDetailPageData{}, err
	}
	if cveStatus.MatchedReleaseID > 0 {
		release, err := s.store.SoftwareRelease(ctx, cveStatus.MatchedReleaseID, organisationID)
		if err != nil {
			return deviceDetailPageData{}, err
		}
		data.Device.MatchedReleaseLabel = strings.TrimSpace(release.DeviceModelName + " " + release.Version)
		data.Device.MatchedReleaseURL = releaseDetailURL(release.ID, organisationID)
	}
	if detail.MQTTCredential != nil {
		data.MQTTCredential = &mqttCredentialView{
			Username: detail.MQTTCredential.Username,
			Enabled:  detail.MQTTCredential.Enabled,
		}
	}
	if detail.CoAPCredential != nil {
		data.CoAPCredential = &coAPCredentialView{
			PSKIdentity: detail.CoAPCredential.PSKIdentity,
			Revision:    detail.CoAPCredential.Revision,
			Enabled:     detail.CoAPCredential.Enabled,
			Association: "Unknown",
			CID:         "Unknown",
		}
		if runtime, ok := s.coAPIntegrationRuntime.(CoAPAssociationRuntime); ok {
			if association, associationErr := runtime.Association(ctx, deviceID); associationErr == nil {
				data.CoAPCredential.Association = "Inactive"
				if association.Connected {
					data.CoAPCredential.Association = "Connected"
				}
				data.CoAPCredential.CID = "No"
				if association.CIDNegotiated {
					data.CoAPCredential.CID = "Yes"
				}
			}
		}
	}

	data.TwinProperties, data.RecentEvents, err = s.loadDeviceTelemetry(ctx, deviceID, organisationID)
	if err != nil {
		return deviceDetailPageData{}, err
	}
	data.ActiveAndRecentTasks, err = s.loadActiveAndRecentDeviceTasks(ctx, deviceID, organisationID)
	if err != nil {
		return deviceDetailPageData{}, err
	}
	return data, nil
}

func (s *Server) loadDeviceTaskLaunchPageData(ctx context.Context, shell shellPageData, deviceID string, organisationID int64, taskType string, taskFormError string) (deviceTaskLaunchPageData, error) {
	taskLabel, taskHelp, ok := deviceTaskLaunchCopy(taskType)
	if !ok {
		return deviceTaskLaunchPageData{}, db.ErrNotFound
	}
	detail, err := s.store.DeviceDetail(ctx, deviceID, organisationID)
	if err != nil {
		return deviceTaskLaunchPageData{}, err
	}
	connectivity := deviceConnectivity(detail.Device, time.Now())
	var releases []releaseOptionView
	if taskType == domain.TaskTypeFOTA {
		releases, err = s.loadReleaseOptions(ctx, organisationID)
		if err != nil {
			return deviceTaskLaunchPageData{}, err
		}
	}

	return deviceTaskLaunchPageData{
		Shell: shell,
		Device: deviceDetailView{
			ID:             detail.Device.ID,
			OrganisationID: detail.Device.OrganisationID,
			ModelName:      detail.Device.ModelName,
			Status:         connectivity.Status,
			StatusClass:    connectivity.StatusClass,
			LastSeen:       connectivity.LastSeen,
		},
		TaskType:      taskType,
		TaskLabel:     taskLabel,
		TaskHelp:      taskHelp,
		Releases:      releases,
		TaskFormError: taskFormError,
		WriteValues:   "[{\"path\":\"config.sample_interval\",\"value\":60}]",
		TTLDays:       "7",
	}, nil
}

func deviceTaskLaunchCopy(taskType string) (label string, help string, ok bool) {
	switch taskType {
	case domain.TaskTypeRead:
		return "Read", "Request current twin values from the device.", true
	case domain.TaskTypeWrite:
		return "Write", "Send typed JSON values to device paths.", true
	case domain.TaskTypeFOTA:
		return "FOTA", "Ask the device to download and install a release.", true
	default:
		return "", "", false
	}
}

func campaignTaskLaunchCopy(taskType string) (label string, help string, ok bool) {
	switch taskType {
	case domain.TaskTypeRead:
		return "Read", "Request current twin values from the selected devices.", true
	case domain.TaskTypeWrite:
		return "Write", "Send typed JSON values to paths on the selected devices.", true
	case domain.TaskTypeFOTA:
		return "FOTA", "Ask the selected devices to download and install a release.", true
	default:
		return "", "", false
	}
}

func (s *Server) loadCampaignSelectionPageData(ctx context.Context, shell shellPageData, organisationID int64, deviceIDs []string, taskType string, formError string) (campaignSelectionPageData, error) {
	shell.SelectedOrganisationID = organisationID
	taskLabel, taskHelp, ok := campaignTaskLaunchCopy(taskType)
	if !ok {
		return campaignSelectionPageData{}, errors.New("choose a supported task type")
	}
	var devices []domain.Device
	var err error
	if len(deviceIDs) > 0 {
		devices, err = s.store.CampaignTargetDevices(ctx, organisationID, deviceIDs)
		if err != nil {
			return campaignSelectionPageData{}, err
		}
	}
	views := make([]campaignDevicePreviewView, 0, len(devices))
	for _, device := range devices {
		views = append(views, campaignDevicePreviewView{ID: device.ID, ModelName: device.ModelName, ModelID: device.DeviceModelID})
	}
	models, err := s.store.ListDeviceModels(ctx, organisationID)
	if err != nil {
		return campaignSelectionPageData{}, err
	}
	modelOptions := make([]deviceModelOptionView, 0, len(models))
	for _, model := range models {
		modelOptions = append(modelOptions, deviceModelOption(model))
	}
	tags, err := s.store.ListTagSuggestions(ctx, organisationID, "")
	if err != nil {
		return campaignSelectionPageData{}, err
	}
	var releases []releaseOptionView
	if taskType == domain.TaskTypeFOTA {
		releases, err = s.loadReleaseOptions(ctx, organisationID)
		if err != nil {
			return campaignSelectionPageData{}, err
		}
	}
	help := ""
	if taskType == domain.TaskTypeFOTA && len(releases) == 0 {
		help = "No releases are available in this organisation."
	}
	targetType := "filters"
	if len(deviceIDs) > 0 {
		targetType = db.CampaignTargetExplicit
	}
	return campaignSelectionPageData{
		Shell:          shell,
		Devices:        views,
		Releases:       releases,
		FormError:      formError,
		TaskType:       taskType,
		TaskLabel:      taskLabel,
		TaskHelp:       taskHelp,
		WriteValues:    "[{\"path\":\"config.sample_interval\",\"value\":60}]",
		TTLDays:        domain.DefaultTaskTTLDays,
		CanUseFOTA:     len(releases) > 0,
		FOTAHelpText:   help,
		DeviceModels:   modelOptions,
		TagSuggestions: tags,
		TargetType:     targetType,
		EstimatedCount: len(devices),
	}, nil
}

func (s *Server) loadDeviceTelemetry(ctx context.Context, deviceID string, organisationID int64) ([]twinPropertyView, []deviceEventView, error) {
	properties, err := s.store.ListDeviceTwinProperties(ctx, deviceID, organisationID)
	if err != nil {
		return nil, nil, err
	}
	twinProperties := make([]twinPropertyView, 0, len(properties))
	for _, property := range properties {
		twinProperties = append(twinProperties, twinPropertyView{
			Path:       property.Path,
			Value:      property.ValueJSON,
			ValueType:  property.ValueType,
			Protocol:   property.Protocol,
			SourcePath: property.SourcePath,
			TSObserved: formatUnixMS(property.TSObservedMS),
			TSReceived: formatUnixMS(property.TSReceivedMS),
		})
	}

	events, err := s.store.ListRecentDeviceEvents(ctx, deviceID, organisationID, 25)
	if err != nil {
		return nil, nil, err
	}
	recentEvents := make([]deviceEventView, 0, len(events))
	for _, event := range events {
		recentEvents = append(recentEvents, deviceEventView{
			ID:             event.ID,
			TSReceived:     formatUnixMS(event.TSReceivedMS),
			Protocol:       event.Protocol,
			Direction:      event.Direction,
			Operation:      event.Operation,
			Topic:          event.Topic,
			CoAPPath:       event.CoAPPath,
			ContentFormat:  event.ContentFormat,
			Source:         event.Source,
			PayloadJSON:    event.PayloadJSON,
			PayloadRawSize: len(event.PayloadRaw),
		})
	}

	return twinProperties, recentEvents, nil
}

func (s *Server) loadActiveAndRecentDeviceTasks(ctx context.Context, deviceID string, organisationID int64) ([]deviceTaskView, error) {
	tasks, err := s.store.ListActiveAndRecentDeviceTasks(ctx, deviceID, organisationID, 3)
	if err != nil {
		return nil, err
	}

	views := make([]deviceTaskView, 0, len(tasks))
	for _, task := range tasks {
		views = append(views, deviceTaskView{
			ID:            task.ID,
			Type:          task.Type,
			Summary:       deviceTaskSummary(task),
			Status:        formatDeviceTaskStatus(task.Status),
			StatusClass:   deviceTaskStatusClass(task.Status),
			StatusMessage: task.StatusMessage,
			CampaignID:    dereferenceInt64(task.CampaignID),
			CampaignURL:   campaignURLForTask(task, organisationID),
			CreatedAt:     task.CreatedAt,
			ExpiresAt:     task.ExpiresAt,
			CompletedAt:   task.CompletedAt,
		})
	}

	return views, nil
}

func dereferenceInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func campaignURLForTask(task domain.DeviceTask, organisationID int64) string {
	if task.CampaignID == nil {
		return ""
	}
	return campaignDetailURL(*task.CampaignID, organisationID)
}

func (s *Server) loadReleaseOptions(ctx context.Context, organisationID int64) ([]releaseOptionView, error) {
	releases, err := s.store.ListSoftwareReleases(ctx, organisationID)
	if err != nil {
		return nil, err
	}

	options := make([]releaseOptionView, 0, len(releases))
	for _, release := range releases {
		options = append(options, releaseOptionView{
			ID:        release.ID,
			ModelName: release.DeviceModelName,
			Version:   release.Version,
			Label:     strings.TrimSpace(release.DeviceModelName + " " + release.Version),
		})
	}
	return options, nil
}

func deviceTaskSummary(task domain.DeviceTask) string {
	switch task.Type {
	case domain.TaskTypeRead:
		var params domain.ReadTaskParameters
		if err := json.Unmarshal([]byte(task.ParametersJSON), &params); err == nil && len(params.Paths) > 0 {
			return summarizeStrings("Read", params.Paths)
		}
	case domain.TaskTypeWrite:
		var params domain.WriteTaskParameters
		if err := json.Unmarshal([]byte(task.ParametersJSON), &params); err == nil && len(params.Values) > 0 {
			values := make([]string, 0, len(params.Values))
			for _, value := range params.Values {
				values = append(values, value.Path+" = "+string(value.Value))
			}
			return summarizeStrings("Write", values)
		}
	case domain.TaskTypeFOTA:
		params, err := domain.ParseFOTATaskParameters(task.ParametersJSON)
		if err == nil {
			return "Release #" + strconv.FormatInt(params.ReleaseID, 10)
		}
	}
	return "Invalid parameters"
}

func summarizeStrings(prefix string, values []string) string {
	const maxShown = 3
	if len(values) <= maxShown {
		return prefix + " " + strings.Join(values, ", ")
	}
	return prefix + " " + strings.Join(values[:maxShown], ", ") + " +" + strconv.Itoa(len(values)-maxShown)
}

func deviceModelOption(model domain.DeviceModel) deviceModelOptionView {
	return deviceModelOptionView{
		ID:                       model.ID,
		Name:                     model.Name,
		ExpectedHeartbeatSeconds: model.ExpectedHeartbeatSeconds,
		ExpectedProtocol:         model.ExpectedProtocol,
		ExpectedReleaseLabel:     expectedReleaseLabel(model),
	}
}

func deviceModelViewFor(model domain.DeviceModel) deviceModelView {
	return deviceModelView{
		OrganisationID:           model.OrganisationID,
		ID:                       model.ID,
		Name:                     model.Name,
		ExpectedHeartbeatSeconds: model.ExpectedHeartbeatSeconds,
		ExpectedProtocol:         model.ExpectedProtocol,
		ExpectedReleaseID:        model.ExpectedReleaseID,
		ExpectedReleaseLabel:     expectedReleaseLabel(model),
		CreatedAt:                model.CreatedAt,
	}
}

func expectedReleaseLabel(model domain.DeviceModel) string {
	if model.ExpectedReleaseID == nil {
		return ""
	}
	if model.ExpectedReleaseModelName == "" && model.ExpectedReleaseVersion == "" {
		return strconv.FormatInt(*model.ExpectedReleaseID, 10)
	}
	return strings.TrimSpace(model.ExpectedReleaseModelName + " " + model.ExpectedReleaseVersion)
}

func cveStatusViewFor(status domain.CVEImpactStatus) cveStatusView {
	view := cveStatusView{
		Status:       string(status.Status),
		Label:        cveStatusLabel(status.Status),
		StatusClass:  cveStatusClass(status.Status),
		ActiveCount:  status.ActiveCVECount,
		HighestLabel: cveSeverityDisplay(status.HighestActiveSeverity),
	}
	if status.HasLatestScanWarning {
		view.Warning = status.LatestScanWarning
	}
	return view
}

func cveStatusLabel(status domain.CVEImpactStatusValue) string {
	switch status {
	case domain.CVEStatusNoSBOM:
		return "No SBOM"
	case domain.CVEStatusScanPending:
		return "Scan pending"
	case domain.CVEStatusNotScanned:
		return "Not scanned"
	case domain.CVEStatusImpacted:
		return "Impacted"
	case domain.CVEStatusNotImpacted:
		return "Not impacted"
	case domain.CVEStatusScanFailed:
		return "Scan failed"
	case domain.CVEStatusUnknownRelease:
		return "Unknown release"
	default:
		return string(status)
	}
}

func cveStatusClass(status domain.CVEImpactStatusValue) string {
	switch status {
	case domain.CVEStatusImpacted, domain.CVEStatusScanFailed:
		return "status-danger"
	case domain.CVEStatusScanPending:
		return "status-warning"
	case domain.CVEStatusNotImpacted:
		return "status-success"
	default:
		return "status-neutral"
	}
}

func cveScanRunStatusLabel(status string) string {
	switch status {
	case "pending":
		return "Pending"
	case "running":
		return "Running"
	case "success":
		return "Success"
	case "failed":
		return "Failed"
	default:
		return status
	}
}

func cveScanRunStatusClass(status string) string {
	switch status {
	case "pending", "running":
		return "status-warning"
	case "success":
		return "status-success"
	case "failed":
		return "status-danger"
	default:
		return "status-neutral"
	}
}

func cveScanRunDisplayTime(run domain.CVEScanRun) string {
	if run.FinishedAt != "" {
		return run.FinishedAt
	}
	if run.StartedAt != "" {
		return run.StartedAt
	}
	return run.CreatedAt
}

func groupedCVEFindings(findings []domain.CVEScanFinding, waivers []domain.ReleaseCVEWaiver) ([]cveGroupView, []cveGroupView) {
	waiverNotes := make(map[string]string, len(waivers))
	for _, waiver := range waivers {
		waiverNotes[waiver.CVEID] = waiver.Note
	}

	activeGroups := make(map[string]*cveGroupView)
	waivedGroups := make(map[string]*cveGroupView)
	for _, finding := range findings {
		cveID := strings.TrimSpace(finding.CVEID)
		if cveID == "" {
			continue
		}
		groups := activeGroups
		if _, waived := waiverNotes[cveID]; waived {
			groups = waivedGroups
		}
		group := groups[cveID]
		if group == nil {
			group = &cveGroupView{
				CVEID:      cveID,
				NVDURL:     cveDetailURL(cveID),
				WaiverNote: waiverNotes[cveID],
			}
			groups[cveID] = group
		}
		group.Severity = higherCVESeverity(group.Severity, finding.Severity)
		group.SeverityClass = cveSeverityClass(group.Severity)
		group.Evidence = append(group.Evidence, cveEvidenceView{
			PackageName:      finding.PackageName,
			InstalledVersion: finding.InstalledVersion,
		})
	}
	for _, waiver := range waivers {
		if _, ok := waivedGroups[waiver.CVEID]; ok {
			continue
		}
		waivedGroups[waiver.CVEID] = &cveGroupView{
			CVEID:      waiver.CVEID,
			NVDURL:     cveDetailURL(waiver.CVEID),
			WaiverNote: waiver.Note,
		}
	}
	return sortedCVEGroups(activeGroups), sortedCVEGroups(waivedGroups)
}

func sortedCVEGroups(groups map[string]*cveGroupView) []cveGroupView {
	result := make([]cveGroupView, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool {
		iRank := cveSeverityRank(result[i].Severity)
		jRank := cveSeverityRank(result[j].Severity)
		if iRank != jRank {
			return iRank > jRank
		}
		return result[i].CVEID < result[j].CVEID
	})
	return result
}

func higherCVESeverity(current string, candidate string) string {
	candidate = normalizedCVESeverity(candidate)
	if candidate == "" {
		return current
	}
	if current == "" || cveSeverityRank(candidate) > cveSeverityRank(current) {
		return candidate
	}
	return current
}

func normalizedCVESeverity(severity string) string {
	severity = strings.TrimSpace(strings.ToLower(severity))
	switch severity {
	case "critical", "high", "medium", "low", "negligible", "unknown":
		return severity
	case "":
		return ""
	default:
		return "unknown"
	}
}

func cveSeverityRank(severity string) int {
	switch normalizedCVESeverity(severity) {
	case "critical":
		return 6
	case "high":
		return 5
	case "medium":
		return 4
	case "low":
		return 3
	case "negligible":
		return 2
	case "unknown":
		return 1
	default:
		return 0
	}
}

func cveSeverityDisplay(severity string) string {
	severity = normalizedCVESeverity(severity)
	if severity == "" {
		return ""
	}
	return strings.ToUpper(severity[:1]) + severity[1:]
}

func cveSeverityClass(severity string) string {
	switch normalizedCVESeverity(severity) {
	case "critical", "high":
		return "status-danger"
	case "medium":
		return "status-warning"
	case "low", "negligible":
		return "status-info"
	case "unknown":
		return "status-neutral"
	default:
		return "status-neutral"
	}
}

func cveSeverityCounts(findings []domain.CVEScanFinding) cveSeverityCountsView {
	byCVE := make(map[string]string, len(findings))
	for _, finding := range findings {
		cveID := strings.TrimSpace(finding.CVEID)
		if cveID == "" {
			continue
		}
		byCVE[cveID] = higherCVESeverity(byCVE[cveID], finding.Severity)
	}

	counts := cveSeverityCountsView{Total: len(byCVE)}
	for _, severity := range byCVE {
		switch normalizedCVESeverity(severity) {
		case "critical":
			counts.Critical++
		case "high":
			counts.High++
		case "medium":
			counts.Medium++
		case "low":
			counts.Low++
		default:
			counts.Other++
		}
	}
	return counts
}

func cveDetailURL(cveID string) string {
	return "https://nvd.nist.gov/vuln/detail/" + strings.TrimSpace(cveID)
}

func (s *Server) findReleaseOption(ctx context.Context, organisationID int64, releaseID int64) (releaseOptionView, bool, error) {
	releases, err := s.loadReleaseOptions(ctx, organisationID)
	if err != nil {
		return releaseOptionView{}, false, err
	}
	for _, release := range releases {
		if release.ID == releaseID {
			return release, true, nil
		}
	}
	return releaseOptionView{}, false, nil
}

func releaseBinaryURLPath(releaseID int64, organisationID int64) string {
	return "/org/" + strconv.FormatInt(organisationID, 10) + "/releases/" + strconv.FormatInt(releaseID, 10) + "/binary"
}

func releaseDetailURL(releaseID int64, organisationID int64) string {
	return "/releases/" + strconv.FormatInt(releaseID, 10) + "?organisation_id=" + strconv.FormatInt(organisationID, 10)
}

func parsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func nonBlankStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func parseTagInput(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	return nonBlankStrings(strings.Split(value, ","))
}

// Preserve active filters even when they are outside the suggestion limit or
// no longer assigned to a device, so changing another filter cannot drop them.
func deviceTagFilterOptions(suggestions, active []string) []tagOptionView {
	selected := make(map[string]bool, len(active))
	for _, tag := range active {
		selected[tag] = true
	}
	options := make([]tagOptionView, 0, len(suggestions)+len(active))
	seen := make(map[string]bool, len(suggestions)+len(active))
	for _, tags := range [][]string{active, suggestions} {
		for _, tag := range tags {
			if !seen[tag] {
				options = append(options, tagOptionView{Name: tag, Selected: selected[tag]})
				seen[tag] = true
			}
		}
	}
	return options
}

func normalizeFilterTags(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		tag, err := db.NormalizeTag(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	sort.Strings(result)
	return result, nil
}

func devicePaginationView(path string, organisationID int64, query string, tags []string, modelID int64, pagination db.Pagination) paginationView {
	view := paginationView{
		Page:       pagination.Page,
		PageSize:   pagination.PageSize,
		TotalRows:  pagination.TotalRows,
		TotalPages: pagination.TotalPages,
		HasPrev:    pagination.Page > 1,
		HasNext:    pagination.Page < pagination.TotalPages,
		PageSizes:  []int{25, 50, 100},
		FormAction: path,
		Query:      query,
		Tags:       tags,
		ModelID:    modelID,
	}
	if pagination.TotalRows > 0 {
		view.RangeStart = pagination.Offset + 1
		view.RangeEnd = pagination.Offset + pagination.PageSize
		if view.RangeEnd > pagination.TotalRows {
			view.RangeEnd = pagination.TotalRows
		}
	}
	if view.HasPrev {
		view.PrevURL = devicePageURL(path, organisationID, query, tags, modelID, pagination.Page-1, pagination.PageSize)
	}
	if view.HasNext {
		view.NextURL = devicePageURL(path, organisationID, query, tags, modelID, pagination.Page+1, pagination.PageSize)
	}
	return view
}

func devicePageURL(path string, organisationID int64, query string, tags []string, modelID int64, page int, pageSize int) string {
	values := url.Values{}
	values.Set("organisation_id", strconv.FormatInt(organisationID, 10))
	query = strings.TrimSpace(query)
	if query != "" {
		values.Set("q", query)
	}
	for _, tag := range tags {
		values.Add("tag", tag)
	}
	if modelID > 0 {
		values.Set("model_id", strconv.FormatInt(modelID, 10))
	}
	values.Set("page", strconv.Itoa(page))
	values.Set("page_size", strconv.Itoa(pageSize))
	return path + "?" + values.Encode()
}

func campaignPaginationView(path string, organisationID int64, campaignID int64, status string, pagination db.Pagination) paginationView {
	view := paginationView{
		Page:       pagination.Page,
		PageSize:   pagination.PageSize,
		TotalRows:  pagination.TotalRows,
		TotalPages: pagination.TotalPages,
		HasPrev:    pagination.Page > 1,
		HasNext:    pagination.Page < pagination.TotalPages,
		PageSizes:  []int{25, 50, 100},
		FormAction: path,
		Status:     status,
	}
	if pagination.TotalRows > 0 {
		view.RangeStart = pagination.Offset + 1
		view.RangeEnd = pagination.Offset + pagination.PageSize
		if view.RangeEnd > pagination.TotalRows {
			view.RangeEnd = pagination.TotalRows
		}
	}
	if view.HasPrev {
		view.PrevURL = campaignPageURL(path, organisationID, status, pagination.Page-1, pagination.PageSize)
	}
	if view.HasNext {
		view.NextURL = campaignPageURL(path, organisationID, status, pagination.Page+1, pagination.PageSize)
	}
	_ = campaignID
	return view
}

func campaignPageURL(path string, organisationID int64, status string, page int, pageSize int) string {
	values := url.Values{}
	values.Set("organisation_id", strconv.FormatInt(organisationID, 10))
	if strings.TrimSpace(status) != "" {
		values.Set("status", strings.TrimSpace(status))
	}
	values.Set("page", strconv.Itoa(page))
	values.Set("page_size", strconv.Itoa(pageSize))
	return path + "?" + values.Encode()
}

func campaignDetailURL(campaignID int64, organisationID int64) string {
	return "/campaigns/" + strconv.FormatInt(campaignID, 10) + "?organisation_id=" + strconv.FormatInt(organisationID, 10)
}

func deviceModelDetailURL(modelID int64, organisationID int64) string {
	return "/device-models/" + strconv.FormatInt(modelID, 10) + "?organisation_id=" + strconv.FormatInt(organisationID, 10)
}

func (s *Server) fotaDownloadURL(releaseID int64, organisationID int64) string {
	path := releaseBinaryURLPath(releaseID, organisationID)
	baseURL := strings.TrimRight(strings.TrimSpace(s.fotaDownloadBaseURL), "/")
	if baseURL == "" {
		return path
	}
	return baseURL + path
}

func formatDeviceTaskStatus(status string) string {
	switch status {
	case db.DeviceTaskStatusPending:
		return "Pending"
	case db.DeviceTaskStatusInProgress:
		return "In progress"
	case db.DeviceTaskStatusSuccess:
		return "Success"
	case db.DeviceTaskStatusFailure:
		return "Failure"
	case db.DeviceTaskStatusQueued:
		return "Queued"
	case db.DeviceTaskStatusExpired:
		return "Expired"
	case db.DeviceTaskStatusCanceled:
		return "Canceled"
	default:
		return status
	}
}

func deviceTaskStatusClass(status string) string {
	switch status {
	case db.DeviceTaskStatusQueued:
		return "status-neutral"
	case db.DeviceTaskStatusPending:
		return "status-warning"
	case db.DeviceTaskStatusInProgress:
		return "status-info"
	case db.DeviceTaskStatusSuccess:
		return "status-success"
	case db.DeviceTaskStatusFailure, db.DeviceTaskStatusExpired, db.DeviceTaskStatusCanceled:
		return "status-danger"
	default:
		return "status-neutral"
	}
}

func (s *Server) campaignView(campaign domain.Campaign) campaignView {
	return campaignView{
		ID:             campaign.ID,
		OrganisationID: campaign.OrganisationID,
		Name:           campaign.Name,
		Type:           campaign.TaskType,
		Summary:        deviceTaskSummary(domain.DeviceTask{Type: campaign.TaskType, ParametersJSON: campaign.ParametersJSON}),
		Status:         formatCampaignStatus(campaign.Status),
		StatusClass:    campaignStatusClass(campaign.Status),
		CreatedAt:      campaign.CreatedAt,
		FinishedAt:     campaign.FinishedAt,
		CanceledAt:     campaign.CanceledAt,
		TTLDays:        campaign.TaskTTLSeconds / domain.SecondsPerDay,
		TargetCount:    campaign.TargetCount,
		Target:         campaignTargetLabel(campaign),
		Queued:         campaign.Counts.Queued,
		Pending:        campaign.Counts.Pending,
		InProgress:     campaign.Counts.InProgress,
		Success:        campaign.Counts.Success,
		Failure:        campaign.Counts.Failure,
		Expired:        campaign.Counts.Expired,
		Canceled:       campaign.Counts.Canceled,
		DetailURL:      campaignDetailURL(campaign.ID, campaign.OrganisationID),
		CancelAction:   "/campaigns/" + strconv.FormatInt(campaign.ID, 10) + "/cancel",
	}
}

func campaignTargetLabel(campaign domain.Campaign) string {
	switch campaign.TargetType {
	case "tag":
		return "Tag: " + campaign.TargetTag
	case "model":
		return "Model: " + campaign.TargetModelName
	case "tag_model":
		return "Tag: " + campaign.TargetTag + " · Model: " + campaign.TargetModelName
	default:
		return "Explicit device selection (" + strconv.Itoa(len(campaign.TargetDeviceIDs)) + ")"
	}
}

func (s *Server) campaignTaskView(row domain.CampaignTaskRow, organisationID int64, campaignID int64, rawQuery string) campaignTaskView {
	return campaignTaskView{
		ID:            row.Task.ID,
		DeviceID:      row.Task.DeviceID,
		DeviceURL:     "/devices/" + row.Task.DeviceID + "?organisation_id=" + strconv.FormatInt(organisationID, 10),
		ModelName:     row.DeviceModelName,
		Type:          row.Task.Type,
		Summary:       deviceTaskSummary(row.Task),
		Status:        formatDeviceTaskStatus(row.Task.Status),
		StatusClass:   deviceTaskStatusClass(row.Task.Status),
		StatusValue:   row.Task.Status,
		StatusMessage: row.Task.StatusMessage,
		CreatedAt:     row.Task.CreatedAt,
		ExpiresAt:     row.Task.ExpiresAt,
		CompletedAt:   row.Task.CompletedAt,
		CancelAction:  "/campaigns/" + strconv.FormatInt(campaignID, 10) + "/tasks/" + strconv.FormatInt(row.Task.ID, 10) + "/cancel",
	}
}

func formatCampaignStatus(status string) string {
	switch status {
	case db.CampaignStatusRunning:
		return "Running"
	case db.CampaignStatusFinished:
		return "Finished"
	case db.CampaignStatusCanceled:
		return "Canceled"
	default:
		return status
	}
}

func campaignStatusClass(status string) string {
	switch status {
	case db.CampaignStatusRunning:
		return "status-info"
	case db.CampaignStatusFinished:
		return "status-success"
	case db.CampaignStatusCanceled:
		return "status-danger"
	default:
		return "status-neutral"
	}
}

type deviceConnectivityView struct {
	Connected   bool
	Status      string
	StatusClass string
	LastSeen    string
}

func deviceConnectivity(device domain.Device, now time.Time) deviceConnectivityView {
	lastSeenMS := device.LastSeenMS
	if lastSeenMS == 0 {
		lastSeenMS = device.LastEventReceivedMS
	}
	if lastSeenMS == 0 {
		return deviceConnectivityView{
			Status:      "Disconnected",
			StatusClass: "status-danger",
			LastSeen:    "Never",
		}
	}

	lastSeenAt := time.UnixMilli(lastSeenMS)
	connected := now.Sub(lastSeenAt) <= time.Duration(device.ExpectedHeartbeatSeconds)*time.Second
	if connected {
		return deviceConnectivityView{
			Connected:   true,
			Status:      "Connected",
			StatusClass: "status-success",
			LastSeen:    formatUnixMS(lastSeenMS),
		}
	}
	return deviceConnectivityView{
		Status:      "Disconnected",
		StatusClass: "status-danger",
		LastSeen:    formatUnixMS(lastSeenMS),
	}
}

func apiCredentialViewFor(credential domain.OrganisationAPICredential) apiCredentialView {
	view := apiCredentialView{
		ID:          credential.ID,
		Name:        credential.Name,
		Enabled:     credential.Enabled,
		Status:      "Disabled",
		StatusClass: "status-neutral",
		LastUsedAt:  "Never",
		CreatedAt:   credential.CreatedAt,
	}
	if credential.Enabled {
		view.Status = "Active"
		view.StatusClass = "status-success"
	}
	if strings.TrimSpace(credential.LastUsedAt) != "" {
		view.LastUsedAt = credential.LastUsedAt
	}
	return view
}

func formatUnixMS(timestampMS int64) string {
	if timestampMS == 0 {
		return ""
	}
	return time.UnixMilli(timestampMS).UTC().Format(time.RFC3339)
}

func localTimeElement(value string) template.HTML {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	utcValue, ok := normalizeUTCTime(value)
	if !ok {
		return template.HTML(html.EscapeString(value))
	}
	escapedUTC := html.EscapeString(utcValue)
	return template.HTML(`<time class="local-time" datetime="` + escapedUTC + `" data-local-time title="` + escapedUTC + `">` + escapedUTC + `</time>`)
}

func normalizeUTCTime(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}

	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z07",
		"2006-01-02 15:04:05.999999Z07:00",
		"2006-01-02 15:04:05.999999Z07",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05Z07",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC().Format(time.RFC3339), true
		}
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.UTC); err == nil {
		return parsed.UTC().Format(time.RFC3339), true
	}
	return "", false
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		user, err := s.store.UserBySession(r.Context(), cookie.Value, time.Now())
		if errors.Is(err, db.ErrNotFound) {
			http.SetCookie(w, expiredSessionCookie())
			redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if err != nil {
			http.Error(w, "session error", http.StatusInternalServerError)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) currentUser(r *http.Request) (domain.User, bool) {
	user, ok := r.Context().Value(userContextKey).(domain.User)
	return user, ok
}

func (s *Server) userFromRequest(r *http.Request) (domain.User, bool) {
	if user, ok := s.currentUser(r); ok {
		return user, true
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return domain.User{}, false
	}

	user, err := s.store.UserBySession(r.Context(), cookie.Value, time.Now())
	return user, err == nil
}

func sessionCookie(token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func expiredSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func formatSoftwareVersions(versions domain.SoftwareVersions) string {
	if len(versions) == 0 {
		return "-"
	}

	keys := []string{"firmware", "modem", "app"}
	result := ""
	for _, key := range keys {
		value, ok := versions[key]
		if !ok || value == "" {
			continue
		}
		if result != "" {
			result += " / "
		}
		result += key + "=" + value
	}
	if result != "" {
		return result
	}

	remainingKeys := make([]string, 0, len(versions))
	for key := range versions {
		remainingKeys = append(remainingKeys, key)
	}
	sort.Strings(remainingKeys)

	for _, key := range remainingKeys {
		value := versions[key]
		if result != "" {
			result += " / "
		}
		result += key + "=" + value
	}
	return result
}

func firmwareVersion(versions domain.SoftwareVersions) string {
	return strings.TrimSpace(versions["firmware"])
}

package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"anchor/internal/cve"
	"anchor/internal/db"
	"anchor/internal/domain"

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
)

type contextKey string

const userContextKey contextKey = "user"

type Server struct {
	store                  *db.Store
	templates              *template.Template
	internalMQTTClientAuth InternalMQTTClientAuthConfig
	taskPublisher          DeviceTaskPublisher
	cveScanWorker          CVEScanWorker
	releaseStorageDir      string
	fotaDownloadBaseURL    string
}

type ServerConfig struct {
	InternalMQTTClientAuth InternalMQTTClientAuthConfig
	TaskPublisher          DeviceTaskPublisher
	ReleaseStorageDir      string
	FOTADownloadBaseURL    string
	CVEScanWorkerEnabled   bool
	CVEScannerPath         string
	CVEScanWorker          CVEScanWorker
}

// InternalMQTTClientAuthConfig authorizes Anchor's own MQTT broker client in MQTT auth callbacks.
type InternalMQTTClientAuthConfig struct {
	Username string
	Password string
}

type DeviceTaskPublisher interface {
	PublishDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) error
	PublishPendingDeviceTasks(ctx context.Context, deviceID string, organisationID int64) error
}

type CVEScanWorker interface {
	Notify()
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

type devicesPageData struct {
	Shell            shellPageData
	Devices          []deviceView
	DeviceModelCount int
	OnlineCount      int
}

type deviceCreatePageData struct {
	Shell          shellPageData
	DeviceModels   []deviceModelOptionView
	MQTTFormError  string
	DeviceFormNote string
}

type deviceDetailPageData struct {
	Shell                shellPageData
	Device               deviceDetailView
	MQTTCredential       *mqttCredentialView
	TwinProperties       []twinPropertyView
	RecentEvents         []deviceEventView
	ActiveAndRecentTasks []deviceTaskView
	Releases             []releaseOptionView
	TaskFormError        string
}

type releasesPageData struct {
	Shell            shellPageData
	Releases         []releaseView
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
	Shell          shellPageData
	Models         []deviceModelView
	Releases       []releaseOptionView
	ModelFormError string
}

type otaDeploymentsPageData struct {
	Shell       shellPageData
	Deployments []domain.OTADeployment
}

type organisationPageData struct {
	Shell               shellPageData
	Organisation        domain.Organisation
	Admins              []domain.OrganisationMember
	Members             []domain.OrganisationMember
	IsOrganisationAdmin bool
	RenameFormError     string
	InviteFormError     string
	RemoveFormError     string
	InviteURL           string
	InviteMessage       string
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
}

type deviceDetailView struct {
	ID                  string
	OrganisationID      int64
	DeviceModelID       int64
	ModelName           string
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
}

type mqttCredentialView struct {
	Username string
	Enabled  bool
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
	ID          int64
	Type        string
	Parameter   string
	Status      string
	StatusClass string
	CreatedAt   string
	CompletedAt string
}

type releaseView struct {
	domain.SoftwareRelease
	SBOMFileCount int
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
}

type deviceModelOptionView struct {
	ID                       int64
	Name                     string
	ExpectedHeartbeatSeconds int64
	ExpectedProtocol         string
	ExpectedReleaseLabel     string
}

type deviceModelView struct {
	ID                       int64
	Name                     string
	ExpectedHeartbeatSeconds int64
	ExpectedProtocol         string
	ExpectedReleaseLabel     string
	CreatedAt                string
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

func NewServer(store *db.Store, configs ...ServerConfig) http.Handler {
	var config ServerConfig
	if len(configs) > 0 {
		config = configs[0]
	}

	server := &Server{
		store:                  store,
		templates:              template.Must(template.New("").Funcs(template.FuncMap{"dict": templateDict}).ParseGlob("templates/*.html")),
		internalMQTTClientAuth: config.InternalMQTTClientAuth,
		taskPublisher:          config.TaskPublisher,
		cveScanWorker:          config.CVEScanWorker,
		releaseStorageDir:      config.ReleaseStorageDir,
		fotaDownloadBaseURL:    strings.TrimRight(strings.TrimSpace(config.FOTADownloadBaseURL), "/"),
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /logo.png", server.logo)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("GET /", server.home)
	mux.HandleFunc("GET /login", server.login)
	mux.HandleFunc("POST /login", server.loginPost)
	mux.HandleFunc("POST /logout", server.logout)
	mux.Handle("GET /devices", server.requireAuth(http.HandlerFunc(server.devices)))
	mux.Handle("GET /devices/new", server.requireAuth(http.HandlerFunc(server.deviceNew)))
	mux.Handle("GET /devices/{deviceID}", server.requireAuth(http.HandlerFunc(server.deviceDetail)))
	mux.Handle("GET /devices/{deviceID}/events", server.requireAuth(http.HandlerFunc(server.deviceEvents)))
	mux.Handle("GET /devices/{deviceID}/telemetry", server.requireAuth(http.HandlerFunc(server.deviceTelemetry)))
	mux.Handle("GET /devices/{deviceID}/tasks", server.requireAuth(http.HandlerFunc(server.deviceTasks)))
	mux.Handle("POST /devices", server.requireAuth(http.HandlerFunc(server.devicesPost)))
	mux.Handle("POST /devices/{deviceID}/tasks", server.requireAuth(http.HandlerFunc(server.deviceTaskPost)))
	mux.Handle("POST /devices/{deviceID}/tasks/{taskID}/cancel", server.requireAuth(http.HandlerFunc(server.deviceTaskCancelPost)))
	mux.Handle("POST /devices/delete", server.requireAuth(http.HandlerFunc(server.deviceDeletePost)))
	mux.Handle("GET /device-models", server.requireAuth(http.HandlerFunc(server.deviceModels)))
	mux.Handle("POST /device-models", server.requireAuth(http.HandlerFunc(server.deviceModelsPost)))
	mux.Handle("GET /releases", server.requireAuth(http.HandlerFunc(server.releases)))
	mux.Handle("POST /releases", server.requireAuth(http.HandlerFunc(server.releasesPost)))
	mux.Handle("GET /releases/{releaseID}", server.requireAuth(http.HandlerFunc(server.releaseDetail)))
	mux.Handle("GET /releases/{releaseID}/cves", server.requireAuth(http.HandlerFunc(server.releaseCVEState)))
	mux.Handle("GET /releases/{releaseID}/events", server.requireAuth(http.HandlerFunc(server.releaseEvents)))
	mux.Handle("POST /releases/{releaseID}/sbom", server.requireAuth(http.HandlerFunc(server.releaseSBOMReplacePost)))
	mux.Handle("POST /releases/{releaseID}/rescan", server.requireAuth(http.HandlerFunc(server.releaseRescanPost)))
	mux.Handle("POST /releases/{releaseID}/cves/{cveID}/waiver", server.requireAuth(http.HandlerFunc(server.releaseCVEMarkNotRelevantPost)))
	mux.Handle("POST /releases/{releaseID}/cves/{cveID}/waiver/delete", server.requireAuth(http.HandlerFunc(server.releaseCVEUnmarkNotRelevantPost)))
	mux.HandleFunc("GET /org/{organisationID}/releases/{releaseID}/binary", server.releaseBinary)
	mux.Handle("GET /ota-updates", server.requireAuth(http.HandlerFunc(server.otaUpdates)))
	mux.Handle("GET /organisations", server.requireAuth(http.HandlerFunc(server.organisations)))
	mux.Handle("POST /organisations/rename", server.requireAuth(http.HandlerFunc(server.organisationRenamePost)))
	mux.Handle("POST /organisations/invitations", server.requireAuth(http.HandlerFunc(server.organisationInvitationsPost)))
	mux.Handle("POST /organisations/members/remove", server.requireAuth(http.HandlerFunc(server.organisationMemberRemovePost)))
	mux.HandleFunc("GET /invitations/{token}", server.invitationSignup)
	mux.HandleFunc("POST /invitations/{token}", server.invitationSignupPost)
	mux.HandleFunc("POST /mqtt/auth", server.mqttAuth)
	mux.HandleFunc("POST /mqtt/superuser", server.mqttSuperuser)
	mux.HandleFunc("POST /mqtt/acl", server.mqttACL)

	return mux
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
	http.ServeFile(w, r, "logo.png")
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
	s.render(w, "login.html", loginPageData{})
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
		s.render(w, "login.html", loginPageData{
			Error: "Invalid email or password.",
			Email: email,
		})
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

	devices, err := s.store.ListDevicesWithMQTT(r.Context(), shell.SelectedOrganisationID)
	if err != nil {
		http.Error(w, "device query error", http.StatusInternalServerError)
		return
	}
	deviceModels, err := s.store.ListDeviceModels(r.Context(), shell.SelectedOrganisationID)
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}

	views := make([]deviceView, 0, len(devices))
	now := time.Now()
	onlineCount := 0
	for _, device := range devices {
		communication := []string{}
		if device.MQTTCredential != nil {
			communication = append(communication, "MQTT")
		}
		if device.Device.IsGateway {
			communication = append(communication, "Gateway")
		}
		connectivity := deviceConnectivity(device.Device, now)
		if connectivity.Connected {
			onlineCount++
		}
		cveStatus, err := s.store.DeviceCVEImpactStatus(r.Context(), shell.SelectedOrganisationID, device.Device.ID)
		if err != nil {
			http.Error(w, "device cve status query error", http.StatusInternalServerError)
			return
		}
		views = append(views, deviceView{
			ID:               device.Device.ID,
			OrganisationID:   device.Device.OrganisationID,
			ModelName:        device.Device.ModelName,
			SoftwareVersions: formatSoftwareVersions(device.Device.SoftwareVersions),
			FirmwareVersion:  firmwareVersion(device.Device.SoftwareVersions),
			CVEStatus:        cveStatusViewFor(cveStatus),
			IsGateway:        device.Device.IsGateway,
			Communication:    communication,
			Status:           connectivity.Status,
			StatusClass:      connectivity.StatusClass,
			LastSeen:         connectivity.LastSeen,
		})
	}

	s.renderDevices(w, r, devicesPageData{
		Shell:            shell,
		Devices:          views,
		DeviceModelCount: len(deviceModels),
		OnlineCount:      onlineCount,
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

	data, err := s.loadDeviceDetailPageData(r.Context(), shell, deviceID, organisationID, "")
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
	releases, err := s.loadReleaseOptions(r.Context(), organisationID)
	if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}

	s.renderDeviceTasks(w, deviceDetailPageData{
		Device: deviceDetailView{
			ID:             deviceID,
			OrganisationID: organisationID,
		},
		ActiveAndRecentTasks: tasks,
		Releases:             releases,
	})
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

	deviceID := r.FormValue("device_id")
	username := r.FormValue("mqtt_username")
	password := r.FormValue("mqtt_password")
	if deviceID == "" || username == "" || password == "" {
		s.renderDeviceNewWithError(w, r, "Device ID, model, MQTT username, and password are required.")
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
	if _, err := s.store.DeviceModel(r.Context(), deviceModelID, organisationID); errors.Is(err, db.ErrNotFound) {
		s.renderDeviceNewForOrganisationWithError(w, r, shell, organisationID, "Choose a device model from this organisation.")
		return
	} else if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "credential error", http.StatusInternalServerError)
		return
	}

	if err := s.store.SaveDeviceWithMQTTCredential(r.Context(), domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               deviceID,
			OrganisationID:   organisationID,
			DeviceModelID:    deviceModelID,
			SoftwareVersions: domain.SoftwareVersions{},
			IsGateway:        r.FormValue("is_gateway") == "on",
		},
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

	http.Redirect(w, r, "/devices?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
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
	if taskType != "fota" {
		s.renderDeviceDetailWithTaskError(w, r, shell, deviceID, organisationID, "Only FOTA task launch is available for now.")
		return
	}

	releaseID, err := strconv.ParseInt(r.FormValue("release_id"), 10, 64)
	if err != nil || releaseID <= 0 {
		s.renderDeviceDetailWithTaskError(w, r, shell, deviceID, organisationID, "Choose a release for the FOTA task.")
		return
	}
	release, ok, err := s.findReleaseOption(r.Context(), organisationID, releaseID)
	if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}
	if !ok {
		s.renderDeviceDetailWithTaskError(w, r, shell, deviceID, organisationID, "Choose a release from this organisation.")
		return
	}

	task := domain.DeviceTask{
		DeviceID:  deviceID,
		Type:      "fota",
		Parameter: s.fotaDownloadURL(release.ID, organisationID),
		Status:    db.DeviceTaskStatusPending,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	taskID, err := s.store.CreateDeviceTask(r.Context(), task, organisationID)
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "device task create error", http.StatusInternalServerError)
		return
	}
	task.ID = taskID
	s.publishDeviceTask(r.Context(), task, organisationID)

	http.Redirect(w, r, "/devices/"+deviceID+"?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
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

	err = s.store.UpdateDeviceTaskStatus(r.Context(), taskID, deviceID, organisationID, db.DeviceTaskStatusCanceled, time.Now().UTC().Format(time.RFC3339))
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "device task cancel error", http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		s.renderDeviceTasksForDevice(w, r, deviceID, organisationID)
		return
	}

	http.Redirect(w, r, "/devices/"+deviceID+"?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
}

func (s *Server) renderDeviceDetailWithTaskError(w http.ResponseWriter, r *http.Request, shell shellPageData, deviceID string, organisationID int64, message string) {
	data, err := s.loadDeviceDetailPageData(r.Context(), shell, deviceID, organisationID, message)
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

	data, err := s.loadDeviceModelsPageData(r.Context(), shell, organisationID, "")
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	s.renderDeviceModels(w, data)
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
		s.renderDeviceModelsForOrganisationWithError(w, r, shell, organisationID, "Name, heartbeat, and protocol are required.")
		return
	}

	var expectedReleaseID *int64
	releaseValue := strings.TrimSpace(r.FormValue("expected_release_id"))
	if releaseValue != "" {
		releaseID, err := strconv.ParseInt(releaseValue, 10, 64)
		if err != nil || releaseID <= 0 {
			s.renderDeviceModelsForOrganisationWithError(w, r, shell, organisationID, "Choose a valid expected release.")
			return
		}
		if _, err := s.store.SoftwareRelease(r.Context(), releaseID, organisationID); errors.Is(err, db.ErrNotFound) {
			s.renderDeviceModelsForOrganisationWithError(w, r, shell, organisationID, "Choose a release from this organisation.")
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
		s.renderDeviceModelsForOrganisationWithError(w, r, shell, organisationID, "A device model with this name already exists.")
		return
	}
	if err != nil {
		http.Error(w, "device model create error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/device-models?organisation_id="+strconv.FormatInt(organisationID, 10), http.StatusSeeOther)
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
	models, err := s.deviceModelOptions(r.Context(), organisationID)
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}

	note := ""
	if len(models) == 0 {
		note = "Create a device model before creating releases."
	}
	s.renderReleases(w, releasesPageData{
		Shell:           shell,
		Releases:        releases,
		DeviceModels:    models,
		ReleaseFormNote: note,
	})
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
		s.renderReleasesForOrganisationWithError(w, r, shell, organisationID, "Choose a device model.")
		return
	}
	if _, err := s.store.DeviceModel(r.Context(), deviceModelID, organisationID); errors.Is(err, db.ErrNotFound) {
		s.renderReleasesForOrganisationWithError(w, r, shell, organisationID, "Choose a device model from this organisation.")
		return
	} else if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}

	artifact, cleanup, err := s.saveReleaseArtifact(r, organisationID)
	if err != nil {
		s.renderReleasesForOrganisationWithError(w, r, shell, organisationID, err.Error())
		return
	}
	sbom, sbomCleanup, err := s.saveReleaseSBOMFiles(r, artifact.Path)
	if err != nil {
		cleanup()
		s.renderReleasesForOrganisationWithError(w, r, shell, organisationID, err.Error())
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
			s.renderReleasesForOrganisationWithError(w, r, shell, organisationID, "A release already exists for this device model and version.")
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
		sbom, err := s.store.CurrentReleaseSBOM(ctx, organisationID, release.ID)
		if errors.Is(err, db.ErrNotFound) {
			err = nil
		}
		if err != nil {
			return nil, err
		}
		views = append(views, releaseView{
			SoftwareRelease: release,
			SBOMFileCount:   sbom.FileCount,
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

func (s *Server) otaUpdates(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	organisationID, ok := requestedOrganisationID(r.URL.Query().Get("organisation_id"), shell.Organisations)
	if !ok {
		http.NotFound(w, r)
		return
	}

	deployments, err := s.store.ListOngoingOTADeployments(r.Context(), organisationID)
	if err != nil {
		http.Error(w, "ota deployment query error", http.StatusInternalServerError)
		return
	}

	s.renderOTAUpdates(w, otaDeploymentsPageData{
		Shell:       shell,
		Deployments: deployments,
	})
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
	data, ok := s.loadOrganisationPageDataForShell(w, r, shell, organisationID, renameError, inviteError, removeError, inviteURL, inviteMessage)
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
	return s.loadOrganisationPageDataForShell(w, r, shell, organisationID, renameError, inviteError, removeError, inviteURL, inviteMessage)
}

func (s *Server) loadOrganisationPageDataForShell(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, renameError string, inviteError string, removeError string, inviteURL string, inviteMessage string) (organisationPageData, bool) {
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

	shell.SelectedOrganisationID = organisationID
	return organisationPageData{
		Shell:               shell,
		Organisation:        organisation,
		Admins:              admins,
		Members:             members,
		IsOrganisationAdmin: isOrganisationAdmin,
		RenameFormError:     renameError,
		InviteFormError:     inviteError,
		RemoveFormError:     removeError,
		InviteURL:           inviteURL,
		InviteMessage:       inviteMessage,
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

	s.renderReleasesForOrganisationWithError(w, r, shell, organisationID, message)
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

func (s *Server) renderReleasesForOrganisationWithError(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, message string) {
	shell.SelectedOrganisationID = organisationID
	releases, err := s.releaseViews(r.Context(), organisationID)
	if err != nil {
		http.Error(w, "release query error", http.StatusInternalServerError)
		return
	}
	models, err := s.deviceModelOptions(r.Context(), organisationID)
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}

	note := ""
	if len(models) == 0 {
		note = "Create a device model before creating releases."
	}
	s.renderReleases(w, releasesPageData{
		Shell:            shell,
		Releases:         releases,
		DeviceModels:     models,
		ReleaseFormError: message,
		ReleaseFormNote:  note,
	})
}

func (s *Server) renderDeviceModelsForOrganisationWithError(w http.ResponseWriter, r *http.Request, shell shellPageData, organisationID int64, message string) {
	data, err := s.loadDeviceModelsPageData(r.Context(), shell, organisationID, message)
	if err != nil {
		http.Error(w, "device model query error", http.StatusInternalServerError)
		return
	}
	s.renderDeviceModels(w, data)
}

func (s *Server) renderDevices(w http.ResponseWriter, r *http.Request, data devicesPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "devices.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceNew(w http.ResponseWriter, data deviceCreatePageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "device_new.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceDetail(w http.ResponseWriter, data deviceDetailPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "device_detail.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceTelemetry(w http.ResponseWriter, data deviceDetailPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "device_telemetry", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceTasks(w http.ResponseWriter, data deviceDetailPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "device_tasks", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderReleases(w http.ResponseWriter, data releasesPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "releases.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderReleaseDetail(w http.ResponseWriter, data releaseDetailPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "release_detail.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderReleaseCVEState(w http.ResponseWriter, data releaseDetailPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "release_cve_state", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderDeviceModels(w http.ResponseWriter, data deviceModelsPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "device_models.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderOTAUpdates(w http.ResponseWriter, data otaDeploymentsPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "ota_updates.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderOrganisation(w http.ResponseWriter, data organisationPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "organisations.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) renderInvitationSignup(w http.ResponseWriter, data invitationSignupPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "invitation_signup.html", data); err != nil {
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

	note := ""
	if len(modelOptions) == 0 {
		note = "Create a device model before registering devices."
	}
	return deviceCreatePageData{
		Shell:          shell,
		DeviceModels:   modelOptions,
		MQTTFormError:  formError,
		DeviceFormNote: note,
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

func (s *Server) loadDeviceModelsPageData(ctx context.Context, shell shellPageData, organisationID int64, formError string) (deviceModelsPageData, error) {
	shell.SelectedOrganisationID = organisationID
	models, err := s.store.ListDeviceModels(ctx, organisationID)
	if err != nil {
		return deviceModelsPageData{}, err
	}
	modelViews := make([]deviceModelView, 0, len(models))
	for _, model := range models {
		modelViews = append(modelViews, deviceModelView{
			ID:                       model.ID,
			Name:                     model.Name,
			ExpectedHeartbeatSeconds: model.ExpectedHeartbeatSeconds,
			ExpectedProtocol:         model.ExpectedProtocol,
			ExpectedReleaseLabel:     expectedReleaseLabel(model),
			CreatedAt:                model.CreatedAt,
		})
	}
	releases, err := s.loadReleaseOptions(ctx, organisationID)
	if err != nil {
		return deviceModelsPageData{}, err
	}
	return deviceModelsPageData{
		Shell:          shell,
		Models:         modelViews,
		Releases:       releases,
		ModelFormError: formError,
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

func (s *Server) loadDeviceDetailPageData(ctx context.Context, shell shellPageData, deviceID string, organisationID int64, taskFormError string) (deviceDetailPageData, error) {
	detail, err := s.store.DeviceDetail(ctx, deviceID, organisationID)
	if err != nil {
		return deviceDetailPageData{}, err
	}
	connectivity := deviceConnectivity(detail.Device, time.Now())
	cveStatus, err := s.store.DeviceCVEImpactStatus(ctx, organisationID, deviceID)
	if err != nil {
		return deviceDetailPageData{}, err
	}

	data := deviceDetailPageData{
		Shell: shell,
		Device: deviceDetailView{
			ID:                  detail.Device.ID,
			OrganisationID:      detail.Device.OrganisationID,
			DeviceModelID:       detail.Device.DeviceModelID,
			ModelName:           detail.Device.ModelName,
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
		},
		TaskFormError: taskFormError,
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

	data.TwinProperties, data.RecentEvents, err = s.loadDeviceTelemetry(ctx, deviceID, organisationID)
	if err != nil {
		return deviceDetailPageData{}, err
	}
	data.ActiveAndRecentTasks, err = s.loadActiveAndRecentDeviceTasks(ctx, deviceID, organisationID)
	if err != nil {
		return deviceDetailPageData{}, err
	}
	data.Releases, err = s.loadReleaseOptions(ctx, organisationID)
	if err != nil {
		return deviceDetailPageData{}, err
	}

	return data, nil
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
			ID:          task.ID,
			Type:        task.Type,
			Parameter:   task.Parameter,
			Status:      formatDeviceTaskStatus(task.Status),
			StatusClass: deviceTaskStatusClass(task.Status),
			CreatedAt:   task.CreatedAt,
			CompletedAt: task.CompletedAt,
		})
	}

	return views, nil
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

func deviceModelOption(model domain.DeviceModel) deviceModelOptionView {
	return deviceModelOptionView{
		ID:                       model.ID,
		Name:                     model.Name,
		ExpectedHeartbeatSeconds: model.ExpectedHeartbeatSeconds,
		ExpectedProtocol:         model.ExpectedProtocol,
		ExpectedReleaseLabel:     expectedReleaseLabel(model),
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
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]cveGroupView, 0, len(keys))
	for _, key := range keys {
		result = append(result, *groups[key])
	}
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
	case db.DeviceTaskStatusCanceled:
		return "Canceled"
	default:
		return status
	}
}

func deviceTaskStatusClass(status string) string {
	switch status {
	case db.DeviceTaskStatusPending:
		return "status-warning"
	case db.DeviceTaskStatusInProgress:
		return "status-info"
	case db.DeviceTaskStatusSuccess:
		return "status-success"
	case db.DeviceTaskStatusFailure, db.DeviceTaskStatusCanceled:
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
	if device.LastEventReceivedMS == 0 {
		return deviceConnectivityView{
			Status:      "Disconnected",
			StatusClass: "status-danger",
			LastSeen:    "Never",
		}
	}

	lastSeenAt := time.UnixMilli(device.LastEventReceivedMS)
	connected := now.Sub(lastSeenAt) <= time.Duration(device.ExpectedHeartbeatSeconds)*time.Second
	if connected {
		return deviceConnectivityView{
			Connected:   true,
			Status:      "Connected",
			StatusClass: "status-success",
			LastSeen:    formatUnixMS(device.LastEventReceivedMS),
		}
	}
	return deviceConnectivityView{
		Status:      "Disconnected",
		StatusClass: "status-danger",
		LastSeen:    formatUnixMS(device.LastEventReceivedMS),
	}
}

func formatUnixMS(timestampMS int64) string {
	if timestampMS == 0 {
		return ""
	}
	return time.UnixMilli(timestampMS).UTC().Format(time.RFC3339)
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		user, err := s.store.UserBySession(r.Context(), cookie.Value, time.Now())
		if errors.Is(err, db.ErrNotFound) {
			http.SetCookie(w, expiredSessionCookie())
			http.Redirect(w, r, "/login", http.StatusSeeOther)
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

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
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

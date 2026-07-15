package domain

type User struct {
	// ID is the internal user identifier.
	ID int64
	// Email is the user's login email address.
	Email string
	// Name is the user's display name.
	Name string
	// PasswordHash is the bcrypt hash used for authentication.
	PasswordHash string
	// IsAdmin grants access to every organisation.
	IsAdmin bool
}

type Organisation struct {
	// ID is the internal organisation identifier.
	ID int64
	// Name is the organisation display name.
	Name string
}

type OrganisationMembership struct {
	// UserID identifies the member user.
	UserID int64
	// OrganisationID identifies the organisation the user belongs to.
	OrganisationID int64
	// Role is the built-in organisation role assigned to the user.
	Role string
}

type OrganisationMember struct {
	// UserID identifies the member user.
	UserID int64
	// OrganisationID identifies the organisation the user belongs to.
	OrganisationID int64
	// Email is the member login email.
	Email string
	// Name is the member display name.
	Name string
	// Role is the built-in organisation role assigned to the user.
	Role string
}

type OrganisationInvitation struct {
	// ID is the internal invitation identifier.
	ID int64
	// OrganisationID identifies the invited organisation.
	OrganisationID int64
	// OrganisationName is populated when reading invitation details for display.
	OrganisationName string
	// Email is the invited email address.
	Email string
	// TokenHash is the stored hash of the raw invitation token.
	TokenHash string
	// ExpiresAt is the invitation expiry timestamp.
	ExpiresAt string
	// AcceptedAt is set when the invitation has been consumed.
	AcceptedAt string
	// InviterUserID identifies the user who created the invite.
	InviterUserID int64
	// CreatedAt is when the invite was created.
	CreatedAt string
}

type OrganisationAPICredential struct {
	// ID is the internal credential identifier.
	ID int64
	// OrganisationID identifies the organisation this token provisions for.
	OrganisationID int64
	// Name is the human-readable label shown to organisation admins.
	Name string
	// TokenHash stores a one-way hash of the bearer token.
	TokenHash string
	// Enabled controls whether the token may authenticate.
	Enabled bool
	// LastUsedAt is set after successful API authentication.
	LastUsedAt string
	// CreatedAt is when the credential was created.
	CreatedAt string
	// UpdatedAt is when the credential was last changed.
	UpdatedAt string
}

// MQTTIntegrationConfig contains Anchor's application-level Mosquitto connection settings.
type MQTTIntegrationConfig struct {
	Enabled    bool
	BrokerURL  string
	ClientID   string
	Username   string
	Password   string
	QoS        byte
	Configured bool
	UpdatedAt  string
}

const (
	MQTTConnectionDisabled     = "disabled"
	MQTTConnectionConnecting   = "connecting"
	MQTTConnectionConnected    = "connected"
	MQTTConnectionDisconnected = "disconnected"
	MQTTConnectionFailed       = "failed"
)

// MQTTIntegrationStatus describes the internal client's current broker connection state.
type MQTTIntegrationStatus struct {
	State     string
	Reason    string
	UpdatedAt string
}

type Device struct {
	// ID is the stable device identifier.
	ID string
	// OrganisationID identifies the organisation that owns the device.
	OrganisationID int64
	// DeviceModelID identifies the required model definition for this device.
	DeviceModelID int64
	// ModelName is the linked device model name shown to users.
	ModelName string
	// ExpectedHeartbeatSeconds is copied from the linked model for connectivity checks.
	ExpectedHeartbeatSeconds int64
	// LastEventReceivedMS is the latest received device event timestamp in Unix milliseconds.
	LastEventReceivedMS int64
	// SoftwareVersions contains reported component versions.
	SoftwareVersions SoftwareVersions
	// IsGateway allows the device to publish data for other devices in its organisation.
	IsGateway bool
}

type DeviceModel struct {
	// ID is the internal model identifier.
	ID int64
	// OrganisationID identifies the organisation that owns the model.
	OrganisationID int64
	// Name is the model display name.
	Name string
	// ExpectedHeartbeatSeconds is the expected communication interval in seconds.
	ExpectedHeartbeatSeconds int64
	// ExpectedProtocol is the protocol devices of this model are expected to use.
	ExpectedProtocol string
	// ExpectedReleaseID optionally identifies the expected software release.
	ExpectedReleaseID *int64
	// ExpectedReleaseModelName is populated when listing models with release details.
	ExpectedReleaseModelName string
	// ExpectedReleaseVersion is populated when listing models with release details.
	ExpectedReleaseVersion string
	// CreatedAt is the model creation timestamp.
	CreatedAt string
}

type DeviceMQTTCredential struct {
	// DeviceID identifies the device that owns the credential.
	DeviceID string
	// Username is the MQTT username presented by the broker client.
	Username string
	// PasswordHash is the bcrypt hash used for MQTT authentication.
	PasswordHash string
	// Enabled controls whether the credential may authenticate.
	Enabled bool
}

type DeviceWithMQTTCredential struct {
	// Device is the device being configured.
	Device Device
	// Credential is the MQTT credential for the device.
	Credential DeviceMQTTCredential
}

type DeviceWithMQTT struct {
	// Device is the configured device.
	Device Device
	// MQTTCredential is the device MQTT credential, when present.
	MQTTCredential *DeviceMQTTCredential
}

type DeviceListRow struct {
	// Device is the configured device.
	Device Device
	// HasMQTTCredential records whether a credential exists without exposing auth material.
	HasMQTTCredential bool
	// CVEStatus is the denormalized status for the device's matched firmware release.
	CVEStatus CVEImpactStatus
}

type DeviceDetail struct {
	// Device is the device common data.
	Device Device
	// MQTTCredential is the device MQTT credential, when present.
	MQTTCredential *DeviceMQTTCredential
}

type DeviceEvent struct {
	// ID is the internal event identifier.
	ID int64
	// DeviceID identifies the device this event belongs to.
	DeviceID string
	// TSReceivedMS is the server receive timestamp in Unix milliseconds.
	TSReceivedMS int64
	// Protocol is the transport protocol, such as mqtt, coap, or lwm2m.
	Protocol string
	// Direction is inbound or outbound from Anchor's perspective.
	Direction string
	// Operation is the protocol operation, such as publish, get, post, notify, or ack.
	Operation string
	// Topic is the MQTT topic, when applicable.
	Topic string
	// CoAPPath is the CoAP path, when applicable.
	CoAPPath string
	// Method is the HTTP-like protocol method, when applicable.
	Method string
	// Code is the protocol response code, when applicable.
	Code string
	// ContentFormat describes the payload encoding.
	ContentFormat string
	// PayloadRaw contains the original payload bytes.
	PayloadRaw []byte
	// PayloadJSON contains decoded JSON when available.
	PayloadJSON string
	// CorrelationID links related protocol messages.
	CorrelationID string
	// SchemaHint identifies the expected payload schema, when known.
	SchemaHint string
	// Source identifies where the event came from, such as broker, gateway, direct, or simulator.
	Source string
	// Retained records whether the event was retained by the transport.
	Retained bool
}

type DeviceTwinProperty struct {
	// DeviceID identifies the device this current property belongs to.
	DeviceID string
	// Path is the canonical property path, such as battery.percent or lwm2m./3/0/9.
	Path string
	// ValueJSON stores the current value as JSON.
	ValueJSON string
	// ValueType is null, bool, number, string, object, array, or bytes.
	ValueType string
	// SourceEventID points at the event that last updated this property, when retained.
	SourceEventID *int64
	// TSObservedMS is the device observation timestamp in Unix milliseconds.
	TSObservedMS int64
	// TSReceivedMS is the server receive timestamp in Unix milliseconds.
	TSReceivedMS int64
	// Protocol is the transport protocol that produced this property.
	Protocol string
	// SourcePath is the protocol-specific source path or topic.
	SourcePath string
}

type DeviceTwinDetail struct {
	// Device is the device common data.
	Device Device
	// Properties are the current materialized twin values.
	Properties []DeviceTwinProperty
	// RecentEvents are the latest raw protocol events for this device.
	RecentEvents []DeviceEvent
}

type DeviceTask struct {
	// ID is the internal task identifier.
	ID int64
	// DeviceID identifies the device this task targets.
	DeviceID string
	// Type is the task kind: read, write, or fota.
	Type string
	// ParametersJSON stores validated, protocol-neutral task intent.
	ParametersJSON string
	// Status is queued, pending, in_progress, success, failure, expired, or canceled.
	Status string
	// StatusMessage is the latest optional device-reported task message.
	StatusMessage string
	// CampaignID links the task to a campaign when it was campaign-created.
	CampaignID *int64
	// CreatedAt is when the task was created.
	CreatedAt string
	// ExpiresAt is when the task times out if it is still non-terminal.
	ExpiresAt string
	// CompletedAt is set when the task reaches a terminal status.
	CompletedAt string
}

type Campaign struct {
	ID               int64
	OrganisationID   int64
	Name             string
	TaskType         string
	ParametersJSON   string
	TaskTTLSeconds   int64
	Status           string
	CreatedAt        string
	FinishedAt       string
	CanceledAt       string
	TargetCount      int
	ParameterSummary string
	Counts           TaskStatusCounts
}

type CampaignTaskRow struct {
	Task            DeviceTask
	DeviceModelID   int64
	DeviceModelName string
}

type TaskStatusCounts struct {
	Queued     int
	Pending    int
	InProgress int
	Success    int
	Failure    int
	Expired    int
	Canceled   int
}

func (c TaskStatusCounts) Total() int {
	return c.Queued + c.Pending + c.InProgress + c.Success + c.Failure + c.Expired + c.Canceled
}

type MQTTPrincipal struct {
	// DeviceID identifies the authenticated device.
	DeviceID string
	// OrganisationID identifies the organisation that owns the device.
	OrganisationID int64
	// IsGateway allows publishing data for other devices in the same organisation.
	IsGateway bool
	// Enabled controls whether the credential may authenticate.
	Enabled bool
}

type SoftwareRelease struct {
	// ID is the internal software release identifier.
	ID int64
	// OrganisationID identifies the organisation that owns the release.
	OrganisationID int64
	// DeviceModelID identifies the model this firmware release targets.
	DeviceModelID int64
	// DeviceModelName is populated when listing releases with model details.
	DeviceModelName string
	// Version is the release version string.
	Version string
	// ArtifactPath is the relative path to the release binary in artifact storage.
	ArtifactPath string
	// ArtifactFilename is the original uploaded filename.
	ArtifactFilename string
	// ArtifactContentType is the uploaded binary content type.
	ArtifactContentType string
	// ArtifactSizeBytes is the uploaded binary size in bytes.
	ArtifactSizeBytes int64
	// CreatedAt is the release creation timestamp.
	CreatedAt string
}

type ReleaseSBOM struct {
	// ID is the current SBOM set identifier for a release.
	ID int64
	// OrganisationID identifies the organisation that owns the release.
	OrganisationID int64
	// ReleaseID identifies the software release this SBOM belongs to.
	ReleaseID int64
	// FileCount is the number of SPDX files in the current SBOM set.
	FileCount int
	// TotalSizeBytes is the aggregate SPDX upload size in bytes.
	TotalSizeBytes int64
	// CreatedAt is when this SBOM set was registered.
	CreatedAt string
	// UpdatedAt is when this SBOM set was last replaced.
	UpdatedAt string
}

type CVEScanRun struct {
	// ID is the internal scan run identifier.
	ID int64
	// OrganisationID identifies the organisation that owns the release.
	OrganisationID int64
	// ReleaseID identifies the release being scanned.
	ReleaseID int64
	// ReleaseSBOMID identifies the current SBOM set being scanned.
	ReleaseSBOMID int64
	// Trigger is auto or manual.
	Trigger string
	// Status is pending, running, success, or failed.
	Status string
	// ErrorMessage stores scanner failure detail for failed runs.
	ErrorMessage string
	// CreatedAt is when the scan was enqueued.
	CreatedAt string
	// StartedAt is set when the worker starts the scan.
	StartedAt string
	// FinishedAt is set when the scan reaches a terminal state.
	FinishedAt string
}

type CVEScanFinding struct {
	// ID is the internal finding identifier.
	ID int64
	// OrganisationID identifies the organisation that owns the release.
	OrganisationID int64
	// ReleaseID identifies the scanned release.
	ReleaseID int64
	// ScanRunID identifies the scan run that produced this finding.
	ScanRunID int64
	// CVEID is the vulnerability identifier, such as CVE-2026-1234.
	CVEID string
	// Severity is the scanner-provided severity.
	Severity string
	// PackageName is the scanner-provided package or component name.
	PackageName string
	// InstalledVersion is the vulnerable installed version.
	InstalledVersion string
	// CreatedAt is when this finding was stored.
	CreatedAt string
}

type ReleaseCVEWaiver struct {
	// ID is the internal waiver identifier.
	ID int64
	// OrganisationID identifies the organisation that owns the release.
	OrganisationID int64
	// ReleaseID identifies the release this waiver applies to.
	ReleaseID int64
	// CVEID is the release-scoped CVE being marked not relevant.
	CVEID string
	// Note is the optional user note.
	Note string
	// UserID identifies the user who marked the CVE not relevant when available.
	UserID int64
	// CreatedAt is when this waiver was first created.
	CreatedAt string
	// UpdatedAt is when this waiver was last updated.
	UpdatedAt string
}

type Session struct {
	// ID is the opaque session token.
	ID string
	// UserID links the session to a user.
	UserID int64
	// ExpiresAt is the session expiration timestamp.
	ExpiresAt string
}

type SoftwareVersions map[string]string

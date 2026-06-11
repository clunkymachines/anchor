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

type Device struct {
	// ID is the stable device identifier.
	ID string
	// OrganisationID identifies the organisation that owns the device.
	OrganisationID int64
	// ModelName is the device model name shown to users.
	ModelName string
	// SoftwareVersions contains reported component versions.
	SoftwareVersions SoftwareVersions
	// IsGateway allows the device to publish data for other devices in its organisation.
	IsGateway bool
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
	// Type is the task kind: read, write, exec, or fota.
	Type string
	// Parameter is an optional freeform task argument, limited to 256 characters.
	Parameter string
	// Status is pending, in_progress, success, failure, or canceled.
	Status string
	// CreatedAt is when the task was created.
	CreatedAt string
	// CompletedAt is set when the task reaches a terminal status.
	CompletedAt string
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
	// Name is the software component or product name.
	Name string
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

type OTADeployment struct {
	// ID is the internal OTA deployment identifier.
	ID int64
	// OrganisationID identifies the organisation that owns the deployment.
	OrganisationID int64
	// ReleaseID identifies the software release being deployed.
	ReleaseID int64
	// ReleaseName is the software component or product name being deployed.
	ReleaseName string
	// ReleaseVersion is the version string being deployed.
	ReleaseVersion string
	// Target is the deployment target, such as a fleet, organisation, or filter.
	Target string
	// Status is the current deployment state.
	Status string
	// CreatedAt is the deployment creation timestamp.
	CreatedAt string
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

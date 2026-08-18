// Package coapapi contains the versioned private HTTP contract shared by
// Anchor and the CoAP frontend. It deliberately has no database or runtime
// dependencies.
package coapapi

import "encoding/base64"

const (
	VersionPrefix          = "/internal/coap/v1"
	ResolveCredentialsPath = VersionPrefix + "/credentials/resolve"
	ActivityPathPrefix     = VersionPrefix + "/devices/"
)

type CredentialResolveRequest struct {
	PSKIdentity string `json:"psk_identity"`
}
type CredentialResolveResponse struct {
	DeviceID                 string `json:"device_id"`
	OrganisationID           int64  `json:"organisation_id"`
	PSK                      string `json:"psk"`
	Revision                 int64  `json:"revision"`
	ExpectedHeartbeatSeconds int64  `json:"expected_heartbeat_seconds"`
	ExpectedProtocol         string `json:"expected_protocol"`
}

func EncodePSK(psk []byte) string            { return base64.StdEncoding.EncodeToString(psk) }
func DecodePSK(value string) ([]byte, error) { return base64.StdEncoding.DecodeString(value) }

type TaskProjection struct {
	ID          int64            `json:"id"`
	DeviceID    string           `json:"device_id"`
	Type        string           `json:"type"`
	CreatedAt   string           `json:"created_at"`
	ExpiresAt   string           `json:"expires_at"`
	ReadPaths   []string         `json:"read_paths,omitempty"`
	WriteValues []TaskWriteValue `json:"write_values,omitempty"`
	FOTATaskID  int64            `json:"fota_task_id,omitempty"`
	ArtifactURL string           `json:"artifact_url,omitempty"`
}
type TaskWriteValue struct {
	Path  string `json:"path"`
	Value any    `json:"value"`
}

type ActivityRequest struct {
	TimestampMS int64  `json:"timestamp_ms"`
	Reason      string `json:"reason,omitempty"`
}
type TelemetryRequest struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	ContentFormat string `json:"content_format"`
	CorrelationID string `json:"correlation_id"`
	TimestampMS   int64  `json:"timestamp_ms"`
	Payload       []byte `json:"payload"`
}
type OperationResultRequest struct {
	TaskID          int64  `json:"task_id"`
	CorrelationID   string `json:"correlation_id"`
	Method          string `json:"method"`
	Path            string `json:"path"`
	RequestCode     string `json:"request_code,omitempty"`
	ResponseCode    string `json:"response_code,omitempty"`
	ContentFormat   string `json:"content_format,omitempty"`
	RequestPayload  []byte `json:"request_payload,omitempty"`
	ResponsePayload []byte `json:"response_payload,omitempty"`
	Error           string `json:"error,omitempty"`
	TimestampMS     int64  `json:"timestamp_ms"`
}
type TaskStatusRequest struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}
type AssociationStatus struct {
	DeviceID           string `json:"device_id"`
	Connected          bool   `json:"connected"`
	Generation         uint64 `json:"generation,omitempty"`
	CredentialRevision int64  `json:"credential_revision,omitempty"`
	CIDNegotiated      bool   `json:"cid_negotiated"`
	CIDLength          int    `json:"cid_length,omitempty"`
	PeerAddress        string `json:"peer_address,omitempty"`
	LastActivityMS     int64  `json:"last_activity_ms,omitempty"`
}
type FrontendStatus struct {
	State              string `json:"state"`
	Reason             string `json:"reason,omitempty"`
	ActiveAssociations int    `json:"active_associations"`
}

type InvalidateRequest struct {
	Revision int64 `json:"revision"`
	Force    bool  `json:"force"`
}

type Metrics struct {
	ActiveAssociations  int64 `json:"active_associations"`
	HandshakeSuccess    int64 `json:"handshake_success"`
	HandshakeFailure    int64 `json:"handshake_failure"`
	CIDNegotiated       int64 `json:"cid_negotiated"`
	CIDLength           int64 `json:"cid_length"`
	CIDPacketReceived   int64 `json:"cid_packet_received"`
	CIDPacketRouted     int64 `json:"cid_packet_routed"`
	PeerAddressChanged  int64 `json:"peer_address_changed"`
	CoAPRequestReceived int64 `json:"coap_request_received"`
	RequestsAccepted    int64 `json:"requests_accepted"`
	RequestsRejected    int64 `json:"requests_rejected"`
	DispatchStarted     int64 `json:"dispatch_started"`
	DispatchFailed      int64 `json:"dispatch_failed"`
	DispatchCompleted   int64 `json:"dispatch_completed"`
}

// Package coapcontrol implements Anchor's private control-plane client for the
// separate CoAP frontend. It never carries device secrets in logs or URLs.
package coapcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"anchor/internal/coapapi"
	"anchor/internal/domain"
)

type Config struct {
	BaseURL               string
	BearerToken           string
	ConnectTimeout        time.Duration
	ResponseHeaderTimeout time.Duration
	RequestTimeout        time.Duration
	OverallTimeout        time.Duration
	FOTADownloadBaseURL   string
}

type Manager struct {
	config Config
	client *http.Client
}

func New(config Config) (*Manager, error) {
	if err := domain.ValidateCoAPFrontendURL(config.BaseURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.BearerToken) == "" {
		return nil, errors.New("CoAP bearer token is required")
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 2 * time.Second
	}
	if config.ResponseHeaderTimeout <= 0 {
		config.ResponseHeaderTimeout = 5 * time.Second
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 10 * time.Second
	}
	if config.OverallTimeout <= 0 {
		config.OverallTimeout = 15 * time.Second
	}
	transport := &http.Transport{ResponseHeaderTimeout: config.ResponseHeaderTimeout, DialContext: (&net.Dialer{Timeout: config.ConnectTimeout}).DialContext}
	return &Manager{config: config, client: &http.Client{Transport: transport, Timeout: config.OverallTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// ProjectTask converts a durable task into the frontend's protocol-neutral
// projection and validates literal CoAP paths.
func ProjectTask(task domain.DeviceTask, artifactURL string) (coapapi.TaskProjection, error) {
	projection := coapapi.TaskProjection{ID: task.ID, DeviceID: task.DeviceID, Type: task.Type, CreatedAt: task.CreatedAt, ExpiresAt: task.ExpiresAt}
	switch task.Type {
	case domain.TaskTypeRead:
		var params domain.ReadTaskParameters
		if err := json.Unmarshal([]byte(task.ParametersJSON), &params); err != nil {
			return projection, err
		}
		for _, path := range params.Paths {
			if err := domain.ValidateCoAPResourcePath(path); err != nil {
				return projection, err
			}
		}
		projection.ReadPaths = params.Paths
	case domain.TaskTypeWrite:
		var params domain.WriteTaskParameters
		if err := json.Unmarshal([]byte(task.ParametersJSON), &params); err != nil {
			return projection, err
		}
		for _, value := range params.Values {
			if err := domain.ValidateCoAPResourcePath(value.Path); err != nil {
				return projection, err
			}
			var decoded any
			decoder := json.NewDecoder(bytes.NewReader(value.Value))
			decoder.UseNumber()
			if err := decoder.Decode(&decoded); err != nil {
				return projection, err
			}
			projection.WriteValues = append(projection.WriteValues, coapapi.TaskWriteValue{Path: value.Path, Value: decoded})
		}
	case domain.TaskTypeFOTA:
		params, err := domain.ParseFOTATaskParameters(task.ParametersJSON)
		if err != nil {
			return projection, err
		}
		u, err := url.Parse(artifactURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return projection, errors.New("absolute FOTA artifact URL is required")
		}
		projection.FOTATaskID = params.ReleaseID
		projection.ArtifactURL = artifactURL
	default:
		return projection, fmt.Errorf("unsupported task type %q", task.Type)
	}
	return projection, nil
}

func (m *Manager) do(ctx context.Context, method, path string, request any, response any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, m.config.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.config.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.config.BearerToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("coap frontend returned HTTP %d", res.StatusCode)
	}
	if response == nil || res.StatusCode == http.StatusNoContent || res.ContentLength == 0 {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(response); err != nil {
		return fmt.Errorf("decode coap frontend response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("CoAP frontend response must contain exactly one JSON value")
	}
	return nil
}

func (m *Manager) ResolveCredentials(ctx context.Context, identity string) (coapapi.CredentialResolveResponse, error) {
	var out coapapi.CredentialResolveResponse
	err := m.do(ctx, http.MethodPost, coapapi.ResolveCredentialsPath, coapapi.CredentialResolveRequest{PSKIdentity: identity}, &out)
	return out, err
}

func (m *Manager) PublishDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) error {
	artifactURL := ""
	if m.config.FOTADownloadBaseURL != "" {
		artifactURL = strings.TrimRight(m.config.FOTADownloadBaseURL, "/") + "/org/" + strconv.FormatInt(organisationID, 10) + "/releases/"
		if params, err := domain.ParseFOTATaskParameters(task.ParametersJSON); err == nil {
			artifactURL += strconv.FormatInt(params.ReleaseID, 10) + "/binary"
		}
	}
	projection, err := ProjectTask(task, artifactURL)
	if err != nil {
		return err
	}
	return m.Dispatch(ctx, projection)
}

// PublishPendingDeviceTasks is intentionally a no-op. Pending tasks remain in
// Anchor and the authenticated frontend asks Anchor for the single current
// pending task when a device association becomes available.
func (m *Manager) PublishPendingDeviceTasks(context.Context, string, int64) error { return nil }
func (m *Manager) Dispatch(ctx context.Context, task coapapi.TaskProjection) error {
	return m.do(ctx, http.MethodPost, "/internal/coap/v1/tasks/"+strconv.FormatInt(task.ID, 10)+"/dispatch", task, nil)
}
func (m *Manager) Invalidate(ctx context.Context, deviceID string, revision int64, force bool) error {
	return m.do(ctx, http.MethodPost, "/internal/coap/v1/devices/"+url.PathEscape(deviceID)+"/invalidate", map[string]any{"revision": revision, "force": force}, nil)
}
func (m *Manager) Association(ctx context.Context, deviceID string) (coapapi.AssociationStatus, error) {
	var out coapapi.AssociationStatus
	err := m.do(ctx, http.MethodGet, "/internal/coap/v1/devices/"+url.PathEscape(deviceID)+"/association", nil, &out)
	return out, err
}
func (m *Manager) Status(ctx context.Context) (coapapi.FrontendStatus, error) {
	var out coapapi.FrontendStatus
	err := m.do(ctx, http.MethodGet, "/internal/coap/v1/status", nil, &out)
	return out, err
}

// IntegrationStatus probes the frontend and translates its private status
// response into Anchor's UI-facing integration state.
func (m *Manager) IntegrationStatus(ctx context.Context) domain.CoAPIntegrationStatus {
	status, err := m.Status(ctx)
	if err != nil {
		return domain.CoAPIntegrationStatus{State: domain.CoAPIntegrationUnreachable, Reason: "Frontend health check failed: " + err.Error()}
	}
	state := domain.CoAPIntegrationDegraded
	switch status.State {
	case "healthy":
		state = domain.CoAPIntegrationHealthy
	case "disabled":
		state = domain.CoAPIntegrationDisabled
	}
	return domain.CoAPIntegrationStatus{State: state, Reason: status.Reason, ActiveAssociations: status.ActiveAssociations, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
}

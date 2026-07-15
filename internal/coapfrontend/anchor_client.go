package coapfrontend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"anchor/internal/coapapi"
)

type AnchorClient interface {
	ResolveCredentials(context.Context, string) (coapapi.CredentialResolveResponse, error)
	Activity(context.Context, string, coapapi.ActivityRequest) error
	Telemetry(context.Context, string, coapapi.TelemetryRequest) error
	Operation(context.Context, string, int64, coapapi.OperationResultRequest) error
	TaskStatus(context.Context, string, int64, coapapi.TaskStatusRequest) error
	PendingTask(context.Context, string) (coapapi.TaskProjection, bool, error)
}

type HTTPAnchorClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPAnchorClient(baseURL, token string, client *http.Client) (*HTTPAnchorClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("Anchor internal URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("internal bearer token is required")
	}
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &HTTPAnchorClient{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: client}, nil
}

func (c *HTTPAnchorClient) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("Anchor returned HTTP %d", res.StatusCode)
	}
	if out != nil && res.StatusCode != http.StatusNoContent {
		dec := json.NewDecoder(io.LimitReader(res.Body, 1<<20))
		dec.UseNumber()
		dec.DisallowUnknownFields()
		if err := dec.Decode(out); err != nil {
			return res.StatusCode, err
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return res.StatusCode, errors.New("Anchor response must contain exactly one JSON value")
		}
	}
	return res.StatusCode, nil
}

func (c *HTTPAnchorClient) ResolveCredentials(ctx context.Context, identity string) (coapapi.CredentialResolveResponse, error) {
	var out coapapi.CredentialResolveResponse
	_, err := c.do(ctx, http.MethodPost, coapapi.ResolveCredentialsPath, coapapi.CredentialResolveRequest{PSKIdentity: identity}, &out)
	return out, err
}
func (c *HTTPAnchorClient) Activity(ctx context.Context, deviceID string, in coapapi.ActivityRequest) error {
	_, err := c.do(ctx, http.MethodPost, coapapi.VersionPrefix+"/devices/"+url.PathEscape(deviceID)+"/activity", in, nil)
	return err
}
func (c *HTTPAnchorClient) Telemetry(ctx context.Context, deviceID string, in coapapi.TelemetryRequest) error {
	_, err := c.do(ctx, http.MethodPost, coapapi.VersionPrefix+"/devices/"+url.PathEscape(deviceID)+"/telemetry", in, nil)
	return err
}
func (c *HTTPAnchorClient) Operation(ctx context.Context, deviceID string, taskID int64, in coapapi.OperationResultRequest) error {
	_, err := c.do(ctx, http.MethodPost, coapapi.VersionPrefix+"/devices/"+url.PathEscape(deviceID)+"/tasks/"+strconv.FormatInt(taskID, 10)+"/operations", in, nil)
	return err
}
func (c *HTTPAnchorClient) TaskStatus(ctx context.Context, deviceID string, taskID int64, in coapapi.TaskStatusRequest) error {
	_, err := c.do(ctx, http.MethodPut, coapapi.VersionPrefix+"/devices/"+url.PathEscape(deviceID)+"/tasks/"+strconv.FormatInt(taskID, 10)+"/status", in, nil)
	return err
}
func (c *HTTPAnchorClient) PendingTask(ctx context.Context, deviceID string) (coapapi.TaskProjection, bool, error) {
	var out coapapi.TaskProjection
	status, err := c.do(ctx, http.MethodGet, coapapi.VersionPrefix+"/devices/"+url.PathEscape(deviceID)+"/tasks/pending", nil, &out)
	if err != nil {
		return out, false, err
	}
	return out, status != http.StatusNoContent, nil
}

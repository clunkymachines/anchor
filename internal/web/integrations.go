package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"anchor/internal/coapcontrol"
	"anchor/internal/db"
	"anchor/internal/domain"
)

func (s *Server) integrations(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.adminShellData(w, r)
	if !ok {
		return
	}

	config, err := s.store.MQTTIntegration(r.Context())
	if errors.Is(err, db.ErrNotFound) {
		config = defaultMQTTIntegrationConfig()
	} else if err != nil {
		http.Error(w, "integration query error", http.StatusInternalServerError)
		return
	}
	coapConfig, coapErr := s.store.CoAPIntegration(r.Context())
	if errors.Is(coapErr, db.ErrNotFound) {
		coapConfig = defaultCoAPIntegrationConfig()
	} else if coapErr != nil {
		http.Error(w, "CoAP integration query error", http.StatusInternalServerError)
		return
	}

	mqttMessage, coapMessage := "", ""
	switch r.URL.Query().Get("saved") {
	case "1":
		mqttMessage = "MQTT configuration saved."
	case "coap":
		coapMessage = "CoAP configuration saved."
	}
	data := s.integrationsPageDataFor(shell, config, "", mqttMessage)
	s.setCoAPIntegrationPageData(r.Context(), &data, coapConfig, "", coapMessage)
	s.renderIntegrations(w, data)
}

func (s *Server) mqttIntegrationStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok || !user.IsAdmin {
		http.Error(w, "Anchor admin required", http.StatusForbidden)
		return
	}

	config, err := s.store.MQTTIntegration(r.Context())
	if errors.Is(err, db.ErrNotFound) {
		config = defaultMQTTIntegrationConfig()
	} else if err != nil {
		http.Error(w, "integration query error", http.StatusInternalServerError)
		return
	}
	if err := s.render(w, http.StatusOK, s.integrationsPageDataFor(shellPageData{}, config, "", ""), "mqtt_integration_status"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) mqttIntegrationPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	shell, ok := s.adminShellData(w, r)
	if !ok {
		return
	}

	existing, err := s.store.MQTTIntegration(r.Context())
	if errors.Is(err, db.ErrNotFound) {
		existing = domain.MQTTIntegrationConfig{}
	} else if err != nil {
		http.Error(w, "integration query error", http.StatusInternalServerError)
		return
	}

	qos, qosErr := strconv.Atoi(r.FormValue("qos"))
	config := domain.MQTTIntegrationConfig{
		Enabled:    r.FormValue("enabled") == "1",
		BrokerURL:  strings.TrimSpace(r.FormValue("broker_url")),
		ClientID:   strings.TrimSpace(r.FormValue("client_id")),
		Username:   strings.TrimSpace(r.FormValue("username")),
		Password:   r.FormValue("password"),
		Configured: true,
	}
	if qosErr == nil && qos >= 0 && qos <= 2 {
		config.QoS = byte(qos)
	}
	if config.Password == "" {
		config.Password = existing.Password
	}

	if message := validateMQTTIntegrationConfig(config, qosErr); message != "" {
		s.renderIntegrations(w, s.integrationsPageDataFor(shell, config, message, ""))
		return
	}

	if err := s.store.SaveMQTTIntegration(r.Context(), config); err != nil {
		http.Error(w, "integration save error", http.StatusInternalServerError)
		return
	}
	if s.mqttIntegrationRuntime != nil {
		if err := s.mqttIntegrationRuntime.ApplyMQTTIntegration(r.Context(), config); err != nil {
			s.renderIntegrations(w, s.integrationsPageDataFor(shell, config, fmt.Sprintf("Configuration saved, but MQTT could not start: %v", err), ""))
			return
		}
	}

	target := "/integrations?saved=1"
	if shell.SelectedOrganisationID > 0 {
		target += "&organisation_id=" + strconv.FormatInt(shell.SelectedOrganisationID, 10)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) coAPIntegrationPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.adminShellData(w, r)
	if !ok {
		return
	}
	existing, err := s.store.CoAPIntegration(r.Context())
	if errors.Is(err, db.ErrNotFound) {
		existing = defaultCoAPIntegrationConfig()
	} else if err != nil {
		http.Error(w, "integration query error", http.StatusInternalServerError)
		return
	}
	config := domain.CoAPIntegrationConfig{Enabled: r.FormValue("enabled") == "1", FrontendURL: strings.TrimSpace(r.FormValue("frontend_url")), BearerToken: r.FormValue("bearer_token"), Configured: true}
	if config.FrontendURL == "" {
		config.FrontendURL = existing.FrontendURL
	}
	if config.BearerToken == "" {
		config.BearerToken = existing.BearerToken
	}
	if config.Enabled && config.BearerToken == "" {
		data := s.integrationsPageDataFor(shell, domain.MQTTIntegrationConfig{}, "", "")
		s.setCoAPIntegrationPageData(r.Context(), &data, config, "Bearer token is required when CoAP is active.", "")
		s.renderIntegrations(w, data)
		return
	}
	if err := s.store.SaveCoAPIntegration(r.Context(), config); err != nil {
		data := s.integrationsPageDataFor(shell, domain.MQTTIntegrationConfig{}, "", "")
		s.setCoAPIntegrationPageData(r.Context(), &data, config, err.Error(), "")
		s.renderIntegrations(w, data)
		return
	}
	s.coAPInternalToken = config.BearerToken
	s.coAPIntegrationEnabled = config.Enabled
	if runtime, ok := s.coAPIntegrationRuntime.(CoAPConfigRuntime); ok {
		if err := runtime.ApplyCoAPIntegration(r.Context(), config); err != nil {
			http.Error(w, "could not apply CoAP integration", http.StatusBadGateway)
			return
		}
	}
	if config.Enabled {
		if _, managed := s.coAPIntegrationRuntime.(CoAPConfigRuntime); !managed {
			if runtime, err := coapcontrol.New(coapcontrol.Config{BaseURL: config.FrontendURL, BearerToken: config.BearerToken}); err == nil {
				s.coAPIntegrationRuntime = runtime
			}
		}
	} else {
		if _, managed := s.coAPIntegrationRuntime.(CoAPConfigRuntime); !managed {
			s.coAPIntegrationRuntime = nil
		}
	}
	target := "/integrations?saved=coap"
	if shell.SelectedOrganisationID > 0 {
		target += "&organisation_id=" + strconv.FormatInt(shell.SelectedOrganisationID, 10)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) coAPIntegrationStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok || !user.IsAdmin {
		http.Error(w, "Anchor admin required", http.StatusForbidden)
		return
	}
	config, err := s.store.CoAPIntegration(r.Context())
	if errors.Is(err, db.ErrNotFound) {
		config = defaultCoAPIntegrationConfig()
	} else if err != nil {
		http.Error(w, "integration query error", http.StatusInternalServerError)
		return
	}
	data := s.integrationsPageDataFor(shellPageData{}, domain.MQTTIntegrationConfig{}, "", "")
	s.setCoAPIntegrationPageData(r.Context(), &data, config, "", "")
	if err := s.render(w, http.StatusOK, data, "coap_integration_status"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) adminShellData(w http.ResponseWriter, r *http.Request) (shellPageData, bool) {
	user, ok := s.currentUser(r)
	if !ok || !user.IsAdmin {
		http.Error(w, "Anchor admin required", http.StatusForbidden)
		return shellPageData{}, false
	}
	return s.shellData(w, r)
}

func defaultMQTTIntegrationConfig() domain.MQTTIntegrationConfig {
	return domain.MQTTIntegrationConfig{
		BrokerURL: "mqtt://127.0.0.1:1883",
		ClientID:  "anchor-ingest",
		Username:  "anchor-ingest",
		QoS:       0,
	}
}

func defaultCoAPIntegrationConfig() domain.CoAPIntegrationConfig {
	return domain.CoAPIntegrationConfig{FrontendURL: "http://127.0.0.1:8081"}
}

func validateMQTTIntegrationConfig(config domain.MQTTIntegrationConfig, qosErr error) string {
	if config.BrokerURL == "" || config.ClientID == "" || config.Username == "" || config.Password == "" {
		return "Broker URL, client ID, username, and password are required."
	}
	brokerURL, err := url.Parse(config.BrokerURL)
	if err != nil || brokerURL.Host == "" {
		return "Enter a valid MQTT broker URL."
	}
	switch brokerURL.Scheme {
	case "mqtt", "mqtts", "ws", "wss":
	default:
		return "Broker URL must use mqtt, mqtts, ws, or wss."
	}
	if qosErr != nil || config.QoS > 2 {
		return "QoS must be 0, 1, or 2."
	}
	return ""
}

func (s *Server) integrationsPageDataFor(shell shellPageData, config domain.MQTTIntegrationConfig, formError string, message string) integrationsPageData {
	status := "Inactive"
	statusClass := "status-neutral"
	if config.Enabled {
		status = "Active"
		statusClass = "status-success"
	}
	connectionStatus := domain.MQTTIntegrationStatus{
		State:  domain.MQTTConnectionDisabled,
		Reason: "The integration is inactive.",
	}
	if s.mqttIntegrationRuntime != nil {
		connectionStatus = s.mqttIntegrationRuntime.MQTTIntegrationStatus()
	} else if config.Enabled {
		connectionStatus = domain.MQTTIntegrationStatus{
			State:  domain.MQTTConnectionConnecting,
			Reason: "Runtime connection status is unavailable.",
		}
	}
	connectionLabel, connectionClass := mqttConnectionStatusView(connectionStatus.State)

	return integrationsPageData{
		Shell:                     shell,
		MQTT:                      config,
		MQTTFormError:             formError,
		MQTTMessage:               message,
		MQTTStatus:                status,
		MQTTStatusClass:           statusClass,
		MQTTConnectionStatus:      connectionLabel,
		MQTTConnectionStatusClass: connectionClass,
		MQTTConnectionReason:      connectionStatus.Reason,
		MQTTConnectionUpdatedAt:   connectionStatus.UpdatedAt,
		PasswordConfigured:        config.Password != "",
	}
}

func (s *Server) setCoAPIntegrationPageData(ctx context.Context, data *integrationsPageData, config domain.CoAPIntegrationConfig, formError, message string) {
	status, statusClass := "Inactive", "status-neutral"
	frontendStatus, frontendClass, reason := "Disabled", "status-neutral", "The integration is inactive."
	if config.Enabled {
		status, statusClass = "Active", "status-success"
		frontendStatus, frontendClass, reason = "Unknown", "status-neutral", "Frontend health is unavailable."
		var health domain.CoAPIntegrationStatus
		if s.coAPIntegrationRuntime != nil {
			health = s.coAPIntegrationRuntime.IntegrationStatus(ctx)
		} else if runtime, err := coapcontrol.New(coapcontrol.Config{BaseURL: config.FrontendURL, BearerToken: config.BearerToken}); err == nil {
			health = runtime.IntegrationStatus(ctx)
		}
		if health.State != "" {
			frontendStatus, frontendClass, reason = coAPFrontendStatusView(health)
			if health.UpdatedAt != "" {
				config.UpdatedAt = health.UpdatedAt
			}
		}
	}
	data.CoAP = config
	data.CoAPFormError = formError
	data.CoAPMessage = message
	data.CoAPStatus = status
	data.CoAPStatusClass = statusClass
	data.CoAPFrontendStatus = frontendStatus
	data.CoAPFrontendStatusClass = frontendClass
	data.CoAPFrontendReason = reason
	data.CoAPTokenConfigured = config.BearerToken != ""
}

func coAPFrontendStatusView(status domain.CoAPIntegrationStatus) (string, string, string) {
	reason := status.Reason
	if reason == "" {
		reason = "Frontend is responding."
	}
	switch status.State {
	case domain.CoAPIntegrationHealthy:
		return "Healthy", "status-success", reason
	case domain.CoAPIntegrationDegraded:
		return "Degraded", "status-warning", reason
	case domain.CoAPIntegrationUnreachable:
		return "Unreachable", "status-danger", reason
	default:
		return "Unknown", "status-neutral", "Frontend health is unavailable."
	}
}

func mqttConnectionStatusView(state string) (string, string) {
	switch state {
	case domain.MQTTConnectionConnected:
		return "Connected", "status-success"
	case domain.MQTTConnectionConnecting:
		return "Connecting", "status-warning"
	case domain.MQTTConnectionDisconnected:
		return "Reconnecting", "status-warning"
	case domain.MQTTConnectionFailed:
		return "Connection failed", "status-danger"
	case domain.MQTTConnectionDisabled:
		return "Disabled", "status-neutral"
	default:
		return "Unknown", "status-neutral"
	}
}

func (s *Server) renderIntegrations(w http.ResponseWriter, data integrationsPageData) {
	if err := s.render(w, http.StatusOK, data, "integrations.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

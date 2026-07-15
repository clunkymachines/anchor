package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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

	message := ""
	if r.URL.Query().Get("saved") == "1" {
		message = "MQTT configuration saved."
	}
	s.renderIntegrations(w, s.integrationsPageDataFor(shell, config, "", message))
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
	s.render(w, "mqtt_integration_status", s.integrationsPageDataFor(shellPageData{}, config, "", ""))
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
	s.render(w, "integrations.html", data)
}

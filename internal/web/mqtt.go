package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"anchor/internal/domain"
)

const (
	mqttDataTopicFilter = "dev/+/+/data"
	mqttTaskTopicFilter = "dev/+/+/task"
)

func parseMQTTAuthRequest(r *http.Request) (mqttAuthRequest, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var req mqttAuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return mqttAuthRequest{}, err
		}
		return req, nil
	}

	if err := r.ParseForm(); err != nil {
		return mqttAuthRequest{}, err
	}
	return mqttAuthRequest{
		Username: r.FormValue("username"),
		Password: r.FormValue("password"),
		ClientID: r.FormValue("clientid"),
	}, nil
}

func parseMQTTACLRequest(r *http.Request) (mqttACLRequest, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var req mqttACLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return mqttACLRequest{}, err
		}
		return req, nil
	}

	if err := r.ParseForm(); err != nil {
		return mqttACLRequest{}, err
	}
	return mqttACLRequest{
		Username: r.FormValue("username"),
		ClientID: r.FormValue("clientid"),
		Topic:    r.FormValue("topic"),
		Access:   r.FormValue("acc"),
	}, nil
}

func mqttAuthorized(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func mqttUnauthorized(w http.ResponseWriter) {
	http.Error(w, "not authorized", http.StatusUnauthorized)
}

func mqttActionsForAccess(access any) []string {
	var acc int64
	switch value := access.(type) {
	case float64:
		acc = int64(value)
	case int:
		acc = int64(value)
	case int64:
		acc = value
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil
		}
		acc = parsed
	default:
		return nil
	}

	switch acc {
	case 1:
		return []string{"read"}
	case 2:
		return []string{"write"}
	case 3:
		return []string{"read", "write"}
	case 4:
		return []string{"subscribe"}
	default:
		return nil
	}
}

func (s *Server) mqttTopicAllowed(ctx context.Context, username string, action string, topic string) (bool, error) {
	principal, err := s.store.FindMQTTPrincipalByUsername(ctx, username)
	if err != nil {
		return false, err
	}
	if !principal.Enabled {
		return false, nil
	}

	switch action {
	case "write":
		return s.mqttDataWriteAllowed(ctx, principal, topic)
	case "read", "subscribe":
		return mqttTaskAllowed(principal, topic), nil
	default:
		return false, nil
	}
}

func (s *Server) internalMQTTClientAuthenticated(username string, password string) bool {
	if !s.internalMQTTClientConfiguredFor(username) || s.internalMQTTClientAuth.Password == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(password), []byte(s.internalMQTTClientAuth.Password)) == 1
}

func (s *Server) internalMQTTClientConfiguredFor(username string) bool {
	return s.internalMQTTClientAuth.Username != "" && username == s.internalMQTTClientAuth.Username
}

func (s *Server) internalMQTTClientTopicAllowed(action string, topic string) bool {
	switch action {
	case "subscribe":
		return topic == mqttDataTopicFilter
	case "read":
		return topic == mqttDataTopicFilter || mqttTopicFilterMatches(mqttDataTopicFilter, topic)
	case "write":
		return mqttTopicFilterMatches(mqttTaskTopicFilter, topic)
	default:
		return false
	}
}

func (s *Server) mqttDataWriteAllowed(ctx context.Context, principal domain.MQTTPrincipal, topic string) (bool, error) {
	organisationID, deviceID, ok := parseMQTTDataTopic(topic)
	if !ok || organisationID != principal.OrganisationID {
		return false, nil
	}
	if deviceID == principal.DeviceID {
		return true, nil
	}
	if !principal.IsGateway {
		return false, nil
	}

	return s.store.DeviceExistsInOrganisation(ctx, deviceID, organisationID)
}

func mqttTaskAllowed(principal domain.MQTTPrincipal, topic string) bool {
	organisationID, deviceID, ok := parseMQTTTaskTopic(topic)
	return ok && organisationID == principal.OrganisationID && deviceID == principal.DeviceID
}

func (s *Server) publishDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) {
	if s.taskPublisher == nil {
		return
	}
	_ = s.taskPublisher.PublishDeviceTask(ctx, task, organisationID)
}

func (s *Server) processTaskQueue(ctx context.Context) {
	now := time.Now().UTC()
	_, _ = s.store.ExpireOverdueDeviceTasks(ctx, now)
	promoted, err := s.store.PromoteQueuedDeviceTasks(ctx, now)
	if err == nil {
		for _, task := range promoted {
			s.publishDeviceTask(ctx, task.Task, task.OrganisationID)
		}
	}
	_, _ = s.store.FinalizeFinishedCampaigns(ctx, now)
}

func (s *Server) runTaskScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	s.processTaskQueue(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processTaskQueue(ctx)
		}
	}
}

func (s *Server) publishPendingTasksAfterSubscribe(topic string, actions []string) {
	if s.taskPublisher == nil || !mqttActionsInclude(actions, "subscribe") {
		return
	}

	organisationID, deviceID, ok := parseMQTTTaskTopic(topic)
	if !ok {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.taskPublisher.PublishPendingDeviceTasks(ctx, deviceID, organisationID)
	}()
}

func mqttActionsInclude(actions []string, needle string) bool {
	for _, action := range actions {
		if action == needle {
			return true
		}
	}
	return false
}

func mqttDataTopic(organisationID int64, deviceID string) string {
	return "dev/" + strconv.FormatInt(organisationID, 10) + "/" + deviceID + "/data"
}

func mqttTaskTopic(organisationID int64, deviceID string) string {
	return "dev/" + strconv.FormatInt(organisationID, 10) + "/" + deviceID + "/task"
}

func parseMQTTDataTopic(topic string) (int64, string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 || parts[0] != "dev" || parts[3] != "data" {
		return 0, "", false
	}

	organisationID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || organisationID <= 0 || parts[2] == "" {
		return 0, "", false
	}

	return organisationID, parts[2], true
}

func parseMQTTTaskTopic(topic string) (int64, string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 || parts[0] != "dev" || parts[3] != "task" {
		return 0, "", false
	}

	organisationID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || organisationID <= 0 || parts[2] == "" {
		return 0, "", false
	}

	return organisationID, parts[2], true
}

func mqttTopicFilterMatches(filter string, topic string) bool {
	if filter == "" || topic == "" {
		return false
	}

	filterParts := strings.Split(filter, "/")
	topicParts := strings.Split(topic, "/")

	for i, filterPart := range filterParts {
		if filterPart == "#" {
			return i == len(filterParts)-1
		}
		if i >= len(topicParts) {
			return false
		}
		if filterPart == "+" {
			continue
		}
		if strings.Contains(filterPart, "#") || strings.Contains(filterPart, "+") {
			return false
		}
		if filterPart != topicParts[i] {
			return false
		}
	}

	return len(filterParts) == len(topicParts)
}

package web

import (
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"unicode/utf8"

	"anchor/internal/db"

	"golang.org/x/crypto/bcrypt"
)

const (
	minUserPasswordLength = 8
	maxUserPasswordLength = 72
)

type settingsPageData struct {
	Shell           shellPageData
	ProfileName     string
	ProfileEmail    string
	ProfileError    string
	ProfileMessage  string
	PasswordError   string
	PasswordMessage string
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	data := settingsPageData{
		Shell:        shell,
		ProfileName:  shell.User.Name,
		ProfileEmail: shell.User.Email,
	}
	switch r.URL.Query().Get("saved") {
	case "profile":
		data.ProfileMessage = "Profile updated."
	case "password":
		data.PasswordMessage = "Password changed. Other sessions have been signed out."
	}
	s.renderSettings(w, data)
}

func (s *Server) settingsProfilePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	shell.SelectedOrganisationID = selectedOrganisationIDFromValue(r.FormValue("organisation_id"), shell.Organisations)

	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	data := settingsPageData{Shell: shell, ProfileName: name, ProfileEmail: email}
	if name == "" {
		data.ProfileError = "Display name is required."
		s.renderSettings(w, data)
		return
	}
	if !validUserEmail(email) {
		data.ProfileError = "Enter a valid email address."
		s.renderSettings(w, data)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(shell.User.PasswordHash), []byte(r.FormValue("current_password"))) != nil {
		data.ProfileError = "Current password is incorrect."
		s.renderSettings(w, data)
		return
	}

	if err := s.store.UpdateUserProfile(r.Context(), shell.User.ID, name, email); errors.Is(err, db.ErrConflict) {
		data.ProfileError = "That email address is already in use."
		s.renderSettings(w, data)
		return
	} else if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "profile update error", http.StatusInternalServerError)
		return
	}

	redirect(w, r, settingsURL("profile", shell.SelectedOrganisationID), http.StatusSeeOther)
}

func (s *Server) settingsPasswordPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	shell, ok := s.shellData(w, r)
	if !ok {
		return
	}
	shell.SelectedOrganisationID = selectedOrganisationIDFromValue(r.FormValue("organisation_id"), shell.Organisations)
	data := settingsPageData{Shell: shell, ProfileName: shell.User.Name, ProfileEmail: shell.User.Email}
	if bcrypt.CompareHashAndPassword([]byte(shell.User.PasswordHash), []byte(r.FormValue("current_password"))) != nil {
		data.PasswordError = "Current password is incorrect."
		s.renderSettings(w, data)
		return
	}

	newPassword := r.FormValue("new_password")
	if utf8.RuneCountInString(newPassword) < minUserPasswordLength {
		data.PasswordError = "New password must be at least 8 characters."
		s.renderSettings(w, data)
		return
	}
	if len([]byte(newPassword)) > maxUserPasswordLength {
		data.PasswordError = "New password must be at most 72 bytes."
		s.renderSettings(w, data)
		return
	}
	if newPassword != r.FormValue("confirm_password") {
		data.PasswordError = "New passwords do not match."
		s.renderSettings(w, data)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(shell.User.PasswordHash), []byte(newPassword)) == nil {
		data.PasswordError = "New password must be different from the current password."
		s.renderSettings(w, data)
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "password update error", http.StatusInternalServerError)
		return
	}
	keepSessionID := ""
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		keepSessionID = cookie.Value
	}
	if err := s.store.UpdateUserPassword(r.Context(), shell.User.ID, string(passwordHash), keepSessionID); errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "password update error", http.StatusInternalServerError)
		return
	}

	redirect(w, r, settingsURL("password", shell.SelectedOrganisationID), http.StatusSeeOther)
}

func validUserEmail(email string) bool {
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email && strings.Contains(email, "@")
}

func settingsURL(saved string, organisationID int64) string {
	target := "/settings?saved=" + saved
	if organisationID > 0 {
		target += "&organisation_id=" + strconv.FormatInt(organisationID, 10)
	}
	return target
}

func (s *Server) renderSettings(w http.ResponseWriter, data settingsPageData) {
	if err := s.render(w, http.StatusOK, data, "settings.html"); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

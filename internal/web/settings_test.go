package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

func TestSettingsProfilePostUpdatesNameAndEmail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, user := testSettingsUser(t, "member@example.com", "Member", "current-password")
	defer store.Close()
	if _, err := store.CreateUser(ctx, domain.User{
		Email:        "existing@example.com",
		Name:         "Existing",
		PasswordHash: user.PasswordHash,
	}); err != nil {
		t.Fatalf("create existing user: %v", err)
	}
	server := testServerWithTemplates(t, store)

	req := formRequest(http.MethodPost, "/settings/profile", url.Values{
		"name":             {"Updated Member"},
		"email":            {" Updated@Example.com "},
		"current_password": {"current-password"},
	}, user)
	res := httptest.NewRecorder()
	server.settingsProfilePost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected profile update redirect, got %d: %s", res.Code, res.Body.String())
	}
	if location := res.Header().Get("Location"); !strings.HasPrefix(location, "/settings?saved=profile") {
		t.Fatalf("unexpected profile redirect: %q", location)
	}
	updated, err := store.FindUserByEmail(ctx, "updated@example.com")
	if err != nil {
		t.Fatalf("find updated user: %v", err)
	}
	if updated.Name != "Updated Member" {
		t.Fatalf("expected updated display name, got %q", updated.Name)
	}

	conflictReq := formRequest(http.MethodPost, "/settings/profile", url.Values{
		"name":             {"Updated Member"},
		"email":            {"existing@example.com"},
		"current_password": {"current-password"},
	}, updated)
	conflictRes := httptest.NewRecorder()
	server.settingsProfilePost(conflictRes, conflictReq)
	if conflictRes.Code != http.StatusOK || !strings.Contains(conflictRes.Body.String(), "already in use") {
		t.Fatalf("expected duplicate email validation, got %d: %s", conflictRes.Code, conflictRes.Body.String())
	}
	if _, err := store.FindUserByEmail(ctx, "updated@example.com"); err != nil {
		t.Fatalf("expected original email after conflict: %v", err)
	}
}

func TestSettingsProfilePostRequiresCurrentPassword(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, user := testSettingsUser(t, "member@example.com", "Member", "current-password")
	defer store.Close()
	server := testServerWithTemplates(t, store)

	req := formRequest(http.MethodPost, "/settings/profile", url.Values{
		"name":             {"Changed"},
		"email":            {"changed@example.com"},
		"current_password": {"wrong-password"},
	}, user)
	res := httptest.NewRecorder()
	server.settingsProfilePost(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Current password is incorrect") {
		t.Fatalf("expected current password validation, got %d: %s", res.Code, res.Body.String())
	}
	if _, err := store.FindUserByEmail(ctx, "member@example.com"); err != nil {
		t.Fatalf("expected profile to remain unchanged: %v", err)
	}
}

func TestSettingsPasswordPostChangesPasswordAndRevokesOtherSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, user := testSettingsUser(t, "member@example.com", "Member", "current-password")
	defer store.Close()
	now := time.Now()
	for _, sessionID := range []string{"active-session", "other-session"} {
		if err := store.CreateSession(ctx, domain.Session{
			ID:        sessionID,
			UserID:    user.ID,
			ExpiresAt: now.Add(time.Hour).UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("create %s: %v", sessionID, err)
		}
	}
	server := testServerWithTemplates(t, store)

	req := formRequest(http.MethodPost, "/settings/password", url.Values{
		"current_password": {"current-password"},
		"new_password":     {"new-password-123"},
		"confirm_password": {"new-password-123"},
	}, user)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "active-session"})
	res := httptest.NewRecorder()
	server.settingsPasswordPost(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("expected password update redirect, got %d: %s", res.Code, res.Body.String())
	}

	updated, err := store.FindUserByEmail(ctx, user.Email)
	if err != nil {
		t.Fatalf("find updated user: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte("new-password-123")); err != nil {
		t.Fatalf("new password does not match stored hash: %v", err)
	}
	if _, err := store.UserBySession(ctx, "active-session", now); err != nil {
		t.Fatalf("active session should remain valid: %v", err)
	}
	if _, err := store.UserBySession(ctx, "other-session", now); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("other session should be revoked, got %v", err)
	}
}

func TestSettingsPasswordPostRejectsInvalidChange(t *testing.T) {
	t.Parallel()

	store, user := testSettingsUser(t, "member@example.com", "Member", "current-password")
	defer store.Close()
	server := testServerWithTemplates(t, store)

	tests := []struct {
		name     string
		current  string
		password string
		confirm  string
		message  string
	}{
		{name: "wrong current password", current: "wrong-password", password: "new-password-123", confirm: "new-password-123", message: "Current password is incorrect"},
		{name: "too short", current: "current-password", password: "short", confirm: "short", message: "at least 8 characters"},
		{name: "confirmation mismatch", current: "current-password", password: "new-password-123", confirm: "different-password", message: "do not match"},
		{name: "same password", current: "current-password", password: "current-password", confirm: "current-password", message: "must be different"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := formRequest(http.MethodPost, "/settings/password", url.Values{
				"current_password": {test.current},
				"new_password":     {test.password},
				"confirm_password": {test.confirm},
			}, user)
			res := httptest.NewRecorder()
			server.settingsPasswordPost(res, req)
			if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), test.message) {
				t.Fatalf("expected %q validation, got %d: %s", test.message, res.Code, res.Body.String())
			}
		})
	}
}

func testSettingsUser(t *testing.T, email string, name string, password string) (*db.Store, domain.User) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{
		Dialect: db.DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		store.Close()
		t.Fatalf("hash password: %v", err)
	}
	userID, err := store.CreateUser(ctx, domain.User{
		Email:        email,
		Name:         name,
		PasswordHash: string(passwordHash),
	})
	if err != nil {
		store.Close()
		t.Fatalf("create user: %v", err)
	}
	return store, domain.User{
		ID:           userID,
		Email:        email,
		Name:         name,
		PasswordHash: string(passwordHash),
	}
}

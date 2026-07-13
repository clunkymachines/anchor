package db

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"anchor/internal/domain"
)

func TestOrganisationAPICredentialLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{
		Dialect: DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}

	created, err := store.CreateOrganisationAPICredential(ctx, organisationID, "Simulator")
	if err != nil {
		t.Fatalf("create api credential: %v", err)
	}
	if !strings.HasPrefix(created.Token, "anc_org_") {
		t.Fatalf("expected prefixed token, got %q", created.Token)
	}
	if created.Credential.TokenHash == "" || strings.Contains(created.Credential.TokenHash, created.Token) {
		t.Fatalf("expected stored hash without plaintext token, got %#v", created.Credential)
	}

	credentials, err := store.ListOrganisationAPICredentials(ctx, organisationID)
	if err != nil {
		t.Fatalf("list api credentials: %v", err)
	}
	if len(credentials) != 1 || credentials[0].Name != "Simulator" || !credentials[0].Enabled {
		t.Fatalf("unexpected credentials: %#v", credentials)
	}

	authenticated, err := store.AuthenticateOrganisationAPIToken(ctx, created.Token, time.Unix(100, 0))
	if err != nil {
		t.Fatalf("authenticate api token: %v", err)
	}
	if authenticated.OrganisationID != organisationID || authenticated.LastUsedAt == "" {
		t.Fatalf("unexpected authenticated credential: %#v", authenticated)
	}

	if err := store.DisableOrganisationAPICredential(ctx, organisationID, created.Credential.ID); err != nil {
		t.Fatalf("disable api credential: %v", err)
	}
	if _, err := store.AuthenticateOrganisationAPIToken(ctx, created.Token, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected disabled token to fail auth, got %v", err)
	}

	rotated, err := store.RotateOrganisationAPICredential(ctx, organisationID, created.Credential.ID)
	if err != nil {
		t.Fatalf("rotate api credential: %v", err)
	}
	if rotated.Token == created.Token {
		t.Fatal("expected rotated token to change")
	}
	if _, err := store.AuthenticateOrganisationAPIToken(ctx, created.Token, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected old rotated token to fail auth, got %v", err)
	}
	if _, err := store.AuthenticateOrganisationAPIToken(ctx, rotated.Token, time.Now()); err != nil {
		t.Fatalf("authenticate rotated token: %v", err)
	}
}

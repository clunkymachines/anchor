package main

import (
	"context"
	"log/slog"

	"anchor/internal/db"
	"anchor/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

// ensureBootstrapData creates the default organisation and admin user needed for first startup.
func ensureBootstrapData(ctx context.Context, store *db.Store) error {
	organisationID, err := ensureDefaultOrganisation(ctx, store)
	if err != nil {
		return err
	}

	count, err := store.UserCount(ctx)
	if err != nil {
		return err
	}
	email := envOrDefault("ANCHOR_ADMIN_EMAIL", "admin@anchor.local")
	if count > 0 {
		user, err := store.FindUserByEmail(ctx, email)
		if err == nil {
			if !user.IsAdmin {
				if err := store.SetUserAdmin(ctx, user.ID, true); err != nil {
					return err
				}
				slog.Info("granted bootstrap admin access", "email", email)
			}
			if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
				UserID:         user.ID,
				OrganisationID: organisationID,
				Role:           db.OrganisationRoleAdmin,
			}); err != nil {
				return err
			}
		}
		return nil
	}

	name := envOrDefault("ANCHOR_ADMIN_NAME", "Anchor Admin")
	password := envOrDefault("ANCHOR_ADMIN_PASSWORD", "anchor")

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	userID, err := store.CreateUser(ctx, domain.User{
		Email:        email,
		Name:         name,
		PasswordHash: string(passwordHash),
		IsAdmin:      true,
	})
	if err != nil {
		return err
	}
	if err := store.AddUserToOrganisation(ctx, domain.OrganisationMembership{
		UserID:         userID,
		OrganisationID: organisationID,
		Role:           db.OrganisationRoleAdmin,
	}); err != nil {
		return err
	}

	slog.Info("created bootstrap admin user", "email", email)
	return nil
}

// ensureDefaultOrganisation returns an existing organisation or creates the configured default.
func ensureDefaultOrganisation(ctx context.Context, store *db.Store) (int64, error) {
	count, err := store.OrganisationCount(ctx)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		organisations, err := store.ListOrganisations(ctx)
		if err != nil {
			return 0, err
		}
		if len(organisations) == 0 {
			return 0, nil
		}
		return organisations[0].ID, nil
	}

	name := envOrDefault("ANCHOR_ORGANISATION_NAME", "Clunky Machines")
	id, err := store.CreateOrganisation(ctx, domain.Organisation{Name: name})
	if err != nil {
		return 0, err
	}

	slog.Info("created default organisation", "name", name)
	return id, nil
}

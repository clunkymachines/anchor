package main

import (
	"context"
	"log/slog"

	"anchor/internal/db"
	"anchor/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

// ensureBootstrapData creates the admin user needed for first startup.
func ensureBootstrapData(ctx context.Context, store *db.Store) error {
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
		}
		return nil
	}

	name := envOrDefault("ANCHOR_ADMIN_NAME", "Anchor Admin")
	password := envOrDefault("ANCHOR_ADMIN_PASSWORD", "anchor")

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	_, err = store.CreateUser(ctx, domain.User{
		Email:        email,
		Name:         name,
		PasswordHash: string(passwordHash),
		IsAdmin:      true,
	})
	if err != nil {
		return err
	}

	slog.Info("created bootstrap admin user", "email", email)
	return nil
}

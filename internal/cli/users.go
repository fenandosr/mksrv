// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fenandosr/mksrv/internal/keycloak"
	"github.com/fenandosr/mksrv/internal/ui"
)

func (a *App) newUsersCommand(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "users", Short: "Reconcile declarative tenant users"}
	cmd.AddCommand(&cobra.Command{
		Use:   "apply [ID...]",
		Short: "Create/update Keycloak users from tenants/<id>.users.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUsersApply(cmd.Context(), a.printer(opts), opts, args)
		},
	})
	return cmd
}

func (a *App) runUsersApply(ctx context.Context, printer ui.Printer, globals *globalOptions, args []string) error {
	f, err := a.openFleet(ctx, printer, globals)
	if err != nil {
		return err
	}
	if err := f.ensureSecrets(ctx); err != nil {
		return err
	}
	dep := f.data.Deployment

	adminPass, err := f.resolver.Get(ctx, "/mksrv/{env}/identity/keycloak_admin_password")
	if err != nil {
		return &ExitError{Code: 2, Err: fmt.Errorf("keycloak admin password: %w", err)}
	}
	kc := keycloak.New("https://" + dep.Identity.KeycloakDomain)
	if err := kc.Login(ctx, "admin", adminPass); err != nil {
		return &ExitError{Code: 1, Err: err}
	}

	selected, err := f.selectedTenants(args)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	for _, id := range selected {
		usersFile, ok := f.data.Users[id]
		if !ok || len(usersFile.Users) == 0 {
			printer.Info("tenant %s: no users file; skipping", id)
			continue
		}
		realm := tenantRealm(f.data.Tenants[id])
		specs := make([]keycloak.UserSpec, 0, len(usersFile.Users))
		for _, u := range usersFile.Users {
			enabled := true
			if u.Enabled != nil {
				enabled = *u.Enabled
			}
			specs = append(specs, keycloak.UserSpec{
				Email:   u.Email,
				Name:    u.Name,
				Groups:  u.Groups,
				Enabled: enabled,
			})
		}
		res, err := kc.EnsureUsers(ctx, realm, specs)
		if err != nil {
			return &ExitError{Code: 1, Err: fmt.Errorf("realm %s users: %w", realm, err)}
		}
		printer.Success("tenant %s: %d created, %d updated", id, len(res.Created), len(res.Updated))
	}
	return nil
}

// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/fenandosr/mksrv/internal/render"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
	"github.com/fenandosr/mksrv/internal/ui"
)

// themeName returns the Keycloak theme name for a tenant's login page, and the
// directory name under themes/. Namespaced so it never collides with a theme
// Keycloak ships itself. This MUST agree with three places: the realm's
// `loginTheme` (set from here via RealmSpec.LoginTheme), the theme dir mounted
// into the Keycloak container (keycloak.container.tmpl), and the mkdir in
// themeDirsCommand. A mismatch makes Keycloak fall back to the default theme
// with no error.
func themeName(id string) string { return "mksrv-" + id }

// themeFiles are the per-tenant login-theme templates, relative to
// stacks/identity/templates, paired with their destination on the identity
// host (the `%s` is filled with themeName(id), not the raw id). Mirrors
// stack.yaml's per_tenant entries (kept in sync manually — there is no
// per-tenant deploy path through DeployStack, see ADR 0023).
var themeFiles = []struct{ src, dst string }{
	{"login-theme/theme.properties.tmpl", "/var/lib/mksrv/stacks/identity/themes/%s/login/theme.properties"},
	{"login-theme/login.css.tmpl", "/var/lib/mksrv/stacks/identity/themes/%s/login/resources/css/login.css"},
}

// provisionTenantBranding renders each tenant's login theme (CSS + the logo
// embedded as the data URI already in Branding — no separate asset file) onto
// the identity host and restarts Keycloak once, unconditionally, if any
// tenant was processed (ADR 0023; mirrors reconcilePostgREST's own
// unconditional per-run restart rather than diffing).
func (f *fleet) provisionTenantBranding(ctx context.Context, printer ui.Printer, edgeClient *sshx.Client, tenants []string) error {
	edge := f.identityHost()
	if edge == nil || len(tenants) == 0 {
		return nil
	}
	rctxBase := f.renderContext(*edge)

	for _, id := range tenants {
		t := f.data.Tenants[id]
		rctx := rctxBase
		rctx.Tenant = &t
		for _, tf := range themeFiles {
			content, err := render.One(filepath.Join(f.stacksRoot, "identity", "templates", tf.src), rctx)
			if err != nil {
				return fmt.Errorf("tenant %s: render %s: %w", id, tf.src, err)
			}
			dst := fmt.Sprintf(tf.dst, themeName(id))
			if err := edgeClient.WriteFileSudo(ctx, dst, content, 0o644); err != nil {
				return fmt.Errorf("tenant %s: write %s: %w", id, dst, err)
			}
		}
	}

	if _, err := edgeClient.Run(ctx, "sudo systemctl restart mksrv-keycloak.service"); err != nil {
		return fmt.Errorf("restart keycloak for branding: %w", err)
	}
	printer.Success("keycloak restarted (%d tenant login theme(s) applied)", len(tenants))
	return nil
}

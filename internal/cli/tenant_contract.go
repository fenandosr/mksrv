// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	engineassets "github.com/fenandosr/mksrv"
	"github.com/fenandosr/mksrv/internal/keycloak"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/render"
	"github.com/fenandosr/mksrv/internal/workspace"
)

// `mksrv tenant contract` (ADR 0048): what a tenant's application sees of the
// fleet, derived offline from the workspace by the same functions `tenant apply`
// uses. It reads no credential and contacts nothing; every password and secret
// is a placeholder.

// contractVersion changes when a field is removed or changes meaning.
const contractVersion = 1

// Placeholders a consumer substitutes with its own local values.
const (
	contractPassword     = "__PASSWORD__"
	contractAuthPassword = "__AUTH_PASSWORD__"
	contractSecret       = "__SECRET__"
)

func contractServicePassword(role string) string { return "__PASSWORD_" + role + "__" }

type tenantContract struct {
	ContractVersion int    `json:"contract_version"`
	Engine          string `json:"engine"`
	Tenant          string `json:"tenant"`
	// Images maps stack -> app -> the image the catalog declares. What runs may
	// differ in a floating tag's patch level.
	Images   map[string]map[string]string `json:"images"`
	Database *contractDatabase            `json:"database,omitempty"`
	OpenBao  *contractOpenBao             `json:"openbao,omitempty"`
	// Secrets maps an entry under kv/tenants/<id>/ to its field names. Only the
	// entries mksrv writes; the tenant's own are not listed.
	Secrets   map[string][]string   `json:"secrets,omitempty"`
	Keycloak  contractKeycloak      `json:"keycloak"`
	Cache     *contractCache        `json:"cache,omitempty"`
	PostgREST *contractPostgREST    `json:"postgrest,omitempty"`
	StorageS3 *model.TenantS3Bucket `json:"storage_s3,omitempty"`
	// NotCovered names what a consumer must still take from the repository.
	NotCovered []string `json:"not_covered"`
}

type contractDatabase struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
	// GlobalSQL then TenantSQL are psql scripts (they use \gexec and \connect),
	// run as a superuser against the maintenance database.
	GlobalSQL string `json:"global_sql"`
	TenantSQL string `json:"tenant_sql"`
}

type contractOpenBao struct {
	KVMount  string                       `json:"kv_mount"`
	Policies map[string]string            `json:"policies"`
	AppRoles map[string]map[string]string `json:"approles"`
	// TransitKeyArgs are the `bao` arguments that create the tenant's key.
	TransitKeyArgs []string `json:"transit_key_args"`
	OIDCMount      string   `json:"oidc_mount"`
	// OIDCConfigArgs are the `bao` arguments that configure the mount.
	OIDCConfigArgs []string `json:"oidc_config_args"`
	// OIDCRoles are the bodies of auth/<oidc_mount>/role/<name>.
	OIDCRoles map[string]json.RawMessage `json:"oidc_roles"`
}

type contractClient struct {
	Representation map[string]any   `json:"representation"`
	Mappers        []map[string]any `json:"mappers"`
}

type contractKeycloak struct {
	Issuer string         `json:"issuer"`
	Realm  map[string]any `json:"realm"`
	// RealmUpdates are applied after creation: the password reset that goes
	// with SMTP, and any declared settings.
	RealmUpdates map[string]any   `json:"realm_updates"`
	Groups       []string         `json:"groups"`
	AdminGroup   string           `json:"admin_group"`
	Clients      []contractClient `json:"clients"`
}

type contractCache struct {
	ACL    string `json:"acl"`
	Config string `json:"config"`
}

type contractPostgREST struct {
	// Unit is the rendered Quadlet, with __PRIVATE_IP__ for the host address.
	Unit string `json:"unit"`
}

func (a *App) newTenantContractCommand(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "contract ID",
		Short: "Print what a tenant's application sees of the fleet, as JSON (offline, no secrets)",
		Long: "Print the SQL, OpenBao policies and roles, secret names, Keycloak realm and clients, Redis ACL,\n" +
			"PostgREST unit and images that `tenant apply` derives from one tenant, using the same code.\n" +
			"It reads only the workspace: no cloud, no SSH, no credential. Passwords are placeholders.\n" +
			"The output is always JSON.",
		Example:           "  mksrv tenant contract acme > acme.contract.json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.tenantIDsCompleter(opts, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runTenantContract(cmd.Context(), opts, args[0])
		},
	}
}

func (a *App) runTenantContract(ctx context.Context, globals *globalOptions, id string) error {
	root, err := workspace.Discover(defaultStartDirectory(), globals.Workspace)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return &ExitError{Code: 2, Err: fmt.Errorf("%w; pass --workspace", err)}
		}
		return err
	}
	data, report, err := workspace.Validate(ctx, root, workspace.ValidateOptions{
		RunningVersion: fallback(a.build.Version, "dev"),
		AllowDowngrade: globals.AllowDowngrade,
	})
	if err != nil {
		return err
	}
	if !report.Valid {
		return &ExitError{Code: 1, Err: fmt.Errorf("workspace is invalid; run mksrv validate")}
	}
	if _, ok := data.Tenants[id]; !ok {
		return &ExitError{Code: 2, Err: fmt.Errorf("unknown tenant %q", id)}
	}
	contract, err := buildTenantContract(data, fallback(a.build.Version, "dev"), id)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(a.stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(contract)
}

func buildTenantContract(data workspace.Data, engineVersion, id string) (tenantContract, error) {
	t := data.Tenants[id]
	uses := func(stack string) bool { return slices.Contains(t.Stacks, stack) }
	// A fleet with no infrastructure outputs and no clients: only what derives
	// from deployment.yaml may be asked of it.
	f := &fleet{data: data}
	cluster := false
	for _, h := range data.Deployment.Hosts {
		if slices.Contains(h.Stacks, "postgres") {
			cluster = true
		}
	}

	c := tenantContract{
		ContractVersion: contractVersion,
		Engine:          engineVersion,
		Tenant:          id,
		Images:          map[string]map[string]string{},
		NotCovered: []string{
			"the S3 IAM policy: infra/root/main.tf, aws_iam_user_policy.tenant_s3",
			"Keycloak's own default client scopes (email, profile), which come with its image",
			"edge, DNS, mail, mesh ACL and per-tenant files or monitoring services",
		},
	}

	stacks := append([]string{"identity"}, t.Stacks...)
	if uses("database") && cluster {
		stacks = append(stacks, "postgres")
		if f.leaderProxyEndpoint() != "" {
			stacks = append(stacks, "pgproxy")
		}
	}
	for _, name := range stacks {
		apps := map[string]string{}
		for _, app := range data.Catalog[name].Apps {
			apps[app.Name] = app.Image
		}
		if len(apps) > 0 {
			c.Images[name] = apps
		}
	}

	issuer := "https://" + data.Deployment.Identity.KeycloakDomain + "/realms/" + tenantRealm(t)
	c.Keycloak = keycloakContract(id, t, issuer)

	if uses("database") {
		servicePasswords := map[string]string{}
		for role := range t.DBServiceRoles() {
			servicePasswords[role] = contractServicePassword(role)
		}
		authPassword := ""
		if t.PostgRESTEnabled() {
			authPassword = contractAuthPassword
		}
		c.Database = &contractDatabase{
			Name:      "db_" + id,
			Schema:    t.DBSchema(),
			GlobalSQL: globalRBACRolesSQL(),
			TenantSQL: tenantDatabaseSQLWith(id, contractPassword, authPassword, servicePasswords, t),
		}
	}

	if uses("openbao") {
		c.OpenBao = openBaoContract(id, t, issuer)
		var pg postgresCluster
		if cluster {
			// Only the field NAMES are reported, so a stand-in node is enough.
			pg = postgresCluster{Nodes: []postgresNode{{Host: "node"}}, LeaderProxy: f.leaderProxyEndpoint()}
		}
		c.Secrets = secretsContract(pg, data.Deployment.Env, id, t)
	}

	if uses("cache") {
		conf, err := engineassets.FS.ReadFile("stacks/cache/templates/redis.conf.tmpl")
		if err != nil {
			return c, fmt.Errorf("read the redis configuration: %w", err)
		}
		c.Cache = &contractCache{ACL: redisACLLine(id, contractPassword), Config: string(conf)}
	}

	if uses("database") && t.PostgRESTEnabled() {
		unit, err := postgrestContractUnit(data, cluster, id, issuer)
		if err != nil {
			return c, err
		}
		c.PostgREST = &contractPostgREST{Unit: unit}
	}

	if t.Storage != nil {
		c.StorageS3 = t.Storage.S3
	}
	return c, nil
}

func keycloakContract(id string, t model.Tenant, issuer string) contractKeycloak {
	spec := tenantRealmSpec(id, t, nil)
	updates := keycloak.PasswordResetSettings()
	for k, v := range keycloak.SettingsRepresentation(spec.Settings) {
		updates[k] = v
	}
	out := contractKeycloak{
		Issuer:       issuer,
		Realm:        keycloak.RealmRepresentation(spec),
		RealmUpdates: updates,
		Groups:       spec.Groups,
		AdminGroup:   spec.AdminGroup,
	}
	for _, cs := range spec.Clients {
		mappers := keycloak.Mappers(cs)
		// These two also go through EnsureClient, which adds the audience mapper.
		openbao := cs.ClientID == openbaoClientID && slices.Contains(t.Stacks, "openbao")
		websso := t.WebSSO() && cs.ClientID == webSSOClientID(id)
		if openbao || websso {
			mappers = append(mappers, keycloak.AudienceMapper(cs.ClientID))
		}
		out.Clients = append(out.Clients, contractClient{Representation: keycloak.ClientRepresentation(cs), Mappers: mappers})
	}
	return out
}

// baoArgsMap turns the `key=value` arguments of a `bao write` into a map.
func baoArgsMap(args []string) map[string]string {
	out := map[string]string{}
	for _, a := range args {
		if k, v, ok := strings.Cut(a, "="); ok {
			out[k] = v
		}
	}
	return out
}

func openBaoContract(id string, t model.Tenant, issuer string) *contractOpenBao {
	tenantRole, svcRole := "tenant-"+id, "svc-"+id
	o := &contractOpenBao{
		KVMount: "kv",
		Policies: map[string]string{
			tenantRole + "-admin": tenantAdminPolicyHCL(id),
			tenantRole + "-dev":   tenantDevPolicyHCL(id),
			svcRole:               tenantServicePolicyHCL(id),
		},
		AppRoles: map[string]map[string]string{
			tenantRole: baoArgsMap(unboundAppRoleArgs(tenantRole, tenantRole+"-dev")),
			svcRole:    baoArgsMap(unboundAppRoleArgs(svcRole, svcRole)),
		},
		TransitKeyArgs: transitKeyArgs(id),
		OIDCMount:      "oidc-" + id,
		OIDCConfigArgs: oidcConfigArgs(id, issuer, contractSecret),
		OIDCRoles:      map[string]json.RawMessage{},
	}
	for _, r := range oidcGroupRoles(id) {
		o.OIDCRoles[tenantRole+"-"+r.suffix] = oidcGroupRolePayload(r.groups, r.policy)
	}
	if t.Storage != nil {
		for _, r := range t.Storage.AppRoles {
			name := r.AppRoleName(id)
			o.Policies[name] = tenantAppRolePolicyHCL(id, r)
			o.AppRoles[name] = baoArgsMap(tenantAppRoleWriteArgs(id, r))
		}
	}
	return o
}

func fieldNames(fields []string) []string {
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		name, _, _ := strings.Cut(f, "=")
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func secretsContract(pg postgresCluster, env, id string, t model.Tenant) map[string][]string {
	out := map[string][]string{}
	if slices.Contains(t.Stacks, "database") {
		out["database"] = fieldNames(tenantDBSecretFields(pg, env, id, contractPassword))
		for role := range t.DBServiceRoles() {
			out["database-"+role] = fieldNames(dbSecretFields(pg, env, id, role, contractPassword))
		}
		if t.PublishesAuthenticator() {
			out[authenticatorSecretName(id)] = fieldNames(dbSecretFields(pg, env, id, id+"_auth", contractPassword))
		}
	}
	if slices.Contains(t.Stacks, "cache") {
		out["cache"] = fieldNames(tenantCacheSecretFields("", id, contractPassword))
	}
	if t.Storage != nil && t.Storage.S3 != nil {
		var doc map[string]string
		// storageSecretJSON marshals a map of strings, so it always decodes.
		_ = json.Unmarshal(storageSecretJSON(contractSecret, contractSecret), &doc)
		out[storageSecretName] = sortedKeys(doc)
	}
	return out
}

func postgrestContractUnit(data workspace.Data, cluster bool, id, issuer string) (string, error) {
	var existing map[string]int
	if data.Lock != nil {
		existing = data.Lock.PostgRESTPorts
	}
	// Computed, never persisted: this command writes nothing to the workspace.
	ports, err := assignPostgRESTPorts(data.Tenants, existing)
	if err != nil {
		return "", err
	}
	body, err := engineassets.FS.ReadFile("stacks/database/templates/postgrest.container.tmpl")
	if err != nil {
		return "", fmt.Errorf("read the PostgREST unit: %w", err)
	}
	tenant := data.Tenants[id]
	rctx := render.Context{
		Env:    data.Deployment.Env,
		Tenant: &tenant,
		Host:   render.HostView{PrivateIP: "__PRIVATE_IP__"},
		Images: map[string]string{},
		Secrets: map[string]string{
			"listen_port": strconv.Itoa(ports[id]),
			"rest_fqdn":   id + ".rest." + data.Deployment.DNS.RootDomain,
			"oidc_issuer": issuer,
		},
		StackHosts: map[string]string{},
	}
	for _, app := range data.Catalog["database"].Apps {
		rctx.Images[app.Name] = app.Image
	}
	if cluster {
		rctx.StackHosts["postgres"] = "__POSTGRES__"
	}
	unit, err := render.Text("postgrest.container.tmpl", string(body), rctx)
	if err != nil {
		return "", err
	}
	return string(unit), nil
}

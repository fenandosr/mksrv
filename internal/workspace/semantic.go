// SPDX-License-Identifier: Apache-2.0

package workspace

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fenandosr/mksrv/internal/model"
)

// normalizeImplicitStacks appends stacks a fleet needs implicitly rather than
// by explicit per-host declaration (M23). Currently just `agent`
// (node-exporter + cAdvisor): once any host carries `monitor`, every host
// gets `agent` so the central Prometheus has a full fleet view without the
// operator listing it on every host. Runs before semanticChecks so capacity
// and dependency checks see the effective stack list.
func normalizeImplicitStacks(deployment *model.Deployment) {
	hasMonitor := false
	for _, host := range deployment.Hosts {
		if contains(host.Stacks, "monitor") {
			hasMonitor = true
			break
		}
	}
	if !hasMonitor {
		return
	}
	for name, host := range deployment.Hosts {
		if !contains(host.Stacks, "agent") {
			host.Stacks = append([]string{"agent"}, host.Stacks...)
			deployment.Hosts[name] = host
		}
	}
}

func semanticChecks(data *Data, report *Report, options ValidateOptions) {
	deployment := data.Deployment
	if deployment.Timezone == "" {
		deployment.Timezone = "Etc/UTC"
	}
	if _, err := time.LoadLocation(deployment.Timezone); err != nil {
		semanticError(report, "deployment.yaml", "$.timezone", "timezone.invalid", fmt.Sprintf("%q is not an installed IANA timezone", deployment.Timezone))
	}
	if deployment.MgmtCIDR != "auto" {
		if _, _, err := net.ParseCIDR(deployment.MgmtCIDR); err != nil {
			semanticError(report, "deployment.yaml", "$.mgmt_cidr", "cidr.invalid", fmt.Sprintf("invalid management CIDR: %v", err))
		}
	}

	checkEngineVersions(data, report, options)
	checkDomainWithinRoot(report, "deployment.yaml", "$.identity.keycloak_domain", deployment.Identity.KeycloakDomain, deployment.DNS.RootDomain)
	checkDomainWithinRoot(report, "deployment.yaml", "$.identity.headscale_domain", deployment.Identity.HeadscaleDomain, deployment.DNS.RootDomain)

	assigned := make(map[string][]string)
	baseHosts := make([]string, 0)
	identityHosts := make([]string, 0)
	hostNames := make([]string, 0, len(deployment.Hosts))
	deploymentHostNames := make(map[string]bool, len(deployment.Hosts))
	for hostName := range deployment.Hosts {
		hostNames = append(hostNames, hostName)
		deploymentHostNames[strings.ToLower(hostName)] = true
	}
	sort.Strings(hostNames)
	for _, hostName := range hostNames {
		host := deployment.Hosts[hostName]
		target := "cloud"
		if host.Provider == "existing" {
			target = "local"
		}
		for index, stackName := range host.Stacks {
			stack, exists := data.Catalog[stackName]
			stackPath := fmt.Sprintf("$.hosts.%s.stacks[%d]", hostName, index)
			if !exists {
				semanticError(report, "deployment.yaml", stackPath, "stack.unknown", fmt.Sprintf("stack %q is not present in the embedded catalog", stackName))
				continue
			}
			assigned[stackName] = append(assigned[stackName], hostName)
			if !contains(stack.Targets, target) {
				semanticError(report, "deployment.yaml", stackPath, "stack.target", fmt.Sprintf("stack %q cannot run on %s host %q; allowed targets: %s", stackName, host.Provider, hostName, strings.Join(stack.Targets, ", ")))
			}
			if stack.LocalHostsOnly {
				semanticError(report, "deployment.yaml", stackPath, "stack.local_hosts_only", fmt.Sprintf("stack %q is only usable via a tenant's local_hosts (ADR 0034), not deployment.yaml", stackName))
			}
			if stackName == "base" {
				baseHosts = append(baseHosts, hostName)
			}
			if stackName == "identity" {
				identityHosts = append(identityHosts, hostName)
			}
		}
		checkHostCapacity(data, report, hostName, host)
		checkHostStorage(data, report, hostName, host)
	}
	if len(baseHosts) != 1 {
		semanticError(report, "deployment.yaml", "$.hosts", "stack.base.count", fmt.Sprintf("exactly one host must carry base; found %d (%s)", len(baseHosts), strings.Join(baseHosts, ", ")))
	}
	if len(identityHosts) != 1 {
		semanticError(report, "deployment.yaml", "$.hosts", "stack.identity.count", fmt.Sprintf("exactly one host must carry identity; found %d (%s)", len(identityHosts), strings.Join(identityHosts, ", ")))
	}
	clusterStacks := make([]string, 0, len(assigned))
	for stackName := range assigned {
		if data.Catalog[stackName].Kind == "cluster" {
			clusterStacks = append(clusterStacks, stackName)
		}
	}
	sort.Strings(clusterStacks)
	for _, stackName := range clusterStacks {
		n := len(assigned[stackName])
		if n < 3 || n%2 == 0 {
			semanticError(report, "deployment.yaml", "$.hosts", "stack.cluster.count", fmt.Sprintf("cluster stack %q needs an odd number of hosts >= 3; found %d (%s)", stackName, n, strings.Join(assigned[stackName], ", ")))
		}
	}
	for stackName := range assigned {
		for _, dependency := range data.Catalog[stackName].DependsOn {
			if len(assigned[dependency]) == 0 {
				semanticError(report, "deployment.yaml", "$.hosts", "stack.dependency", fmt.Sprintf("assigned stack %q depends on %q, but %q is not assigned anywhere", stackName, dependency, dependency))
			}
		}
	}
	checkCatalogCycles(data.Catalog, report)

	realms := make(map[string]string)
	seenLocalHostNames := make(map[string]string)
	tenantIDs := make([]string, 0, len(data.Tenants))
	for tenantID := range data.Tenants {
		tenantIDs = append(tenantIDs, tenantID)
	}
	sort.Strings(tenantIDs)
	for _, tenantID := range tenantIDs {
		tenant := data.Tenants[tenantID]
		tenantFile := data.TenantFiles[tenantID]
		fileID := strings.TrimSuffix(filepath.Base(tenantFile), ".yaml")
		if tenant.ID != fileID {
			semanticError(report, tenantFile, "$.id", "tenant.filename", fmt.Sprintf("tenant id %q must match filename %q", tenant.ID, filepath.Base(tenantFile)))
		}
		if tenant.DNSOverride == nil {
			checkDomainWithinRoot(report, tenantFile, "$.base_domain", tenant.BaseDomain, deployment.DNS.RootDomain)
		}
		realm := tenant.Keycloak.Realm
		if realm == "" {
			realm = tenant.ID
		}
		if owner, exists := realms[realm]; exists {
			semanticError(report, tenantFile, "$.keycloak.realm", "tenant.realm.duplicate", fmt.Sprintf("realm %q is already used by tenant %q", realm, owner))
		} else {
			realms[realm] = tenant.ID
		}
		for index, stackName := range tenant.Stacks {
			stackPath := fmt.Sprintf("$.stacks[%d]", index)
			stack, exists := data.Catalog[stackName]
			if !exists {
				semanticError(report, tenantFile, stackPath, "stack.unknown", fmt.Sprintf("stack %q is not present in the embedded catalog", stackName))
				continue
			}
			if !stack.PerTenant {
				semanticError(report, tenantFile, stackPath, "tenant.stack.scope", fmt.Sprintf("stack %q is not tenant-consumable", stackName))
			}
			if len(assigned[stackName]) == 0 {
				semanticError(report, tenantFile, stackPath, "tenant.stack.unassigned", fmt.Sprintf("tenant consumes %q, but no host carries that stack", stackName))
			}
		}
		checkTenantForwards(report, tenantFile, tenant)
		checkTenantDNS(report, tenantFile, tenant)
		checkTenantMeshRoutes(report, tenantFile, tenant)
		checkTenantWeb(report, tenantFile, tenant)
		checkTenantDatabase(report, tenantFile, tenant)
		checkTenantRoles(report, tenantFile, tenant, data.Tenants)
		checkTenantKeycloak(report, tenantFile, tenant)
		checkTenantMail(report, tenantFile, tenant, len(assigned["mail"]) > 0)
		checkTenantLocalHosts(report, tenantFile, tenant, data.Catalog, seenLocalHostNames, deploymentHostNames)
	}
	checkMultiInstanceStacks(data.Catalog, report)

	for tenantID, users := range data.Users {
		usersFile := data.UsersFiles[tenantID]
		fileTenant := strings.TrimSuffix(filepath.Base(usersFile), ".users.yaml")
		if users.Tenant != fileTenant {
			semanticError(report, usersFile, "$.tenant", "users.filename", fmt.Sprintf("tenant %q must match filename %q", users.Tenant, filepath.Base(usersFile)))
		}
		if _, exists := data.Tenants[tenantID]; !exists {
			semanticError(report, usersFile, "$.tenant", "users.tenant.missing", fmt.Sprintf("users file refers to unknown tenant %q", tenantID))
		}
		seenEmails := make(map[string]int)
		for index, user := range users.Users {
			normalized := strings.ToLower(strings.TrimSpace(user.Email))
			if first, exists := seenEmails[normalized]; exists {
				semanticError(report, usersFile, fmt.Sprintf("$.users[%d].email", index), "users.email.duplicate", fmt.Sprintf("email duplicates users[%d]", first))
			} else {
				seenEmails[normalized] = index
			}
			if t, ok := data.Tenants[tenantID]; ok {
				for _, g := range user.Groups {
					if !contains(t.KeycloakGroups(), g) {
						semanticError(report, usersFile, fmt.Sprintf("$.users[%d].groups", index), "users.group.unknown", fmt.Sprintf("group %q is not a realm group of tenant %q (%s)", g, tenantID, strings.Join(t.KeycloakGroups(), "|")))
					}
				}
			}
		}
	}
}

func checkEngineVersions(data *Data, report *Report, options ValidateOptions) {
	running := strings.TrimSpace(options.RunningVersion)
	declared := strings.TrimSpace(data.Deployment.Engine)
	if running != "" && running != "dev" && declared != "" && declared != "dev" {
		comparison, err := compareSemver(running, declared)
		if err != nil {
			semanticError(report, "deployment.yaml", "$.engine", "engine.version", err.Error())
		} else if comparison < 0 && !options.AllowDowngrade {
			semanticError(report, "deployment.yaml", "$.engine", "engine.downgrade", fmt.Sprintf("workspace requires engine %s but running binary is older (%s); use --allow-downgrade only for deliberate recovery", declared, running))
		} else if comparison > 0 {
			addIssue(report, Issue{Severity: "warning", File: "deployment.yaml", Path: "$.engine", Code: "engine.upgrade", Message: fmt.Sprintf("running binary %s is newer than workspace engine %s; a successful apply will update mksrv.lock", running, declared)})
		}
	}
	if data.Lock != nil {
		if strings.TrimSpace(data.Lock.Engine) == "" {
			semanticError(report, "mksrv.lock", "$.engine", "lock.engine.empty", "lock engine version must not be empty")
		} else if declared != "" && data.Lock.Engine != declared {
			addIssue(report, Issue{Severity: "warning", File: "mksrv.lock", Path: "$.engine", Code: "lock.engine.drift", Message: fmt.Sprintf("lock records %s while deployment declares %s", data.Lock.Engine, declared)})
		}
	}
}

func checkDomainWithinRoot(report *Report, file, valuePath, domain, root string) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	root = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(root)), ".")
	if domain == "" || root == "" {
		return
	}
	if domain != root && !strings.HasSuffix(domain, "."+root) {
		semanticError(report, file, valuePath, "domain.outside_root", fmt.Sprintf("%q must equal or be a subdomain of %q; set dns_override to use an independent apex domain", domain, root))
	}
}

// reservedForwardIDs are the forward ids builtinForwards emits for every
// tenant; a tenant forward may not collide with them.
var reservedForwardIDs = map[string]bool{
	"edge-health": true, "database": true, "rest": true, "cache": true, "openbao": true,
}

func checkTenantForwards(report *Report, file string, tenant model.Tenant) {
	seen := make(map[string]int, len(tenant.Forwards))
	for i, fwd := range tenant.Forwards {
		path := fmt.Sprintf("$.forwards[%d]", i)
		if reservedForwardIDs[fwd.ID] {
			semanticError(report, file, path+".id", "forward.reserved", fmt.Sprintf("forward id %q is reserved by mksrv", fwd.ID))
		}
		if first, dup := seen[fwd.ID]; dup {
			semanticError(report, file, path+".id", "forward.duplicate", fmt.Sprintf("forward id %q duplicates forwards[%d]", fwd.ID, first))
		} else {
			seen[fwd.ID] = i
		}
		if _, _, err := net.SplitHostPort(fwd.Target); err != nil {
			semanticError(report, file, path+".target", "forward.target", fmt.Sprintf("target %q must be host:port", fwd.Target))
		}
		if fwd.Type == "ssh" && strings.TrimSpace(fwd.SSHAlias) == "" {
			semanticError(report, file, path+".ssh_alias", "forward.ssh_alias", "an ssh forward requires ssh_alias")
		}
	}
	// builtinForwards emits at most 5 built-in forwards; Cloud-IT VPN caps at 32.
	if len(tenant.Forwards)+5 > 32 {
		semanticError(report, file, "$.forwards", "forward.count", fmt.Sprintf("%d tenant forwards plus mksrv's built-ins exceed the 32-forward limit", len(tenant.Forwards)))
	}
}

func checkTenantDNS(report *Report, file string, tenant model.Tenant) {
	if len(tenant.DNS) == 0 {
		return
	}
	if tenant.DNSOverride == nil || tenant.DNSOverride.Provider != "route53" || strings.TrimSpace(tenant.DNSOverride.ZoneID) == "" {
		semanticError(report, file, "$.dns", "tenant.dns.no_zone", "dns records require dns_override with provider route53 and a zone_id")
		return
	}
	for i, rec := range tenant.DNS {
		path := fmt.Sprintf("$.dns[%d]", i)
		switch rec.Type {
		case "A", "AAAA":
			if net.ParseIP(rec.Value) == nil {
				semanticError(report, file, path+".value", "tenant.dns.value", fmt.Sprintf("%s record value %q is not an IP address", rec.Type, rec.Value))
			}
		case "CNAME":
			if net.ParseIP(rec.Value) != nil {
				semanticError(report, file, path+".value", "tenant.dns.value", "CNAME record value must be a hostname, not an IP")
			}
		}
	}
}

func checkTenantMeshRoutes(report *Report, file string, tenant model.Tenant) {
	for i, route := range tenant.MeshRoutes {
		if _, _, err := net.ParseCIDR(route); err != nil {
			semanticError(report, file, fmt.Sprintf("$.mesh_routes[%d]", i), "tenant.mesh_route", fmt.Sprintf("%q is not a valid CIDR", route))
		}
	}
}

// checkTenantWeb validates the `web:` block (ADR 0028): each hostname must be in
// the tenant's own apex, needs a route53 dns_override, must not clash with a
// `dns:` record, and `cdn: true` is not implemented yet.
func checkTenantWeb(report *Report, file string, tenant model.Tenant) {
	if len(tenant.Web) == 0 {
		return
	}
	if tenant.DNSOverride == nil || tenant.DNSOverride.Provider != "route53" || strings.TrimSpace(tenant.DNSOverride.ZoneID) == "" {
		semanticError(report, file, "$.web", "tenant.web.no_zone", "web endpoints require dns_override with provider route53 and a zone_id")
		return
	}
	dnsNames := make(map[string]bool, len(tenant.DNS))
	for _, r := range tenant.DNS {
		if r.Name == "@" {
			dnsNames[strings.ToLower(tenant.BaseDomain)] = true
		} else {
			dnsNames[strings.ToLower(r.Name+"."+tenant.BaseDomain)] = true
		}
	}
	seen := make(map[string]int, len(tenant.Web))
	for i, w := range tenant.Web {
		path := fmt.Sprintf("$.web[%d]", i)
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(w.Hostname)), ".")
		root := strings.TrimSuffix(strings.ToLower(tenant.BaseDomain), ".")
		if host != root && !strings.HasSuffix(host, "."+root) {
			semanticError(report, file, path+".hostname", "tenant.web.outside_apex", fmt.Sprintf("%q must equal or be a subdomain of the tenant's base_domain %q", w.Hostname, tenant.BaseDomain))
		}
		if first, dup := seen[host]; dup {
			semanticError(report, file, path+".hostname", "tenant.web.duplicate", fmt.Sprintf("hostname %q duplicates web[%d]", w.Hostname, first))
		} else {
			seen[host] = i
		}
		if dnsNames[host] {
			semanticError(report, file, path+".hostname", "tenant.web.dns_clash", fmt.Sprintf("hostname %q is also declared as a dns record", w.Hostname))
		}
		if _, _, err := net.SplitHostPort(w.Target); err != nil {
			semanticError(report, file, path+".target", "tenant.web.target", fmt.Sprintf("target %q must be host:port", w.Target))
		}
		if w.Provider != "" && w.Provider != "edge" {
			semanticError(report, file, path+".provider", "tenant.web.provider", fmt.Sprintf("provider %q is not supported (only \"edge\")", w.Provider))
		}
		if w.CDN {
			semanticError(report, file, path+".cdn", "tenant.web.cdn_unimplemented", "cdn is not implemented yet — omit it or set false")
		}
		if w.SSO && w.CDN {
			semanticError(report, file, path+".sso", "tenant.web.sso_cdn", "sso and cdn cannot be combined yet")
		}
		if len(w.SSOGroups) > 0 && !w.SSO {
			semanticError(report, file, path+".sso_groups", "tenant.web.sso_groups", "sso_groups requires sso: true")
		}
		for _, g := range w.SSOGroups {
			if !contains(tenant.KeycloakGroups(), g) {
				semanticError(report, file, path+".sso_groups", "tenant.web.sso_groups", fmt.Sprintf("unknown realm group %q (%s)", g, strings.Join(tenant.KeycloakGroups(), "|")))
			}
		}
		if len(w.SSOBypassPaths) > 0 && !w.SSO {
			semanticError(report, file, path+".sso_bypass_paths", "tenant.web.sso_bypass_paths", "sso_bypass_paths requires sso: true")
		}
	}
}

// checkTenantMail validates the `mail:` block (ADR 0032): domains require a
// route53 dns_override (mksrv writes MX/SPF/DMARC/DKIM there), every mailbox
// address must be under a declared domain, and some host in the fleet must
// actually carry the `mail` stack or there's nowhere to reconcile against.
func checkTenantMail(report *Report, file string, tenant model.Tenant, mailStackAssigned bool) {
	if tenant.Mail == nil || (len(tenant.Mail.Domains) == 0 && len(tenant.Mail.Mailboxes) == 0) {
		return
	}
	if len(tenant.Mail.Domains) == 0 {
		semanticError(report, file, "$.mail.mailboxes", "tenant.mail.no_domain", "mailboxes require at least one domain in mail.domains")
		return
	}
	if tenant.DNSOverride == nil || tenant.DNSOverride.Provider != "route53" || strings.TrimSpace(tenant.DNSOverride.ZoneID) == "" {
		semanticError(report, file, "$.mail", "tenant.mail.no_zone", "mail requires dns_override with provider route53 and a zone_id")
		return
	}
	if !mailStackAssigned {
		semanticError(report, file, "$.mail", "tenant.mail.no_stack", "tenant declares mail but no host in the fleet carries the `mail` stack")
	}
	domains := make(map[string]bool, len(tenant.Mail.Domains))
	for _, d := range tenant.Mail.Domains {
		domains[strings.ToLower(strings.TrimSuffix(d, "."))] = true
	}
	seen := make(map[string]int, len(tenant.Mail.Mailboxes))
	for i, mb := range tenant.Mail.Mailboxes {
		path := fmt.Sprintf("$.mail.mailboxes[%d]", i)
		addr := strings.ToLower(strings.TrimSpace(mb.Address))
		at := strings.LastIndexByte(addr, '@')
		if at <= 0 || at == len(addr)-1 {
			semanticError(report, file, path+".address", "tenant.mail.address", fmt.Sprintf("%q is not a valid mailbox address", mb.Address))
			continue
		}
		if !domains[addr[at+1:]] {
			semanticError(report, file, path+".address", "tenant.mail.domain", fmt.Sprintf("mailbox domain %q is not in mail.domains", addr[at+1:]))
		}
		if first, dup := seen[addr]; dup {
			semanticError(report, file, path+".address", "tenant.mail.duplicate", fmt.Sprintf("address %q duplicates mailboxes[%d]", mb.Address, first))
		} else {
			seen[addr] = i
		}
	}
}

// allowedDBExtensions are the contrib extensions a tenant may request in its
// `database.extensions` block (ADR 0029) — all bundled with the postgres image,
// none introduce an untrusted procedural language or a non-standard image need.
var allowedDBExtensions = map[string]bool{
	"pgcrypto": true, "uuid-ossp": true, "citext": true, "pg_trgm": true,
	"btree_gist": true, "btree_gin": true, "hstore": true, "unaccent": true,
	"ltree": true, "intarray": true, "tablefunc": true, "fuzzystrmatch": true,
}

// reservedSchemas can't be used as a tenant's application schema.
var reservedSchemas = map[string]bool{
	"public": true, "pg_catalog": true, "information_schema": true, "pg_toast": true,
}

func checkTenantDatabase(report *Report, file string, tenant model.Tenant) {
	if tenant.Database == nil {
		return
	}
	if !contains(tenant.Stacks, "database") {
		semanticError(report, file, "$.database", "tenant.database.no_stack", "a database block requires the tenant to consume the `database` stack")
		return
	}
	if s := tenant.Database.Schema; s != "" {
		if reservedSchemas[s] || strings.HasPrefix(s, "pg_") {
			semanticError(report, file, "$.database.schema", "tenant.database.schema", fmt.Sprintf("schema %q is reserved", s))
		}
	}
	for i, ext := range tenant.Database.Extensions {
		if !allowedDBExtensions[ext] {
			semanticError(report, file, fmt.Sprintf("$.database.extensions[%d]", i), "tenant.database.extension", fmt.Sprintf("extension %q is not in the allow-list", ext))
		}
	}
	pg := tenant.PostgRESTSettings()
	for i, s := range pg.Schemas {
		if reservedSchemas[s] || strings.HasPrefix(s, "pg_") || s == "mksrv" {
			semanticError(report, file, fmt.Sprintf("$.database.postgrest.schemas[%d]", i), "tenant.database.schema", fmt.Sprintf("schema %q is reserved", s))
		}
	}
	if hook := pg.PreRequestHook; hook != "" {
		sch, _, _ := strings.Cut(hook, ".")
		if reservedSchemas[sch] || strings.HasPrefix(sch, "pg_") || sch == "mksrv" {
			semanticError(report, file, "$.database.postgrest.pre_request_hook", "tenant.database.hook_name", fmt.Sprintf("hook schema %q is reserved", sch))
		}
	}
	for name, r := range tenant.Database.Roles {
		if len(r.When) > 0 {
			semanticWarning(report, file, "$.database.roles."+name+".when", "tenant.database.rbac_shadow", "roles with `when` are declared: the admin/dev -> mksrv_owner shortcut is disabled for PostgREST (declare it explicitly if needed)")
			break
		}
	}
	if !tenant.PostgRESTEnabled() && tenant.Database.Schema != "" {
		semanticWarning(report, file, "$.database.schema", "tenant.database.schema_unused", "schema is set but postgrest is disabled — it only names the tenant's application schema")
	}
}

// checkTenantLocalHosts validates a tenant's local_hosts (ADR 0034) — a
// third host category alongside cloud and `existing` hosts: no SSH, no
// cloud provisioning, reached only by name (`mksrv deploy <name>` / `mksrv
// mesh`, run on that machine itself). seen is threaded across every
// tenant's local_hosts, same shape as the realms map above: a local host's
// name doubles as its real machine hostname, so it must be globally unique
// — including against deploymentHostNames (deployment.yaml's own host
// names, lower-cased): the hostname guard compares case-insensitively, and
// a collision would silently steal that cloud host's entry in fleet.byName.
func checkTenantLocalHosts(report *Report, file string, tenant model.Tenant, catalog map[string]model.Stack, seen map[string]string, deploymentHostNames map[string]bool) {
	names := make([]string, 0, len(tenant.LocalHosts))
	for name := range tenant.LocalHosts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := tenant.LocalHosts[name]
		path := fmt.Sprintf("$.local_hosts.%s", name)
		if owner, dup := seen[name]; dup {
			semanticError(report, file, path, "tenant.local_host.duplicate", fmt.Sprintf("local host name %q is already used by tenant %q — it doubles as the machine's real hostname and must be globally unique", name, owner))
		} else {
			seen[name] = tenant.ID
		}
		if deploymentHostNames[strings.ToLower(name)] {
			semanticError(report, file, path, "tenant.local_host.collides_with_deployment_host", fmt.Sprintf("local host name %q collides with a deployment.yaml host of the same name", name))
		}

		var meshInstances []string
		if entry.Meshnode != nil {
			for _, inst := range entry.Meshnode.Instances {
				meshInstances = append(meshInstances, inst.Name)
			}
		}

		for i, stackName := range entry.Stacks {
			stackPath := fmt.Sprintf("%s.stacks[%d]", path, i)
			stack, exists := catalog[stackName]
			if !exists {
				semanticError(report, file, stackPath, "stack.unknown", fmt.Sprintf("stack %q is not present in the embedded catalog", stackName))
				continue
			}
			// Deliberately stricter than the cloud per-host check's
			// `contains(stack.Targets, target)`: `local` in Targets alone
			// also matches a cloud-fleet stack like `database`/`cache`
			// (their Targets include "local" for deployment.yaml's
			// `existing`-provider hosts, a different meaning of the same
			// word). Only a stack actually built for Context.LocalHost
			// (LocalHostsOnly) belongs here.
			if !stack.LocalHostsOnly {
				semanticError(report, file, stackPath, "stack.local_hosts_only", fmt.Sprintf("stack %q is not usable via local_hosts (missing local_hosts_only); allowed targets here: %s", stackName, strings.Join(stack.Targets, ", ")))
			}
			for _, dep := range stack.DependsOn {
				if !contains(entry.Stacks, dep) {
					semanticError(report, file, stackPath, "tenant.local_host.dependency", fmt.Sprintf("stack %q depends on %q, but %q is not in this local host's own stacks", stackName, dep, dep))
				}
			}
		}

		if contains(entry.Stacks, "meshnode") && len(meshInstances) == 0 {
			semanticError(report, file, path+".meshnode", "tenant.local_host.meshnode_missing", `stack "meshnode" requires a meshnode.instances block`)
		}
		if contains(entry.Stacks, "gitea") {
			if entry.Gitea == nil {
				semanticError(report, file, path+".gitea", "tenant.local_host.gitea_missing", `stack "gitea" requires a gitea.mesh block`)
			} else if !contains(meshInstances, entry.Gitea.Mesh) {
				semanticError(report, file, path+".gitea.mesh", "tenant.local_host.mesh", fmt.Sprintf("gitea.mesh %q does not match any meshnode.instances[].name", entry.Gitea.Mesh))
			}
		}
		if contains(entry.Stacks, "codeserver") {
			if entry.Codeserver == nil {
				semanticError(report, file, path+".codeserver", "tenant.local_host.codeserver_missing", `stack "codeserver" requires a codeserver.mesh block`)
			} else if !contains(meshInstances, entry.Codeserver.Mesh) {
				semanticError(report, file, path+".codeserver.mesh", "tenant.local_host.mesh", fmt.Sprintf("codeserver.mesh %q does not match any meshnode.instances[].name", entry.Codeserver.Mesh))
			}
		}
		if contains(entry.Stacks, "vikunja") {
			if entry.Vikunja == nil {
				semanticError(report, file, path+".vikunja", "tenant.local_host.vikunja_missing", `stack "vikunja" requires a vikunja.mesh block`)
			} else if !contains(meshInstances, entry.Vikunja.Mesh) {
				semanticError(report, file, path+".vikunja.mesh", "tenant.local_host.mesh", fmt.Sprintf("vikunja.mesh %q does not match any meshnode.instances[].name", entry.Vikunja.Mesh))
			}
		}
	}
}

// checkMultiInstanceStacks enforces the stack.v1.json cross-field rule that
// isn't expressible as a JSON Schema conditional: multi_instance:true
// requires instance_key and at least one scope:"per_instance" template.
// Lives alongside checkCatalogCycles — the existing precedent for
// catalog-level invariants validated in Go rather than schema if/then.
func checkMultiInstanceStacks(catalog map[string]model.Stack, report *Report) {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		stack := catalog[name]
		if !stack.MultiInstance {
			continue
		}
		file := "stacks/" + name + "/stack.yaml"
		if strings.TrimSpace(stack.InstanceKey) == "" {
			semanticError(report, file, "$.instance_key", "stack.multi_instance.instance_key", fmt.Sprintf("stack %q sets multi_instance but no instance_key", name))
		}
		hasPerInstance := false
		for _, t := range stack.Templates {
			if t.Scope == "per_instance" {
				hasPerInstance = true
				break
			}
		}
		if !hasPerInstance {
			semanticError(report, file, "$.templates", "stack.multi_instance.no_template", fmt.Sprintf("stack %q sets multi_instance but has no scope:\"per_instance\" template", name))
		}
	}
}

func checkCatalogCycles(catalog map[string]model.Stack, report *Report) {
	state := make(map[string]int)
	var visit func(string, []string)
	visit = func(name string, chain []string) {
		if state[name] == 2 {
			return
		}
		if state[name] == 1 {
			semanticError(report, "stacks/"+name+"/stack.yaml", "$.depends_on", "stack.dependency.cycle", "dependency cycle: "+strings.Join(append(chain, name), " -> "))
			return
		}
		state[name] = 1
		for _, dependency := range catalog[name].DependsOn {
			if _, exists := catalog[dependency]; !exists {
				semanticError(report, "stacks/"+name+"/stack.yaml", "$.depends_on", "stack.dependency.unknown", fmt.Sprintf("catalog dependency %q does not exist", dependency))
				continue
			}
			visit(dependency, append(chain, name))
		}
		state[name] = 2
	}
	for name := range catalog {
		visit(name, nil)
	}
}

func compareSemver(left, right string) (int, error) {
	leftParts, err := parseSemver(left)
	if err != nil {
		return 0, fmt.Errorf("invalid running version %q: %w", left, err)
	}
	rightParts, err := parseSemver(right)
	if err != nil {
		return 0, fmt.Errorf("invalid workspace engine version %q: %w", right, err)
	}
	for index := range leftParts {
		if leftParts[index] < rightParts[index] {
			return -1, nil
		}
		if leftParts[index] > rightParts[index] {
			return 1, nil
		}
	}
	return 0, nil
}

func parseSemver(value string) ([3]int, error) {
	var result [3]int
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	value = strings.SplitN(value, "-", 2)[0]
	value = strings.SplitN(value, "+", 2)[0]
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return result, fmt.Errorf("expected major.minor.patch")
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return result, fmt.Errorf("invalid numeric component %q", part)
		}
		result[index] = number
	}
	return result, nil
}

func semanticError(report *Report, file, path, code, message string) {
	addIssue(report, Issue{Severity: "error", File: file, Path: path, Code: code, Message: message})
}

func semanticWarning(report *Report, file, path, code, message string) {
	addIssue(report, Issue{Severity: "warning", File: file, Path: path, Code: code, Message: message})
}

// checkHostCapacity warns (not errors) when a host's assigned stacks' declared
// min_ram_mb exceeds the instance memory. Unknown instance types are skipped.
func checkHostCapacity(data *Data, report *Report, hostName string, host model.Host) {
	if host.Provider != "aws" {
		return
	}
	itype := host.InstanceType
	if itype == "" {
		itype = "t4g.small"
	}
	have, known := model.InstanceRAMMB(itype)
	if !known {
		return
	}
	want := 0
	for _, s := range host.Stacks {
		want += data.Catalog[s].Resources.MinRAMMB
	}
	if want <= have {
		return
	}
	swap := model.SwapForStacks(host.Stacks, data.Catalog, itype)
	addIssue(report, Issue{
		Severity: "warning",
		File:     "deployment.yaml",
		Path:     "$.hosts." + hostName,
		Code:     "capacity.overcommit",
		Message: fmt.Sprintf("host %q stacks want ~%d MiB RAM; %s has %d MiB (+ %d MiB adaptive swap) — consider a larger instance_type",
			hostName, want, itype, have, swap),
	})
}

// checkHostStorage verifies a host's aggregated stack storage: unique volume
// names and at most 10 (the /dev/sd[g-p] guest device range).
func checkHostStorage(data *Data, report *Report, hostName string, host model.Host) {
	if host.Provider == "existing" {
		return
	}
	seen := map[string]string{}
	total := 0
	for _, stackName := range host.Stacks {
		for _, v := range data.Catalog[stackName].Storage {
			total++
			if owner, dup := seen[v.Name]; dup {
				semanticError(report, "deployment.yaml", "$.hosts."+hostName, "storage.name.collision",
					fmt.Sprintf("stacks %q and %q both request a %q volume on host %q", owner, stackName, v.Name, hostName))
			} else {
				seen[v.Name] = stackName
			}
		}
	}
	if total > 10 {
		semanticError(report, "deployment.yaml", "$.hosts."+hostName, "storage.count",
			fmt.Sprintf("host %q needs %d dedicated volumes; the maximum is 10", hostName, total))
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// checkTenantRoles validates tenant-declared database roles (ADR 0039 §3).
// Roles are cluster-global, so names are namespaced by role_prefix, may not
// collide with any tenant's <id>_login/<id>_auth or mksrv_*, and two tenants
// sharing a prefix must define a shared role identically.
func checkTenantRoles(report *Report, file string, tenant model.Tenant, all map[string]model.Tenant) {
	d := tenant.Database
	if d == nil || (len(d.Roles) == 0 && len(d.ServiceRoles) == 0) {
		return
	}
	prefix := tenant.DBRolePrefix()
	reserved := func(name string) bool {
		if strings.HasPrefix(name, "mksrv_") || strings.HasPrefix(name, "pg_") {
			return true
		}
		for id := range all {
			if name == id+"_login" || name == id+"_auth" {
				return true
			}
		}
		return false
	}
	checkName := func(path, name string) {
		switch {
		case !strings.HasPrefix(name, prefix+"_"):
			semanticError(report, file, path, "tenant.database.role_name", fmt.Sprintf("role %q must start with %q", name, prefix+"_"))
		case reserved(name):
			semanticError(report, file, path, "tenant.database.role_name", fmt.Sprintf("role name %q is reserved", name))
		}
	}
	for name := range d.Roles {
		checkName("$.database.roles."+name, name)
		if _, dup := d.ServiceRoles[name]; dup {
			semanticError(report, file, "$.database.service_roles."+name, "tenant.database.role_name", fmt.Sprintf("role %q is declared in both roles and service_roles", name))
		}
	}
	for name := range d.ServiceRoles {
		checkName("$.database.service_roles."+name, name)
	}
	member := func(path, from string, targets []string) {
		for _, m := range targets {
			if _, ok := d.Roles[m]; !ok {
				semanticError(report, file, path, "tenant.database.role_member", fmt.Sprintf("%q is member_of undeclared role %q (service roles can only join declared NOLOGIN roles)", from, m))
			}
		}
	}
	for name, r := range d.Roles {
		member("$.database.roles."+name+".member_of", name, r.MemberOf)
	}
	for name, r := range d.ServiceRoles {
		member("$.database.service_roles."+name+".member_of", name, r.MemberOf)
	}
	// Cycle detection over declared NOLOGIN roles.
	state := map[string]int{}
	var visit func(string) bool
	visit = func(n string) bool {
		switch state[n] {
		case 1:
			return true
		case 2:
			return false
		}
		state[n] = 1
		for _, m := range d.Roles[n].MemberOf {
			if _, ok := d.Roles[m]; ok && visit(m) {
				return true
			}
		}
		state[n] = 2
		return false
	}
	names := make([]string, 0, len(d.Roles))
	for n := range d.Roles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if visit(n) {
			semanticError(report, file, "$.database.roles."+n+".member_of", "tenant.database.role_member", fmt.Sprintf("role membership cycle involving %q", n))
			break
		}
	}
	// Shared prefix: identical definitions for any role two tenants both declare.
	for otherID, other := range all {
		if otherID >= tenant.ID || other.Database == nil || other.DBRolePrefix() != prefix {
			continue
		}
		for name, r := range d.Roles {
			if or, ok := other.Database.Roles[name]; ok && !sameRole(r, or) {
				semanticError(report, file, "$.database.roles."+name, "tenant.database.role_conflict", fmt.Sprintf("role %q is also declared by tenant %q (same prefix) with a different definition", name, otherID))
			}
		}
		for name := range d.ServiceRoles {
			if _, ok := other.Database.ServiceRoles[name]; ok {
				semanticError(report, file, "$.database.service_roles."+name, "tenant.database.role_conflict", fmt.Sprintf("service role %q is also declared by tenant %q: LOGIN roles cannot be shared (would share a password)", name, otherID))
			}
		}
	}
}

// sameRole compares what mksrv manages for a shared role: membership only
// (`when` is a per-tenant pre-request concern).
func sameRole(a, b model.DBRole) bool {
	x, y := append([]string(nil), a.MemberOf...), append([]string(nil), b.MemberOf...)
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(x, y)
}

// checkTenantKeycloak validates declared Keycloak clients and the `when` groups
// of database roles (ADR 0039 §5).
func checkTenantKeycloak(report *Report, file string, tenant model.Tenant) {
	groups := tenant.KeycloakGroups()
	if tenant.Database != nil {
		for name, r := range tenant.Database.Roles {
			for _, g := range r.When {
				if !contains(groups, g) {
					semanticError(report, file, "$.database.roles."+name+".when", "tenant.database.when_group", fmt.Sprintf("group %q is not a realm group of the tenant (%s); declare it under keycloak.groups", g, strings.Join(groups, "|")))
				}
			}
		}
	}
	seen := map[string]bool{}
	for i, c := range tenant.Keycloak.Clients {
		path := fmt.Sprintf("$.keycloak.clients[%d]", i)
		switch {
		case !strings.HasPrefix(c.ID, tenant.ID+"-"):
			semanticError(report, file, path+".id", "tenant.keycloak.client_id", fmt.Sprintf("client id %q must start with %q", c.ID, tenant.ID+"-"))
		case c.ID == tenant.ID+"-websso":
			semanticError(report, file, path+".id", "tenant.keycloak.client_id", fmt.Sprintf("client id %q is reserved for the web SSO gate", c.ID))
		}
		if seen[c.ID] {
			semanticError(report, file, path+".id", "tenant.keycloak.client_id", fmt.Sprintf("duplicate client id %q", c.ID))
		}
		seen[c.ID] = true
		for j, u := range c.RedirectURIs {
			checkClientRedirect(report, file, fmt.Sprintf("%s.redirect_uris[%d]", path, j), u, tenant)
		}
		for j, u := range c.PostLogoutRedirectURIs {
			checkClientRedirect(report, file, fmt.Sprintf("%s.post_logout_redirect_uris[%d]", path, j), u, tenant)
		}
		if c.DirectGrants && c.Public {
			semanticError(report, file, path+".direct_grants", "tenant.keycloak.client_id", "a public client must not enable direct_grants (password grant)")
		}
	}
}

func checkClientRedirect(report *Report, file, path, raw string, tenant model.Tenant) {
	bad := func(msg string) {
		semanticError(report, file, path, "tenant.keycloak.redirect", fmt.Sprintf("redirect %q %s", raw, msg))
	}
	if strings.Contains(raw, "*") {
		bad("must not contain a wildcard")
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		bad("is not a valid absolute URL")
		return
	}
	host := u.Hostname()
	if tenant.Keycloak.AllowLocalhost && (host == "localhost" || host == "127.0.0.1") && (u.Scheme == "http" || u.Scheme == "https") {
		return
	}
	if u.Scheme != "https" {
		bad("must use https")
		return
	}
	base := tenant.BaseDomain
	if base == "" || (host != base && !strings.HasSuffix(host, "."+base)) {
		bad(fmt.Sprintf("must be inside the tenant base_domain %q", base))
	}
}

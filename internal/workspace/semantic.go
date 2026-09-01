// SPDX-License-Identifier: Apache-2.0

package workspace

import (
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fenandosr/mksrv/internal/model"
)

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
	for hostName := range deployment.Hosts {
		hostNames = append(hostNames, hostName)
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
			if stackName == "base" {
				baseHosts = append(baseHosts, hostName)
			}
			if stackName == "identity" {
				identityHosts = append(identityHosts, hostName)
			}
		}
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
	}

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

// reservedForwardIDs are the forward ids demoForwards emits for every tenant; a
// tenant forward may not collide with them.
var reservedForwardIDs = map[string]bool{"edge-health": true, "database": true, "rest": true, "cache": true}

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
	// demoForwards emits at most 4 built-in forwards; Cloud-IT VPN caps at 32.
	if len(tenant.Forwards)+4 > 32 {
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

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

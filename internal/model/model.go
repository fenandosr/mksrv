// SPDX-License-Identifier: Apache-2.0

// Package model defines the public workspace and stack data contracts.
package model

// Deployment is the decoded deployment.yaml document.
type Deployment struct {
	Version   int              `json:"version"`
	Engine    string           `json:"engine"`
	Env       string           `json:"env"`
	Timezone  string           `json:"timezone,omitempty"`
	MgmtCIDR  string           `json:"mgmt_cidr"`
	AWS       AWSConfig        `json:"aws"`
	Backend   BackendConfig    `json:"backend"`
	DNS       DNSConfig        `json:"dns"`
	Identity  IdentityConfig   `json:"identity"`
	Mail      *MailConfig      `json:"mail,omitempty"`
	Hosts     map[string]Host  `json:"hosts"`
	Retention *RetentionConfig `json:"retention,omitempty"`
	Telemetry TelemetryConfig  `json:"telemetry,omitempty"`
}

// RetentionConfig sets how long metrics/logs are kept and the assumed daily
// growth used to size their volumes.
type RetentionConfig struct {
	MetricsDays     int     `json:"metrics_days,omitempty"`
	LogsDays        int     `json:"logs_days,omitempty"`
	MetricsGBPerDay float64 `json:"metrics_gb_per_day,omitempty"`
	LogsGBPerDay    float64 `json:"logs_gb_per_day,omitempty"`
}

// Resolved returns a copy with defaults applied. Safe to call on a nil receiver.
func (r *RetentionConfig) Resolved() RetentionConfig {
	out := RetentionConfig{MetricsDays: 15, LogsDays: 14, MetricsGBPerDay: 0.3, LogsGBPerDay: 2}
	if r == nil {
		return out
	}
	if r.MetricsDays > 0 {
		out.MetricsDays = r.MetricsDays
	}
	if r.LogsDays > 0 {
		out.LogsDays = r.LogsDays
	}
	if r.MetricsGBPerDay > 0 {
		out.MetricsGBPerDay = r.MetricsGBPerDay
	}
	if r.LogsGBPerDay > 0 {
		out.LogsGBPerDay = r.LogsGBPerDay
	}
	return out
}

// LogsHours is LogsDays in hours, for the Loki config template.
func (r RetentionConfig) LogsHours() int { return r.LogsDays * 24 }

type AWSConfig struct {
	Region  string `json:"region"`
	Profile string `json:"profile,omitempty"`
}

type BackendConfig struct {
	Type          string `json:"type"`
	Bucket        string `json:"bucket"`
	Key           string `json:"key,omitempty"`
	DynamoDBTable string `json:"dynamodb_table"`
	Region        string `json:"region,omitempty"`
}

type DNSConfig struct {
	Provider   string            `json:"provider"`
	RootDomain string            `json:"root_domain"`
	Route53    *Route53Config    `json:"route53,omitempty"`
	Cloudflare *CloudflareConfig `json:"cloudflare,omitempty"`
	RFC2136    *RFC2136Config    `json:"rfc2136,omitempty"`
}

type Route53Config struct {
	ZoneID   string `json:"zone_id,omitempty"`
	ZoneName string `json:"zone_name,omitempty"`
}

type CloudflareConfig struct {
	ZoneID      string `json:"zone_id"`
	APITokenRef string `json:"api_token_ref"`
}

type RFC2136Config struct {
	Server string `json:"server"`
	KeyRef string `json:"key_ref"`
}

type IdentityConfig struct {
	KeycloakDomain  string `json:"keycloak_domain"`
	HeadscaleDomain string `json:"headscale_domain"`
	ACMEEmail       string `json:"acme_email"`
}

type MailConfig struct {
	Inbound bool `json:"inbound,omitempty"`
	// OutboundSMTP provisions an SES sending identity for the operator root
	// domain plus a scoped SMTP credential, and configures every tenant
	// realm's Keycloak SMTP settings from it (password reset / email
	// verification). Never touches a tenant's own domain (M25).
	OutboundSMTP bool `json:"outbound_smtp,omitempty"`
}

type Host struct {
	Provider          string   `json:"provider"`
	InstanceType      string   `json:"instance_type,omitempty"`
	RootGB            int      `json:"root_gb,omitempty"`
	DataGB            int      `json:"data_gb,omitempty"`
	Address           string   `json:"address,omitempty"`
	SSHUser           string   `json:"ssh_user,omitempty"`
	SSHPort           int      `json:"ssh_port,omitempty"`
	AdvertiseExitNode bool     `json:"advertise_exitnode,omitempty"`
	Stacks            []string `json:"stacks"`
}

type TelemetryConfig struct {
	Enabled bool `json:"enabled"`
}

// Tenant is a tenants/<id>.yaml document.
type Tenant struct {
	Version     int               `json:"version"`
	ID          string            `json:"id"`
	DisplayName string            `json:"display_name"`
	BaseDomain  string            `json:"base_domain"`
	DNSOverride *DNSOverride      `json:"dns_override,omitempty"`
	Keycloak    TenantKeycloak    `json:"keycloak,omitempty"`
	Mail        *TenantMail       `json:"mail,omitempty"`
	Stacks      []string          `json:"stacks"`
	Forwards    []TenantForward   `json:"forwards,omitempty"`
	DNS         []TenantDNSRecord `json:"dns,omitempty"`
	MeshRoutes  []string          `json:"mesh_routes,omitempty"`
	DeviceLimit int               `json:"device_limit,omitempty"`
	Branding    Branding          `json:"branding,omitempty"`
}

// TenantForward is one Cloud-IT VPN forward a tenant exposes to its members. It
// is translated to configd.Forward and appended to the broker roster.
type TenantForward struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Type     string `json:"type"`           // http | tcp | ssh
	Target   string `json:"target"`         // host:port, a MagicDNS name of a tenant mesh node
	Open     string `json:"open,omitempty"` // browser | none | ssh-terminal | copy
	Path     string `json:"path,omitempty"`
	SSHAlias string `json:"ssh_alias,omitempty"`
}

// TenantDNSRecord is one record mksrv writes into the tenant's own hosted zone
// (dns_override.zone_id), never the operator zone.
type TenantDNSRecord struct {
	Name  string `json:"name"` // label/subdomain under base_domain, or "@" for the apex
	Type  string `json:"type"` // A | AAAA | CNAME
	Value string `json:"value"`
	TTL   int    `json:"ttl,omitempty"`
}

// TenantMail declares the mail identities and policy for a tenant. It drives
// per-domain SES identity, DKIM, and MAIL FROM creation in the mail stack.
type TenantMail struct {
	Domains  []string `json:"domains,omitempty"`
	Inbound  bool     `json:"inbound,omitempty"`
	DMARCRUA string   `json:"dmarc_rua,omitempty"`
}

type DNSOverride struct {
	Provider string `json:"provider"`
	ZoneID   string `json:"zone_id,omitempty"`
	Server   string `json:"server,omitempty"`
}

type TenantKeycloak struct {
	Realm string `json:"realm,omitempty"`
}

type Branding struct {
	Primary     string `json:"primary,omitempty"`
	Secondary   string `json:"secondary,omitempty"`
	LogoDataURI string `json:"logo_data_uri,omitempty"`
}

// UsersFile is a declarative tenant user list.
type UsersFile struct {
	Version int    `json:"version"`
	Tenant  string `json:"tenant"`
	Users   []User `json:"users"`
}

type User struct {
	Email   string   `json:"email"`
	Name    string   `json:"name,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"`
}

// Stack describes a stack catalog entry.
type Stack struct {
	Name         string          `json:"name"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Targets      []string        `json:"targets"`
	Kind         string          `json:"kind,omitempty"` // "service" (default) or "cluster"
	PerTenant    bool            `json:"per_tenant"`
	DependsOn    []string        `json:"depends_on,omitempty"`
	Apps         []StackApp      `json:"apps"`
	Networks     []string        `json:"networks,omitempty"`
	Secrets      []string        `json:"secrets,omitempty"`
	TailnetPorts []int           `json:"tailnet_ports,omitempty"`
	Resources    StackResources  `json:"resources,omitempty"`
	Storage      []StackVolume   `json:"storage,omitempty"`
	Templates    []StackTemplate `json:"templates,omitempty"`
	Hooks        StackHooks      `json:"hooks,omitempty"`
	Health       []StackHealth   `json:"health,omitempty"`
}

// StackVolume is a dedicated EBS volume a stack wants, mounted on the host at
// /var/lib/mksrv/vol/<name> (referenced from templates via {{ .Volume "<name>" }}).
type StackVolume struct {
	Name       string `json:"name"`
	GB         int    `json:"gb"`
	IOPS       int    `json:"iops,omitempty"`       // gp3 provisioned; 0 = baseline 3000
	Throughput int    `json:"throughput,omitempty"` // gp3 MB/s; 0 = baseline 125
	GrowsWith  string `json:"grows_with,omitempty"` // data | metrics | logs — retention-driven sizing
	From       string `json:"from,omitempty"`       // legacy named podman volume, for `mksrv host migrate-volume`
	Unit       string `json:"unit,omitempty"`       // systemd unit to stop while migrating
}

type StackApp struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type StackResources struct {
	MinRAMMB int `json:"min_ram_mb,omitempty"`
	DiskGB   int `json:"disk_gb,omitempty"`
}

type StackTemplate struct {
	Src   string `json:"src"`
	Dst   string `json:"dst"`
	Scope string `json:"scope"`
}

type StackHooks struct {
	PostDeploy []StackHook `json:"post_deploy,omitempty"`
}

type StackHook struct {
	Run   string `json:"run"`
	Scope string `json:"scope"`
}

type StackHealth struct {
	App  string `json:"app"`
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	Port int    `json:"port,omitempty"`
}

// Lock is the workspace mksrv.lock contract.
type Lock struct {
	Engine    string `json:"engine"`
	AppliedAt string `json:"applied_at,omitempty"`
}

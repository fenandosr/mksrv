// SPDX-License-Identifier: Apache-2.0

// Package model defines the public workspace and stack data contracts.
package model

// Deployment is the decoded deployment.yaml document.
type Deployment struct {
	Version   int             `json:"version"`
	Engine    string          `json:"engine"`
	Env       string          `json:"env"`
	Timezone  string          `json:"timezone,omitempty"`
	MgmtCIDR  string          `json:"mgmt_cidr"`
	AWS       AWSConfig       `json:"aws"`
	Backend   BackendConfig   `json:"backend"`
	DNS       DNSConfig       `json:"dns"`
	Identity  IdentityConfig  `json:"identity"`
	Mail      *MailConfig     `json:"mail,omitempty"`
	Hosts     map[string]Host `json:"hosts"`
	Telemetry TelemetryConfig `json:"telemetry,omitempty"`
}

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
	Version     int            `json:"version"`
	ID          string         `json:"id"`
	DisplayName string         `json:"display_name"`
	BaseDomain  string         `json:"base_domain"`
	DNSOverride *DNSOverride   `json:"dns_override,omitempty"`
	Keycloak    TenantKeycloak `json:"keycloak,omitempty"`
	Mail        *TenantMail    `json:"mail,omitempty"`
	Stacks      []string       `json:"stacks"`
	DeviceLimit int            `json:"device_limit,omitempty"`
	Branding    Branding       `json:"branding,omitempty"`
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
	PerTenant    bool            `json:"per_tenant"`
	DependsOn    []string        `json:"depends_on,omitempty"`
	Apps         []StackApp      `json:"apps"`
	Networks     []string        `json:"networks,omitempty"`
	Secrets      []string        `json:"secrets,omitempty"`
	TailnetPorts []int           `json:"tailnet_ports,omitempty"`
	Resources    StackResources  `json:"resources,omitempty"`
	Templates    []StackTemplate `json:"templates,omitempty"`
	Hooks        StackHooks      `json:"hooks,omitempty"`
	Health       []StackHealth   `json:"health,omitempty"`
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

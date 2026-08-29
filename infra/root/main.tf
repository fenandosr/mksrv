locals {
  d           = var.deployment
  env         = local.d.env
  region      = local.d.aws.region
  aws_profile = try(local.d.aws.profile, "") != "" ? local.d.aws.profile : null
  timezone    = try(local.d.timezone, "Etc/UTC")
  mgmt_cidr   = local.d.mgmt_cidr

  hosts = local.d.hosts

  aws_hosts      = { for name, h in local.hosts : name => h if try(h.provider, "aws") == "aws" }
  existing_hosts = { for name, h in local.hosts : name => h if try(h.provider, "aws") == "existing" }

  # The single host carrying `base` is the edge; workspace validation guarantees
  # exactly one exists.
  base_host = one([for name, h in local.hosts : name if contains(h.stacks, "base")])
  edge_ip   = module.aws_host[local.base_host].public_ip

  root_domain = local.d.dns.root_domain
  zone_id     = try(local.d.dns.route53.zone_id, null)

  # Per-tenant PostgREST data API: <id>.rest.<root_domain>, fronted by the edge.
  tenant_rest_fqdns = [
    for id, t in var.tenants : "${id}.rest.${local.root_domain}"
    if contains(try(t.stacks, []), "database")
  ]

  # Shared operator endpoints, all fronted by the edge.
  operator_fqdns = distinct(concat([
    local.d.identity.keycloak_domain,
    local.d.identity.headscale_domain,
    "cfg.${local.root_domain}",
    "grafana.${local.root_domain}",
    "pgadmin.${local.root_domain}",
  ], local.tenant_rest_fqdns))
  operator_records = [
    for fqdn in local.operator_fqdns : {
      fqdn  = fqdn
      type  = "A"
      value = local.edge_ip
      ttl   = 300
    }
  ]
}

module "network" {
  source = "../modules/network"
  env    = local.env
}

resource "aws_key_pair" "operator" {
  count      = var.ssh_public_key != "" ? 1 : 0
  key_name   = "mksrv-${local.env}"
  public_key = var.ssh_public_key
  tags       = { "mksrv:env" = local.env }
}

module "aws_host" {
  source   = "../modules/aws-host"
  for_each = local.aws_hosts

  name          = each.key
  env           = local.env
  region        = local.region
  vpc_id        = module.network.vpc_id
  subnet_id     = module.network.subnet_id
  vpc_cidr      = module.network.cidr
  instance_type = try(each.value.instance_type, "t4g.small")
  root_gb       = try(each.value.root_gb, 30)
  data_gb       = try(each.value.data_gb, 40)
  mgmt_cidr     = local.mgmt_cidr
  stacks        = toset(each.value.stacks)
  timezone      = local.timezone
  key_name      = var.ssh_public_key != "" ? aws_key_pair.operator[0].key_name : ""
  swap_mb       = contains(each.value.stacks, "identity") ? 2048 : 0

  advertise_exitnode = try(each.value.advertise_exitnode, false)
}

module "existing_host" {
  source   = "../modules/existing-host"
  for_each = local.existing_hosts

  name     = each.key
  address  = each.value.address
  ssh_user = each.value.ssh_user
  stacks   = toset(each.value.stacks)
}

module "dns_operator" {
  source  = "../modules/dns"
  records = local.operator_records
  provider_config = {
    kind    = local.d.dns.provider
    zone_id = local.zone_id
  }
  # mksrv owns auth./vpn./cfg. in the operator zone; adopt them if a prior
  # partial apply already created them.
  allow_overwrite = true
}

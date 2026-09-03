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

  # Records mksrv writes into each tenant's own hosted zone (never the operator
  # zone). allow_overwrite is false downstream, so a name that already exists in
  # the tenant zone fails the apply instead of being clobbered.
  tenant_dns = {
    for id, t in var.tenants : id => {
      zone_id = try(t.dns_override.zone_id, "")
      records = [
        for r in try(t.dns, []) : {
          fqdn  = r.name == "@" ? t.base_domain : "${r.name}.${t.base_domain}"
          type  = r.type
          value = r.value
          ttl   = try(r.ttl, 300)
        }
      ]
    }
    if try(t.dns_override.provider, "") == "route53" && length(try(t.dns, [])) > 0
  }

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
  source       = "../modules/network"
  env          = local.env
  subnet_count = var.subnet_count
}

locals {
  # Round-robin AWS hosts across the AZ subnets by sorted host name, so a
  # 3-node cluster (pg1/pg2/pg3) lands one node per AZ.
  sorted_aws_hosts = sort(keys(local.aws_hosts))
  host_subnet = {
    for i, name in local.sorted_aws_hosts :
    name => module.network.subnet_ids[i % length(module.network.subnet_ids)]
  }
}

resource "aws_key_pair" "operator" {
  count      = var.ssh_public_key != "" ? 1 : 0
  key_name   = "mksrv-${local.env}"
  public_key = var.ssh_public_key
  tags       = { "mksrv:env" = local.env }
}

# OpenBao auto-unseal: one KMS key for the whole fleet; only hosts carrying the
# `openbao` stack get the kms:Encrypt/Decrypt grant (in the aws-host module).
locals {
  openbao_hosts = { for name, h in local.aws_hosts : name => h if contains(h.stacks, "openbao") }
}

resource "aws_kms_key" "openbao" {
  count                   = length(local.openbao_hosts) > 0 ? 1 : 0
  description             = "mksrv ${local.env} OpenBao auto-unseal"
  enable_key_rotation     = true
  deletion_window_in_days = 14
  tags                    = { "mksrv:env" = local.env }
}

resource "aws_kms_alias" "openbao" {
  count         = length(local.openbao_hosts) > 0 ? 1 : 0
  name          = "alias/mksrv-${local.env}-openbao"
  target_key_id = aws_kms_key.openbao[0].key_id
}

# Backups: one restic S3 bucket for the fleet; only hosts carrying the `backup`
# stack get the s3 grant (in the aws-host module). Bucket name is deterministic
# so the stack template computes it from env + region.
locals {
  backup_hosts = { for name, h in local.aws_hosts : name => h if contains(h.stacks, "backup") }
}

resource "aws_s3_bucket" "backups" {
  count  = length(local.backup_hosts) > 0 ? 1 : 0
  bucket = "mksrv-${local.env}-backups"
  tags   = { "mksrv:env" = local.env }
}

resource "aws_s3_bucket_versioning" "backups" {
  count  = length(local.backup_hosts) > 0 ? 1 : 0
  bucket = aws_s3_bucket.backups[0].id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  count  = length(local.backup_hosts) > 0 ? 1 : 0
  bucket = aws_s3_bucket.backups[0].id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "backups" {
  count                   = length(local.backup_hosts) > 0 ? 1 : 0
  bucket                  = aws_s3_bucket.backups[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  count  = length(local.backup_hosts) > 0 ? 1 : 0
  bucket = aws_s3_bucket.backups[0].id
  rule {
    id     = "housekeeping"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days = 30
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

module "aws_host" {
  source   = "../modules/aws-host"
  for_each = local.aws_hosts

  name          = each.key
  env           = local.env
  region        = local.region
  vpc_id        = module.network.vpc_id
  subnet_id     = local.host_subnet[each.key]
  vpc_cidr      = module.network.cidr
  instance_type = try(each.value.instance_type, "t4g.small")
  root_gb       = try(each.value.root_gb, 30)
  data_gb       = try(each.value.data_gb, 40)
  mgmt_cidr     = local.mgmt_cidr
  stacks        = toset(each.value.stacks)
  timezone      = local.timezone
  key_name      = var.ssh_public_key != "" ? aws_key_pair.operator[0].key_name : ""
  volumes       = try(var.host_volumes[each.key], [])

  openbao_kms_key_arn = contains(each.value.stacks, "openbao") && length(local.openbao_hosts) > 0 ? aws_kms_key.openbao[0].arn : ""
  backup_bucket_arn   = contains(each.value.stacks, "backup") && length(local.backup_hosts) > 0 ? aws_s3_bucket.backups[0].arn : ""

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

module "dns_tenant" {
  source   = "../modules/dns"
  for_each = local.tenant_dns

  records         = each.value.records
  provider_config = { kind = "route53", zone_id = each.value.zone_id }
  # Tenant zones hold the tenant's own live records (mail included); never
  # overwrite a name mksrv did not create.
  allow_overwrite = false
}

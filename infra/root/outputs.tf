output "hosts" {
  description = "Per-host connection data consumed by the CLI's deploy phase."
  value = merge(
    {
      for name, host in module.aws_host : name => {
        provider           = "aws"
        management_ip      = host.management_ip
        private_ip         = host.private_ip
        public_ip          = host.public_ip
        instance_id        = host.instance_id
        ebs_device         = host.ebs_device
        data_volume_id     = host.data_volume_id
        volumes            = host.volumes
        openbao_kms_key_id = host.openbao_kms_key_id
        az                 = host.availability_zone
      }
    },
    {
      for name, host in module.existing_host : name => {
        provider           = "existing"
        management_ip      = host.management_ip
        private_ip         = null
        public_ip          = null
        instance_id        = null
        ebs_device         = null
        data_volume_id     = null
        volumes            = tomap({})
        openbao_kms_key_id = ""
        az                 = null
      }
    },
  )
}

output "dns" {
  description = "Operator-zone and per-tenant-zone DNS records."
  value = {
    created        = module.dns_operator.created
    pending        = module.dns_operator.pending
    tenant_created = { for id, m in module.dns_tenant : id => m.created }
  }
}

output "mail_smtp" {
  description = "SES sending identity + SMTP credential for Keycloak's transactional email (M25). enabled=false when mail.outbound_smtp is off."
  sensitive   = true
  # A single object literal, every attribute ternary'd individually (both
  # branches always the same primitive type) rather than choosing between two
  # differently-shaped objects: Terraform's type unification across a
  # conditional's two branches falls back to coercing mismatched primitives
  # to string when it can't reconcile the shapes, which silently turned
  # `enabled` (and everything else) into a string on the wire — Go's
  # json.Unmarshal then rejected `"enabled": "false"` for a bool field.
  value = {
    enabled           = local.outbound_smtp
    access_key_id     = local.outbound_smtp ? aws_iam_access_key.ses_smtp[0].id : ""
    secret_access_key = local.outbound_smtp ? aws_iam_access_key.ses_smtp[0].secret : ""
    smtp_host         = local.outbound_smtp ? "email-smtp.${local.region}.amazonaws.com" : ""
    smtp_port         = local.outbound_smtp ? 587 : 0
    from_address      = local.outbound_smtp ? "noreply@${local.root_domain}" : ""
  }
}

output "network" {
  value = {
    vpc_id            = module.network.vpc_id
    subnet_id         = module.network.subnet_id
    subnet_ids        = module.network.subnet_ids
    azs               = module.network.azs
    availability_zone = module.network.availability_zone
  }
}

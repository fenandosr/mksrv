terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60, < 7.0"
    }
  }
}

locals {
  record_fqdns = [for record in var.records : record.fqdn]
  # route53 records keyed by "TYPE fqdn" so a zone can hold an A and AAAA (etc.)
  # for the same name without collision.
  route53_records = var.provider_config.kind == "route53" ? {
    for record in var.records : "${record.type} ${record.fqdn}" => record
  } : {}
}

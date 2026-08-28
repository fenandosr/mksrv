output "created" {
  description = "FQDNs for which a record was created by this module."
  value       = var.provider_config.kind == "manual" ? [] : sort([for r in aws_route53_record.this : r.fqdn])
}

output "pending" {
  description = "Records the operator must create by hand (manual provider)."
  value       = var.provider_config.kind == "manual" ? var.records : []
}

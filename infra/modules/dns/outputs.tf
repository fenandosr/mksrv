output "created" {
  value = var.provider_config.kind == "manual" ? [] : local.record_fqdns
}
output "pending" {
  value = var.provider_config.kind == "manual" ? var.records : []
}

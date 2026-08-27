terraform {
  required_version = ">= 1.8.0"
}
locals {
  record_fqdns = [for record in var.records : record.fqdn]
}

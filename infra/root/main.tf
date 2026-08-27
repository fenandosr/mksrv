# M0 scaffold only. Resource wiring starts in milestone M1.
terraform {
  required_version = ">= 1.8.0"
}

locals {
  host_names = sort(keys(var.deployment.hosts))
}

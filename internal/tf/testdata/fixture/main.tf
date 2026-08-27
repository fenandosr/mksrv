# SPDX-License-Identifier: Apache-2.0
# Provider-free fixture for internal/tf integration tests: init -backend=false,
# validate, plan, apply, and output all work without network access.

terraform {
  required_version = ">= 1.0.0"
}

variable "greeting" {
  type    = string
  default = "hola"
}

output "greeting" {
  value = var.greeting
}

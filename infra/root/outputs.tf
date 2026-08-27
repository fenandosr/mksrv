output "hosts" {
  description = "M1 will expose management and private host connection data."
  value       = {}
}

output "dns" {
  description = "M1 will expose created and pending DNS records."
  value = {
    created = []
    pending = []
  }
}

output "ses" {
  description = "M5 will expose SES identity data."
  value       = null
}

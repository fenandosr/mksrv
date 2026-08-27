output "dns_records" {
  value = []
}

output "dkim_tokens" {
  value = []
}

output "mail_from_domain" {
  value = "mail.${var.domain}"
}

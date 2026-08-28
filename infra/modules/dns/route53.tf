# Route 53 records. Only the records mksrv is asked to manage are created; the
# rest of the hosted zone is untouched. allow_overwrite is deliberately left at
# its default (false) so a name collision fails loudly instead of clobbering an
# existing record.

resource "aws_route53_record" "this" {
  for_each = local.route53_records

  zone_id = var.provider_config.zone_id
  name    = each.value.fqdn
  type    = each.value.type
  ttl     = each.value.ttl
  records = [each.value.value]
}

# Route 53 records. Only the records mksrv is asked to manage are created; the
# rest of the hosted zone is untouched. allow_overwrite defaults to false so a
# name collision fails loudly; the operator zone opts in because mksrv owns
# those subdomains.

resource "aws_route53_record" "this" {
  for_each = local.route53_records

  zone_id         = var.provider_config.zone_id
  name            = each.value.fqdn
  type            = each.value.type
  ttl             = each.value.ttl
  records         = [each.value.value]
  allow_overwrite = var.allow_overwrite
}

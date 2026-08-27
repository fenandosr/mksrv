variable "records" {
  type = list(object({
    fqdn    = string
    type    = string
    value   = string
    ttl     = optional(number, 300)
    proxied = optional(bool, false)
  }))
  default = []
}
variable "provider_config" {
  type = object({
    kind      = string
    zone_id   = optional(string)
    zone_name = optional(string)
    server    = optional(string)
  })
}

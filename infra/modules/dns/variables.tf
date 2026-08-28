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

variable "allow_overwrite" {
  description = "Adopt an existing record of the same name/type instead of failing. Safe for the operator zone (mksrv owns those names); leave false for tenant zones."
  type        = bool
  default     = false
}

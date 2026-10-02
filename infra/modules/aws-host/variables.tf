variable "name" {
  type = string
}

variable "env" {
  type = string
}

variable "region" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "subnet_id" {
  type = string
}

variable "vpc_cidr" {
  type    = string
  default = ""
}

variable "instance_type" {
  type    = string
  default = "t4g.small"
}

variable "root_gb" {
  type    = number
  default = 30
}

variable "data_gb" {
  type    = number
  default = 40
}

variable "mgmt_cidr" {
  type = string
}

variable "stacks" {
  type = set(string)
}

variable "advertise_exitnode" {
  type    = bool
  default = false
}

variable "is_nat" {
  description = "This host is the fleet's NAT instance (the edge of a multi-host fleet, ADR 0027) — disables the source/dest check."
  type        = bool
  default     = false
}

variable "ami_id" {
  type    = string
  default = ""
}

variable "ami_owner" {
  type    = string
  default = "792107900819" # Rocky Linux official AMI publisher
}

variable "key_name" {
  type    = string
  default = ""
}

variable "timezone" {
  type    = string
  default = "Etc/UTC"
}

variable "volumes" {
  description = "Dedicated gp3 EBS volumes for stacks that declare `storage:`."
  type = list(object({
    name       = string
    gb         = number
    iops       = optional(number, 0)
    throughput = optional(number, 0)
    device     = string
  }))
  default = []
}

variable "openbao_kms_key_arn" {
  description = "KMS key ARN for OpenBao auto-unseal; empty on hosts that do not carry the `openbao` stack."
  type        = string
  default     = ""
}

variable "openbao_kms_enabled" {
  description = "Whether this host carries `openbao` and a fleet KMS key exists (known at plan time)."
  type        = bool
  default     = false
}

variable "backup_bucket_arn" {
  description = "S3 bucket ARN for restic backups; empty on hosts that do not carry the `backup` stack."
  type        = string
  default     = ""
}

variable "backup_enabled" {
  description = "Whether this host carries `backup` and a fleet backups bucket exists (known at plan time)."
  type        = bool
  default     = false
}

variable "mail_cert_zone_arns" {
  description = <<-EOT
    Route53 hosted zone ARNs this host's acme.sh DNS-01 mail-cert issuance may
    write to: the operator zone plus every mail-hosted tenant zone with
    mail.branded_hostname. Empty on hosts that do not carry `mail` — Caddy
    cannot issue a multi-SAN certificate (confirmed with its own maintainers:
    "Caddy does not support multi-SAN certificates, for a multitude of
    reasons"), so the shared mail server's cert (one hostname per mail-hosted
    domain that opted into branded_hostname) is obtained directly via DNS-01,
    scoped to only the zones it actually needs to write a _acme-challenge TXT
    record into.
  EOT
  type        = list(string)
  default     = []
}

variable "tags" {
  type    = map(string)
  default = {}
}

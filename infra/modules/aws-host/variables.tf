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

variable "tags" {
  type    = map(string)
  default = {}
}

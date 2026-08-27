variable "name" {
  type = string
}

variable "env" {
  type = string
}

variable "region" {
  type = string
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

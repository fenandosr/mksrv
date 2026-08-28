variable "env" {
  type = string
}

variable "cidr" {
  type    = string
  default = "10.20.0.0/16"
}

variable "availability_zone" {
  type    = string
  default = ""
}

variable "tags" {
  type    = map(string)
  default = {}
}

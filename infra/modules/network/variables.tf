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

variable "subnet_count" {
  description = "Number of public subnets, one per AZ. 1 for a single-node fleet; 3 for a distributed (cluster) fleet."
  type        = number
  default     = 1
}

variable "tags" {
  type    = map(string)
  default = {}
}

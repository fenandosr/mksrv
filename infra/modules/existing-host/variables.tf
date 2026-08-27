variable "name" {
  type = string
}

variable "address" {
  type = string

  validation {
    condition     = length(trimspace(var.address)) > 0
    error_message = "address must not be empty"
  }
}

variable "ssh_user" {
  type = string

  validation {
    condition     = length(trimspace(var.ssh_user)) > 0
    error_message = "ssh_user must not be empty"
  }
}

variable "stacks" {
  type = set(string)
}

variable "allowed_local_stacks" {
  type    = set(string)
  default = ["database", "files", "analytics", "monitor"]
}

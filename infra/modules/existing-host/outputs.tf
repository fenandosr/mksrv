output "management_ip" {
  value = var.address

  precondition {
    condition     = length(setsubtract(var.stacks, var.allowed_local_stacks)) == 0
    error_message = "existing hosts may only receive local-capable stacks"
  }
}

output "ssh_user" {
  value = var.ssh_user
}

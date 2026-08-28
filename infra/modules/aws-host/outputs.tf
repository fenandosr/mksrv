output "management_ip" {
  description = "Address the operator uses for SSH (the Elastic IP)."
  value       = aws_eip.host.public_ip
}

output "private_ip" {
  value = aws_instance.host.private_ip
}

output "public_ip" {
  value = aws_eip.host.public_ip
}

output "instance_id" {
  value = aws_instance.host.id
}

output "availability_zone" {
  value = aws_instance.host.availability_zone
}

output "ebs_device" {
  description = "Guest device path of the attached data volume."
  value       = local.data_device
}

output "security_group_id" {
  value = aws_security_group.host.id
}

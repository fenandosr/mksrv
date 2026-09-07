output "management_ip" {
  description = "Address the CLI connects to: the edge's Elastic IP, or a private host's VPC IP (reached via the edge jump, ADR 0027)."
  value       = local.is_edge ? aws_eip.host[0].public_ip : aws_instance.host.private_ip
}

output "private_ip" {
  value = aws_instance.host.private_ip
}

output "public_ip" {
  description = "The Elastic IP on the edge; empty on private hosts."
  value       = local.is_edge ? aws_eip.host[0].public_ip : ""
}

output "primary_network_interface_id" {
  description = "The instance's primary ENI — the private route table's default route targets the edge's."
  value       = aws_instance.host.primary_network_interface_id
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

output "data_volume_id" {
  value = aws_ebs_volume.data.id
}

output "volumes" {
  description = "Dedicated volume name -> EBS volume id, for the bootstrap to match NVMe disks."
  value       = { for k, v in aws_ebs_volume.extra : k => v.id }
}

output "openbao_kms_key_id" {
  description = "KMS key ARN this host's OpenBao uses for auto-unseal (empty when not an openbao host)."
  value       = var.openbao_kms_key_arn
}

output "security_group_id" {
  value = aws_security_group.host.id
}

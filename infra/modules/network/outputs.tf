output "vpc_id" {
  value = aws_vpc.this.id
}

output "subnet_id" {
  description = "First public subnet — the single-node default."
  value       = aws_subnet.public[0].id
}

output "subnet_ids" {
  description = "All public subnet ids, one per AZ."
  value       = aws_subnet.public[*].id
}

output "azs" {
  value = aws_subnet.public[*].availability_zone
}

output "availability_zone" {
  value = aws_subnet.public[0].availability_zone
}

output "cidr" {
  value = aws_vpc.this.cidr_block
}

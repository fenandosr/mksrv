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

output "private_subnet_ids" {
  description = "All private subnet ids, one per AZ (ADR 0027)."
  value       = aws_subnet.private[*].id
}

output "public_route_table_id" {
  description = "Route table for the public subnets — the S3 gateway endpoint attaches here too."
  value       = aws_route_table.public.id
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

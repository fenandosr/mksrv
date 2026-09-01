# Dedicated single-subnet VPC for an mksrv fleet. No NAT gateway: hosts reach
# the internet through the internet gateway using their own public IPs, which
# keeps the monthly cost near zero.

terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60, < 7.0"
    }
  }
}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  name = "mksrv-${var.env}"
  # Subnet 0 keeps a pinned AZ (when supplied) for a stable single-node fleet;
  # extra subnets take consecutive AZs.
  azs = [
    for i in range(var.subnet_count) :
    i == 0 && var.availability_zone != "" ? var.availability_zone : data.aws_availability_zones.available.names[i]
  ]
  tags = merge(var.tags, { "mksrv:env" = var.env })
}

resource "aws_vpc" "this" {
  cidr_block           = var.cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = merge(local.tags, { Name = local.name })
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(local.tags, { Name = local.name })
}

resource "aws_subnet" "public" {
  count                   = var.subnet_count
  vpc_id                  = aws_vpc.this.id
  availability_zone       = local.azs[count.index]
  cidr_block              = cidrsubnet(var.cidr, 8, count.index)
  map_public_ip_on_launch = false
  tags                    = merge(local.tags, { Name = count.index == 0 ? "${local.name}-public" : "${local.name}-public-${count.index}" })
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  tags = merge(local.tags, { Name = "${local.name}-public" })
}

resource "aws_route_table_association" "public" {
  count          = var.subnet_count
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# Converting the single subnet/association to count-indexed must not destroy the
# live resources.
moved {
  from = aws_subnet.public
  to   = aws_subnet.public[0]
}

moved {
  from = aws_route_table_association.public
  to   = aws_route_table_association.public[0]
}

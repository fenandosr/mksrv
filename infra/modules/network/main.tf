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
  az   = var.availability_zone != "" ? var.availability_zone : data.aws_availability_zones.available.names[0]
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
  vpc_id                  = aws_vpc.this.id
  availability_zone       = local.az
  cidr_block              = cidrsubnet(var.cidr, 8, 0)
  map_public_ip_on_launch = false
  tags                    = merge(local.tags, { Name = "${local.name}-public" })
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
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

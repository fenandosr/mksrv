# One Rocky Linux 9 (arm64) EC2 host with an attached gp3 data volume. The host
# carrying `base` is the edge: it gets an Elastic IP and public 80/443. Every
# host gets SSH from the management CIDR and an instance profile that can read
# its own SSM parameter path and use Session Manager.

terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60, < 7.0"
    }
  }
}

data "aws_ami" "rocky9" {
  count       = var.ami_id == "" ? 1 : 0
  most_recent = true
  owners      = [var.ami_owner]

  filter {
    name   = "name"
    values = ["Rocky-9-EC2-Base-9.*aarch64*"]
  }
  filter {
    name   = "architecture"
    values = ["arm64"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

locals {
  ami_id       = var.ami_id != "" ? var.ami_id : data.aws_ami.rocky9[0].id
  is_edge      = contains(var.stacks, "base")
  name         = "mksrv-${var.env}-${var.name}"
  mgmt_is_ipv6 = strcontains(var.mgmt_cidr, ":")
  data_device  = "/dev/sdf"
  tags         = merge(var.tags, { "mksrv:env" = var.env, "mksrv:host" = var.name })
}

resource "aws_security_group" "host" {
  name_prefix = "${local.name}-"
  description = "mksrv ${var.env} ${var.name}"
  vpc_id      = var.vpc_id
  tags        = merge(local.tags, { Name = local.name })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_ingress_rule" "ssh_v4" {
  count             = local.mgmt_is_ipv6 ? 0 : 1
  security_group_id = aws_security_group.host.id
  description       = "SSH from management network"
  cidr_ipv4         = var.mgmt_cidr
  from_port         = 22
  to_port           = 22
  ip_protocol       = "tcp"
}

resource "aws_vpc_security_group_ingress_rule" "ssh_v6" {
  count             = local.mgmt_is_ipv6 ? 1 : 0
  security_group_id = aws_security_group.host.id
  description       = "SSH from management network"
  cidr_ipv6         = var.mgmt_cidr
  from_port         = 22
  to_port           = 22
  ip_protocol       = "tcp"
}

resource "aws_vpc_security_group_ingress_rule" "web" {
  for_each          = local.is_edge ? toset(["80", "443"]) : toset([])
  security_group_id = aws_security_group.host.id
  description       = "public web"
  cidr_ipv4         = "0.0.0.0/0"
  from_port         = tonumber(each.value)
  to_port           = tonumber(each.value)
  ip_protocol       = "tcp"
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.host.id
  description       = "all egress"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_iam_role" "host" {
  name_prefix = "${local.name}-"
  tags        = merge(local.tags, { Name = local.name })
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "ssm_read" {
  name_prefix = "mksrv-ssm-read-"
  role        = aws_iam_role.host.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath"]
      Resource = "arn:aws:ssm:${var.region}:*:parameter/mksrv/${var.env}/*"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "ssm_core" {
  role       = aws_iam_role.host.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "host" {
  name_prefix = "${local.name}-"
  role        = aws_iam_role.host.name
}

resource "aws_instance" "host" {
  ami                     = local.ami_id
  instance_type           = var.instance_type
  subnet_id               = var.subnet_id
  vpc_security_group_ids  = [aws_security_group.host.id]
  iam_instance_profile    = aws_iam_instance_profile.host.name
  key_name                = var.key_name != "" ? var.key_name : null
  monitoring              = false
  disable_api_termination = false

  root_block_device {
    volume_type = "gp3"
    volume_size = var.root_gb
    encrypted   = true
  }

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 2
  }

  user_data_base64 = base64encode(templatefile("${path.module}/user_data.sh.tftpl", {
    hostname = "${var.name}.${var.env}.mksrv"
    timezone = var.timezone
    swap_mb  = var.swap_mb
  }))

  tags        = merge(local.tags, { Name = local.name })
  volume_tags = merge(local.tags, { Name = "${local.name}-root" })
}

resource "aws_ebs_volume" "data" {
  availability_zone = aws_instance.host.availability_zone
  size              = var.data_gb
  type              = "gp3"
  encrypted         = true
  tags              = merge(local.tags, { Name = "${local.name}-data" })
}

resource "aws_volume_attachment" "data" {
  device_name = local.data_device
  volume_id   = aws_ebs_volume.data.id
  instance_id = aws_instance.host.id
}

resource "aws_eip" "host" {
  domain   = "vpc"
  instance = aws_instance.host.id
  tags     = merge(local.tags, { Name = local.name })
}

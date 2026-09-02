output "hosts" {
  description = "Per-host connection data consumed by the CLI's deploy phase."
  value = merge(
    {
      for name, host in module.aws_host : name => {
        provider       = "aws"
        management_ip  = host.management_ip
        private_ip     = host.private_ip
        public_ip      = host.public_ip
        instance_id    = host.instance_id
        ebs_device     = host.ebs_device
        data_volume_id = host.data_volume_id
        volumes        = host.volumes
        az             = host.availability_zone
      }
    },
    {
      for name, host in module.existing_host : name => {
        provider       = "existing"
        management_ip  = host.management_ip
        private_ip     = null
        public_ip      = null
        instance_id    = null
        ebs_device     = null
        data_volume_id = null
        volumes        = tomap({})
        az             = null
      }
    },
  )
}

output "dns" {
  description = "Operator-zone and per-tenant-zone DNS records."
  value = {
    created        = module.dns_operator.created
    pending        = module.dns_operator.pending
    tenant_created = { for id, m in module.dns_tenant : id => m.created }
  }
}

output "network" {
  value = {
    vpc_id            = module.network.vpc_id
    subnet_id         = module.network.subnet_id
    subnet_ids        = module.network.subnet_ids
    azs               = module.network.azs
    availability_zone = module.network.availability_zone
  }
}

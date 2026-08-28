provider "aws" {
  region  = local.region
  profile = local.aws_profile

  default_tags {
    tags = {
      "mksrv:env"       = local.env
      "mksrv:managed"   = "true"
      "mksrv:component" = "infra"
    }
  }
}

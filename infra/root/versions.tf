terraform {
  required_version = ">= 1.8.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60, < 7.0"
    }
  }

  # The mksrv CLI supplies every setting through `-backend-config` flags at
  # init time; the state itself lives in the private workspace's bucket.
  backend "s3" {}
}

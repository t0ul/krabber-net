terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      project    = "krabber"
      env        = "shared"
      managed-by = "terraform"
      stack      = "bootstrap"
    }
  }
}

data "aws_caller_identity" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id

  state_bucket     = "krabber-tfstate-${local.account_id}"
  artifacts_bucket = "krabber-artifacts-${local.account_id}"
  eb_bucket        = "elasticbeanstalk-${var.region}-${local.account_id}"

  table_arn = "arn:aws:dynamodb:${var.region}:${local.account_id}:table/krabber-prod"
}

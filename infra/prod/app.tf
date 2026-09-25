# Elastic Beanstalk: one t4g instance behind CloudFront (PLAN.md sections 1
# and 3.4). Terraform owns the environment's settings; cmd/deploy owns which
# app version runs, so there's no version_label here.

locals {
  eb_service_role      = "arn:aws:iam::${local.account_id}:role/krabber-eb-service"
  eb_instance_profile  = "krabber-eb-instance"
  artifacts_bucket     = "krabber-artifacts-${local.account_id}"
  app_log_group_prefix = "/aws/elasticbeanstalk/${local.env_name}"
}

# App bundles.
resource "aws_s3_bucket" "artifacts" {
  bucket = local.artifacts_bucket
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket                  = aws_s3_bucket.artifacts.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id
  rule {
    id     = "expire-old-bundles"
    status = "Enabled"
    filter {}
    expiration {
      days = 30
    }
  }
}

# Port 80 from CloudFront only. The managed prefix list counts as about 55 of
# a group's 60 rules, so this group holds nothing else.
data "aws_ec2_managed_prefix_list" "cloudfront" {
  name = "com.amazonaws.global.cloudfront.origin-facing"
}

data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

resource "aws_security_group" "origin" {
  name        = "krabber-eb-origin"
  description = "HTTP from CloudFront only"
  vpc_id      = data.aws_vpc.default.id
}

resource "aws_vpc_security_group_ingress_rule" "cloudfront" {
  security_group_id = aws_security_group.origin.id
  description       = "CloudFront origin-facing"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  prefix_list_id    = data.aws_ec2_managed_prefix_list.cloudfront.id
}

# Out to DynamoDB, SES, SSM and link-card fetches.
resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.origin.id
  description       = "Everything out"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

# The app's log, created here so the mail-cap metric filter (alerts.tf) has
# something to attach to before the first instance boots.
resource "aws_cloudwatch_log_group" "web" {
  name              = "${local.app_log_group_prefix}/var/log/web.stdout.log"
  retention_in_days = 14
}

resource "aws_elastic_beanstalk_application" "krabber" {
  name        = "krabber"
  description = "krabber.net"

  appversion_lifecycle {
    service_role          = local.eb_service_role
    max_count             = 10
    delete_source_from_s3 = true
  }
}

data "aws_elastic_beanstalk_solution_stack" "go" {
  most_recent = true
  name_regex  = "^64bit Amazon Linux 2023 (.*) running Go 1$"
}

locals {
  eb_settings = [
    # Where and what.
    ["aws:elasticbeanstalk:environment", "EnvironmentType", "SingleInstance"],
    ["aws:elasticbeanstalk:environment", "ServiceRole", local.eb_service_role],
    ["aws:ec2:instances", "InstanceTypes", var.instance_type],
    ["aws:ec2:instances", "SupportedArchitectures", "arm64"],
    ["aws:ec2:vpc", "VPCId", data.aws_vpc.default.id],
    ["aws:ec2:vpc", "Subnets", join(",", sort(data.aws_subnets.default.ids))],
    ["aws:ec2:vpc", "AssociatePublicIpAddress", "true"],
    ["aws:autoscaling:launchconfiguration", "IamInstanceProfile", local.eb_instance_profile],
    ["aws:autoscaling:launchconfiguration", "SecurityGroups", aws_security_group.origin.id],
    ["aws:autoscaling:launchconfiguration", "DisableDefaultEC2SecurityGroup", "true"],
    ["aws:autoscaling:launchconfiguration", "DisableIMDSv1", "true"],
    ["aws:autoscaling:launchconfiguration", "RootVolumeType", "gp3"],

    # Every deploy boots a fresh instance and switches only when it's healthy.
    ["aws:elasticbeanstalk:command", "DeploymentPolicy", "Immutable"],
    ["aws:autoscaling:updatepolicy:rollingupdate", "RollingUpdateEnabled", "true"],
    ["aws:autoscaling:updatepolicy:rollingupdate", "RollingUpdateType", "Immutable"],

    # Health, updates, logs, notices.
    ["aws:elasticbeanstalk:healthreporting:system", "SystemType", "enhanced"],
    ["aws:elasticbeanstalk:application", "Application Healthcheck URL", "/healthz"],
    ["aws:elasticbeanstalk:managedactions", "ManagedActionsEnabled", "true"],
    ["aws:elasticbeanstalk:managedactions", "PreferredStartTime", "Sun:09:00"],
    ["aws:elasticbeanstalk:managedactions:platformupdate", "UpdateLevel", "minor"],
    ["aws:elasticbeanstalk:cloudwatch:logs", "StreamLogs", "true"],
    ["aws:elasticbeanstalk:cloudwatch:logs", "RetentionInDays", "14"],
    ["aws:elasticbeanstalk:cloudwatch:logs", "DeleteOnTerminate", "false"],
    ["aws:elasticbeanstalk:sns:topics", "Notification Topic ARN", aws_sns_topic.alerts.arn],

    # The app (secrets come from SSM, config.tf).
    ["aws:elasticbeanstalk:application:environment", "APP_ENV", "prod"],
    ["aws:elasticbeanstalk:application:environment", "AWS_REGION", var.region],
    ["aws:elasticbeanstalk:application:environment", "TABLE_NAME", aws_dynamodb_table.main.name],
    ["aws:elasticbeanstalk:application:environment", "SSM_PREFIX", "/krabber/prod"],
    ["aws:elasticbeanstalk:application:environment", "BASE_URL", "https://${var.domain}"],
    ["aws:elasticbeanstalk:application:environment", "MAX_KRABS", tostring(var.max_krabs)],
  ]
}

resource "aws_elastic_beanstalk_environment" "prod" {
  name                = local.env_name
  application         = aws_elastic_beanstalk_application.krabber.name
  solution_stack_name = data.aws_elastic_beanstalk_solution_stack.go.name
  tier                = "WebServer"

  dynamic "setting" {
    for_each = local.eb_settings
    content {
      namespace = setting.value[0]
      name      = setting.value[1]
      value     = setting.value[2]
    }
  }

  # cmd/deploy picks the running version; platform updates are managed.
  lifecycle {
    ignore_changes = [solution_stack_name]
  }

  depends_on = [
    aws_ssm_parameter.origin_verify_secret,
    aws_ssm_parameter.plain,
    aws_cloudwatch_log_group.web,
  ]
}

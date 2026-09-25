resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

# ---------------------------------------------------------------------------
# Plan role: pull requests only, read-only. GitHub doesn't issue OIDC tokens
# to workflows triggered from forks.
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "gha_plan_trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${local.github_subject}:pull_request"]
    }
  }
}

resource "aws_iam_role" "gha_plan" {
  name                 = "krabber-gha-plan"
  description          = "GitHub Actions: terraform plan on pull requests (read-only)."
  assume_role_policy   = data.aws_iam_policy_document.gha_plan_trust.json
  max_session_duration = 3600
}

resource "aws_iam_role_policy_attachment" "gha_plan_readonly" {
  role       = aws_iam_role.gha_plan.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

data "aws_iam_policy_document" "gha_plan_extra" {
  statement {
    sid       = "StateLockFile"
    actions   = ["s3:PutObject", "s3:DeleteObject"]
    resources = ["${aws_s3_bucket.state.arn}/*.tflock"]
  }

  statement {
    sid       = "DecryptSecureStringParameters"
    actions   = ["kms:Decrypt"]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${var.region}.amazonaws.com"]
    }
  }

  # ReadOnlyAccess includes reading data, which a plan never needs: the
  # table's items (emails, password hashes), stored mail, app bundles and
  # logs. (The Terraform state holds the origin secret, so the plan role can
  # still read that; a pull request can run any workflow, so only people you
  # trust should have push access.)
  statement {
    sid    = "NoData"
    effect = "Deny"
    actions = [
      "dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:Query", "dynamodb:Scan",
      "dynamodb:ExportTableToPointInTime", "dynamodb:GetRecords",
      "logs:GetLogEvents", "logs:FilterLogEvents", "logs:StartQuery", "logs:GetQueryResults",
    ]
    resources = ["*"]
  }

  statement {
    sid     = "NoStoredMail"
    effect  = "Deny"
    actions = ["s3:GetObject", "s3:GetObjectVersion"]
    resources = [
      "arn:aws:s3:::krabber-inbound-mail-${local.account_id}/*",
      "arn:aws:s3:::${local.artifacts_bucket}/*",
    ]
  }
}

resource "aws_iam_role_policy" "gha_plan_extra" {
  name   = "krabber-plan-extra"
  role   = aws_iam_role.gha_plan.id
  policy = data.aws_iam_policy_document.gha_plan_extra.json
}

# ---------------------------------------------------------------------------
# Deploy role: pushes to main only. It can manage the services Krabber uses,
# but it can't create or change IAM; it can only pass the workload roles
# defined in workload_roles.tf.
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "gha_deploy_trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    # Only the krabber environment's jobs. The subject doesn't say which
    # branch ran, so the environment must be limited to main in the
    # repository's settings (Settings > Environments > krabber > Deployment
    # branches); any other environment name would be unprotected.
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${local.github_subject}:environment:krabber"]
    }
  }
}

resource "aws_iam_role" "gha_deploy" {
  name                 = "krabber-gha-deploy"
  description          = "GitHub Actions: terraform apply and Beanstalk deploys from main."
  assume_role_policy   = data.aws_iam_policy_document.gha_deploy_trust.json
  max_session_duration = 3600
}

data "aws_iam_policy_document" "gha_deploy_services" {
  statement {
    sid = "ManagedServices"
    actions = [
      "acm:*",
      "autoscaling:*",
      "budgets:*",
      "ce:*",
      "cloudformation:*",
      "cloudfront:*",
      "cloudwatch:*",
      "ec2:*",
      "elasticbeanstalk:*",
      "elasticloadbalancing:Describe*",
      "events:*",
      "logs:*",
      "route53:*",
      "scheduler:*",
      "ses:*",
      "wafv2:*",
      "pricingplanmanager:Get*",
      "pricingplanmanager:List*",
      "sts:GetCallerIdentity",
      "tag:GetResources",
    ]
    resources = ["*"]
  }

  statement {
    sid     = "Buckets"
    actions = ["s3:*"]
    resources = [
      "arn:aws:s3:::krabber-*",
      "arn:aws:s3:::krabber-*/*",
      "arn:aws:s3:::${local.eb_bucket}",
      "arn:aws:s3:::${local.eb_bucket}/*",
    ]
  }

  statement {
    sid       = "BucketDiscovery"
    actions   = ["s3:ListAllMyBuckets", "s3:GetBucketLocation", "s3:CreateBucket"]
    resources = ["*"]
  }

  statement {
    sid     = "KrabberTables"
    actions = ["dynamodb:*"]
    resources = [
      "arn:aws:dynamodb:${var.region}:${local.account_id}:table/krabber-*",
      "arn:aws:dynamodb:${var.region}:${local.account_id}:table/krabber-*/*",
    ]
  }

  statement {
    sid       = "DynamoDBDiscovery"
    actions   = ["dynamodb:ListTables", "dynamodb:DescribeLimits", "dynamodb:DescribeEndpoints", "dynamodb:ListTagsOfResource"]
    resources = ["*"]
  }

  statement {
    sid       = "KrabberFunctions"
    actions   = ["lambda:*"]
    resources = ["arn:aws:lambda:${var.region}:${local.account_id}:function:krabber-*"]
  }

  statement {
    sid       = "LambdaDiscovery"
    actions   = ["lambda:GetAccountSettings", "lambda:ListFunctions", "lambda:ListTags"]
    resources = ["*"]
  }

  statement {
    sid     = "KrabberParameters"
    actions = ["ssm:*"]
    resources = [
      "arn:aws:ssm:${var.region}:${local.account_id}:parameter/krabber/*",
    ]
  }

  statement {
    sid       = "ParameterDiscovery"
    actions   = ["ssm:DescribeParameters"]
    resources = ["*"]
  }

  statement {
    sid       = "KrabberTopics"
    actions   = ["sns:*"]
    resources = ["arn:aws:sns:*:${local.account_id}:krabber-*"]
  }

  statement {
    sid       = "TopicDiscovery"
    actions   = ["sns:ListTopics"]
    resources = ["*"]
  }

  statement {
    sid       = "SecureStringKeys"
    actions   = ["kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey", "kms:DescribeKey"]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${var.region}.amazonaws.com"]
    }
  }
}

resource "aws_iam_policy" "gha_deploy_services" {
  name        = "krabber-deploy-services"
  description = "Services the Krabber deploy role may manage."
  policy      = data.aws_iam_policy_document.gha_deploy_services.json
}

data "aws_iam_policy_document" "gha_deploy_iam_and_guards" {
  statement {
    sid       = "IamRead"
    actions   = ["iam:Get*", "iam:List*"]
    resources = ["*"]
  }

  statement {
    sid     = "PassWorkloadRoles"
    actions = ["iam:PassRole"]
    resources = [
      aws_iam_role.eb_service.arn,
      aws_iam_role.eb_instance.arn,
      aws_iam_role.canary.arn,
      aws_iam_role.mail_forward.arn,
    ]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values = [
        "elasticbeanstalk.amazonaws.com",
        "ec2.amazonaws.com",
        "lambda.amazonaws.com",
      ]
    }
  }

  statement {
    sid       = "ServiceLinkedRoles"
    actions   = ["iam:CreateServiceLinkedRole"]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "iam:AWSServiceName"
      values = [
        "autoscaling.amazonaws.com",
        "elasticbeanstalk.amazonaws.com",
        "managedupdates.elasticbeanstalk.amazonaws.com",
      ]
    }
  }

  # Cost guards: nothing in Krabber needs these, and each can run up a bill.
  statement {
    sid       = "DenyLargeInstances"
    effect    = "Deny"
    actions   = ["ec2:RunInstances"]
    resources = ["arn:aws:ec2:*:*:instance/*"]

    condition {
      test     = "StringNotEquals"
      variable = "ec2:InstanceType"
      values   = var.allowed_instance_types
    }
  }

  statement {
    sid    = "DenyCostlyPurchases"
    effect = "Deny"
    actions = [
      "ec2:AllocateHosts",
      "ec2:CreateCapacityReservation",
      "ec2:CreateNatGateway",
      "ec2:PurchaseHostReservation",
      "ec2:PurchaseReservedInstancesOffering",
      "ec2:PurchaseScheduledInstances",
      "ec2:RequestSpotFleet",
      "ec2:RequestSpotInstances",
      "dynamodb:PurchaseReservedCapacityOfferings",
      "lambda:PutProvisionedConcurrencyConfig",
      "ses:CreateDedicatedIpPool",
      "ses:PutDedicatedIpWarmupAttributes",
      "ses:PutAccountVdmAttributes",
      "cloudfront:CreateRealtimeLogConfig",
      "pricingplanmanager:CreateSubscription",
      "pricingplanmanager:UpdateSubscription",
      "pricingplanmanager:ApprovePaidSubscription",
    ]
    resources = ["*"]
  }

  statement {
    sid    = "ProtectStateBucket"
    effect = "Deny"
    actions = [
      "s3:DeleteBucket", "s3:PutBucketPolicy", "s3:DeleteBucketPolicy", "s3:PutBucketVersioning",
      "s3:PutLifecycleConfiguration", "s3:PutBucketAcl", "s3:PutBucketOwnershipControls",
      "s3:PutBucketPublicAccessBlock", "s3:DeleteBucketPublicAccessBlock",
    ]
    resources = [aws_s3_bucket.state.arn]
  }

  # Old versions are the state's history; deleting them isn't a deploy's job.
  statement {
    sid       = "KeepStateHistory"
    effect    = "Deny"
    actions   = ["s3:DeleteObjectVersion", "s3:PutObjectAcl", "s3:PutObjectVersionAcl"]
    resources = ["${aws_s3_bucket.state.arn}/*"]
  }

  # Sharing a disk snapshot or image with another account would copy data out.
  statement {
    sid       = "NoSharingOut"
    effect    = "Deny"
    actions   = ["ec2:ModifySnapshotAttribute", "ec2:ModifyImageAttribute", "ec2:ModifyFpgaImageAttribute"]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "gha_deploy_iam_and_guards" {
  name        = "krabber-deploy-iam-and-guards"
  description = "PassRole for workload roles, service-linked roles, and cost guards for the Krabber deploy role."
  policy      = data.aws_iam_policy_document.gha_deploy_iam_and_guards.json
}

resource "aws_iam_role_policy_attachment" "gha_deploy_services" {
  role       = aws_iam_role.gha_deploy.name
  policy_arn = aws_iam_policy.gha_deploy_services.arn
}

resource "aws_iam_role_policy_attachment" "gha_deploy_iam_and_guards" {
  role       = aws_iam_role.gha_deploy.name
  policy_arn = aws_iam_policy.gha_deploy_iam_and_guards.arn
}

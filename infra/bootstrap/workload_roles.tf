# Roles used by the running workloads. They live here, not in infra/prod, so
# CI never needs permission to create or modify IAM.

# ---------------------------------------------------------------------------
# Elastic Beanstalk service role (health reporting and managed updates)
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "eb_service_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["elasticbeanstalk.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = ["elasticbeanstalk"]
    }
  }
}

resource "aws_iam_role" "eb_service" {
  name               = "krabber-eb-service"
  description        = "Elastic Beanstalk service role for krabber-prod."
  assume_role_policy = data.aws_iam_policy_document.eb_service_trust.json
}

resource "aws_iam_role_policy_attachment" "eb_service" {
  for_each = toset([
    "arn:aws:iam::aws:policy/service-role/AWSElasticBeanstalkEnhancedHealth",
    "arn:aws:iam::aws:policy/AWSElasticBeanstalkManagedUpdatesCustomerRolePolicy",
  ])

  role       = aws_iam_role.eb_service.name
  policy_arn = each.value
}

# ---------------------------------------------------------------------------
# Elastic Beanstalk instance role (what the Go app runs as)
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "eb_instance_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eb_instance" {
  name               = "krabber-eb-instance"
  description        = "Instance role for the krabber-prod web server."
  assume_role_policy = data.aws_iam_policy_document.eb_instance_trust.json
}

resource "aws_iam_role_policy_attachment" "eb_instance" {
  for_each = toset([
    "arn:aws:iam::aws:policy/AWSElasticBeanstalkWebTier",
    "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
  ])

  role       = aws_iam_role.eb_instance.name
  policy_arn = each.value
}

data "aws_iam_policy_document" "eb_instance_app" {
  statement {
    sid = "Table"
    actions = [
      "dynamodb:BatchGetItem",
      "dynamodb:BatchWriteItem",
      "dynamodb:ConditionCheckItem",
      "dynamodb:DeleteItem",
      "dynamodb:DescribeTable",
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:Query",
      "dynamodb:Scan",
      "dynamodb:UpdateItem",
    ]
    resources = [
      local.table_arn,
      "${local.table_arn}/index/*",
    ]
  }

  statement {
    sid     = "SendMail"
    actions = ["ses:SendEmail", "ses:SendRawEmail"]
    # identity/* because in the SES sandbox a send is also checked against
    # the recipient's verified identity. The FromAddress condition still
    # limits it to no-reply@krabber.net.
    resources = [
      "arn:aws:ses:${var.region}:${local.account_id}:identity/*",
      "arn:aws:ses:${var.region}:${local.account_id}:configuration-set/krabber-transactional",
    ]

    condition {
      test     = "StringEquals"
      variable = "ses:FromAddress"
      values   = ["no-reply@krabber.net"]
    }
  }

  statement {
    sid     = "Config"
    actions = ["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath"]
    resources = [
      "arn:aws:ssm:${var.region}:${local.account_id}:parameter/krabber/prod",
      "arn:aws:ssm:${var.region}:${local.account_id}:parameter/krabber/prod/*",
    ]
  }

  statement {
    sid       = "DecryptConfig"
    actions   = ["kms:Decrypt"]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${var.region}.amazonaws.com"]
    }
  }

  # The origin certificate for nginx (infra/prod/origin_tls.tf). Only
  # exportable certificates can be exported; the CloudFront one isn't.
  statement {
    sid       = "OriginCertificate"
    actions   = ["acm:ExportCertificate"]
    resources = ["arn:aws:acm:${var.region}:${local.account_id}:certificate/*"]
  }
}

resource "aws_iam_role_policy" "eb_instance_app" {
  name   = "krabber-app"
  role   = aws_iam_role.eb_instance.id
  policy = data.aws_iam_policy_document.eb_instance_app.json
}

resource "aws_iam_instance_profile" "eb_instance" {
  name = "krabber-eb-instance"
  role = aws_iam_role.eb_instance.name
}

# ---------------------------------------------------------------------------
# support@krabber.net forwarder Lambda
# ---------------------------------------------------------------------------

resource "aws_iam_role" "mail_forward" {
  name               = "krabber-mail-forward"
  description        = "Execution role for the support@krabber.net forwarder."
  assume_role_policy = data.aws_iam_policy_document.canary_trust.json
}

data "aws_iam_policy_document" "mail_forward" {
  statement {
    sid       = "ReadInbound"
    actions   = ["s3:GetObject"]
    resources = ["arn:aws:s3:::krabber-inbound-mail-${local.account_id}/support/*"]
  }

  # identity/* because in the SES sandbox a send is also checked against the
  # recipient's verified identity; the configuration set because it's the
  # domain's default, so every send uses it.
  statement {
    sid     = "Forward"
    actions = ["ses:SendEmail", "ses:SendRawEmail"]
    resources = [
      "arn:aws:ses:${var.region}:${local.account_id}:identity/*",
      "arn:aws:ses:${var.region}:${local.account_id}:configuration-set/krabber-transactional",
    ]

    condition {
      test     = "StringEquals"
      variable = "ses:FromAddress"
      values   = ["support@krabber.net"]
    }
  }

  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:${local.account_id}:log-group:/aws/lambda/krabber-mail-forward:*",
    ]
  }

  # Its daily forward counter, and nothing else in the table.
  statement {
    sid       = "DailyCap"
    actions   = ["dynamodb:UpdateItem"]
    resources = [local.table_arn]

    condition {
      test     = "ForAllValues:StringEquals"
      variable = "dynamodb:LeadingKeys"
      values   = ["RL#mail-forward#daily"]
    }
  }
}

resource "aws_iam_role_policy" "mail_forward" {
  name   = "krabber-mail-forward"
  role   = aws_iam_role.mail_forward.id
  policy = data.aws_iam_policy_document.mail_forward.json
}

# ---------------------------------------------------------------------------
# Uptime canary Lambda
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "canary_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "canary" {
  name               = "krabber-canary"
  description        = "Execution role for the krabber-canary uptime check."
  assume_role_policy = data.aws_iam_policy_document.canary_trust.json
}

data "aws_iam_policy_document" "canary_logs" {
  statement {
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:${local.account_id}:log-group:/aws/lambda/krabber-canary:*",
    ]
  }
}

resource "aws_iam_role_policy" "canary_logs" {
  name   = "krabber-canary-logs"
  role   = aws_iam_role.canary.id
  policy = data.aws_iam_policy_document.canary_logs.json
}

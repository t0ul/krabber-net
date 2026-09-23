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
    resources = [
      "arn:aws:ses:${var.region}:${local.account_id}:identity/krabber.net",
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

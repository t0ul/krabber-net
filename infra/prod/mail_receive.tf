# support@krabber.net (PLAN.md section 3.4): SES receives it, scans it for
# spam and viruses, stores it in S3 for 30 days (the privacy page's promise)
# and invokes cmd/mailforward, which re-sends it to alert_email. The Lambda's
# role is in infra/bootstrap. Build dist/mailforward.zip first (make lambdas).

locals {
  inbound_bucket = "krabber-inbound-mail-${local.account_id}"
  support_rule   = "arn:aws:ses:${var.region}:${local.account_id}:receipt-rule-set/krabber-inbound:receipt-rule/support"
}

resource "aws_s3_bucket" "inbound" {
  bucket = local.inbound_bucket
}

resource "aws_s3_bucket_public_access_block" "inbound" {
  bucket                  = aws_s3_bucket.inbound.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "inbound" {
  bucket = aws_s3_bucket.inbound.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "inbound" {
  bucket = aws_s3_bucket.inbound.id
  rule {
    id     = "expire-mail"
    status = "Enabled"
    filter {}
    expiration {
      days = 30
    }
  }
}

data "aws_iam_policy_document" "inbound" {
  statement {
    sid       = "SESWrites"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.inbound.arn}/support/*"]
    principals {
      type        = "Service"
      identifiers = ["ses.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceAccount"
      values   = [local.account_id]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [local.support_rule]
    }
  }
}

resource "aws_s3_bucket_policy" "inbound" {
  bucket = aws_s3_bucket.inbound.id
  policy = data.aws_iam_policy_document.inbound.json
}

resource "aws_cloudwatch_log_group" "mail_forward" {
  name              = "/aws/lambda/krabber-mail-forward"
  retention_in_days = 14
}

resource "aws_lambda_function" "mail_forward" {
  function_name    = "krabber-mail-forward"
  description      = "Forwards support@krabber.net to the alert address."
  role             = "arn:aws:iam::${local.account_id}:role/krabber-mail-forward"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  handler          = "bootstrap"
  filename         = "${path.module}/../../dist/mailforward.zip"
  source_code_hash = filebase64sha256("${path.module}/../../dist/mailforward.zip")
  memory_size      = 128
  timeout          = 30

  environment {
    variables = {
      BUCKET     = aws_s3_bucket.inbound.bucket
      PREFIX     = "support/"
      FROM       = "Krabber support <support@${var.domain}>"
      FORWARD_TO = var.alert_email
    }
  }

  depends_on = [aws_cloudwatch_log_group.mail_forward]
}

resource "aws_lambda_permission" "ses" {
  statement_id   = "ses"
  action         = "lambda:InvokeFunction"
  function_name  = aws_lambda_function.mail_forward.function_name
  principal      = "ses.amazonaws.com"
  source_account = local.account_id
  source_arn     = local.support_rule
}

resource "aws_ses_receipt_rule_set" "inbound" {
  rule_set_name = "krabber-inbound"
}

resource "aws_ses_active_receipt_rule_set" "inbound" {
  rule_set_name = aws_ses_receipt_rule_set.inbound.rule_set_name
}

resource "aws_ses_receipt_rule" "support" {
  name          = "support"
  rule_set_name = aws_ses_receipt_rule_set.inbound.rule_set_name
  recipients    = ["support@${var.domain}"]
  enabled       = true
  scan_enabled  = true
  tls_policy    = "Require"

  s3_action {
    bucket_name       = aws_s3_bucket.inbound.bucket
    object_key_prefix = "support/"
    position          = 1
  }

  lambda_action {
    function_arn    = aws_lambda_function.mail_forward.arn
    invocation_type = "Event"
    position        = 2
  }

  depends_on = [aws_s3_bucket_policy.inbound, aws_lambda_permission.ses]
}

# Mail for krabber.net goes to SES (bounces for our own sending still go to
# mail.krabber.net's MX, email.tf).
resource "aws_route53_record" "inbound_mx" {
  zone_id = var.hosted_zone_id
  name    = var.domain
  type    = "MX"
  ttl     = 3600
  records = ["10 inbound-smtp.${var.region}.amazonaws.com"]
}

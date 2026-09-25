# Alarms, budgets and anomaly detection, all to your email (PLAN.md section
# 11). Each SNS topic sends a confirmation email: click it once.

resource "aws_sns_topic" "alerts" {
  name = "krabber-alerts"
}

data "aws_iam_policy_document" "alerts" {
  statement {
    sid       = "Owner"
    actions   = ["sns:Publish", "sns:Subscribe", "sns:GetTopicAttributes", "sns:SetTopicAttributes"]
    resources = [aws_sns_topic.alerts.arn]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${local.account_id}:root"]
    }
  }

  statement {
    sid       = "Services"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.alerts.arn]
    principals {
      type        = "Service"
      identifiers = ["ses.amazonaws.com", "cloudwatch.amazonaws.com", "elasticbeanstalk.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
  }
}

resource "aws_sns_topic_policy" "alerts" {
  arn    = aws_sns_topic.alerts.arn
  policy = data.aws_iam_policy_document.alerts.json
}

resource "aws_sns_topic_subscription" "alerts_email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

# CloudFront's metrics live in us-east-1, and an alarm can only notify a
# topic in its own region.
resource "aws_sns_topic" "alerts_us_east_1" {
  provider = aws.us_east_1
  name     = "krabber-alerts"
}

resource "aws_sns_topic_subscription" "alerts_us_east_1_email" {
  provider  = aws.us_east_1
  topic_arn = aws_sns_topic.alerts_us_east_1.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

# ---------------------------------------------------------------------------
# Alarms. Beanstalk health changes arrive as environment notifications on
# krabber-alerts (app.tf), which costs nothing.
# ---------------------------------------------------------------------------

resource "aws_cloudwatch_metric_alarm" "cpu_surplus" {
  alarm_name          = "krabber-cpu-surplus-charged"
  alarm_description   = "The t4g unlimited-credit surcharge ($0.04 per vCPU-hour at most) has started."
  namespace           = "AWS/EC2"
  metric_name         = "CPUSurplusCreditsCharged"
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  treat_missing_data  = "notBreaching"
  dimensions = {
    AutoScalingGroupName = aws_elastic_beanstalk_environment.prod.autoscaling_groups[0]
  }
  alarm_actions = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "dynamodb_throttled" {
  alarm_name          = "krabber-dynamodb-throttled"
  alarm_description   = "The table is hitting its throughput caps: an attack, or time to raise them with MAX_KRABS (PLAN.md section 4.1)."
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  evaluation_periods  = 3
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]

  metric_query {
    id          = "throttled"
    expression  = "reads + writes"
    label       = "Throttle events"
    return_data = true
  }

  dynamic "metric_query" {
    for_each = { reads = "ReadThrottleEvents", writes = "WriteThrottleEvents" }
    content {
      id = metric_query.key
      metric {
        namespace   = "AWS/DynamoDB"
        metric_name = metric_query.value
        stat        = "Sum"
        period      = 300
        dimensions = {
          TableName = aws_dynamodb_table.main.name
        }
      }
    }
  }
}

resource "aws_cloudwatch_metric_alarm" "ses_bounces" {
  alarm_name          = "krabber-ses-bounce-rate"
  alarm_description   = "Bounce rate over 4% (AWS reviews accounts at 5%)."
  namespace           = "AWS/SES"
  metric_name         = "Reputation.BounceRate"
  statistic           = "Maximum"
  period              = 3600
  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0.04
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "ses_complaints" {
  alarm_name          = "krabber-ses-complaint-rate"
  alarm_description   = "Complaint rate over 0.08% (AWS reviews accounts at 0.1%)."
  namespace           = "AWS/SES"
  metric_name         = "Reputation.ComplaintRate"
  statistic           = "Maximum"
  period              = 3600
  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0.0008
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_log_metric_filter" "mail_cap" {
  name           = "krabber-mail-cap-reached"
  log_group_name = aws_cloudwatch_log_group.web.name
  pattern        = "{ $.msg = \"mail_cap_reached\" }"

  metric_transformation {
    name          = "MailCapReached"
    namespace     = "Krabber"
    value         = "1"
    default_value = "0"
  }
}

resource "aws_cloudwatch_metric_alarm" "mail_cap" {
  alarm_name          = "krabber-mail-cap-reached"
  alarm_description   = "The app hit its daily email cap: someone may be abusing signup or reset."
  namespace           = "Krabber"
  metric_name         = "MailCapReached"
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "cloudfront_5xx" {
  provider            = aws.us_east_1
  alarm_name          = "krabber-cloudfront-5xx"
  alarm_description   = "Over 5% of requests failing with 5xx: the origin is down or failing."
  namespace           = "AWS/CloudFront"
  metric_name         = "5xxErrorRate"
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  comparison_operator = "GreaterThanThreshold"
  threshold           = 5
  treat_missing_data  = "notBreaching"
  dimensions = {
    DistributionId = aws_cloudfront_distribution.site.id
    Region         = "Global"
  }
  alarm_actions = [aws_sns_topic.alerts_us_east_1.arn]
}

# ---------------------------------------------------------------------------
# Budgets and anomaly detection
# ---------------------------------------------------------------------------

resource "aws_budgets_budget" "monthly" {
  name         = "krabber-monthly"
  budget_type  = "COST"
  limit_amount = tostring(var.monthly_budget)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  dynamic "notification" {
    for_each = [
      { type = "ACTUAL", threshold = 80 },
      { type = "ACTUAL", threshold = 100 },
      { type = "FORECASTED", threshold = 100 },
    ]
    content {
      comparison_operator        = "GREATER_THAN"
      threshold                  = notification.value.threshold
      threshold_type             = "PERCENTAGE"
      notification_type          = notification.value.type
      subscriber_email_addresses = [var.alert_email]
    }
  }
}

resource "aws_budgets_budget" "daily" {
  name         = "krabber-daily"
  budget_type  = "COST"
  limit_amount = tostring(var.daily_budget)
  limit_unit   = "USD"
  time_unit    = "DAILY"

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
  }
}

resource "aws_ce_anomaly_monitor" "services" {
  name              = "krabber-services"
  monitor_type      = "DIMENSIONAL"
  monitor_dimension = "SERVICE"
}

resource "aws_ce_anomaly_subscription" "email" {
  name             = "krabber-anomalies"
  frequency        = "DAILY"
  monitor_arn_list = [aws_ce_anomaly_monitor.services.arn]

  subscriber {
    type    = "EMAIL"
    address = var.alert_email
  }

  threshold_expression {
    dimension {
      key           = "ANOMALY_TOTAL_IMPACT_ABSOLUTE"
      match_options = ["GREATER_THAN_OR_EQUAL"]
      values        = ["3"]
    }
  }
}

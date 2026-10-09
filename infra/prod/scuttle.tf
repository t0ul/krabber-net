# @scuttle, the tide-and-weather bot. A small Go Lambda (cmd/scuttle) runs on an
# EventBridge schedule, reads the Manhattan tide (NOAA) and sky (NWS), both
# keyless US-gov APIs, and posts one molt to krabber.net through the Krabber
# API. Its only secret is the API key, held in SSM. The role is in
# infra/bootstrap. Build dist/scuttle.zip first (make lambdas).
#
# One-time setup, after the first apply:
#   1. Create the bot and mint its key against the prod table:
#        TABLE_NAME=krabber-prod go run ./cmd/krabctl bot scuttle scuttle@krabber.net
#        TABLE_NAME=krabber-prod go run ./cmd/krabctl apikey scuttle "scuttle bot"
#   2. Store the key (the resource below holds only a placeholder):
#        aws ssm put-parameter --name /krabber/scuttle/api-key --type SecureString \
#          --overwrite --value kb_...

resource "aws_ssm_parameter" "scuttle_api_key" {
  name        = "/krabber/scuttle/api-key"
  description = "Krabber API key for the @scuttle bot (set by hand with put-parameter)."
  type        = "SecureString"
  value       = "set-me-with-put-parameter"

  # The real key is written out of band, so Terraform must not overwrite it.
  lifecycle {
    ignore_changes = [value]
  }
}

resource "aws_cloudwatch_log_group" "scuttle" {
  name              = "/aws/lambda/krabber-scuttle"
  retention_in_days = 14
}

resource "aws_lambda_function" "scuttle" {
  function_name    = "krabber-scuttle"
  description      = "Posts the Manhattan tide and weather to krabber.net as @scuttle."
  role             = "arn:aws:iam::${local.account_id}:role/krabber-scuttle"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  handler          = "bootstrap"
  filename         = "${path.module}/../../dist/scuttle.zip"
  source_code_hash = filebase64sha256("${path.module}/../../dist/scuttle.zip")
  memory_size      = 128
  timeout          = 30

  environment {
    variables = {
      KRABBER_API_URL = "https://${var.domain}/api/v1/molts"
      API_KEY_PARAM   = aws_ssm_parameter.scuttle_api_key.name
      NWS_LAT         = "40.7829" # Central Park
      NWS_LON         = "-73.9654"
      NOAA_STATION    = "8518750" # The Battery, NY
      # NWS asks for a descriptive User-Agent with a contact. Use the support
      # inbox, not a personal address, so nothing personal leaks to the feeds.
      USER_AGENT = "krabber-scuttle (+https://${var.domain}; support@${var.domain})"
    }
  }

  depends_on = [aws_cloudwatch_log_group.scuttle]
}

# When it runs. Times are UTC, so they drift an hour with US daylight saving:
# morning is 07:00 EDT / 06:00 EST, afternoon 17:00 EDT / 16:00 EST. Fine for a
# tide bot, and well within the API's 100-molts-an-hour limit.
locals {
  scuttle_schedules = {
    morning   = "cron(0 11 * * ? *)"
    afternoon = "cron(0 21 * * ? *)"
  }
}

resource "aws_cloudwatch_event_rule" "scuttle" {
  for_each            = local.scuttle_schedules
  name                = "krabber-scuttle-${each.key}"
  description         = "Triggers the @scuttle bot (${each.key})."
  schedule_expression = each.value
}

resource "aws_cloudwatch_event_target" "scuttle" {
  for_each  = local.scuttle_schedules
  rule      = aws_cloudwatch_event_rule.scuttle[each.key].name
  target_id = "scuttle"
  arn       = aws_lambda_function.scuttle.arn
}

resource "aws_lambda_permission" "scuttle_events" {
  for_each      = local.scuttle_schedules
  statement_id  = "events-${each.key}"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.scuttle.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.scuttle[each.key].arn
}

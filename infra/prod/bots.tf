# The compose-and-post bots: small Go Lambdas (cmd/<bot>) that build one molt's
# text and post it to krabber.net through the Krabber API on an EventBridge
# schedule. They share the shape of @scuttle but carry no feed or table access —
# their only secret is the API key in SSM. Roles are in infra/bootstrap. Build
# the bundles first (make lambdas). @scuttle keeps its own file (scuttle.tf).
#
# Add a bot by adding a block to local.bots below, then building its bundle and
# doing the one-time setup once, after the first apply:
#   1. Mint its key against the prod table (replace gm with the bot name):
#        TABLE_NAME=krabber-prod go run ./cmd/krabctl bot gm gm@krabber.net
#        TABLE_NAME=krabber-prod go run ./cmd/krabctl apikey gm "gm bot"
#   2. Store the key (the resource below holds only a placeholder):
#        aws ssm put-parameter --name /krabber/gm/api-key --type SecureString \
#          --overwrite --value kb_...
#
# Schedule times are UTC and so drift an hour across US daylight saving:
#   gm       cron(0 11 ...)  07:00 EDT / 06:00 EST  — morning
#   barnacle cron(0 16 ...)  12:00 EDT / 11:00 EST  — midday
#   holidays cron(0 12 ...)  08:00 EDT / 07:00 EST  — morning heads-up (day before)

locals {
  # Rush-hour slots (UTC): 08:00 / 12:00 / 16:00 ET across daylight saving.
  rush = {
    am   = "cron(0 12 * * ? *)" # 08:00 EDT / 07:00 EST
    noon = "cron(0 16 * * ? *)" # 12:00 EDT / 11:00 EST
    pm   = "cron(0 20 * * ? *)" # 16:00 EDT / 15:00 EST
  }

  bots = {
    gm = {
      description = "Posts a sunrise good-morning molt to krabber.net as @gm."
      schedules   = { morning = "cron(0 11 * * ? *)" }
      env         = {}
    }
    barnacle = {
      description = "Posts a daily Shell Prompt conversation starter as @barnacle."
      schedules   = { midday = "cron(0 16 * * ? *)" }
      env         = {}
    }
    holidays = {
      description = "Posts a day-before heads-up for days off (federal + NYC school) as @holidays."
      schedules   = { morning = "cron(0 12 * * ? *)" }
      env         = {}
    }
    ferry = {
      description = "Posts active NYC Ferry service alerts as @ferry."
      schedules   = local.rush
      env = {
        FEED_URL = "http://nycferry.connexionz.net/rtt/public/utility/gtfsrealtime.aspx/alert"
        # No ROUTES => watch every ferry route (the network is small).
      }
    }
    bus = {
      description = "Posts active MTA bus service alerts for watched Manhattan routes as @bus."
      schedules   = local.rush
      env = {
        FEED_URL = "https://gtfsrt.prod.obanyc.com/alerts"
        ROUTES   = "M11,M12,M20,M23,M42,M50,M57,M66,M72,M79,M104"
      }
    }
  }

  # Flatten bots x schedules into one map keyed "bot-slot" for the event wiring.
  bot_schedules = merge([
    for name, cfg in local.bots : {
      for slot, expr in cfg.schedules : "${name}-${slot}" => {
        bot      = name
        schedule = expr
      }
    }
  ]...)
}

resource "aws_ssm_parameter" "bot_api_key" {
  for_each    = local.bots
  name        = "/krabber/${each.key}/api-key"
  description = "Krabber API key for the @${each.key} bot (set by hand with put-parameter)."
  type        = "SecureString"
  value       = "set-me-with-put-parameter"

  # The real key is written out of band, so Terraform must not overwrite it.
  lifecycle {
    ignore_changes = [value]
  }
}

resource "aws_cloudwatch_log_group" "bot" {
  for_each          = local.bots
  name              = "/aws/lambda/krabber-${each.key}"
  retention_in_days = 14
}

resource "aws_lambda_function" "bot" {
  for_each         = local.bots
  function_name    = "krabber-${each.key}"
  description      = each.value.description
  role             = "arn:aws:iam::${local.account_id}:role/krabber-${each.key}"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  handler          = "bootstrap"
  filename         = "${path.module}/../../dist/${each.key}.zip"
  source_code_hash = filebase64sha256("${path.module}/../../dist/${each.key}.zip")
  memory_size      = 128
  timeout          = 30

  environment {
    variables = merge({
      KRABBER_API_URL = "https://${var.domain}/api/v1/molts"
      API_KEY_PARAM   = aws_ssm_parameter.bot_api_key[each.key].name
      USER_AGENT      = "krabber-${each.key} (+https://${var.domain}; support@${var.domain})"
    }, each.value.env)
  }

  depends_on = [aws_cloudwatch_log_group.bot]
}

resource "aws_cloudwatch_event_rule" "bot" {
  for_each            = local.bot_schedules
  name                = "krabber-${each.key}"
  description         = "Triggers the @${each.value.bot} bot (${each.key})."
  schedule_expression = each.value.schedule
}

resource "aws_cloudwatch_event_target" "bot" {
  for_each  = local.bot_schedules
  rule      = aws_cloudwatch_event_rule.bot[each.key].name
  target_id = each.value.bot
  arn       = aws_lambda_function.bot[each.value.bot].arn
}

resource "aws_lambda_permission" "bot_events" {
  for_each      = local.bot_schedules
  statement_id  = "events-${each.key}"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.bot[each.value.bot].function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.bot[each.key].arn
}

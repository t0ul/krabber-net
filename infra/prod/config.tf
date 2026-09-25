# App settings in SSM under /krabber/prod (the app reads them through
# SSM_PREFIX; each name becomes its upper-case variable). Turnstile keys are
# added by hand once you have them:
#
#   aws ssm put-parameter --name /krabber/prod/turnstile_site_key --type String --value ...
#   aws ssm put-parameter --name /krabber/prod/turnstile_secret --type SecureString --value ...

resource "random_password" "origin_verify" {
  length  = 48
  special = false
}

resource "aws_ssm_parameter" "origin_verify_secret" {
  name  = "/krabber/prod/origin_verify_secret"
  type  = "SecureString"
  value = random_password.origin_verify.result
}

# To rotate: copy the current value here, taint random_password.origin_verify,
# apply, and clear this one on the next rotation.
resource "aws_ssm_parameter" "origin_verify_secret_previous" {
  name  = "/krabber/prod/origin_verify_secret_previous"
  type  = "SecureString"
  value = random_password.origin_verify.result
}

resource "aws_ssm_parameter" "plain" {
  for_each = {
    mail_from      = "Krabber <no-reply@${var.domain}>"
    mail_daily_cap = "500"
    contact_email  = "support@${var.domain}"
  }

  name  = "/krabber/prod/${each.key}"
  type  = "String"
  value = each.value
}

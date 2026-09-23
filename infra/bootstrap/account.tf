resource "aws_iam_account_password_policy" "this" {
  minimum_password_length        = 14
  require_lowercase_characters   = true
  require_uppercase_characters   = true
  require_numbers                = true
  require_symbols                = true
  allow_users_to_change_password = true
  password_reuse_prevention      = 24
}

resource "aws_iam_account_alias" "this" {
  account_alias = var.account_alias
}

resource "aws_s3_account_public_access_block" "this" {
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_account_alternate_contact" "security" {
  count = var.security_contact_phone == "" ? 0 : 1

  alternate_contact_type = "SECURITY"
  name                   = var.security_contact_name
  title                  = "Owner"
  email_address          = var.alert_email
  phone_number           = var.security_contact_phone
}

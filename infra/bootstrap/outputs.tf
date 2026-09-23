output "state_bucket" {
  value = aws_s3_bucket.state.bucket
}

output "gha_plan_role_arn" {
  value = aws_iam_role.gha_plan.arn
}

output "gha_deploy_role_arn" {
  value = aws_iam_role.gha_deploy.arn
}

output "eb_service_role_arn" {
  value = aws_iam_role.eb_service.arn
}

output "eb_instance_profile_name" {
  value = aws_iam_instance_profile.eb_instance.name
}

output "canary_role_arn" {
  value = aws_iam_role.canary.arn
}

output "console_sign_in_url" {
  value = "https://${aws_iam_account_alias.this.account_alias}.signin.aws.amazon.com/console"
}

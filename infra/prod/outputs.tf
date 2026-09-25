output "site_url" {
  value = "https://${var.domain}"
}

output "beanstalk_application" {
  value = aws_elastic_beanstalk_application.krabber.name
}

output "beanstalk_environment" {
  value = aws_elastic_beanstalk_environment.prod.name
}

output "beanstalk_cname" {
  value = aws_elastic_beanstalk_environment.prod.cname
}

output "artifacts_bucket" {
  value = aws_s3_bucket.artifacts.bucket
}

output "distribution_id" {
  value = aws_cloudfront_distribution.site.id
}

output "distribution_arn" {
  description = "For M7: subscribe this distribution to the flat-rate Free plan."
  value       = aws_cloudfront_distribution.site.arn
}

output "web_acl_arn" {
  value = aws_wafv2_web_acl.site.arn
}

output "table_name" {
  value = aws_dynamodb_table.main.name
}

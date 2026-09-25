# TLS between CloudFront and the instance (PLAN.md section 7.2). An ACM
# exportable certificate ($7 per 198-day certificate, renewed by ACM) for
# origin.<domain>; the instance exports it at deploy time and daily after
# (.platform/hooks/prebuild/10_origin_cert.sh). CloudFront accepts a
# certificate that matches the origin's domain name even though it forwards
# the viewer's Host header.
#
# Switching over without downtime:
#   1. apply with the defaults, then deploy: the instance serves 443 too
#   2. origin_https = true: CloudFront talks to origin.<domain> over HTTPS
#   3. origin_http_open = false: port 80 closes

locals {
  origin_host = "origin.${var.domain}"
}

resource "aws_acm_certificate" "origin" {
  domain_name       = local.origin_host
  validation_method = "DNS"

  options {
    export = "ENABLED"
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "origin_cert_validation" {
  for_each = {
    for o in aws_acm_certificate.origin.domain_validation_options : o.domain_name => {
      name   = o.resource_record_name
      type   = o.resource_record_type
      record = o.resource_record_value
    }
  }

  zone_id         = var.hosted_zone_id
  name            = each.value.name
  type            = each.value.type
  ttl             = 300
  records         = [each.value.record]
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "origin" {
  certificate_arn         = aws_acm_certificate.origin.arn
  validation_record_fqdns = [for r in aws_route53_record.origin_cert_validation : r.fqdn]
}

resource "aws_route53_record" "origin" {
  zone_id = var.hosted_zone_id
  name    = local.origin_host
  type    = "CNAME"
  ttl     = 300
  records = [aws_elastic_beanstalk_environment.prod.cname]
}

# Its own group: the CloudFront prefix list takes most of a group's rules.
resource "aws_security_group" "origin_https" {
  name        = "krabber-eb-origin-https"
  description = "HTTPS from CloudFront only"
  vpc_id      = data.aws_vpc.default.id
}

resource "aws_vpc_security_group_ingress_rule" "cloudfront_https" {
  security_group_id = aws_security_group.origin_https.id
  description       = "CloudFront origin-facing"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  prefix_list_id    = data.aws_ec2_managed_prefix_list.cloudfront.id
}

# CloudFront + WAF + TLS + DNS (PLAN.md sections 1, 3.4 and 7.2). Only
# features the flat-rate Free plan allows: managed cache and origin request
# policies, at most 5 WAF rules, no real-time logs.

locals {
  www = "www.${var.domain}"
}

# ---------------------------------------------------------------------------
# Certificate (us-east-1 for CloudFront)
# ---------------------------------------------------------------------------

resource "aws_acm_certificate" "site" {
  provider                  = aws.us_east_1
  domain_name               = var.domain
  subject_alternative_names = [local.www]
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "cert_validation" {
  for_each = {
    for o in aws_acm_certificate.site.domain_validation_options : o.domain_name => {
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

resource "aws_acm_certificate_validation" "site" {
  provider                = aws.us_east_1
  certificate_arn         = aws_acm_certificate.site.arn
  validation_record_fqdns = [for r in aws_route53_record.cert_validation : r.fqdn]
}

# ---------------------------------------------------------------------------
# WAF: exactly 5 rules, the Free plan's limit
# ---------------------------------------------------------------------------

resource "aws_wafv2_web_acl" "site" {
  provider    = aws.us_east_1
  name        = "krabber"
  description = "krabber.net edge rules"
  scope       = "CLOUDFRONT"

  default_action {
    allow {}
  }

  # 1. Maintenance switch: matches everything; counts normally, blocks when
  #    maintenance_mode is on.
  rule {
    name     = "maintenance"
    priority = 0

    action {
      dynamic "count" {
        for_each = var.maintenance_mode ? [] : [1]
        content {}
      }
      dynamic "block" {
        for_each = var.maintenance_mode ? [1] : []
        content {}
      }
    }

    statement {
      size_constraint_statement {
        comparison_operator = "GE"
        size                = 0
        field_to_match {
          uri_path {}
        }
        text_transformation {
          priority = 0
          type     = "NONE"
        }
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "maintenance"
      sampled_requests_enabled   = false
    }
  }

  # 2. Login, signup, password and resend pages: 20 requests per IP per 5 minutes.
  rule {
    name     = "auth-rate"
    priority = 1

    action {
      block {}
    }

    statement {
      rate_based_statement {
        limit                 = 20
        evaluation_window_sec = 300
        aggregate_key_type    = "IP"

        scope_down_statement {
          byte_match_statement {
            positional_constraint = "STARTS_WITH"
            search_string         = "/krab/"
            field_to_match {
              uri_path {}
            }
            text_transformation {
              priority = 0
              type     = "LOWERCASE"
            }
          }
        }
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "auth-rate"
      sampled_requests_enabled   = true
    }
  }

  # 3. Everything: 500 requests per IP per 5 minutes.
  rule {
    name     = "all-rate"
    priority = 2

    action {
      block {}
    }

    statement {
      rate_based_statement {
        limit                 = 500
        evaluation_window_sec = 300
        aggregate_key_type    = "IP"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "all-rate"
      sampled_requests_enabled   = true
    }
  }

  # 4. Known-bad IPs.
  rule {
    name     = "ip-reputation"
    priority = 3

    override_action {
      none {}
    }

    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesAmazonIpReputationList"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "ip-reputation"
      sampled_requests_enabled   = true
    }
  }

  # 5. Common exploits. Counting for the first week to check it doesn't break
  #    htmx posts; then switch override_action to none {} to block.
  rule {
    name     = "common"
    priority = 4

    override_action {
      count {}
    }

    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesCommonRuleSet"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "common"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "krabber"
    sampled_requests_enabled   = true
  }
}

# ---------------------------------------------------------------------------
# CloudFront
# ---------------------------------------------------------------------------

resource "aws_cloudfront_function" "www_redirect" {
  name    = "krabber-www-redirect"
  runtime = "cloudfront-js-2.0"
  comment = "www.krabber.net to krabber.net"
  publish = true
  code    = <<-JS
    function handler(event) {
      var request = event.request;
      var host = request.headers.host && request.headers.host.value;
      if (host !== "${local.www}") return request;
      var qs = [];
      for (var key in request.querystring) {
        var v = request.querystring[key];
        qs.push(encodeURIComponent(key) + (v.value ? "=" + encodeURIComponent(v.value) : ""));
      }
      var location = "https://${var.domain}" + request.uri + (qs.length ? "?" + qs.join("&") : "");
      return { statusCode: 301, statusDescription: "Moved Permanently", headers: { location: { value: location } } };
    }
  JS
}

data "aws_cloudfront_cache_policy" "disabled" {
  name = "Managed-CachingDisabled"
}

data "aws_cloudfront_cache_policy" "optimized" {
  name = "Managed-CachingOptimized"
}

# Keeps ?v= in the cache key and honors the app's Cache-Control.
data "aws_cloudfront_cache_policy" "origin_headers_qs" {
  name = "UseOriginCacheControlHeaders-QueryStrings"
}

data "aws_cloudfront_origin_request_policy" "all_viewer" {
  name = "Managed-AllViewerAndCloudFrontHeaders-2022-06"
}

locals {
  origin_id = "beanstalk"
  # Cached paths: static files (versioned with ?v=) and the generated
  # avatars and banners (content-addressed by trait code).
  cached_paths = {
    "/static/*" = data.aws_cloudfront_cache_policy.origin_headers_qs.id
    "/avatar/*" = data.aws_cloudfront_cache_policy.optimized.id
    "/banner/*" = data.aws_cloudfront_cache_policy.optimized.id
  }
}

resource "aws_cloudfront_distribution" "site" {
  enabled         = true
  is_ipv6_enabled = true
  http_version    = "http2and3"
  comment         = var.domain
  aliases         = [var.domain, local.www]
  web_acl_id      = aws_wafv2_web_acl.site.arn

  origin {
    origin_id   = local.origin_id
    domain_name = aws_elastic_beanstalk_environment.prod.cname

    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "http-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }

    custom_header {
      name  = "X-Origin-Verify"
      value = random_password.origin_verify.result
    }
  }

  default_cache_behavior {
    target_origin_id         = local.origin_id
    viewer_protocol_policy   = "redirect-to-https"
    allowed_methods          = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods           = ["GET", "HEAD"]
    cache_policy_id          = data.aws_cloudfront_cache_policy.disabled.id
    origin_request_policy_id = data.aws_cloudfront_origin_request_policy.all_viewer.id
    compress                 = true

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.www_redirect.arn
    }
  }

  dynamic "ordered_cache_behavior" {
    for_each = local.cached_paths
    content {
      path_pattern           = ordered_cache_behavior.key
      target_origin_id       = local.origin_id
      viewer_protocol_policy = "redirect-to-https"
      allowed_methods        = ["GET", "HEAD"]
      cached_methods         = ["GET", "HEAD"]
      cache_policy_id        = ordered_cache_behavior.value
      compress               = true

      function_association {
        event_type   = "viewer-request"
        function_arn = aws_cloudfront_function.www_redirect.arn
      }
    }
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    acm_certificate_arn      = aws_acm_certificate_validation.site.certificate_arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }
}

# ---------------------------------------------------------------------------
# DNS
# ---------------------------------------------------------------------------

resource "aws_route53_record" "site" {
  for_each = {
    apex_a    = { name = var.domain, type = "A" }
    apex_aaaa = { name = var.domain, type = "AAAA" }
    www_a     = { name = local.www, type = "A" }
    www_aaaa  = { name = local.www, type = "AAAA" }
  }

  zone_id = var.hosted_zone_id
  name    = each.value.name
  type    = each.value.type

  alias {
    name                   = aws_cloudfront_distribution.site.domain_name
    zone_id                = aws_cloudfront_distribution.site.hosted_zone_id
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "caa" {
  zone_id = var.hosted_zone_id
  name    = var.domain
  type    = "CAA"
  ttl     = 3600
  records = [
    "0 issue \"amazon.com\"",
    "0 issuewild \";\"",
  ]
}

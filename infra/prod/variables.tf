variable "region" {
  description = "Primary region."
  type        = string
  default     = "us-east-2"
}

variable "domain" {
  description = "The site's domain."
  type        = string
  default     = "krabber.net"
}

variable "hosted_zone_id" {
  description = "Route 53 hosted zone for the domain (created when the domain was registered)."
  type        = string
  default     = "Z02627693RXH6QHSOUU5O"
}

variable "alert_email" {
  description = "Where alarms, budgets, bounces and DMARC reports go."
  type        = string
}

variable "instance_type" {
  description = "Beanstalk instance type: t4g.micro, t4g.small past about 5,000 daily active krabs (PLAN.md section 2.2)."
  type        = string
  default     = "t4g.micro"
}

variable "max_krabs" {
  description = "MAX_KRABS: signups close once this many krabs can sign in. Raise it with the throughput caps below (PLAN.md section 4.1)."
  type        = number
  default     = 3000
}

variable "origin_https" {
  description = "CloudFront reaches the instance over HTTPS at origin.<domain> (step 2 in origin_tls.tf)."
  type        = bool
  default     = true
}

variable "origin_http_open" {
  description = "Port 80 stays open to CloudFront; close it once origin_https has served traffic (step 3 in origin_tls.tf)."
  type        = bool
  default     = false
}

variable "table_caps" {
  description = "On-demand maximum throughput (units per second) for the table and each index; the launch stage of PLAN.md section 4.1."
  type = object({
    table   = object({ reads = number, writes = number })
    indexes = map(object({ reads = number, writes = number }))
  })
  default = {
    table = { reads = 50, writes = 40 }
    indexes = {
      GSI2 = { reads = 150, writes = 10 }
      GSI3 = { reads = 25, writes = 10 }
      GSI5 = { reads = 25, writes = 10 }
      GSI6 = { reads = 25, writes = 10 }
      GSI7 = { reads = 25, writes = 10 }
      GSI8 = { reads = 10, writes = 5 }
    }
  }
}

variable "maintenance_mode" {
  description = "Block every request at WAF (the maintenance switch, PLAN.md section 7.2)."
  type        = bool
  default     = false
}

variable "monthly_budget" {
  description = "Monthly budget in USD; raise it with max_krabs (PLAN.md section 3.4)."
  type        = number
  default     = 15
}

variable "daily_budget" {
  description = "Daily budget in USD."
  type        = number
  default     = 1.5
}

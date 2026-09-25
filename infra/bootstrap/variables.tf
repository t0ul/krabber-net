variable "region" {
  description = "Primary region for all Krabber resources."
  type        = string
  default     = "us-east-2"
}

variable "github_repo" {
  description = "GitHub repository (owner/name) allowed to assume the CI roles."
  type        = string
  default     = "t0ul/krabber-net"
}

# GitHub's OIDC subjects name the owner and repository with their numeric IDs
# (repo:t0ul@298514713/krabber-net@1383711855:...), which a recreated repo or
# a new account with the same name can't match. Seen in CloudTrail's
# AssumeRoleWithWebIdentity events.
variable "github_owner_id" {
  description = "Numeric ID of the repository's owner."
  type        = string
  default     = "298514713"
}

variable "github_repo_id" {
  description = "Numeric ID of the repository."
  type        = string
  default     = "1383711855"
}

variable "admin_user_name" {
  description = "IAM user used for local admin work and bootstrap applies."
  type        = string
  default     = "t"
}

variable "account_alias" {
  description = "Account alias used in the console sign-in URL."
  type        = string
  default     = "krabber-net"
}

variable "alert_email" {
  description = "Email for the security alternate contact (and later for alarms and budgets)."
  type        = string
}

variable "security_contact_name" {
  description = "Name on the security alternate contact."
  type        = string
  default     = "Krabber Security"
}

variable "security_contact_phone" {
  description = "Phone number (E.164, for example +15555550100) for the security alternate contact. AWS requires one; leave empty to skip the contact."
  type        = string
  default     = ""
}

variable "allowed_instance_types" {
  description = "EC2 instance types the CI deploy role may launch directly."
  type        = list(string)
  default     = ["t4g.nano", "t4g.micro", "t4g.small"]
}

variable "aws_region" {
  description = "Region to deploy into. Must match the app stack's region."
  type        = string
  default     = "us-east-1"
}

# No default: a default pointing at the original repo would silently provision a
# CI role that trusts someone else's workflows, and the failure (an
# AssumeRoleWithWebIdentity denial) gives no hint at the cause.
variable "github_repo" {
  description = "owner/name of the repository whose workflows may assume the CI role."
  type        = string
}

variable "create_oidc_provider" {
  description = <<-EOT
    Whether to create the account's GitHub OIDC provider. It is a per-issuer
    singleton: set true if no other stack in this account has created it, false to
    reuse the existing one. Check with:
    aws iam list-open-id-connect-providers
  EOT
  type        = bool
  default     = true
}

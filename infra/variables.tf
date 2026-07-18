variable "aws_region" {
  description = "Region to deploy into. Bedrock model availability varies by region."
  type        = string
  default     = "us-east-1"
}

variable "image_tag" {
  description = "ECR image tag to deploy. Override with a git SHA to pin an exact build."
  type        = string
  default     = "latest"
}

# No default: the keys are a secret, so they must be supplied deliberately via
# TF_VAR_api_keys or a gitignored tfvars file rather than living in the repo.
variable "api_keys" {
  description = "Comma-separated API keys clients present in the X-API-Key header."
  type        = string
  sensitive   = true
}

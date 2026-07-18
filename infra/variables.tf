variable "image_tag" {
  description = "ECR image tag to deploy. Override with a git SHA to pin an exact build."
  type        = string
  default     = "latest"
}

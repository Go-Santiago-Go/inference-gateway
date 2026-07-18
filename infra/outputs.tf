# The registry is owned by the bootstrap stack so it survives this stack's
# teardown. Read it here so the ECS service can reference the image URL.
data "aws_ecr_repository" "gateway" {
  name = "inference-gateway"
}

output "ecr_url" {
  value = data.aws_ecr_repository.gateway.repository_url
}

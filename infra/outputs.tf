# infra/outputs.tf

# The full registry URL CI tags and pushes the image to in Phase 9.
output "ecr_url" {
  value = aws_ecr_repository.gateway.repository_url
}

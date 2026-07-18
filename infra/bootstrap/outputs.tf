# The registry URL CI tags and pushes images to.
output "ecr_url" {
  value = aws_ecr_repository.gateway.repository_url
}

# Set as the AWS_ROLE_ARN repository variable in GitHub so deploy.yml can assume it.
output "github_ci_role_arn" {
  value = aws_iam_role.github_ci.arn
}

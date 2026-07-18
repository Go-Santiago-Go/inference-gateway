# The registry is owned by the bootstrap stack so it survives this stack's
# teardown. Read it here so the ECS service can reference the image URL.
data "aws_ecr_repository" "gateway" {
  name = "inference-gateway"
}

output "ecr_url" {
  value = data.aws_ecr_repository.gateway.repository_url
}

# The gateway's public HTTPS endpoint. Build the client with this as
# VITE_API_BASE so the browser calls the deployed API.
output "gateway_url" {
  value = one(aws_ecs_express_gateway_service.gateway.ingress_paths).endpoint
}

# Where the client is served from. Already in the gateway's CORS allowlist.
output "client_url" {
  value = "https://${aws_cloudfront_distribution.client.domain_name}"
}

# Upload target for the built client bundle.
output "client_bucket" {
  value = aws_s3_bucket.client.bucket
}

# Needed to invalidate the CDN cache after uploading a new build.
output "client_distribution_id" {
  value = aws_cloudfront_distribution.client.id
}

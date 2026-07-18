# The API keys the gateway authenticates against. Held as a SecureString so the
# value is KMS-encrypted at rest, and injected into the container at startup by
# the execution role rather than baked into the image.
#
# Terraform state contains this value in plaintext, which is why state is local
# and gitignored here. A team deployment would use an encrypted remote backend.
resource "aws_ssm_parameter" "api_keys" {
  name  = "/inference-gateway/api-keys"
  type  = "SecureString"
  value = var.api_keys
}

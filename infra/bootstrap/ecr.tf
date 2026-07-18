# The image registry lives in the bootstrap stack because it must outlive the
# nightly `terraform destroy` of the main stack: destroying it would delete every
# pushed image and break any deploy that references them by digest.
resource "aws_ecr_repository" "gateway" {
  name = "inference-gateway"
}

# Static hosting for the React client: a private S3 bucket fronted by CloudFront.
# The bucket is never public; CloudFront reaches it through an Origin Access
# Control, so the only way to the objects is through the distribution.

# Bucket names are globally unique across all AWS accounts, so the account ID is
# appended to keep this deployable in someone else's account without collision.
resource "aws_s3_bucket" "client" {
  bucket        = "inference-gateway-client-${data.aws_caller_identity.current.account_id}"
  force_destroy = true # demo infrastructure: let destroy remove the built assets
}

# Defense in depth: even if a bucket policy or ACL were misconfigured later, these
# account-level blocks keep the objects from becoming publicly readable.
resource "aws_s3_bucket_public_access_block" "client" {
  bucket                  = aws_s3_bucket.client.id
  block_public_acls       = true
  block_public_policy     = false # the OAC policy below is attached deliberately
  ignore_public_acls      = true
  restrict_public_buckets = false
}

# Origin Access Control is the current mechanism for CloudFront-to-S3 auth; it
# signs requests with SigV4 and replaces the legacy Origin Access Identity.
resource "aws_cloudfront_origin_access_control" "client" {
  name                              = "inference-gateway-client"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

resource "aws_cloudfront_distribution" "client" {
  enabled             = true
  default_root_object = "index.html"

  # PriceClass_100 restricts edge locations to North America and Europe, which is
  # the cheapest tier and ample for a demo.
  price_class = "PriceClass_100"

  origin {
    domain_name              = aws_s3_bucket.client.bucket_regional_domain_name
    origin_id                = "client-bucket"
    origin_access_control_id = aws_cloudfront_origin_access_control.client.id
  }

  default_cache_behavior {
    target_origin_id       = "client-bucket"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD", "OPTIONS"]
    cached_methods         = ["GET", "HEAD"]

    # AWS-managed CachingOptimized policy: sensible defaults for static assets
    # without hand-rolling TTLs.
    cache_policy_id = "658327ea-f89d-4fab-a63d-7e88639e58f6"
  }

  # The client is a single-page app: paths it owns do not exist as S3 objects, so
  # S3 answers 403/404 and CloudFront rewrites those to index.html for the router.
  custom_error_response {
    error_code         = 403
    response_code      = 200
    response_page_path = "/index.html"
  }

  custom_error_response {
    error_code         = 404
    response_code      = 200
    response_page_path = "/index.html"
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  # The default *.cloudfront.net certificate; a custom domain would need ACM.
  viewer_certificate {
    cloudfront_default_certificate = true
  }
}

# Grant the distribution, and only the distribution, read access to the objects.
resource "aws_s3_bucket_policy" "client" {
  bucket = aws_s3_bucket.client.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "cloudfront.amazonaws.com" }
      Action    = "s3:GetObject"
      Resource  = "${aws_s3_bucket.client.arn}/*"
      Condition = {
        StringEquals = {
          "AWS:SourceArn" = aws_cloudfront_distribution.client.arn
        }
      }
    }]
  })
}

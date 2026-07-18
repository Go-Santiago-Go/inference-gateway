# Keyless CI auth: instead of storing a long-lived AWS access key in GitHub, we
# federate GitHub's OIDC identity provider and let a workflow in THIS repo assume
# a role scoped to pushing one ECR repository. The workflow trades a short-lived,
# signed GitHub token for temporary AWS credentials via
# sts:AssumeRoleWithWebIdentity. Nothing to leak, nothing to rotate.

# The GitHub OIDC provider is an account-level singleton, one per issuer URL. A
# fresh account has none and must create it; an account where another project
# already did must reuse it, since creating a second one fails. create_oidc_provider
# selects between the two, and reusing via a data source also keeps a destroy here
# from removing a provider other stacks depend on.
resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 1 : 0

  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]
}

data "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 0 : 1

  url = "https://token.actions.githubusercontent.com"
}

# One place for the ARN so the trust policy does not care which branch produced it.
locals {
  github_oidc_arn = var.create_oidc_provider ? one(aws_iam_openid_connect_provider.github[*].arn) : one(data.aws_iam_openid_connect_provider.github[*].arn)
}

# Trust policy: WHO may assume the CI role.
data "aws_iam_policy_document" "github_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.github_oidc_arn]
    }

    # The token's audience must be STS.
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    # The token's subject must be a workflow in this repo. Without this condition
    # any GitHub repository could assume the role. `:*` allows any branch; tighten
    # to `:ref:refs/heads/main` once the pipeline is proven.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${var.github_repo}:*"]
    }
  }
}

resource "aws_iam_role" "github_ci" {
  name               = "inference-gateway-github-ci"
  assume_role_policy = data.aws_iam_policy_document.github_assume.json
}

# Permissions policy: WHAT the CI role may do.
data "aws_iam_policy_document" "github_ecr_push" {
  # `docker login`: GetAuthorizationToken is an account-level call and cannot be
  # scoped to a single repository.
  statement {
    sid       = "EcrAuth"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  # The push itself, scoped to this project's repository only.
  statement {
    sid = "EcrPush"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:InitiateLayerUpload",
      "ecr:UploadLayerPart",
      "ecr:CompleteLayerUpload",
      "ecr:PutImage",
      "ecr:BatchGetImage",
    ]
    resources = [aws_ecr_repository.gateway.arn]
  }
}

resource "aws_iam_role_policy" "github_ecr_push" {
  name   = "inference-gateway-github-ecr-push"
  role   = aws_iam_role.github_ci.id
  policy = data.aws_iam_policy_document.github_ecr_push.json
}

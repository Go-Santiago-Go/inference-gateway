# Keyless CI auth: instead of storing a long-lived AWS access key in GitHub, we
# federate GitHub's OIDC identity provider and let a workflow in THIS repo assume
# a role scoped to pushing one ECR repository. The workflow trades a short-lived,
# signed GitHub token for temporary AWS credentials via
# sts:AssumeRoleWithWebIdentity. Nothing to leak, nothing to rotate.

# The GitHub OIDC provider is an account-level singleton (one per issuer URL) and
# already exists in this account, created by the go-rag-api stack. Reading it with
# a data source avoids ownership conflicts and keeps `terraform destroy` here from
# removing a provider that another project depends on.
data "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"
}

# Trust policy: WHO may assume the CI role.
data "aws_iam_policy_document" "github_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [data.aws_iam_openid_connect_provider.github.arn]
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

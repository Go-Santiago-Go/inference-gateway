# infra/iam.tf

# Identity the running gateway process assumes. Its permissions (Bedrock) come next.
resource "aws_iam_role" "task" {
  name = "inference-gateway-task"

  # Trust policy: only the ECS tasks service may assume this role.
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
    }]
  })
}

# What the task role may do: invoke Bedrock models, streaming and non-streaming.
resource "aws_iam_role_policy" "task_bedrock" {
  name = "bedrock-invoke"
  role = aws_iam_role.task.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "bedrock:InvokeModel",
        "bedrock:InvokeModelWithResponseStream", # the ConverseStream path
      ]
      # The gateway fronts a cross-region inference profile (the "us." model ID
      # prefix), so Bedrock routes each call to whichever US region has capacity.
      # That needs permission on the profile itself AND on the foundation model in
      # every region the profile can route to; granting only the local foundation
      # model returns AccessDenied before the request ever reaches a model.
      Resource = [
        "arn:aws:bedrock:${var.aws_region}:${data.aws_caller_identity.current.account_id}:inference-profile/*",
        "arn:aws:bedrock:${var.aws_region}::foundation-model/*",
        "arn:aws:bedrock:us-east-2::foundation-model/*",
        "arn:aws:bedrock:us-west-2::foundation-model/*",
      ]
    }]
  })
}

# Identity the ECS platform uses to pull the image and inject secrets at startup.
resource "aws_iam_role" "execution" {
  name = "inference-gateway-execution"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
    }]
  })
}

# AWS-maintained baseline: pull from ECR + ship logs to CloudWatch.
resource "aws_iam_role_policy_attachment" "execution_baseline" {
  role       = aws_iam_role.execution.id
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# Look up the current account ID so ARNs aren't hardcoded.
data "aws_caller_identity" "current" {}

# Let the execution role read ONLY the api-keys parameter at startup.
resource "aws_iam_role_policy" "execution_ssm" {
  name = "read-api-keys"
  role = aws_iam_role.execution.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "ssm:GetParameters"
      Resource = aws_ssm_parameter.api_keys.arn
    }]
  })
}

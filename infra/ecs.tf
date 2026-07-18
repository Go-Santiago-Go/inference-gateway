# Express Mode provisions the Fargate service, ALB, TLS certificate, auto scaling,
# and networking from a container image, so this stack declares the service and
# the two roles it needs rather than a VPC, listeners, and target groups.

# Identity ECS itself assumes to provision infrastructure on the account's behalf.
# Note the principal: ecs.amazonaws.com is the control plane, distinct from the
# ecs-tasks.amazonaws.com principal the task and execution roles trust.
resource "aws_iam_role" "infrastructure" {
  name = "inference-gateway-infrastructure"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "ecs.amazonaws.com" }
    }]
  })
}

# AWS-maintained policy scoped to what Express Mode provisions: load balancing,
# security groups, SSL certificates, and auto scaling.
resource "aws_iam_role_policy_attachment" "infrastructure_express" {
  role       = aws_iam_role.infrastructure.id
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSInfrastructureRoleforExpressGatewayServices"
}

# Retention is set explicitly because the default is never-expire, which bills
# indefinitely for logs that stop being useful after a demo. One day is the
# minimum CloudWatch accepts.
resource "aws_cloudwatch_log_group" "gateway" {
  name              = "/ecs/inference-gateway"
  retention_in_days = 1
}

resource "aws_ecs_express_gateway_service" "gateway" {
  service_name            = "inference-gateway"
  execution_role_arn      = aws_iam_role.execution.arn
  infrastructure_role_arn = aws_iam_role.infrastructure.arn
  task_role_arn           = aws_iam_role.task.arn

  # Smallest Fargate size: the gateway relays a stream and holds no model state,
  # so it is I/O bound on Bedrock rather than CPU bound.
  cpu    = "256"
  memory = "512"

  health_check_path = "/health"

  # Block until the service reaches steady state so a failed deploy fails the
  # apply instead of reporting success while tasks crash-loop.
  wait_for_steady_state = true

  primary_container {
    image          = "${data.aws_ecr_repository.gateway.repository_url}:${var.image_tag}"
    container_port = 8080

    aws_logs_configuration {
      log_group         = aws_cloudwatch_log_group.gateway.name
      log_stream_prefix = "gateway"
    }

    environment {
      name  = "AWS_REGION"
      value = "us-east-1"
    }

    # API keys are injected from SSM at task startup by the execution role, so
    # they never appear in the task definition, the image, or Terraform state.
    secret {
      name       = "API_KEYS"
      value_from = "arn:aws:ssm:us-east-1:${data.aws_caller_identity.current.account_id}:parameter/inference-gateway/api-keys"
    }
  }

  scaling_target {
    auto_scaling_metric       = "AVERAGE_CPU"
    auto_scaling_target_value = 70
    min_task_count            = 1
    max_task_count            = 2
  }
}

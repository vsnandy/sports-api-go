data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

locals {
  name    = "sports-api"
  ssm_arn = "arn:aws:ssm:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:parameter"
}

# --- Players dump cache ---

resource "aws_s3_bucket" "players" {
  bucket_prefix = "sports-api-players-"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "players" {
  bucket                  = aws_s3_bucket.players.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "players" {
  bucket = aws_s3_bucket.players.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# --- Logs ---

resource "aws_cloudwatch_log_group" "api" {
  name              = "/aws/lambda/${local.name}"
  retention_in_days = 14
}

# --- IAM ---

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "api" {
  name               = "${local.name}-lambda"
  assume_role_policy = data.aws_iam_policy_document.assume.json
  # Created by deploy/bootstrap. The CI deploy role may only write this role with the boundary attached.
  permissions_boundary = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:policy/${local.name}-lambda-boundary"
}

data "aws_iam_policy_document" "api" {
  statement {
    actions = ["ssm:GetParameters"]
    resources = [
      for p in [var.ssm_api_key_param, var.ssm_espn_s2_param, var.ssm_espn_swid_param] : "${local.ssm_arn}${p}"
    ]
  }
  statement {
    actions   = ["kms:Decrypt"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${data.aws_region.current.name}.amazonaws.com"]
    }
  }
  statement {
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${aws_s3_bucket.players.arn}/players/*"]
  }
  # Without ListBucket, GetObject on a missing key returns AccessDenied instead of NoSuchKey.
  # The bucket is private and single-purpose, so this isn't scoped further: a prefix
  # condition on ListBucket isn't reliably evaluated for the 404-vs-403 decision on GetObject.
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.players.arn]
  }
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.api.arn}:*"]
  }
}

resource "aws_iam_role_policy" "api" {
  role   = aws_iam_role.api.id
  policy = data.aws_iam_policy_document.api.json
}

# --- Lambda ---

resource "aws_lambda_function" "api" {
  function_name    = local.name
  role             = aws_iam_role.api.arn
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  handler          = "bootstrap"
  filename         = var.lambda_zip
  source_code_hash = filebase64sha256(var.lambda_zip)
  memory_size      = 512
  timeout          = 15

  environment {
    variables = {
      SLEEPER_USERNAME    = var.sleeper_username
      ESPN_LEAGUE_IDS     = join(",", var.espn_league_ids)
      SSM_API_KEY_PARAM   = var.ssm_api_key_param
      SSM_ESPN_S2_PARAM   = var.ssm_espn_s2_param
      SSM_ESPN_SWID_PARAM = var.ssm_espn_swid_param
      PLAYERS_BUCKET      = aws_s3_bucket.players.bucket
    }
  }

  depends_on = [aws_cloudwatch_log_group.api, aws_iam_role_policy.api]
}

# --- API Gateway HTTP API ---

resource "aws_apigatewayv2_api" "api" {
  name          = local.name
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_integration" "api" {
  api_id                 = aws_apigatewayv2_api.api.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.api.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "default" {
  api_id    = aws_apigatewayv2_api.api.id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.api.id}"
}

resource "aws_apigatewayv2_stage" "default" {
  api_id      = aws_apigatewayv2_api.api.id
  name        = "$default"
  auto_deploy = true
}

resource "aws_lambda_permission" "apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.api.execution_arn}/*/*"
}

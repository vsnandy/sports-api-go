# Policy documents are built with jsonencode from deterministic ARNs so they are
# fully known at plan time (and testable with a mocked provider).
locals {
  oidc_provider_arn = "arn:aws:iam::${local.account_id}:oidc-provider/${local.oidc_host}"
  state_arn         = "arn:aws:s3:::${local.state_bucket}"
  players_arn       = "arn:aws:s3:::${local.name}-players-*"
  lambda_arn        = "arn:aws:lambda:${var.region}:${local.account_id}:function:${local.name}"
  lambda_role_arn   = "arn:aws:iam::${local.account_id}:role/${local.name}-lambda"
  boundary_arn      = "arn:aws:iam::${local.account_id}:policy/${local.name}-lambda-boundary"
  log_group_arn     = "arn:aws:logs:${var.region}:${local.account_id}:log-group:/aws/lambda/${local.name}"
  ssm_param_prefix  = "arn:aws:ssm:${var.region}:${local.account_id}:parameter${var.ssm_prefix}"
  apigw_arns = [
    "arn:aws:apigateway:${var.region}::/apis",
    "arn:aws:apigateway:${var.region}::/apis/*",
    "arn:aws:apigateway:${var.region}::/tags/*",
  ]
  kms_via_ssm = { StringEquals = { "kms:ViaService" = "ssm.${var.region}.amazonaws.com" } }

  github_subjects = {
    plan   = "repo:${var.github_repo}:pull_request"
    deploy = "repo:${var.github_repo}:ref:refs/heads/main"
  }
  github_trust = {
    for role, sub in local.github_subjects : role => jsonencode({
      Version = "2012-10-17"
      Statement = [{
        Effect    = "Allow"
        Principal = { Federated = local.oidc_provider_arn }
        Action    = "sts:AssumeRoleWithWebIdentity"
        Condition = {
          StringEquals = {
            "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
            "token.actions.githubusercontent.com:sub" = sub
          }
        }
      }]
    })
  }

  # Read-only access Terraform needs to refresh deploy/terraform. No SSM or KMS.
  read_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "StateList", Effect = "Allow", Action = ["s3:ListBucket"], Resource = [local.state_arn] },
      { Sid = "StateRead", Effect = "Allow", Action = ["s3:GetObject"], Resource = ["${local.state_arn}/sports-api/*"] },
      { Sid = "PlayersBucketRead", Effect = "Allow", Action = ["s3:Get*", "s3:List*"], Resource = [local.players_arn] },
      { Sid = "LambdaRead", Effect = "Allow", Action = ["lambda:Get*", "lambda:List*"], Resource = [local.lambda_arn] },
      { Sid = "ApiGatewayRead", Effect = "Allow", Action = ["apigateway:GET"], Resource = local.apigw_arns },
      {
        Sid      = "LambdaRoleRead"
        Effect   = "Allow"
        Action   = ["iam:GetRole", "iam:GetRolePolicy", "iam:ListRolePolicies", "iam:ListAttachedRolePolicies", "iam:ListRoleTags"]
        Resource = [local.lambda_role_arn]
      },
      { Sid = "LogGroupsDescribe", Effect = "Allow", Action = ["logs:DescribeLogGroups"], Resource = ["*"] },
      { Sid = "LogGroupTags", Effect = "Allow", Action = ["logs:ListTagsForResource", "logs:ListTagsLogGroup"], Resource = ["${local.log_group_arn}*"] },
    ]
  })

  # Write access for deploys. Role writes require the boundary; removing it is denied.
  deploy_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "StateWrite", Effect = "Allow", Action = ["s3:PutObject", "s3:DeleteObject"], Resource = ["${local.state_arn}/sports-api/*"] },
      {
        Sid      = "PlayersBucketManage"
        Effect   = "Allow"
        Action   = ["s3:CreateBucket", "s3:DeleteBucket", "s3:PutBucket*", "s3:PutEncryptionConfiguration", "s3:PutLifecycleConfiguration", "s3:ListBucketVersions"]
        Resource = [local.players_arn]
      },
      { Sid = "PlayersObjectsDelete", Effect = "Allow", Action = ["s3:DeleteObject", "s3:DeleteObjectVersion"], Resource = ["${local.players_arn}/*"] },
      {
        Sid    = "LambdaManage"
        Effect = "Allow"
        Action = [
          "lambda:CreateFunction", "lambda:UpdateFunctionCode", "lambda:UpdateFunctionConfiguration", "lambda:DeleteFunction",
          "lambda:AddPermission", "lambda:RemovePermission", "lambda:TagResource", "lambda:UntagResource",
        ]
        Resource = [local.lambda_arn]
      },
      # API Gateway authorizes by HTTP verb; tagging is POST/DELETE on /tags/*.
      { Sid = "ApiGatewayManage", Effect = "Allow", Action = ["apigateway:POST", "apigateway:PUT", "apigateway:PATCH", "apigateway:DELETE"], Resource = local.apigw_arns },
      {
        Sid      = "LogGroupManage"
        Effect   = "Allow"
        Action   = ["logs:CreateLogGroup", "logs:DeleteLogGroup", "logs:PutRetentionPolicy", "logs:DeleteRetentionPolicy", "logs:TagResource", "logs:UntagResource", "logs:TagLogGroup"]
        Resource = [local.log_group_arn, "${local.log_group_arn}:*"]
      },
      {
        Sid       = "LambdaRoleWritesRequireBoundary"
        Effect    = "Allow"
        Action    = ["iam:CreateRole", "iam:PutRolePermissionsBoundary", "iam:PutRolePolicy", "iam:DeleteRolePolicy"]
        Resource  = [local.lambda_role_arn]
        Condition = { StringEquals = { "iam:PermissionsBoundary" = local.boundary_arn } }
      },
      {
        Sid      = "LambdaRoleManage"
        Effect   = "Allow"
        Action   = ["iam:DeleteRole", "iam:UpdateAssumeRolePolicy", "iam:TagRole", "iam:UntagRole", "iam:ListInstanceProfilesForRole"]
        Resource = [local.lambda_role_arn]
      },
      {
        Sid       = "PassLambdaRole"
        Effect    = "Allow"
        Action    = ["iam:PassRole"]
        Resource  = [local.lambda_role_arn]
        Condition = { StringEquals = { "iam:PassedToService" = "lambda.amazonaws.com" } }
      },
      { Sid = "DenyBoundaryRemoval", Effect = "Deny", Action = ["iam:DeleteRolePermissionsBoundary"], Resource = [local.lambda_role_arn] },
      { Sid = "SmokeTestApiKey", Effect = "Allow", Action = ["ssm:GetParameter"], Resource = ["${local.ssm_param_prefix}api-key"] },
      { Sid = "SmokeTestDecrypt", Effect = "Allow", Action = ["kms:Decrypt"], Resource = ["*"], Condition = local.kms_via_ssm },
    ]
  })

  # The most the Lambda's role can ever do, whatever its own policy says.
  boundary_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "SSMRead", Effect = "Allow", Action = ["ssm:GetParameters"], Resource = ["${local.ssm_param_prefix}*"] },
      { Sid = "SSMDecrypt", Effect = "Allow", Action = ["kms:Decrypt"], Resource = ["*"], Condition = local.kms_via_ssm },
      { Sid = "PlayersObjects", Effect = "Allow", Action = ["s3:GetObject", "s3:PutObject"], Resource = ["${local.players_arn}/players/*"] },
      { Sid = "PlayersList", Effect = "Allow", Action = ["s3:ListBucket"], Resource = [local.players_arn] },
      { Sid = "Logs", Effect = "Allow", Action = ["logs:CreateLogStream", "logs:PutLogEvents"], Resource = ["${local.log_group_arn}:*"] },
    ]
  })
}

resource "aws_iam_policy" "read" {
  name   = "${local.name}-read"
  policy = local.read_policy
}

resource "aws_iam_policy" "deploy" {
  name   = "${local.name}-deploy"
  policy = local.deploy_policy
}

resource "aws_iam_policy" "lambda_boundary" {
  name   = "${local.name}-lambda-boundary"
  policy = local.boundary_policy
}

resource "aws_iam_role" "plan" {
  name               = "${local.name}-gha-plan"
  assume_role_policy = local.github_trust["plan"]
  depends_on         = [aws_iam_openid_connect_provider.github]
}

resource "aws_iam_role" "deploy" {
  name               = "${local.name}-gha-deploy"
  assume_role_policy = local.github_trust["deploy"]
  depends_on         = [aws_iam_openid_connect_provider.github]
}

resource "aws_iam_role_policy_attachment" "plan_read" {
  role       = aws_iam_role.plan.name
  policy_arn = aws_iam_policy.read.arn
}

resource "aws_iam_role_policy_attachment" "deploy_read" {
  role       = aws_iam_role.deploy.name
  policy_arn = aws_iam_policy.read.arn
}

resource "aws_iam_role_policy_attachment" "deploy_write" {
  role       = aws_iam_role.deploy.name
  policy_arn = aws_iam_policy.deploy.arn
}

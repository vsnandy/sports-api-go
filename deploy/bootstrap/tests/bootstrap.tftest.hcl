mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
    }
  }
}

run "state_bucket_name" {
  command = plan
  assert {
    condition     = aws_s3_bucket.state.bucket == "sports-api-tfstate-123456789012"
    error_message = "state bucket must be sports-api-tfstate-<account_id>"
  }
}

run "plan_role_trust" {
  command = plan
  assert {
    condition     = jsondecode(aws_iam_role.plan.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:vsnandy/sports-api-go:pull_request"
    error_message = "plan role must trust only pull_request tokens from this repo"
  }
  assert {
    condition     = jsondecode(aws_iam_role.plan.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:aud"] == "sts.amazonaws.com"
    error_message = "plan role must require the sts.amazonaws.com audience"
  }
  assert {
    condition     = jsondecode(aws_iam_role.plan.assume_role_policy).Statement[0].Principal.Federated == "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
    error_message = "plan role must trust the GitHub OIDC provider"
  }
}

run "deploy_role_trust" {
  command = plan
  assert {
    condition     = jsondecode(aws_iam_role.deploy.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:vsnandy/sports-api-go:ref:refs/heads/main"
    error_message = "deploy role must trust only the main branch"
  }
  assert {
    condition     = length(keys(jsondecode(aws_iam_role.deploy.assume_role_policy).Statement[0].Condition)) == 1
    error_message = "deploy role trust must use only StringEquals (no wildcard StringLike)"
  }
}

run "read_policy_has_no_secret_access" {
  command = plan
  assert {
    condition = alltrue(flatten([
      for s in jsondecode(aws_iam_policy.read.policy).Statement : [
        for a in s.Action : !startswith(a, "ssm:") && !startswith(a, "kms:")
      ]
    ]))
    error_message = "the read policy (used by PR plans) must not grant SSM or KMS actions"
  }
}

run "deploy_policy_requires_boundary" {
  command = plan
  assert {
    condition = one([
      for s in jsondecode(aws_iam_policy.deploy.policy).Statement : s if s.Sid == "LambdaRoleWritesRequireBoundary"
    ]).Condition.StringEquals["iam:PermissionsBoundary"] == "arn:aws:iam::123456789012:policy/sports-api-lambda-boundary"
    error_message = "role writes on sports-api-lambda must require the boundary"
  }
  assert {
    condition = alltrue([
      for s in jsondecode(aws_iam_policy.deploy.policy).Statement :
      !contains(s.Action, "iam:PutRolePolicy") || s.Sid == "LambdaRoleWritesRequireBoundary"
    ])
    error_message = "iam:PutRolePolicy may only appear in the boundary-conditioned statement"
  }
}

run "deploy_policy_denies_boundary_removal" {
  command = plan
  assert {
    condition = one([
      for s in jsondecode(aws_iam_policy.deploy.policy).Statement : s if s.Sid == "DenyBoundaryRemoval"
    ]).Effect == "Deny"
    error_message = "the deploy policy must explicitly deny removing the boundary"
  }
}

run "boundary_scopes_ssm_to_prefix" {
  command = plan
  assert {
    condition = one([
      for s in jsondecode(aws_iam_policy.lambda_boundary.policy).Statement : s if s.Sid == "SSMRead"
    ]).Resource == ["arn:aws:ssm:us-east-1:123456789012:parameter/sports-api/*"]
    error_message = "the boundary must limit SSM reads to the prefix"
  }
  assert {
    condition     = aws_iam_policy.lambda_boundary.name == "sports-api-lambda-boundary"
    error_message = "boundary policy name is referenced by deploy/terraform and must not change"
  }
}

run "existing_oidc_provider_is_not_created" {
  command = plan
  variables {
    create_oidc_provider = false
  }
  assert {
    condition     = length(aws_iam_openid_connect_provider.github) == 0
    error_message = "create_oidc_provider = false must not create a provider"
  }
}

run "rejects_bad_ssm_prefix" {
  command = plan
  variables {
    ssm_prefix = "sports-api"
  }
  expect_failures = [var.ssm_prefix]
}

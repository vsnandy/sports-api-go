mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
    }
  }
  mock_data "aws_region" {
    defaults = {
      name = "us-east-1"
    }
  }
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }
}

variables {
  sleeper_username = "tester"
  lambda_zip       = "tests/fixtures/lambda.zip"
}

run "lambda_role_has_boundary" {
  command = plan
  assert {
    condition     = aws_iam_role.api.permissions_boundary == "arn:aws:iam::123456789012:policy/sports-api-lambda-boundary"
    error_message = "the Lambda role must carry the bootstrap boundary, or the deploy role cannot create it"
  }
  assert {
    condition     = aws_iam_role.api.name == "sports-api-lambda"
    error_message = "the deploy role's IAM permissions are scoped to the role name sports-api-lambda"
  }
}

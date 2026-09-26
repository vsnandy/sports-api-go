output "state_bucket" {
  value = aws_s3_bucket.state.bucket
}

output "plan_role_arn" {
  value = aws_iam_role.plan.arn
}

output "deploy_role_arn" {
  value = aws_iam_role.deploy.arn
}

output "boundary_policy_arn" {
  value = aws_iam_policy.lambda_boundary.arn
}

output "region" {
  value = var.region
}

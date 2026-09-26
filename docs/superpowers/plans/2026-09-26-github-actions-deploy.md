# GitHub Actions Deploy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy the sports-api Lambda stack from GitHub Actions: tests and a Terraform plan on PRs, apply plus smoke test on merges to `main`, with OIDC roles and remote state created by a one-time bootstrap config.

**Architecture:** A new `deploy/bootstrap` Terraform root (applied once by the owner) creates the S3 state bucket, the GitHub OIDC provider, a read-only plan role trusted only by PRs, a deploy role trusted only by `main`, and a permissions boundary the deploy role must attach to the Lambda's role. The existing `deploy/terraform` root moves to an S3 backend and attaches that boundary. One workflow file runs test → plan (PRs) or test → apply → smoke (`main`), using two small tested shell helpers.

**Tech Stack:** Terraform ≥ 1.10 (CI pins 1.16.4) with `hashicorp/aws ~> 5.0` and native `terraform test` + `mock_provider`; GitHub Actions (`actions/checkout@v4`, `actions/setup-go@v5`, `hashicorp/setup-terraform@v3`, `aws-actions/configure-aws-credentials@v4`); bash; actionlint (run via `go run`, not added to `go.mod`).

**Spec:** `docs/superpowers/specs/2026-09-26-github-actions-deploy-design.md`

## Global Constraints

- Terraform `required_version = ">= 1.10"` in both roots; AWS provider stays `~> 5.0`. CI pins `TF_VERSION: "1.16.4"`.
- Resource names: state bucket `sports-api-tfstate-<account_id>`; roles `sports-api-gha-plan`, `sports-api-gha-deploy`; policies `sports-api-read`, `sports-api-deploy`, `sports-api-lambda-boundary`; the Lambda role stays `sports-api-lambda`.
- OIDC trust (StringEquals): `token.actions.githubusercontent.com:aud = sts.amazonaws.com`; plan `sub = repo:vsnandy/sports-api-go:pull_request`; deploy `sub = repo:vsnandy/sports-api-go:ref:refs/heads/main`.
- State keys: stack `sports-api/terraform.tfstate`, bootstrap `bootstrap/terraform.tfstate`; backend `use_lockfile = true`, `encrypt = true`; `bucket`/`region` only via `-backend-config`.
- Repository variables: `AWS_REGION`, `TF_STATE_BUCKET`, `AWS_PLAN_ROLE_ARN`, `AWS_DEPLOY_ROLE_ARN`, `SLEEPER_USERNAME`, `ESPN_LEAGUE_IDS` (JSON list), optional `SSM_PREFIX` (default `/sports-api/`).
- Secrets never enter GitHub or Terraform state. The API key read for the smoke test is masked with `::add-mask::` before any other use.
- The plan role's policy grants no `ssm:*` or `kms:*` actions.
- Plan output in the job summary is truncated to 60,000 characters with a note.
- No Go code changes; `make test` and `make build` must keep passing.
- During implementation: no `terraform plan`/`apply` against real AWS and no `aws` CLI calls. All Terraform tests use `mock_provider "aws"`.

## Review Focus

1. **First run with repository variables not yet set** → the job fails immediately with an error naming each missing variable, not a cryptic Terraform error. (Task 3: `test-ci-scripts.sh` check-vars cases.)
2. **A non-`main` ref (PR branch, tag, other branch) asking for deploy credentials** → AWS refuses; only the exact `ref:refs/heads/main` subject is trusted, and PRs only get the read-only role. (Task 1: `plan_role_trust` and `deploy_role_trust` runs.)
3. **A merge that tries to give the Lambda role more permissions (or drop its boundary)** → denied by the deploy role's policy. (Task 1: `deploy_policy_requires_boundary` and `deploy_policy_denies_boundary_removal` runs; Task 2: `lambda_role_has_boundary` run.)
4. **`ssm_prefix` set without leading/trailing slashes** → rejected at plan time instead of producing ARNs that silently match nothing. (Task 1: `rejects_bad_ssm_prefix` run.)
5. **A very large plan (first deploy creates everything)** → summary still renders, truncated with a note, and the step never fails the job for size. (Task 3: `test-ci-scripts.sh` plan-summary cases.)

---

## File Map

```
deploy/bootstrap/versions.tf            # terraform + provider requirements
deploy/bootstrap/variables.tf           # region, github_repo, create_oidc_provider, ssm_prefix
deploy/bootstrap/main.tf                # state bucket, OIDC provider
deploy/bootstrap/iam.tf                 # policy documents (locals), policies, roles, attachments
deploy/bootstrap/outputs.tf
deploy/bootstrap/tests/bootstrap.tftest.hcl
deploy/bootstrap/.terraform.lock.hcl    # committed
deploy/terraform/versions.tf            # MODIFY: >= 1.10, s3 backend
deploy/terraform/main.tf                # MODIFY: permissions_boundary on aws_iam_role.api
deploy/terraform/tests/lambda_role.tftest.hcl
deploy/terraform/tests/fixtures/lambda.zip   # tiny fixture for filebase64sha256
scripts/check-vars.sh
scripts/plan-summary.sh
scripts/test-ci-scripts.sh
.github/workflows/deploy.yml
Makefile                                # MODIFY: tf-init
.gitignore                              # MODIFY: deploy/bootstrap/.terraform/
README.md                               # MODIFY: Deploy section
```

---

### Task 1: Bootstrap Terraform config

**Files:**
- Create: `deploy/bootstrap/versions.tf`, `deploy/bootstrap/variables.tf`, `deploy/bootstrap/main.tf`, `deploy/bootstrap/iam.tf`, `deploy/bootstrap/outputs.tf`, `deploy/bootstrap/.terraform.lock.hcl` (generated by init)
- Modify: `.gitignore`
- Test: `deploy/bootstrap/tests/bootstrap.tftest.hcl`

**Interfaces:**
- Consumes: nothing.
- Produces: outputs `state_bucket`, `plan_role_arn`, `deploy_role_arn`, `boundary_policy_arn`, `region`; IAM policy named `sports-api-lambda-boundary` (Task 2 references it by ARN `arn:aws:iam::<account_id>:policy/sports-api-lambda-boundary`).

- [ ] **Step 1: Make sure Terraform is at least 1.10**

Run: `terraform version`
If the version is below 1.10, run `brew install hashicorp/tap/terraform` (this works whether Terraform came from Homebrew core or the HashiCorp tap), then `terraform version` again.
Expected: `Terraform v1.10.0` or newer.

- [ ] **Step 2: Add the scaffolding and ignore rule**

Append to `.gitignore` under the `# Terraform` block:

```gitignore
deploy/bootstrap/.terraform/
```

`deploy/bootstrap/versions.tf`:

```hcl
terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.region
}
```

`deploy/bootstrap/variables.tf`:

```hcl
variable "region" {
  type    = string
  default = "us-east-1"
}

variable "github_repo" {
  description = "owner/name of the GitHub repository allowed to assume the CI roles"
  type        = string
  default     = "vsnandy/sports-api-go"
  validation {
    condition     = can(regex("^[^/]+/[^/]+$", var.github_repo))
    error_message = "github_repo must look like owner/name."
  }
}

variable "create_oidc_provider" {
  description = "false when this account already has a token.actions.githubusercontent.com OIDC provider"
  type        = bool
  default     = true
}

variable "ssm_prefix" {
  description = "SSM parameter path the Lambda and the smoke test may read"
  type        = string
  default     = "/sports-api/"
  validation {
    condition     = startswith(var.ssm_prefix, "/") && endswith(var.ssm_prefix, "/")
    error_message = "ssm_prefix must start and end with /."
  }
}
```

Run: `terraform -chdir=deploy/bootstrap init -input=false`
Expected: `Terraform has been successfully initialized!` and a new `deploy/bootstrap/.terraform.lock.hcl`.

- [ ] **Step 3: Write the failing test**

`deploy/bootstrap/tests/bootstrap.tftest.hcl`:

```hcl
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
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `terraform -chdir=deploy/bootstrap test`
Expected: FAIL — errors like `Reference to undeclared resource` for `aws_s3_bucket.state`.

- [ ] **Step 5: Write the implementation**

`deploy/bootstrap/main.tf`:

```hcl
data "aws_caller_identity" "current" {}

locals {
  name         = "sports-api"
  account_id   = data.aws_caller_identity.current.account_id
  state_bucket = "${local.name}-tfstate-${local.account_id}"
  oidc_host    = "token.actions.githubusercontent.com"
}

# --- Remote state bucket ---

resource "aws_s3_bucket" "state" {
  bucket = local.state_bucket

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    id     = "expire-noncurrent-versions"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days = 90
    }
  }
  depends_on = [aws_s3_bucket_versioning.state]
}

# --- GitHub OIDC provider ---

resource "aws_iam_openid_connect_provider" "github" {
  count           = var.create_oidc_provider ? 1 : 0
  url             = "https://${local.oidc_host}"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1", "1c58a3a8518e8759bf075b76b750d4f2df264fcd"]
}

# Fails the plan early if create_oidc_provider = false but no provider exists.
data "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 0 : 1
  url   = "https://${local.oidc_host}"
}
```

`deploy/bootstrap/iam.tf`:

```hcl
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
```

`deploy/bootstrap/outputs.tf`:

```hcl
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
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `terraform -chdir=deploy/bootstrap fmt -check -recursive && terraform -chdir=deploy/bootstrap validate && terraform -chdir=deploy/bootstrap test`
Expected: `Success! The configuration is valid.` then `Success! 9 passed, 0 failed.`

- [ ] **Step 7: Commit**

```bash
git add .gitignore deploy/bootstrap
git commit -m "feat: add bootstrap Terraform for state bucket, OIDC, and CI roles"
```

---

### Task 2: Remote backend and Lambda role boundary in the stack

**Files:**
- Modify: `deploy/terraform/versions.tf`, `deploy/terraform/main.tf` (resource `aws_iam_role.api`), `Makefile`
- Create: `deploy/terraform/tests/fixtures/lambda.zip`
- Test: `deploy/terraform/tests/lambda_role.tftest.hcl`

**Interfaces:**
- Consumes: the policy name `sports-api-lambda-boundary` from Task 1.
- Produces: backend key `sports-api/terraform.tfstate` initialized with `-backend-config=bucket=… -backend-config=region=…`; `make tf-init` reading `TF_STATE_BUCKET` and `AWS_REGION` from the environment. Task 3's workflow runs `terraform -chdir=deploy/terraform init -input=false -backend=false && terraform -chdir=deploy/terraform test` in its test job.

- [ ] **Step 1: Write the failing test and fixture**

Run: `mkdir -p deploy/terraform/tests/fixtures && printf 'fixture' > deploy/terraform/tests/fixtures/lambda.zip`

`deploy/terraform/tests/lambda_role.tftest.hcl`:

```hcl
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `terraform -chdir=deploy/terraform init -input=false -backend=false && terraform -chdir=deploy/terraform test`
Expected: FAIL on `lambda_role_has_boundary` — the condition is false because `permissions_boundary` is null.

- [ ] **Step 3: Implement**

Replace `deploy/terraform/versions.tf` with:

```hcl
terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # bucket and region come from -backend-config (see `make tf-init` and the workflow).
  backend "s3" {
    key          = "sports-api/terraform.tfstate"
    use_lockfile = true
    encrypt      = true
  }
}

provider "aws" {
  region = var.region
}
```

In `deploy/terraform/main.tf`, change `resource "aws_iam_role" "api"` to:

```hcl
resource "aws_iam_role" "api" {
  name               = "${local.name}-lambda"
  assume_role_policy = data.aws_iam_policy_document.assume.json
  # Created by deploy/bootstrap. The CI deploy role may only write this role with the boundary attached.
  permissions_boundary = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:policy/${local.name}-lambda-boundary"
}
```

In `Makefile`, add `tf-init` to `.PHONY` and add this target after `build` (recipe lines start with a TAB):

```make
tf-init:
	terraform -chdir=deploy/terraform init -backend-config=bucket=$(TF_STATE_BUCKET) -backend-config=region=$(AWS_REGION)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `terraform -chdir=deploy/terraform fmt -check -recursive && terraform -chdir=deploy/terraform init -input=false -backend=false && terraform -chdir=deploy/terraform validate && terraform -chdir=deploy/terraform test && make test && make build`
Expected: `Success! The configuration is valid.`, `Success! 1 passed, 0 failed.`, all Go packages `ok`, and `dist/bootstrap.zip` built.

- [ ] **Step 5: Commit**

```bash
git add deploy/terraform/versions.tf deploy/terraform/main.tf deploy/terraform/tests Makefile
git commit -m "feat: move stack state to S3 and attach the Lambda permissions boundary"
```

---

### Task 3: CI helper scripts and workflow

**Files:**
- Create: `scripts/check-vars.sh`, `scripts/plan-summary.sh`, `.github/workflows/deploy.yml`
- Test: `scripts/test-ci-scripts.sh`

**Interfaces:**
- Consumes: `make build`, `make smoke` (reads `API_URL`, `API_KEY`), `terraform output -raw api_url` (existing), the Terraform tests from Tasks 1–2, the backend from Task 2.
- Produces: `scripts/check-vars.sh NAME...` (exit 1 and `::error::Missing repository variables: <names>` when any named env var is empty or unset); `scripts/plan-summary.sh PLAN_FILE [SUMMARY_FILE]` (appends a fenced, 60,000-char-capped plan to `SUMMARY_FILE`, default `$GITHUB_STEP_SUMMARY`; exit 0 if `PLAN_FILE` is missing).

- [ ] **Step 1: Write the failing test**

`scripts/test-ci-scripts.sh`:

```bash
#!/usr/bin/env bash
# Tests for the CI helper scripts. Run: ./scripts/test-ci-scripts.sh
set -euo pipefail
cd "$(dirname "$0")"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $*"; exit 1; }

# check-vars.sh
FOO=1 BAR=2 ./check-vars.sh FOO BAR >/dev/null || fail "check-vars should pass when all variables are set"
if out=$(FOO=1 BAR= ./check-vars.sh FOO BAR BAZ); then
  fail "check-vars should fail when variables are missing"
fi
[[ $out == *"Missing repository variables: BAR BAZ"* ]] || fail "check-vars should name the missing variables, got: $out"

# plan-summary.sh
printf 'Plan: 1 to add, 0 to change, 0 to destroy.\n' > "$tmp/small.txt"
./plan-summary.sh "$tmp/small.txt" "$tmp/small.md"
grep -q 'Plan: 1 to add' "$tmp/small.md" || fail "summary should contain the plan"
if grep -q 'truncated' "$tmp/small.md"; then fail "a small plan should not be truncated"; fi

head -c 70000 /dev/zero | tr '\0' 'x' > "$tmp/big.txt"
./plan-summary.sh "$tmp/big.txt" "$tmp/big.md"
grep -q 'truncated to 60000 characters' "$tmp/big.md" || fail "a large plan should be truncated with a note"
size=$(wc -c < "$tmp/big.md" | tr -d ' ')
[ "$size" -lt 61000 ] || fail "summary should stay under the cap, got $size bytes"

./plan-summary.sh "$tmp/missing.txt" "$tmp/missing.md" || fail "a missing plan file should not fail the step"

echo "ci script tests passed"
```

Run: `chmod +x scripts/test-ci-scripts.sh`

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/test-ci-scripts.sh`
Expected: FAIL — `./check-vars.sh: No such file or directory`, then `FAIL: check-vars should pass when all variables are set`.

- [ ] **Step 3: Implement the scripts**

`scripts/check-vars.sh`:

```bash
#!/usr/bin/env bash
# Fails with a GitHub Actions error naming every listed environment variable that is empty.
# Usage: check-vars.sh NAME...
set -euo pipefail
missing=()
for name in "$@"; do
  [ -n "${!name:-}" ] || missing+=("$name")
done
if [ ${#missing[@]} -gt 0 ]; then
  echo "::error::Missing repository variables: ${missing[*]} (Settings > Secrets and variables > Actions > Variables)"
  exit 1
fi
```

`scripts/plan-summary.sh`:

```bash
#!/usr/bin/env bash
# Appends a Terraform plan to the GitHub Actions job summary, capped to stay under its size limit.
# Usage: plan-summary.sh PLAN_FILE [SUMMARY_FILE]   (SUMMARY_FILE defaults to $GITHUB_STEP_SUMMARY)
set -euo pipefail
plan=$1
summary=${2:-${GITHUB_STEP_SUMMARY:?GITHUB_STEP_SUMMARY is not set}}
limit=60000
if [ ! -f "$plan" ]; then
  echo "no plan output at $plan"
  exit 0
fi
{
  echo '### Terraform plan'
  echo '```'
  head -c "$limit" "$plan"
  echo
  echo '```'
  if [ "$(wc -c < "$plan" | tr -d ' ')" -gt "$limit" ]; then
    echo "_Plan truncated to $limit characters; see the job log for the full output._"
  fi
} >> "$summary"
```

Run: `chmod +x scripts/check-vars.sh scripts/plan-summary.sh`

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/test-ci-scripts.sh`
Expected: `ci script tests passed`

- [ ] **Step 5: Write the workflow**

`.github/workflows/deploy.yml`:

```yaml
name: deploy

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]
  workflow_dispatch:

permissions:
  contents: read

env:
  TF_VERSION: "1.16.4"
  TF_IN_AUTOMATION: "true"
  AWS_REGION: ${{ vars.AWS_REGION }}
  TF_STATE_BUCKET: ${{ vars.TF_STATE_BUCKET }}
  SSM_PREFIX: ${{ vars.SSM_PREFIX || '/sports-api/' }}
  TF_VAR_region: ${{ vars.AWS_REGION }}
  TF_VAR_sleeper_username: ${{ vars.SLEEPER_USERNAME }}
  TF_VAR_espn_league_ids: ${{ vars.ESPN_LEAGUE_IDS || '[]' }}

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: hashicorp/setup-terraform@v3
        with:
          terraform_version: ${{ env.TF_VERSION }}
          terraform_wrapper: false
      - name: gofmt
        run: |
          unformatted=$(gofmt -l .)
          if [ -n "$unformatted" ]; then echo "$unformatted"; exit 1; fi
      - name: go vet
        run: go vet ./...
      - name: go test
        run: go test -race ./...
      - name: CI script tests
        run: ./scripts/test-ci-scripts.sh
      - name: terraform fmt
        run: terraform fmt -check -recursive deploy
      - name: terraform test (bootstrap)
        run: terraform -chdir=deploy/bootstrap init -input=false -backend=false && terraform -chdir=deploy/bootstrap test
      - name: terraform test (stack)
        run: terraform -chdir=deploy/terraform init -input=false -backend=false && terraform -chdir=deploy/terraform test

  plan:
    if: github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository
    needs: test
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write
    concurrency:
      group: plan-${{ github.event.pull_request.number }}
      cancel-in-progress: true
    steps:
      - uses: actions/checkout@v4
      - name: Check repository variables
        env:
          AWS_PLAN_ROLE_ARN: ${{ vars.AWS_PLAN_ROLE_ARN }}
          SLEEPER_USERNAME: ${{ vars.SLEEPER_USERNAME }}
        run: ./scripts/check-vars.sh AWS_REGION TF_STATE_BUCKET AWS_PLAN_ROLE_ARN SLEEPER_USERNAME
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Build Lambda
        run: make build
      - uses: hashicorp/setup-terraform@v3
        with:
          terraform_version: ${{ env.TF_VERSION }}
          terraform_wrapper: false
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: ${{ vars.AWS_PLAN_ROLE_ARN }}
          aws-region: ${{ vars.AWS_REGION }}
      - name: terraform init
        run: terraform -chdir=deploy/terraform init -input=false -backend-config="bucket=$TF_STATE_BUCKET" -backend-config="region=$AWS_REGION"
      - name: terraform plan
        run: |
          set -o pipefail
          terraform -chdir=deploy/terraform plan -lock=false -input=false -no-color | tee plan.txt
      - name: Publish plan to summary
        if: always()
        run: ./scripts/plan-summary.sh plan.txt

  deploy:
    if: github.ref == 'refs/heads/main' && (github.event_name == 'push' || github.event_name == 'workflow_dispatch')
    needs: test
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write
    concurrency:
      group: deploy-main
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@v4
      - name: Check repository variables
        env:
          AWS_DEPLOY_ROLE_ARN: ${{ vars.AWS_DEPLOY_ROLE_ARN }}
          SLEEPER_USERNAME: ${{ vars.SLEEPER_USERNAME }}
        run: ./scripts/check-vars.sh AWS_REGION TF_STATE_BUCKET AWS_DEPLOY_ROLE_ARN SLEEPER_USERNAME
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Build Lambda
        run: make build
      - uses: hashicorp/setup-terraform@v3
        with:
          terraform_version: ${{ env.TF_VERSION }}
          terraform_wrapper: false
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: ${{ vars.AWS_DEPLOY_ROLE_ARN }}
          aws-region: ${{ vars.AWS_REGION }}
      - name: terraform init
        run: terraform -chdir=deploy/terraform init -input=false -backend-config="bucket=$TF_STATE_BUCKET" -backend-config="region=$AWS_REGION"
      - name: terraform apply
        run: terraform -chdir=deploy/terraform apply -auto-approve -input=false -no-color
      - name: Smoke test
        run: |
          API_URL=$(terraform -chdir=deploy/terraform output -raw api_url)
          API_KEY=$(aws ssm get-parameter --name "${SSM_PREFIX}api-key" --with-decryption --query Parameter.Value --output text)
          echo "::add-mask::$API_KEY"
          export API_URL API_KEY
          make smoke
```

- [ ] **Step 6: Lint the workflow**

Run: `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/deploy.yml && git diff --exit-code go.mod go.sum`
Expected: no actionlint output (exit 0), and `go.mod`/`go.sum` unchanged.

- [ ] **Step 7: Run the local equivalent of the test job**

Run: `./scripts/test-ci-scripts.sh && terraform fmt -check -recursive deploy && terraform -chdir=deploy/bootstrap test && terraform -chdir=deploy/terraform test && make test`
Expected: `ci script tests passed`, both Terraform test suites `Success!`, all Go packages `ok`.

- [ ] **Step 8: Commit**

```bash
git add scripts/check-vars.sh scripts/plan-summary.sh scripts/test-ci-scripts.sh .github/workflows/deploy.yml
git commit -m "feat: add GitHub Actions workflow for plan on PRs and deploy on main"
```

---

### Task 4: README deploy docs

**Files:**
- Modify: `README.md` (replace from the line `## Deploy` up to, not including, `## Rotating ESPN cookies`)

**Interfaces:**
- Consumes: bootstrap outputs (Task 1), `make tf-init` (Task 2), workflow and repository variable names (Task 3).
- Produces: owner-facing setup instructions.

- [ ] **Step 1: Replace the Deploy section**

Replace everything from `## Deploy` up to (not including) `## Rotating ESPN cookies` with:

````markdown
## Deploy

Deploys run in GitHub Actions (`.github/workflows/deploy.yml`):

- **Pull requests:** tests, then `terraform plan` (shown on the run's summary page).
- **Merge to `main`:** tests, `terraform apply`, then the smoke test.
- **Actions → deploy → Run workflow:** redeploy `main`, e.g. after changing a repository variable.

Actions signs in to AWS with GitHub OIDC; no AWS keys are stored in GitHub.

### One-time setup

1. Install Terraform 1.10 or newer: `brew install hashicorp/tap/terraform`.
2. Create the secrets in SSM (once; they never enter Terraform state or GitHub). `espn_s2` and `SWID` are
   cookies from a logged-in espn.com session; keep the braces in `SWID`.
   ```bash
   aws ssm put-parameter --name /sports-api/api-key   --type SecureString --value "$(openssl rand -hex 32)"
   aws ssm put-parameter --name /sports-api/espn-s2   --type SecureString --value '<espn_s2>'
   aws ssm put-parameter --name /sports-api/espn-swid --type SecureString --value '{<SWID>}'
   ```
   Keep every parameter under `/sports-api/`: the Lambda's permissions boundary only allows that path.
3. Create the state bucket, GitHub OIDC provider, and CI roles with your own admin credentials:
   ```bash
   terraform -chdir=deploy/bootstrap init
   terraform -chdir=deploy/bootstrap apply
   ```
   If this AWS account already has a GitHub OIDC provider, add `-var create_oidc_provider=false`.
4. Move the bootstrap state into the new bucket so your laptop isn't the only copy:
   ```bash
   BUCKET=$(terraform -chdir=deploy/bootstrap output -raw state_bucket)
   REGION=$(terraform -chdir=deploy/bootstrap output -raw region)
   cat > deploy/bootstrap/backend.tf <<'EOF'
   terraform {
     backend "s3" {
       key          = "bootstrap/terraform.tfstate"
       use_lockfile = true
       encrypt      = true
     }
   }
   EOF
   terraform -chdir=deploy/bootstrap init -migrate-state \
     -backend-config="bucket=$BUCKET" -backend-config="region=$REGION"
   ```
   Answer `yes` to copy the state, then commit `deploy/bootstrap/backend.tf`.
5. In GitHub, go to **Settings → Secrets and variables → Actions → Variables** and add:

   | Variable | Value |
   |---|---|
   | `AWS_REGION` | `us-east-1` (or the region you bootstrapped) |
   | `TF_STATE_BUCKET` | `terraform -chdir=deploy/bootstrap output -raw state_bucket` |
   | `AWS_PLAN_ROLE_ARN` | `terraform -chdir=deploy/bootstrap output -raw plan_role_arn` |
   | `AWS_DEPLOY_ROLE_ARN` | `terraform -chdir=deploy/bootstrap output -raw deploy_role_arn` |
   | `SLEEPER_USERNAME` | your Sleeper username |
   | `ESPN_LEAGUE_IDS` | JSON list, e.g. `["123456"]` (`[]` or unset for none) |

6. Open a PR to check the plan, then merge it to `main`. The first deploy and smoke test run in Actions;
   the API URL is in the deploy job log (`terraform output`) or via `terraform output -raw api_url` after `make tf-init`.

### Break-glass local deploy

```bash
export TF_STATE_BUCKET=<state bucket> AWS_REGION=us-east-1
cp deploy/terraform/terraform.tfvars.example deploy/terraform/terraform.tfvars   # fill it in
make tf-init
make deploy
```

### Smoke test by hand

Requires `curl` and `jq`:

```bash
API_URL=$(terraform -chdir=deploy/terraform output -raw api_url) \
API_KEY=$(aws ssm get-parameter --name /sports-api/api-key --with-decryption --query Parameter.Value --output text) \
make smoke
```

````

- [ ] **Step 2: Check the README against the code**

Run: `grep -n 'AWS_PLAN_ROLE_ARN\|AWS_DEPLOY_ROLE_ARN\|TF_STATE_BUCKET\|SLEEPER_USERNAME\|ESPN_LEAGUE_IDS' .github/workflows/deploy.yml README.md && grep -n 'output "' deploy/bootstrap/outputs.tf && grep -n '^tf-init' Makefile`
Expected: every variable name appears in both files; outputs `state_bucket`, `plan_role_arn`, `deploy_role_arn` exist; the `tf-init` target exists.

- [ ] **Step 3: Final verification**

Run: `./scripts/test-ci-scripts.sh && terraform fmt -check -recursive deploy && terraform -chdir=deploy/bootstrap test && terraform -chdir=deploy/terraform test && make test && make build && go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 && git status --short`
Expected: all pass; `git status` shows only `README.md` modified (build output and `.terraform/` are ignored).

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document GitHub Actions deploy and one-time bootstrap"
```

- [ ] **Step 5 (owner, real AWS — not run by implementers): Acceptance**

Follow the README one-time setup, open the PR for this branch after the v1 PR is merged, confirm the plan job renders a plan in the summary, merge, and confirm the deploy job prints `smoke test passed`. An `AccessDenied` names the missing action: add it to the matching statement in `deploy/bootstrap/iam.tf`, run `terraform -chdir=deploy/bootstrap apply` locally, and re-run the job.

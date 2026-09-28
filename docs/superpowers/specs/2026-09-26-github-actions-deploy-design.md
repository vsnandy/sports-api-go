# GitHub Actions Deploy Design

Date: 2026-09-26
Status: Approved design, pending spec review
Builds on: `docs/superpowers/specs/2026-09-25-sports-api-v1-design.md`

## 1. Purpose and Scope

Deploy the sports-api Lambda and its AWS resources from GitHub Actions instead of a
laptop. Pull requests show a Terraform plan; merges to `main` apply and smoke-test.

The stack has never been deployed, so there is no local state to migrate: CI performs
the first deploy.

### In scope
- One-time bootstrap Terraform config (`deploy/bootstrap/`): remote-state bucket,
  GitHub OIDC provider, a read-only plan role, a main-only deploy role, and a
  permissions boundary for the Lambda's role
- Remote S3 backend for the existing `deploy/terraform` config
- Workflow `.github/workflows/deploy.yml`: test → plan on PRs; test → apply → smoke on
  `main`
- README rewrite of the Deploy section; `make tf-init`

### Out of scope
- Multiple environments (staging/prod), GitHub Environments with approvals
- PR comments with the plan (plan goes to the run summary only)
- Managing SSM secret values in Terraform or GitHub
- Automated rollback on smoke-test failure

## 2. Key Decisions

| Decision | Choice | Rationale |
|---|---|---|
| AWS auth | GitHub OIDC → IAM roles | No long-lived keys in GitHub |
| Roles | Separate read-only plan role (PRs) and deploy role (`main` only) | A PR that edits the workflow still cannot obtain write credentials |
| Chicken-and-egg | `deploy/bootstrap` applied once from a laptop | CI cannot create the role it assumes |
| State | S3 bucket, versioned, `use_lockfile = true` | Native S3 locking; no DynamoDB table |
| Terraform version | `>= 1.10` (CI pins one exact version) | Required for `use_lockfile` |
| Deploy settings | GitHub repository variables → `TF_VAR_*` | Change leagues without a commit |
| Secrets | Stay in SSM; CI reads only the API key, for the smoke test | Secrets never enter GitHub or Terraform state |
| Privilege escalation guard | Permissions boundary required on the Lambda role | Deploy role cannot grant the Lambda more than the boundary allows |
| Triggers | `pull_request` → plan; `push` to `main` and `workflow_dispatch` → apply | Every merge deploys; manual redeploy after variable changes |

## 3. Bootstrap Config (`deploy/bootstrap/`)

Applied once, locally, by the owner with admin credentials. Starts with local state;
after the first apply the owner migrates it into the state bucket under key
`bootstrap/terraform.tfstate` (`terraform init -migrate-state` with a backend block
added — documented in the README).

### Variables
| Name | Default | Purpose |
|---|---|---|
| `region` | `us-east-1` | Region for all resources and ARNs |
| `github_repo` | `vsnandy/sports-api-go` | OIDC subject prefix |
| `create_oidc_provider` | `true` | `false` → look up an existing provider with a data source |
| `ssm_prefix` | `/sports-api/` | SSM parameter path the Lambda and smoke test may read |

### Resources

**State bucket** `sports-api-tfstate-<account_id>`
- Versioning enabled; SSE-S3; all public access blocked
- Lifecycle rule: expire noncurrent versions after 90 days
- `lifecycle { prevent_destroy = true }`

**OIDC provider** `token.actions.githubusercontent.com`
- Client ID `sts.amazonaws.com`; thumbprints
  `6938fd4d98bab03faadb97b34396831e3780aea1`, `1c58a3a8518e8759bf075b76b750d4f2df264fcd`
- Created only when `create_oidc_provider = true`

**Plan role** `sports-api-gha-plan`
- Trust: `sts:AssumeRoleWithWebIdentity` from the OIDC provider with
  `token.actions.githubusercontent.com:aud = sts.amazonaws.com` and
  `token.actions.githubusercontent.com:sub = repo:<github_repo>:pull_request` (StringEquals)
- Policy `sports-api-read` (see §5)

**Deploy role** `sports-api-gha-deploy`
- Trust: same, with `sub = repo:<github_repo>:ref:refs/heads/main` (StringEquals).
  `workflow_dispatch` runs on `main` carry the same subject.
- Policies `sports-api-read` and `sports-api-deploy` (see §5)

**Permissions boundary** policy `sports-api-lambda-boundary` (see §5)

### Outputs
`state_bucket`, `plan_role_arn`, `deploy_role_arn`, `boundary_policy_arn`

## 4. Changes to `deploy/terraform`

- `versions.tf`: `required_version = ">= 1.10"`; add
  ```hcl
  backend "s3" {
    key          = "sports-api/terraform.tfstate"
    use_lockfile = true
    encrypt      = true
  }
  ```
  `bucket` and `region` are supplied via `terraform init -backend-config=...`.
- `main.tf`: `aws_iam_role.api` gains
  `permissions_boundary = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:policy/sports-api-lambda-boundary"`.
  The Lambda's existing inline policy is unchanged and falls within the boundary as long
  as the SSM parameter names stay under `/sports-api/`.
- Existing `variables.tf`, `outputs.tf`, and the Lambda/API/S3/log resources are
  otherwise unchanged.

## 5. IAM Policies

`<acct>` = account ID, `<region>` = `var.region`, `<state>` = state bucket name.

### `sports-api-read` (plan role and deploy role)
| Service | Actions | Resources |
|---|---|---|
| S3 (state) | `s3:ListBucket` | `arn:aws:s3:::<state>` |
| S3 (state) | `s3:GetObject` | `arn:aws:s3:::<state>/sports-api/*` |
| S3 (players) | `s3:Get*`, `s3:List*` | `arn:aws:s3:::sports-api-players-*` (bucket ARN only, not objects) |
| Lambda | `lambda:Get*`, `lambda:List*` | `arn:aws:lambda:<region>:<acct>:function:sports-api` |
| API Gateway | `apigateway:GET` | `arn:aws:apigateway:<region>::/apis`, `…::/apis/*`, `…::/tags/*` |
| IAM | `iam:GetRole`, `iam:GetRolePolicy`, `iam:ListRolePolicies`, `iam:ListAttachedRolePolicies`, `iam:ListRoleTags` | `arn:aws:iam::<acct>:role/sports-api-lambda` |
| Logs | `logs:DescribeLogGroups` | `*` (API requires it) |
| Logs | `logs:ListTagsForResource`, `logs:ListTagsLogGroup` | `arn:aws:logs:<region>:<acct>:log-group:/aws/lambda/sports-api*` |

The read policy grants no SSM or KMS access, so the plan role cannot read secrets.

### `sports-api-deploy` (deploy role only)
| Service | Actions | Resources / Conditions |
|---|---|---|
| S3 (state) | `s3:PutObject`, `s3:DeleteObject` | `arn:aws:s3:::<state>/sports-api/*` (state + `.tflock`) |
| S3 (players) | `s3:CreateBucket`, `s3:DeleteBucket`, `s3:PutBucket*`, `s3:PutEncryptionConfiguration`, `s3:PutLifecycleConfiguration` | `arn:aws:s3:::sports-api-players-*` |
| S3 (players objects, for `force_destroy`) | `s3:ListBucketVersions`, `s3:DeleteObject`, `s3:DeleteObjectVersion` | bucket and `/*` |
| Lambda | `lambda:CreateFunction`, `lambda:UpdateFunctionCode`, `lambda:UpdateFunctionConfiguration`, `lambda:DeleteFunction`, `lambda:AddPermission`, `lambda:RemovePermission`, `lambda:TagResource`, `lambda:UntagResource` | function `sports-api` |
| API Gateway | `apigateway:POST`, `apigateway:PUT`, `apigateway:PATCH`, `apigateway:DELETE` (tagging is POST/DELETE on `/tags/*`) | `/apis`, `/apis/*`, `/tags/*` in `<region>` |
| Logs | `logs:CreateLogGroup`, `logs:DeleteLogGroup`, `logs:PutRetentionPolicy`, `logs:DeleteRetentionPolicy`, `logs:TagResource`, `logs:UntagResource`, `logs:TagLogGroup` | log group `/aws/lambda/sports-api` (and `:*`) |
| IAM | `iam:CreateRole`, `iam:PutRolePermissionsBoundary`, `iam:PutRolePolicy`, `iam:DeleteRolePolicy` | role `sports-api-lambda`, **Condition** `iam:PermissionsBoundary = arn:aws:iam::<acct>:policy/sports-api-lambda-boundary` |
| IAM | `iam:DeleteRole`, `iam:UpdateAssumeRolePolicy`, `iam:TagRole`, `iam:UntagRole`, `iam:ListInstanceProfilesForRole` | role `sports-api-lambda` |
| IAM | `iam:PassRole` | role `sports-api-lambda`, Condition `iam:PassedToService = lambda.amazonaws.com` |
| IAM (Deny) | `iam:DeleteRolePermissionsBoundary` | role `sports-api-lambda` |
| SSM | `ssm:GetParameter` | `arn:aws:ssm:<region>:<acct>:parameter<ssm_prefix>api-key` |
| KMS | `kms:Decrypt` | `*`, Condition `kms:ViaService = ssm.<region>.amazonaws.com` |

### `sports-api-lambda-boundary`
| Actions | Resources / Conditions |
|---|---|
| `ssm:GetParameters` | `arn:aws:ssm:<region>:<acct>:parameter<ssm_prefix>*` |
| `kms:Decrypt` | `*`, Condition `kms:ViaService = ssm.<region>.amazonaws.com` |
| `s3:GetObject`, `s3:PutObject` | `arn:aws:s3:::sports-api-players-*/players/*` |
| `s3:ListBucket` | `arn:aws:s3:::sports-api-players-*` |
| `logs:CreateLogStream`, `logs:PutLogEvents` | `arn:aws:logs:<region>:<acct>:log-group:/aws/lambda/sports-api:*` |

## 6. Workflow (`.github/workflows/deploy.yml`)

### Triggers and permissions
```yaml
on:
  pull_request:
    branches: [main]
  push:
    branches: [main]
  workflow_dispatch:
permissions:
  contents: read
  id-token: write
```

### Concurrency
- `deploy` job: group `deploy-main`, `cancel-in-progress: false`
- `plan` job: group `plan-${{ github.event.pull_request.number }}`, `cancel-in-progress: true`

### Environment
- `TF_VERSION`: the latest stable Terraform release (≥ 1.10) at implementation time, as one exact version string defined once at workflow level
- `TF_VAR_sleeper_username: ${{ vars.SLEEPER_USERNAME }}`
- `TF_VAR_espn_league_ids: ${{ vars.ESPN_LEAGUE_IDS }}` (JSON list, e.g. `["123456"]`;
  `[]` when there are no ESPN leagues)
- `TF_VAR_region: ${{ vars.AWS_REGION }}`

### Jobs
**`test`** (all triggers)
1. `actions/checkout@v4`; `actions/setup-go@v5` with `go-version-file: go.mod`
2. `test -z "$(gofmt -l .)"`; `go vet ./...`; `go test -race ./...`
3. `./scripts/test-ci-scripts.sh` (tests for the workflow's helper scripts)
4. `terraform fmt -check -recursive deploy`; `terraform test` for `deploy/bootstrap` and
   `deploy/terraform` (both `init -backend=false`, `mock_provider "aws"`, no AWS credentials)

**`plan`** (`pull_request` only; `needs: test`;
`if: github.event.pull_request.head.repo.full_name == github.repository`)
1. Preflight: fail with a named message if any of `AWS_REGION`, `TF_STATE_BUCKET`,
   `AWS_PLAN_ROLE_ARN`, `SLEEPER_USERNAME` is empty
2. `make build`
3. `hashicorp/setup-terraform@v3` with `terraform_version: ${{ env.TF_VERSION }}` and
   `terraform_wrapper: false`
4. `aws-actions/configure-aws-credentials@v4` with `role-to-assume: ${{ vars.AWS_PLAN_ROLE_ARN }}`
5. `terraform -chdir=deploy/terraform init -input=false -backend-config=bucket=$TF_STATE_BUCKET -backend-config=region=$AWS_REGION`
6. `terraform -chdir=deploy/terraform plan -lock=false -input=false -no-color` → tee to
   a file; append to `$GITHUB_STEP_SUMMARY` inside a code fence, truncated to 60,000
   characters with a truncation note. The step fails if plan fails.

**`deploy`** (`push` to `main` or `workflow_dispatch`; `needs: test`;
`if: github.ref == 'refs/heads/main'`)
1. Preflight: as above, with `AWS_DEPLOY_ROLE_ARN` instead of the plan role
2. `make build`; set up Terraform; configure credentials with the deploy role
3. `terraform init` (as above); `terraform apply -auto-approve -input=false`
4. Smoke:
   - `API_URL=$(terraform -chdir=deploy/terraform output -raw api_url)`
   - `API_KEY=$(aws ssm get-parameter --name "${SSM_PREFIX}api-key" --with-decryption --query Parameter.Value --output text)`;
     `echo "::add-mask::$API_KEY"` before any other use
   - `make smoke` with both exported

`SSM_PREFIX` defaults to `/sports-api/` (repository variable `SSM_PREFIX` may override).

### Repository variables (set by the owner)
| Variable | Source |
|---|---|
| `AWS_REGION` | Region used for bootstrap |
| `TF_STATE_BUCKET` | bootstrap output `state_bucket` |
| `AWS_PLAN_ROLE_ARN` | bootstrap output `plan_role_arn` |
| `AWS_DEPLOY_ROLE_ARN` | bootstrap output `deploy_role_arn` |
| `SLEEPER_USERNAME` | Owner's Sleeper username |
| `ESPN_LEAGUE_IDS` | JSON list of ESPN league IDs |

## 7. Makefile and README

- New target:
  ```make
  tf-init:
  	terraform -chdir=deploy/terraform init -backend-config=bucket=$(TF_STATE_BUCKET) -backend-config=region=$(AWS_REGION)
  ```
  `make deploy` stays as the break-glass local path (requires `make tf-init` first).
- README "Deploy" rewritten:
  1. `brew upgrade terraform` (≥ 1.10)
  2. Create the three SSM parameters (unchanged commands)
  3. `terraform -chdir=deploy/bootstrap init && terraform -chdir=deploy/bootstrap apply`
  4. Migrate bootstrap state into the bucket (exact backend block and
     `terraform init -migrate-state` command)
  5. Set the repository variables from the outputs
  6. Merge to `main` → first deploy and smoke test run in Actions
  - "Break-glass local deploy" subsection: `TF_STATE_BUCKET=… AWS_REGION=… make tf-init && make deploy`
  - Note: the Lambda's SSM parameters must stay under `ssm_prefix` or the boundary blocks them

## 8. Rollout Order

1. Merge the v1 PR (`design/sports-api-v1` → `main`). No workflow exists yet; nothing deploys.
2. Owner completes README steps 1–5.
3. Open the PR for `ci/github-actions-deploy`. The `plan` job exercises the plan role
   against an empty stack (plan shows all resources to create).
4. Merge. The `deploy` job performs the first apply and smoke test.

If step 3 or 4 fails with `AccessDenied`, the missing action is added to the
appropriate policy in `deploy/bootstrap`, re-applied locally, and the job re-run.

## 9. Verification

- `terraform fmt -check` and `terraform validate` for `deploy/bootstrap` and
  `deploy/terraform` (the latter with `init -backend=false`)
- `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` on the workflow (does not
  modify `go.mod`)
- `make test` and `make build` still pass
- Acceptance (owner, real AWS): §8 steps 3–4 succeed — plan renders in the run summary,
  apply completes, smoke test prints `smoke test passed`

## 10. Risks

- **IAM action gaps**: Terraform's AWS provider may call read/tag actions not listed in
  §5. Mitigated by §8's fix-and-rerun loop; the tables make gaps easy to locate.
- **Apply without human gate**: every merge deploys. Accepted for a single-owner repo;
  the PR plan is the review point.
- **Smoke failure after apply**: the new version is already live; fix forward.

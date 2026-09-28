# sports-api-go

Personal fantasy football API that unifies ESPN and Sleeper leagues with Sleeper
player stats, game logs, and fantasy points. Runs locally or on AWS Lambda.

Design: `docs/superpowers/specs/2026-09-25-sports-api-v1-design.md`

## Endpoints

All `/v1` routes require `X-API-Key`.

| Route | Returns |
|---|---|
| `GET /healthz` | `{"status":"ok"}` |
| `GET /v1/nfl/leagues?season=` | Your ESPN and Sleeper leagues |
| `GET /v1/nfl/leagues/{id}?season=` | Teams, scoring, roster slots |
| `GET /v1/nfl/leagues/{id}/rosters?season=` | Rosters with normalized players |
| `GET /v1/nfl/leagues/{id}/matchups?week=&season=&include=stats` | Matchups, optionally with player stats and points |
| `GET /v1/nfl/players/{id}` | Player info (Sleeper ID) |
| `GET /v1/nfl/players/{id}/gamelog?season=&scoring=` | Weekly stats; `scoring` = `ppr`, `half`, `std`, or a league ID |

League IDs look like `espn:123456` or `sleeper:987654321`.

## Run locally

```bash
API_KEY=dev SLEEPER_USERNAME=you make run
# with ESPN:
API_KEY=dev SLEEPER_USERNAME=you ESPN_LEAGUE_IDS=123456 ESPN_S2='...' ESPN_SWID='{...}' make run
curl -H 'X-API-Key: dev' localhost:8080/v1/nfl/leagues
```

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
   Answer `yes` to copy the state, then run `terraform fmt deploy/bootstrap` (copying from this
   list can indent the file, which fails CI's `terraform fmt -check`) and commit `deploy/bootstrap/backend.tf`.
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
   the API URL is in the deploy job log (`terraform output`) or via `terraform -chdir=deploy/terraform output -raw api_url` after `make tf-init`.

The CI roles trust GitHub's default OIDC subject, which embeds the owner and repo IDs (`repo:<owner>@<owner_id>/<repo>@<repo_id>:pull_request` for PRs, `…:ref:refs/heads/main` for deploys). For a fork or renamed repo, set `github_repo`, `github_owner_id` and `github_repo_id` in `deploy/bootstrap` (`curl -s https://api.github.com/repos/<owner>/<repo> | jq '.owner.id, .id'`). Don't customize the repository's OIDC subject claim or add a GitHub `environment:` to the workflow jobs without updating the trust policies in `deploy/bootstrap/iam.tf`, or role assumption fails with AccessDenied.

### Changing CI permissions

Bootstrap state now lives in the state bucket, so a fresh clone needs `-backend-config` to find
it. To grant a role more (or less) access:

1. Edit `deploy/bootstrap/iam.tf`.
2. ```bash
   terraform -chdir=deploy/bootstrap init -backend-config="bucket=<state bucket>" -backend-config="region=<region>"
   terraform -chdir=deploy/bootstrap apply
   ```
   If the first bootstrap used `-var create_oidc_provider=false`, pass it on every later apply too,
   or Terraform will try to create a second GitHub OIDC provider.
3. Re-run the failed Actions job.

This is also how you fix an `AccessDenied` from the first `plan` or `deploy` run: the CI roles
usually just need a policy update in `iam.tf`, applied the same way.

### Stale state lock

A cancelled or killed deploy can leave `sports-api/terraform.tfstate.tflock` in the state bucket,
which blocks the next plan/apply. With your own (owner) AWS credentials:

```bash
TF_STATE_BUCKET=<state bucket> AWS_REGION=<region> make tf-init
terraform -chdir=deploy/terraform force-unlock <LOCK_ID>
```

`<LOCK_ID>` is printed in the failed job's error message.

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

## Rotating ESPN cookies

A `502` with code `espn_auth_failed` (or an `espn_leagues_unavailable` warning) means the
cookies expired. Update them with `aws ssm put-parameter --overwrite ...`, then force a
cold start: `aws lambda update-function-configuration --function-name sports-api --description "rotated $(date +%F)"`.

That `--description` change is not managed by Terraform, so the next `terraform apply`
will show it as drift and reset the description to whatever Terraform has configured
(or none). This is harmless -- it doesn't affect the running function -- but expect to
see it in the plan.

## Test

```bash
make test
```

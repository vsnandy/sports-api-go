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

1. Create secrets (once; they never enter Terraform state). `espn_s2` and `SWID` are
   cookies from a logged-in espn.com session; keep the braces in `SWID`.
   ```bash
   aws ssm put-parameter --name /sports-api/api-key   --type SecureString --value "$(openssl rand -hex 32)"
   aws ssm put-parameter --name /sports-api/espn-s2   --type SecureString --value '<espn_s2>'
   aws ssm put-parameter --name /sports-api/espn-swid --type SecureString --value '{<SWID>}'
   ```
2. `cp deploy/terraform/terraform.tfvars.example deploy/terraform/terraform.tfvars` and fill it in.
3. `terraform -chdir=deploy/terraform init`, then `make deploy`.
4. Smoke test (requires `curl` and `jq`):
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

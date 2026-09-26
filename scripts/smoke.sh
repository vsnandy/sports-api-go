#!/usr/bin/env bash
# Hits a deployed (or local) API with real config. Not part of `go test`.
# Usage: API_URL=$(terraform -chdir=deploy/terraform output -raw api_url) API_KEY=... make smoke
set -euo pipefail
: "${API_URL:?set API_URL}"
: "${API_KEY:?set API_KEY}"

body=$(mktemp)
trap 'rm -f "$body"' EXIT

get() {
  local code
  code=$(curl -sS -o "$body" -w '%{http_code}' -H "X-API-Key: $API_KEY" "$API_URL$1")
  if [[ $code != "${2:-200}" ]]; then
    echo "FAIL $1 -> $code: $(cat "$body")"
    exit 1
  fi
  echo "ok   $1 ($code)"
  jq -r '.meta.warnings[]? | "     warning: " + .' "$body"
}

get /healthz
code=$(curl -sS -o /dev/null -w '%{http_code}' "$API_URL/v1/nfl/leagues")
[[ $code == 401 ]] || { echo "FAIL unauthenticated request returned $code"; exit 1; }
echo "ok   /v1/nfl/leagues without key (401)"

get /v1/nfl/leagues
leagues=$(jq -r '.data[].id' "$body")
for league in $leagues; do
  get "/v1/nfl/leagues/$league"
  get "/v1/nfl/leagues/$league/rosters"
  get "/v1/nfl/leagues/$league/matchups?include=stats"
done

get /v1/nfl/players/4046
get "/v1/nfl/players/4046/gamelog?scoring=ppr"
echo "smoke test passed"

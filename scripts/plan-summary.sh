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

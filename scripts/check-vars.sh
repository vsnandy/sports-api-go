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

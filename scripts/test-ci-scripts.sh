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

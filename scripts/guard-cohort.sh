#!/usr/bin/env bash
# guard-cohort: internal/cohort must not IMPORT a cloud SDK or scheduler.
# Checks real import paths only (go list), not comments.
set -euo pipefail
bad=0
for pkg in $(go list ./internal/cohort/...); do
  imports=$(go list -f '{{ join .Imports "\n" }}' "$pkg")
  if echo "$imports" | grep -Eq 'aws-sdk-go|azure-sdk|cloud\.google|/internal/slurm|/internal/substrate'; then
    echo "FAIL: $pkg imports a provider/scheduler/domain package:"
    echo "$imports" | grep -E 'aws-sdk-go|azure-sdk|cloud\.google|/internal/slurm|/internal/substrate' | sed 's/^/  /'
    bad=1
  fi
done
[ "$bad" -eq 0 ] && echo "ok: internal/cohort is provider-, scheduler-, and domain-agnostic"
exit $bad

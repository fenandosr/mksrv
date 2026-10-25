#!/usr/bin/env bash
# Seed the initial Vikunja user, once. No env-var-only bootstrap exists --
# `vikunja user create` is the documented CLI mechanism (same shape as
# gitea's hook). Does NOT also set the instance-admin flag: confirmed live
# that `vikunja user set-admin <user> --admin` refuses with "the
# admin-panel license feature is not active" even on a completely fresh,
# unlicensed instance -- Vikunja's free tier has no instance-admin concept
# to grant, just the one user account.
set -euo pipefail

for _ in $(seq 1 30); do
  if podman exec vikunja curl -fsS -o /dev/null http://127.0.0.1:3456/api/v1/info 2>/dev/null; then
    break
  fi
  sleep 2
done

# Only "already exists" is swallowed (idempotent across redeploys); any
# other failure -- including "not ready yet" if the loop above timed out --
# fails the hook for real.
output=$(podman exec vikunja sh -c '
  /app/vikunja/vikunja user create \
    --username admin \
    --email "admin@'"${MKSRV_TENANT}"'.mksrv.local" \
    --password "$VIKUNJA_ADMIN_PASS"
' 2>&1) && { echo "$output"; exit 0; }

if echo "$output" | grep -qi "already exists"; then
  echo "vikunja admin user already exists"
  exit 0
fi
echo "$output" >&2
exit 1

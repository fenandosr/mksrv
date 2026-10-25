#!/usr/bin/env bash
# Seed the initial Gitea admin user, once. gitea/gitea has no env-var-only
# admin bootstrap (confirmed live against the real image) -- this is the
# documented mechanism, run inside the container so GITEA_ADMIN_PASS (the
# podman Secret-injected env var) never leaves it. Idempotent: errors
# harmlessly once the user already exists.
set -euo pipefail

podman exec --user git gitea sh -c '
  gitea admin user create \
    --username admin \
    --password "$GITEA_ADMIN_PASS" \
    --email "admin@'"${MKSRV_TENANT}"'.mksrv.local" \
    --admin --must-change-password=false
' || true

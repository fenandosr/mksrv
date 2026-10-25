#!/usr/bin/env bash
# Seed the initial Gitea admin user, once. gitea/gitea has no env-var-only
# admin bootstrap (confirmed live against the real image) -- this is the
# documented mechanism, run inside the container so GITEA_ADMIN_PASS (the
# podman Secret-injected env var) never leaves it.
set -euo pipefail

# DeployStack runs this hook right after the restart and before any
# stack-level health check (this stack has none anyway -- its port only
# exists inside the meshnode's network namespace, see ADR 0034) -- nothing
# else waits for gitea to finish migrating its SQLite DB first. Without this,
# `gitea admin user create` against a not-yet-migrated DB fails on a fresh
# deploy, confirmed live.
for _ in $(seq 1 30); do
  if podman exec gitea curl -fsS -o /dev/null http://127.0.0.1:3000/api/healthz 2>/dev/null; then
    break
  fi
  sleep 2
done

# Only "already exists" is swallowed (idempotent across redeploys); any
# other failure -- including "not ready yet" if the loop above timed out --
# fails the hook for real instead of silently reporting deploy success with
# no admin user.
output=$(podman exec --user git gitea sh -c '
  gitea admin user create \
    --username admin \
    --password "$GITEA_ADMIN_PASS" \
    --email "admin@'"${MKSRV_TENANT}"'.mksrv.local" \
    --admin --must-change-password=false
' 2>&1) && { echo "$output"; exit 0; }

if echo "$output" | grep -qi "already exists"; then
  echo "gitea admin user already exists"
  exit 0
fi
echo "$output" >&2
exit 1

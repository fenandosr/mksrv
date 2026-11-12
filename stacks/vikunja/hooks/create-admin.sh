#!/usr/bin/env bash
# Seed the initial Vikunja user, once. No env-var-only bootstrap exists --
# `vikunja user create` is the documented CLI mechanism. Unlike gitea's
# admin-creation hook, this needs no readiness wait: confirmed live that
# `user create` runs its own migration check synchronously within the same
# short-lived process and succeeds immediately against a brand-new,
# never-started database.
#
# The vikunja/vikunja image has no shell at all (confirmed live:
# `podman exec vikunja sh` -> "executable file `sh` not found in $PATH" --
# not even `ls`/`cat`/`curl` exist, just the single static binary), so the
# password can't be read from inside the container the way gitea's hook
# reads GITEA_ADMIN_PASS. `podman secret inspect --showsecret` reads it
# from the host instead -- mksrv's own process already held this value in
# memory to push the secret in the first place, so this isn't a new
# exposure, just a different place to read the same value from.
set -euo pipefail

password=$(sudo podman secret inspect --showsecret --format '{{.SecretData}}' "mksrv-vikunja-tenant_${MKSRV_TENANT}_admin_pass")

# Only "already exists" is swallowed (idempotent across redeploys); any
# other failure fails the hook for real.
output=$(podman exec vikunja /app/vikunja/vikunja user create \
  --username admin \
  --email "admin@${MKSRV_TENANT}.mksrv.local" \
  --password "$password" 2>&1) && { echo "$output"; exit 0; }

if echo "$output" | grep -qi "already exists"; then
  echo "vikunja admin user already exists"
  exit 0
fi
echo "$output" >&2
exit 1

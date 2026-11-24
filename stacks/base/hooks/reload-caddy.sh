#!/usr/bin/env bash
# Reload Caddy gracefully after a deploy so a changed Caddyfile takes effect. A reload keeps
# existing connections (a restart would interrupt every published site). A Caddy that is not
# running yet (a first deploy) is skipped; a reload that FAILS (an invalid Caddyfile) fails the
# deploy, because the running config is then not the one that was written.
set -euo pipefail

if ! podman container exists mksrv-caddy 2>/dev/null || [ "$(podman inspect -f '{{.State.Running}}' mksrv-caddy 2>/dev/null)" != "true" ]; then
  echo "mksrv-caddy is not running; nothing to reload"
  exit 0
fi
podman exec mksrv-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
echo "caddy reloaded"

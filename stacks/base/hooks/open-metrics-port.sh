#!/usr/bin/env bash
# Open :9019 to the monitor host(s) after a deploy, from the rendered script, without
# restarting Caddy.
set -euo pipefail
bash /var/lib/mksrv/stacks/base/metrics-firewall.sh

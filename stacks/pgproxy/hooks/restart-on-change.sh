#!/usr/bin/env bash
# Restart the proxy when its configuration changed (a node was added or removed).
# The unit file alone is not enough: haproxy.cfg is a bind-mounted file, and the
# deploy only restarts units whose quadlet changed. A marker holds the hash of the
# config the running container started with; the first run just records it.
set -euo pipefail

cfg=/var/lib/mksrv/stacks/pgproxy/haproxy.cfg
marker=/var/lib/mksrv/stacks/pgproxy/.applied-sha256
now=$(sha256sum "$cfg" | cut -d' ' -f1)

if [ ! -f "$marker" ]; then
	echo "$now" >"$marker"
	echo "pgproxy: first deploy, config recorded"
	exit 0
fi
if [ "$(cat "$marker")" = "$now" ]; then
	echo "pgproxy: config unchanged"
	exit 0
fi
systemctl restart mksrv-pgproxy.service
echo "$now" >"$marker"
echo "pgproxy: config changed, restarted"

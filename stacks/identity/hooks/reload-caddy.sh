#!/usr/bin/env bash
# Reload Caddy so a newly written vhost fragment takes effect.
set -euo pipefail

if systemctl is-active --quiet mksrv-caddy.service; then
	if podman exec mksrv-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile 2>/dev/null; then
		echo "caddy reloaded"
	else
		systemctl restart mksrv-caddy.service
		echo "caddy restarted"
	fi
else
	echo "caddy not running on this host; skipping reload"
fi

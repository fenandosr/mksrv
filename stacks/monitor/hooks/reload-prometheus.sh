#!/usr/bin/env bash
# Reload Prometheus so a changed scrape config (fleet grew, a new exporter
# came online) takes effect without restarting the container and losing the
# in-memory head block. --web.enable-lifecycle is set on the container.
set -euo pipefail

if systemctl is-active --quiet mksrv-prometheus.service; then
	if podman exec mksrv-prometheus wget -q --post-data='' -O- http://localhost:9090/-/reload 2>/dev/null; then
		echo "prometheus reloaded"
	else
		systemctl restart mksrv-prometheus.service
		echo "prometheus restarted"
	fi
else
	echo "prometheus not running on this host; skipping reload"
fi

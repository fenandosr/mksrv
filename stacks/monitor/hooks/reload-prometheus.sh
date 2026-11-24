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

# blackbox_exporter reads its modules at start and the hook above only reloads Prometheus, so a new
# module (http_reachable) was unknown to the running exporter and every probe using it returned 400
# until it was reloaded by hand. It has its own reload endpoint; restart only if that fails.
if systemctl is-active --quiet mksrv-blackbox.service; then
	if podman exec mksrv-blackbox wget -q --post-data='' -O- http://localhost:9115/-/reload >/dev/null 2>&1; then
		echo "blackbox reloaded"
	else
		systemctl restart mksrv-blackbox.service
		echo "blackbox restarted"
	fi
else
	echo "blackbox not running on this host; skipping reload"
fi

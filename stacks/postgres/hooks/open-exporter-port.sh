#!/usr/bin/env bash
# Open :9187 to the monitor host(s) after a deploy. The rules come from the rendered script;
# running it here (not as the exporter's ExecStartPre) means no unit has to restart for it,
# which matters on the database hosts.
set -euo pipefail
bash /var/lib/mksrv/stacks/postgres/exporter-firewall.sh

#!/usr/bin/env bash
# Activate the daily backup timer after the unit files are written.
set -euo pipefail

systemctl daemon-reload
systemctl enable --now mksrv-backup.timer
echo "mksrv-backup.timer enabled ($(systemctl show -p NextElapseUSecRealtime --value mksrv-backup.timer))"

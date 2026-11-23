#!/usr/bin/env bash
# Scheduled entry point for periodic MPB backups.
#
# cron example (hourly, logs to the archive's own log directory):
#   7 * * * * /opt/meridian/backup/run-scheduled.sh /etc/meridian/backup.env
#
# The environment file must export MERIDIAN_URL and BACKUP_ARCHIVE, and may
# export MPB_VERIFIER plus any rotation override (KEEP, HOURLY, DAILY,
# WEEKLY, MONTHLY). The backup tool itself is single-shot, so the scheduler
# (cron, systemd timer, Kubernetes CronJob ...) owns periodicity.
set -euo pipefail

ENV_FILE="${1:?usage: run-scheduled.sh <env-file>}"
# shellcheck disable=SC1090
set -a; . "$ENV_FILE"; set +a

: "${MERIDIAN_URL:?MERIDIAN_URL must be set in $ENV_FILE}"
: "${BACKUP_ARCHIVE:?BACKUP_ARCHIVE must be set in $ENV_FILE}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$BACKUP_ARCHIVE/logs"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"

exec python3 "$SCRIPT_DIR/mpb_backup.py" \
  --url "$MERIDIAN_URL" \
  --archive "$BACKUP_ARCHIVE" \
  ${MPB_VERIFIER:+--verifier "$MPB_VERIFIER"} \
  --keep "${KEEP:-3}" \
  --hourly "${HOURLY:-6}" \
  --daily "${DAILY:-7}" \
  --weekly "${WEEKLY:-4}" \
  --monthly "${MONTHLY:-3}" \
  >>"$BACKUP_ARCHIVE/logs/backup-$STAMP.log" 2>&1

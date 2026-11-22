#!/usr/bin/env bash
# Nightly logical backup of the TunnelHub database (users, devices, tunnels,
# audit log, traffic totals). Run from cron on the relay host, e.g.:
#   17 3 * * *  /opt/tunnelhub/deploy/scripts/backup.sh >> /var/log/tunnelhub-backup.log 2>&1
#
# Writes deploy/backups/tunnelhub-YYYYmmdd-HHMMSS.sql.gz (mode 0600) and keeps
# BACKUP_KEEP_DAYS (default 14). Copy the directory off the host regularly: a
# backup on the same disk does not survive disk failure.
set -euo pipefail
cd "$(dirname "$0")/.."

KEEP_DAYS="${BACKUP_KEEP_DAYS:-14}"
OUT_DIR="${BACKUP_DIR:-$PWD/backups}"
mkdir -p "$OUT_DIR"
chmod 700 "$OUT_DIR"

ts="$(date -u +%Y%m%d-%H%M%S)"
out="$OUT_DIR/tunnelhub-$ts.sql.gz"
umask 077
docker compose exec -T postgres pg_dump -U tunnelhub --clean --if-exists tunnelhub | gzip -9 > "$out.partial"
mv "$out.partial" "$out"
echo "backup written: $out ($(stat -c %s "$out") bytes)"

find "$OUT_DIR" -maxdepth 1 -name 'tunnelhub-*.sql.gz' -mtime "+$KEEP_DAYS" -print -delete

#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "DATABASE_URL must be set" >&2
  exit 1
fi

BACKUP_PATH=${1:-}
if [[ -z "$BACKUP_PATH" ]]; then
  BACKUP_DIR=${BACKUP_DIR:-/backups}
  BACKUP_PATH=$(ls -1t "$BACKUP_DIR"/keepstack-*.sql.gz 2>/dev/null | head -n1 || true)
fi

if [[ -z "$BACKUP_PATH" ]]; then
  echo "no backup files found" >&2
  exit 1
fi

echo "restoring from $BACKUP_PATH"
gunzip -c "$BACKUP_PATH" | psql "$DATABASE_URL"

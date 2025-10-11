#!/usr/bin/env bash
set -euo pipefail

log() {
  local level="$1"
  shift || true
  printf '[%(%Y-%m-%dT%H:%M:%S%z)T] [%s]' -1 "$level"
  for message in "$@"; do
    printf ' %s' "$message"
  done
  printf '\n'
}

log_info() {
  log "INFO" "$@"
}

log_error() {
  log "ERROR" "$@"
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    log_error "missing required command" "$1"
    exit 1
  fi
}

require_command kubectl
require_command jq

NAMESPACE="${KS_NAMESPACE:-keepstack}"
RELEASE="${KS_RELEASE:-keepstack}"
JOB_PREFIX="${KS_BACKUP_JOB_PREFIX:-keepstack-backup-now}"
TIME_FORMAT="${KS_BACKUP_TIME_FORMAT:-%Y%m%d-%H%M%S}"
TIMEOUT="${KS_BACKUP_TIMEOUT:-600s}"
FOLLOW_LOGS="${KS_BACKUP_FOLLOW_LOGS:-true}"

selector="app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=backup"
cronjob_name=$(kubectl -n "$NAMESPACE" get cronjob -l "$selector" -o json | jq -r '.items[0].metadata.name' 2>/dev/null)
if [[ -z "$cronjob_name" || "$cronjob_name" == "null" ]]; then
  log_error "backup CronJob not found" "selector=${selector}" "namespace=${NAMESPACE}"
  exit 1
fi

timestamp=$(date +"${TIME_FORMAT}")
job_name="${JOB_PREFIX}-${timestamp}"

log_info "Creating backup job" "cronjob=${cronjob_name}" "job=${job_name}" "namespace=${NAMESPACE}"

kubectl -n "$NAMESPACE" create job "$job_name" --from="cronjob/${cronjob_name}"

log_info "Waiting for job completion" "timeout=${TIMEOUT}"
if ! kubectl -n "$NAMESPACE" wait --for=condition=complete "job/${job_name}" --timeout="$TIMEOUT"; then
  log_error "backup job did not complete within timeout" "job=${job_name}"
  kubectl -n "$NAMESPACE" get pods -l "job-name=${job_name}" || true
  exit 1
fi

log_info "Backup job completed" "job=${job_name}"

if [[ "$FOLLOW_LOGS" == "true" ]]; then
  log_info "Tailing backup logs" "job=${job_name}"
  kubectl -n "$NAMESPACE" logs "job/${job_name}"
fi

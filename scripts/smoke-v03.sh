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
require_command curl
require_command jq

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
BASE_URL="${SMOKE_BASE_URL:-http://keepstack.localtest.me:18080}"
NAMESPACE="${KS_NAMESPACE:-keepstack}"
RELEASE="${KS_RELEASE:-keepstack}"
RESURF_LIMIT="${KS_RESURF_LIMIT:-5}"
RESURF_TIMEOUT="${KS_RESURF_TIMEOUT:-300s}"

log_info "Starting smoke test v0.3 pipeline" "base=${BASE_URL}" "namespace=${NAMESPACE}"

log_info "Running smoke-v02"
"${SCRIPT_DIR}/smoke-v02.sh"

log_info "Verifying observability stack"
"${SCRIPT_DIR}/verify-obs.sh"

log_info "Triggering on-demand backup"
"${SCRIPT_DIR}/backup-now.sh"

selector_resurfacer="app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=resurfacer"
cronjob_name=$(kubectl -n "$NAMESPACE" get cronjob -l "$selector_resurfacer" -o json | jq -r '.items[0].metadata.name' 2>/dev/null)
if [[ -z "$cronjob_name" || "$cronjob_name" == "null" ]]; then
  log_error "Resurfacer CronJob not found" "selector=${selector_resurfacer}"
  exit 1
fi

job_name="${cronjob_name}-now-$(date +%s)"

log_info "Launching resurfacer job" "cronjob=${cronjob_name}" "job=${job_name}"

kubectl -n "$NAMESPACE" create job "$job_name" --from="cronjob/${cronjob_name}"

log_info "Waiting for resurfacer job" "timeout=${RESURF_TIMEOUT}"
if ! kubectl -n "$NAMESPACE" wait --for=condition=complete "job/${job_name}" --timeout="$RESURF_TIMEOUT"; then
  log_error "Resurfacer job failed to complete" "job=${job_name}"
  kubectl -n "$NAMESPACE" get pods -l "job-name=${job_name}" || true
  exit 1
fi

kubectl -n "$NAMESPACE" logs "job/${job_name}" || true

log_info "Fetching resurfacer recommendations" "limit=${RESURF_LIMIT}"
response=$(curl -fsS --max-time 15 "$BASE_URL/api/recommendations?limit=${RESURF_LIMIT}")
count=$(jq -r '.count // .items | if type=="array" then length else . end' <<<"$response" 2>/dev/null || echo 0)
if (( count <= 0 )); then
  log_error "No resurfacer recommendations returned"
  printf '%s\n' "$response"
  exit 1
fi

log_info "Resurfacer returned ${count} recommendations"
log_info "Smoke v0.3 workflow completed successfully"

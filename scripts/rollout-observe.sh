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

NAMESPACE="${KS_NAMESPACE:-keepstack}"
RELEASE="${KS_RELEASE:-keepstack}"
BASE_URL="${SMOKE_BASE_URL:-http://keepstack.localtest.me:18080}"
POLL_INTERVAL="${KS_ROLLOUT_CURL_INTERVAL:-5}"
CURL_TIMEOUT="${KS_ROLLOUT_CURL_TIMEOUT:-5}"
ROLLBACK_ON_FAIL="${KS_ROLLOUT_ROLLBACK:-false}"

selector="app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=api"
deployment=$(kubectl -n "$NAMESPACE" get deploy -l "$selector" -o json | jq -r '.items[0].metadata.name' 2>/dev/null)
if [[ -z "$deployment" || "$deployment" == "null" ]]; then
  log_error "API deployment not found" "selector=${selector}" "namespace=${NAMESPACE}"
  exit 1
fi

log_info "Patching deployment" "deployment=${deployment}"
nonce=$(date +%s)
patch="{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"rollout-observe/nonce\":\"${nonce}\"}}}}}"

kubectl -n "$NAMESPACE" patch deploy "$deployment" -p "$patch" >/dev/null

monitor_done=$(mktemp)
rm -f "$monitor_done"

cleanup() {
  if [[ -f "$monitor_done" ]]; then
    rm -f "$monitor_done"
  fi
}
trap cleanup EXIT

monitor_rollout() {
  while true; do
    status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time "$CURL_TIMEOUT" "$BASE_URL/api/links") || status="000"
    if [[ "$status" =~ ^5 ]]; then
      log_error "Detected 5xx during rollout" "status=${status}"
      touch "$monitor_done"
      return 1
    fi
    if [[ "$status" == "000" ]]; then
      log_info "Request failed" "status=${status}" "retrying"
    fi
    if [[ -f "$monitor_done" ]]; then
      return 0
    fi
    sleep "$POLL_INTERVAL"
  done
}

monitor_rollout &
monitor_pid=$!

set +e
kubectl -n "$NAMESPACE" rollout status "deploy/${deployment}" --timeout="${KS_ROLLOUT_TIMEOUT:-300s}"
rollout_status=$?
set -e

touch "$monitor_done"
wait "$monitor_pid"
monitor_status=$?

if [[ $rollout_status -ne 0 ]]; then
  log_error "Rollout failed" "deployment=${deployment}"
  if [[ "$ROLLBACK_ON_FAIL" == "true" ]]; then
    kubectl -n "$NAMESPACE" rollout undo "deploy/${deployment}" || true
  fi
  exit 1
fi

if [[ $monitor_status -ne 0 ]]; then
  log_error "Monitoring detected failures during rollout"
  exit 1
fi

log_info "Rollout completed without observed 5xx responses"

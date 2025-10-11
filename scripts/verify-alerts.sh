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
PROM_NAMESPACE="${PROM_NAMESPACE:-monitoring}"
PROM_RELEASE="${PROM_RELEASE:-kube-prom-stack}"
PROM_SERVICE="${PROM_SERVICE:-${PROM_RELEASE}-kube-p-prometheus}"
BASE_URL="${SMOKE_BASE_URL:-http://keepstack.localtest.me:8080}"
API_ERROR_DURATION="${KS_ALERT_API_ERROR_DURATION:-90}"
WORKER_FAILURE_COUNT="${KS_ALERT_WORKER_FAILURE_COUNT:-6}"
WORKER_FAILURE_INTERVAL="${KS_ALERT_WORKER_FAILURE_INTERVAL:-5}"
ALERT_TIMEOUT="${KS_ALERT_TIMEOUT_SECONDS:-600}"
API_RECOVERY_REQUESTS="${KS_ALERT_API_RECOVERY_REQUESTS:-40}"
PROM_LOCAL_PORT="${KS_ALERT_PROM_LOCAL_PORT:-19091}"
API_WINDOW="${KS_ALERT_API_WINDOW:-1m}"
API_FOR="${KS_ALERT_API_FOR:-1m}"
WORKER_WINDOW="${KS_ALERT_WORKER_WINDOW:-1m}"
WORKER_FOR="${KS_ALERT_WORKER_FOR:-1m}"

selector_api="app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=api"
selector_worker="app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=worker"
selector_nats="app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=nats"
selector_rules="app.kubernetes.io/instance=${RELEASE}"

log_info "Preparing alert verification" "namespace=${NAMESPACE}" "prom_namespace=${PROM_NAMESPACE}"

prom_rule_name=$(kubectl -n "$NAMESPACE" get prometheusrule -l "$selector_rules" -o json | jq -r '.items[0].metadata.name' 2>/dev/null)
if [[ -z "$prom_rule_name" || "$prom_rule_name" == "null" ]]; then
  log_error "PrometheusRule not found" "selector=${selector_rules}"
  exit 1
fi

rule_original=$(mktemp)
rule_clean=$(mktemp)
rule_mutated=$(mktemp)

kubectl -n "$NAMESPACE" get prometheusrule "$prom_rule_name" -o json >"$rule_original"

jq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp, .metadata.generation, .metadata.managedFields, .metadata.annotations["kubectl.kubernetes.io/last-applied-configuration"], .status)' "$rule_original" >"$rule_clean"

jq --arg api_for "$API_FOR" --arg api_window "$API_WINDOW" \
   --arg worker_for "$WORKER_FOR" --arg worker_window "$WORKER_WINDOW" '
  (.spec.groups[] | select(.name=="keepstack.api").rules[] | select(.alert=="KeepstackHighErrorRate").for) = $api_for |
  (.spec.groups[] | select(.name=="keepstack.api").rules[] | select(.alert=="KeepstackHighErrorRate").expr) |=
    (gsub("\\[[0-9]+m\\]"; "[" + $api_window + "]")) |
  (.spec.groups[] | select(.name=="keepstack.worker").rules[] | select(.alert=="KeepstackWorkerFailures").for) = $worker_for |
  (.spec.groups[] | select(.name=="keepstack.worker").rules[] | select(.alert=="KeepstackWorkerFailures").expr) |=
    (gsub("\\[[0-9]+m\\]"; "[" + $worker_window + "]"))
' "$rule_clean" >"$rule_mutated"

kubectl -n "$NAMESPACE" apply -f "$rule_mutated" >/dev/null

cleanup() {
  local rc=$?
  trap - EXIT
  kubectl -n "$NAMESPACE" apply -f "$rule_clean" >/dev/null 2>&1 || true
  if [[ -n "${nats_statefulset:-}" ]]; then
    kubectl -n "$NAMESPACE" scale statefulset "$nats_statefulset" --replicas="${nats_original_replicas:-1}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${prom_pf_pid:-}" ]]; then
    kill "$prom_pf_pid" >/dev/null 2>&1 || true
    wait "$prom_pf_pid" 2>/dev/null || true
  fi
  rm -f "$rule_original" "$rule_clean" "$rule_mutated"
  exit "$rc"
}
trap cleanup EXIT

start_port_forward() {
  local namespace="$1"
  local resource="$2"
  local local_port="$3"
  local target_port="$4"
  local pf_log
  pf_log=$(mktemp)
  kubectl -n "$namespace" port-forward "$resource" "${local_port}:${target_port}" --address 127.0.0.1 >"$pf_log" 2>&1 &
  local pid=$!
  for _ in {1..40}; do
    if grep -qi 'error' "$pf_log"; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" 2>/dev/null || true
      log_error "port-forward failed" "resource=${resource}" "namespace=${namespace}" "log=$(cat "$pf_log")"
      rm -f "$pf_log"
      exit 1
    fi
    if curl -fsS "http://127.0.0.1:${local_port}/-/ready" >/dev/null 2>&1; then
      rm -f "$pf_log"
      echo "$pid"
      return
    fi
    sleep 0.5
  done
  kill "$pid" >/dev/null 2>&1 || true
  wait "$pid" 2>/dev/null || true
  log_error "port-forward timeout" "resource=${resource}" "namespace=${namespace}"
  rm -f "$pf_log"
  exit 1
}


prom_pf_pid=$(start_port_forward "$PROM_NAMESPACE" "svc/${PROM_SERVICE}" "$PROM_LOCAL_PORT" 9090)

get_alert_state() {
  local alert="$1"
  local response
  response=$(curl -fsS "http://127.0.0.1:${PROM_LOCAL_PORT}/api/v1/alerts" 2>/dev/null || true)
  if [[ -z "$response" ]]; then
    echo "inactive"
    return
  fi
  local state
  state=$(jq -r --arg alert "$alert" '(.data.alerts[] | select(.labels.alertname==$alert).state) // empty' <<<"$response" | head -n1)
  if [[ -z "$state" ]]; then
    echo "inactive"
  else
    echo "$state"
  fi
}

wait_for_alert_state() {
  local alert="$1"
  local desired="$2"
  local timeout="$3"
  local start
  start=$(date +%s)
  while true; do
    local state
    state=$(get_alert_state "$alert")
    log_info "Alert state" "alert=${alert}" "state=${state}" "expected=${desired}"
    if [[ "$state" == "$desired" ]]; then
      return 0
    fi
    if (( $(date +%s) - start >= timeout )); then
      log_error "Timeout waiting for alert state" "alert=${alert}" "expected=${desired}"
      return 1
    fi
    sleep 10
  done
}

api_service=$(kubectl -n "$NAMESPACE" get svc -l "$selector_api" -o json | jq -r '.items[0].metadata.name' 2>/dev/null)
if [[ -z "$api_service" || "$api_service" == "null" ]]; then
  log_error "API service not found" "selector=${selector_api}"
  exit 1
fi

nats_statefulset=$(kubectl -n "$NAMESPACE" get statefulset -l "$selector_nats" -o json | jq -r '.items[0].metadata.name' 2>/dev/null)
if [[ -z "$nats_statefulset" || "$nats_statefulset" == "null" ]]; then
  log_error "NATS StatefulSet not found" "selector=${selector_nats}"
  exit 1
fi

nats_original_replicas=$(kubectl -n "$NAMESPACE" get statefulset "$nats_statefulset" -o jsonpath='{.spec.replicas}' 2>/dev/null)
if [[ -z "$nats_original_replicas" ]]; then
  nats_original_replicas=1
fi

log_info "Scaling NATS to provoke API errors" "statefulset=${nats_statefulset}"
kubectl -n "$NAMESPACE" scale statefulset "$nats_statefulset" --replicas=0 >/dev/null
kubectl -n "$NAMESPACE" rollout status "statefulset/${nats_statefulset}" --timeout=120s >/dev/null 2>&1 || true

log_info "Generating API 5xx responses" "duration=${API_ERROR_DURATION}s"
end_time=$((SECONDS + API_ERROR_DURATION))
while (( SECONDS < end_time )); do
  payload=$(jq -n --arg url "https://error-${SECONDS}.example.com" '{url: $url, title: "Alert provocation"}')
  status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 -H 'Content-Type: application/json' -d "$payload" -X POST "$BASE_URL/api/links" || echo "000")
  log_info "API request" "status=${status}"
  sleep 2
done

log_info "Restoring NATS replicas" "replicas=${nats_original_replicas}"
kubectl -n "$NAMESPACE" scale statefulset "$nats_statefulset" --replicas="$nats_original_replicas" >/dev/null
kubectl -n "$NAMESPACE" rollout status "statefulset/${nats_statefulset}" --timeout=180s >/dev/null 2>&1 || true

if ! wait_for_alert_state "KeepstackHighErrorRate" "firing" "$ALERT_TIMEOUT"; then
  exit 1
fi

log_info "Driving successful traffic for recovery" "requests=${API_RECOVERY_REQUESTS}"
for ((i=0; i<API_RECOVERY_REQUESTS; i++)); do
  curl -sS -o /dev/null --max-time 10 "$BASE_URL/api/links" || true
  sleep 1
done

if ! wait_for_alert_state "KeepstackHighErrorRate" "inactive" "$ALERT_TIMEOUT"; then
  exit 1
fi

log_info "Creating worker failures" "count=${WORKER_FAILURE_COUNT}"
for ((i=0; i<WORKER_FAILURE_COUNT; i++)); do
  payload=$(jq -n --arg url "http://127.0.0.1:65535/fail-${SECONDS}-${i}" '{url: $url, title: "Worker failure"}')
  status=$(curl -sS -o /tmp/worker-fail-response.$$ -w '%{http_code}' --max-time 10 -H 'Content-Type: application/json' -d "$payload" -X POST "$BASE_URL/api/links")
  if [[ "$status" != "201" ]]; then
    log_error "Failed to enqueue worker job" "status=${status}"
  fi
  rm -f /tmp/worker-fail-response.$$ || true
  sleep "$WORKER_FAILURE_INTERVAL"
done

if ! wait_for_alert_state "KeepstackWorkerFailures" "firing" "$ALERT_TIMEOUT"; then
  exit 1
fi

log_info "Waiting for worker alert to clear"
if ! wait_for_alert_state "KeepstackWorkerFailures" "inactive" "$ALERT_TIMEOUT"; then
  exit 1
fi

log_info "Alerts fired and recovered successfully"

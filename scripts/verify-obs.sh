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
COMPONENT_LABEL="app.kubernetes.io/component"
INSTANCE_LABEL="app.kubernetes.io/instance"

log_info "Checking ServiceMonitors" "namespace=${NAMESPACE}" "release=${RELEASE}"

get_single_resource() {
  local kind="$1"
  local selector="$2"
  local field="$3"
  local value
  value=$(kubectl -n "$NAMESPACE" get "$kind" -l "$selector" -o json | jq -r "$field" 2>/dev/null)
  if [[ -z "$value" || "$value" == "null" ]]; then
    return 1
  fi
  printf '%s' "$value"
}

api_selector="${INSTANCE_LABEL}=${RELEASE},${COMPONENT_LABEL}=api"
worker_selector="${INSTANCE_LABEL}=${RELEASE},${COMPONENT_LABEL}=worker"

api_sm=$(get_single_resource servicemonitor.monitoring.coreos.com "$api_selector" '.items[0].metadata.name') || {
  log_error "API ServiceMonitor missing" "selector=${api_selector}"
  exit 1
}
worker_sm=$(get_single_resource servicemonitor.monitoring.coreos.com "$worker_selector" '.items[0].metadata.name') || {
  log_error "Worker ServiceMonitor missing" "selector=${worker_selector}"
  exit 1
}

log_info "Found ServiceMonitors" "api=${api_sm}" "worker=${worker_sm}"

start_port_forward() {
  local resource_type="$1"
  local resource_name="$2"
  local local_port="$3"
  local target_port="$4"
  local pf_log
  pf_log=$(mktemp)
  kubectl -n "$NAMESPACE" port-forward "$resource_type/$resource_name" "${local_port}:${target_port}" --address 127.0.0.1 >"$pf_log" 2>&1 &
  local pf_pid=$!

  for _ in {1..20}; do
    if grep -qiE 'error|already in use' "$pf_log"; then
      kill "$pf_pid" >/dev/null 2>&1 || true
      wait "$pf_pid" 2>/dev/null || true
      log_error "port-forward failed" "resource=${resource_type}/${resource_name}" "log=$(cat "$pf_log")"
      rm -f "$pf_log"
      exit 1
    fi
    if curl -fsS "http://127.0.0.1:${local_port}/metrics" >/dev/null 2>&1; then
      rm -f "$pf_log"
      echo "$pf_pid"
      return
    fi
    sleep 0.5
  done

  kill "$pf_pid" >/dev/null 2>&1 || true
  wait "$pf_pid" 2>/dev/null || true
  log_error "port-forward timeout" "resource=${resource_type}/${resource_name}" "local_port=${local_port}"
  rm -f "$pf_log"
  exit 1
}

get_service_name() {
  local selector="$1"
  kubectl -n "$NAMESPACE" get svc -l "$selector" -o json | jq -r '.items[0].metadata.name' 2>/dev/null
}

api_service=$(get_service_name "$api_selector")
if [[ -z "$api_service" || "$api_service" == "null" ]]; then
  log_error "API service not found" "selector=${api_selector}"
  exit 1
fi

worker_service=$(get_service_name "$worker_selector")
if [[ -z "$worker_service" || "$worker_service" == "null" ]]; then
  log_error "Worker service not found" "selector=${worker_selector}"
  exit 1
fi

api_port=$(kubectl -n "$NAMESPACE" get svc "$api_service" -o jsonpath='{.spec.ports[?(@.name=="http")].port}')
if [[ -z "$api_port" ]]; then
  api_port=$(kubectl -n "$NAMESPACE" get svc "$api_service" -o jsonpath='{.spec.ports[0].port}')
fi
worker_port=$(kubectl -n "$NAMESPACE" get svc "$worker_service" -o jsonpath='{.spec.ports[?(@.name=="metrics")].port}')
if [[ -z "$worker_port" ]]; then
  worker_port=$(kubectl -n "$NAMESPACE" get svc "$worker_service" -o jsonpath='{.spec.ports[0].port}')
fi

api_local_port=${KS_API_METRICS_PORT:-18080}
worker_local_port=${KS_WORKER_METRICS_PORT:-19090}

log_info "Port-forwarding metrics" "api_service=${api_service}" "worker_service=${worker_service}"

cleanup() {
  for pid in "${forward_pids[@]:-}"; do
    if [[ -n "$pid" ]]; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" 2>/dev/null || true
    fi
  done
}
trap cleanup EXIT

forward_pids=()

api_pid=$(start_port_forward svc "$api_service" "$api_local_port" "$api_port")
forward_pids+=("$api_pid")
worker_pid=$(start_port_forward svc "$worker_service" "$worker_local_port" "$worker_port")
forward_pids+=("$worker_pid")

api_metrics=$(curl -fsS "http://127.0.0.1:${api_local_port}/metrics")
worker_metrics=$(curl -fsS "http://127.0.0.1:${worker_local_port}/metrics")

if ! grep -q 'keepstack_api_http_requests_total' <<<"$api_metrics"; then
  log_error "API metrics missing keepstack_api_http_requests_total"
  exit 1
fi

if ! grep -q 'keepstack_api_http_requests_non_2xx_total' <<<"$api_metrics"; then
  log_error "API metrics missing keepstack_api_http_requests_non_2xx_total"
  exit 1
fi

if ! grep -q 'keepstack_worker_jobs_processed_total' <<<"$worker_metrics"; then
  log_error "Worker metrics missing keepstack_worker_jobs_processed_total"
  exit 1
fi

if ! grep -q 'keepstack_worker_jobs_failed_total' <<<"$worker_metrics"; then
  log_error "Worker metrics missing keepstack_worker_jobs_failed_total"
  exit 1
fi

log_info "Metrics validated successfully"

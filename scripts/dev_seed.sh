#!/usr/bin/env bash
set -euo pipefail

default_url="http://keepstack.localtest.me:18080/api/links"
fallback_url="http://127.0.0.1:18081/api/links"
if [[ -v SEED_URL ]]; then
  url="$SEED_URL"
  using_default_url=false
else
  url="$default_url"
  using_default_url=true
fi
payload_default='{"url":"https://example.com","title":"Example Domain"}'
payload="${SEED_PAYLOAD:-$payload_default}"
attempts="${SEED_ATTEMPTS:-5}"
delay="${SEED_RETRY_DELAY:-3}"
namespace="${SEED_NAMESPACE:-keepstack}"
release_name="${SEED_RELEASE:-keepstack}"

if ! [[ "$attempts" =~ ^[0-9]+$ ]] || (( attempts < 1 )); then
  echo "SEED_ATTEMPTS must be a positive integer" >&2
  exit 1
fi

if ! [[ "$delay" =~ ^[0-9]+$ ]]; then
  echo "SEED_RETRY_DELAY must be a non-negative integer" >&2
  exit 1
fi

body_file="$(mktemp)"
status_file="$(mktemp)"
port_forward_log=""
port_forward_pid=""

cleanup() {
  local rc=$?
  trap - EXIT
  rm -f "$body_file" "$status_file"
  if [[ -n "$port_forward_pid" ]]; then
    kill "$port_forward_pid" >/dev/null 2>&1 || true
    wait "$port_forward_pid" 2>/dev/null || true
  fi
  if [[ -n "$port_forward_log" ]]; then
    rm -f "$port_forward_log"
  fi
  exit "$rc"
}
trap cleanup EXIT

send_request() {
  local request_url="$1"
  : >"$body_file"
  : >"$status_file"

  curl_exit=0
  if ! curl \
    --silent \
    --show-error \
    --location \
    --header 'Content-Type: application/json' \
    --data "$payload" \
    --output "$body_file" \
    --write-out '%{http_code}' \
    "$request_url" >"$status_file"; then
    curl_exit=$?
  fi
  status="$(<"$status_file")"
  if [[ "$status" =~ ^[0-9]+$ ]] && (( status >= 200 && status < 300 )); then
    return 0
  fi
  return 1
}

discover_api_service() {
  local service
  service=$(kubectl -n "$namespace" get svc -l app.kubernetes.io/component=api -o jsonpath='{.items[0].metadata.name}' 2>/dev/null | tr -d '\r') || true
  if [[ -n "$service" ]]; then
    echo "$service"
    return 0
  fi
  if kubectl -n "$namespace" get svc "${release_name}-api" >/dev/null 2>&1; then
    echo "${release_name}-api"
    return 0
  fi
  if kubectl -n "$namespace" get svc "${namespace}-api" >/dev/null 2>&1; then
    echo "${namespace}-api"
    return 0
  fi
  return 1
}

start_port_forward() {
  local service_name="$1"
  port_forward_log=$(mktemp)
  kubectl -n "$namespace" port-forward "svc/${service_name}" "18081:80" --address 127.0.0.1 >"$port_forward_log" 2>&1 &
  port_forward_pid=$!
  for _ in {1..40}; do
    if ! kill -0 "$port_forward_pid" >/dev/null 2>&1; then
      echo "Failed to start port-forward. Log:" >&2
      cat "$port_forward_log" >&2
      port_forward_pid=""
      return 1
    fi
    if grep -q "Forwarding from 127.0.0.1:18081 ->" "$port_forward_log"; then
      return 0
    fi
    if grep -qi "error" "$port_forward_log"; then
      echo "Port-forward reported an error. Log:" >&2
      cat "$port_forward_log" >&2
      kill "$port_forward_pid" >/dev/null 2>&1 || true
      wait "$port_forward_pid" 2>/dev/null || true
      port_forward_pid=""
      return 1
    fi
    sleep 0.25
  done
  echo "Timed out waiting for port-forward to become ready. Log:" >&2
  cat "$port_forward_log" >&2
  kill "$port_forward_pid" >/dev/null 2>&1 || true
  wait "$port_forward_pid" 2>/dev/null || true
  port_forward_pid=""
  return 1
}

echo "Seeding Keepstack API at $url"
transport_fallback_used=false

for (( attempt=1; attempt<=attempts; attempt++ )); do
  echo "Attempt $attempt of $attempts..."
  if send_request "$url"; then
    echo "Seed succeeded with HTTP $status."
    if [[ -s "$body_file" ]]; then
      echo "Response body:"
      cat "$body_file"
      echo
    fi
    exit 0
  fi

  echo "Seed attempt $attempt failed." >&2
  if [[ -n "$status" ]]; then
    echo "HTTP status: $status" >&2
  fi
  if (( curl_exit != 0 )); then
    echo "curl exit code: $curl_exit" >&2
  fi
  if [[ -s "$body_file" ]]; then
    echo "Response body:" >&2
    cat "$body_file" >&2
    echo >&2
  fi

  transport_failure=false
  if [[ "$status" == "000" ]]; then
    transport_failure=true
  fi
  if (( curl_exit == 7 || curl_exit == 28 || curl_exit == 52 )); then
    transport_failure=true
  fi

  if $transport_failure && $using_default_url && ! $transport_fallback_used; then
    transport_fallback_used=true
    if ! command -v kubectl >/dev/null 2>&1; then
      echo "kubectl not available; cannot attempt port-forward fallback." >&2
    else
      echo "Direct ingress appears unreachable (transport failure). Attempting kubectl port-forward fallback..." >&2
      if service_name=$(discover_api_service); then
        echo "Port-forwarding service ${service_name} in namespace ${namespace}." >&2
        if start_port_forward "$service_name"; then
          echo "Port-forward established, retrying against $fallback_url." >&2
          url="$fallback_url"
          using_default_url=false
          ((attempt--))
          continue
        else
          echo "Failed to establish port-forward; continuing with standard retries." >&2
        fi
      else
        echo "Unable to locate API service for port-forward fallback." >&2
      fi
    fi
  fi

  if (( attempt < attempts )); then
    echo "Retrying in ${delay}s..." >&2
    sleep "$delay"
  fi

done

echo "All seed attempts failed." >&2
if command -v kubectl >/dev/null 2>&1; then
  echo "Collecting Kubernetes diagnostics..." >&2
  kubectl -n "$namespace" get pods >&2 || true
  kubectl -n "$namespace" get ingress >&2 || true
fi

exit 1

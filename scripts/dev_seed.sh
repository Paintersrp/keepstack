#!/usr/bin/env bash
set -euo pipefail

url="${SEED_URL:-http://keepstack.localtest.me:18080/api/links}"
payload_default='{"url":"https://example.com","title":"Example Domain"}'
payload="${SEED_PAYLOAD:-$payload_default}"
attempts="${SEED_ATTEMPTS:-5}"
delay="${SEED_RETRY_DELAY:-3}"
namespace="${SEED_NAMESPACE:-keepstack}"

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
trap 'rm -f "$body_file" "$status_file"' EXIT

echo "Seeding Keepstack API at $url"
for attempt in $(seq 1 "$attempts"); do
  echo "Attempt $attempt of $attempts..."
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
    "$url" >"$status_file"; then
    curl_exit=$?
  fi

  status="$(<"$status_file")"

  if [[ "$status" =~ ^[0-9]+$ ]] && (( status >= 200 && status < 300 )); then
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

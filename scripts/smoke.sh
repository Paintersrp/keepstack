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

cleanup_files=()
cleanup() {
  if [[ ${#cleanup_files[@]} -gt 0 ]]; then
    rm -f "${cleanup_files[@]}" 2>/dev/null || true
  fi
}
trap cleanup EXIT

BASE_URL="${SMOKE_BASE_URL:-http://keepstack.localtest.me:8080}"
POST_PATH="${SMOKE_POST_PATH:-/api/links}"
GET_PATH="${SMOKE_GET_PATH:-/api/links}"
POST_TIMEOUT="${SMOKE_POST_TIMEOUT:-15}"
GET_TIMEOUT="${SMOKE_GET_TIMEOUT:-10}"
POLL_INTERVAL="${SMOKE_POLL_INTERVAL:-2}"
POLL_TIMEOUT="${SMOKE_POLL_TIMEOUT:-60}"

slug=$(printf 'smoke-%s-%s' "$(date +%s)" "$RANDOM")
LINK_URL="${SMOKE_LINK_URL:-https://example.com/${slug}}"
LINK_TITLE="${SMOKE_LINK_TITLE:-Smoke Test ${slug}}"
QUERY="${SMOKE_QUERY:-${slug}}"

log_info "Starting smoke test" "base=${BASE_URL}" "post=${POST_PATH}" "get=${GET_PATH}" "query=${QUERY}"

post_tmp=$(mktemp)
cleanup_files+=("$post_tmp")
post_body=$(printf '{"url":"%s","title":"%s"}' "$LINK_URL" "$LINK_TITLE")

set +e
post_status=$(curl -sS -o "$post_tmp" -w '%{http_code}' \
  --max-time "$POST_TIMEOUT" \
  -H 'Content-Type: application/json' \
  -X POST "$BASE_URL$POST_PATH" \
  -d "$post_body")
post_exit=$?
set -e

if [[ $post_exit -ne 0 ]]; then
  log_error "POST request failed" "exit=${post_exit}"
  [[ -s "$post_tmp" ]] && cat "$post_tmp" >&2
  exit 1
fi

if [[ "$post_status" != "201" ]]; then
  log_error "Unexpected POST response" "status=${post_status}"
  [[ -s "$post_tmp" ]] && cat "$post_tmp" >&2
  exit 1
fi

log_info "POST succeeded" "status=${post_status}" "url=${LINK_URL}"

deadline=$((SECONDS + POLL_TIMEOUT))
attempt=1
while (( SECONDS <= deadline )); do
  remaining=$((deadline - SECONDS))
  log_info "Polling" "attempt=${attempt}" "remaining=${remaining}s"
  get_tmp=$(mktemp)
  cleanup_files+=("$get_tmp")

  set +e
  get_status=$(curl -sS -o "$get_tmp" -w '%{http_code}' \
    --max-time "$GET_TIMEOUT" \
    -G "$BASE_URL$GET_PATH" \
    --data-urlencode "q=${QUERY}" \
    --data-urlencode 'limit=5')
  get_exit=$?
  set -e

  if [[ $get_exit -ne 0 ]]; then
    log_error "GET request failed" "exit=${get_exit}"
    [[ -s "$get_tmp" ]] && cat "$get_tmp" >&2
    exit 1
  fi

  if [[ "$get_status" != "200" ]]; then
    log_info "GET returned non-200" "status=${get_status}"
  else
    if grep -q "$slug" "$get_tmp"; then
      log_info "Smoke test passed" "attempt=${attempt}"
      exit 0
    fi
    log_info "Link not visible yet"
  fi

  if (( SECONDS + POLL_INTERVAL > deadline )); then
    break
  fi

  sleep "$POLL_INTERVAL"
  ((attempt++))
done

log_error "Smoke test timed out" "query=${QUERY}"
last_body=$(cat "$get_tmp")
log_error "Last response" "$last_body"
exit 1

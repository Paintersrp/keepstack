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
TAG_PATH="${SMOKE_TAG_PATH:-/api/tags}"
POST_TIMEOUT="${SMOKE_POST_TIMEOUT:-15}"
GET_TIMEOUT="${SMOKE_GET_TIMEOUT:-10}"
POLL_INTERVAL="${SMOKE_POLL_INTERVAL:-2}"
POLL_TIMEOUT="${SMOKE_POLL_TIMEOUT:-60}"
DIGEST_TIMEOUT="${SMOKE_DIGEST_TIMEOUT:-20}"
DIGEST_PATH="${SMOKE_DIGEST_PATH:-/api/digest/test}"
RUN_ID="${SMOKE_RUN_ID:-smoke-v02}"

LINK_URL="${SMOKE_LINK_URL:-https://example.com/keepstack/${RUN_ID}}"
LINK_TITLE="${SMOKE_LINK_TITLE:-Keepstack Smoke ${RUN_ID}}"
QUERY="${SMOKE_QUERY:-Keepstack smoke ${RUN_ID}}"
TAG_NAME_PRIMARY="${SMOKE_TAG_NAME_PRIMARY:-Smoke Primary ${RUN_ID}}"
TAG_NAME_SECONDARY="${SMOKE_TAG_NAME_SECONDARY:-Smoke Secondary ${RUN_ID}}"
TAG_NAME_EXTRA="${SMOKE_TAG_NAME_EXTRA:-Smoke Extra ${RUN_ID}}"
HIGHLIGHT_QUOTE="${SMOKE_HIGHLIGHT_QUOTE:-Keepstack highlight for ${RUN_ID}}"
HIGHLIGHT_NOTE="${SMOKE_HIGHLIGHT_NOTE:-Keepstack note for ${RUN_ID}}"
DIGEST_TEST_FLAG="${DIGEST_TEST:-}"
DIGEST_TRANSPORT="${SMTP_URL:-log://}"

log_info "Starting smoke test v0.2" "base=${BASE_URL}" "run_id=${RUN_ID}"

perform_request() {
  local output="$1"
  shift

  set +e
  local status
  status=$(curl -sS -o "$output" -w '%{http_code}' "$@")
  local exit_code=$?
  set -e

  if [[ $exit_code -ne 0 ]]; then
    log_error "Request failed" "exit=${exit_code}" "$*"
    if [[ -s "$output" ]]; then
      cat "$output" >&2
    fi
    exit 1
  fi

  printf '%s' "$status"
}

post_tmp=$(mktemp)
cleanup_files+=("$post_tmp")
post_body=$(jq -n --arg url "$LINK_URL" --arg title "$LINK_TITLE" '{url: $url, title: $title}')

post_status=$(perform_request "$post_tmp" \
  --max-time "$POST_TIMEOUT" \
  -H 'Content-Type: application/json' \
  -X POST "$BASE_URL$POST_PATH" \
  -d "$post_body")

if [[ "$post_status" != "201" ]]; then
  log_error "Unexpected POST response" "status=${post_status}"
  [[ -s "$post_tmp" ]] && cat "$post_tmp" >&2
  exit 1
fi

link_id=$(jq -r '.id // empty' "$post_tmp" 2>/dev/null || true)
if [[ -z "$link_id" ]]; then
  log_error "POST response missing link id"
  [[ -s "$post_tmp" ]] && cat "$post_tmp" >&2
  exit 1
fi

log_info "Link created" "id=${link_id}" "url=${LINK_URL}"

deadline=$((SECONDS + POLL_TIMEOUT))
attempt=1
link_found=false
while (( SECONDS <= deadline )); do
  remaining=$((deadline - SECONDS))
  log_info "Polling for link" "attempt=${attempt}" "remaining=${remaining}s"
  get_tmp=$(mktemp)
  cleanup_files+=("$get_tmp")

  get_status=$(perform_request "$get_tmp" \
    --max-time "$GET_TIMEOUT" \
    -G "$BASE_URL$GET_PATH" \
    --data-urlencode "q=${QUERY}" \
    --data-urlencode 'limit=5')

  if [[ "$get_status" == "200" ]]; then
    if jq -e --arg id "$link_id" '.items[] | select(.id == $id)' "$get_tmp" >/dev/null 2>&1; then
      log_info "Link visible in search" "attempt=${attempt}"
      link_found=true
      break
    fi
    log_info "Link not visible yet"
  else
    log_info "GET returned non-200" "status=${get_status}"
    if [[ -s "$get_tmp" ]]; then
      cat "$get_tmp" >&2
    fi
  fi

  if (( SECONDS + POLL_INTERVAL > deadline )); then
    break
  fi

  sleep "$POLL_INTERVAL"
  ((attempt++))
done

if [[ "$link_found" != "true" ]]; then
  log_error "Smoke test timed out waiting for link" "query=${QUERY}"
  if [[ -n "${get_tmp:-}" && -s "$get_tmp" ]]; then
    cat "$get_tmp" >&2
  fi
  exit 1
fi

ensure_tag() {
  local name="$1"
  local tmp
  tmp=$(mktemp)
  cleanup_files+=("$tmp")
  local body
  body=$(jq -n --arg name "$name" '{name: $name}')

  local status
  status=$(perform_request "$tmp" \
    --max-time "$POST_TIMEOUT" \
    -H 'Content-Type: application/json' \
    -X POST "$BASE_URL$TAG_PATH" \
    -d "$body")

  case "$status" in
    200|201|409) ;;
    *)
      log_error "Unexpected tag response" "name=${name}" "status=${status}"
      [[ -s "$tmp" ]] && cat "$tmp" >&2
      exit 1
      ;;
  esac

  if [[ "$status" == "201" ]]; then
    local id
    id=$(jq -r '.id // empty' "$tmp" 2>/dev/null || true)
    if [[ -n "$id" ]]; then
      log_info "Tag ensured" "name=${name}" "id=${id}"
      printf '%s' "$id"
      return
    fi
  fi

  local list_tmp
  list_tmp=$(mktemp)
  cleanup_files+=("$list_tmp")
  local list_status
  list_status=$(perform_request "$list_tmp" \
    --max-time "$GET_TIMEOUT" \
    -G "$BASE_URL$TAG_PATH")

  if [[ "$list_status" != "200" ]]; then
    log_error "Failed to list tags" "status=${list_status}"
    [[ -s "$list_tmp" ]] && cat "$list_tmp" >&2
    exit 1
  fi

  local id
  id=$(jq -r --arg name "$name" '.tags[] | select(.name == $name) | .id' "$list_tmp" 2>/dev/null || true)
  if [[ -z "$id" ]]; then
    log_error "Unable to resolve tag id" "name=${name}"
    exit 1
  fi

  log_info "Tag ensured" "name=${name}" "id=${id}"
  printf '%s' "$id"
}

tag_primary=$(ensure_tag "$TAG_NAME_PRIMARY")
tag_secondary=$(ensure_tag "$TAG_NAME_SECONDARY")
tag_extra=$(ensure_tag "$TAG_NAME_EXTRA")

log_info "Tags ready" "primary=${tag_primary}" "secondary=${tag_secondary}" "extra=${tag_extra}"

pre_tmp=$(mktemp)
cleanup_files+=("$pre_tmp")
pre_status=$(perform_request "$pre_tmp" \
  --max-time "$GET_TIMEOUT" \
  -G "$BASE_URL$GET_PATH" \
  --data-urlencode "tags=${TAG_NAME_PRIMARY},${TAG_NAME_SECONDARY}" \
  --data-urlencode 'limit=5')

if [[ "$pre_status" != "200" ]]; then
  log_error "Pre-assignment tag query failed" "status=${pre_status}"
  [[ -s "$pre_tmp" ]] && cat "$pre_tmp" >&2
  exit 1
fi

if jq -e --arg id "$link_id" '.items[] | select(.id == $id)' "$pre_tmp" >/dev/null 2>&1; then
  log_error "Link unexpectedly present before tag assignment" "link=${link_id}"
  cat "$pre_tmp" >&2
  exit 1
fi

assign_extra_body=$(jq -n --arg id "$tag_extra" '{tagIds: [($id|tonumber)]}')
assign_extra_tmp=$(mktemp)
cleanup_files+=("$assign_extra_tmp")
assign_extra_status=$(perform_request "$assign_extra_tmp" \
  --max-time "$POST_TIMEOUT" \
  -H 'Content-Type: application/json' \
  -X POST "$BASE_URL$POST_PATH/${link_id}/tags" \
  -d "$assign_extra_body")

if [[ "$assign_extra_status" != "201" ]]; then
  log_error "Unexpected initial tag assignment status" "status=${assign_extra_status}"
  [[ -s "$assign_extra_tmp" ]] && cat "$assign_extra_tmp" >&2
  exit 1
fi

if ! jq -e --arg id "$tag_extra" '(.tags | length == 1) and (.tags[0].id == ($id|tonumber))' "$assign_extra_tmp" >/dev/null 2>&1; then
  log_error "Initial tag assignment payload unexpected"
  cat "$assign_extra_tmp" >&2
  exit 1
fi

replace_body=$(jq -n --arg id1 "$tag_primary" --arg id2 "$tag_secondary" '{tagIds: [($id1|tonumber), ($id2|tonumber)]}')
replace_tmp=$(mktemp)
cleanup_files+=("$replace_tmp")
replace_status=$(perform_request "$replace_tmp" \
  --max-time "$POST_TIMEOUT" \
  -H 'Content-Type: application/json' \
  -X PUT "$BASE_URL$POST_PATH/${link_id}/tags" \
  -d "$replace_body")

if [[ "$replace_status" != "200" ]]; then
  log_error "Unexpected replace tag status" "status=${replace_status}"
  [[ -s "$replace_tmp" ]] && cat "$replace_tmp" >&2
  exit 1
fi

if ! jq -e --arg id1 "$tag_primary" --arg id2 "$tag_secondary" \
  '([.tags[].id] | sort == [($id1|tonumber), ($id2|tonumber)])' \
  "$replace_tmp" >/dev/null 2>&1; then
  log_error "Replace tag response mismatch"
  cat "$replace_tmp" >&2
  exit 1
fi

replace_again_tmp=$(mktemp)
cleanup_files+=("$replace_again_tmp")
replace_again_status=$(perform_request "$replace_again_tmp" \
  --max-time "$POST_TIMEOUT" \
  -H 'Content-Type: application/json' \
  -X PUT "$BASE_URL$POST_PATH/${link_id}/tags" \
  -d "$replace_body")

case "$replace_again_status" in
  200|201) ;;
  *)
    log_error "Idempotent tag replace failed" "status=${replace_again_status}"
    [[ -s "$replace_again_tmp" ]] && cat "$replace_again_tmp" >&2
    exit 1
    ;;
esac

if ! jq -e --arg id1 "$tag_primary" --arg id2 "$tag_secondary" \
  '([.tags[].id] | sort == [($id1|tonumber), ($id2|tonumber)])' \
  "$replace_again_tmp" >/dev/null 2>&1; then
  log_error "Repeat replace response mismatch"
  cat "$replace_again_tmp" >&2
  exit 1
fi

log_info "Tag replacement verified" "link=${link_id}"

and_tmp=$(mktemp)
cleanup_files+=("$and_tmp")
and_status=$(perform_request "$and_tmp" \
  --max-time "$GET_TIMEOUT" \
  -G "$BASE_URL$GET_PATH" \
  --data-urlencode "tags=${TAG_NAME_PRIMARY},${TAG_NAME_SECONDARY}" \
  --data-urlencode 'limit=5')

if [[ "$and_status" != "200" ]]; then
  log_error "AND tag query failed" "status=${and_status}"
  [[ -s "$and_tmp" ]] && cat "$and_tmp" >&2
  exit 1
fi

if ! jq -e --arg id "$link_id" '.items[] | select(.id == $id)' "$and_tmp" >/dev/null 2>&1; then
  log_error "AND tag query missing link" "link=${link_id}"
  [[ -s "$and_tmp" ]] && cat "$and_tmp" >&2
  exit 1
fi

and_negative_tmp=$(mktemp)
cleanup_files+=("$and_negative_tmp")
and_negative_status=$(perform_request "$and_negative_tmp" \
  --max-time "$GET_TIMEOUT" \
  -G "$BASE_URL$GET_PATH" \
  --data-urlencode "tags=${TAG_NAME_PRIMARY},${TAG_NAME_SECONDARY},${TAG_NAME_EXTRA}" \
  --data-urlencode 'limit=5')

if [[ "$and_negative_status" != "200" ]]; then
  log_error "AND negative tag query failed" "status=${and_negative_status}"
  [[ -s "$and_negative_tmp" ]] && cat "$and_negative_tmp" >&2
  exit 1
fi

if jq -e --arg id "$link_id" '.items[] | select(.id == $id)' "$and_negative_tmp" >/dev/null 2>&1; then
  log_error "Link unexpectedly present when requiring third tag" "link=${link_id}"
  [[ -s "$and_negative_tmp" ]] && cat "$and_negative_tmp" >&2
  exit 1
fi

log_info "Tag filtering verified" "link=${link_id}"

highlight_tmp=$(mktemp)
cleanup_files+=("$highlight_tmp")
highlight_body=$(jq -n --arg text "$HIGHLIGHT_QUOTE" --arg note "$HIGHLIGHT_NOTE" '{text: $text, note: $note}')

highlight_status=$(perform_request "$highlight_tmp" \
  --max-time "$POST_TIMEOUT" \
  -H 'Content-Type: application/json' \
  -X POST "$BASE_URL$POST_PATH/${link_id}/highlights" \
  -d "$highlight_body")

if [[ "$highlight_status" != "201" ]]; then
  log_error "Unexpected highlight status" "status=${highlight_status}"
  [[ -s "$highlight_tmp" ]] && cat "$highlight_tmp" >&2
  exit 1
fi

highlight_id=$(jq -r '.id // empty' "$highlight_tmp" 2>/dev/null || true)
if [[ -z "$highlight_id" ]]; then
  log_error "Highlight response missing id"
  [[ -s "$highlight_tmp" ]] && cat "$highlight_tmp" >&2
  exit 1
fi

log_info "Highlight created" "id=${highlight_id}"

highlight_check_tmp=$(mktemp)
cleanup_files+=("$highlight_check_tmp")
highlight_check_status=$(perform_request "$highlight_check_tmp" \
  --max-time "$GET_TIMEOUT" \
  -G "$BASE_URL$GET_PATH" \
  --data-urlencode "q=${QUERY}" \
  --data-urlencode 'limit=5')

if [[ "$highlight_check_status" != "200" ]]; then
  log_error "Highlight verification failed" "status=${highlight_check_status}"
  [[ -s "$highlight_check_tmp" ]] && cat "$highlight_check_tmp" >&2
  exit 1
fi

if ! jq -e --arg id "$link_id" --arg text "$HIGHLIGHT_QUOTE" --arg note "$HIGHLIGHT_NOTE" \
  '.items[] | select(.id == $id) | .highlights[] | select(.text == $text and (.note // "") == $note)' \
  "$highlight_check_tmp" >/dev/null 2>&1; then
  log_error "Highlight with note not present in results" "link=${link_id}"
  [[ -s "$highlight_check_tmp" ]] && cat "$highlight_check_tmp" >&2
  exit 1
fi

log_info "Highlight verification succeeded"

if [[ -n "$DIGEST_TEST_FLAG" ]]; then
  log_info "Running digest dry-run" "path=${DIGEST_PATH}" "smtp_url=${DIGEST_TRANSPORT}"
  digest_tmp=$(mktemp)
  cleanup_files+=("$digest_tmp")
  digest_body=$(jq -n --arg transport "$DIGEST_TRANSPORT" '{transport: $transport}')
  digest_status=$(perform_request "$digest_tmp" \
    --max-time "$DIGEST_TIMEOUT" \
    -H 'Content-Type: application/json' \
    -X POST "$BASE_URL$DIGEST_PATH" \
    -d "$digest_body")

  case "$digest_status" in
    200|201|202) ;;
    *)
      log_error "Digest dry-run failed" "status=${digest_status}"
      [[ -s "$digest_tmp" ]] && cat "$digest_tmp" >&2
      exit 1
      ;;
  esac

  if [[ -s "$digest_tmp" ]]; then
    if ! grep -q "Keepstack Digest" "$digest_tmp"; then
      log_error "Digest dry-run response missing marker"
      cat "$digest_tmp" >&2
      exit 1
    fi
  fi

  log_info "Digest dry-run completed"
else
  log_info "Digest dry-run skipped" "reason=DIGEST_TEST not set"
fi

log_info "Smoke test v0.2 completed successfully"

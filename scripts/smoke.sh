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

perform_request() {
  local output="$1"
  shift

  set +e
  local status
  status=$(curl -sS -o "$output" -w '%{http_code}' "$@")
  local exit_code=$?
  set -e

  if [[ $exit_code -ne 0 ]]; then
    log_error "Request failed" "exit=${exit_code}"
    if [[ -s "$output" ]]; then
      cat "$output" >&2
    fi
    exit 1
  fi

  printf '%s' "$status"
}

slug=$(printf 'smoke-%s-%s' "$(date +%s)" "$RANDOM")
LINK_URL="${SMOKE_LINK_URL:-https://example.com/${slug}}"
LINK_TITLE="${SMOKE_LINK_TITLE:-Smoke Test ${slug}}"
QUERY="${SMOKE_QUERY:-${slug}}"
TAG_NAME="${SMOKE_TAG_NAME:-Smoke Tag ${slug}}"
TAG_NAME_ALT="${SMOKE_TAG_NAME_ALT:-${TAG_NAME} Alt}"
HIGHLIGHT_QUOTE="${SMOKE_HIGHLIGHT_QUOTE:-Smoke highlight ${slug}}"
HIGHLIGHT_ANNOTATION="${SMOKE_HIGHLIGHT_ANNOTATION:-Smoke annotation ${slug}}"

log_info "Starting smoke test" "base=${BASE_URL}" "post=${POST_PATH}" "get=${GET_PATH}" "query=${QUERY}"

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

log_info "POST succeeded" "status=${post_status}" "url=${LINK_URL}" "id=${link_id}"

deadline=$((SECONDS + POLL_TIMEOUT))
attempt=1
link_found=false
while (( SECONDS <= deadline )); do
  remaining=$((deadline - SECONDS))
  log_info "Polling" "attempt=${attempt}" "remaining=${remaining}s"
  get_tmp=$(mktemp)
  cleanup_files+=("$get_tmp")

  get_status=$(perform_request "$get_tmp" \
    --max-time "$GET_TIMEOUT" \
    -G "$BASE_URL$GET_PATH" \
    --data-urlencode "q=${QUERY}" \
    --data-urlencode 'limit=5')

  if [[ "$get_status" != "200" ]]; then
    log_info "GET returned non-200" "status=${get_status}"
    if [[ -s "$get_tmp" ]]; then
      cat "$get_tmp" >&2
    fi
  else
    if grep -q "$slug" "$get_tmp"; then
      log_info "Link visible" "attempt=${attempt}"
      link_found=true
      break
    fi
    log_info "Link not visible yet"
  fi

  if (( SECONDS + POLL_INTERVAL > deadline )); then
    break
  fi

  sleep "$POLL_INTERVAL"
  ((attempt++))
done

if [[ "$link_found" != "true" ]]; then
  log_error "Smoke test timed out" "query=${QUERY}"
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
    -X POST "$BASE_URL/api/tags" \
    -d "$body")

  if [[ "$status" != "201" && "$status" != "200" && "$status" != "409" ]]; then
    log_error "Unexpected tag response" "name=${name}" "status=${status}"
    if [[ -s "$tmp" ]]; then
      cat "$tmp" >&2
    fi
    exit 1
  fi

  log_info "Tag ensured" "name=${name}" "status=${status}"

  local id
  if [[ "$status" == "201" ]]; then
    id=$(jq -r '.id // empty' "$tmp" 2>/dev/null || true)
  fi

  if [[ -z "$id" ]]; then
    local list_tmp
    list_tmp=$(mktemp)
    cleanup_files+=("$list_tmp")

    local list_status
    list_status=$(perform_request "$list_tmp" \
      --max-time "$GET_TIMEOUT" \
      -G "$BASE_URL/api/tags")

    if [[ "$list_status" != "200" ]]; then
      log_error "Failed to fetch tags" "status=${list_status}"
      if [[ -s "$list_tmp" ]]; then
        cat "$list_tmp" >&2
      fi
      exit 1
    fi

    id=$(jq -r --arg name "$name" '.tags[] | select(.name == $name) | .id' "$list_tmp" 2>/dev/null || true)
  fi

  if [[ -z "$id" ]]; then
    log_error "Unable to resolve tag id" "name=${name}"
    exit 1
  fi

  printf '%s' "$id"
}

tag_id=$(ensure_tag "$TAG_NAME")
tag_id_alt=$(ensure_tag "$TAG_NAME_ALT")

# Verify AND semantics prior to assignment
pre_assign_tmp=$(mktemp)
cleanup_files+=("$pre_assign_tmp")

pre_assign_status=$(perform_request "$pre_assign_tmp" \
  --max-time "$GET_TIMEOUT" \
  -G "$BASE_URL$GET_PATH" \
  --data-urlencode "tags=${TAG_NAME},${TAG_NAME_ALT}" \
  --data-urlencode 'limit=5')

if [[ "$pre_assign_status" != "200" ]]; then
  log_error "Pre-assignment tag query failed" "status=${pre_assign_status}"
  if [[ -s "$pre_assign_tmp" ]]; then
    cat "$pre_assign_tmp" >&2
  fi
  exit 1
fi

if jq -e --arg id "$link_id" '.items[] | select(.id == $id)' "$pre_assign_tmp" >/dev/null 2>&1; then
  log_error "Link unexpectedly present before tag assignment" "link=${link_id}"
  cat "$pre_assign_tmp" >&2
  exit 1
fi

assign_body=$(jq -n \
  --arg id1 "$tag_id" \
  --arg id2 "$tag_id_alt" \
  '{tagIds: [($id1|tonumber), ($id2|tonumber)]}')

assign_tmp=$(mktemp)
cleanup_files+=("$assign_tmp")

assign_status=$(perform_request "$assign_tmp" \
  --max-time "$POST_TIMEOUT" \
  -H 'Content-Type: application/json' \
  -X POST "$BASE_URL/api/links/${link_id}/tags" \
  -d "$assign_body")

if [[ "$assign_status" != "201" ]]; then
  log_error "Unexpected tag assignment response" "status=${assign_status}"
  if [[ -s "$assign_tmp" ]]; then
    cat "$assign_tmp" >&2
  fi
  exit 1
fi

if ! jq -e --arg id1 "$tag_id" --arg id2 "$tag_id_alt" \
  '([.tags[].id] | sort == [($id1|tonumber), ($id2|tonumber)])' \
  "$assign_tmp" >/dev/null 2>&1; then
  log_error "Unexpected tag assignment payload"
  cat "$assign_tmp" >&2
  exit 1
fi

assign_tmp_repeat=$(mktemp)
cleanup_files+=("$assign_tmp_repeat")

assign_status_repeat=$(perform_request "$assign_tmp_repeat" \
  --max-time "$POST_TIMEOUT" \
  -H 'Content-Type: application/json' \
  -X POST "$BASE_URL/api/links/${link_id}/tags" \
  -d "$assign_body")

if [[ "$assign_status_repeat" != "201" && "$assign_status_repeat" != "200" ]]; then
  log_error "Unexpected repeat tag assignment response" "status=${assign_status_repeat}"
  if [[ -s "$assign_tmp_repeat" ]]; then
    cat "$assign_tmp_repeat" >&2
  fi
  exit 1
fi

if ! jq -e --arg id1 "$tag_id" --arg id2 "$tag_id_alt" \
  '([.tags[].id] | sort == [($id1|tonumber), ($id2|tonumber)])' \
  "$assign_tmp_repeat" >/dev/null 2>&1; then
  log_error "Repeat tag assignment not idempotent"
  cat "$assign_tmp_repeat" >&2
  exit 1
fi

log_info "Tags assigned" "link=${link_id}" "tags=${TAG_NAME},${TAG_NAME_ALT}"

tag_query_tmp=$(mktemp)
cleanup_files+=("$tag_query_tmp")

tag_query_status=$(perform_request "$tag_query_tmp" \
  --max-time "$GET_TIMEOUT" \
  -G "$BASE_URL$GET_PATH" \
  --data-urlencode "tags=${TAG_NAME},${TAG_NAME_ALT}" \
  --data-urlencode 'limit=5')

if [[ "$tag_query_status" != "200" ]]; then
  log_error "Tag-filtered GET failed" "status=${tag_query_status}"
  if [[ -s "$tag_query_tmp" ]]; then
    cat "$tag_query_tmp" >&2
  fi
  exit 1
fi

if ! jq -e --arg id "$link_id" '.items[] | select(.id == $id)' "$tag_query_tmp" >/dev/null 2>&1; then
  log_error "Tag-filtered GET missing link" "link=${link_id}"
  if [[ -s "$tag_query_tmp" ]]; then
    cat "$tag_query_tmp" >&2
  fi
  exit 1
fi

log_info "Tag-filtered query succeeded" "tags=${TAG_NAME},${TAG_NAME_ALT}"

highlight_tmp=$(mktemp)
cleanup_files+=("$highlight_tmp")
highlight_body=$(jq -n --arg quote "$HIGHLIGHT_QUOTE" --arg annotation "$HIGHLIGHT_ANNOTATION" '{quote: $quote, annotation: $annotation}')

highlight_status=$(perform_request "$highlight_tmp" \
  --max-time "$POST_TIMEOUT" \
  -H 'Content-Type: application/json' \
  -X POST "$BASE_URL/api/links/${link_id}/highlights" \
  -d "$highlight_body")

if [[ "$highlight_status" != "201" ]]; then
  log_error "Unexpected highlight response" "status=${highlight_status}"
  if [[ -s "$highlight_tmp" ]]; then
    cat "$highlight_tmp" >&2
  fi
  exit 1
fi

highlight_id=$(jq -r '.id // empty' "$highlight_tmp" 2>/dev/null || true)
if [[ -z "$highlight_id" ]]; then
  log_error "Highlight response missing id"
  if [[ -s "$highlight_tmp" ]]; then
    cat "$highlight_tmp" >&2
  fi
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
  log_error "Highlight verification GET failed" "status=${highlight_check_status}"
  if [[ -s "$highlight_check_tmp" ]]; then
    cat "$highlight_check_tmp" >&2
  fi
  exit 1
fi

if ! jq -e --arg id "$link_id" --arg quote "$HIGHLIGHT_QUOTE" \
  '.items[] | select(.id == $id) | .highlights[] | select(.quote == $quote)' \
  "$highlight_check_tmp" >/dev/null 2>&1; then
  log_error "Highlight not present in query results" "link=${link_id}" "quote=${HIGHLIGHT_QUOTE}"
  if [[ -s "$highlight_check_tmp" ]]; then
    cat "$highlight_check_tmp" >&2
  fi
  exit 1
fi

log_info "Smoke test completed successfully"

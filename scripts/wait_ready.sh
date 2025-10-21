#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

RELEASE="${RELEASE:-keepstack}"
NAMESPACE="${NAMESPACE:-keepstack}"
CHART="${CHART:-${ROOT_DIR}/deploy/charts/keepstack}"
RELEASE_PREFIX="${RELEASE_PREFIX:-}"
COMPONENTS="${COMPONENTS:-api worker web resurfacer}"
ROLL_OUT_STATUS_TIMEOUT="${ROLL_OUT_STATUS_TIMEOUT:-60s}"
ROLL_OUT_MAX_ATTEMPTS="${ROLL_OUT_MAX_ATTEMPTS:-5}"
RESOURCE_POLL_INTERVAL="${RESOURCE_POLL_INTERVAL:-5}"
RESOURCE_CREATION_TIMEOUT="${RESOURCE_CREATION_TIMEOUT:-120}"

chart_dir="${CHART%/}"
if [[ -z "${RELEASE_PREFIX}" ]]; then
    if [[ -f "${chart_dir}/Chart.yaml" ]]; then
        chart_name="$(awk 'BEGIN{FS=": *"}/^name:/ {print $2; exit}' "${chart_dir}/Chart.yaml" | tr -d '[:space:]')"
    fi
    chart_name="${chart_name:-$(basename "${chart_dir}")}"
    RELEASE_PREFIX="${RELEASE}-${chart_name}"
fi

require_command() {
    local cmd="$1"
    if ! command -v "${cmd}" >/dev/null 2>&1; then
        echo "${cmd} is required but not installed" >&2
        exit 1
    fi
}

require_command kubectl

wait_for_resource_creation() {
    local resource="$1"
    local start="$(date +%s)"
    while true; do
        if kubectl -n "${NAMESPACE}" get "${resource}" >/dev/null 2>&1; then
            return 0
        fi
        local now="$(date +%s)"
        if (( now - start >= RESOURCE_CREATION_TIMEOUT )); then
            echo "Timed out waiting for ${resource} to be created" >&2
            return 1
        fi
        sleep "${RESOURCE_POLL_INTERVAL}"
    done
}

rollout_with_retries() {
    local resource="$1"
    local attempts=0
    while (( attempts < ROLL_OUT_MAX_ATTEMPTS )); do
        if kubectl -n "${NAMESPACE}" rollout status "${resource}" --timeout="${ROLL_OUT_STATUS_TIMEOUT}"; then
            return 0
        fi
        attempts=$((attempts + 1))
        echo "Retrying rollout status for ${resource} (${attempts}/${ROLL_OUT_MAX_ATTEMPTS})" >&2
    done
    return 1
}

dump_component_diagnostics() {
    local resource_type="$1"
    local resource_name="$2"
    local component="$3"
    kubectl -n "${NAMESPACE}" describe "${resource_type}" "${resource_name}" >&2 || true
    kubectl -n "${NAMESPACE}" get pods -l "app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=${component}" >&2 || true
    kubectl -n "${NAMESPACE}" logs -l "app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=${component}" --all-containers --tail=100 >&2 || true
}

declare -A RESOURCE_TYPES=(
    [api]="deployment"
    [worker]="deployment"
    [web]="deployment"
    [resurfacer]="cronjob"
)

for component in ${COMPONENTS}; do
    resource_type="${RESOURCE_TYPES[${component}]:-deployment}"
    resource_name="${RELEASE_PREFIX}-${component}"
    resource="${resource_type}/${resource_name}"
    echo "Waiting for ${resource}..."
    if ! wait_for_resource_creation "${resource}"; then
        dump_component_diagnostics "${resource_type}" "${resource_name}" "${component}"
        exit 1
    fi
    if ! rollout_with_retries "${resource}"; then
        echo "Rollout failed for ${resource}" >&2
        dump_component_diagnostics "${resource_type}" "${resource_name}" "${component}"
        exit 1
    fi
    echo "${resource} is ready."
done


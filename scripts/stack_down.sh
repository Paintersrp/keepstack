#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

CLUSTER="${CLUSTER:-keepstack}"
DELETE_CLUSTER="${DELETE_CLUSTER:-true}"
RELEASE="${RELEASE:-keepstack}"
NAMESPACE="${NAMESPACE:-keepstack}"
CHART="${CHART:-${ROOT_DIR}/deploy/charts/keepstack}"
DELETE_JOBS="${DELETE_JOBS:-true}"
JOB_SELECTOR="${JOB_SELECTOR:-app.kubernetes.io/instance=${RELEASE}}"
KUBE_CONTEXT="${KUBE_CONTEXT:-k3d-${CLUSTER}}"

chart_dir="${CHART%/}"
if [[ -f "${chart_dir}/Chart.yaml" ]]; then
    chart_name="$(awk 'BEGIN{FS=": *"}/^name:/ {print $2; exit}' "${chart_dir}/Chart.yaml" | tr -d '[:space:]')"
fi
chart_name="${chart_name:-$(basename "${chart_dir}")}"
RELEASE_PREFIX="${RELEASE_PREFIX:-${RELEASE}-${chart_name}}"

require_command() {
    local cmd="$1"
    if ! command -v "${cmd}" >/dev/null 2>&1; then
        echo "${cmd} is required but not installed" >&2
        exit 1
    fi
}

collect_namespace_events() {
    if ! kubectl get namespace "${NAMESPACE}" >/dev/null 2>&1; then
        return
    fi
    kubectl -n "${NAMESPACE}" get events --sort-by=.metadata.creationTimestamp | tail -n 20 >&2 || true
}

cluster_exists() {
    command -v k3d >/dev/null 2>&1 || return 1
    k3d cluster list -o json 2>/dev/null | grep -q "\"name\":\"${CLUSTER}\""
}

require_command kubectl
require_command helm

kubectl config use-context "${KUBE_CONTEXT}" >/dev/null 2>&1 || true

if [[ "${DELETE_JOBS}" == "true" ]]; then
    if kubectl get namespace "${NAMESPACE}" >/dev/null 2>&1; then
        echo "Deleting jobs in namespace ${NAMESPACE} matching selector ${JOB_SELECTOR}..."
        kubectl -n "${NAMESPACE}" delete job -l "${JOB_SELECTOR}" --ignore-not-found || true
        for job in "${RELEASE_PREFIX}-migrate" "${RELEASE_PREFIX}-verify-schema"; do
            kubectl -n "${NAMESPACE}" delete job "${job}" --ignore-not-found || true
        done
    fi
fi

if helm status "${RELEASE}" -n "${NAMESPACE}" >/dev/null 2>&1; then
    echo "Uninstalling Helm release ${RELEASE} from namespace ${NAMESPACE}..."
    if ! helm uninstall "${RELEASE}" -n "${NAMESPACE}"; then
        echo "Helm uninstall failed" >&2
        collect_namespace_events
        exit 1
    fi
else
    echo "Helm release ${RELEASE} not found in namespace ${NAMESPACE}; skipping uninstall"
fi

if [[ "${DELETE_CLUSTER}" == "true" ]]; then
    if cluster_exists; then
        echo "Deleting k3d cluster ${CLUSTER}..."
        k3d cluster delete "${CLUSTER}"
    else
        echo "k3d cluster ${CLUSTER} not found; skipping delete"
    fi
fi


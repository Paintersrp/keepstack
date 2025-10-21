#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

CLUSTER="${CLUSTER:-keepstack}"
CREATE_CLUSTER="${CREATE_CLUSTER:-true}"
K3D_CONFIG="${K3D_CONFIG:-${ROOT_DIR}/deploy/k3d/cluster.yaml}"
KUBE_CONTEXT="${KUBE_CONTEXT:-k3d-${CLUSTER}}"
INGRESS_KUSTOMIZE="${INGRESS_KUSTOMIZE:-${ROOT_DIR}/deploy/k3d/ingress-nginx}"
RELEASE="${RELEASE:-keepstack}"
NAMESPACE="${NAMESPACE:-keepstack}"
CHART="${CHART:-${ROOT_DIR}/deploy/charts/keepstack}"
VALUES_FILE="${VALUES_FILE:-${ROOT_DIR}/deploy/values/dev.yaml}"
STACK_ENV="${STACK_ENV:-}"
REGISTRY="${REGISTRY:-ghcr.io/Paintersrp}"
TAG="${TAG:-}"
IMAGE_IMPORT="${IMAGE_IMPORT:-true}"
WAIT_FOR_READY="${WAIT_FOR_READY:-true}"
HELM_DEBUG_FLAG="${HELM_DEBUG:-}"
HELM_ARGS="${HELM_ARGS:-}"

if [[ -n "${STACK_ENV}" ]]; then
    candidate_values="${ROOT_DIR}/deploy/values/${STACK_ENV}.yaml"
    if [[ -f "${candidate_values}" ]]; then
        VALUES_FILE="${candidate_values}"
    fi
fi

sanitize_registry() {
    local value="$1"
    echo "${value}" | tr '[:upper:]' '[:lower:]'
}

if [[ -z "${TAG}" ]]; then
    if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
        TAG="sha-$(git rev-parse --short HEAD)"
    else
        TAG="latest"
    fi
fi

REGISTRY_SANITIZED="$(sanitize_registry "${REGISTRY}")"
API_IMAGE="${API_IMAGE:-${REGISTRY_SANITIZED}/keepstack-api:${TAG}}"
WORKER_IMAGE="${WORKER_IMAGE:-${REGISTRY_SANITIZED}/keepstack-worker:${TAG}}"
WEB_IMAGE="${WEB_IMAGE:-${REGISTRY_SANITIZED}/keepstack-web:${TAG}}"

chart_dir="${CHART%/}"
if [[ -f "${chart_dir}/Chart.yaml" ]]; then
    chart_name="$(awk 'BEGIN{FS=": *"}/^name:/ {print $2; exit}' "${chart_dir}/Chart.yaml" | tr -d '[:space:]')"
fi
chart_name="${chart_name:-$(basename "${chart_dir}")}"
RELEASE_PREFIX="${RELEASE_PREFIX:-${RELEASE}-${chart_name}}"

function require_command() {
    local cmd="$1"
    if ! command -v "${cmd}" >/dev/null 2>&1; then
        echo "${cmd} is required but not installed" >&2
        exit 1
    fi
}

require_command kubectl
require_command helm

collect_ingress_diagnostics() {
    echo "Collecting ingress-nginx diagnostics..." >&2
    kubectl -n ingress-nginx get pods >&2 || true
    kubectl -n ingress-nginx describe pods -l app.kubernetes.io/component=controller >&2 || true
    kubectl -n ingress-nginx logs deploy/ingress-nginx-controller >&2 || true
}

collect_release_diagnostics() {
    echo "Collecting Helm release diagnostics..." >&2
    kubectl -n "${NAMESPACE}" get pods >&2 || true
    kubectl -n "${NAMESPACE}" get jobs >&2 || true
    kubectl -n "${NAMESPACE}" get events --sort-by=.metadata.creationTimestamp | tail -n 20 >&2 || true
}

cluster_exists() {
    command -v k3d >/dev/null 2>&1 || return 1
    k3d cluster list -o json 2>/dev/null | grep -q "\"name\":\"${CLUSTER}\""
}

if [[ "${CREATE_CLUSTER}" == "true" ]]; then
    require_command k3d
    if ! cluster_exists; then
        echo "Creating k3d cluster ${CLUSTER}..."
        k3d cluster create --config "${K3D_CONFIG}"
    else
        echo "Reusing existing k3d cluster ${CLUSTER}"
    fi
fi

kubectl config use-context "${KUBE_CONTEXT}" >/dev/null 2>&1 || true

echo "Waiting for Kubernetes nodes to become ready..."
if ! kubectl wait --for=condition=Ready node --all --timeout=180s; then
    kubectl get nodes || true
    echo "Nodes failed to become ready" >&2
    exit 1
fi

if [[ -d "${INGRESS_KUSTOMIZE}" ]]; then
    echo "Applying ingress-nginx manifests..."
    kubectl apply -k "${INGRESS_KUSTOMIZE}"
    echo "Waiting for ingress-nginx controller..."
    if ! kubectl -n ingress-nginx wait --for=condition=Ready pods --selector=app.kubernetes.io/component=controller --timeout=180s; then
        collect_ingress_diagnostics
        exit 1
    fi
fi

if [[ "${IMAGE_IMPORT}" == "true" ]]; then
    require_command k3d
    echo "Importing images into k3d cluster ${CLUSTER}..."
    k3d image import "${API_IMAGE}" "${WORKER_IMAGE}" "${WEB_IMAGE}" --cluster "${CLUSTER}"
fi

helm_debug_args=()
if [[ -n "${HELM_DEBUG_FLAG}" ]]; then
    helm_debug_args+=("--debug")
fi

release_exists="true"
if ! helm status "${RELEASE}" -n "${NAMESPACE}" >/dev/null 2>&1; then
    release_exists="false"
fi

wait_flag=("--wait")
if [[ "${release_exists}" == "false" ]]; then
    wait_flag=()
fi

if [[ ! -f "${VALUES_FILE}" ]]; then
    echo "Values file ${VALUES_FILE} not found" >&2
    exit 1
fi

echo "Deploying Helm release ${RELEASE} into namespace ${NAMESPACE}..."
if ! helm upgrade --install "${RELEASE}" "${chart_dir}" -n "${NAMESPACE}" --create-namespace -f "${VALUES_FILE}" \
    --set image.registry="${REGISTRY_SANITIZED}" --set image.tag="${TAG}" "${wait_flag[@]}" --timeout 10m "${helm_debug_args[@]}" ${HELM_ARGS}; then
    status=$?
    echo "Helm upgrade failed" >&2
    collect_release_diagnostics
    exit "${status}"
fi

if [[ "${release_exists}" == "false" && "${WAIT_FOR_READY}" == "true" ]]; then
    echo "Waiting for core workloads to become ready..."
    RELEASE="${RELEASE}" NAMESPACE="${NAMESPACE}" CHART="${chart_dir}" RELEASE_PREFIX="${RELEASE_PREFIX}" "${SCRIPT_DIR}/wait_ready.sh"
fi


#!/usr/bin/env bash
set -euo pipefail

# --- Config (tweak as needed) -------------------------------------------------
HELM_RELEASE="kube-prom-stack"
HELM_NS="monitoring"
HELM_CHART="prometheus-community/kube-prometheus-stack"

# --- Flags --------------------------------------------------------------------
SKIP_PULL=0
SKIP_OBS=0  # skip the kube-prometheus-stack step
SMOKE=1     # run smoke tests
TAIL_LOGS=1 # tail logs at the end

# --- CLI ----------------------------------------------------------------------
usage() {
  cat <<EOF
Usage: $0 [options]

Options:
  --no-pull          Skip 'git pull'
  --no-obs           Skip installing/upgrading kube-prometheus-stack
  --no-smoke         Skip 'make smoke'
  --no-logs          Skip 'make logs' at the end
  --ns <namespace>   Helm namespace to use (default: ${HELM_NS})
  -h, --help         Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  --no-pull)
    SKIP_PULL=1
    shift
    ;;
  --no-obs)
    SKIP_OBS=1
    shift
    ;;
  --no-smoke)
    SMOKE=0
    shift
    ;;
  --no-logs)
    TAIL_LOGS=0
    shift
    ;;
  --ns)
    HELM_NS="$2"
    shift 2
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    echo "Unknown arg: $1"
    usage
    exit 1
    ;;
  esac
done

# --- Helpers ------------------------------------------------------------------
ts() { date +"%Y-%m-%d %H:%M:%S"; }
log() { echo -e "[$(ts)] $*"; }

run() {
  # run <cmd...>
  log "→ $*"
  "$@"
}

retry() {
  # retry <attempts> <sleep> <cmd...>
  local attempts="$1"
  shift
  local delay="$1"
  shift
  local n=1
  until "$@"; do
    if ((n >= attempts)); then
      log "✗ Command failed after ${attempts} attempts: $*"
      return 1
    fi
    log "… retry ${n}/${attempts} in ${delay}s: $*"
    sleep "${delay}"
    ((n++))
  done
}

# Ensure helm repo exists (idempotent)
ensure_helm_repo() {
  if ! helm repo list | awk '{print $1}' | grep -q '^prometheus-community$'; then
    run helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
  fi
  run helm repo update
}

ensure_ns() {
  if ! kubectl get ns "${HELM_NS}" >/dev/null 2>&1; then
    run kubectl create ns "${HELM_NS}"
  fi
}

# --- Steps mapped to your history --------------------------------------------
step_dev_down() { run make dev-down; }
step_git_pull() { [[ $SKIP_PULL -eq 1 ]] || run git pull; }
step_bootstrap_dev() { run make bootstrap-dev; }
step_install_obs() {
  [[ $SKIP_OBS -eq 1 ]] && {
    log "Skipping obs install/upgrade"
    return 0
  }
  ensure_helm_repo
  ensure_ns
  # --wait makes this block until the stack is ready
  retry 2 10 \
    helm upgrade --install "${HELM_RELEASE}" "${HELM_CHART}" \
    --namespace "${HELM_NS}" --create-namespace --wait
}
step_helm_dev() { run make helm-dev; }
step_verify_obs() { run make verify-obs; }
step_smoke() { [[ $SMOKE -eq 1 ]] && run make smoke || log "Skipping smoke"; }
step_logs() { [[ $TAIL_LOGS -eq 1 ]] && run make logs || log "Skipping logs"; }

# --- Main ---------------------------------------------------------------------
log "Starting dev cycle"
step_dev_down
step_git_pull
step_bootstrap_dev
step_install_obs
step_helm_dev
step_verify_obs
step_smoke
step_logs
log "Dev cycle complete ✔"

#!/usr/bin/env bash
set -euo pipefail

log() {
  local ts
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  echo "[${ts}] $*" >&2
}

require() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

require kubectl
require helm
require jq
require mktemp

SRC_NS=${SRC_NS:-keepstack}
DST_NS=${DST_NS:-restore-drill}
RELEASE_NAME=${RELEASE_NAME:-keepstack}
CHART_PATH=${CHART_PATH:-deploy/charts/keepstack}
VALUES_FILES=${VALUES_FILES:-deploy/values/dev.yaml}
HELM_EXTRA_ARGS=${HELM_EXTRA_ARGS:-}
BACKUP_PVC_NAME=${BACKUP_PVC_NAME:-keepstack-backups}
BACKUP_DIR=${BACKUP_DIR:-/backups}
RESTORE_JOB_NAME=${RESTORE_JOB_NAME:-${RELEASE_NAME}-restore}
RESTORE_SCRIPT_CONFIGMAP=${RESTORE_SCRIPT_CONFIGMAP:-${RELEASE_NAME}-restore-script}
RESTORE_SERVICE_ACCOUNT=${RESTORE_SERVICE_ACCOUNT:-${RELEASE_NAME}-api}
VALIDATION_JOB_NAME=${VALIDATION_JOB_NAME:-${RELEASE_NAME}-validate}
VALIDATION_URL=${VALIDATION_URL:-http://${RELEASE_NAME}-api:8080/api/links}
SRC_HELPER_POD=${SRC_HELPER_POD:-${RELEASE_NAME}-backup-src}
DST_HELPER_POD=${DST_HELPER_POD:-${RELEASE_NAME}-backup-dst}
HELM_TIMEOUT=${HELM_TIMEOUT:-10m0s}
RESTORE_TIMEOUT=${RESTORE_TIMEOUT:-10m0s}
VALIDATION_TIMEOUT=${VALIDATION_TIMEOUT:-2m0s}

IFS=',' read -r -a VALUE_FILE_ARRAY <<<"${VALUES_FILES}"
VALUE_ARGS=()
for value_file in "${VALUE_FILE_ARRAY[@]}"; do
  if [[ -n "${value_file}" ]]; then
    VALUE_ARGS+=( -f "${value_file}" )
  fi
done

read -r -a HELM_EXTRA_ARGS_ARRAY <<<"${HELM_EXTRA_ARGS}"

TMP_BACKUP_FILE=""
cleanup() {
  local exit_code=$?
  set +e
  if [[ -n "${TMP_BACKUP_FILE}" && -f "${TMP_BACKUP_FILE}" ]]; then
    rm -f "${TMP_BACKUP_FILE}"
  fi
  kubectl -n "${SRC_NS}" delete pod "${SRC_HELPER_POD}" --ignore-not-found >/dev/null 2>&1
  kubectl -n "${DST_NS}" delete pod "${DST_HELPER_POD}" --ignore-not-found >/dev/null 2>&1
  kubectl -n "${DST_NS}" delete job "${VALIDATION_JOB_NAME}" --ignore-not-found >/dev/null 2>&1
  set -e
  exit "$exit_code"
}
trap cleanup EXIT

log "Ensuring recent backup exists in ${SRC_NS}"
kubectl -n "${SRC_NS}" delete pod "${SRC_HELPER_POD}" --ignore-not-found >/dev/null 2>&1
cat <<YAML | kubectl -n "${SRC_NS}" apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${SRC_HELPER_POD}
  labels:
    app.kubernetes.io/name: ${SRC_HELPER_POD}
spec:
  restartPolicy: Never
  containers:
    - name: shell
      image: alpine:3.19
      command: ["sh", "-c", "sleep 3600"]
      volumeMounts:
        - name: backups
          mountPath: ${BACKUP_DIR}
  volumes:
    - name: backups
      persistentVolumeClaim:
        claimName: ${BACKUP_PVC_NAME}
YAML
kubectl -n "${SRC_NS}" wait --for=condition=Ready "pod/${SRC_HELPER_POD}" --timeout=120s >/dev/null

BACKUP_PATH=$(kubectl -n "${SRC_NS}" exec "${SRC_HELPER_POD}" -- sh -c "ls -1t ${BACKUP_DIR}/keepstack-*.sql.gz 2>/dev/null | head -n1" | tr -d '\r')
if [[ -z "${BACKUP_PATH}" ]]; then
  echo "no backup files found in ${SRC_NS}:${BACKUP_DIR}" >&2
  exit 1
fi
BACKUP_BASENAME=$(basename "${BACKUP_PATH}")
log "Latest backup: ${BACKUP_BASENAME}"
TMP_BACKUP_FILE=$(mktemp "/tmp/${BACKUP_BASENAME}.XXXXXX")
log "Copying backup ${BACKUP_BASENAME} to ${TMP_BACKUP_FILE}"
rm -f "${TMP_BACKUP_FILE}"
kubectl cp "${SRC_NS}/${SRC_HELPER_POD}:${BACKUP_PATH}" "${TMP_BACKUP_FILE}" >/dev/null
kubectl -n "${SRC_NS}" delete pod "${SRC_HELPER_POD}" --ignore-not-found >/dev/null 2>&1

if kubectl get namespace "${DST_NS}" >/dev/null 2>&1; then
  log "Deleting namespace ${DST_NS}"
  kubectl delete namespace "${DST_NS}" --wait >/dev/null
fi
log "Creating namespace ${DST_NS}"
kubectl create namespace "${DST_NS}" >/dev/null

log "Installing database-only release ${RELEASE_NAME} in ${DST_NS}"
helm upgrade --install "${RELEASE_NAME}" "${CHART_PATH}" \
  -n "${DST_NS}" \
  "${VALUE_ARGS[@]}" \
  "${HELM_EXTRA_ARGS_ARRAY[@]}" \
  --set api.replicas=0 \
  --set worker.replicas=0 \
  --set web.replicas=0 \
  --wait \
  --timeout "${HELM_TIMEOUT}" >/dev/null

log "Waiting for PostgreSQL to become ready"
kubectl -n "${DST_NS}" rollout status statefulset/"${RELEASE_NAME}"-postgres --timeout="${HELM_TIMEOUT}" >/dev/null
kubectl -n "${DST_NS}" wait --for=condition=Bound pvc/"${BACKUP_PVC_NAME}" --timeout=120s >/dev/null

log "Copying backup into destination namespace"
kubectl -n "${DST_NS}" delete pod "${DST_HELPER_POD}" --ignore-not-found >/dev/null 2>&1
cat <<YAML | kubectl -n "${DST_NS}" apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${DST_HELPER_POD}
  labels:
    app.kubernetes.io/name: ${DST_HELPER_POD}
spec:
  restartPolicy: Never
  containers:
    - name: shell
      image: alpine:3.19
      command: ["sh", "-c", "sleep 3600"]
      volumeMounts:
        - name: backups
          mountPath: ${BACKUP_DIR}
  volumes:
    - name: backups
      persistentVolumeClaim:
        claimName: ${BACKUP_PVC_NAME}
YAML
kubectl -n "${DST_NS}" wait --for=condition=Ready "pod/${DST_HELPER_POD}" --timeout=120s >/dev/null
DST_BACKUP_PATH="${BACKUP_DIR}/${BACKUP_BASENAME}"
kubectl cp "${TMP_BACKUP_FILE}" "${DST_NS}/${DST_HELPER_POD}:${DST_BACKUP_PATH}" >/dev/null

log "Preparing restore job resources"
kubectl -n "${DST_NS}" create configmap "${RESTORE_SCRIPT_CONFIGMAP}" \
  --from-file=restore-db.sh=scripts/restore-db.sh \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

VALUES_JSON=$(helm -n "${DST_NS}" get values "${RELEASE_NAME}" -o json)
IMAGE_REGISTRY=$(echo "${VALUES_JSON}" | jq -r '.image.registry // ""')
API_REPOSITORY=$(echo "${VALUES_JSON}" | jq -r '.image.apiRepository // "keepstack-api"')
IMAGE_TAG=$(echo "${VALUES_JSON}" | jq -r '.image.tag // "latest"')
IMAGE_PULL_POLICY=$(echo "${VALUES_JSON}" | jq -r '.image.pullPolicy // "IfNotPresent"')
SECRETS_NAME=$(echo "${VALUES_JSON}" | jq -r '.secrets.name // "keepstack-secrets"')
if [[ -n "${IMAGE_REGISTRY}" ]]; then
  RESTORE_IMAGE="${IMAGE_REGISTRY}/${API_REPOSITORY}:${IMAGE_TAG}"
else
  RESTORE_IMAGE="${API_REPOSITORY}:${IMAGE_TAG}"
fi

log "Running restore job ${RESTORE_JOB_NAME}"
kubectl -n "${DST_NS}" delete job "${RESTORE_JOB_NAME}" --ignore-not-found >/dev/null 2>&1
cat <<YAML | kubectl -n "${DST_NS}" apply -f - >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: ${RESTORE_JOB_NAME}
  labels:
    app.kubernetes.io/name: ${RESTORE_JOB_NAME}
    app.kubernetes.io/component: restore
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ${RESTORE_JOB_NAME}
        app.kubernetes.io/component: restore
    spec:
      serviceAccountName: ${RESTORE_SERVICE_ACCOUNT}
      restartPolicy: Never
      containers:
        - name: restore
          image: ${RESTORE_IMAGE}
          imagePullPolicy: ${IMAGE_PULL_POLICY}
          command:
            - /bin/sh
            - -c
            - |
              set -euo pipefail
              chmod +x /scripts/restore-db.sh
              /scripts/restore-db.sh "${DST_BACKUP_PATH}"
          envFrom:
            - secretRef:
                name: ${SECRETS_NAME}
          volumeMounts:
            - name: restore-script
              mountPath: /scripts
            - name: backup-data
              mountPath: ${BACKUP_DIR}
      volumes:
        - name: restore-script
          configMap:
            name: ${RESTORE_SCRIPT_CONFIGMAP}
            defaultMode: 0555
        - name: backup-data
          persistentVolumeClaim:
            claimName: ${BACKUP_PVC_NAME}
YAML
kubectl -n "${DST_NS}" wait --for=condition=Complete "job/${RESTORE_JOB_NAME}" --timeout="${RESTORE_TIMEOUT}" >/dev/null
kubectl -n "${DST_NS}" logs job/"${RESTORE_JOB_NAME}"

log "Re-enabling full application stack"
helm upgrade --install "${RELEASE_NAME}" "${CHART_PATH}" \
  -n "${DST_NS}" \
  "${VALUE_ARGS[@]}" \
  "${HELM_EXTRA_ARGS_ARRAY[@]}" \
  --reset-values \
  --wait \
  --timeout "${HELM_TIMEOUT}" >/dev/null

for deploy in api worker web; do
  if kubectl -n "${DST_NS}" get deploy "${RELEASE_NAME}-${deploy}" >/dev/null 2>&1; then
    log "Waiting for deployment ${RELEASE_NAME}-${deploy}"
    kubectl -n "${DST_NS}" rollout status deploy/"${RELEASE_NAME}-${deploy}" --timeout="${HELM_TIMEOUT}" >/dev/null
  fi
done

log "Validating application response from ${VALIDATION_URL}"
kubectl -n "${DST_NS}" delete job "${VALIDATION_JOB_NAME}" --ignore-not-found >/dev/null 2>&1
kubectl -n "${DST_NS}" create job "${VALIDATION_JOB_NAME}" --image=curlimages/curl:8.6.0 -- \
  curl -fsS "${VALIDATION_URL}"
kubectl -n "${DST_NS}" wait --for=condition=Complete "job/${VALIDATION_JOB_NAME}" --timeout="${VALIDATION_TIMEOUT}" >/dev/null
kubectl -n "${DST_NS}" logs job/"${VALIDATION_JOB_NAME}"

kubectl -n "${DST_NS}" delete job "${RESTORE_JOB_NAME}" --ignore-not-found >/dev/null 2>&1
kubectl -n "${DST_NS}" delete pod "${DST_HELPER_POD}" --ignore-not-found >/dev/null 2>&1
kubectl -n "${DST_NS}" delete job "${VALIDATION_JOB_NAME}" --ignore-not-found >/dev/null 2>&1

log "Restore drill completed successfully"

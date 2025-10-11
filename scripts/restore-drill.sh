#!/usr/bin/env bash
set -euo pipefail

NAMESPACE=${NAMESPACE:-keepstack}
CHART=${CHART_PATH:-deploy/charts/keepstack}
VALUES=${VALUES_FILE:-deploy/values/dev.yaml}

cat <<'INSTRUCTIONS'
# Keepstack restore drill
1. Scale down workloads and confirm a fresh backup is available:
   just backup-now
   BACKUP_JOB=$(kubectl -n ${NAMESPACE} get jobs -l app.kubernetes.io/component=backup \
     --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}')
   kubectl -n ${NAMESPACE} wait --for=condition=complete "job/${BACKUP_JOB}" --timeout=120s || true

2. Capture the latest backup file path by mounting the keepstack-backups PVC on a helper pod:
   cat <<'YAML' | kubectl -n ${NAMESPACE} apply -f -
   apiVersion: v1
   kind: Pod
   metadata:
     name: backup-shell
   spec:
     restartPolicy: Never
     containers:
     - name: shell
       image: alpine:3.19
       command: ["sleep", "3600"]
       volumeMounts:
       - name: backups
         mountPath: /backups
     volumes:
     - name: backups
       persistentVolumeClaim:
         claimName: keepstack-backups
   YAML
   kubectl -n ${NAMESPACE} wait --for=condition=Ready pod/backup-shell --timeout=60s
   kubectl -n ${NAMESPACE} exec pod/backup-shell -- ls -1t /backups | head -n1
   kubectl -n ${NAMESPACE} delete pod/backup-shell

3. Simulate disaster by removing the release:
   helm uninstall keepstack -n ${NAMESPACE}

4. Reinstall Postgres only:
   helm upgrade --install keepstack ${CHART} -n ${NAMESPACE} -f ${VALUES} \
     --set api.replicas=0 --set worker.replicas=0 --set web.replicas=0 --wait

5. Run the restore job example:
   helm template keepstack ${CHART} -n "${NAMESPACE}" -f "${VALUES}" --show-only templates/job-restore-example.yaml \
     | kubectl -n "${NAMESPACE}" apply -f -
   kubectl -n ${NAMESPACE} wait --for=condition=complete job/keepstack-restore --timeout=180s

6. Re-enable the application:
   helm upgrade --install keepstack ${CHART} -n ${NAMESPACE} -f ${VALUES} --wait

7. Verify data is present:
   curl http://keepstack.localtest.me:8080/api/links | jq '.total_count'
INSTRUCTIONS

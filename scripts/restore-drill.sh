#!/usr/bin/env bash
set -euo pipefail

NAMESPACE=${NAMESPACE:-keepstack}
CHART=${CHART_PATH:-deploy/charts/keepstack}
VALUES=${VALUES_FILE:-deploy/values/dev.yaml}

cat <<'INSTRUCTIONS'
# Keepstack restore drill
1. Scale down workloads and confirm a fresh backup is available:
   just backup-now
   kubectl -n ${NAMESPACE} wait --for=condition=complete job -l job-name=keepstack-backup-now --timeout=120s || true

2. Capture the latest backup file path:
   kubectl -n ${NAMESPACE} exec deploy/keepstack-api -- ls -1t /backups | head -n1

3. Simulate disaster by removing the release:
   helm uninstall keepstack -n ${NAMESPACE}

4. Reinstall Postgres only:
   helm upgrade --install keepstack ${CHART} -n ${NAMESPACE} -f ${VALUES} \
     --set api.replicas=0 --set worker.replicas=0 --set web.replicas=0 --wait

5. Run the restore job example:
   kubectl -n ${NAMESPACE} apply -f deploy/charts/keepstack/templates/job-restore-example.yaml
   kubectl -n ${NAMESPACE} wait --for=condition=complete job/keepstack-restore --timeout=180s

6. Re-enable the application:
   helm upgrade --install keepstack ${CHART} -n ${NAMESPACE} -f ${VALUES} --wait

7. Verify data is present:
   curl http://keepstack.localtest.me:8080/api/links | jq '.total_count'
INSTRUCTIONS

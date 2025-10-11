set shell := ["bash", "-cu"]

REGISTRY := env_var_or_default("REGISTRY", "ghcr.io/YOUR_GH_USERNAME_OR_ORG")
TAG := env_var_or_default("TAG", "sha-$(git rev-parse --short HEAD)")
NAMESPACE := "keepstack"
CHART := "deploy/charts/keepstack"
DEV_VALUES := "deploy/values/dev.yaml"
VERIFY_JOB := "keepstack-keepstack-verify-schema"

alias d := dev-up

dev-up:
	k3d cluster create --config deploy/k3d/cluster.yaml
	kubectl cluster-info
	kubectl wait --for=condition=Ready node/k3d-keepstack-server-0 --timeout=120s
	kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
	kubectl wait --namespace ingress-nginx --for=condition=Ready pods --selector=app.kubernetes.io/component=controller --timeout=180s || (
		kubectl -n ingress-nginx get pods;
		kubectl -n ingress-nginx describe pods -l app.kubernetes.io/component=controller;
		kubectl -n ingress-nginx logs deploy/ingress-nginx-controller || true;
		exit 1
	)

dev-down:
	k3d cluster delete keepstack

build:
	docker buildx build --platform linux/amd64 \
	--tag {{REGISTRY}}/keepstack-api:{{TAG}} \
	-f apps/api/Dockerfile .
	docker buildx build --platform linux/amd64 \
	--tag {{REGISTRY}}/keepstack-worker:{{TAG}} \
	-f apps/worker/Dockerfile .
	docker buildx build --platform linux/amd64 \
	--tag {{REGISTRY}}/keepstack-web:{{TAG}} \
	-f apps/web/Dockerfile .

push:
	docker push {{REGISTRY}}/keepstack-api:{{TAG}}
	docker push {{REGISTRY}}/keepstack-worker:{{TAG}}
	docker push {{REGISTRY}}/keepstack-web:{{TAG}}

helm-dev:
	helm upgrade --install keepstack {{CHART}} -n {{NAMESPACE}} --create-namespace -f {{DEV_VALUES}} --set image.registry={{REGISTRY}} --set image.tag={{TAG}}

logs:
        kubectl -n {{NAMESPACE}} logs deploy/keepstack-api -f

seed:
        curl -fsS -X POST "http://keepstack.localtest.me:8080/api/links" \
        -H 'Content-Type: application/json' \
        -d '{"url":"https://example.com","title":"Example Domain"}'

dash-grafana:
        kubectl -n monitoring port-forward svc/grafana 3000:80

smoke:
        {{justfile_directory()}}/scripts/smoke.sh

smoke-v02:
        if [[ -n "${DIGEST_TEST:-}" && -z "${SMTP_URL:-}" ]]; then export SMTP_URL=log://; fi
        {{justfile_directory()}}/scripts/smoke-v02.sh

digest-once:
        kubectl -n {{NAMESPACE}} create job digest-once-$(date +%s) --from=cronjob/keepstack-digest

# Environment: KS_NAMESPACE, KS_RELEASE, KS_BACKUP_JOB_PREFIX, KS_BACKUP_TIME_FORMAT, KS_BACKUP_TIMEOUT, KS_BACKUP_FOLLOW_LOGS
backup-now:
        {{justfile_directory()}}/scripts/backup-now.sh

resurfacer-now:
        kubectl -n {{NAMESPACE}} create job keepstack-resurfacer-now-$(date +%s) --from=cronjob/keepstack-resurfacer

# Environment: KS_NAMESPACE, KS_RELEASE, KS_API_METRICS_PORT, KS_WORKER_METRICS_PORT
verify-obs:
        {{justfile_directory()}}/scripts/verify-obs.sh

# Environment: KS_NAMESPACE, KS_RELEASE, PROM_NAMESPACE, PROM_RELEASE, PROM_SERVICE, SMOKE_BASE_URL,
#              KS_ALERT_API_ERROR_DURATION, KS_ALERT_WORKER_FAILURE_COUNT, KS_ALERT_WORKER_FAILURE_INTERVAL,
#              KS_ALERT_TIMEOUT_SECONDS, KS_ALERT_API_RECOVERY_REQUESTS, KS_ALERT_PROM_LOCAL_PORT,
#              KS_ALERT_API_WINDOW, KS_ALERT_API_FOR, KS_ALERT_WORKER_WINDOW, KS_ALERT_WORKER_FOR
verify-alerts:
        {{justfile_directory()}}/scripts/verify-alerts.sh

restore-drill:
        {{justfile_directory()}}/scripts/restore-drill.sh

# Environment: KS_NAMESPACE, KS_RELEASE, SMOKE_BASE_URL, KS_ROLLOUT_CURL_INTERVAL, KS_ROLLOUT_CURL_TIMEOUT,
#              KS_ROLLOUT_ROLLBACK, KS_ROLLOUT_TIMEOUT
rollout-observe:
        {{justfile_directory()}}/scripts/rollout-observe.sh

# Environment: SMOKE_BASE_URL, KS_NAMESPACE, KS_RELEASE, KS_RESURF_LIMIT, KS_RESURF_TIMEOUT
smoke-v03:
        {{justfile_directory()}}/scripts/smoke-v03.sh

verify-schema:
        kubectl -n {{NAMESPACE}} delete job {{VERIFY_JOB}} --ignore-not-found
        helm template keepstack {{CHART}} -n {{NAMESPACE}} --set image.registry={{REGISTRY}} --set image.tag={{TAG}} --show-only templates/job-verify-schema.yaml | kubectl -n {{NAMESPACE}} apply -f -
        kubectl -n {{NAMESPACE}} wait --for=condition=Complete job/{{VERIFY_JOB}} --timeout=120s || (kubectl -n {{NAMESPACE}} logs job/{{VERIFY_JOB}} && exit 1)
        kubectl -n {{NAMESPACE}} logs job/{{VERIFY_JOB}}

test:
        (cd apps/api && go test ./...)
        (cd apps/worker && go test ./...)
        (cd apps/web && npm run build)

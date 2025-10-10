set shell := ["bash", "-cu"]

REGISTRY := env_var_or_default("REGISTRY", "ghcr.io/YOUR_GH_USERNAME_OR_ORG")
TAG := env_var_or_default("TAG", "sha-$(git rev-parse --short HEAD)")
NAMESPACE := "keepstack"
CHART := "deploy/charts/keepstack"
DEV_VALUES := "deploy/values/dev.yaml"

alias d := dev-up

dev-up:
	k3d cluster create --config deploy/k3d/cluster.yaml
	kubectl cluster-info
	kubectl wait --for=condition=Ready node/k3d-keepstack-server-0 --timeout=120s
	kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
	kubectl wait --namespace ingress-nginx --for=condition=Ready pods --selector=app.kubernetes.io/component=controller --timeout=180s

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

smoke:
        {{justfile_directory()}}/scripts/smoke.sh

smoke-v02:
        if [[ -n "${DIGEST_TEST:-}" && -z "${SMTP_TRANSPORT:-}" ]]; then export SMTP_TRANSPORT=log; fi
        {{justfile_directory()}}/scripts/smoke-v02.sh

digest-once:
        kubectl -n {{NAMESPACE}} create job digest-once-$(date +%s) --from=cronjob/keepstack-digest

test:
        (cd apps/api && go test ./...)
        (cd apps/worker && go test ./...)
        (cd apps/web && npm run build)

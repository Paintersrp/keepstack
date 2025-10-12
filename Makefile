SHELL := /bin/bash

REGISTRY ?= ghcr.io/Paintersrp
TAG ?= sha-$(shell git rev-parse --short HEAD)
RELEASE ?= keepstack
NAMESPACE ?= keepstack
CHART ?= deploy/charts/keepstack
DEV_VALUES ?= deploy/values/dev.yaml
VERIFY_JOB ?= keepstack-keepstack-verify-schema
ROOT_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))

.PHONY: help d dev-up dev-down build push helm-dev logs seed dash-grafana smoke smoke-v02 digest-once backup-now \
        resurfacer-now verify-obs verify-alerts restore-drill rollout-observe smoke-v03 verify-schema test

help:
	@grep -E '^[a-zA-Z0-9_-]+:([^=]|$$)' $(MAKEFILE_LIST) | cut -d':' -f1 | sort | uniq

d: dev-up

dev-up:
	k3d cluster create --config deploy/k3d/cluster.yaml
	kubectl cluster-info
	kubectl wait --for=condition=Ready node/k3d-keepstack-server-0 --timeout=120s
	kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/cloud/deploy.yaml
	kubectl wait --namespace ingress-nginx --for=condition=Ready pods --selector=app.kubernetes.io/component=controller --timeout=180s || { \
		kubectl -n ingress-nginx get pods || true; \
		kubectl -n ingress-nginx describe pods -l app.kubernetes.io/component=controller || true; \
		kubectl -n ingress-nginx logs deploy/ingress-nginx-controller || true; \
		exit 1; \
	}

dev-down:
	k3d cluster delete keepstack

build:
	docker buildx build --platform linux/amd64 \
		--tag $(REGISTRY)/keepstack-api:$(TAG) \
		-f apps/api/Dockerfile .
	docker buildx build --platform linux/amd64 \
		--tag $(REGISTRY)/keepstack-worker:$(TAG) \
		-f apps/worker/Dockerfile .
	docker buildx build --platform linux/amd64 \
		--tag $(REGISTRY)/keepstack-web:$(TAG) \
		-f apps/web/Dockerfile .

push:
	docker push $(REGISTRY)/keepstack-api:$(TAG)
	docker push $(REGISTRY)/keepstack-worker:$(TAG)
	docker push $(REGISTRY)/keepstack-web:$(TAG)

helm-dev:
	set -euo pipefail; \
	release="$(RELEASE)"; \
	namespace="$(NAMESPACE)"; \
	chart="$(CHART)"; \
	release_prefix="$${release}-keepstack"; \
	migrate_job="$${release_prefix}-migrate"; \
	verify_job="$${release_prefix}-verify-schema"; \
	if ! helm upgrade --install "$${release}" "$${chart}" -n "$${namespace}" --create-namespace -f $(DEV_VALUES) --set image.registry=$(REGISTRY) --set image.tag=$(TAG) --wait --timeout 10m --debug; then \
		status=$$?; \
		echo "Helm upgrade failed. Collecting diagnostics..."; \
		kubectl -n "$${namespace}" get pods || true; \
		kubectl -n "$${namespace}" get jobs || true; \
		for job in "$${migrate_job}" "$${verify_job}"; do \
			kubectl -n "$${namespace}" describe job "$${job}" || true; \
			kubectl -n "$${namespace}" logs job/"$${job}" || true; \
		done; \
		kubectl -n "$${namespace}" get events --sort-by=.metadata.creationTimestamp | tail -n 20 || true; \
		exit "$${status}"; \
	fi

logs:
	kubectl -n $(NAMESPACE) logs deploy/keepstack-api -f

seed:
	curl -fsS -X POST "http://keepstack.localtest.me:8080/api/links" \
		-H 'Content-Type: application/json' \
		-d '{"url":"https://example.com","title":"Example Domain"}'

dash-grafana:
	kubectl -n monitoring port-forward svc/grafana 3000:80

smoke:
	$(ROOT_DIR)scripts/smoke.sh

smoke-v02:
	if [[ -n "$${DIGEST_TEST:-}" && -z "$${SMTP_URL:-}" ]]; then export SMTP_URL=log://; fi; \
	$(ROOT_DIR)scripts/smoke-v02.sh

digest-once:
	kubectl -n $(NAMESPACE) create job digest-once-$$(date +%s) --from=cronjob/keepstack-digest

backup-now:
	$(ROOT_DIR)scripts/backup-now.sh

resurfacer-now:
	kubectl -n $(NAMESPACE) create job keepstack-resurfacer-now-$$(date +%s) --from=cronjob/keepstack-resurfacer

verify-obs:
	$(ROOT_DIR)scripts/verify-obs.sh

verify-alerts:
	$(ROOT_DIR)scripts/verify-alerts.sh

restore-drill:
	$(ROOT_DIR)scripts/restore-drill.sh

rollout-observe:
	$(ROOT_DIR)scripts/rollout-observe.sh

smoke-v03:
	$(ROOT_DIR)scripts/smoke-v03.sh

verify-schema:
	kubectl -n $(NAMESPACE) delete job $(VERIFY_JOB) --ignore-not-found
	set -euo pipefail; \
	helm template keepstack $(CHART) -n $(NAMESPACE) --set image.registry=$(REGISTRY) --set image.tag=$(TAG) --show-only templates/job-verify-schema.yaml | kubectl -n $(NAMESPACE) apply -f -
	kubectl -n $(NAMESPACE) wait --for=condition=Complete job/$(VERIFY_JOB) --timeout=120s || (kubectl -n $(NAMESPACE) logs job/$(VERIFY_JOB) && exit 1)
	kubectl -n $(NAMESPACE) logs job/$(VERIFY_JOB)

test:
	(cd apps/api && go test ./...)
	(cd apps/worker && go test ./...)
	(cd apps/web && npm run build)

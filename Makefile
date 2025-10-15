SHELL := /bin/bash

REGISTRY ?= ghcr.io/Paintersrp
REGISTRY_SANITIZED := $(shell echo $(REGISTRY) | tr '[:upper:]' '[:lower:]')
TAG ?= sha-$(shell git rev-parse --short HEAD)
RELEASE ?= keepstack
NAMESPACE ?= keepstack
CHART ?= deploy/charts/keepstack
DEV_VALUES ?= deploy/values/dev.yaml
VERIFY_JOB ?= keepstack-keepstack-verify-schema
ROOT_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
CLUSTER ?= keepstack

.PHONY: help d dev-up dev-down build push helm-dev logs seed bootstrap-dev dash-grafana smoke smoke-v02 digest-once backup-now \
        resurfacer-now verify-obs verify-alerts restore-drill rollout-observe smoke-v03 verify-schema test build-local

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
		--tag $(REGISTRY_SANITIZED)/keepstack-api:$(TAG) \
		-f apps/api/Dockerfile .
	docker buildx build --platform linux/amd64 \
		--tag $(REGISTRY_SANITIZED)/keepstack-worker:$(TAG) \
		-f apps/worker/Dockerfile .
	docker buildx build --platform linux/amd64 \
		--tag $(REGISTRY_SANITIZED)/keepstack-web:$(TAG) \
		-f apps/web/Dockerfile .

build-local:
	docker buildx build --platform linux/amd64 --load \
		--tag $(REGISTRY_SANITIZED)/keepstack-api:$(TAG) \
		-f apps/api/Dockerfile .
	docker buildx build --platform linux/amd64 --load \
		--tag $(REGISTRY_SANITIZED)/keepstack-worker:$(TAG) \
		-f apps/worker/Dockerfile .
	docker buildx build --platform linux/amd64 --load \
		--tag $(REGISTRY_SANITIZED)/keepstack-web:$(TAG) \
		-f apps/web/Dockerfile .
	k3d image import \
		$(REGISTRY_SANITIZED)/keepstack-api:$(TAG) \
		$(REGISTRY_SANITIZED)/keepstack-worker:$(TAG) \
		$(REGISTRY_SANITIZED)/keepstack-web:$(TAG) \
		--cluster $(CLUSTER)

push:
	docker push $(REGISTRY_SANITIZED)/keepstack-api:$(TAG)
	docker push $(REGISTRY_SANITIZED)/keepstack-worker:$(TAG)
	docker push $(REGISTRY_SANITIZED)/keepstack-web:$(TAG)

helm-dev:
	set -euo pipefail; \
	release="$(RELEASE)"; \
	namespace="$(NAMESPACE)"; \
	chart="$(CHART)"; \
	release_prefix="$${release}-keepstack"; \
	migrate_job="$${release_prefix}-migrate"; \
	verify_job="$${release_prefix}-verify-schema"; \
	collect_diagnostics() { \
	        echo "Collecting diagnostics..."; \
	        kubectl -n "$${namespace}" get pods || true; \
	        kubectl -n "$${namespace}" get jobs || true; \
                for job in "$${migrate_job}" "$${verify_job}"; do \
                        kubectl -n "$${namespace}" describe job "$${job}" || true; \
                        kubectl -n "$${namespace}" logs job/"$${job}" || true; \
                        pods="$$(kubectl -n "$${namespace}" get pods -l job-name="$${job}" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' || true)"; \
                        for pod in $$pods; do \
                                containers="$$(kubectl -n "$${namespace}" get pod "$$pod" -o jsonpath='{range .spec.initContainers[*]}{.name}{"\n"}{end}{range .spec.containers[*]}{.name}{"\n"}{end}' || true)"; \
                                for container in $$containers; do \
                                        echo "--- Logs for job $$job pod $$pod container $$container ---"; \
                                        kubectl -n "$${namespace}" logs "$$pod" -c "$$container" || true; \
                                done; \
                        done; \
                done; \
                kubectl -n "$${namespace}" get events --sort-by=.metadata.creationTimestamp | tail -n 20 || true; \
        }; \
	wait_flag="--wait"; \
	release_exists="true"; \
	if ! helm status "$${release}" -n "$${namespace}" >/dev/null 2>&1; then \
	        release_exists="false"; \
	        wait_flag=""; \
	fi; \
	if ! helm upgrade --install "$${release}" "$${chart}" -n "$${namespace}" --create-namespace -f $(DEV_VALUES) --set image.registry=$(REGISTRY_SANITIZED) --set image.tag=$(TAG) $${wait_flag} --timeout 10m --debug; then \
	        status=$$?; \
	        echo "Helm upgrade failed."; \
	        collect_diagnostics; \
	        exit "$${status}"; \
	fi; \
	if [[ "$${release_exists}" == "false" ]]; then \
	        echo "Helm release installed without --wait; waiting for core workloads to become ready..."; \
	        wait_targets=( \
	                "statefulset/$${release_prefix}-postgres" \
	                "statefulset/$${release_prefix}-nats" \
	                "deployment/$${release_prefix}-api" \
	                "deployment/$${release_prefix}-worker" \
	                "deployment/$${release_prefix}-web" \
	        ); \
	        for target in "$${wait_targets[@]}"; do \
	                echo "Waiting for $$target..."; \
	                if ! kubectl -n "$${namespace}" rollout status "$${target}" --timeout=10m; then \
	                        echo "Rollout for $$target failed."; \
	                        collect_diagnostics; \
	                        exit 1; \
	                fi; \
	        done; \
	fi

logs:
	kubectl -n $(NAMESPACE) logs deploy/keepstack-api -f

seed:
	curl -fsS -X POST "http://keepstack.localtest.me:8080/api/links" \
		-H 'Content-Type: application/json' \
		-d '{"url":"https://example.com","title":"Example Domain"}'



bootstrap-dev:
	@set -euo pipefail; \
		echo "==> Bootstrapping Keepstack dev environment"; \
		build_target="build"; \
		if $(MAKE) -n build-local >/dev/null 2>&1; then \
			build_target="build-local"; \
		fi; \
		need_push="true"; \
		if [[ "$$build_target" == "build-local" ]]; then \
			need_push="false"; \
		fi; \
		steps=("dev-up" "$$build_target"); \
		if [[ "$$need_push" == "true" ]]; then \
			steps+=("push"); \
		fi; \
		steps+=("helm-dev" "seed"); \
		for step in "$${steps[@]}"; do \
			echo ""; \
			echo "==> Running $$step"; \
			if ! $(MAKE) --no-print-directory "$$step"; then \
				status="$$?"; \
				echo "❌ $$step failed"; \
				exit "$$status"; \
			fi; \
			echo "✅ $$step completed"; \
		done; \
		echo ""; \
			echo "==> Dev environment bootstrap complete"; \
			python3 scripts/print_dev_summary.py || true
			echo ""

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
	helm template keepstack $(CHART) -n $(NAMESPACE) --set image.registry=$(REGISTRY_SANITIZED) --set image.tag=$(TAG) --show-only templates/job-verify-schema.yaml | kubectl -n $(NAMESPACE) apply -f -
	kubectl -n $(NAMESPACE) wait --for=condition=Complete job/$(VERIFY_JOB) --timeout=120s || (kubectl -n $(NAMESPACE) logs job/$(VERIFY_JOB) && exit 1)
	kubectl -n $(NAMESPACE) logs job/$(VERIFY_JOB)

test:
	(cd apps/api && go test ./...)
	(cd apps/worker && go test ./...)
	(cd apps/web && npm run build)

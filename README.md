# Keepstack

Keepstack is a self-hosted reading queue and web archiver designed for homelab and small team deployments. Drop a link into the API or web UI and Keepstack fetches the page, extracts the readable content, persists it in Postgres, and makes it instantly searchable. The worker pipeline is built to be observable and resilient so you always know which pages have been processed.

Built with Go, React, Postgres, NATS, and Kubernetes, Keepstack v0.1 focuses on delivering an end-to-end slice that is easy to run locally or on lightweight clusters such as k3d. Future releases will layer in richer user management, browser automation, and deeper archive controls.

## Repository layout

```
keepstack/
├─ apps/
│  ├─ api/        # Echo API exposing link CRUD + health and metrics endpoints
│  ├─ worker/     # NATS consumer that fetches, parses, and persists archives
│  └─ web/        # Vite/React frontend with TanStack Router + Query
├─ db/            # goose migrations and sqlc configuration
├─ deploy/        # Helm chart, k3d cluster spec, environment values
├─ infra/         # Placeholder for future monitoring additions
├─ .github/       # GitHub Actions CI pipeline
└─ justfile       # Helper commands for local/dev automation
```

## Quickstart

1. **Prerequisites**
   - Docker with Buildx, k3d, kubectl, Helm, and Just installed
   - Access to a container registry (defaults to GHCR)

2. **Bootstrap a dev cluster and install ingress-nginx**

   ```sh
   just dev-up
   kubectl create ns keepstack || true

   kubectl -n keepstack create secret generic keepstack-secrets \
     --from-literal=DATABASE_URL='postgres://keepstack:keepstack@postgres:5432/keepstack?sslmode=disable' \
     --from-literal=NATS_URL='nats://nats:4222' \
     --from-literal=JWT_SECRET='devdevdevdevdevdevdevdevdevdevdevdev' || true
   ```

3. **Build and push images** (override `REGISTRY` if you own another registry)

   ```sh
   just build
   just push
   ```

4. **Deploy via Helm**

   ```sh
   just helm-dev
   ```

5. **Verify pods and tail API logs**

   ```sh
   kubectl -n keepstack get pods
   just logs
   ```

6. **Seed sample data and run a smoke test**

   ```sh
   just seed
   just smoke
   ```

7. **Open the app**

   ```sh
   echo "Open: http://keepstack.localtest.me:8080"
   ```

### Smoke test expectations

`just smoke` issues a `GET /api/links?q=example` request against the ingress host. The command succeeds when the response contains at least one link, ensuring the API, worker, Postgres, and ingress are wired together correctly.

## Verify v0.1

Run the workflow below to exercise the full v0.1 deployment path from cluster bootstrap through smoke testing. It bootstraps a local k3d cluster with ingress-nginx, provisions the shared application secret, deploys the Helm chart, waits for the core workloads to become available, and finally executes the smoke test against the ingress endpoint.

```sh
just dev-up
kubectl create ns keepstack || true
kubectl -n keepstack create secret generic keepstack-secrets \
  --from-literal=DATABASE_URL='postgres://keepstack:keepstack@postgres:5432/keepstack?sslmode=disable' \
  --from-literal=NATS_URL='nats://nats:4222' \
  --from-literal=JWT_SECRET='devdevdevdevdevdevdevdevdevdevdevdev' || true
just build
just push
just helm-dev
kubectl -n keepstack wait --for=condition=Available deploy/keepstack-api --timeout=120s
kubectl -n keepstack wait --for=condition=Available deploy/keepstack-worker --timeout=120s
kubectl -n keepstack wait --for=condition=Available deploy/keepstack-web --timeout=120s
just smoke
```

The wait commands confirm that each deployment reports an `Available` status before the smoke test runs. If any wait operation times out, inspect the relevant pod logs (for example, `kubectl -n keepstack logs deploy/keepstack-api`) before re-running the workflow.

### Autoscaling policy

The API deployment includes a Horizontal Pod Autoscaler that keeps at least two replicas running and can scale up to six based on 70% CPU utilization. Override `api.autoscaling.minReplicas` or `api.autoscaling.maxReplicas` in your Helm values to adjust the range for your environment.

## Developer workflow

- **Local testing**: `just test` (runs API and worker Go tests plus the web production build).
- **Image builds**: `just build` creates linux/amd64 images tagged with `sha-<short commit>`.
- **CI**: GitHub Actions runs Go tests, web builds, Docker image pushes to GHCR, and `helm lint` on every PR and main push.

### Smoke test script usage & troubleshooting

- **Basic usage**: Run `./scripts/smoke.sh` (or `just smoke`) once the Helm release is ready. Override defaults such as `SMOKE_BASE_URL`, `SMOKE_POST_TIMEOUT`, or `SMOKE_POLL_TIMEOUT` to target alternative ingress URLs or tune slow environments.
- **Ingress routing failures**: If the script reports connection or DNS errors, confirm the ingress controller is ready with `kubectl -n ingress-nginx get pods` and that `/etc/hosts` (or your DNS) resolves `keepstack.localtest.me`.
- **Pending database migrations**: A `201` POST followed by repeated polling without the link appearing usually indicates the worker cannot finish migrations. Check the Postgres pod logs (`kubectl -n keepstack logs statefulset/keepstack-postgres`) and re-run `helm-dev` after resolving schema issues.
- **API readiness**: HTTP `5xx` responses or cURL timeouts imply the API deployment is still starting. Verify readiness with `kubectl -n keepstack get deploy keepstack-api` and inspect logs via `just logs`.
- **Tear down**: Clean up the development environment with `just dev-down` after smoke testing to delete the k3d cluster.

## v0.1 Scope & Definition of Done

- ✅ Goose migration covering users, links, archives, tags, and search triggers
- ✅ sqlc-generated data access layer for the API
- ✅ API exposes `/healthz`, `/livez`, `/metrics`, `POST /api/links`, `GET /api/links`
- ✅ Worker consumes `keepstack.links.saved`, fetches content, parses, persists archive data, and updates FTS
- ✅ React web UI supports listing, searching, and adding links
- ✅ Helm chart deploys API, worker, web, Postgres, NATS, and Chrome (placeholder) with ingress routing
- ✅ Justfile automates cluster lifecycle, builds, deploys, and smoke testing
- ✅ CI builds/tests all components, publishes images to GHCR, and runs Helm lint
- ✅ Metrics exposed on API and worker pods for scraping

## Roadmap (beyond v0.1)

- Authenticated multi-user support with proper session handling
- Scheduled re-ingest and bookmark tagging UX
- Richer observability (Grafana dashboards, alerting)
- Optional external object storage for large archives

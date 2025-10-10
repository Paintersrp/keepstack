# Keepstack

Keepstack is a self-hosted reading queue and web archiver designed for homelab and small team deployments. Drop a link into the API or web UI and Keepstack fetches the page, extracts the readable content, persists it in Postgres, and makes it instantly searchable. The worker pipeline is built to be observable and resilient so you always know which pages have been processed.

Built with Go, React, Postgres, NATS, and Kubernetes, Keepstack v0.1 focuses on delivering an end-to-end slice that is easy to run locally or on lightweight clusters such as k3d. Future releases will layer in richer user management, browser automation, and deeper archive controls.

## v0.2 Features

<table>
  <tr>
    <td width="50%">

![Tag chips mockup](docs/assets/v0.2-tags.svg)

### Link tagging

Organize long-running reading queues with reusable tag chips. Tags can be created on the fly, re-applied to any archive, and queried from the API or web UI to generate topic-specific filters.

    </td>
    <td width="50%">

![Highlights mockup](docs/assets/v0.2-highlights.svg)

### Highlights & annotations

Capture notable excerpts, jot down context for teammates, and surface highlights alongside search results to accelerate future research.

    </td>
  </tr>
  <tr>
    <td width="50%">

![Parser pipeline illustration](docs/assets/v0.2-parser.svg)

### Improved parsing

The worker normalizes noisy HTML, strips boilerplate, and stores structured metadata so full-text search returns cleaner, more relevant matches.

    </td>
    <td width="50%">

![Digest workflow timeline](docs/assets/v0.2-digest.svg)

### Digest workflow

Schedule automated recap emails summarizing unread items. The CronJob renders a templated digest and hands it off to your SMTP provider so teams stay current without visiting the dashboard.

    </td>
  </tr>
</table>

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
     --from-literal=JWT_SECRET='devdevdevdevdevdevdevdevdevdevdevdev' \
     --from-literal=SMTP_HOST='smtp.keepstack.local' \
     --from-literal=SMTP_PORT='587' \
     --from-literal=SMTP_USERNAME='keepstack' \
     --from-literal=SMTP_PASSWORD='changeme' \
     --from-literal=DIGEST_SENDER='Keepstack Digest <digest@keepstack.local>' \
     --from-literal=DIGEST_RECIPIENT='reader@keepstack.local' \
   --from-literal=DIGEST_LIMIT='10' || true
 ```

   The `deploy/values/dev.yaml` file enables a scheduled digest CronJob. Adjust
   `digest.schedule`, `digest.limit`, `digest.sender`, and `digest.recipient`
   to control when emails are sent, how many unread links they include, and
   where they are delivered.

### Enabling the scheduled digest

The digest CronJob is disabled in the chart defaults so production clusters can
opt in explicitly. Provide SMTP credentials via `keepstack-secrets` and enable
the job with Helm overrides:

```sh
helm upgrade --install keepstack deploy/charts/keepstack \
  --namespace keepstack \
  --values deploy/values/dev.yaml \
  --set digest.enabled=true \
  --set digest.schedule="0 13 * * 1-5" \
  --set digest.limit=15 \
  --set digest.sender="Keepstack Digest <digest@example.com>" \
  --set digest.recipient="team@example.com"
```

Override `digest.schedule` to change when the CronJob fires, update
`digest.limit` to cap the number of unread links, and set sender/recipient
addresses that match your SMTP provider. The CronJob uses the shared secrets
(`DIGEST_SENDER`, `DIGEST_RECIPIENT`, `DIGEST_LIMIT`) if you prefer to keep
values out of Helm overrides.

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

`just smoke-v02` executes the v0.2 workflow end-to-end: it creates a link, waits for the worker to archive it, exercises tag replacement semantics (including multi-tag AND filtering), and verifies that highlights capture both text and note content. The run passes when the tagged link and highlight appear in API results, confirming the API, worker, Postgres, and ingress are all wired together.

## Verify v0.1

Run the workflow below to exercise the full v0.1 deployment path from cluster bootstrap through smoke testing. It bootstraps a local k3d cluster with ingress-nginx, provisions the shared application secret, deploys the Helm chart, waits for the core workloads to become available, and finally executes the smoke test against the ingress endpoint.

```sh
just dev-up
kubectl create ns keepstack || true
kubectl -n keepstack create secret generic keepstack-secrets \
  --from-literal=DATABASE_URL='postgres://keepstack:keepstack@postgres:5432/keepstack?sslmode=disable' \
  --from-literal=NATS_URL='nats://nats:4222' \
  --from-literal=JWT_SECRET='devdevdevdevdevdevdevdevdevdevdevdev' \
  --from-literal=SMTP_HOST='smtp.keepstack.local' \
  --from-literal=SMTP_PORT='587' \
  --from-literal=SMTP_USERNAME='keepstack' \
  --from-literal=SMTP_PASSWORD='changeme' \
  --from-literal=DIGEST_SENDER='Keepstack Digest <digest@keepstack.local>' \
  --from-literal=DIGEST_RECIPIENT='reader@keepstack.local' \
  --from-literal=DIGEST_LIMIT='10' || true
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

- **Basic usage**: Run `./scripts/smoke-v02.sh` (or `just smoke-v02`) once the Helm release is ready. Override defaults such as `SMOKE_BASE_URL`, `SMOKE_POST_TIMEOUT`, or `SMOKE_POLL_TIMEOUT` to target alternative ingress URLs or tune slow environments.
- **Digest dry-run**: Export `DIGEST_TEST=1` to trigger the optional digest preview step. When set, `just smoke-v02` defaults `SMTP_TRANSPORT=log` so the API logs the rendered email instead of attempting an SMTP delivery.
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

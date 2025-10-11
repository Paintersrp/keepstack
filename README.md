# Keepstack

Keepstack is a self-hosted reading queue and web archiver designed for homelab and small team deployments. Drop a link into the API or web UI and Keepstack fetches the page, extracts the readable content, persists it in Postgres, and makes it instantly searchable. The worker pipeline is built to be observable and resilient so you always know which pages have been processed.

Built with Go, React, Postgres, NATS, and Kubernetes, Keepstack v0.2 layers tagging, highlighting, and digest automation onto the original end-to-end slice so releases stay focused on a polished, deployable experience. Future releases will continue building richer user management, browser automation, and deeper archive controls.

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
     --from-literal=SMTP_URL='smtp://keepstack:changeme@smtp.keepstack.local:587' \
     --from-literal=DIGEST_SENDER='Keepstack Digest <digest@keepstack.local>' \
     --from-literal=DIGEST_RECIPIENT='reader@keepstack.local' \
     --from-literal=DIGEST_LIMIT='10' || true
```

   The `deploy/values/dev.yaml` file enables a scheduled digest CronJob. Adjust
   `digest.schedule`, `digest.limit`, `digest.sender`, and `digest.recipient`
   to control when emails are sent, how many unread links they include, and
   where they are delivered.

   `SMTP_URL` accepts standard SMTP connection strings such as
   `smtp://username:password@smtp.keepstack.local:587`. For local testing, use
   `log://` to write a base64-encoded payload to the API logs instead of
   delivering mail. The fallback prints `mail.digest` log entries that include
   the rendered subject line and a base64 body so you can confirm the template
   output by running `just logs` without a live SMTP relay.

### Enabling the scheduled digest

The digest CronJob is disabled in the chart defaults so production clusters can
opt in explicitly. Provide an SMTP URL via `keepstack-secrets` and enable
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

Kubernetes CronJobs interpret schedules in the cluster's timezone (UTC on most
managed offerings). Adjust `digest.schedule` accordingly if you expect digests
to land in a specific local time window.

### Observability, dashboards, and alerts

Keepstack v0.3 introduces first-class Prometheus metrics and a lightweight
Grafana dashboard to keep an eye on request volume, latency, worker throughput,
and queue health. Enable the stack by setting `observability.enabled=true` and
deploying kube-prometheus-stack alongside the chart:

```sh
helm upgrade --install kube-prom-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace --wait
helm upgrade --install keepstack deploy/charts/keepstack \
  --namespace keepstack --create-namespace -f deploy/values/dev.yaml --wait
```

Forward Grafana locally with `just dash-grafana` (defaults to
`http://localhost:3000`, admin/admin) and open the **Keepstack Overview**
dashboard. Out of the box it charts:

* API request rate, error percentage, and p50/p95 latency
* Worker job throughput, parse duration, queue lag, and success rate

PrometheusRule resources fire two warning alerts when the API 5xx rate exceeds
the configured threshold or when worker job failures spike. Adjust thresholds
under `observability.alerts` in `values.yaml`.

If your Prometheus Operator release uses a different Helm release name, set
`observability.prometheusRelease` to match so the ServiceMonitor and
PrometheusRule resources are picked up automatically.

### Backups, restore drills, and S3 offload

Nightly `pg_dump` backups run via the `keepstack-backup` CronJob whenever
`backup.enabled` is true. By default they land on a dedicated PVC
(`keepstack-backups`) with automatic retention. Trigger an on-demand snapshot
with:

```sh
just backup-now
kubectl -n keepstack get jobs | grep keepstack-backup-now
```

To exercise the disaster-recovery drill, run `just restore-drill` and follow the
annotated steps: uninstall the release, reinstall Postgres only, execute the
example restore job, then bring the deployments back online. Step five now
renders the restore Job with Helm to ensure values overrides are honored before
applying it:

```sh
helm template keepstack deploy/charts/keepstack -n "${NAMESPACE}" -f "${VALUES}" \
  --show-only templates/job-restore-example.yaml | kubectl -n "${NAMESPACE}" apply -f -
```

The reusable `scripts/restore-db.sh` helper accepts an explicit dump path or automatically
selects the most recent archive. S3/minio uploads are also supported—configure
`backup.storage.kind=s3` along with the bucket, endpoint, and access key
secrets, and the CronJob will stream the compressed dump directly to object
storage.

### Suggested resurfacing

The new resurfacer CronJob scores unread links nightly and persists the top
entries per user into a lightweight `recommendations` table. Enable it with
`resurfacer.enabled=true`, or trigger a manual run with `just resurfacer-now`.
The API exposes the curated list at
`GET /api/recommendations?limit=20`, and the React dashboard now includes a
**Suggested picks** filter that surfaces long-unread favorites without touching
your saved searches.

### Optional TLS issuers

Set `tls.enabled=true` to annotate the ingress for cert-manager. A
self-signed ClusterIssuer ships by default for development clusters, while
`tls.issuer=letsencrypt` (or `letsencrypt-staging`) provisions ACME HTTP-01
certificates using the configured contact email. Override the issuer without
touching the ingress manifest.

## Verification checklist

1. `cd apps/api && go test ./...` – API unit tests and new resurfacer logic
2. `cd apps/worker && go test ./...` – ingestion pipeline and queue metrics
3. `cd apps/web && npm run build` – ensure the updated Suggested filter compiles
4. `helm upgrade --install keepstack deploy/charts/keepstack -n keepstack -f deploy/values/dev.yaml --wait`
5. `just smoke-v02` – populate the system end-to-end
6. `just dash-grafana` – inspect the Keepstack Overview dashboard
7. `just backup-now` and `just restore-drill` – validate database backup + restore workflow
8. `just resurfacer-now` followed by `curl .../api/recommendations` – verify resurfaced items

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

   The Helm chart configures graceful shutdown delays for the API and worker
   Deployments. Adjust `api.shutdownDelaySeconds`,
   `api.terminationGracePeriodSeconds`, `worker.shutdownDelaySeconds`, and
   `worker.terminationGracePeriodSeconds` in `deploy/charts/keepstack/values.yaml`
   (or your override files) if your cluster needs more time to drain requests
   before a rollout.

6. **Seed sample data and run a smoke test**

   ```sh
   just seed
   just smoke-v02
   ```

7. **Open the app**

   ```sh
   echo "Open: http://keepstack.localtest.me:8080"
   ```

### Smoke test expectations

`just smoke-v02` executes the v0.2 workflow end-to-end: it creates a link, waits for the worker to archive it, exercises tag replacement semantics (including multi-tag AND filtering), and verifies that highlights capture both text and note content. Highlights inherit their source offsets so re-parsed articles can reflow while annotations still render in the original context, and tag updates are idempotent—reposting a tag set overwrites the previous values rather than appending duplicates. The run passes when the tagged link and highlight appear in API results, confirming the API, worker, Postgres, and ingress are all wired together.

## Verify v0.2

Follow the workflow below to exercise the full v0.2 deployment path from cluster bootstrap through smoke testing. It bootstraps a local k3d cluster with ingress-nginx, provisions the shared application secret, deploys the Helm chart, waits for the core workloads to become available, and finally executes both the schema check and smoke test against the ingress endpoint.

```sh
just dev-up
kubectl create ns keepstack || true
kubectl -n keepstack create secret generic keepstack-secrets \
  --from-literal=DATABASE_URL='postgres://keepstack:keepstack@postgres:5432/keepstack?sslmode=disable' \
  --from-literal=NATS_URL='nats://nats:4222' \
  --from-literal=JWT_SECRET='devdevdevdevdevdevdevdevdevdevdevdev' \
  --from-literal=SMTP_URL='smtp://keepstack:changeme@smtp.keepstack.local:587' \
  --from-literal=DIGEST_SENDER='Keepstack Digest <digest@keepstack.local>' \
  --from-literal=DIGEST_RECIPIENT='reader@keepstack.local' \
  --from-literal=DIGEST_LIMIT='10' || true
just build
just push
just helm-dev
kubectl -n keepstack wait --for=condition=Available deploy/keepstack-api --timeout=120s
kubectl -n keepstack wait --for=condition=Available deploy/keepstack-worker --timeout=120s
kubectl -n keepstack wait --for=condition=Available deploy/keepstack-web --timeout=120s
just verify-schema
just smoke-v02
DIGEST_TEST=1 just smoke-v02
kubectl -n keepstack describe netpol keepstack-allow-api-to-nats
```

The wait commands confirm that each deployment reports an `Available` status before the smoke test runs. If any wait operation times out, inspect the relevant pod logs (for example, `kubectl -n keepstack logs deploy/keepstack-api`) before re-running the workflow.

`just verify-schema` renders a one-off Kubernetes Job that runs `keepstack cron verify-schema`, ensuring the Postgres schema includes the metadata columns, highlights table, and search triggers expected by the v0.2 release. The job exits non-zero when required objects are missing so pending migrations are surfaced before smoke testing.

`just smoke-v02` drives the API/worker/web flow while validating tag replacement, highlight persistence, and digest rendering. Setting `DIGEST_TEST=1` exercises the digest preview mode so the API emits a `log://` fallback email without attempting SMTP delivery.

`kubectl describe netpol keepstack-allow-api-to-nats` surfaces NetworkPolicy status and matching pod selectors. If the smoke test reports `nats:4222` dial errors or `timeout waiting on ack` messages, confirm the policy is present and that both the API and NATS pods carry the expected labels.

### Autoscaling policy

The API deployment includes a Horizontal Pod Autoscaler that keeps at least two replicas running and can scale up to six based on 70% CPU utilization. Override `api.autoscaling.minReplicas` or `api.autoscaling.maxReplicas` in your Helm values to adjust the range for your environment. The worker deployment also ships with a Horizontal Pod Autoscaler that keeps between one and four replicas at the same CPU target. Disable it with `worker.autoscaling.enabled=false` or tweak the bounds through `worker.autoscaling.minReplicas` and `worker.autoscaling.maxReplicas`.

### Observability integrations

Set `observability.enabled=true` in your Helm values to render ServiceMonitors, Prometheus alert rules, and the bundled Grafana dashboard. The chart ships alert thresholds for elevated API error rates and repeated worker ingestion failures; tune them through the `observability.alerts.*` subtree. When running alongside [`kube-prometheus-stack`](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack), make sure the Grafana admin credentials and service account align with your installation by overriding `observability.grafana.*`.

To spin up a compatible monitoring stack locally:

```
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm upgrade --install monitoring prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace \
  --set grafana.adminUser=admin --set grafana.adminPassword=admin
```

With the stack running, install Keepstack using `deploy/values/dev.yaml` (observability is enabled by default) or override `observability.enabled=true` in your own values file. Grafana will automatically pick up the `keepstack-overview` dashboard via the ConfigMap generated by the chart.

## Developer workflow

- **Local testing**: `just test` (runs API and worker Go tests plus the web production build).
- **Image builds**: `just build` creates linux/amd64 images tagged with `sha-<short commit>`.
- **CI**: GitHub Actions runs Go tests, web builds, Docker image pushes to GHCR, and `helm lint` on every PR and main push.

### Smoke test script usage & troubleshooting

- **Basic usage**: Run `./scripts/smoke-v02.sh` (or `just smoke-v02`) once the Helm release is ready. Override defaults such as `SMOKE_BASE_URL`, `SMOKE_POST_TIMEOUT`, or `SMOKE_POLL_TIMEOUT` to target alternative ingress URLs or tune slow environments.
- **Digest dry-run**: Export `DIGEST_TEST=1` to trigger the optional digest preview step. When set, `just smoke-v02` defaults `SMTP_URL=log://` so the API logs the rendered email instead of attempting an SMTP delivery.
- **Ingress routing failures**: If the script reports connection or DNS errors, confirm the ingress controller is ready with `kubectl -n ingress-nginx get pods` and that `/etc/hosts` (or your DNS) resolves `keepstack.localtest.me`.
- **Pending database migrations**: A `201` POST followed by repeated polling without the link appearing usually indicates the worker cannot finish migrations. Check the Postgres pod logs (`kubectl -n keepstack logs statefulset/keepstack-postgres`) and re-run `helm-dev` after resolving schema issues.
- **API readiness**: HTTP `5xx` responses or cURL timeouts imply the API deployment is still starting. Readiness now verifies the
  archives metadata columns and highlights table exist; failures surface hints about pending migrations alongside Prometheus metrics.
  Verify deployment health with `kubectl -n keepstack get deploy keepstack-api` and inspect logs via `just logs` to confirm database
  migrations ran successfully.
- **Link publish failures**: Persistent HTTP `5xx` errors or `timeout waiting on ack` messages when posting new links can indicate the API pods cannot reach NATS. Confirm the `keepstack-allow-api-to-nats` NetworkPolicy is installed, that its podSelectors match the API and NATS labels via `kubectl -n keepstack describe netpol keepstack-allow-api-to-nats`, and that the NATS StatefulSet is healthy with `kubectl -n keepstack get statefulset keepstack-nats`.
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

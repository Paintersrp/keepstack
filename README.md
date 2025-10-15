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
└─ Makefile       # Helper commands for local/dev automation
```

## Quickstart

1. **Prerequisites**
   - Review the [comprehensive tooling guide](docs/prerequisites.md) to install Docker with Buildx, k3d, kubectl, Helm, and GNU Make
   - Ensure you can authenticate to a container registry (defaults to GHCR)

2. **Build images for your workflow**
   - Run `make build && make push` when you plan to publish the images to your registry (the chart pulls from `$(REGISTRY)`)
   - Run `make build-local` when you just need the images inside the local k3d cluster. The target builds with `--load` and calls `k3d image import` so `make helm-dev` can deploy without reaching GHCR.

3. **Bootstrap a dev cluster and install ingress-nginx**

   ```sh
   make dev-up
   kubectl create ns keepstack || true
```

   The Helm chart manages the `keepstack-secrets` Secret when
   `secrets.create=true` (the default in `deploy/values/dev.yaml`). Update that
   file to change the bootstrap credentials instead of creating the Secret by
   hand. If you previously created the Secret manually, delete it with
   `kubectl -n keepstack delete secret keepstack-secrets` before running
   `make helm-dev` so Helm can recreate it with the proper ownership metadata.

   Both the migration and schema verification jobs include optional
   `pg_isready` init containers that wait for Postgres connections before the
   main containers start. Tune the behavior via Helm values:

   * `migrate.waitForDatabase` and `verifySchema.waitForDatabase` control
     whether the wait container runs, how frequently it polls, and when to time
     out.
   * `verifySchema.backoffLimit` sets how many times Kubernetes retries the
     verification Job when the check fails (defaults to `0`).

   Disable the wait containers with
   `--set migrate.waitForDatabase.enabled=false --set verifySchema.waitForDatabase.enabled=false`
   if you are pointing `DATABASE_URL` at an always-on, externally managed
   database.

   The `deploy/values/dev.yaml` file enables a scheduled digest CronJob. Adjust
   `digest.schedule`, `digest.limit`, `digest.sender`, and `digest.recipient`
   to control when emails are sent, how many unread links they include, and
   where they are delivered.

   `SMTP_URL` accepts standard SMTP connection strings such as
   `smtp://username:password@smtp.keepstack.local:587`. For local testing, use
   `log://` to write a base64-encoded payload to the API logs instead of
   delivering mail. The fallback prints `mail.digest` log entries that include
   the rendered subject line and a base64 body so you can confirm the template
   output by running `make logs` without a live SMTP relay.

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

Forward Grafana locally with `make dash-grafana` (defaults to
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
make backup-now
BACKUP_JOB=$(kubectl -n keepstack get jobs -l app.kubernetes.io/component=backup \
  --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}')
kubectl -n keepstack wait --for=condition=complete "job/${BACKUP_JOB}" --timeout=120s
```

To exercise the disaster-recovery drill, run `make restore-drill` and follow the
annotated steps: uninstall the release, reinstall Postgres only, execute the
example restore job, then bring the deployments back online. Step two now walks
through mounting the `keepstack-backups` PVC onto a helper pod so you can list
available dumps (the API Deployment does not expose `/backups`). Step five still
renders the restore Job with Helm to ensure values overrides are honored before
applying it:

```sh
cat <<'YAML' | kubectl -n keepstack apply -f -
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
kubectl -n keepstack wait --for=condition=Ready pod/backup-shell --timeout=60s
kubectl -n keepstack exec pod/backup-shell -- ls -1t /backups | head -n1
kubectl -n keepstack delete pod/backup-shell
```

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
`resurfacer.enabled=true`, or trigger a manual run with `make resurfacer-now`.
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

1. `cd apps/api && go test ./...` – API unit tests and resurfacer logic
2. `cd apps/worker && go test ./...` – ingestion pipeline and queue metrics
3. `cd apps/web && npm run build` – ensure the Suggested filter compiles
4. `helm upgrade --install keepstack deploy/charts/keepstack -n keepstack -f deploy/values/dev.yaml --wait`
5. `make verify-obs` – confirm ServiceMonitor wiring and metrics exposure
6. `make smoke-v03` – exercise the full v0.3 workflow, including resurfacer automation
7. `make verify-alerts` – provoke and clear API/worker alerts end-to-end
8. `make backup-now` and `DST_NS=restore-drill make restore-drill` – validate database backup + restore workflow

Override `digest.schedule` to change when the CronJob fires, update
`digest.limit` to cap the number of unread links, and set sender/recipient
addresses that match your SMTP provider. The CronJob uses the shared secrets
(`DIGEST_SENDER`, `DIGEST_RECIPIENT`, `DIGEST_LIMIT`) if you prefer to keep
values out of Helm overrides.

3. **Build and push images** (override `REGISTRY` if you own another registry)

   ```sh
   make build
   make push
   ```

4. **Deploy via Helm**

   ```sh
   make helm-dev
   ```

   The observability extras in `deploy/values/dev.yaml` assume the
   Prometheus Operator CRDs are installed. If your cluster does not include
   them yet, Helm will now skip the ServiceMonitor and PrometheusRule
   resources automatically; re-run `make helm-dev` after installing the CRDs
   (or temporarily set `observability.enabled=false`) to add the monitoring
   objects.

5. **Verify pods and tail API logs**

   ```sh
   kubectl -n keepstack get pods
   make logs
   ```

   The Helm chart configures graceful shutdown windows for the API and worker
   Deployments via `api.terminationGracePeriodSeconds` and
   `worker.terminationGracePeriodSeconds` in
   `deploy/charts/keepstack/values.yaml` (or your override files). Increase
   these values if your cluster needs more time to drain requests before a
   rollout.

6. **Seed sample data and run a smoke test**

   ```sh
   make seed
   make smoke-v03
   ```

7. **Open the app**

   ```sh
   echo "Open: http://keepstack.localtest.me:18080"
   ```

> 💡 Prefer to let Make orchestrate the workflow? Run `make bootstrap-dev` to
> sequentially create the k3d cluster, build (or `build-local` when available),
> push images only when required, deploy the Helm chart, and seed example data.
> When it completes it prints the ingress URL plus the bootstrap credentials
> sourced from `deploy/values/dev.yaml`. Follow up with `make smoke-v03` to run
> the comprehensive smoke test and `make verify-obs` if you want to confirm the
> observability stack is scraping metrics—both remain optional but are handy
> confidence checks before you start developing.

> ℹ️ The bootstrap path now defaults backups to an ephemeral `emptyDir` volume
> so the Helm release no longer blocks when a PersistentVolumeClaim fails to
> bind (a common hiccup on fresh k3d clusters). If you want durable backup
> storage, override `backup.storage.kind` (and related PVC settings) in your
> values file.

### Smoke test expectations

`make smoke-v03` drives the v0.3 verification workflow: it reuses the v0.2 link, tag, and highlight assertions, then checks Prometheus metrics exposure, triggers an on-demand resurfacer Job, and confirms recommendations are returned by the API. The run passes when the original smoke expectations succeed, ServiceMonitors respond, and the resurfacer emits at least one recommendation, proving the API, worker, observability stack, and nightly jobs are operating together.

## Verify v0.3

Follow the workflow below to exercise the full v0.3 deployment path, including observability, backups, resurfacing, and alert validation. Start from a clean workstation shell so the helper scripts can source their defaults.

```sh
make dev-up
kubectl create ns keepstack || true
```

If you prefer to manage the Secret outside of Helm, set
`secrets.create=false` in your values file and add the required ownership
metadata when creating it:

```sh
kubectl -n keepstack create secret generic keepstack-secrets \
  --from-literal=DATABASE_URL='postgres://keepstack:keepstack@postgres:5432/keepstack?sslmode=disable' \
  --from-literal=NATS_URL='nats://nats:4222' \
  --from-literal=JWT_SECRET='devdevdevdevdevdevdevdevdevdevdevdev' \
  --from-literal=SMTP_URL='smtp://keepstack:changeme@smtp.keepstack.local:587' \
  --from-literal=DIGEST_SENDER='Keepstack Digest <digest@keepstack.local>' \
  --from-literal=DIGEST_RECIPIENT='reader@keepstack.local' \
  --from-literal=DIGEST_LIMIT='10'
kubectl -n keepstack annotate --overwrite secret keepstack-secrets \
  meta.helm.sh/release-name=keepstack \
  meta.helm.sh/release-namespace=keepstack
kubectl -n keepstack label --overwrite secret keepstack-secrets \
  app.kubernetes.io/managed-by=Helm
```

Ensure your values file enables the v0.3 automation before building. The provided `deploy/values/dev.yaml` already sets:

```yaml
observability:
  enabled: true
backup:
  enabled: true
resurfacer:
  enabled: true
```

With the prerequisites in place, build, push, and deploy the chart:

```sh
make build
make push
make helm-dev
kubectl -n keepstack wait --for=condition=Available deploy/keepstack-api --timeout=120s
kubectl -n keepstack wait --for=condition=Available deploy/keepstack-worker --timeout=120s
kubectl -n keepstack wait --for=condition=Available deploy/keepstack-web --timeout=120s
```

Once the core workloads are ready, run the verification commands in order:

```sh
make verify-obs
make smoke-v03
make verify-alerts
make backup-now
DST_NS=keepstack-restore make restore-drill
```

Collect the required evidence along the way:

- Capture the **Keepstack Overview** dashboard after `make verify-obs` (`docs/assets/verify-v03-grafana.png`).
- Grab a Grafana alert panel showing both the firing and recovered states created during `make verify-alerts` (`docs/assets/verify-v03-alerts.png`).
- Record the successful restore drill output (for example, `kubectl -n keepstack-restore logs job/keepstack-restore`) and archive a screenshot (`docs/assets/verify-v03-restore.png`).

`make verify-obs` confirms the ServiceMonitor resources exist and that both API and worker pods expose the expected Prometheus metrics before any smoke traffic runs. If the script reports missing monitors, re-run `make helm-dev` with `observability.enabled=true` until they appear.

`make smoke-v03` publishes the seed link, validates tags and highlights, verifies that the resurfacer Job completes, and calls `/api/recommendations` to confirm resurfaced items are stored.

`make verify-alerts` temporarily scales NATS to zero, injects API errors, and posts failing worker jobs so the `KeepstackHighErrorRate` and `KeepstackWorkerFailures` alerts fire. It then restores normal traffic and waits for the alerts to return to the `inactive` state.

`make backup-now` triggers the CronJob and waits for the most recent backup Job to complete. Follow it with the restore drill (see below) to practice disaster recovery against a fresh namespace.

`DST_NS=keepstack-restore make restore-drill` copies the newest dump from the source namespace into a clean namespace, rehydrates Postgres, and validates the API before exiting. Override `DST_NS` with a unique suffix per run to avoid name collisions in shared clusters.

If any wait operation times out, inspect the relevant pod logs (for example, `kubectl -n keepstack logs deploy/keepstack-api`) before re-running the workflow.

### Confirming alert fire & recovery

`make verify-alerts` prints `Alert state` lines as it polls Prometheus. Wait for the script to log both `KeepstackHighErrorRate` and `KeepstackWorkerFailures` reaching the `firing` state and then returning to `inactive`. Capture the Grafana **Alerting → Alerts** screen while the alerts are firing and again after they clear to document recovery. The script automatically restores the PrometheusRule configuration and scales NATS back to its original replica count.

### Restore drill in a fresh namespace

The restore helper accepts both source and destination namespaces. Passing a unique `DST_NS` (for example, `DST_NS=keepstack-restore-$(date +%s) make restore-drill`) deletes any existing namespace with that name, recreates it, and installs a database-only release before running the restore Job. When the script finishes, confirm the drill succeeded with `kubectl -n ${DST_NS} get jobs,pods` or by curling `http://${RELEASE_NAME}-api:8080/api/links` inside the namespace. Cleanup happens automatically on the next invocation.

### Optional TLS toggle

To validate TLS ingress changes, toggle the Helm values and run the rollout observer script:

```sh
helm upgrade --install keepstack deploy/charts/keepstack \
  -n keepstack -f deploy/values/dev.yaml \
  --set tls.enabled=true --set tls.issuer=selfsigned --wait
SMOKE_BASE_URL=https://keepstack.localtest.me KS_NAMESPACE=keepstack scripts/rollout-observe.sh
```

`scripts/rollout-observe.sh` patches the API deployment to trigger a restart while continuously curling the base URL. Use it to confirm no 5xx responses occur while TLS is enabled, then revert the toggle with `--set tls.enabled=false` for subsequent non-TLS smoke tests.

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

- **Local testing**: `make test` (runs API and worker Go tests plus the web production build).
- **Image builds**: `make build` creates linux/amd64 images tagged with `sha-<short commit>`.
- **CI**: GitHub Actions runs Go tests, web builds, Docker image pushes to GHCR, and `helm lint` on every PR and main push.

### Smoke test script usage & troubleshooting

- **Basic usage**: Run `make smoke-v03` once the Helm release is ready. Override defaults such as `SMOKE_BASE_URL`, `SMOKE_POST_TIMEOUT`, or `SMOKE_POLL_TIMEOUT` to target alternative ingress URLs or tune slow environments. For a narrower pass that only exercises the legacy flow, call `./scripts/smoke-v02.sh` directly.
- **Digest dry-run**: Export `DIGEST_TEST=1` to trigger the optional digest preview step. When set, `make smoke-v03` inherits the `log://` SMTP fallback from `smoke-v02` so the API logs the rendered email instead of attempting SMTP delivery.
- **Ingress routing failures**: If the script reports connection or DNS errors, confirm the ingress controller is ready with `kubectl -n ingress-nginx get pods` and that `/etc/hosts` (or your DNS) resolves `keepstack.localtest.me`.
- **Pending database migrations**: A `201` POST followed by repeated polling without the link appearing usually indicates the worker cannot finish migrations. Check the Postgres pod logs (`kubectl -n keepstack logs statefulset/keepstack-postgres`) and re-run `helm-dev` after resolving schema issues.
- **API readiness**: HTTP `5xx` responses or cURL timeouts imply the API deployment is still starting. Readiness now verifies the
  archives metadata columns and highlights table exist; failures surface hints about pending migrations alongside Prometheus metrics.
  Verify deployment health with `kubectl -n keepstack get deploy keepstack-api` and inspect logs via `make logs` to confirm database
  migrations ran successfully.
- **Link publish failures**: Persistent HTTP `5xx` errors or `timeout waiting on ack` messages when posting new links can indicate the API pods cannot reach NATS. Confirm the `keepstack-allow-api-to-nats` NetworkPolicy is installed, that its podSelectors match the API and NATS labels via `kubectl -n keepstack describe netpol keepstack-allow-api-to-nats`, and that the NATS StatefulSet is healthy with `kubectl -n keepstack get statefulset keepstack-nats`.
- **Tear down**: Clean up the development environment with `make dev-down` after smoke testing to delete the k3d cluster.

## v0.1 Scope & Definition of Done

- ✅ Goose migration covering users, links, archives, tags, and search triggers
- ✅ sqlc-generated data access layer for the API
- ✅ API exposes `/healthz`, `/livez`, `/metrics`, `POST /api/links`, `GET /api/links`
- ✅ Worker consumes `keepstack.links.saved`, fetches content, parses, persists archive data, and updates FTS
- ✅ React web UI supports listing, searching, and adding links
- ✅ Helm chart deploys API, worker, web, Postgres, NATS, and Chrome (placeholder) with ingress routing
- ✅ Makefile automates cluster lifecycle, builds, deploys, and smoke testing
- ✅ CI builds/tests all components, publishes images to GHCR, and runs Helm lint
- ✅ Metrics exposed on API and worker pods for scraping

## Roadmap (beyond v0.1)

- Authenticated multi-user support with proper session handling
- Scheduled re-ingest and bookmark tagging UX
- Richer observability (Grafana dashboards, alerting)
- Optional external object storage for large archives

# Smoke test suite

The smoke suite is implemented in Go under [`test/smoke`](../test/smoke). It replaces the
older Bash harness with a single `go test` entry point so local runs and CI
executions share the same assertions and helpers.

## Running the suite locally

The Makefile exposes two convenience targets that hydrate the development
cluster, wait for readiness, and then execute the suite:

- `make smoke` – runs the full suite with the default tag set
  (`digest,observability,resurfacer`).
- `make smoke-fast` – skips the observability and resurfacer checks by default by
  only enabling the `digest` tag.

Both targets honour the `SMOKE_TAGS` environment variable. Set it explicitly to
run a focused subset (for example,
`SMOKE_TAGS=observability,resurfacer make smoke`). Any scenario that is guarded
with `cfg.SkipUnlessTagged` will be skipped unless its tag is present.

The suite can also be invoked directly with the Go toolchain:

```sh
go test ./test/smoke
```

This is useful when you already have a running cluster or want to point the
suite at a remote deployment. Override the environment variables from
[`helpers.go`](../test/smoke/helpers.go) to customise behaviour:

- `SMOKE_BASE_URL`, `SMOKE_POST_PATH`, `SMOKE_GET_PATH`, `SMOKE_TAG_PATH`, and
  `SMOKE_DIGEST_PATH` change the API targets.
- `SMOKE_LINK_URL`, `SMOKE_LINK_TITLE`, `SMOKE_QUERY`, `SMOKE_HIGHLIGHT_QUOTE`,
  and `SMOKE_HIGHLIGHT_NOTE` control the data seeded during the run.
- `SMOKE_TIMEOUT`, `SMOKE_POLL_INTERVAL`, `SMOKE_POLL_TIMEOUT`, and
  `SMOKE_DIGEST_TIMEOUT` tune HTTP and polling behaviour.
- `KS_NAMESPACE` and `KS_RELEASE` point the Kubernetes helpers at a different
  release when the defaults (`keepstack`) do not apply.
- `SMOKE_TAGS` enables additional tagged checks.

Refer to [`test/smoke/smoke_test.go`](../test/smoke/smoke_test.go) for the full
set of helpers and environment variables honoured by the suite.

## Continuous integration

[`.github/workflows/smoke.yml`](../.github/workflows/smoke.yml) runs the suite
on every push to `main` and for pull requests. The job matrix exercises both the
Docker compose (`STACK_ENV=docker`) and k3d (`STACK_ENV=k3d`) paths against
Postgres 15 and 16. Each job calls `make smoke`, which ensures images are built
or pulled, the stack is provisioned, and the Go smoke suite runs with the full
(`SMOKE_TAGS_FULL`) tag set.

When the workflow fails it archives diagnostics (cluster resources, pod logs,
and any suite artifacts) as GitHub Action artifacts to aid troubleshooting.

## Tags and scenarios

Each scenario in `TestSmokeSuite` is optionally guarded by a tag. The table
below lists the available tags and the checks they enable:

| Tag | Subtests | Notes |
| --- | --- | --- |
| (always on) | `health and readiness`, `link crud and search`, `tag assignment and replacement`, `highlight verification`, `backup job trigger` | These subtests always run and cover the baseline CRUD workflow plus backup automation. |
| `digest` | `digest dry run` | Enabled by default in both `make smoke` and `make smoke-fast`. Exercises the `/api/digest/test` endpoint. |
| `observability` | `observability metrics` | Ensures ServiceMonitor resources and metrics endpoints respond before and after port-forwarding. |
| `resurfacer` | `resurfacer recommendations` | Triggers the resurfacer job and verifies recommendations are returned via the API. |

## Mapping legacy checks

Legacy GitHub checks mapped to Bash scripts are superseded by the Go suite. Use
this table to correlate them with the new subtests:

| Legacy check | Equivalent Go subtests | Default tags required |
| --- | --- | --- |
| `smoke` | `health and readiness`, `link crud and search`, `tag assignment and replacement`, `highlight verification` | always on |
| `smoke-v02` | All `smoke` subtests plus `digest dry run` and `backup job trigger` | `digest` |
| `smoke-v03` | All `smoke-v02` subtests plus `observability metrics` and `resurfacer recommendations` | `digest, observability, resurfacer` |

The previous Bash implementations are retained under
[`scripts/archive/smoke`](../scripts/archive/smoke) for reference.

## Adding new smoke scenarios

1. Extend `TestSmokeSuite` in [`smoke_test.go`](../test/smoke/smoke_test.go) with
   a new `t.Run` block. Reuse `cfg.SkipUnlessTagged` to make the scenario
   opt-in.
2. Add the scenario implementation to `scenarioState` or a helper in
   [`helpers.go`](../test/smoke/helpers.go). The existing helpers demonstrate
   polling patterns, Kubernetes access (`cfg.KubeOrSkip`), and structured API
   calls (`cfg.DoJSON`).
3. Update this document and the Makefile tag defaults (`SMOKE_TAGS_FULL` and
   `SMOKE_TAGS_FAST`) if the new scenario needs to be enabled by default.
4. If you keep supplemental shell scripts for specialised runs, place them under
   [`scripts/archive`](../scripts/archive/README.md) so future migrations can
   find them easily.


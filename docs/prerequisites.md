# Development prerequisites

This guide collects the tooling you need before running the Keepstack dev environment and highlights common install commands for macOS (Homebrew) and Debian/Ubuntu. If you manage packages differently, adapt the install steps but make sure each requirement is available on your `PATH`.

## Required tools

| Tool | Purpose | macOS install | Debian/Ubuntu install |
| --- | --- | --- | --- |
| Docker Desktop <br /> (with Buildx) | Builds the container images that power the API, worker, and web UI. Buildx is bundled with current releases. | [Download Docker Desktop](https://www.docker.com/products/docker-desktop/) and enable **Use Docker Compose V2** + **Use containerd for pulls** in the settings. | Install the Docker Engine packages from [docs.docker.com/engine/install](https://docs.docker.com/engine/install/) and add your user to the `docker` group. Install Buildx with `docker buildx install`. |
| k3d | Spins up the local Kubernetes cluster used by the Helm chart. | `brew install k3d` | `curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash` |
| kubectl | Interacts with the k3d cluster and applies manifests. | `brew install kubectl` | `sudo apt-get update && sudo apt-get install -y kubectl` |
| Helm | Deploys the Keepstack chart into the dev cluster. | `brew install helm` | `curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash` |
| GNU Make | Runs the helper targets (e.g., `make dev-up`). | Included with the Xcode Command Line Tools (`xcode-select --install`). | `sudo apt-get update && sudo apt-get install -y make` |

## Optional but recommended

| Tool | Purpose | macOS install | Debian/Ubuntu install |
| --- | --- | --- | --- |
| `gh` (GitHub CLI) | Simplifies logging into GHCR and inspecting releases. | `brew install gh` | `sudo apt-get install -y gh` |
| `jq` | Helps inspect JSON output from Kubernetes and scripts. | `brew install jq` | `sudo apt-get install -y jq` |

## Registry access

Keepstack images default to the GitHub Container Registry (GHCR). Authenticate once so `make build` can push images and the cluster can pull them:

```sh
# Create a GitHub personal access token with the "read:packages" and "write:packages" scopes
export CR_PAT=ghp_your_token_here

echo "$CR_PAT" | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
```

If you are using another registry, export the `REGISTRY` environment variable before running any Make targets, for example `export REGISTRY=registry.example.com/keepstack`.

### Allow the dev cluster to pull private images

If your Keepstack images live in a private GHCR repository, create a pull secret in the `keepstack` namespace before running `make helm-dev`:

```sh
kubectl create namespace keepstack --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret docker-registry keepstack-ghcr \
  --namespace keepstack \
  --docker-server=ghcr.io \
  --docker-username=YOUR_GITHUB_USERNAME \
  --docker-password="$CR_PAT"
```

Then point the Helm chart at the secret by setting `image.pullSecrets` in `deploy/values/dev.yaml` (or another override file) before running `make helm-dev`:

```yaml
image:
  pullSecrets:
    - name: keepstack-ghcr
```

## Verify your setup

1. Confirm Docker is running and Buildx works: `docker buildx ls`.
2. Check the Kubernetes toolchain: `k3d version`, `kubectl version --client`, `helm version`.
3. Validate the helper runner: `make help`.

Once the commands above succeed you are ready to continue with [`make dev-up`](../README.md#quickstart) and the rest of the development workflow.

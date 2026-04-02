# Kubernetes Config Watcher

`config-watcher` is a Go sidecar that acts similarly to the Linux [inotify](https://www.man7.org/linux/man-pages/man7/inotify.7.html) that watches for the file changes and is able to execute defined actions.
The watcher polls for content changes in the files stored on the pods' filesystem and triggers a configured action.

This repo contains:

- Standalone Kubernetes templates under `templates/`.
- The `config-watcher` Go source code under `config-watcher/`.

## What it does

- Polls watched files using metadata-first detection and SHA-256 confirmation.
- Debounces transient/partial writes.
- Adds jitter and cooldown to reduce synchronized restart storms.
- Triggers an action when a stable change is confirmed.

Supported actions:

- `signal` (using `pidFile`)
- `command` (executes a local command)
- `kubernetes_restart` (patches the workload so Argo Rollouts / Deployments restart without in-container process signalling)

## Included templates (`templates/`)

- `templates/configmap.yaml` - watcher runtime config (`/etc/config-watcher/config.yaml`)
- `templates/serviceaccount.yaml` - dedicated service account
- `templates/rbac.yaml` - namespace-scoped Role/RoleBinding limited to a single workload name
- `templates/sidecar-container.yaml` - sidecar container snippet to inject into Pod specs

## How to use the templates

1. Copy templates to your own repository.
2. Replace placeholder values:
   - `${NAMESPACE}`
   - `${WORKLOAD_NAME}`
   - `${WORKLOAD_TYPE}` (`rollout` or `deployment`)
   - `${WATCHER_IMAGE}`
   - `${CONFIG_PATH}` (file to monitor)
3. Mount the same shared volume in app + watcher containers (for example `/ha_shared`).
4. Ensure your Pod uses the dedicated service account from `serviceaccount.yaml`.
5. Add the sidecar snippet under `spec.template.spec.containers` (or the Pod `spec` you use).

## Kubernetes API connectivity (important)

When using `action.type: kubernetes_restart`, `config-watcher` calls the in-cluster Kubernetes API.

It prefers:

- `KUBERNETES_SERVICE_HOST`
- `KUBERNETES_SERVICE_PORT`

So it does not rely on DNS resolution of `kubernetes.default.svc`.

## Example: restart an Argo Rollout

In `templates/configmap.yaml`, configure:

- `action.type: kubernetes_restart`
- `action.kubernetes.workloadType: rollout`
- `action.kubernetes.workloadName: ${WORKLOAD_NAME}`

## Pod spec example

Make sure your workload Pod uses the service account from the template, for example:

```yaml
spec:
  serviceAccountName: ${WORKLOAD_NAME}
  containers:
    # your app container...
    # add the sidecar snippet from templates/sidecar-container.yaml
```

## Source code (`config-watcher/`)

This folder contains the Go module for `config-watcher`.

Build/run (from inside the `config-watcher/` directory):

```bash
go build -o config-watcher .
./config-watcher -config ./config.example.yaml
```

Notes:
- The example config in `config-watcher/config.example.yaml` may include `signal`/`command`.
- For Kubernetes restart, set `action.type: kubernetes_restart` in your rendered watcher config (as done by `templates/configmap.yaml`).

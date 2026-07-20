# Integration testing

The integration suites exercise the real `NodeImageReconciler` end to end against
in-process fakes — no Docker daemon required:

- `test/integration/nodeimage_vsphere/` — the vSphere provider.
- `test/integration/nodeimage_proxmox/` — the Proxmox provider.

Shared in-process infrastructure:

- **envtest** — a real Kubernetes API server + etcd with the `NodeImage` CRD installed.
- **fake S3** — an in-process S3-compatible bucket (`gofakes3`) seeded with image fixtures.

Provider simulators:

- **vcsim** — govmomi's in-process vSphere simulator, standing in for a real vCenter.
- **fake Proxmox** — a hand-rolled in-process Proxmox REST API (`testutil/proxmox.go`);
  Proxmox has no upstream simulator, so the fake implements just the endpoints the
  provider exercises, backed by an in-memory VM inventory and task registry.

## What the tests cover

Each spec creates a `NodeImage`, reconciles it, and asserts against the simulator's
inventory. Both suites cover the same five scenarios:

| Spec | Scenario | Expected outcome |
|------|----------|------------------|
| Import | Valid image seeded in S3 | Template imported into the provider, status `Available` |
| Delete | Imported image, then `NodeImage` deleted | Template destroyed, finalizer cleared, object gone |
| Idempotency | Already-imported image re-reconciled | No re-import (template identity unchanged), stays `Available` |
| Missing | S3 object absent | Reconcile requeues without error, status `Missing`, no template |
| Error | S3 object present but not a valid image | Reconcile returns an error, status `Error`, no template |

vSphere seeds an OVA fixture and downloads it client-side (govmomi); the Error spec
seeds bytes that are not a valid OVA. Proxmox seeds a qcow2 fixture that the fake
downloads and validates server-side; the Error spec seeds bytes that are not a valid
qcow2.

## Running

```sh
make test-integration
```

This first runs `make setup-envtest` to download the envtest control-plane binaries into
`bin/k8s`, then runs the suite with the `integration` build tag. The tag keeps these tests
out of the default `make test` / `go test ./...` run.

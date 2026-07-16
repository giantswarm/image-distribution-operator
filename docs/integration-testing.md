# Integration testing

The integration suite (`test/integration/nodeimage_vsphere/`) exercises the real
`NodeImageReconciler` end to end against in-process fakes — no Docker daemon required:

- **envtest** — a real Kubernetes API server + etcd with the `NodeImage` CRD installed.
- **vcsim** — govmomi's in-process vSphere simulator, standing in for a real vCenter.
- **fake S3** — an in-process S3-compatible bucket (`gofakes3`) seeded with OVA fixtures.

## What the tests cover

Each spec creates a `NodeImage`, reconciles it, and asserts against vcsim's inventory:

| Spec | Scenario | Expected outcome |
|------|----------|------------------|
| Import | Valid OVA seeded in S3 | Template imported into vSphere, status `Available` |
| Delete | Imported image, then `NodeImage` deleted | Template destroyed, finalizer cleared, object gone |
| Idempotency | Already-imported image re-reconciled | No re-import (template ref unchanged), stays `Available` |
| Missing | S3 object absent | Reconcile requeues without error, status `Missing`, no template |
| Error | S3 object present but not a valid OVA | Reconcile returns an error, status `Error`, no template |

## Running

```sh
make test-integration
```

This first runs `make setup-envtest` to download the envtest control-plane binaries into
`bin/k8s`, then runs the suite with the `integration` build tag. The tag keeps these tests
out of the default `make test` / `go test ./...` run.

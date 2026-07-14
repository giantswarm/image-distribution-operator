## Codebase Overview

`image-distribution-operator` is a Giant Swarm Kubernetes operator that syncs VM node OS images across infrastructure providers (vSphere, VMware Cloud Director, Proxmox). It watches external `Release` CRs, derives `NodeImage` CRs (its own CRD), and imports images from S3 into each provider's catalog via a pluggable `Provider` interface.

**Stack**: Go 1.26.3, controller-runtime/kubebuilder (multigroup), Ginkgo/Gomega tests, Helm chart distribution, govmomi (vSphere) / go-vcloud-director (VCD) / hand-rolled REST client (Proxmox).

**Structure**: `cmd/main.go` wires the manager; `internal/controller/{image,release}` hold the two reconcilers; `pkg/provider` defines the provider abstraction implemented by `pkg/{vsphere,cloud-director,proxmox}`; `pkg/image` and `pkg/s3` hold naming/CRUD and S3 helpers; `config/` is the kubebuilder scaffold (dev source of truth) which is mechanically synced into the production `helm/image-distribution-operator` chart via `sync/sync.sh`.

For detailed architecture, module guide, data flow, conventions, and gotchas, see [docs/CODEBASE_MAP.md](docs/CODEBASE_MAP.md).

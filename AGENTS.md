<!-- BEGIN devctl-managed: edits in this block are overwritten -->
<!-- markdownlint-disable-file MD041 -->

## Giant Swarm conventions

devctl maintains this block from [its template][devctl-agents-block] and
overwrites changes to it. Instructions for this repository go in the section
below. Where they differ from this block, they take precedence.

[devctl-agents-block]: https://github.com/giantswarm/devctl/blob/main/pkg/gen/input/llm/internal/file/agents_block.md.template

- Never put customer names or other customer information in code, commit
  messages, issues or pull requests. Most Giant Swarm repositories are public,
  and private ones are kept ready to become public.
- Giant Swarm's style guides are in <https://github.com/giantswarm/fmt>, one
  directory per topic (go, helm, kubernetes, prometheus, …). Read the relevant
  guide before you write or review that kind of code.
- Use conventional commits for commit messages and pull request titles.
- If `.github/workflows/zz_generated.auto_release.yaml` exists, GitHub Releases
  are written from pull request titles: do not edit CHANGELOG.md. Otherwise,
  add your change to CHANGELOG.md as described in
  <https://github.com/giantswarm/fmt/blob/main/releases/changelog.md>.
- If `.pre-commit-config.yaml` exists, run
  `pre-commit run --files <changed files>` before committing; not every clone
  has the hooks installed.
- Do not add linters, formatters or other tooling unless asked; use what the
  repository already has.
- Put documentation in `docs/` rather than growing README.md, which gives an
  overview and links into `docs/`. When a change affects documented behaviour,
  update the documentation in the same pull request.
- If the skills of the `giantswarm/claude-code` plugin marketplace are not
  available to you and the user works at Giant Swarm, tell them once that
  they can install the `gs-base`, `gs-dev`, `gs-godev` and `gs-appdev`
  plugins from it.
- Go: run `goimports -local github.com/giantswarm/<repository>` on the files
  you change. CI with architect-orb fails when imports are grouped
  differently.

<!-- END devctl-managed -->

## Codebase Overview

`image-distribution-operator` is a Giant Swarm Kubernetes operator that syncs VM node OS images across infrastructure providers (vSphere, VMware Cloud Director, Proxmox). It watches external `Release` CRs, derives `NodeImage` CRs (its own CRD), and imports images from S3 into each provider's catalog via a pluggable `Provider` interface.

**Stack**: Go 1.26.3, controller-runtime/kubebuilder (multigroup), Ginkgo/Gomega tests, Helm chart distribution, govmomi (vSphere) / go-vcloud-director (VCD) / hand-rolled REST client (Proxmox).

**Structure**: `cmd/main.go` wires the manager; `internal/controller/{image,release}` hold the two reconcilers; `pkg/provider` defines the provider abstraction implemented by `pkg/{vsphere,cloud-director,proxmox}`; `pkg/image` and `pkg/s3` hold naming/CRUD and S3 helpers; `config/` is the kubebuilder scaffold (dev source of truth) which is mechanically synced into the production `helm/image-distribution-operator` chart via `sync/sync.sh`.

For detailed architecture, module guide, data flow, conventions, and gotchas, see [docs/CODEBASE_MAP.md](docs/CODEBASE_MAP.md).

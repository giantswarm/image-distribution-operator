# Integration test harness (hand-maintained; picked up via `include Makefile.*.mk`).
#
# Stands up an in-process test environment - a real Kubernetes API server (envtest),
# an in-process S3-compatible bucket, and in-process provider APIs (vcsim for vSphere,
# hand-rolled fakes for Proxmox and VMware Cloud Director) - and runs the NodeImage
# controller against it. No Docker daemon is required.

LOCALBIN            ?= $(shell pwd)/bin
ENVTEST             ?= $(LOCALBIN)/setup-envtest
ENVTEST_VERSION     ?= release-0.24
ENVTEST_K8S_VERSION ?= 1.33.0

##@ Integration testing

.PHONY: setup-envtest
setup-envtest: ## Downloads the envtest control-plane binaries (kube-apiserver, etcd) into bin/k8s.
	@echo "====> $@"
	@mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)
	$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/k8s -p path

.PHONY: test-integration
test-integration: setup-envtest ## Runs the in-process integration suites (envtest + vcsim/fake Proxmox/fake VCD + fake S3).
	@echo "====> $@"
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/k8s -p path)" \
		go test -tags=integration ./test/integration/... -v -timeout 300s

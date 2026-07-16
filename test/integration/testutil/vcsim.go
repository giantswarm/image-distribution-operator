//go:build integration

package testutil

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"
)

// vcsim inventory names below match govmomi's default VPX model (see
// simulator.VPX()). They are also written into the locations file consumed by
// pkg/vsphere, and reused by the test to assert against the imported VM.
const (
	VCSimLocation   = "integration"
	VCSimDatacenter = "DC0"
	VCSimCluster    = "DC0_C0"
	VCSimDatastore  = "LocalDS_0"
	VCSimFolder     = "/DC0/vm"
)

// VCSim is an in-process vSphere API simulator serving over HTTPS.
type VCSim struct {
	model  *simulator.Model
	server *simulator.Server
}

// StartVCSim boots the govmomi simulator with the default VPX inventory and TLS
// enabled (pkg/vsphere always dials https://<vcenter>/sdk).
func StartVCSim() (*VCSim, error) {
	model := simulator.VPX()
	if err := model.Create(); err != nil {
		return nil, fmt.Errorf("failed to create vcsim model: %w", err)
	}

	// Serve over HTTPS: pkg/vsphere hard-codes the https scheme.
	model.Service.TLS = new(tls.Config)
	server := model.Service.NewServer()

	return &VCSim{model: model, server: server}, nil
}

// Close tears down the simulator and removes its temporary state.
func (v *VCSim) Close() {
	v.server.Close()
	v.model.Remove()
}

// Host returns the simulator's host:port, used as the "vcenter" credential.
func (v *VCSim) Host() string {
	return v.server.URL.Host
}

// WriteConfig writes the credentials and locations YAML files that pkg/vsphere
// expects, wired to this simulator and its default inventory, and returns their
// paths.
func (v *VCSim) WriteConfig(dir string) (credentialsPath, locationsPath string, err error) {
	credentials := fmt.Sprintf("vcenter: %s\nusername: user\npassword: pass\n", v.Host())
	credentialsPath = filepath.Join(dir, "credentials.yaml")
	if err := os.WriteFile(credentialsPath, []byte(credentials), 0o600); err != nil {
		return "", "", fmt.Errorf("failed to write credentials file: %w", err)
	}

	locations := fmt.Sprintf(`%s:
  datacenter: %s
  datastore: %s
  folder: %s
  cluster: %s
  resourcepool: Resources
`, VCSimLocation, VCSimDatacenter, VCSimDatastore, VCSimFolder, VCSimCluster)
	locationsPath = filepath.Join(dir, "locations.yaml")
	if err := os.WriteFile(locationsPath, []byte(locations), 0o600); err != nil {
		return "", "", fmt.Errorf("failed to write locations file: %w", err)
	}

	return credentialsPath, locationsPath, nil
}

// Client returns a govmomi client connected to the simulator, for asserting on
// inventory state independently of the code under test.
func (v *VCSim) Client(ctx context.Context) (*govmomi.Client, error) {
	u := &url.URL{
		Scheme: "https",
		Host:   v.Host(),
		Path:   "/sdk",
		User:   url.UserPassword("user", "pass"),
	}
	return govmomi.NewClient(ctx, u, true)
}

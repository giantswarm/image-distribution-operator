package vsphere

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/types"
)

// based on upstream importer package except we use pull instead of push
func PullImport(ctx context.Context,
	fpath string, opts importer.Options, imp *importer.Importer, url string) (*types.ManagedObjectReference, error) {

	o, err := importer.ReadOvf(fpath, imp.Archive)
	if err != nil {
		return nil, err
	}

	e, err := importer.ReadEnvelope(o)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ovf: %s", err)
	}

	if e.VirtualSystem != nil {
		if e.VirtualSystem != nil {
			if opts.Name == nil {
				opts.Name = &e.VirtualSystem.ID
				if e.VirtualSystem.Name != nil {
					opts.Name = e.VirtualSystem.Name
				}
			}
		}
		if imp.Hidden {
			// TODO: userConfigurable is optional and defaults to false, so we should *add* userConfigurable=true
			// if not set for a Property. But, there'd be a bunch more work involved to preserve other data in doing
			// a complete xml.Marshal of the .ovf
			o = bytes.ReplaceAll(o, []byte(`userConfigurable="false"`), []byte(`userConfigurable="true"`))
		}
	}

	name := "Govc Virtual Appliance"
	if opts.Name != nil {
		name = *opts.Name
	}

	nmap, err := imp.NetworkMap(ctx, e, opts.NetworkMapping)
	if err != nil {
		return nil, err
	}

	cisp := types.OvfCreateImportSpecParams{
		DiskProvisioning:   opts.DiskProvisioning,
		EntityName:         name,
		IpAllocationPolicy: opts.IPAllocationPolicy,
		IpProtocol:         opts.IPProtocol,
		OvfManagerCommonParams: types.OvfManagerCommonParams{
			DeploymentOption: opts.Deployment,
			Locale:           "US"},
		PropertyMapping: importer.OVFMap(opts.PropertyMapping),
		NetworkMapping:  nmap,
	}

	m := ovf.NewManager(imp.Client)
	spec, err := m.CreateImportSpec(ctx, string(o), imp.ResourcePool, imp.Datastore, &cisp)
	if err != nil {
		return nil, err
	}
	if spec.Error != nil {
		return nil, errors.New(spec.Error[0].LocalizedMessage)
	}
	if spec.Warning != nil {
		for _, w := range spec.Warning {
			_, _ = imp.Log(fmt.Sprintf("Warning: %s\n", w.LocalizedMessage))
		}
	}

	if opts.Annotation != "" {
		switch s := spec.ImportSpec.(type) {
		case *types.VirtualMachineImportSpec:
			s.ConfigSpec.Annotation = opts.Annotation
		case *types.VirtualAppImportSpec:
			s.VAppConfigSpec.Annotation = opts.Annotation
		}
	}

	if imp.VerifyManifest {
		if err := imp.ReadManifest(fpath); err != nil {
			return nil, err
		}
	}

	lease, err := imp.ResourcePool.ImportVApp(ctx, spec.ImportSpec, imp.Folder, imp.Host)
	if err != nil {
		return nil, err
	}

	thumbprint, err := getSSLFingerprint(url)
	if err != nil {
		_ = lease.Abort(ctx, nil)
		return nil, fmt.Errorf("failed to get SSL fingerprint: %w", err)
	}

	sourceFiles := make([]types.HttpNfcLeaseSourceFile, len(spec.FileItem))
	for i, fileItem := range spec.FileItem {
		sourceFiles[i] = types.HttpNfcLeaseSourceFile{
			Url:            url,
			TargetDeviceId: fileItem.DeviceId,
			Create:         fileItem.Create,
			Size:           fileItem.Size,
			MemberName:     fileItem.Path,
			SslThumbprint:  thumbprint,
		}
	}

	// Wait for lease to be ready
	info, err := lease.Wait(ctx, spec.FileItem)
	if err != nil {
		_ = lease.Abort(ctx, nil)
		return nil, fmt.Errorf("failed to wait for lease: %w", err)
	}

	t, err := methods.HttpNfcLeasePullFromUrls_Task(ctx, imp.Client, &types.HttpNfcLeasePullFromUrls_Task{
		This:  lease.Reference(),
		Files: sourceFiles,
	})
	if err != nil {
		_ = lease.Abort(ctx, nil)
		return nil, fmt.Errorf("failed to start pull task: %w", err)
	}

	// Wait for task completion
	task := object.NewTask(imp.Client, t.Returnval)
	if err := task.WaitEx(ctx); err != nil {
		_ = lease.Abort(ctx, nil)
		return nil, fmt.Errorf("pull task failed: %w", err)
	}

	// Complete the lease
	return &info.Entity, lease.Complete(ctx)
}

func getSSLFingerprint(imageURL string) (string, error) {
	u, err := url.Parse(imageURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	// if its http, return 0000
	if u.Scheme == "http" {
		return "0000", nil
	}

	host := u.Hostname()

	conn, err := tls.Dial("tcp", net.JoinHostPort(host, "443"), &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12, // #nosec G402 - minimum secure version
	})
	if err != nil {
		return "", fmt.Errorf("failed to connect: %w", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			fmt.Printf("failed to close connection: %v\n", cerr)
		}
	}()

	cert := conn.ConnectionState().PeerCertificates[0]
	hash := sha1.Sum(cert.Raw) // #nosec G401 -- vSphere requires SHA1 for certificate thumbprints

	return strings.ToUpper(hex.EncodeToString(hash[:])), nil
}

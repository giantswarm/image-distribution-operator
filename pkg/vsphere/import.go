package vsphere

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ImporterConfig holds the configuration for the OVF importer
type ImporterConfig struct {
	Name         string
	Datacenter   *object.Datacenter
	Datastore    *object.Datastore
	Folder       *object.Folder
	Host         *object.HostSystem
	Network      types.ManagedObjectReference
	ResourcePool *object.ResourcePool
	Finder       *find.Finder
	Path         string
}

// importImage imports an OVF image to vSphere
func (c *Client) importImage(ctx context.Context, imageURL string, imageName string, loc string) (
	*types.ManagedObjectReference, error) {

	log := log.FromContext(ctx)

	finder := find.NewFinder(c.vsphere.Client, true)

	dc, err := c.getDatacenter(ctx, finder, loc)
	if err != nil {
		return nil, fmt.Errorf("failed to get datacenter: %w", err)
	}
	finder.SetDatacenter(dc)

	datastore, err := c.getDatastore(ctx, finder, loc)
	if err != nil {
		return nil, fmt.Errorf("failed to get datastore: %w", err)
	}

	folder, err := c.getFolder(ctx, c.locations[loc].Folder, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get folder: %w", err)
	}

	pool, err := c.getResourcePool(ctx, c.locations[loc].Resourcepool, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource pool: %w", err)
	}

	host, err := c.getHost(ctx, c.locations[loc].Host, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}

	network, err := c.getNetwork(ctx, c.locations[loc].Network, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get network: %w", err)
	}

	imageSuffix := c.locations[loc].ImageSuffix
	if len(imageSuffix) > 0 {
		imageName = fmt.Sprintf("%s-%s", imageName, imageSuffix)
	}

	options := &importer.Options{
		Name:             &imageName,
		DiskProvisioning: "thin",
		NetworkMapping: []importer.Network{
			{
				Name:    "nic0",
				Network: network.String(),
			},
		},
	}

	importer := c.getImporter(
		ImporterConfig{
			Name:         imageName,
			Datacenter:   dc,
			Datastore:    datastore,
			Folder:       folder,
			Host:         host,
			ResourcePool: pool,
			Finder:       finder,
			Path:         imageURL,
		},
	)

	log.Info("Importing OVF", "imageURL", imageURL, "imageName", imageName)

	if c.pullMode {
		log.Info("Pull mode enabled")
		return pullImport(ctx, "*.ovf", *options, importer, imageURL)
	}
	return importer.Import(ctx, "*.ovf", *options)
}

func (c *Client) getImporter(config ImporterConfig) *importer.Importer {
	archive := &importer.TapeArchive{Path: config.Path}
	archive.Client = c.vsphere.Client

	return &importer.Importer{
		Name:           config.Name,
		Client:         c.vsphere.Client,
		Datacenter:     config.Datacenter,
		Datastore:      config.Datastore,
		Folder:         config.Folder,
		Host:           config.Host,
		ResourcePool:   config.ResourcePool,
		Finder:         config.Finder,
		Log:            func(msg string) (int, error) { return fmt.Print(msg) },
		Archive:        archive,
		Manifest:       nil, // Placeholder, update if needed
		VerifyManifest: false,
	}
}

// based on upstream importer package except we use pull instead of push
func pullImport(ctx context.Context,
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

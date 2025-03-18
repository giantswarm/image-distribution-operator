package vsphere

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/nfc"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/vim25/progress"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Client wraps the govmomi client
type Client struct {
	vsphere      *govmomi.Client
	url          string
	datacenter   string
	datastore    string
	username     string
	password     string
	folder       string
	host         string
	resourcepool string
	network      string
}

// Config holds the configuration for the vSphere client
type Config struct {
	URL          string
	Username     string
	Password     string
	Datacenter   string
	Datastore    string
	Folder       string
	Host         string
	ResourcePool string
	Network      string
}

// New initializes a new vSphere client
func New(c Config, ctx context.Context) (*Client, error) {
	log := log.FromContext(ctx)

	log.Info("Connecting to vSphere", "vSphereURL", c.URL)

	u, err := url.Parse(fmt.Sprintf("https://%s:%s@%s/sdk",
		c.Username,
		c.Password,
		c.URL,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to parse vSphere URL:\n%w", err)
	}

	client, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create vSphere client:\n%w", err)
	}

	log.Info("Successfully connected to vSphere", "vSphereURL", c.URL)

	return &Client{
		vsphere:      client,
		url:          c.URL,
		datacenter:   c.Datacenter,
		datastore:    c.Datastore,
		username:     c.Username,
		password:     c.Password,
		folder:       c.Folder,
		host:         c.Host,
		resourcepool: c.ResourcePool,
	}, nil
}

func (c *Client) Import(ctx context.Context, imageURL string, imageName string) (*types.ManagedObjectReference, error) {
	// Create a new finder
	finder := find.NewFinder(c.vsphere.Client, true)

	// Get the datacenter object
	dc, err := c.GetDatacenter(ctx, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get datacenter: %w", err)
	}
	finder.SetDatacenter(dc)

	// Get the datastore object
	datastore, err := c.GetDatastore(ctx, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get datastore: %w", err)
	}

	// Get the folder object
	folder, err := c.GetFolder(ctx, c.folder, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get folder: %w", err)
	}

	// Get the resource pool object
	pool, err := c.GetResourcePool(ctx, c.resourcepool, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource pool: %w", err)
	}

	host, err := c.GetHost(ctx, c.host, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}

	network, err := c.GetNetwork(ctx, c.network, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get network: %w", err)
	}

	options := &importer.Options{
		Name: &imageName,
		NetworkMapping: []importer.Network{
			{
				Name:    "nic0",
				Network: network.String(),
			},
		},
	}

	importer := c.getImporter(
		ctx,
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

	// Use `importer.Import` to handle OVA import automatically
	return importer.Import(ctx, imageURL, *options)
}

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

func (c *Client) getImporter(ctx context.Context, config ImporterConfig) *importer.Importer {
	return &importer.Importer{
		Name:         config.Name,
		Client:       c.vsphere.Client,
		Datacenter:   config.Datacenter,
		Datastore:    config.Datastore,
		Folder:       config.Folder,
		Host:         config.Host,
		ResourcePool: config.ResourcePool,
		Finder:       config.Finder,
		Sinker:       &progress.ProgressLogger{},
		Archive:      &importer.TapeArchive{Path: config.Path},
		Manifest:     nil, // Placeholder, update if needed
	}
}

// GetImportParams returns the import parameters
func (c *Client) ImportOVAFromURL(ctx context.Context, imageURL string, imageName string) (*nfc.Lease, *[]types.OvfFileItem, error) {

	// Fetch the OVF descriptor from the given URL
	ovfDescriptor, err := FetchOVFDescriptorFromOVA(ctx, imageURL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch OVF descriptor: %w", err)
	}

	// Create a new finder
	finder := find.NewFinder(c.vsphere.Client, true)

	// Get the datacenter object
	dc, err := c.GetDatacenter(ctx, finder)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get datacenter: %w", err)
	}
	finder.SetDatacenter(dc)

	// Get the datastore object
	datastore, err := c.GetDatastore(ctx, finder)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get datastore: %w", err)
	}

	// Get the folder object
	folder, err := c.GetFolder(ctx, c.folder, finder)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get folder: %w", err)
	}

	// Get the resource pool object
	pool, err := c.GetResourcePool(ctx, c.resourcepool, finder)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get resource pool: %w", err)
	}

	host, err := c.GetHost(ctx, c.host, finder)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get host: %w", err)
	}

	network, err := c.GetNetwork(ctx, c.network, finder)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get network: %w", err)
	}

	// Create the import parameters
	importParams := types.OvfCreateImportSpecParams{
		DiskProvisioning: "thin",
		EntityName:       imageName,
		NetworkMapping: []types.OvfNetworkMapping{
			{
				Name:    "nic0",
				Network: network,
			},
		},
	}

	// Create the import task
	lease, items, err := c.CreateImportTask(ctx, TaskConfig{
		ResourcePool:  pool,
		Datastore:     datastore,
		Folder:        folder,
		OVFDescriptor: ovfDescriptor,
		ImportParams:  importParams,
		Host:          host,
		RemotePath:    imageURL,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create import task: %w", err)
	}

	return lease, items, nil
}

type TaskConfig struct {
	ResourcePool  *object.ResourcePool
	Datastore     *object.Datastore
	Folder        *object.Folder
	OVFDescriptor string
	ImportParams  types.OvfCreateImportSpecParams
	Host          *object.HostSystem
	RemotePath    string
}

// CreateImportTask creates an OVA import task and returns the lease
func (c *Client) CreateImportTask(ctx context.Context, config TaskConfig) (*nfc.Lease, *[]types.OvfFileItem, error) {
	ovfManager := ovf.NewManager(c.vsphere.Client)

	importSpec, err := ovfManager.CreateImportSpec(
		ctx,
		config.OVFDescriptor,
		config.ResourcePool,
		config.Datastore,
		&config.ImportParams,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create import spec: %w", err)
	}

	if importSpec.Error != nil {
		return nil, nil, fmt.Errorf("import spec contains errors: %+v", importSpec.Error)
	}

	// Create the import task
	lease, err := config.ResourcePool.ImportVApp(ctx, importSpec.ImportSpec, config.Folder, config.Host)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start OVA import task: %w", err)
	}

	return lease, &importSpec.FileItem, nil
}

// GetDatacenter returns the datacenter object
func (c *Client) GetDatacenter(ctx context.Context, finder *find.Finder) (*object.Datacenter, error) {
	dc, err := finder.DatacenterOrDefault(ctx, c.datacenter)
	if err != nil {
		return nil, fmt.Errorf("failed to find datacenter %s:\n%w", c.datacenter, err)
	}
	return dc, nil
}

// GetDatastore returns the datastore object
func (c *Client) GetDatastore(ctx context.Context, finder *find.Finder) (*object.Datastore, error) {
	datastore, err := finder.DatastoreOrDefault(ctx, c.datastore)
	if err != nil {
		return nil, fmt.Errorf("failed to find datastore %s: %w", c.datastore, err)
	}
	return datastore, nil
}

// GetFolder returns the folder object
func (c *Client) GetFolder(ctx context.Context, folder string, finder *find.Finder) (*object.Folder, error) {
	folderObj, err := finder.FolderOrDefault(ctx, folder)
	if err != nil {
		return nil, fmt.Errorf("failed to find folder %s: %w", folder, err)
	}
	return folderObj, nil
}

// GetHost returns the host object
func (c *Client) GetHost(ctx context.Context, hostName string, finder *find.Finder) (*object.HostSystem, error) {
	var host *object.HostSystem
	var err error
	if hostName != "" {
		host, err = finder.HostSystemOrDefault(ctx, hostName)
		fmt.Printf("%v", host)
		if err != nil {
			return nil, fmt.Errorf("failed to find host %s: %w", hostName, err)
		}
	} else {
		hosts, err := finder.HostSystemList(ctx, "*") // Get all hosts
		if err != nil {
			return nil, fmt.Errorf("failed to list hosts: %w", err)
		}
		if len(hosts) == 0 {
			return nil, fmt.Errorf("no hosts found in vSphere")
		}
		host = hosts[0]
	}
	return host, nil
}

// GetResourcePool returns the resource pool object
func (c *Client) GetResourcePool(ctx context.Context, resourcePool string, finder *find.Finder) (*object.ResourcePool, error) {
	pool, err := finder.ResourcePoolOrDefault(ctx, resourcePool)
	if err != nil {
		return nil, fmt.Errorf("failed to find resource pool %s: %w", resourcePool, err)
	}
	return pool, nil
}

// GetNetwork returns the network object
func (c *Client) GetNetwork(ctx context.Context, networkName string, finder *find.Finder) (types.ManagedObjectReference, error) {
	var network object.NetworkReference
	var err error
	if networkName != "" {
		network, err = finder.NetworkOrDefault(ctx, networkName)
		if err != nil {
			return types.ManagedObjectReference{}, fmt.Errorf("failed to find network %s: %w", networkName, err)
		}
	} else {
		networks, err := finder.NetworkList(ctx, "*") // Get all networks
		if err != nil {
			return types.ManagedObjectReference{}, fmt.Errorf("failed to list networks: %w", err)
		}
		if len(networks) == 0 {
			return types.ManagedObjectReference{}, fmt.Errorf("no networks found in vSphere")
		}
		network = networks[0]
	}
	return network.Reference(), nil
}

// This was done by chatgpt, TODO: make it cleaner
func FetchOVFDescriptorFromOVA(ctx context.Context, ovaURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ovaURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch OVA: %w", err)
	}
	defer resp.Body.Close()

	// Detect if the OVA is compressed (gzipped)
	var tarReader *tar.Reader
	if strings.HasSuffix(ovaURL, ".gz") {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return "", fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gzipReader.Close()
		tarReader = tar.NewReader(gzipReader)
	} else {
		tarReader = tar.NewReader(resp.Body)
	}

	// Scan the OVA tar archive for the OVF descriptor
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return "", fmt.Errorf("failed to read tar header: %w", err)
		}

		// Find the .ovf file
		if strings.HasSuffix(header.Name, ".ovf") {
			ovfData, err := io.ReadAll(tarReader)
			if err != nil {
				return "", fmt.Errorf("failed to read OVF descriptor: %w", err)
			}
			return string(ovfData), nil
		}
	}

	return "", fmt.Errorf("no OVF descriptor found in OVA")
}

// WIP
// UploadToLease uploads the OVA to vSphere using the lease URLs.
func (c *Client) UploadToLease(ctx context.Context, lease *nfc.Lease, localOVAPath string, items []types.OvfFileItem) error {
	log := log.FromContext(ctx)

	info, updater, err := c.MonitorLeaseProgress(ctx, lease, items)
	if err != nil {
		return err
	}
	defer updater.Done() // Stop progress updater

	file, err := os.Open(localOVAPath)
	if err != nil {
		lease.Abort(ctx, &types.LocalizedMethodFault{LocalizedMessage: "Failed to open OVA"})
		return fmt.Errorf("failed to open OVA file: %w", err)
	}
	defer file.Close()

	fileStat, err := file.Stat()
	if err != nil {
		lease.Abort(ctx, &types.LocalizedMethodFault{LocalizedMessage: "Failed to get file size"})
		return fmt.Errorf("failed to get file size: %w", err)
	}

	for _, item := range info.Items {
		log.Info(fmt.Sprintf("Uploading file %s to %s", item.DeviceId, item.URL.String()))

		err = lease.Upload(ctx, item, file, soap.Upload{ContentLength: fileStat.Size()})
		if err != nil {
			lease.Abort(ctx, &types.LocalizedMethodFault{LocalizedMessage: "Upload failed"})
			return fmt.Errorf("failed to upload OVA: %w", err)
		}
	}

	log.Info("OVA successfully uploaded to vSphere!")
	return nil
}

func (c *Client) MonitorLeaseProgress(ctx context.Context, lease *nfc.Lease, items []types.OvfFileItem) (*nfc.LeaseInfo, *nfc.LeaseUpdater, error) {
	log := log.FromContext(ctx)

	info, err := lease.Wait(ctx, items)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get lease info: %w", err)
	}

	log.Info("Lease acquired. Starting progress updater...")
	updater := lease.StartUpdater(ctx, info)

	return info, updater, nil
}

func (c *Client) CompleteLease(ctx context.Context, lease *nfc.Lease) error {
	log := log.FromContext(ctx)

	err := lease.Complete(ctx)
	if err != nil {
		return fmt.Errorf("failed to complete lease: %w", err)
	}

	log.Info("Lease completed successfully.")
	return nil
}

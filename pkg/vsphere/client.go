package vsphere

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/nfc"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf"
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

// GetImportParams returns the import parameters
func (c *Client) ImportOVAFromURL(ctx context.Context, imageURL string, imageName string) (*nfc.Lease, error) {
	// Fetch the OVF descriptor from the given URL
	ovfDescriptor, err := FetchOVFDescriptor(ctx, imageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OVF descriptor: %w", err)
	}

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

	// Get the host object
	host, err := c.GetHost(ctx, c.host, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}

	// Create the import parameters
	importParams := types.OvfCreateImportSpecParams{
		DiskProvisioning: "thin",
		EntityName:       imageName,
	}

	// Create the import task
	lease, err := c.CreateImportTask(ctx, TaskConfig{
		ResourcePool:  pool,
		Datastore:     datastore,
		Folder:        folder,
		OVFDescriptor: ovfDescriptor,
		ImportParams:  importParams,
		Host:          host,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create import task: %w", err)
	}

	return lease, nil
}

type TaskConfig struct {
	ResourcePool  *object.ResourcePool
	Datastore     *object.Datastore
	Folder        *object.Folder
	OVFDescriptor string
	ImportParams  types.OvfCreateImportSpecParams
	Host          *object.HostSystem
}

// CreateImportTask creates an OVA import task and returns the lease
func (c *Client) CreateImportTask(ctx context.Context, config TaskConfig) (*nfc.Lease, error) {
	ovfManager := ovf.NewManager(c.vsphere.Client)

	importSpec, err := ovfManager.CreateImportSpec(
		ctx,
		config.OVFDescriptor,
		config.ResourcePool,
		config.Datastore,
		&config.ImportParams,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create import spec: %w", err)
	}

	if importSpec.Error != nil {
		return nil, fmt.Errorf("import spec contains errors: %+v", importSpec.Error)
	}

	// Create the import task
	lease, err := config.ResourcePool.ImportVApp(ctx, importSpec.ImportSpec, config.Folder, config.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to start OVA import task: %w", err)
	}

	return lease, nil
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
	host, err := finder.HostSystemOrDefault(ctx, hostName)
	if err != nil {
		return nil, fmt.Errorf("failed to find host %s: %w", hostName, err)
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

// FetchOVFDescriptor fetches the OVF descriptor from the given URL
func FetchOVFDescriptor(ctx context.Context, imageURL string) (string, error) {
	resp, err := http.Get(imageURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch OVF descriptor: %w", err)
	}
	defer resp.Body.Close()

	// Read the OVF descriptor into a string
	ovfDescriptor, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read OVF descriptor: %w", err)
	}

	return string(ovfDescriptor), nil
}

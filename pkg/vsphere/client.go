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
	vsphere    *govmomi.Client
	url        string
	datacenter string
	datastore  string
	username   string
	password   string
}

// Config holds the configuration for the vSphere client
type Config struct {
	URL        string
	Username   string
	Password   string
	Datacenter string
	Datastore  string
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
		vsphere:    client,
		url:        c.URL,
		datacenter: c.Datacenter,
		datastore:  c.Datastore,
		username:   c.Username,
		password:   c.Password,
	}, nil
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
func (c *Client) GetDatacenter(ctx context.Context) (*object.Datacenter, error) {
	finder := find.NewFinder(c.vsphere.Client, true)
	dc, err := finder.DatacenterOrDefault(ctx, c.datacenter)
	if err != nil {
		return nil, fmt.Errorf("failed to find datacenter %s:\n%w", c.datacenter, err)
	}
	finder.SetDatacenter(dc)
	return dc, nil
}

// GetDatastore returns the datastore object
func (c *Client) GetDatastore(ctx context.Context) (*object.Datastore, error) {
	finder := find.NewFinder(c.vsphere.Client, true)
	datastore, err := finder.DatastoreOrDefault(ctx, c.datastore)
	if err != nil {
		return nil, fmt.Errorf("failed to find datastore %s: %w", c.datastore, err)
	}
	return datastore, nil
}

// GetFolder returns the folder object
func (c *Client) GetFolder(ctx context.Context, dc *object.Datacenter) (*object.Folder, error) {
	finder := find.NewFinder(c.vsphere.Client, true)

	// Find the default folder - we might want to do something more sophisticated here
	finder.SetDatacenter(dc)
	folder, err := finder.DefaultFolder(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find default folder: %w", err)
	}
	return folder, nil
}

// GetHost returns the host object
func (c *Client) GetHost(ctx context.Context, hostName string) (*object.HostSystem, error) {
	finder := find.NewFinder(c.vsphere.Client, true)
	host, err := finder.HostSystem(ctx, hostName)
	if err != nil {
		return nil, fmt.Errorf("failed to find host %s: %w", hostName, err)
	}
	return host, nil
}

// GetResourcePool returns the resource pool object
func (c *Client) GetResourcePool(ctx context.Context, dc *object.Datacenter) (*object.ResourcePool, error) {
	finder := find.NewFinder(c.vsphere.Client, true)

	// Find the default resource pool - we might want to do something more sophisticated here
	finder.SetDatacenter(dc)
	pool, err := finder.DefaultResourcePool(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find default resource pool: %w", err)
	}
	return pool, nil
}

// GetImportParams returns the import parameters
func (c *Client) ImportOVAFromURL(ctx context.Context, imageURL string, imageName string) (*nfc.Lease, error) {
	// Fetch the OVF descriptor from the given URL
	ovfDescriptor, err := FetchOVFDescriptor(ctx, imageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OVF descriptor: %w", err)
	}

	// Get the datacenter object
	dc, err := c.GetDatacenter(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get datacenter: %w", err)
	}

	// Get the datastore object
	datastore, err := c.GetDatastore(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get datastore: %w", err)
	}

	// Get the folder object
	folder, err := c.GetFolder(ctx, dc)
	if err != nil {
		return nil, fmt.Errorf("failed to get folder: %w", err)
	}

	// Get the resource pool object
	pool, err := c.GetResourcePool(ctx, dc)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource pool: %w", err)
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
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create import task: %w", err)
	}

	return lease, nil
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

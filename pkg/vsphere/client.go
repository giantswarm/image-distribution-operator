package vsphere

import (
	"context"
	"fmt"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/vim25/progress"
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

func (c *Client) Import(ctx context.Context, imageURL string, imagePath string, imageName string) (*types.ManagedObjectReference, error) {
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
	return importer.Import(ctx, "*.ovf", *options)
}

func (c *Client) Deploy(ctx context.Context, ref types.ManagedObjectReference) error {
	vm := object.NewVirtualMachine(c.vsphere.Client, ref)
	// todo: do stuff with vm
	fmt.Printf("Deploying VM: %v", vm)
	return nil
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
	fmt.Printf("config: %v", config)

	archive := &importer.TapeArchive{Path: config.Path}
	archive.Client = c.vsphere.Client

	logger := progress.NewProgressLogger(func(msg string) (int, error) {
		fmt.Println(msg) // Log the message
		return len(msg), nil
	}, "upload")

	return &importer.Importer{
		Name:           config.Name,
		Client:         c.vsphere.Client,
		Datacenter:     config.Datacenter,
		Datastore:      config.Datastore,
		Folder:         config.Folder,
		Host:           config.Host,
		ResourcePool:   config.ResourcePool,
		Finder:         config.Finder,
		Sinker:         logger,
		Log:            func(msg string) (int, error) { return len(msg), nil },
		Archive:        archive,
		Manifest:       nil, // Placeholder, update if needed
		VerifyManifest: false,
	}
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

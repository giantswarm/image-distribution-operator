package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/vim25/types"
	"gopkg.in/yaml.v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Client wraps the govmomi client
type Client struct {
	vsphere   *govmomi.Client
	url       string
	pullMode  bool
	Locations map[string]*Location
}

type Location struct {
	Datacenter   string `yaml:"datacenter"`
	Datastore    string `yaml:"datastore"`
	Folder       string `yaml:"folder"`
	Host         string `yaml:"host"`
	Resourcepool string `yaml:"resourcepool"`
	Network      string `yaml:"network"`
	Cluster      string `yaml:"cluster"`
	ImageSuffix  string `yaml:"imagesuffix"`
}

// Config holds the configuration for the vSphere client
type Config struct {
	CredentialsFile string
	LocationsFile   string
	PullMode        bool
}

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

// New initializes a new vSphere client
func New(c Config, ctx context.Context) (*Client, error) {
	log := log.FromContext(ctx)

	vcenter, username, password, err := LoadCredentials(c.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load credentials:\n%w", err)
	}

	log.Info("Connecting to vSphere", "vSphereURL", vcenter)

	u := &url.URL{
		Scheme: "https",
		Host:   vcenter,
		Path:   "/sdk",
		User:   url.UserPassword(username, password),
	}

	if err != nil {
		return nil, fmt.Errorf("failed to parse vSphere URL:\n%w", err)
	}

	client, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create vSphere client:\n%w", err)
	}

	log.Info("Successfully connected to vSphere", "vSphereURL", vcenter)

	locations, err := LoadLocations(c.LocationsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load locations file:\n%w", err)
	}

	return &Client{
		vsphere:   client,
		url:       vcenter,
		Locations: locations,
		pullMode:  c.PullMode,
	}, nil
}

// Exists checks if an image already exists in vSphere
func (c *Client) Exists(ctx context.Context, name string, loc string) (bool, error) {
	finder := find.NewFinder(c.vsphere.Client, true)

	dc, err := c.getDatacenter(ctx, finder, loc)
	if err != nil {
		return false, fmt.Errorf("failed to get datacenter: %w", err)
	}
	finder.SetDatacenter(dc)

	_, err = finder.VirtualMachine(ctx, c.GetVMPath(name, loc))
	if err != nil {
		return false, nil
	}
	return true, nil
}

// Delete deletes an image from vSphere
func (c *Client) Delete(ctx context.Context, name string, loc string) error {
	log := log.FromContext(ctx)

	finder := find.NewFinder(c.vsphere.Client, true)

	dc, err := c.getDatacenter(ctx, finder, loc)
	if err != nil {
		return fmt.Errorf("failed to get datacenter: %w", err)
	}
	finder.SetDatacenter(dc)

	vm, err := finder.VirtualMachine(ctx, c.GetVMPath(name, loc))
	if err != nil {
		// If the VM doesn't exist, return nil
		return nil
	}

	task, err := vm.Destroy(ctx)
	if err != nil {
		return fmt.Errorf("failed to destroy VM %s: %w", name, err)
	}

	err = task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for task: %w", err)
	}

	log.Info("Deleted VM", "name", name)

	return nil
}

// Import imports an OVF image to vSphere
func (c *Client) Import(ctx context.Context, imageURL string, imageName string, loc string) (
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

	folder, err := c.getFolder(ctx, c.Locations[loc].Folder, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get folder: %w", err)
	}

	pool, err := c.getResourcePool(ctx, c.Locations[loc].Resourcepool, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource pool: %w", err)
	}

	host, err := c.getHost(ctx, c.Locations[loc].Host, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}

	network, err := c.getNetwork(ctx, c.Locations[loc].Network, finder)
	if err != nil {
		return nil, fmt.Errorf("failed to get network: %w", err)
	}

	imageSuffix := c.Locations[loc].ImageSuffix
	if len(imageSuffix) > 0 {
		imageName = fmt.Sprintf("%s-%s", imageName, imageSuffix)
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
		return PullImport(ctx, "*.ovf", *options, importer, imageURL)
	}
	return importer.Import(ctx, "*.ovf", *options)
}

// Process processes the OVF image
func (c *Client) Process(ctx context.Context, ref types.ManagedObjectReference) error {
	log := log.FromContext(ctx)
	vm := object.NewVirtualMachine(c.vsphere.Client, ref)

	err := vm.MarkAsTemplate(ctx)
	if err != nil {
		return fmt.Errorf("failed to mark vm as template: %w", err)
	}

	log.Info("Processed vm", "vm", vm.Name())
	return nil
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

// getDatacenter returns the datacenter object
func (c *Client) getDatacenter(ctx context.Context, finder *find.Finder, loc string) (*object.Datacenter, error) {
	dc, err := finder.DatacenterOrDefault(ctx, c.Locations[loc].Datacenter)
	if err != nil {
		return nil, fmt.Errorf("failed to find datacenter %s:\n%w", c.Locations[loc].Datacenter, err)
	}
	return dc, nil
}

// getDatastore returns the datastore object
func (c *Client) getDatastore(ctx context.Context, finder *find.Finder, loc string) (*object.Datastore, error) {
	datastore, err := finder.DatastoreOrDefault(ctx, c.Locations[loc].Datastore)
	if err != nil {
		return nil, fmt.Errorf("failed to find datastore %s: %w", c.Locations[loc].Datastore, err)
	}
	return datastore, nil
}

// getFolder returns the folder object
func (c *Client) getFolder(ctx context.Context, folder string, finder *find.Finder) (*object.Folder, error) {
	folderObj, err := finder.FolderOrDefault(ctx, folder)
	if err != nil {
		return nil, fmt.Errorf("failed to find folder %s: %w", folder, err)
	}
	return folderObj, nil
}

// getHost returns the host object
func (c *Client) getHost(ctx context.Context, hostName string, finder *find.Finder) (*object.HostSystem, error) {
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

// getResourcePool returns the resource pool object
func (c *Client) getResourcePool(ctx context.Context, rp string, finder *find.Finder) (*object.ResourcePool, error) {
	pool, err := finder.ResourcePoolOrDefault(ctx, rp)
	if err != nil {
		return nil, fmt.Errorf("failed to find resource pool %s: %w", rp, err)
	}
	return pool, nil
}

// getNetwork returns the network object
func (c *Client) getNetwork(ctx context.Context, n string, finder *find.Finder) (types.ManagedObjectReference, error) {
	var network object.NetworkReference
	var err error
	if n != "" {
		network, err = finder.NetworkOrDefault(ctx, n)
		if err != nil {
			return types.ManagedObjectReference{}, fmt.Errorf("failed to find network %s: %w", n, err)
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

func (c *Client) GetVMPath(name string, loc string) string {
	return fmt.Sprintf("%s/%s", c.Locations[loc].Folder, name)
}

func LoadLocations(path string) (map[string]*Location, error) {
	locations := make(map[string]*Location)

	file, err := os.ReadFile(path) // nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("failed to read locations file:\n%w", err)
	}

	if err := yaml.Unmarshal(file, locations); err != nil {
		return nil, fmt.Errorf("failed to unmarshal locations file:\n%w", err)
	}

	for k, v := range locations {
		if v.Datacenter == "" {
			return nil, fmt.Errorf("datacenter is required for location %s", k)
		}
		if v.Datastore == "" {
			return nil, fmt.Errorf("datastore is required for location %s", k)
		}
		if v.Folder == "" {
			return nil, fmt.Errorf("folder is required for location %s", k)
		}
		if v.Cluster == "" {
			return nil, fmt.Errorf("cluster is required for location %s", k)
		}
		locations[k].Resourcepool = fmt.Sprintf("/%s/host/%s/%s", v.Datacenter, v.Cluster, v.Resourcepool)
	}
	return locations, nil
}

func LoadCredentials(path string) (string, string, string, error) {
	file, err := os.ReadFile(path) // nolint:gosec
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read credentials file:\n%w", err)
	}

	var creds struct {
		VCenter  string `yaml:"vcenter"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	}

	if err := yaml.Unmarshal(file, &creds); err != nil {
		return "", "", "", fmt.Errorf("failed to unmarshal credentials file:\n%w", err)
	}

	return creds.VCenter, creds.Username, creds.Password, nil
}

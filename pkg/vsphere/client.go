package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"gopkg.in/yaml.v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Client wraps the govmomi client
type Client struct {
	vsphere   *govmomi.Client
	url       string
	pullMode  bool
	locations map[string]*Location
}

type Credentials struct {
	VCenter  string `yaml:"vcenter"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
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

// New initializes a new vSphere client
func New(c Config, ctx context.Context) (*Client, error) {
	log := log.FromContext(ctx)

	creds, err := loadCredentials(c.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load credentials:\n%w", err)
	}

	log.Info("Connecting to vSphere", "vSphereURL", creds.VCenter)

	u := &url.URL{
		Scheme: "https",
		Host:   creds.VCenter,
		Path:   "/sdk",
		User:   url.UserPassword(creds.Username, creds.Password),
	}

	client, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create vSphere client:\n%w", err)
	}

	log.Info("Successfully connected to vSphere", "vSphereURL", creds.VCenter)

	locations, err := loadLocations(c.LocationsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load locations file:\n%w", err)
	}

	return &Client{
		vsphere:   client,
		url:       creds.VCenter,
		locations: locations,
		pullMode:  c.PullMode,
	}, nil
}

// GetLocations returns all configured vSphere locations
func (c *Client) GetLocations() map[string]interface{} {
	locations := make(map[string]interface{})
	for k, v := range c.locations {
		locations[k] = v
	}
	return locations
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

// Create imports and processes an OVF image to vSphere
func (c *Client) Create(ctx context.Context, imageURL string, imageName string, loc string) error {
	object, err := c.importImage(ctx, imageURL, imageName, loc)
	if err != nil {
		return fmt.Errorf("failed to import OVA: %w", err)
	}
	return c.processImage(ctx, *object)
}

// Process processes the OVF image
func (c *Client) processImage(ctx context.Context, ref types.ManagedObjectReference) error {
	log := log.FromContext(ctx)
	vm := object.NewVirtualMachine(c.vsphere.Client, ref)

	err := vm.MarkAsTemplate(ctx)
	if err != nil {
		return fmt.Errorf("failed to mark vm as template: %w", err)
	}

	log.Info("Processed vm", "vm", vm.Name())
	return nil
}

// getDatacenter returns the datacenter object
func (c *Client) getDatacenter(ctx context.Context, finder *find.Finder, loc string) (*object.Datacenter, error) {
	dc, err := finder.DatacenterOrDefault(ctx, c.locations[loc].Datacenter)
	if err != nil {
		return nil, fmt.Errorf("failed to find datacenter %s:\n%w", c.locations[loc].Datacenter, err)
	}
	return dc, nil
}

// getDatastore returns the datastore object
func (c *Client) getDatastore(ctx context.Context, finder *find.Finder, loc string) (*object.Datastore, error) {
	datastore, err := finder.DatastoreOrDefault(ctx, c.locations[loc].Datastore)
	if err != nil {
		return nil, fmt.Errorf("failed to find datastore %s: %w", c.locations[loc].Datastore, err)
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
	log := log.FromContext(ctx)
	var host *object.HostSystem
	var err error
	if hostName != "" {
		host, err = finder.HostSystemOrDefault(ctx, hostName)
		if err != nil {
			return nil, fmt.Errorf("failed to find host %s: %w", hostName, err)
		}
		// Validate the specified host is in a usable state
		if err := c.validateHostState(ctx, host); err != nil {
			return nil, fmt.Errorf("host %s is not in a usable state: %w", hostName, err)
		}
	} else {
		hosts, err := finder.HostSystemList(ctx, "*") // Get all hosts
		if err != nil {
			return nil, fmt.Errorf("failed to list hosts: %w", err)
		}
		if len(hosts) == 0 {
			return nil, fmt.Errorf("no hosts found in vSphere")
		}
		// Filter hosts to find one that's in a usable state
		host, err = c.findUsableHost(ctx, hosts)
		if err != nil {
			return nil, err
		}
	}
	log.Info("Using host for import", "host", host.Name())
	return host, nil
}

// validateHostState checks if a host is in a usable state for VM operations
func (c *Client) validateHostState(ctx context.Context, host *object.HostSystem) error {
	var hs mo.HostSystem
	err := host.Properties(ctx, host.Reference(), []string{"runtime"}, &hs)
	if err != nil {
		return fmt.Errorf("failed to get host runtime info: %w", err)
	}

	// Check connection state
	if hs.Runtime.ConnectionState != types.HostSystemConnectionStateConnected {
		return fmt.Errorf("host connection state is %s (expected connected)", hs.Runtime.ConnectionState)
	}

	// Check if host is in maintenance mode
	if hs.Runtime.InMaintenanceMode {
		return fmt.Errorf("host is in maintenance mode")
	}

	// Check power state
	if hs.Runtime.PowerState != types.HostSystemPowerStatePoweredOn {
		return fmt.Errorf("host power state is %s (expected poweredOn)", hs.Runtime.PowerState)
	}

	return nil
}

// findUsableHost finds the first host from the list that's in a usable state
func (c *Client) findUsableHost(ctx context.Context, hosts []*object.HostSystem) (*object.HostSystem, error) {
	log := log.FromContext(ctx)

	for _, host := range hosts {
		if err := c.validateHostState(ctx, host); err != nil {
			log.Info("Skipping host due to unusable state", "host", host.Name(), "reason", err.Error())
			continue
		}
		return host, nil
	}

	return nil, fmt.Errorf(
		"no usable hosts found - all hosts are either disconnected, in maintenance mode, or powered off",
	)
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
	return fmt.Sprintf("%s/%s", c.locations[loc].Folder, name)
}

func loadLocations(path string) (map[string]*Location, error) {
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

func loadCredentials(path string) (*Credentials, error) {
	file, err := os.ReadFile(path) // nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file:\n%w", err)
	}

	var creds Credentials

	if err := yaml.Unmarshal(file, &creds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal credentials file:\n%w", err)
	}

	return &creds, nil
}

package clouddirector

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/vmware/go-vcloud-director/v3/govcd"
	"gopkg.in/yaml.v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Client wraps the govcd client
type Client struct {
	cloudDirector *govcd.VCDClient
	url           string
	location      *Location
}

type Credentials struct {
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Org      string `yaml:"org"`
	Insecure bool   `yaml:"insecure"`
}

// Location holds a single location configuration
type Location struct {
	Name    string `yaml:"name"`
	Org     string `yaml:"org"`
	VDC     string `yaml:"vdc"`
	Catalog string `yaml:"catalog"`
}

// Config holds the configuration for the cloudDirector client
type Config struct {
	CredentialsFile string
	LocationsFile   string
	PullMode        bool
}

// ImporterConfig holds the configuration for the OVF importer
type ImporterConfig struct {
	Name string
	Path string
}

// New initializes a new cloudDirector client
func New(c Config, ctx context.Context) (*Client, error) {
	log := log.FromContext(ctx)

	creds, err := loadCredentials(c.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load credentials:\n%w", err)
	}

	log.Info("Connecting to Cloud Director", "vcdURL", creds.URL)

	u, err := url.ParseRequestURI(creds.URL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse URL: %w", err)
	}

	vcdClient := govcd.NewVCDClient(*u, creds.Insecure)
	err = vcdClient.Authenticate(creds.Username, creds.Password, creds.Org)
	if err != nil {
		return nil, fmt.Errorf("unable to authenticate: %w", err)
	}

	log.Info("Successfully connected to Cloud Director", "vcdURL", creds.URL)

	location, err := loadLocation(c.LocationsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load locations file:\n%w", err)
	}

	location.Org = creds.Org
	return &Client{
		cloudDirector: vcdClient,
		url:           creds.URL,
		location:      location,
	}, nil
}

// GetLocations returns all configured cloudDirector locations
func (c *Client) GetLocations() map[string]interface{} {
	locations := make(map[string]interface{})
	locations[c.location.Name] = c.location
	return locations
}

// Exists checks if an image already exists in cloudDirector
func (c *Client) Exists(ctx context.Context, name string, loc string) (bool, error) {
	log := log.FromContext(ctx)

	catalog, err := c.getCatalog(ctx)
	if err != nil {
		return false, err
	}

	// Check if the vApp template exists in the catalog
	_, err = catalog.GetVAppTemplateByName(name)
	if err != nil {
		if govcd.ContainsNotFound(err) {
			log.Info("vApp template not found in catalog", "name", name, "catalog", c.location.Catalog)
			return false, nil
		}
		return false, fmt.Errorf("failed to check for vApp template %s: %w", name, err)
	}

	log.Info("vApp template exists in catalog", "name", name, "catalog", c.location.Catalog)
	return true, nil
}

// Delete deletes an image from cloudDirector
func (c *Client) Delete(ctx context.Context, name string, loc string) error {
	return nil
}

// Create imports and processes an OVF image to cloudDirector
func (c *Client) Create(ctx context.Context, imageURL string, imageName string, loc string) error {
	return nil
}

// getOrg returns the organization object
func (c *Client) getOrg(ctx context.Context) (*govcd.Org, error) {
	org, err := c.cloudDirector.GetOrgByName(c.location.Org)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization %s: %w", c.location.Org, err)
	}
	return org, nil
}

// getCatalog returns the catalog object
func (c *Client) getCatalog(ctx context.Context) (*govcd.Catalog, error) {
	org, err := c.getOrg(ctx)
	if err != nil {
		return nil, err
	}

	catalog, err := org.GetCatalogByName(c.location.Catalog, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get catalog %s for organization %s: %w",
			c.location.Catalog, c.location.Org, err)
	}
	return catalog, nil
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

func loadLocation(path string) (*Location, error) {
	file, err := os.ReadFile(path) // nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("failed to read locations file:\n%w", err)
	}

	var location Location

	if err := yaml.Unmarshal(file, &location); err != nil {
		return nil, fmt.Errorf("failed to unmarshal locations file:\n%w", err)
	}
	if location.Name == "" {
		return nil, fmt.Errorf("location name is required")
	}
	if location.VDC == "" {
		return nil, fmt.Errorf("location VDC is required")
	}
	if location.Catalog == "" {
		return nil, fmt.Errorf("location Catalog is required")
	}

	return &location, nil
}

package clouddirector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/vmware/go-vcloud-director/v3/govcd"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// sessionRefreshThreshold is kept comfortably under Cloud Director's observed
// ~24h absolute session lifetime, so sessions get refreshed before they can
// expire mid-reconcile.
const sessionRefreshThreshold = 20 * time.Hour

// Client wraps the govcd client
type Client struct {
	cloudDirector   *govcd.VCDClient
	url             string
	location        *Location
	downloadDir     string
	credentials     *Credentials
	backoff         wait.Backoff
	authenticatedAt time.Time
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
	Name            string `yaml:"name"`
	Org             string `yaml:"org"`
	VDC             string `yaml:"vdc"`
	Catalog         string `yaml:"catalog"`
	HardwareVersion int    `yaml:"hardwareVersion"`
}

// Config holds the configuration for the cloudDirector client
type Config struct {
	Backoff         wait.Backoff
	CredentialsFile string
	LocationsFile   string
	DownloadDir     string
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

	location, err := loadLocation(c.LocationsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load locations file:\n%w", err)
	}
	location.Org = creds.Org

	client := &Client{
		cloudDirector: govcd.NewVCDClient(*u, creds.Insecure),
		url:           creds.URL,
		location:      location,
		downloadDir:   c.DownloadDir,
		credentials:   creds,
		backoff:       c.Backoff,
	}

	if err := client.authenticate(ctx); err != nil {
		return nil, fmt.Errorf("failed to create Cloud Director client: %w", err)
	}

	return client, nil
}

// authenticate logs in to Cloud Director, retrying with backoff, and records
// the time of the successful login so ensureSession can tell when a refresh
// is due.
func (c *Client) authenticate(ctx context.Context) error {
	log := log.FromContext(ctx)

	var lastErr error
	err := wait.ExponentialBackoff(c.backoff,
		func() (done bool, err error) {
			lastErr = c.cloudDirector.Authenticate(c.credentials.Username, c.credentials.Password, c.credentials.Org)

			// Return if client was successfully created, otherwise retry
			if lastErr == nil {
				return true, nil
			}

			// Retry on any error
			log.Info("Retrying authentication to VCD")
			return false, nil
		})
	if err != nil {
		return lastErr
	}

	c.authenticatedAt = time.Now()
	log.Info("Successfully authenticated to Cloud Director", "vcdURL", c.url)
	return nil
}

// ensureSession re-authenticates with Cloud Director if the current session
// is old enough that it may have expired, so callers never have to deal with
// a stale session themselves.
func (c *Client) ensureSession(ctx context.Context) error {
	if time.Since(c.authenticatedAt) < sessionRefreshThreshold {
		return nil
	}

	if err := c.authenticate(ctx); err != nil {
		return fmt.Errorf("failed to refresh Cloud Director session: %w", err)
	}
	return nil
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
	log := log.FromContext(ctx)

	catalog, err := c.getCatalog(ctx)
	if err != nil {
		return fmt.Errorf("failed to get catalog: %w", err)
	}

	// Get the vApp template
	vAppTemplate, err := catalog.GetVAppTemplateByName(name)
	if err != nil {
		if govcd.ContainsNotFound(err) {
			log.Info("vApp template not found, nothing to delete", "name", name, "catalog", c.location.Catalog)
			return nil
		}
		return fmt.Errorf("failed to get vApp template %s: %w", name, err)
	}

	log.Info("Deleting vApp template", "name", name, "catalog", c.location.Catalog)

	// Delete the vApp template
	err = vAppTemplate.Delete()
	if err != nil {
		if govcd.ContainsNotFound(err) {
			log.Info("vApp template already deleted or not found", "name", name, "catalog", c.location.Catalog)
			return nil
		}
		return fmt.Errorf("failed to delete vApp template %s: %w", name, err)
	}

	log.Info("Successfully deleted vApp template", "name", name, "catalog", c.location.Catalog)
	return nil
}

// Create imports and processes an OVF image to cloudDirector
func (c *Client) Create(ctx context.Context, imageURL string, imageName string, loc string) error {
	log := log.FromContext(ctx)

	// Get the catalog where we'll upload
	catalog, err := c.getCatalog(ctx)
	if err != nil {
		return fmt.Errorf("failed to get catalog: %w", err)
	}

	// Create import configuration
	importConfig := ImporterConfig{
		Name:            imageName,
		Path:            imageURL,
		Catalog:         catalog,
		HardwareVersion: c.location.HardwareVersion,
	}

	log.Info("Starting image import", "name", imageName, "url", imageURL)

	// Import the image (waits for completion internally)
	err = c.importImage(ctx, importConfig)
	if err != nil {
		return fmt.Errorf("failed to import image: %w", err)
	}

	log.Info("Image import completed", "name", imageName)
	return nil
}

// getOrg returns the organization object. go-vcloud-director reports an
// expired session as ErrorEntityNotFound here - indistinguishable from the
// org actually being missing - so on that specific error it forces a
// re-authentication and retries once before giving up.
func (c *Client) getOrg(ctx context.Context) (*govcd.Org, error) {
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}

	org, err := c.cloudDirector.GetOrgByName(c.location.Org)
	if errors.Is(err, govcd.ErrorEntityNotFound) {
		if reauthErr := c.authenticate(ctx); reauthErr == nil {
			org, err = c.cloudDirector.GetOrgByName(c.location.Org)
		}
	}
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

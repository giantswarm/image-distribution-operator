package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Client wraps the Proxmox REST API client
type Client struct {
	baseURL    string
	authHeader string
	httpClient *http.Client
	locations  map[string]*Location
}

// Credentials holds the authentication details for the Proxmox API
type Credentials struct {
	URL      string `yaml:"url"`      // e.g. "proxmox.example.com:8006"
	User     string `yaml:"user"`     // e.g. "root"
	Realm    string `yaml:"realm"`    // e.g. "pam" (defaults to "pam")
	TokenID  string `yaml:"tokenId"`  // e.g. "mytoken"
	Secret   string `yaml:"secret"`   // the API token secret
	Insecure bool   `yaml:"insecure"` // skip TLS verification
}

// Location holds a single Proxmox location configuration
type Location struct {
	Node          string `yaml:"node"`          // Proxmox node name (e.g. "pve")
	StoragePool   string `yaml:"storagePool"`   // target storage for VM disk (e.g. "local-lvm")
	ImportStorage string `yaml:"importStorage"` // storage for downloaded images (defaults to "local")
	Bridge        string `yaml:"bridge"`        // network bridge (e.g. "vmbr0")
}

// Config holds the configuration for the Proxmox client
type Config struct {
	CredentialsFile string
	LocationsFile   string
}

// taskResponse represents the Proxmox task status API response
type taskResponse struct {
	Data struct {
		Status     string `json:"status"`
		ExitStatus string `json:"exitstatus"`
	} `json:"data"`
}

// apiResponse represents a generic Proxmox API response with a data field
type apiResponse struct {
	Data json.RawMessage `json:"data"`
}

// resourceItem represents a single resource from the cluster resources API
type resourceItem struct {
	VMID     int    `json:"vmid"`
	Name     string `json:"name"`
	Node     string `json:"node"`
	Template int    `json:"template"`
	Type     string `json:"type"`
}

// New initializes a new Proxmox client
func New(c Config, ctx context.Context) (*Client, error) {
	log := log.FromContext(ctx)

	creds, err := loadCredentials(c.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load credentials:\n%w", err)
	}

	log.Info("Connecting to Proxmox", "url", creds.URL)

	realm := creds.Realm
	if realm == "" {
		realm = "pam"
	}

	authHeader := fmt.Sprintf("PVEAPIToken=%s@%s!%s=%s", creds.User, realm, creds.TokenID, creds.Secret)
	baseURL := fmt.Sprintf("https://%s/api2/json", creds.URL)

	transport := &http.Transport{}
	if creds.Insecure {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 — user-configured for self-signed certs
		}
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	locations, err := loadLocations(c.LocationsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load locations file:\n%w", err)
	}

	client := &Client{
		baseURL:    baseURL,
		authHeader: authHeader,
		httpClient: httpClient,
		locations:  locations,
	}

	// Validate connectivity
	if _, err := client.doRequest(ctx, http.MethodGet, "/version", nil); err != nil {
		return nil, fmt.Errorf("failed to connect to Proxmox API: %w", err)
	}

	log.Info("Successfully connected to Proxmox", "url", creds.URL)

	return client, nil
}

// GetLocations returns all configured Proxmox locations
func (c *Client) GetLocations() map[string]interface{} {
	locations := make(map[string]interface{})
	for k, v := range c.locations {
		locations[k] = v
	}
	return locations
}

// Exists checks if a template with the given name already exists in Proxmox
func (c *Client) Exists(ctx context.Context, name string, loc string) (bool, error) {
	_, _, found, err := c.findVMByName(ctx, name)
	if err != nil {
		return false, fmt.Errorf("failed to check if template exists: %w", err)
	}
	return found, nil
}

// Delete removes a template from Proxmox
func (c *Client) Delete(ctx context.Context, name string, loc string) error {
	log := log.FromContext(ctx)

	vmid, node, found, err := c.findVMByName(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to find template: %w", err)
	}
	if !found {
		log.Info("Template not found, nothing to delete", "name", name)
		return nil
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d", node, vmid)
	params := url.Values{}
	params.Set("purge", "1")

	body, err := c.doRequest(ctx, http.MethodDelete, path+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("failed to delete VM %d: %w", vmid, err)
	}

	upid, err := extractUPID(body)
	if err != nil {
		return fmt.Errorf("failed to extract UPID from delete response: %w", err)
	}

	if err := c.waitForTask(ctx, node, upid); err != nil {
		return fmt.Errorf("delete task failed: %w", err)
	}

	log.Info("Deleted template", "name", name, "vmid", vmid)
	return nil
}

// Create imports a qcow2 image and creates a VM template in Proxmox
func (c *Client) Create(ctx context.Context, imageURL string, imageName string, loc string) error {
	return c.createTemplate(ctx, imageURL, imageName, loc)
}

// doRequest executes an HTTP request against the Proxmox API
func (c *Client) doRequest(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	fullURL := c.baseURL + path

	var bodyReader io.Reader
	if params != nil && (method == http.MethodPost || method == http.MethodPut) {
		bodyReader = strings.NewReader(params.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", c.authHeader)
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// waitForTask polls a Proxmox task until completion
func (c *Client) waitForTask(ctx context.Context, node string, upid string) error {
	log := log.FromContext(ctx)

	encodedUPID := url.PathEscape(upid)
	path := fmt.Sprintf("/nodes/%s/tasks/%s/status", node, encodedUPID)

	backoff := 2 * time.Second
	maxBackoff := 30 * time.Second
	timeout := 30 * time.Minute

	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("task timed out after %v: %s", timeout, upid)
		}

		body, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return fmt.Errorf("failed to poll task status: %w", err)
		}

		var taskResp taskResponse
		if err := json.Unmarshal(body, &taskResp); err != nil {
			return fmt.Errorf("failed to parse task response: %w", err)
		}

		if taskResp.Data.Status == "stopped" {
			if taskResp.Data.ExitStatus != "OK" {
				// Fetch task log for error details
				logPath := fmt.Sprintf("/nodes/%s/tasks/%s/log", node, encodedUPID)
				logBody, logErr := c.doRequest(ctx, http.MethodGet, logPath, nil)
				if logErr == nil {
					log.Info("Task failed", "upid", upid, "exitstatus", taskResp.Data.ExitStatus, "log", string(logBody))
				}
				return fmt.Errorf("task failed with exit status: %s", taskResp.Data.ExitStatus)
			}
			return nil
		}

		log.V(1).Info("Task still running", "upid", upid, "status", taskResp.Data.Status)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff = backoff * 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// getNextVMID retrieves the next available VMID from Proxmox
func (c *Client) getNextVMID(ctx context.Context) (int, error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/cluster/nextid", nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get next VMID: %w", err)
	}

	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("failed to parse next VMID response: %w", err)
	}

	// The data field is a string containing the VMID number
	var vmidStr string
	if err := json.Unmarshal(resp.Data, &vmidStr); err != nil {
		// Try as integer directly
		var vmid int
		if err := json.Unmarshal(resp.Data, &vmid); err != nil {
			return 0, fmt.Errorf("failed to parse VMID from response: %s", string(resp.Data))
		}
		return vmid, nil
	}

	var vmid int
	if _, err := fmt.Sscanf(vmidStr, "%d", &vmid); err != nil {
		return 0, fmt.Errorf("failed to parse VMID string %q: %w", vmidStr, err)
	}

	return vmid, nil
}

// findVMByName searches for a VM/template by name across the cluster
func (c *Client) findVMByName(ctx context.Context, name string) (vmid int, node string, found bool, err error) {
	body, err := c.doRequest(ctx, http.MethodGet, "/cluster/resources?type=vm", nil)
	if err != nil {
		return 0, "", false, fmt.Errorf("failed to list cluster resources: %w", err)
	}

	var resp struct {
		Data []resourceItem `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, "", false, fmt.Errorf("failed to parse cluster resources: %w", err)
	}

	for _, item := range resp.Data {
		if item.Name == name {
			return item.VMID, item.Node, true, nil
		}
	}

	return 0, "", false, nil
}

// extractUPID extracts the UPID string from a Proxmox API response
func extractUPID(body []byte) (string, error) {
	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	var upid string
	if err := json.Unmarshal(resp.Data, &upid); err != nil {
		return "", fmt.Errorf("failed to parse UPID from response: %s", string(resp.Data))
	}

	return upid, nil
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

	if creds.URL == "" {
		return nil, fmt.Errorf("url is required in credentials")
	}
	if creds.User == "" {
		return nil, fmt.Errorf("user is required in credentials")
	}
	if creds.TokenID == "" {
		return nil, fmt.Errorf("tokenId is required in credentials")
	}
	if creds.Secret == "" {
		return nil, fmt.Errorf("secret is required in credentials")
	}

	return &creds, nil
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
		if v.Node == "" {
			return nil, fmt.Errorf("node is required for location %s", k)
		}
		if v.StoragePool == "" {
			return nil, fmt.Errorf("storagePool is required for location %s", k)
		}
		if v.Bridge == "" {
			return nil, fmt.Errorf("bridge is required for location %s", k)
		}
		if v.ImportStorage == "" {
			locations[k].ImportStorage = "local"
		}
	}

	return locations, nil
}

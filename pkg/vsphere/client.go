package vsphere

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
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

// UploadImage imports an OVA to vSphere using a task
func (c *Client) UploadImage(ctx context.Context, imageURL string, expectedSHA256 string, imageName string) error {
	log := log.FromContext(ctx)

	log.Info("Starting OVA upload to vSphere", "imageURL", imageURL, "imageName", imageName)

	// Stream the OVA while verifying SHA256
	ovaReader, err := downloadAndVerifyOVA(ctx, imageURL, expectedSHA256)
	if err != nil {
		return fmt.Errorf("failed to download and verify OVA.\n%w", err)
	}
	defer ovaReader.Close()

	// Find the datacenter
	finder := find.NewFinder(c.vsphere.Client, true)
	dc, err := finder.DatacenterOrDefault(ctx, c.datacenter)
	if err != nil {
		return fmt.Errorf("failed to find datacenter %s.\n%w", c.datacenter, err)
	}
	finder.SetDatacenter(dc)

	// Find the datastore
	datastore, err := finder.DatastoreOrDefault(ctx, c.datastore)
	if err != nil {
		return fmt.Errorf("failed to find datastore %s.\n%w", c.datastore, err)
	}

	// Use OvfManager to prepare import
	ovfManager := ovf.NewManager(c.vsphere.Client)

	// Generate ImportSpec for OVA
	specParams := types.OvfCreateImportSpecParams{
		DiskProvisioning: "thin",
		EntityName:       imageName,
	}

	importSpec, err := ovfManager.CreateImportSpec(ctx, ovaReader, dc, datastore, specParams)
	if err != nil {
		return fmt.Errorf("failed to create import spec.\n%w", err)
	}
	if importSpec.Error != nil {
		return fmt.Errorf("import spec contains errors: %+v", importSpec.Error)
	}

	// Find resource pool and folder for VM placement
	rp, err := finder.DefaultResourcePool(ctx)
	if err != nil {
		return fmt.Errorf("failed to find resource pool.\n%w", err)
	}

	folder, err := finder.DefaultFolder(ctx)
	if err != nil {
		return fmt.Errorf("failed to find default VM folder.\n%w", err)
	}

	// Create an Import Task
	lease, err := rp.ImportVApp(ctx, importSpec.ImportSpec, folder, nil)
	if err != nil {
		return fmt.Errorf("failed to start OVA import task.\n%w", err)
	}

	// Monitor Task Progress
	log.Info("Started OVA import task", "leaseID", lease.Reference().Value)
	err = waitForLeaseProgress(ctx, log, lease)
	if err != nil {
		return fmt.Errorf("OVA import task failed.\n%w", err)
	}

	log.Info("OVA upload and import completed successfully", "imageName", imageName)
	return nil
}

// waitForLeaseProgress tracks the task progress
func waitForLeaseProgress(ctx context.Context, lease *object.HttpNfcLease) error {
	log := log.FromContext(ctx)
	for {
		info, err := lease.Wait(ctx, nil)
		if err != nil {
			return err
		}

		if info.State == types.HttpNfcLeaseStateDone {
			log.Info("OVA upload completed")
			return nil
		}

		if info.State == types.HttpNfcLeaseStateError {
			return fmt.Errorf("OVA import task failed: %+v", info.Error)
		}

		log.Info("Upload in progress", "progress", info.Progress)
	}
}

// downloadAndVerifyOVA streams the OVA while computing SHA256
func downloadAndVerifyOVA(ctx context.Context, imageURL string, expectedSHA256 string) (io.ReadCloser, error) {
	resp, err := http.Get(imageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download OVA.\n%w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("failed to download OVA, HTTP status: %d", resp.StatusCode)
	}

	// Create SHA256 hasher
	hasher := sha256.New()

	// Use TeeReader to calculate hash while streaming
	reader := io.TeeReader(resp.Body, hasher)

	// Pass the reader to the vSphere upload function while verifying
	return &verifiedReader{
		reader:      reader,
		closer:      resp.Body,
		hasher:      hasher,
		expectedSHA: expectedSHA256,
	}, nil
}

// verifiedReader wraps the streaming OVA download and validates SHA256 at Close()
type verifiedReader struct {
	reader      io.Reader
	closer      io.Closer
	hasher      hash.Hash
	expectedSHA string
}

func (v *verifiedReader) Read(p []byte) (int, error) {
	return v.reader.Read(p) // Stream data while hashing
}

func (v *verifiedReader) Close() error {
	defer v.closer.Close()

	// Compute SHA256 after reading completes
	computedSHA := hex.EncodeToString(v.hasher.Sum(nil))
	if computedSHA != v.expectedSHA {
		return fmt.Errorf("SHA256 mismatch: expected %s, got %s", v.expectedSHA, computedSHA)
	}
	return nil
}

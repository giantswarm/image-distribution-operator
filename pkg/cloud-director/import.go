package clouddirector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"

	"github.com/vmware/go-vcloud-director/v3/govcd"
	"github.com/vmware/go-vcloud-director/v3/types/v56"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ImporterConfig holds the configuration for the OVF importer
type ImporterConfig struct {
	Name    string
	Path    string
	Catalog *govcd.Catalog
}

// importImage handles the actual import using push mode and waits for completion
func (c *Client) importImage(ctx context.Context, config ImporterConfig) error {
	return c.pushImport(ctx, config)
}

// pushImport uses push-based upload (operator downloads then uploads)
func (c *Client) pushImport(ctx context.Context, config ImporterConfig) error {
	log := log.FromContext(ctx)

	// Download the OVA file to local filesystem
	localPath, err := c.downloadImage(ctx, config.Path)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(localPath); removeErr != nil {
			log.Info("Failed to cleanup temp file", "path", localPath, "error", removeErr)
		}
	}() // Cleanup after upload

	log.Info("Starting upload to cloud director", "localPath", localPath)

	// Upload to cloud director
	uploadTask, err := config.Catalog.UploadOvf(
		localPath,   // ovaFileName - local file path
		config.Name, // itemName
		fmt.Sprintf("Node image %s", config.Name), // description
		1024*1024*10, // uploadPieceSize - 10MB chunks
	)
	if err != nil {
		return fmt.Errorf("failed to start push upload: %w", err)
	}

	log.Info("Push upload started, waiting for completion", "name", config.Name)

	// Wait for upload task completion - UploadTask must be waited on directly
	// to ensure proper upload error handling
	err = uploadTask.WaitTaskCompletion()
	if err != nil {
		// Check if there was an upload error
		if uploadErr := uploadTask.GetUploadError(); uploadErr != nil {
			return fmt.Errorf("upload failed: %w", uploadErr)
		}
		return fmt.Errorf("task completion failed: %w", err)
	}

	log.Info("Push upload completed successfully", "name", config.Name)

	// Set VM hardware version to vmx-19
	if err := c.setHardwareVersion(ctx, config); err != nil {
		return fmt.Errorf("failed to set hardware version: %w", err)
	}

	return nil
}

// setHardwareVersion updates the VM hardware version on the uploaded vApp template
// by PUTting a modified virtualHardwareSection directly on the template child VM.
// This avoids using /action/reconfigureVm which is not available on template VMs.
func (c *Client) setHardwareVersion(ctx context.Context, config ImporterConfig) error {
	log := log.FromContext(ctx)

	vAppTemplate, err := config.Catalog.GetVAppTemplateByName(config.Name)
	if err != nil {
		return fmt.Errorf("failed to get vApp template %s: %w", config.Name, err)
	}

	if vAppTemplate.VAppTemplate.Children == nil || len(vAppTemplate.VAppTemplate.Children.VM) == 0 {
		return fmt.Errorf("vApp template %s has no child VMs", config.Name)
	}

	vmHref := vAppTemplate.VAppTemplate.Children.VM[0].HREF
	hwSectionURL := vmHref + "/virtualHardwareSection/"

	// GET the current virtualHardwareSection
	current := &types.ResponseVirtualHardwareSection{}
	_, err = c.cloudDirector.Client.ExecuteRequest(
		hwSectionURL, http.MethodGet,
		types.MimeVirtualHardwareSection,
		"failed to get virtualHardwareSection: %s",
		nil, current,
	)
	if err != nil {
		return fmt.Errorf("failed to get virtualHardwareSection: %w", err)
	}

	// Patch the System element to set vmx-19
	hwVersionRe := regexp.MustCompile(`<vssd:VirtualSystemType>[^<]+</vssd:VirtualSystemType>`)
	patchedSystem := make([]types.InnerXML, len(current.System))
	for i, s := range current.System {
		patchedSystem[i] = types.InnerXML{
			Text: hwVersionRe.ReplaceAllString(s.Text, "<vssd:VirtualSystemType>vmx-19</vssd:VirtualSystemType>"),
		}
	}

	// PUT the updated section back
	payload := &types.RequestVirtualHardwareSection{
		Info:   "Virtual hardware requirements",
		Ovf:    types.XMLNamespaceOVF,
		Rasd:   types.XMLNamespaceRASD,
		Vssd:   types.XMLNamespaceVSSD,
		Ns2:    types.XMLNamespaceVCloud,
		Ns3:    types.XMLNamespaceVCloud,
		Ns4:    types.XMLNamespaceVCloud,
		Ns5:    types.XMLNamespaceVCloud,
		Vmw:    types.XMLNamespaceVMW,
		Xmlns:  types.XMLNamespaceVCloud,
		Type:   current.Type,
		HREF:   hwSectionURL,
		System: patchedSystem,
		Item:   current.Item,
	}

	task, err := c.cloudDirector.Client.ExecuteTaskRequest(
		hwSectionURL, http.MethodPut,
		types.MimeVirtualHardwareSection,
		"failed to set virtualHardwareSection: %s",
		payload,
	)
	if err != nil {
		return fmt.Errorf("failed to update virtualHardwareSection: %w", err)
	}

	if err = task.WaitTaskCompletion(); err != nil {
		return fmt.Errorf("virtualHardwareSection update task failed: %w", err)
	}

	log.Info("Hardware version set to vmx-19", "name", config.Name)
	return nil
}

// downloadImage downloads OVA from S3 to local temp file
func (c *Client) downloadImage(ctx context.Context, imageURL string) (string, error) {
	log := log.FromContext(ctx)

	// Ensure download directory exists
	if err := os.MkdirAll(c.downloadDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create download directory %s: %w", c.downloadDir, err)
	}

	// Create temp file in the configured download directory
	tmpFile, err := os.CreateTemp(c.downloadDir, "vcd-image-*.ova")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() { _ = tmpFile.Close() }()

	// Download from URL
	log.Info("Downloading image", "url", imageURL, "dest", tmpFile.Name())

	resp, err := http.Get(imageURL) // #nosec G107 - URL is from trusted source (Release CR)
	if err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// Copy to file
	written, err := io.Copy(tmpFile, resp.Body)
	if err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	log.Info("Downloaded image", "bytes", written, "path", tmpFile.Name())
	return tmpFile.Name(), nil
}

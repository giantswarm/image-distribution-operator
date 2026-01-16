package clouddirector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/vmware/go-vcloud-director/v3/govcd"
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

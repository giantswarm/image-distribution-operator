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

// importImage handles the actual import (pull vs push)
func (c *Client) importImage(ctx context.Context, config ImporterConfig) (govcd.Task, error) {
	log := log.FromContext(ctx)

	if c.pullMode {
		log.Info("Pull mode enabled", "url", config.Path)
		return c.pullImport(ctx, config)
	}
	return c.pushImport(ctx, config)
}

// pullImport uses cloud director's pull-based import (cloud director fetches from URL)
func (c *Client) pullImport(ctx context.Context, config ImporterConfig) (govcd.Task, error) {
	log := log.FromContext(ctx)

	// cloud director pulls directly from the URL
	task, err := config.Catalog.UploadOvfByLink(
		config.Path, // ovfUrl - the S3 URL
		config.Name, // itemName
		fmt.Sprintf("Node image %s", config.Name), // description
	)
	if err != nil {
		return govcd.Task{}, fmt.Errorf("failed to start pull import: %w", err)
	}

	log.Info("Pull import started", "name", config.Name, "taskHREF", task.Task.HREF)
	return task, nil
}

// pushImport uses push-based upload (operator downloads then uploads)
func (c *Client) pushImport(ctx context.Context, config ImporterConfig) (govcd.Task, error) {
	log := log.FromContext(ctx)

	// Download the OVA file to local filesystem
	localPath, err := c.downloadImage(ctx, config.Path)
	if err != nil {
		return govcd.Task{}, fmt.Errorf("failed to download image: %w", err)
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
		return govcd.Task{}, fmt.Errorf("failed to start push upload: %w", err)
	}

	log.Info("Push upload started", "name", config.Name)

	// UploadTask embeds Task, so we can return it directly
	return govcd.Task{Task: uploadTask.Task.Task}, nil
}

// downloadImage downloads OVA from S3 to local temp file
func (c *Client) downloadImage(ctx context.Context, imageURL string) (string, error) {
	log := log.FromContext(ctx)

	// Create temp file
	tmpFile, err := os.CreateTemp("", "vcd-image-*.ova")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer tmpFile.Close()

	// Download from URL
	log.Info("Downloading image", "url", imageURL, "dest", tmpFile.Name())

	resp, err := http.Get(imageURL) // #nosec G107 - URL is from trusted source (Release CR)
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// Copy to file
	written, err := io.Copy(tmpFile, resp.Body)
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	log.Info("Downloaded image", "bytes", written, "path", tmpFile.Name())
	return tmpFile.Name(), nil
}

// waitForTask waits for cloud director task completion
func (c *Client) waitForTask(ctx context.Context, task govcd.Task) error {
	log := log.FromContext(ctx)

	log.Info("Waiting for task completion", "taskHREF", task.Task.HREF)

	err := task.WaitTaskCompletion()
	if err != nil {
		return fmt.Errorf("task failed: %w", err)
	}

	log.Info("Task completed successfully")
	return nil
}

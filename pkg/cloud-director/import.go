package clouddirector

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

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

	// Patch the OVF descriptor in the OVA to set vmx-19
	patchedPath, err := patchOVAHardwareVersion(localPath, c.downloadDir)
	if err != nil {
		return fmt.Errorf("failed to patch OVA hardware version: %w", err)
	}
	if patchedPath != localPath {
		defer func() {
			if removeErr := os.Remove(patchedPath); removeErr != nil {
				log.Info("Failed to cleanup patched OVA", "path", patchedPath, "error", removeErr)
			}
		}()
		localPath = patchedPath
	}

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

// hwVersionRe matches the VirtualSystemType element in an OVF descriptor
var hwVersionRe = regexp.MustCompile(`(?i)<vssd:VirtualSystemType>[^<]*</vssd:VirtualSystemType>`)

// patchOVAHardwareVersion rewrites the OVF descriptor inside the OVA tarball,
// replacing the VirtualSystemType with vmx-19. Returns the path to the patched
// OVA (a new temp file) or the original path unchanged if no OVF was found.
func patchOVAHardwareVersion(ovaPath, dir string) (string, error) {
	in, err := os.Open(ovaPath) // #nosec G304
	if err != nil {
		return "", fmt.Errorf("open OVA: %w", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.CreateTemp(dir, "vcd-patched-*.ova")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	outPath := out.Name()

	patched, err := rewriteOVA(in, out)
	_ = out.Close()
	if err != nil {
		_ = os.Remove(outPath)
		return "", err
	}
	if !patched {
		_ = os.Remove(outPath)
		return ovaPath, nil // nothing to patch, use original
	}
	return outPath, nil
}

// rewriteOVA copies the tar from r to w, patching any .ovf entry it finds.
// Returns true if an OVF entry was found and patched.
func rewriteOVA(r io.Reader, w io.Writer) (bool, error) {
	tr := tar.NewReader(r)
	tw := tar.NewWriter(w)
	defer func() { _ = tw.Close() }()

	patched := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, fmt.Errorf("read OVA tar: %w", err)
		}

		if strings.HasSuffix(strings.ToLower(hdr.Name), ".ovf") {
			data, err := io.ReadAll(tr)
			if err != nil {
				return false, fmt.Errorf("read OVF entry: %w", err)
			}
			patchedData := hwVersionRe.ReplaceAll(data, []byte("<vssd:VirtualSystemType>vmx-19</vssd:VirtualSystemType>"))
			hdr.Size = int64(len(patchedData))
			if err := tw.WriteHeader(hdr); err != nil {
				return false, fmt.Errorf("write OVF header: %w", err)
			}
			if _, err := tw.Write(patchedData); err != nil {
				return false, fmt.Errorf("write OVF data: %w", err)
			}
			patched = true
		} else {
			if err := tw.WriteHeader(hdr); err != nil {
				return false, fmt.Errorf("write tar header: %w", err)
			}
			if _, err := io.Copy(tw, tr); err != nil {
				return false, fmt.Errorf("copy tar entry: %w", err)
			}
		}
	}
	return patched, nil
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

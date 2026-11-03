package clouddirector

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vmware/go-vcloud-director/v3/govcd"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// maxOVFSize bounds the OVF descriptor, which is read into memory to be patched.
const maxOVFSize = 16 * 1024 * 1024

// xmlDeclaration is added to OVFs lacking one, as govcd only treats text/xml files as OVF.
const xmlDeclaration = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

// ImporterConfig holds the configuration for the OVF importer
type ImporterConfig struct {
	Name            string
	Path            string
	Catalog         *govcd.Catalog
	HardwareVersion int // e.g. 19 → "vmx-19"; 0 means no patching
}

// importImage handles the actual import using push mode and waits for completion
func (c *Client) importImage(ctx context.Context, config ImporterConfig) error {
	return c.pushImport(ctx, config)
}

// pushImport extracts the OVA while downloading and uploads the extracted OVF, keeping one copy on disk.
func (c *Client) pushImport(ctx context.Context, config ImporterConfig) error {
	log := log.FromContext(ctx)

	dir, ovfPath, err := c.downloadAndExtract(ctx, config.Path, hardwareVersionString(config.HardwareVersion))
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}
	// Cleanup after upload
	defer func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			log.Info("Failed to cleanup temp directory", "path", dir, "error", removeErr)
		}
	}()

	log.Info("Starting upload to cloud director", "ovfPath", ovfPath)

	// Upload to cloud director
	uploadTask, err := config.Catalog.UploadOvf(
		ovfPath,     // ovaFileName - path to the extracted OVF descriptor
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

// hardwareVersionString returns e.g. "vmx-19" for 19, or "" (no patching) for 0.
func hardwareVersionString(hardwareVersion int) string {
	if hardwareVersion == 0 {
		return ""
	}
	return fmt.Sprintf("vmx-%d", hardwareVersion)
}

// hwVersionRe matches the VirtualSystemType element in an OVF descriptor
var hwVersionRe = regexp.MustCompile(`(?i)<vssd:VirtualSystemType>[^<]*</vssd:VirtualSystemType>`)

// downloadAndExtract extracts the OVA at imageURL into a new temp dir, which the caller must remove, and returns it and the OVF path.
func (c *Client) downloadAndExtract(ctx context.Context, imageURL, hardwareVersion string) (string, string, error) {
	log := log.FromContext(ctx)

	// Ensure download directory exists
	if err := os.MkdirAll(c.downloadDir, 0750); err != nil {
		return "", "", fmt.Errorf("failed to create download directory %s: %w", c.downloadDir, err)
	}

	dir, err := os.MkdirTemp(c.downloadDir, "vcd-image-*")
	if err != nil {
		return "", "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	ovfPath, err := func() (string, error) {
		log.Info("Downloading and extracting image", "url", imageURL, "dest", dir)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("failed to download: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("download failed with status: %d", resp.StatusCode)
		}

		return extractOVA(resp.Body, dir, hardwareVersion)
	}()
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}

	log.Info("Downloaded and extracted image", "dir", dir, "ovfPath", ovfPath)
	return dir, ovfPath, nil
}

// extractOVA extracts the OVA tar stream r into dir, patching the OVF, and returns the OVF path.
func extractOVA(r io.Reader, dir, hardwareVersion string) (string, error) {
	tr := tar.NewReader(r)
	ovfPath := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read OVA tar: %w", err)
		}

		if !filepath.IsLocal(hdr.Name) {
			return "", fmt.Errorf("invalid path %q in OVA", hdr.Name)
		}
		target := filepath.Join(dir, hdr.Name) // #nosec G305 - validated with filepath.IsLocal above

		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader:
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0750); err != nil {
				return "", fmt.Errorf("create directory %s: %w", hdr.Name, err)
			}
			continue
		case tar.TypeReg:
		default:
			return "", fmt.Errorf("unsupported entry %q of type %q in OVA", hdr.Name, hdr.Typeflag)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
			return "", fmt.Errorf("create directory for %s: %w", hdr.Name, err)
		}

		if strings.HasSuffix(strings.ToLower(hdr.Name), ".ovf") {
			if ovfPath != "" {
				return "", fmt.Errorf("OVA contains more than one OVF descriptor")
			}
			if hdr.Size > maxOVFSize {
				return "", fmt.Errorf("OVF descriptor %s is too large: %d bytes", hdr.Name, hdr.Size)
			}
			data, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
			if err != nil {
				return "", fmt.Errorf("read OVF entry: %w", err)
			}
			patched := patchOVF(data, hardwareVersion)
			if err := writeFile(target, bytes.NewReader(patched), int64(len(patched))); err != nil {
				return "", err
			}
			ovfPath = target
			continue
		}

		if err := writeFile(target, tr, hdr.Size); err != nil {
			return "", err
		}
	}

	if ovfPath == "" {
		return "", errors.New("OVA does not contain an OVF descriptor")
	}
	return ovfPath, nil
}

// patchOVF sets the hardware version ("" means no patching) and ensures an XML declaration.
func patchOVF(data []byte, hardwareVersion string) []byte {
	if hardwareVersion != "" {
		replacement := []byte("<vssd:VirtualSystemType>" + hardwareVersion + "</vssd:VirtualSystemType>")
		data = hwVersionRe.ReplaceAll(data, replacement)
	}

	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !bytes.HasPrefix(bytes.TrimLeft(data, "\t\n\x0c\r "), []byte("<?xml")) {
		data = append([]byte(xmlDeclaration), data...)
	}
	return data
}

// writeFile creates a new file at path with exactly size bytes from r.
func writeFile(path string, r io.Reader, size int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) // #nosec G304 - inside extraction dir
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	_, err = io.CopyN(f, r, size)
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", filepath.Base(path), closeErr)
	}
	return nil
}

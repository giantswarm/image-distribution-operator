package proxmox

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// createTemplate orchestrates the full Proxmox template creation procedure:
// 1. Download qcow2 from S3 to Proxmox import storage
// 2. Create an empty VM
// 3. Import the disk into the VM
// 4. Set boot order
// 5. Convert to template
// 6. Set tags
// 7. Clean up the import file
func (c *Client) createTemplate(ctx context.Context, imageURL string, imageName string, loc string) error {
	log := log.FromContext(ctx)

	location := c.locations[loc]
	node := location.Node
	importStorage := location.ImportStorage
	storagePool := location.StoragePool
	bridge := location.Bridge

	filename := imageName + ".qcow2"

	var vmid int
	var vmCreated bool
	var imageDownloaded bool

	// Step 1: Download the qcow2 from S3 to Proxmox import storage
	log.Info("Downloading image to Proxmox import storage", "url", imageURL, "filename", filename, "node", node)
	{
		params := url.Values{}
		params.Set("content", "import")
		params.Set("filename", filename)
		params.Set("url", imageURL)

		path := fmt.Sprintf("/nodes/%s/storage/%s/download-url", node, importStorage)
		body, err := c.doRequest(ctx, http.MethodPost, path, params)
		if err != nil {
			return fmt.Errorf("failed to start image download: %w", err)
		}

		upid, err := extractUPID(body)
		if err != nil {
			return fmt.Errorf("failed to extract UPID from download response: %w", err)
		}

		if err := c.waitForTask(ctx, node, upid); err != nil {
			return fmt.Errorf("image download failed: %w", err)
		}
		imageDownloaded = true
	}
	log.Info("Image downloaded to import storage", "filename", filename)

	// Step 2: Get next available VMID
	vmid, err := c.getNextVMID(ctx)
	if err != nil {
		c.cleanup(ctx, node, 0, filename, importStorage)
		return fmt.Errorf("failed to get next VMID: %w", err)
	}
	log.Info("Allocated VMID", "vmid", vmid)

	// Step 3: Create an empty VM
	{
		params := url.Values{}
		params.Set("vmid", fmt.Sprintf("%d", vmid))
		params.Set("name", imageName)
		params.Set("memory", "2048")
		params.Set("cores", "2")
		params.Set("scsihw", "virtio-scsi-pci")
		params.Set("bios", "seabios")
		params.Set("agent", "1")
		params.Set("net0", fmt.Sprintf("e1000,bridge=%s", bridge))

		path := fmt.Sprintf("/nodes/%s/qemu", node)
		body, err := c.doRequest(ctx, http.MethodPost, path, params)
		if err != nil {
			c.cleanup(ctx, node, 0, filename, importStorage)
			return fmt.Errorf("failed to create VM: %w", err)
		}

		upid, err := extractUPID(body)
		if err != nil {
			c.cleanup(ctx, node, 0, filename, importStorage)
			return fmt.Errorf("failed to extract UPID from VM creation response: %w", err)
		}

		if err := c.waitForTask(ctx, node, upid); err != nil {
			c.cleanup(ctx, node, 0, filename, importStorage)
			return fmt.Errorf("VM creation failed: %w", err)
		}
		vmCreated = true
	}
	log.Info("Created empty VM", "vmid", vmid, "name", imageName)

	// Step 4: Import the disk into the VM
	// Important: Must use POST not PUT — POST runs as a background task which is required for disk import
	{
		params := url.Values{}
		params.Set("scsi0", fmt.Sprintf("%s:0,import-from=%s:import/%s", storagePool, importStorage, filename))

		path := fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)
		body, err := c.doRequest(ctx, http.MethodPost, path, params)
		if err != nil {
			c.cleanup(ctx, node, vmid, filename, importStorage)
			return fmt.Errorf("failed to start disk import: %w", err)
		}

		upid, err := extractUPID(body)
		if err != nil {
			c.cleanup(ctx, node, vmid, filename, importStorage)
			return fmt.Errorf("failed to extract UPID from disk import response: %w", err)
		}

		if err := c.waitForTask(ctx, node, upid); err != nil {
			c.cleanup(ctx, node, vmid, filename, importStorage)
			return fmt.Errorf("disk import failed: %w", err)
		}
	}
	log.Info("Imported disk into VM", "vmid", vmid)

	// Step 5: Set boot order
	{
		params := url.Values{}
		params.Set("boot", "order=scsi0")

		path := fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)
		if _, err := c.doRequest(ctx, http.MethodPut, path, params); err != nil {
			c.cleanup(ctx, node, vmid, filename, importStorage)
			return fmt.Errorf("failed to set boot order: %w", err)
		}
	}
	log.Info("Set boot order", "vmid", vmid)

	// Step 6: Convert to template
	{
		path := fmt.Sprintf("/nodes/%s/qemu/%d/template", node, vmid)
		body, err := c.doRequest(ctx, http.MethodPost, path, nil)
		if err != nil {
			c.cleanup(ctx, node, vmid, filename, importStorage)
			return fmt.Errorf("failed to convert VM to template: %w", err)
		}

		upid, err := extractUPID(body)
		if err != nil {
			c.cleanup(ctx, node, vmid, filename, importStorage)
			return fmt.Errorf("failed to extract UPID from template conversion response: %w", err)
		}

		if err := c.waitForTask(ctx, node, upid); err != nil {
			c.cleanup(ctx, node, vmid, filename, importStorage)
			return fmt.Errorf("template conversion failed: %w", err)
		}
	}
	log.Info("Converted VM to template", "vmid", vmid)

	// Step 7: Set tags
	tags := buildTags(imageName)
	if tags != "" {
		params := url.Values{}
		params.Set("tags", tags)

		path := fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)
		if _, err := c.doRequest(ctx, http.MethodPut, path, params); err != nil {
			// Tags are non-critical — log but don't fail
			log.Info("Failed to set tags on template", "vmid", vmid, "error", err)
		} else {
			log.Info("Set tags on template", "vmid", vmid, "tags", tags)
		}
	}

	// Step 8: Clean up the import file
	c.cleanupImportFile(ctx, node, filename, importStorage)

	_ = vmCreated
	_ = imageDownloaded

	log.Info("Template creation completed", "name", imageName, "vmid", vmid, "node", node)
	return nil
}

// cleanup performs best-effort cleanup of partial resources on failure
func (c *Client) cleanup(ctx context.Context, node string, vmid int, filename string, importStorage string) {
	log := log.FromContext(ctx)

	if vmid > 0 {
		log.Info("Cleaning up: deleting VM", "vmid", vmid)
		path := fmt.Sprintf("/nodes/%s/qemu/%d", node, vmid)
		params := url.Values{}
		params.Set("purge", "1")
		body, err := c.doRequest(ctx, http.MethodDelete, path+"?"+params.Encode(), nil)
		if err != nil {
			log.Info("Cleanup: failed to delete VM", "vmid", vmid, "error", err)
		} else if upid, err := extractUPID(body); err == nil {
			if err := c.waitForTask(ctx, node, upid); err != nil {
				log.Info("Cleanup: VM deletion task failed", "vmid", vmid, "error", err)
			}
		}
	}

	c.cleanupImportFile(ctx, node, filename, importStorage)
}

// cleanupImportFile removes the downloaded import file from Proxmox storage
func (c *Client) cleanupImportFile(ctx context.Context, node string, filename string, importStorage string) {
	log := log.FromContext(ctx)

	log.Info("Cleaning up import file", "filename", filename)
	path := fmt.Sprintf("/nodes/%s/storage/%s/content/%s:import/%s", node, importStorage, importStorage, filename)
	body, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		log.Info("Cleanup: failed to delete import file", "filename", filename, "error", err)
		return
	}

	upid, err := extractUPID(body)
	if err != nil {
		log.Info("Cleanup: failed to extract UPID from import file deletion", "error", err)
		return
	}

	if err := c.waitForTask(ctx, node, upid); err != nil {
		log.Info("Cleanup: import file deletion task failed", "filename", filename, "error", err)
	}
}

// buildTags constructs Proxmox-compatible tags from an image name.
// Input:  "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs"
// Output: "flatcar_3975.2.0;kubernetes_1.30.4;os-tooling_1.18.1;release-channel_stable"
func buildTags(imageName string) string {
	re := regexp.MustCompile(
		`^flatcar-([a-z]+)-([0-9.]+)-kube-([0-9.]+)-tooling-([0-9.]+)-gs$`,
	)
	matches := re.FindStringSubmatch(imageName)
	if len(matches) != 5 {
		return ""
	}

	channel := matches[1]
	flatcarVersion := matches[2]
	kubeVersion := matches[3]
	toolingVersion := matches[4]

	tags := []string{
		fmt.Sprintf("flatcar_%s", flatcarVersion),
		fmt.Sprintf("kubernetes_%s", kubeVersion),
		fmt.Sprintf("os-tooling_%s", toolingVersion),
		fmt.Sprintf("release-channel_%s", channel),
	}

	return strings.Join(tags, ";")
}

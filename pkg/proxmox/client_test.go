package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTags(t *testing.T) {
	testCases := []struct {
		name         string
		imageName    string
		expectedTags string
	}{
		{
			name:         "case 0: standard image name",
			imageName:    "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
			expectedTags: "flatcar_3975.2.0;kubernetes_1.30.4;os-tooling_v1.18.1;release-channel_stable",
		},
		{
			name:         "case 1: beta channel",
			imageName:    "flatcar-beta-4459.2.4-kube-1.34.5-tooling-1.27.0-gs",
			expectedTags: "flatcar_4459.2.4;kubernetes_1.34.5;os-tooling_v1.27.0;release-channel_beta",
		},
		{
			name:         "case 2: invalid image name returns empty",
			imageName:    "not-a-valid-image-name",
			expectedTags: "",
		},
		{
			name:         "case 3: empty image name returns empty",
			imageName:    "",
			expectedTags: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tags := buildTags(tc.imageName)
			assert.Equal(t, tc.expectedTags, tags)
		})
	}
}

func TestLoadCredentials(t *testing.T) {
	t.Run("valid credentials", func(t *testing.T) {
		content := `url: "proxmox.example.com:8006"
user: "root"
realm: "pam"
tokenId: "mytoken"
secret: "mysecret"
insecure: true`

		path := writeTempFile(t, "creds-*.yaml", content)
		creds, err := loadCredentials(path)
		require.NoError(t, err)
		assert.Equal(t, "proxmox.example.com:8006", creds.URL)
		assert.Equal(t, "root", creds.User)
		assert.Equal(t, "pam", creds.Realm)
		assert.Equal(t, "mytoken", creds.TokenID)
		assert.Equal(t, "mysecret", creds.Secret)
		assert.True(t, creds.Insecure)
	})

	t.Run("missing url returns error", func(t *testing.T) {
		content := `user: "root"
tokenId: "mytoken"
secret: "mysecret"`

		path := writeTempFile(t, "creds-*.yaml", content)
		_, err := loadCredentials(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "url is required")
	})

	t.Run("missing user returns error", func(t *testing.T) {
		content := `url: "proxmox.example.com:8006"
tokenId: "mytoken"
secret: "mysecret"`

		path := writeTempFile(t, "creds-*.yaml", content)
		_, err := loadCredentials(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "user is required")
	})

	t.Run("missing tokenId returns error", func(t *testing.T) {
		content := `url: "proxmox.example.com:8006"
user: "root"
secret: "mysecret"`

		path := writeTempFile(t, "creds-*.yaml", content)
		_, err := loadCredentials(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "tokenId is required")
	})

	t.Run("missing secret returns error", func(t *testing.T) {
		content := `url: "proxmox.example.com:8006"
user: "root"
tokenId: "mytoken"`

		path := writeTempFile(t, "creds-*.yaml", content)
		_, err := loadCredentials(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "secret is required")
	})

	t.Run("file not found returns error", func(t *testing.T) {
		_, err := loadCredentials("/nonexistent/path")
		assert.Error(t, err)
	})
}

func TestLoadLocations(t *testing.T) {
	t.Run("valid locations", func(t *testing.T) {
		content := `datacenter-1:
  node: "pve-node-1"
  storagePool: "local-lvm"
  importStorage: "local"
  bridge: "vmbr0"
datacenter-2:
  node: "pve-node-2"
  storagePool: "ceph-pool"
  bridge: "vmbr1"`

		path := writeTempFile(t, "locs-*.yaml", content)
		locations, err := loadLocations(path)
		require.NoError(t, err)
		assert.Len(t, locations, 2)

		assert.Equal(t, "pve-node-1", locations["datacenter-1"].Node)
		assert.Equal(t, "local-lvm", locations["datacenter-1"].StoragePool)
		assert.Equal(t, "local", locations["datacenter-1"].ImportStorage)
		assert.Equal(t, "vmbr0", locations["datacenter-1"].Bridge)

		// ImportStorage should default to "local" when empty
		assert.Equal(t, "local", locations["datacenter-2"].ImportStorage)
	})

	t.Run("missing node returns error", func(t *testing.T) {
		content := `dc1:
  storagePool: "local-lvm"
  bridge: "vmbr0"`

		path := writeTempFile(t, "locs-*.yaml", content)
		_, err := loadLocations(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "node is required")
	})

	t.Run("missing storagePool returns error", func(t *testing.T) {
		content := `dc1:
  node: "pve"
  bridge: "vmbr0"`

		path := writeTempFile(t, "locs-*.yaml", content)
		_, err := loadLocations(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "storagePool is required")
	})

	t.Run("missing bridge returns error", func(t *testing.T) {
		content := `dc1:
  node: "pve"
  storagePool: "local-lvm"`

		path := writeTempFile(t, "locs-*.yaml", content)
		_, err := loadLocations(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bridge is required")
	})
}

func TestExtractUPID(t *testing.T) {
	t.Run("valid UPID", func(t *testing.T) {
		body := `{"data":"UPID:pve:00001234:00000000:12345678:download:local:root@pam:"}`
		upid, err := extractUPID([]byte(body))
		require.NoError(t, err)
		assert.Equal(t, "UPID:pve:00001234:00000000:12345678:download:local:root@pam:", upid)
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		_, err := extractUPID([]byte("not json"))
		assert.Error(t, err)
	})
}

func TestFindVMByName(t *testing.T) {
	t.Run("finds existing VM", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api2/json/cluster/resources", r.URL.Path)
			assert.Equal(t, "vm", r.URL.Query().Get("type"))

			resp := map[string]interface{}{
				"data": []map[string]interface{}{
					{"vmid": 100, "name": "other-vm", "node": "pve", "template": 0, "type": "qemu"},
					{"vmid": 200, "name": "my-template", "node": "pve-2", "template": 1, "type": "qemu"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
		}

		vmid, node, found, err := client.findVMByName(context.Background(), "my-template")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, 200, vmid)
		assert.Equal(t, "pve-2", node)
	})

	t.Run("VM not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{
				"data": []map[string]interface{}{
					{"vmid": 100, "name": "other-vm", "node": "pve", "template": 0, "type": "qemu"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
		}

		_, _, found, err := client.findVMByName(context.Background(), "nonexistent")
		require.NoError(t, err)
		assert.False(t, found)
	})
}

func TestGetNextVMID(t *testing.T) {
	t.Run("returns VMID as string", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api2/json/cluster/nextid", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":"100"}`))
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
		}

		vmid, err := client.getNextVMID(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 100, vmid)
	})

	t.Run("returns VMID as integer", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":200}`))
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
		}

		vmid, err := client.getNextVMID(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 200, vmid)
	})
}

func TestExists(t *testing.T) {
	t.Run("returns true when template exists", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{
				"data": []map[string]interface{}{
					{"vmid": 100, "name": "my-template", "node": "pve", "template": 1, "type": "qemu"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
		}

		exists, err := client.Exists(context.Background(), "my-template", "dc1")
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("returns false when template not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{"data": []map[string]interface{}{}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
		}

		exists, err := client.Exists(context.Background(), "nonexistent", "dc1")
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestCreateTemplate(t *testing.T) {
	t.Run("happy path - full template creation", func(t *testing.T) {
		var step atomic.Int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch {
			// Step 1: Download image
			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/storage/local/download-url"):
				assert.Equal(t, "import", r.FormValue("content"))
				assert.Contains(t, r.FormValue("filename"), ".qcow2")
				step.Store(1)
				_, _ = w.Write([]byte(`{"data":"UPID:pve:0001:0:1:download:local:root@pam:"}`))

			// Step 2: Get next VMID
			case r.Method == http.MethodGet && r.URL.Path == "/api2/json/cluster/nextid":
				step.Store(2)
				_, _ = w.Write([]byte(`{"data":"100"}`))

			// Step 3: Create VM
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/qemu") && !strings.Contains(r.URL.Path, "/config"):
				assert.Equal(t, "100", r.FormValue("vmid"))
				assert.Equal(t, "2048", r.FormValue("memory"))
				assert.Equal(t, "2", r.FormValue("cores"))
				step.Store(3)
				_, _ = w.Write([]byte(`{"data":"UPID:pve:0002:0:2:qmcreate:100:root@pam:"}`))

			// Step 4: Import disk (POST to config)
			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/config"):
				assert.Contains(t, r.FormValue("scsi0"), "import-from=local:import/")
				step.Store(4)
				_, _ = w.Write([]byte(`{"data":"UPID:pve:0003:0:3:qmconfig:100:root@pam:"}`))

			// Step 5+7: PUT config (boot order or tags)
			case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/config"):
				if r.FormValue("boot") != "" {
					assert.Equal(t, "order=scsi0", r.FormValue("boot"))
					step.Store(5)
				}
				_, _ = w.Write([]byte(`{"data":null}`))

			// Step 6: Convert to template
			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/template"):
				step.Store(6)
				_, _ = w.Write([]byte(`{"data":"UPID:pve:0004:0:4:qmtemplate:100:root@pam:"}`))

			// Task status polling - always return completed
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/"):
				if strings.HasSuffix(r.URL.Path, "/status") {
					_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
				} else {
					_, _ = w.Write([]byte(`{"data":[]}`))
				}

			// Cleanup: delete import file
			case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/storage/"):
				_, _ = w.Write([]byte(`{"data":"UPID:pve:0005:0:5:imgdel:local:root@pam:"}`))

			default:
				t.Logf("Unhandled request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
			locations: map[string]*Location{
				"dc1": {
					Node:          "pve",
					StoragePool:   "local-lvm",
					ImportStorage: "local",
					Bridge:        "vmbr0",
				},
			},
		}

		err := client.createTemplate(
			context.Background(),
			"https://example.com/image.qcow2",
			"flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
			"dc1",
		)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, int(step.Load()), 6, "should have reached at least step 6 (template conversion)")
	})

	t.Run("cleanup on VM creation failure", func(t *testing.T) {
		var cleanupImportCalled atomic.Bool

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch {
			// Download succeeds
			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/download-url"):
				_, _ = w.Write([]byte(`{"data":"UPID:pve:0001:0:1:download:local:root@pam:"}`))

			// Task polling - always OK
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/") && strings.HasSuffix(r.URL.Path, "/status"):
				_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))

			// Get next VMID
			case r.Method == http.MethodGet && r.URL.Path == "/api2/json/cluster/nextid":
				_, _ = w.Write([]byte(`{"data":"100"}`))

			// VM creation fails
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/qemu"):
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"errors":{"vmid":"VM 100 already exists"}}`))

			// Cleanup: delete import file
			case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/storage/"):
				cleanupImportCalled.Store(true)
				_, _ = w.Write([]byte(`{"data":"UPID:pve:0005:0:5:imgdel:local:root@pam:"}`))

			default:
				_, _ = w.Write([]byte(`{"data":null}`))
			}
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
			locations: map[string]*Location{
				"dc1": {
					Node:          "pve",
					StoragePool:   "local-lvm",
					ImportStorage: "local",
					Bridge:        "vmbr0",
				},
			},
		}

		err := client.createTemplate(
			context.Background(),
			"https://example.com/image.qcow2",
			"flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
			"dc1",
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create VM")
		assert.True(t, cleanupImportCalled.Load(), "should have cleaned up import file")
	})
}

func TestDelete(t *testing.T) {
	t.Run("deletes existing template", func(t *testing.T) {
		var deleteCalled atomic.Bool

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			switch {
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/cluster/resources"):
				resp := map[string]interface{}{
					"data": []map[string]interface{}{
						{"vmid": 100, "name": "my-template", "node": "pve", "template": 1, "type": "qemu"},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)

			case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/qemu/100"):
				deleteCalled.Store(true)
				_, _ = w.Write([]byte(`{"data":"UPID:pve:0001:0:1:qmdestroy:100:root@pam:"}`))

			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/") && strings.HasSuffix(r.URL.Path, "/status"):
				_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
			}
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
		}

		err := client.Delete(context.Background(), "my-template", "dc1")
		assert.NoError(t, err)
		assert.True(t, deleteCalled.Load())
	})

	t.Run("noop when template not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]interface{}{"data": []map[string]interface{}{}}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := &Client{
			baseURL:    server.URL + "/api2/json",
			authHeader: "PVEAPIToken=test",
			httpClient: server.Client(),
		}

		err := client.Delete(context.Background(), "nonexistent", "dc1")
		assert.NoError(t, err)
	})
}

func TestGetLocations(t *testing.T) {
	client := &Client{
		locations: map[string]*Location{
			"dc1": {Node: "pve-1", StoragePool: "local-lvm", Bridge: "vmbr0", ImportStorage: "local"},
			"dc2": {Node: "pve-2", StoragePool: "ceph", Bridge: "vmbr1", ImportStorage: "local"},
		},
	}

	locations := client.GetLocations()
	assert.Len(t, locations, 2)
	assert.NotNil(t, locations["dc1"])
	assert.NotNil(t, locations["dc2"])
}

func writeTempFile(t *testing.T, pattern string, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("test-%s", pattern))
	err := os.WriteFile(path, []byte(content), 0600)
	require.NoError(t, err)
	return path
}

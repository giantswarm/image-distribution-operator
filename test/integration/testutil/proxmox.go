//go:build integration

package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Proxmox inventory names below are written into the credentials/locations files
// consumed by pkg/proxmox, and reused by the test to assert against the imported
// template.
const (
	ProxmoxLocation      = "integration"
	ProxmoxNode          = "pve"
	ProxmoxStoragePool   = "local-lvm"
	ProxmoxImportStorage = "local"
	ProxmoxBridge        = "vmbr0"
)

// fakeVM is a single entry in the fake's in-memory inventory.
type fakeVM struct {
	vmid     int
	name     string
	template bool
}

// FakeProxmox is an in-process Proxmox REST API simulator. Proxmox has no
// upstream simulator equivalent to govmomi's vcsim, so this hand-rolled fake
// implements just the endpoints pkg/proxmox exercises, backed by an in-memory
// VM inventory and task registry. It serves over HTTPS because pkg/proxmox
// hard-codes the https scheme (the client is configured with insecure: true so
// it accepts the test certificate).
type FakeProxmox struct {
	server *httptest.Server

	mu      sync.Mutex
	vms     map[int]*fakeVM
	tasks   map[string]bool // upid -> exited OK
	taskSeq int
}

// StartFakeProxmox boots the fake Proxmox API over TLS.
func StartFakeProxmox() *FakeProxmox {
	f := &FakeProxmox{
		vms:   make(map[int]*fakeVM),
		tasks: make(map[string]bool),
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	return f
}

// Close shuts down the fake server.
func (f *FakeProxmox) Close() {
	f.server.Close()
}

// Host returns the fake's host:port, used as the "url" credential.
func (f *FakeProxmox) Host() string {
	return strings.TrimPrefix(f.server.URL, "https://")
}

// FindByName returns the inventory state for a VM/template, for asserting on the
// fake's inventory independently of the code under test.
func (f *FakeProxmox) FindByName(name string) (vmid int, template, found bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, vm := range f.vms {
		if vm.name == name {
			return vm.vmid, vm.template, true
		}
	}
	return 0, false, false
}

// WriteConfig writes the credentials and locations YAML files that pkg/proxmox
// expects, wired to this fake and its inventory, and returns their paths.
func (f *FakeProxmox) WriteConfig(dir string) (credentialsPath, locationsPath string, err error) {
	credentials := fmt.Sprintf(
		"url: %s\nuser: root\nrealm: pam\ntokenId: test\nsecret: secret\ninsecure: true\n",
		f.Host(),
	)
	credentialsPath = filepath.Join(dir, "credentials.yaml")
	if err := os.WriteFile(credentialsPath, []byte(credentials), 0o600); err != nil {
		return "", "", fmt.Errorf("failed to write credentials file: %w", err)
	}

	locations := fmt.Sprintf(`%s:
  node: %s
  storagePool: %s
  importStorage: %s
  bridge: %s
`, ProxmoxLocation, ProxmoxNode, ProxmoxStoragePool, ProxmoxImportStorage, ProxmoxBridge)
	locationsPath = filepath.Join(dir, "locations.yaml")
	if err := os.WriteFile(locationsPath, []byte(locations), 0o600); err != nil {
		return "", "", fmt.Errorf("failed to write locations file: %w", err)
	}

	return credentialsPath, locationsPath, nil
}

// newTask registers a task outcome and returns its UPID.
func (f *FakeProxmox) newTask(ok bool) string {
	f.taskSeq++
	upid := fmt.Sprintf("UPID:pve:%08X:00000000:00000000:task:local:root@pam:", f.taskSeq)
	f.tasks[upid] = ok
	return upid
}

// nextFreeVMID returns the lowest unused VMID at or above 100.
func (f *FakeProxmox) nextFreeVMID() int {
	id := 100
	for {
		if _, ok := f.vms[id]; !ok {
			return id
		}
		id++
	}
}

// handle routes requests to the endpoints pkg/proxmox exercises. Ordering
// mirrors pkg/proxmox/client_test.go so the two stay recognisably aligned.
func (f *FakeProxmox) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	f.mu.Lock()
	defer f.mu.Unlock()

	path := r.URL.Path

	switch {
	// Connectivity check in New().
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/version"):
		_, _ = w.Write([]byte(`{"data":{"version":"8.0.0"}}`))

	// Step 1: download image to import storage. Fetch and validate the bytes so
	// the Error spec (garbage in S3) fails the download task server-side.
	case r.Method == http.MethodPost && strings.Contains(path, "/download-url"):
		ok := fetchIsQCOW2(r.FormValue("url"))
		_, _ = w.Write(taskData(f.newTask(ok)))

	// Step 2: next available VMID.
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/cluster/nextid"):
		_, _ = w.Write([]byte(fmt.Sprintf(`{"data":"%d"}`, f.nextFreeVMID())))

	// findVMByName (Exists / Delete).
	case r.Method == http.MethodGet && strings.Contains(path, "/cluster/resources"):
		f.writeResources(w)

	// Task status / log polling.
	case r.Method == http.MethodGet && strings.Contains(path, "/tasks/"):
		f.writeTaskStatus(w, path)

	// Step 6: convert VM to template.
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/template"):
		if vmid, ok := vmidFromPath(path); ok {
			if vm := f.vms[vmid]; vm != nil {
				vm.template = true
			}
		}
		_, _ = w.Write(taskData(f.newTask(true)))

	// Step 4: import disk (POST to .../config).
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/config"):
		_, _ = w.Write(taskData(f.newTask(true)))

	// Steps 5 & 7: boot order / tags (PUT to .../config) - synchronous.
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/config"):
		_, _ = w.Write([]byte(`{"data":null}`))

	// Step 3: create empty VM.
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/qemu"):
		vmid := atoiDefault(r.FormValue("vmid"), f.nextFreeVMID())
		f.vms[vmid] = &fakeVM{vmid: vmid, name: r.FormValue("name")}
		_, _ = w.Write(taskData(f.newTask(true)))

	// Delete VM/template (DELETE .../qemu/{vmid}).
	case r.Method == http.MethodDelete && strings.Contains(path, "/qemu/"):
		if vmid, ok := vmidFromPath(path); ok {
			delete(f.vms, vmid)
		}
		_, _ = w.Write(taskData(f.newTask(true)))

	// Cleanup: delete import file from storage.
	case r.Method == http.MethodDelete && strings.Contains(path, "/storage/"):
		_, _ = w.Write(taskData(f.newTask(true)))

	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"errors":"unhandled %s %s"}`, r.Method, path)))
	}
}

// writeResources renders the current inventory in the shape findVMByName parses.
func (f *FakeProxmox) writeResources(w http.ResponseWriter) {
	type item struct {
		VMID     int    `json:"vmid"`
		Name     string `json:"name"`
		Node     string `json:"node"`
		Template int    `json:"template"`
		Type     string `json:"type"`
	}
	data := make([]item, 0, len(f.vms))
	for _, vm := range f.vms {
		tmpl := 0
		if vm.template {
			tmpl = 1
		}
		data = append(data, item{VMID: vm.vmid, Name: vm.name, Node: ProxmoxNode, Template: tmpl, Type: "qemu"})
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
}

// writeTaskStatus resolves a task poll from the registry. Every task completes
// immediately ("stopped") so waitForTask never sleeps.
func (f *FakeProxmox) writeTaskStatus(w http.ResponseWriter, path string) {
	if strings.HasSuffix(path, "/log") {
		_, _ = w.Write([]byte(`{"data":[]}`))
		return
	}

	upid := upidFromPath(path)
	exit := "OK"
	if ok, found := f.tasks[upid]; found && !ok {
		exit = "download failed: not a valid qcow2 image"
	}
	_, _ = w.Write([]byte(fmt.Sprintf(`{"data":{"status":"stopped","exitstatus":%q}}`, exit)))
}

// fetchIsQCOW2 downloads url and reports whether it starts with the qcow2 magic.
// Runs server-side, exactly as a real Proxmox download-url would; RedirectS3Transport
// routes the fetch to the fake S3 bucket.
func fetchIsQCOW2(url string) bool {
	if url == "" {
		return false
	}
	resp, err := http.Get(url) // #nosec G107 -- url is the S3 image URL under test
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	head := make([]byte, len(qcow2Magic))
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		return false
	}
	return bytes.Equal(head, qcow2Magic)
}

// taskData wraps a UPID in the {"data":"UPID..."} envelope Proxmox returns for
// asynchronous operations.
func taskData(upid string) []byte {
	return []byte(fmt.Sprintf(`{"data":%q}`, upid))
}

// vmidFromPath extracts the {vmid} segment from a .../qemu/{vmid}[/...] path.
func vmidFromPath(path string) (int, bool) {
	idx := strings.Index(path, "/qemu/")
	if idx < 0 {
		return 0, false
	}
	rest := path[idx+len("/qemu/"):]
	if slash := strings.Index(rest, "/"); slash >= 0 {
		rest = rest[:slash]
	}
	vmid := atoiDefault(rest, -1)
	if vmid < 0 {
		return 0, false
	}
	return vmid, true
}

// upidFromPath extracts the UPID from a .../tasks/{upid}/status path. r.URL.Path
// is already percent-decoded, so the UPID's colons are intact.
func upidFromPath(path string) string {
	idx := strings.Index(path, "/tasks/")
	if idx < 0 {
		return ""
	}
	rest := path[idx+len("/tasks/"):]
	if slash := strings.LastIndex(rest, "/"); slash >= 0 {
		rest = rest[:slash]
	}
	return rest
}

func atoiDefault(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}

//go:build integration

package testutil

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// VCD inventory names below are written into the credentials/locations files
// consumed by pkg/cloud-director, and reused by the test to assert against the
// imported vApp template.
const (
	VCDLocation = "integration"
	VCDOrg      = "test-org"
	VCDVDC      = "test-vdc"
	VCDCatalog  = "test-catalog"

	// Fixed hex UUIDs for the org and catalog. They only need to satisfy
	// govcd's extractUuid regex ([a-f0-9]{8}-...-[a-f0-9]{12}); the values are
	// otherwise opaque.
	vcdOrgUUID     = "aaaaaaaa-0000-0000-0000-000000000000"
	vcdCatalogUUID = "bbbbbbbb-0000-0000-0000-000000000000"

	// vcdToken must be longer than 32 chars so the client additionally sends the
	// "Authorization: bearer" header, matching a real bearer-token session.
	vcdToken = "fake-vcd-access-token-000000000000000000"
)

// fakeVAppTemplate is a single entry in the fake's in-memory catalog.
type fakeVAppTemplate struct {
	uuid string
	name string
	// descriptorUploaded flips once the OVF descriptor has been PUT. Before it,
	// the vApp-template GET reports a single file (the descriptor upload link);
	// after it, it reports the descriptor plus the disk, which is what govcd's
	// waitForTempUploadLinks blocks on.
	descriptorUploaded bool
}

// FakeVCD is an in-process VMware Cloud Director REST API simulator. VCD has no
// upstream simulator equivalent to govmomi's vcsim, so this hand-rolled fake
// implements just the endpoints go-vcloud-director exercises during an OVF
// upload/delete, backed by an in-memory catalog. It serves over HTTPS because
// the provider always dials https (the client is configured with insecure: true
// so it accepts the test certificate).
type FakeVCD struct {
	server *httptest.Server

	mu        sync.Mutex
	templates map[string]*fakeVAppTemplate // uuid -> template
	seq       int
}

// StartFakeVCD boots the fake VCD API over TLS.
func StartFakeVCD() *FakeVCD {
	f := &FakeVCD{
		templates: make(map[string]*fakeVAppTemplate),
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	return f
}

// Close shuts down the fake server.
func (f *FakeVCD) Close() {
	f.server.Close()
}

// URL returns the fake's base URL, used as the "url" credential.
func (f *FakeVCD) URL() string {
	return f.server.URL
}

// FindByName returns the inventory state for a vApp template, for asserting on
// the fake's catalog independently of the code under test. The returned uuid is
// the template's identity: an unchanged uuid across reconciles proves no
// re-import happened.
func (f *FakeVCD) FindByName(name string) (uuid string, found bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.templates {
		if t.name == name {
			return t.uuid, true
		}
	}
	return "", false
}

// WriteConfig writes the credentials and locations YAML files that
// pkg/cloud-director expects, wired to this fake and its inventory, and returns
// their paths.
func (f *FakeVCD) WriteConfig(dir string) (credentialsPath, locationsPath string, err error) {
	credentials := fmt.Sprintf(
		"url: %s\nusername: user\npassword: pass\norg: %s\ninsecure: true\n",
		f.URL(), VCDOrg,
	)
	credentialsPath = filepath.Join(dir, "credentials.yaml")
	if err := os.WriteFile(credentialsPath, []byte(credentials), 0o600); err != nil {
		return "", "", fmt.Errorf("failed to write credentials file: %w", err)
	}

	// hardwareVersion 0 leaves the OVA descriptor untouched (no vmx-N patching),
	// keeping the fixture the client uploads byte-for-byte the one we build.
	locations := fmt.Sprintf(`name: %s
vdc: %s
catalog: %s
hardwareVersion: 0
`, VCDLocation, VCDVDC, VCDCatalog)
	locationsPath = filepath.Join(dir, "locations.yaml")
	if err := os.WriteFile(locationsPath, []byte(locations), 0o600); err != nil {
		return "", "", fmt.Errorf("failed to write locations file: %w", err)
	}

	return credentialsPath, locationsPath, nil
}

// newUUID returns a fresh hex UUID satisfying govcd's extractUuid regex.
func (f *FakeVCD) newUUID() string {
	f.seq++
	return fmt.Sprintf("%08x-0000-0000-0000-000000000000", f.seq)
}

// handle routes requests to the endpoints go-vcloud-director exercises. The flow
// (auth -> org/catalog lookup -> vApp-template query -> OVF upload / delete) is
// documented per case below.
func (f *FakeVCD) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")

	path := r.URL.Path

	switch {
	// Version negotiation (unauthenticated): advertise 37.0 and its login URL.
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/api/versions"):
		f.writeVersions(w)

	// Session login: return the access-token header. Body is ignored by govcd.
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/api/sessions"):
		w.Header().Set("X-Vmware-Vcloud-Access-Token", vcdToken)
		w.WriteHeader(http.StatusOK)

	// Org list, then the org entity itself (GetOrgByName).
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/api/org"):
		f.writeOrgList(w)
	case r.Method == http.MethodGet && strings.Contains(path, "/api/org/"):
		f.writeOrg(w)

	// Typed queries: catalog lookup, vApp-template lookup, and the task poll the
	// upload transport issues before each part.
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/api/query"):
		f.handleQuery(w, r)

	// Create the catalog item for upload (returns the sparse vApp-template href).
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/action/upload"):
		f.handleCreateItem(w, r)

	// Catalog entity (GetCatalogByName). Checked after /action/upload so the POST
	// wins for the upload sub-path.
	case r.Method == http.MethodGet && strings.Contains(path, "/api/catalog/"):
		f.writeCatalog(w)

	// vApp-template entity fetch / delete.
	case r.Method == http.MethodGet && strings.Contains(path, "/api/vAppTemplate/"):
		f.handleGetTemplate(w, path)
	case r.Method == http.MethodDelete && strings.Contains(path, "/api/vAppTemplate/"):
		f.handleDeleteTemplate(w, path)

	// Task polling: every task resolves to success immediately.
	case r.Method == http.MethodGet && strings.Contains(path, "/api/task/"):
		f.writeTask(w, path, "success")

	// OVF descriptor / disk uploads to the transfer folder.
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/transfer/"):
		f.handleTransferPut(w, path)

	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `<Error message="unhandled %s %s"/>`, r.Method, path)
	}
}

func (f *FakeVCD) writeVersions(w http.ResponseWriter) {
	_, _ = fmt.Fprintf(w, `<SupportedVersions>
  <VersionInfo>
    <Version>37.0</Version>
    <LoginUrl>%s/api/sessions</LoginUrl>
  </VersionInfo>
</SupportedVersions>`, f.URL())
}

func (f *FakeVCD) writeOrgList(w http.ResponseWriter) {
	_, _ = fmt.Fprintf(w, `<OrgList>
  <Org href="%s/api/org/%s" name="%s" type="application/vnd.vmware.vcloud.org+xml"/>
</OrgList>`, f.URL(), vcdOrgUUID, VCDOrg)
}

func (f *FakeVCD) writeOrg(w http.ResponseWriter) {
	_, _ = fmt.Fprintf(w, `<Org href="%s/api/org/%s" id="urn:vcloud:org:%s" name="%s" type="application/vnd.vmware.vcloud.org+xml">
  <FullName>%s</FullName>
</Org>`, f.URL(), vcdOrgUUID, vcdOrgUUID, VCDOrg, VCDOrg)
}

func (f *FakeVCD) writeCatalog(w http.ResponseWriter) {
	_, _ = fmt.Fprintf(w, `<Catalog href="%s/api/catalog/%s" id="urn:vcloud:catalog:%s" name="%s" type="application/vnd.vmware.vcloud.catalog+xml">
  <Link rel="add" type="application/vnd.vmware.vcloud.uploadVAppTemplateParams+xml" href="%s/api/catalog/%s/action/upload"/>
  <CatalogItems/>
</Catalog>`, f.URL(), vcdCatalogUUID, vcdCatalogUUID, VCDCatalog, f.URL(), vcdCatalogUUID)
}

// handleQuery serves the typed /api/query endpoint. Catalog lookups always
// resolve to the single configured catalog; vApp-template lookups resolve
// against the in-memory inventory (empty result => govcd's ErrorEntityNotFound);
// task queries return an empty page.
func (f *FakeVCD) handleQuery(w http.ResponseWriter, r *http.Request) {
	// Parse from RawQuery rather than r.URL.Query(): govcd's vApp-template filter
	// is a single "catalogName==X;name==Y" segment, and Go's query parser drops
	// any segment containing a ';', which would silently blank out the filter.
	switch rawParam(r.URL.RawQuery, "type") {
	case "catalog":
		_, _ = fmt.Fprintf(w, `<QueryResultRecords total="1" page="1" pageSize="25">
  <CatalogRecord href="%s/api/catalog/%s" name="%s" orgName="%s"/>
</QueryResultRecords>`, f.URL(), vcdCatalogUUID, VCDCatalog, VCDOrg)

	case "vAppTemplate":
		name := filterValue(rawParam(r.URL.RawQuery, "filter"), "name")
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, t := range f.templates {
			if t.name == name {
				_, _ = fmt.Fprintf(w, `<QueryResultRecords total="1" page="1" pageSize="25">
  <VAppTemplateRecord href="%s/api/vAppTemplate/vappTemplate-%s" name="%s" catalogName="%s"/>
</QueryResultRecords>`, f.URL(), t.uuid, t.name, VCDCatalog)
				return
			}
		}
		_, _ = fmt.Fprint(w, `<QueryResultRecords total="0" page="1" pageSize="25"/>`)

	default:
		_, _ = fmt.Fprint(w, `<QueryResultRecords total="0" page="1" pageSize="25"/>`)
	}
}

// handleCreateItem registers a new vApp template and returns the catalog item
// whose Entity href is the vApp-template URL govcd drives the upload against.
func (f *FakeVCD) handleCreateItem(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Name string `xml:"name,attr"`
	}
	_ = xml.NewDecoder(r.Body).Decode(&params)

	f.mu.Lock()
	uuid := f.newUUID()
	f.templates[uuid] = &fakeVAppTemplate{uuid: uuid, name: params.Name}
	f.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(w, `<CatalogItem href="%s/api/catalogItem/%s" name="%s" type="application/vnd.vmware.vcloud.catalogItem+xml">
  <Entity href="%s/api/vAppTemplate/vappTemplate-%s" name="%s" type="application/vnd.vmware.vcloud.vAppTemplate+xml"/>
</CatalogItem>`, f.URL(), uuid, params.Name, f.URL(), uuid, params.Name)
}

// handleGetTemplate reports the vApp template's upload state. Before the OVF
// descriptor is uploaded it advertises a single file (the descriptor link);
// afterwards it advertises the descriptor (already transferred) plus the disk,
// which unblocks govcd's waitForTempUploadLinks.
func (f *FakeVCD) handleGetTemplate(w http.ResponseWriter, path string) {
	uuid := uuidFromTemplatePath(path)

	f.mu.Lock()
	t := f.templates[uuid]
	uploaded := t != nil && t.descriptorUploaded
	name := ""
	if t != nil {
		name = t.name
	}
	f.mu.Unlock()

	tasks := fmt.Sprintf(`<Tasks>
    <Task href="%s/api/task/%s" status="success" name="task" operation="Importing">
      <Owner href="%s/api/vAppTemplate/vappTemplate-%s" name="%s"/>
    </Task>
  </Tasks>`, f.URL(), uuid, f.URL(), uuid, name)

	var files string
	if uploaded {
		files = fmt.Sprintf(`<Files>
    <File name="descriptor.ovf" size="4096" bytesTransferred="4096">
      <Link rel="upload:default" href="%s/transfer/%s/descriptor.ovf"/>
    </File>
    <File name="%s" size="1024" bytesTransferred="0">
      <Link rel="upload:default" href="%s/transfer/%s/%s"/>
    </File>
  </Files>`, f.URL(), uuid, diskFileName, f.URL(), uuid, diskFileName)
	} else {
		files = fmt.Sprintf(`<Files>
    <File name="descriptor.ovf" size="4096" bytesTransferred="0">
      <Link rel="upload:default" href="%s/transfer/%s/descriptor.ovf"/>
    </File>
  </Files>`, f.URL(), uuid)
	}

	_, _ = fmt.Fprintf(w, `<VAppTemplate href="%s/api/vAppTemplate/vappTemplate-%s" id="urn:vcloud:vapptemplate:%s" name="%s" status="0" ovfDescriptorUploaded="%t">
  %s
  %s
</VAppTemplate>`, f.URL(), uuid, uuid, name, uploaded, tasks, files)
}

// handleDeleteTemplate removes the template and returns a delete task that polls
// to success.
func (f *FakeVCD) handleDeleteTemplate(w http.ResponseWriter, path string) {
	uuid := uuidFromTemplatePath(path)

	f.mu.Lock()
	delete(f.templates, uuid)
	f.mu.Unlock()

	w.WriteHeader(http.StatusAccepted)
	f.writeTask(w, "/api/task/"+uuid, "running")
}

// handleTransferPut accepts an OVF descriptor or disk upload. The descriptor PUT
// flips the template into its "links ready" state; disk PUTs are accepted and
// discarded.
func (f *FakeVCD) handleTransferPut(w http.ResponseWriter, path string) {
	if strings.HasSuffix(path, "/descriptor.ovf") {
		uuid := transferUUID(path)
		f.mu.Lock()
		if t := f.templates[uuid]; t != nil {
			t.descriptorUploaded = true
		}
		f.mu.Unlock()
	}
	w.WriteHeader(http.StatusOK)
}

func (f *FakeVCD) writeTask(w http.ResponseWriter, path, status string) {
	uuid := lastPathSegment(path)
	_, _ = fmt.Fprintf(w, `<Task href="%s/api/task/%s" id="urn:vcloud:task:%s" status="%s" name="task" operation="Importing" progress="100"/>`,
		f.URL(), uuid, uuid, status)
}

// rawParam extracts key's value from a raw (undecoded) query string, splitting
// only on '&'. Unlike net/url's parser it tolerates ';' inside a value, which
// govcd's filter expressions rely on.
func rawParam(rawQuery, key string) string {
	for _, seg := range strings.Split(rawQuery, "&") {
		if v, ok := strings.CutPrefix(seg, key+"="); ok {
			return v
		}
	}
	return ""
}

// filterValue extracts key's value from a govcd filter string such as
// "catalogName==foo;name==bar". Terms are separated by ';' and matched exactly on
// the key to avoid "name" matching inside "catalogName".
func filterValue(filter, key string) string {
	for _, term := range strings.Split(filter, ";") {
		if k, v, ok := strings.Cut(term, "=="); ok && k == key {
			return v
		}
	}
	return ""
}

// uuidFromTemplatePath extracts the uuid from a .../vAppTemplate/vappTemplate-{uuid} path.
func uuidFromTemplatePath(path string) string {
	const marker = "/vAppTemplate/vappTemplate-"
	idx := strings.Index(path, marker)
	if idx < 0 {
		return ""
	}
	return path[idx+len(marker):]
}

// transferUUID extracts the {uuid} segment from a /transfer/{uuid}/{file} path.
func transferUUID(path string) string {
	rest := strings.TrimPrefix(path, "/transfer/")
	if slash := strings.Index(rest, "/"); slash >= 0 {
		return rest[:slash]
	}
	return rest
}

func lastPathSegment(path string) string {
	if slash := strings.LastIndex(path, "/"); slash >= 0 {
		return path[slash+1:]
	}
	return path
}

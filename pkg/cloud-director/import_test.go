package clouddirector

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testOVF = `<?xml version="1.0" encoding="UTF-8"?>
<Envelope xmlns="http://schemas.dmtf.org/ovf/envelope/1" xmlns:vssd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_VirtualSystemSettingData">
  <References>
    <File ovf:href="flatcar.vmdk" ovf:id="file1" ovf:size="4096"/>
  </References>
  <VirtualSystem>
    <System>
      <vssd:VirtualSystemType>vmx-15</vssd:VirtualSystemType>
    </System>
  </VirtualSystem>
</Envelope>
`

type tarEntry struct {
	name     string
	typeflag byte
	body     []byte
	linkname string
}

func buildTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: typeflag,
			Mode:     0644,
			Size:     int64(len(e.body)),
			Linkname: e.linkname,
		}
		if typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if typeflag == tar.TypeReg {
			_, err := tw.Write(e.body)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func TestExtractOVA(t *testing.T) {
	disk := randomBytes(t, 4096)
	manifest := []byte("SHA256(flatcar.ovf)= deadbeef\n")

	testCases := []struct {
		name            string
		hardwareVersion string
		ovf             string
		expectedOVF     string
	}{
		{
			name:            "case 0: OVF is patched to the configured hardware version",
			hardwareVersion: "vmx-19",
			ovf:             testOVF,
			expectedOVF:     strings.Replace(testOVF, "vmx-15", "vmx-19", 1),
		},
		{
			name:            "case 1: empty hardware version leaves the OVF untouched",
			hardwareVersion: "",
			ovf:             testOVF,
			expectedOVF:     testOVF,
		},
		{
			name:            "case 2: OVF without XML declaration gets one so govcd detects it as OVF",
			hardwareVersion: "",
			ovf:             "<Envelope/>\n",
			expectedOVF:     xmlDeclaration + "<Envelope/>\n",
		},
		{
			name:            "case 3: UTF-8 BOM is stripped",
			hardwareVersion: "",
			ovf:             "\xef\xbb\xbf<?xml version=\"1.0\"?><Envelope/>",
			expectedOVF:     "<?xml version=\"1.0\"?><Envelope/>",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ova := buildTar(t, []tarEntry{
				{name: "flatcar.ovf", body: []byte(tc.ovf)},
				{name: "flatcar.mf", body: manifest},
				{name: "flatcar.vmdk", body: disk},
			})

			ovfPath, err := extractOVA(bytes.NewReader(ova), dir, tc.hardwareVersion)
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(dir, "flatcar.ovf"), ovfPath)

			gotOVF, err := os.ReadFile(ovfPath)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedOVF, string(gotOVF))

			gotDisk, err := os.ReadFile(filepath.Join(dir, "flatcar.vmdk"))
			require.NoError(t, err)
			assert.Equal(t, disk, gotDisk, "disk must be byte-identical")

			gotManifest, err := os.ReadFile(filepath.Join(dir, "flatcar.mf"))
			require.NoError(t, err)
			assert.Equal(t, manifest, gotManifest)

			info, err := os.Stat(filepath.Join(dir, "flatcar.vmdk"))
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
		})
	}
}

func TestExtractOVASubdirectory(t *testing.T) {
	dir := t.TempDir()
	ova := buildTar(t, []tarEntry{
		{name: "./", typeflag: tar.TypeDir},
		{name: "./flatcar.ovf", body: []byte(testOVF)},
		{name: "./flatcar.vmdk", body: []byte("disk")},
	})

	ovfPath, err := extractOVA(bytes.NewReader(ova), dir, "vmx-19")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "flatcar.ovf"), ovfPath)

	_, err = os.Stat(filepath.Join(dir, "flatcar.vmdk"))
	require.NoError(t, err)
}

func TestExtractOVAErrors(t *testing.T) {
	ovf := tarEntry{name: "flatcar.ovf", body: []byte(testOVF)}

	testCases := []struct {
		name    string
		entries []tarEntry
	}{
		{
			name:    "case 0: parent directory traversal",
			entries: []tarEntry{ovf, {name: "../evil", body: []byte("x")}},
		},
		{
			name:    "case 1: nested parent directory traversal",
			entries: []tarEntry{ovf, {name: "sub/../../evil", body: []byte("x")}},
		},
		{
			name:    "case 2: absolute path",
			entries: []tarEntry{ovf, {name: "/tmp/evil", body: []byte("x")}},
		},
		{
			name:    "case 3: symlink entry",
			entries: []tarEntry{ovf, {name: "link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}},
		},
		{
			name:    "case 4: hard link entry",
			entries: []tarEntry{ovf, {name: "link", typeflag: tar.TypeLink, linkname: "flatcar.ovf"}},
		},
		{
			name:    "case 5: no OVF descriptor",
			entries: []tarEntry{{name: "flatcar.vmdk", body: []byte("disk")}},
		},
		{
			name:    "case 6: multiple OVF descriptors",
			entries: []tarEntry{ovf, {name: "other.ovf", body: []byte(testOVF)}},
		},
		{
			name:    "case 7: duplicate entry",
			entries: []tarEntry{ovf, {name: "flatcar.vmdk", body: []byte("a")}, {name: "flatcar.vmdk", body: []byte("b")}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "extract")
			require.NoError(t, os.Mkdir(dir, 0700))

			_, err := extractOVA(bytes.NewReader(buildTar(t, tc.entries)), dir, "vmx-19")
			require.Error(t, err)

			_, statErr := os.Lstat(filepath.Join(parent, "evil"))
			assert.True(t, os.IsNotExist(statErr), "nothing may be written outside the extraction directory")
		})
	}
}

func TestExtractOVATruncated(t *testing.T) {
	ova := buildTar(t, []tarEntry{
		{name: "flatcar.ovf", body: []byte(testOVF)},
		{name: "flatcar.vmdk", body: randomBytes(t, 4096)},
	})
	_, err := extractOVA(bytes.NewReader(ova[:len(ova)/2]), t.TempDir(), "vmx-19")
	require.Error(t, err)
}

func TestDownloadAndExtract(t *testing.T) {
	disk := randomBytes(t, 8192)
	ova := buildTar(t, []tarEntry{
		{name: "flatcar.ovf", body: []byte(testOVF)},
		{name: "flatcar.vmdk", body: disk},
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flatcar.ova" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(ova)
	}))
	defer server.Close()

	t.Run("case 0: download is extracted into a fresh directory under the download dir", func(t *testing.T) {
		downloadDir := filepath.Join(t.TempDir(), "images")
		c := &Client{downloadDir: downloadDir}

		dir, ovfPath, err := c.downloadAndExtract(context.Background(), server.URL+"/flatcar.ova", "vmx-19")
		require.NoError(t, err)
		assert.Equal(t, downloadDir, filepath.Dir(dir))
		assert.Equal(t, filepath.Join(dir, "flatcar.ovf"), ovfPath)

		gotOVF, err := os.ReadFile(ovfPath)
		require.NoError(t, err)
		assert.Contains(t, string(gotOVF), "<vssd:VirtualSystemType>vmx-19</vssd:VirtualSystemType>")

		gotDisk, err := os.ReadFile(filepath.Join(dir, "flatcar.vmdk"))
		require.NoError(t, err)
		assert.Equal(t, disk, gotDisk)

		// No OVA copy may be left next to the extracted files.
		entries, err := os.ReadDir(downloadDir)
		require.NoError(t, err)
		assert.Len(t, entries, 1)
	})

	t.Run("case 1: failed download leaves nothing behind", func(t *testing.T) {
		downloadDir := filepath.Join(t.TempDir(), "images")
		c := &Client{downloadDir: downloadDir}

		_, _, err := c.downloadAndExtract(context.Background(), server.URL+"/missing.ova", "vmx-19")
		require.Error(t, err)

		entries, err := os.ReadDir(downloadDir)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestHardwareVersionString(t *testing.T) {
	assert.Equal(t, "", hardwareVersionString(0))
	assert.Equal(t, "vmx-19", hardwareVersionString(19))
}

//go:build integration

package testutil

import "encoding/binary"

// qcow2Magic is the 4-byte signature at the start of every qcow2 image
// ("QFI\xfb"). The fake Proxmox download-url handler validates the bytes it
// fetches from S3 against this, mirroring how a real Proxmox server would reject
// a non-image download.
var qcow2Magic = []byte{0x51, 0x46, 0x49, 0xfb}

// BuildQCOW2 returns a minimal but structurally-valid qcow2 image: the magic
// signature followed by the version field. Nothing inspects the disk contents -
// the fixture only needs to "look like" a qcow2 so the fake's magic-byte check
// passes (the Error spec seeds garbage bytes instead to exercise the failure
// path). This mirrors ova.go's philosophy for the vSphere suite.
func BuildQCOW2() []byte {
	header := make([]byte, 512)
	copy(header, qcow2Magic)
	// version 3, big-endian, immediately after the 4-byte magic.
	binary.BigEndian.PutUint32(header[4:], 3)
	return header
}

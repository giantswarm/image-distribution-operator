//go:build integration

package testutil

import (
	"archive/tar"
	"bytes"
	"fmt"
)

// diskFileName is the disk referenced by the OVF descriptor and must match the
// tar entry written into the archive.
const diskFileName = "flatcar-disk1.vmdk"

// ovfDescriptor is a minimal-but-structurally-valid OVF envelope. It is derived
// from govmomi's own ttylinux test fixture, trimmed to the smallest shape that
// govmomi's importer can parse and that vcsim's OvfManager accepts. The single
// network is named "nic0" so it lines up with the NetworkMapping the vSphere
// provider hard-codes (see pkg/vsphere/import.go).
var ovfDescriptor = fmt.Sprintf(`<Envelope
    xmlns="http://schemas.dmtf.org/ovf/envelope/1"
    xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1"
    xmlns:rasd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData"
    xmlns:vssd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_VirtualSystemSettingData">
  <References>
    <File ovf:href="%s" ovf:id="file1" ovf:size="1024"/>
  </References>
  <DiskSection>
    <Info>Virtual disk information</Info>
    <Disk ovf:capacity="1" ovf:capacityAllocationUnits="byte * 2^20" ovf:diskId="vmdisk1" ovf:fileRef="file1"
          ovf:format="http://www.vmware.com/interfaces/specifications/vmdk.html#streamOptimized"/>
  </DiskSection>
  <NetworkSection>
    <Info>The list of logical networks</Info>
    <Network ovf:name="nic0">
      <Description>The nic0 network</Description>
    </Network>
  </NetworkSection>
  <VirtualSystem ovf:id="vm">
    <Info>A virtual machine</Info>
    <Name>flatcar</Name>
    <OperatingSystemSection ovf:id="36">
      <Info>The kind of installed guest operating system</Info>
    </OperatingSystemSection>
    <VirtualHardwareSection>
      <Info>Virtual hardware requirements</Info>
      <System>
        <vssd:ElementName>Virtual Hardware Family</vssd:ElementName>
        <vssd:InstanceID>0</vssd:InstanceID>
        <vssd:VirtualSystemIdentifier>flatcar</vssd:VirtualSystemIdentifier>
        <vssd:VirtualSystemType>vmx-09</vssd:VirtualSystemType>
      </System>
      <Item>
        <rasd:Description>Number of Virtual CPUs</rasd:Description>
        <rasd:ElementName>1 virtual CPU(s)</rasd:ElementName>
        <rasd:InstanceID>1</rasd:InstanceID>
        <rasd:ResourceType>3</rasd:ResourceType>
        <rasd:VirtualQuantity>1</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:AllocationUnits>byte * 2^20</rasd:AllocationUnits>
        <rasd:Description>Memory Size</rasd:Description>
        <rasd:ElementName>32MB of memory</rasd:ElementName>
        <rasd:InstanceID>2</rasd:InstanceID>
        <rasd:ResourceType>4</rasd:ResourceType>
        <rasd:VirtualQuantity>32</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:Address>0</rasd:Address>
        <rasd:Description>IDE Controller</rasd:Description>
        <rasd:ElementName>ideController0</rasd:ElementName>
        <rasd:InstanceID>3</rasd:InstanceID>
        <rasd:ResourceType>5</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AddressOnParent>0</rasd:AddressOnParent>
        <rasd:ElementName>disk0</rasd:ElementName>
        <rasd:HostResource>ovf:/disk/vmdisk1</rasd:HostResource>
        <rasd:InstanceID>4</rasd:InstanceID>
        <rasd:Parent>3</rasd:Parent>
        <rasd:ResourceType>17</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AddressOnParent>1</rasd:AddressOnParent>
        <rasd:AutomaticAllocation>true</rasd:AutomaticAllocation>
        <rasd:Connection>nic0</rasd:Connection>
        <rasd:ElementName>ethernet0</rasd:ElementName>
        <rasd:InstanceID>5</rasd:InstanceID>
        <rasd:ResourceSubType>E1000</rasd:ResourceSubType>
        <rasd:ResourceType>10</rasd:ResourceType>
      </Item>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>`, diskFileName)

// BuildOVA returns an OVA (uncompressed tar) containing the OVF descriptor and a
// small dummy disk. It only needs to "look and behave like" an image: govmomi
// parses the descriptor and streams the disk bytes to vcsim over an NFC lease -
// the disk contents themselves are never inspected.
func BuildOVA() ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	entries := []struct {
		name string
		data []byte
	}{
		{name: "flatcar.ovf", data: []byte(ovfDescriptor)},
		{name: diskFileName, data: bytes.Repeat([]byte{0}, 1024)},
	}

	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data))}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("failed to write tar header for %s: %w", e.name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, fmt.Errorf("failed to write tar entry %s: %w", e.name, err)
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("failed to close tar writer: %w", err)
	}
	return buf.Bytes(), nil
}

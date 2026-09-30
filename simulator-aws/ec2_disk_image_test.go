package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func ec2PatternDisk(size int) []byte {
	disk := make([]byte, size)
	for i := range disk {
		disk[i] = byte(i*7 + i/ec2Sector)
	}
	return disk
}

func ec2ConvertForTest(t *testing.T, format string, image []byte) ([]byte, int64, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ebs.raw")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	size, convErr := ec2ConvertDiskImage(format, bytes.NewReader(image), int64(len(image)), f)
	if convErr == nil {
		convErr = f.Truncate(size)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return out, size, convErr
}

func ec2VHDFooter(diskSize uint64, diskType uint32, dataOffset uint64) []byte {
	footer := make([]byte, ec2Sector)
	copy(footer, "conectix")
	binary.BigEndian.PutUint64(footer[16:], dataOffset)
	binary.BigEndian.PutUint64(footer[48:], diskSize)
	binary.BigEndian.PutUint32(footer[60:], diskType)
	return footer
}

func TestEC2ConvertRawAndFixedVHD(t *testing.T) {
	disk := ec2PatternDisk(64 * 1024)
	for _, tc := range []struct {
		format string
		image  []byte
	}{
		{"RAW", disk},
		{"VHD", append(append([]byte{}, disk...), ec2VHDFooter(uint64(len(disk)), 2, 0xFFFFFFFFFFFFFFFF)...)},
	} {
		out, size, err := ec2ConvertForTest(t, tc.format, tc.image)
		if err != nil {
			t.Fatalf("%s: %v", tc.format, err)
		}
		if size != int64(len(disk)) || !bytes.Equal(out, disk) {
			t.Fatalf("%s converted to %d bytes (size %d), want the %d-byte disk", tc.format, len(out), size, len(disk))
		}
	}
}

// TestEC2ConvertDynamicVHD maps allocated blocks through the block allocation
// table, reads only the sectors a block's bitmap marks present, and leaves
// unallocated blocks zero.
func TestEC2ConvertDynamicVHD(t *testing.T) {
	const blockSize, blocks = 4096, 4
	diskSize := blockSize * blocks
	var image []byte
	image = append(image, ec2VHDFooter(uint64(diskSize), 3, ec2Sector)...)
	header := make([]byte, 1024)
	copy(header, "cxsparse")
	binary.BigEndian.PutUint64(header[16:], 1536)
	binary.BigEndian.PutUint32(header[28:], blocks)
	binary.BigEndian.PutUint32(header[32:], blockSize)
	image = append(image, header...)
	bat := make([]byte, ec2Sector)
	for i := range blocks {
		binary.BigEndian.PutUint32(bat[i*4:], 0xFFFFFFFF)
	}
	image = append(image, bat...)

	want := make([]byte, diskSize)
	writeBlock := func(index int, bitmap byte, data []byte) {
		binary.BigEndian.PutUint32(image[1536+index*4:], uint32(len(image)/ec2Sector))
		bm := make([]byte, ec2Sector)
		bm[0] = bitmap
		image = append(image, bm...)
		image = append(image, data...)
		for s := range blockSize / ec2Sector {
			if bitmap&(0x80>>s) != 0 {
				copy(want[index*blockSize+s*ec2Sector:], data[s*ec2Sector:(s+1)*ec2Sector])
			}
		}
	}
	writeBlock(0, 0xFF, ec2PatternDisk(blockSize))
	writeBlock(2, 0x80, bytes.Repeat([]byte{0xAB}, blockSize))
	image = append(image, ec2VHDFooter(uint64(diskSize), 3, ec2Sector)...)

	out, size, err := ec2ConvertForTest(t, "VHD", image)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(diskSize) || !bytes.Equal(out, want) {
		t.Fatalf("dynamic VHD converted to %d bytes (size %d) that differ from the disk", len(out), size)
	}
}

func ec2VMDKHeaderBytes(flags uint32, capacity, grainSize, gdOffset uint64, compress uint16) []byte {
	h := make([]byte, ec2Sector)
	binary.LittleEndian.PutUint32(h[0:], ec2VMDKMagic)
	binary.LittleEndian.PutUint32(h[4:], 3)
	binary.LittleEndian.PutUint32(h[8:], flags)
	binary.LittleEndian.PutUint64(h[12:], capacity)
	binary.LittleEndian.PutUint64(h[20:], grainSize)
	binary.LittleEndian.PutUint32(h[44:], 512)
	binary.LittleEndian.PutUint64(h[56:], gdOffset)
	binary.LittleEndian.PutUint16(h[77:], compress)
	return h
}

func ec2PadSector(b []byte) []byte {
	if rem := len(b) % ec2Sector; rem != 0 {
		b = append(b, make([]byte, ec2Sector-rem)...)
	}
	return b
}

// TestEC2ConvertStreamOptimizedVMDK reads compressed grains through the grain
// directory the footer header names.
func TestEC2ConvertStreamOptimizedVMDK(t *testing.T) {
	const grainSectors, grains = 8, 4
	grainBytes := grainSectors * ec2Sector
	diskSize := grainBytes * grains
	want := make([]byte, diskSize)
	flags := uint32(1 | ec2VMDKCompressedGrains | 1<<17)
	image := ec2VMDKHeaderBytes(flags, grains*grainSectors, grainSectors, ec2VMDKGDAtEnd, 1)
	image = append(image, ec2PadSector([]byte("# Disk DescriptorFile\ncreateType=\"streamOptimized\"\n"))...)
	gt := make([]byte, 512*4)
	for _, g := range []int{1, 3} {
		data := ec2PatternDisk(grainBytes)
		data[0] = byte(g)
		copy(want[g*grainBytes:], data)
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint32(gt[g*4:], uint32(len(image)/ec2Sector))
		marker := make([]byte, 12)
		binary.LittleEndian.PutUint64(marker, uint64(g*grainSectors))
		binary.LittleEndian.PutUint32(marker[8:], uint32(z.Len()))
		image = ec2PadSector(append(append(image, marker...), z.Bytes()...))
	}
	gtSector := len(image) / ec2Sector
	image = append(image, gt...)
	gd := make([]byte, ec2Sector)
	binary.LittleEndian.PutUint32(gd, uint32(gtSector))
	gdSector := len(image) / ec2Sector
	image = append(image, gd...)
	image = append(image, ec2VMDKHeaderBytes(flags, grains*grainSectors, grainSectors, uint64(gdSector), 1)...)
	image = append(image, make([]byte, ec2Sector)...)

	out, size, err := ec2ConvertForTest(t, "VMDK", image)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(diskSize) || !bytes.Equal(out, want) {
		t.Fatalf("stream-optimized VMDK converted to %d bytes (size %d) that differ from the disk", len(out), size)
	}
}

func TestEC2ConvertMonolithicSparseVMDK(t *testing.T) {
	const grainSectors, grains = 8, 2
	grainBytes := grainSectors * ec2Sector
	disk := ec2PatternDisk(grainBytes * grains)
	image := ec2VMDKHeaderBytes(1, grains*grainSectors, grainSectors, 1, 0)
	gd := make([]byte, ec2Sector)
	binary.LittleEndian.PutUint32(gd, 2)
	image = append(image, gd...)
	gt := make([]byte, 512*4)
	binary.LittleEndian.PutUint32(gt[0:], 6)
	binary.LittleEndian.PutUint32(gt[4:], 6+grainSectors)
	image = append(image, gt...)
	image = append(image, disk...)

	out, size, err := ec2ConvertForTest(t, "VMDK", image)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(disk)) || !bytes.Equal(out, disk) {
		t.Fatalf("monolithic sparse VMDK converted to %d bytes (size %d) that differ from the disk", len(out), size)
	}
}

func TestEC2ConvertRejectsAnImageNotInItsFormat(t *testing.T) {
	for _, format := range []string{"VMDK", "VHD"} {
		_, _, err := ec2ConvertForTest(t, format, ec2PatternDisk(4096))
		var diskErr *ec2DiskImageError
		if !errors.As(err, &diskErr) {
			t.Fatalf("%s: converting raw bytes returned %v, want a disk validation error", format, err)
		}
		if want := "ClientError: Disk validation failed [Unsupported " + format + " File Format]"; diskErr.statusMessage() != want {
			t.Fatalf("%s: status message %q, want %q", format, diskErr.statusMessage(), want)
		}
	}
}

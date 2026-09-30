package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"strings"
)

const ec2Sector = 512

// ec2DiskImageError is a disk image VM Import/Export cannot read in the
// format it was declared as.
type ec2DiskImageError struct{ detail string }

func (e *ec2DiskImageError) Error() string { return "disk validation failed [" + e.detail + "]" }

// statusMessage is how VM Import/Export reports the failure on the task.
func (e *ec2DiskImageError) statusMessage() string {
	return "ClientError: Disk validation failed [" + e.detail + "]"
}

func ec2DiskValidationError(detail string) error {
	return &ec2DiskImageError{detail: detail}
}

// ec2ConvertDiskImage writes the raw disk a VHD, VMDK or RAW image holds to
// dst and returns the disk's size in bytes.
func ec2ConvertDiskImage(format string, src io.ReaderAt, size int64, dst io.WriterAt) (int64, error) {
	switch strings.ToUpper(format) {
	case "RAW":
		if _, err := io.Copy(&ec2OffsetWriter{dst, 0}, io.NewSectionReader(src, 0, size)); err != nil {
			return 0, err
		}
		return size, nil
	case "VHD":
		return ec2ConvertVHD(src, size, dst)
	case "VMDK":
		return ec2ConvertVMDK(src, size, dst)
	}
	return 0, ec2DiskValidationError("Unsupported disk image format " + format)
}

type ec2OffsetWriter struct {
	w   io.WriterAt
	off int64
}

func (o *ec2OffsetWriter) Write(p []byte) (int, error) {
	n, err := o.w.WriteAt(p, o.off)
	o.off += int64(n)
	return n, err
}

func ec2ReadAt(src io.ReaderAt, off int64, n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := src.ReadAt(buf, off); err != nil {
		return nil, err
	}
	return buf, nil
}

// ec2ConvertVHD reads a fixed or dynamic VHD: the 512-byte footer that ends
// the file names the disk type and size, and a dynamic disk maps its blocks
// through the block allocation table its header points at.
func ec2ConvertVHD(src io.ReaderAt, size int64, dst io.WriterAt) (int64, error) {
	if size < ec2Sector {
		return 0, ec2DiskValidationError("Unsupported VHD File Format")
	}
	footer, err := ec2ReadAt(src, size-ec2Sector, ec2Sector)
	if err != nil {
		return 0, err
	}
	if string(footer[:8]) != "conectix" {
		return 0, ec2DiskValidationError("Unsupported VHD File Format")
	}
	diskSize := int64(binary.BigEndian.Uint64(footer[48:56]))
	switch binary.BigEndian.Uint32(footer[60:64]) {
	case 2:
		n := min(size-ec2Sector, diskSize)
		if _, err := io.Copy(&ec2OffsetWriter{dst, 0}, io.NewSectionReader(src, 0, n)); err != nil {
			return 0, err
		}
		return diskSize, nil
	case 3:
		return diskSize, ec2ConvertDynamicVHD(src, int64(binary.BigEndian.Uint64(footer[16:24])), dst)
	}
	return 0, ec2DiskValidationError("Unsupported VHD File Format")
}

func ec2ConvertDynamicVHD(src io.ReaderAt, headerOffset int64, dst io.WriterAt) error {
	header, err := ec2ReadAt(src, headerOffset, 1024)
	if err != nil {
		return err
	}
	if string(header[:8]) != "cxsparse" {
		return ec2DiskValidationError("Unsupported VHD File Format")
	}
	tableOffset := int64(binary.BigEndian.Uint64(header[16:24]))
	entries := int(binary.BigEndian.Uint32(header[28:32]))
	blockSize := int64(binary.BigEndian.Uint32(header[32:36]))
	if blockSize == 0 || blockSize%ec2Sector != 0 {
		return ec2DiskValidationError("Unsupported VHD File Format")
	}
	bat, err := ec2ReadAt(src, tableOffset, entries*4)
	if err != nil {
		return err
	}
	sectors := blockSize / ec2Sector
	bitmapSize := (sectors/8 + ec2Sector - 1) / ec2Sector * ec2Sector
	for i := range entries {
		entry := binary.BigEndian.Uint32(bat[i*4:])
		if entry == 0xFFFFFFFF {
			continue
		}
		blockStart := int64(entry) * ec2Sector
		bitmap, err := ec2ReadAt(src, blockStart, int(bitmapSize))
		if err != nil {
			return err
		}
		for s := int64(0); s < sectors; s++ {
			if bitmap[s/8]&(0x80>>(s%8)) == 0 {
				continue
			}
			data, err := ec2ReadAt(src, blockStart+bitmapSize+s*ec2Sector, ec2Sector)
			if err != nil {
				return err
			}
			if _, err := dst.WriteAt(data, int64(i)*blockSize+s*ec2Sector); err != nil {
				return err
			}
		}
	}
	return nil
}

// VMDK sparse extent header fields and flags.
const (
	ec2VMDKMagic            = 0x564d444b
	ec2VMDKCompressedGrains = 1 << 16
	ec2VMDKGDAtEnd          = 0xFFFFFFFFFFFFFFFF
)

type ec2VMDKHeader struct {
	flags         uint32
	capacity      uint64
	grainSize     uint64
	gtesPerGT     uint32
	gdOffset      uint64
	compressAlgo  uint16
	compressedFmt bool
}

func ec2ParseVMDKHeader(b []byte) (ec2VMDKHeader, bool) {
	if binary.LittleEndian.Uint32(b[0:4]) != ec2VMDKMagic {
		return ec2VMDKHeader{}, false
	}
	h := ec2VMDKHeader{
		flags:        binary.LittleEndian.Uint32(b[8:12]),
		capacity:     binary.LittleEndian.Uint64(b[12:20]),
		grainSize:    binary.LittleEndian.Uint64(b[20:28]),
		gtesPerGT:    binary.LittleEndian.Uint32(b[44:48]),
		gdOffset:     binary.LittleEndian.Uint64(b[56:64]),
		compressAlgo: binary.LittleEndian.Uint16(b[77:79]),
	}
	h.compressedFmt = h.flags&ec2VMDKCompressedGrains != 0
	return h, true
}

// ec2ConvertVMDK reads a monolithic sparse or stream-optimized VMDK extent
// through its grain directory and grain tables. A stream-optimized extent
// writes its grain directory last and records where in the footer copy of the
// header that precedes the end-of-stream marker.
func ec2ConvertVMDK(src io.ReaderAt, size int64, dst io.WriterAt) (int64, error) {
	if size < 2*ec2Sector {
		return 0, ec2DiskValidationError("Unsupported VMDK File Format")
	}
	first, err := ec2ReadAt(src, 0, ec2Sector)
	if err != nil {
		return 0, err
	}
	h, ok := ec2ParseVMDKHeader(first)
	if !ok || h.grainSize == 0 || h.gtesPerGT == 0 {
		return 0, ec2DiskValidationError("Unsupported VMDK File Format")
	}
	if h.gdOffset == ec2VMDKGDAtEnd {
		footer, err := ec2ReadAt(src, size-2*ec2Sector, ec2Sector)
		if err != nil {
			return 0, err
		}
		if h, ok = ec2ParseVMDKHeader(footer); !ok || h.gdOffset == ec2VMDKGDAtEnd {
			return 0, ec2DiskValidationError("Unsupported VMDK File Format")
		}
	}
	if h.compressedFmt && h.compressAlgo != 1 {
		return 0, ec2DiskValidationError("Unsupported VMDK File Format")
	}
	grainBytes := int64(h.grainSize) * ec2Sector
	grains := (h.capacity + h.grainSize - 1) / h.grainSize
	tables := (grains + uint64(h.gtesPerGT) - 1) / uint64(h.gtesPerGT)
	gd, err := ec2ReadAt(src, int64(h.gdOffset)*ec2Sector, int(tables)*4)
	if err != nil {
		return 0, err
	}
	for t := range tables {
		gtSector := binary.LittleEndian.Uint32(gd[t*4:])
		if gtSector == 0 {
			continue
		}
		gt, err := ec2ReadAt(src, int64(gtSector)*ec2Sector, int(h.gtesPerGT)*4)
		if err != nil {
			return 0, err
		}
		for e := range uint64(h.gtesPerGT) {
			grain := t*uint64(h.gtesPerGT) + e
			if grain >= grains {
				break
			}
			grainSector := binary.LittleEndian.Uint32(gt[e*4:])
			if grainSector <= 1 {
				continue
			}
			data, err := ec2ReadVMDKGrain(src, h, int64(grainSector)*ec2Sector, grainBytes)
			if err != nil {
				return 0, err
			}
			if _, err := dst.WriteAt(data, int64(grain)*grainBytes); err != nil {
				return 0, err
			}
		}
	}
	return int64(h.capacity) * ec2Sector, nil
}

func ec2ReadVMDKGrain(src io.ReaderAt, h ec2VMDKHeader, off, grainBytes int64) ([]byte, error) {
	if !h.compressedFmt {
		return ec2ReadAt(src, off, int(grainBytes))
	}
	marker, err := ec2ReadAt(src, off, 12)
	if err != nil {
		return nil, err
	}
	compressed, err := ec2ReadAt(src, off+12, int(binary.LittleEndian.Uint32(marker[8:12])))
	if err != nil {
		return nil, err
	}
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, ec2DiskValidationError("Unsupported VMDK File Format")
	}
	defer func() { _ = zr.Close() }()
	data, err := io.ReadAll(io.LimitReader(zr, grainBytes))
	if err != nil {
		return nil, ec2DiskValidationError("Unsupported VMDK File Format")
	}
	return data, nil
}

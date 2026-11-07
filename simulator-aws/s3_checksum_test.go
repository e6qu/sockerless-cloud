package main

import (
	"hash/crc64"
	"testing"
)

// CRC-64/NVME's check value, the checksum of "123456789", is
// 0xae8b14860a799888.
func TestS3CRC64NVMECheckValue(t *testing.T) {
	if got := crc64.Checksum([]byte("123456789"), s3CRC64NVMETable); got != 0xae8b14860a799888 {
		t.Fatalf("CRC-64/NVME check value = %#x", got)
	}
}

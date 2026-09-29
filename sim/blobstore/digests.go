package blobstore

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"hash/crc32"
)

// Digests are the length and checksums of one payload, computed in the single
// pass that stores it.
type Digests struct {
	Size   int64
	MD5    [md5.Size]byte
	CRC32C uint32
}

// MD5Hex is the MD5 in lower-case hex, the spelling entity tags use.
func (d Digests) MD5Hex() string { return hex.EncodeToString(d.MD5[:]) }

// MD5Base64 is the MD5 in standard base64, the spelling of Content-MD5.
func (d Digests) MD5Base64() string { return base64.StdEncoding.EncodeToString(d.MD5[:]) }

// CRC32CBase64 is the big-endian CRC32C in standard base64.
func (d Digests) CRC32CBase64() string {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], d.CRC32C)
	return base64.StdEncoding.EncodeToString(raw[:])
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

type digester struct {
	size  int64
	md5   hash.Hash
	crc32 hash.Hash32
}

func newDigester() *digester {
	return &digester{md5: md5.New(), crc32: crc32.New(castagnoli)}
}

func (d *digester) Write(p []byte) (int, error) {
	d.size += int64(len(p))
	_, _ = d.md5.Write(p)
	_, _ = d.crc32.Write(p)
	return len(p), nil
}

func (d *digester) sum() Digests {
	out := Digests{Size: d.size, CRC32C: d.crc32.Sum32()}
	copy(out.MD5[:], d.md5.Sum(nil))
	return out
}

// Digest returns the digests of data.
func Digest(data []byte) Digests {
	d := newDigester()
	_, _ = d.Write(data)
	return d.sum()
}

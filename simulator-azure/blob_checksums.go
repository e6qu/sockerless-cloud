package main

import (
	"encoding/base64"
	"encoding/binary"
	"hash"
	"hash/crc64"
	"net/http"
)

// blobCRC64Table is Azure Storage's CRC64: the reflected CRC-64 over the
// polynomial 0x9A6C9329AC4BC9B5, the table the Azure Storage SDKs build.
var blobCRC64Table = crc64.MakeTable(0x9A6C9329AC4BC9B5)

func blobCRC64(data []byte) uint64 { return crc64.Checksum(data, blobCRC64Table) }

func newBlobCRC64() hash.Hash64 { return crc64.New(blobCRC64Table) }

// blobCRC64Base64 spells a CRC64 the way x-ms-content-crc64 carries it: the
// eight little-endian bytes of the sum in standard base64.
func blobCRC64Base64(sum uint64) string {
	var raw [8]byte
	binary.LittleEndian.PutUint64(raw[:], sum)
	return base64.StdEncoding.EncodeToString(raw[:])
}

// Service versions at which the transactional checksum headers change.
const (
	blobVersionCRC64       = "2019-02-02"
	blobVersionBothDigests = "2026-10-06"
)

// blobRequestVersion is the service version a request executes against: its
// x-ms-version, else the signed version of the SAS it carries, else the
// account's DefaultServiceVersion. An empty result is the oldest version.
// Versions are ISO dates, so they compare as strings.
func blobRequestVersion(r *http.Request, account string) string {
	if v := r.Header.Get("x-ms-version"); v != "" {
		return v
	}
	if v := r.URL.Query().Get("sv"); v != "" {
		return v
	}
	return blobServiceProperties(account).DefaultServiceVersion
}

// blobChecksumHeaders names the pair of headers that state the MD5 and the
// CRC64 of the content a write carries: the request body's own, or, for the
// from-URL writes, the range read from the copy source.
type blobChecksumHeaders struct{ md5, crc64 string }

var (
	blobBodyChecksums   = blobChecksumHeaders{md5: "Content-MD5", crc64: "x-ms-content-crc64"}
	blobSourceChecksums = blobChecksumHeaders{md5: "x-ms-source-content-md5", crc64: "x-ms-source-content-crc64"}
)

// exclusive refuses a request that states both checksums, which Azure
// answers with 400.
func (h blobChecksumHeaders) exclusive(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get(h.md5) == "" || r.Header.Get(h.crc64) == "" {
		return true
	}
	writeStorageError(w, "InvalidHeaderValue",
		"The value for one of the HTTP headers is not in the correct format: "+h.crc64+".",
		http.StatusBadRequest)
	return false
}

// match checks the checksums a request states against the content that
// arrived, writing Md5Mismatch or Crc64Mismatch and returning false when one
// differs.
func (h blobChecksumHeaders) match(w http.ResponseWriter, r *http.Request, md5Base64 string, crc uint64) bool {
	if stated := r.Header.Get(h.md5); stated != "" && stated != md5Base64 {
		writeStorageError(w, "Md5Mismatch",
			"The MD5 value specified in the request did not match with the MD5 value calculated by the server.",
			http.StatusBadRequest)
		return false
	}
	stated := r.Header.Get(h.crc64)
	if stated == "" {
		return true
	}
	raw, err := base64.StdEncoding.DecodeString(stated)
	if err != nil || len(raw) != 8 {
		writeStorageError(w, "InvalidHeaderValue",
			"The value for one of the HTTP headers is not in the correct format: "+h.crc64+".",
			http.StatusBadRequest)
		return false
	}
	if binary.LittleEndian.Uint64(raw) != crc {
		writeStorageError(w, "Crc64Mismatch",
			"The CRC64 value specified in the request did not match with the CRC64 value calculated by the server.",
			http.StatusBadRequest)
		return false
	}
	return true
}

// respond answers a write with the checksums of the content it received, as
// the request's version answers them. Before 2019-02-02 the answer is the MD5
// alone. From then on the MD5 answers only a request that stated one and the
// CRC64 answers the others; from bothFrom on, when the operation has such a
// version, the CRC64 answers every request.
func (h blobChecksumHeaders) respond(w http.ResponseWriter, r *http.Request, account, md5Base64 string, crc uint64, bothFrom string) {
	version := blobRequestVersion(r, account)
	if version < blobVersionCRC64 {
		w.Header().Set("Content-MD5", md5Base64)
		return
	}
	statedMD5 := r.Header.Get(h.md5) != ""
	if statedMD5 {
		w.Header().Set("Content-MD5", md5Base64)
	}
	if !statedMD5 || (bothFrom != "" && version >= bothFrom) {
		w.Header().Set("x-ms-content-crc64", blobCRC64Base64(crc))
	}
}

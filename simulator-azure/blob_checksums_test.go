package main

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// Put Block answers with the checksums its request's version answers with.
func TestBlobPutBlockAnswersChecksumsByVersion(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container = "crcversionacct", "checked"
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusCreated, "CreateContainer")
	data := []byte("block contents")
	md5 := blobstore.Digest(data).MD5Base64()
	crc := blobCRC64Base64(blobCRC64(data))

	cases := []struct {
		version, statedMD5, wantMD5, wantCRC string
	}{
		{"2018-11-09", "", md5, ""},
		{"2025-01-05", "", "", crc},
		{"2025-01-05", md5, md5, ""},
		{"2026-10-06", md5, md5, crc},
	}
	for i, c := range cases {
		headers := map[string]string{"x-ms-version": c.version}
		if c.statedMD5 != "" {
			headers["Content-MD5"] = c.statedMD5
		}
		blockID := base64.StdEncoding.EncodeToString([]byte{byte('a' + i)})
		rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob",
			"/"+container+"/b?comp=block&blockid="+url.QueryEscape(blockID), data, headers)
		assertStatus(t, rec, http.StatusCreated, "PutBlock at "+c.version)
		if got := rec.Header().Get("Content-MD5"); got != c.wantMD5 {
			t.Errorf("version %s, Content-MD5 stated %q: Content-MD5 = %q, want %q", c.version, c.statedMD5, got, c.wantMD5)
		}
		if got := rec.Header().Get("x-ms-content-crc64"); got != c.wantCRC {
			t.Errorf("version %s, Content-MD5 stated %q: x-ms-content-crc64 = %q, want %q", c.version, c.statedMD5, got, c.wantCRC)
		}
	}
}

// A write that states both an MD5 and a CRC64 is refused, and one whose
// CRC64 differs from its content's is refused with Crc64Mismatch.
func TestBlobWritesRefuseBothChecksumsAndACRC64Mismatch(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container = "crcmismatchacct", "checked"
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusCreated, "CreateContainer")
	data := []byte("data")
	right := blobCRC64Base64(blobCRC64(data))
	wrong := blobCRC64Base64(blobCRC64(data) + 1)

	rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/b", data, map[string]string{
		"x-ms-blob-type": "BlockBlob", "Content-MD5": blobstore.Digest(data).MD5Base64(), "x-ms-content-crc64": right,
	})
	assertStatus(t, rec, http.StatusBadRequest, "PutBlob stating both checksums")

	rec = storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/b", data, map[string]string{
		"x-ms-blob-type": "BlockBlob", "x-ms-content-crc64": wrong,
	})
	assertStatus(t, rec, http.StatusBadRequest, "PutBlob with a wrong CRC64")
	if !strings.Contains(rec.Body.String(), "Crc64Mismatch") {
		t.Fatalf("PutBlob error = %s", rec.Body.String())
	}
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"/b", nil, nil),
		http.StatusNotFound, "GetBlob after a refused PutBlob")

	rec = storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/b", data, map[string]string{
		"x-ms-blob-type": "BlockBlob", "x-ms-content-crc64": right,
	})
	assertStatus(t, rec, http.StatusCreated, "PutBlob with the right CRC64")
	if got := rec.Header().Get("x-ms-content-crc64"); got != right {
		t.Fatalf("PutBlob x-ms-content-crc64 = %q, want %q", got, right)
	}
}

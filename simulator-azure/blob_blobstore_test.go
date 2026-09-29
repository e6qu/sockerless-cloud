package main

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// List Blobs counts blob prefixes and blobs together against maxresults, and
// its marker resumes between a blob and its own snapshots.
func TestBlobListPagesPrefixesAndSnapshotsTogether(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container = "listpageacct", "paged"
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusCreated, "CreateContainer")
	for _, name := range []string{"dir/a", "dir/b", "snap.txt", "z"} {
		assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/"+name, []byte(name),
			map[string]string{"x-ms-blob-type": "BlockBlob"}), http.StatusCreated, "PutBlob "+name)
	}
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/snap.txt?comp=snapshot", nil, nil),
		http.StatusCreated, "SnapshotBlob")

	type page struct {
		Blobs []struct {
			Name     string `xml:"Name"`
			Snapshot string `xml:"Snapshot"`
		} `xml:"Blobs>Blob"`
		Prefixes []struct {
			Name string `xml:"Name"`
		} `xml:"Blobs>BlobPrefix"`
		NextMarker string `xml:"NextMarker"`
	}
	var seen []string
	marker := ""
	for pages := 0; pages < 10; pages++ {
		target := "/" + container + "?restype=container&comp=list&delimiter=/&include=snapshots&maxresults=1"
		if marker != "" {
			target += "&marker=" + url.QueryEscape(marker)
		}
		rec := storagePlaneReq(t, srv, http.MethodGet, account, "blob", target, nil, nil)
		assertStatus(t, rec, http.StatusOK, "ListBlobs")
		var p page
		if err := xml.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if n := len(p.Blobs) + len(p.Prefixes); n != 1 {
			t.Fatalf("a page of maxresults=1 held %d entries", n)
		}
		for _, prefix := range p.Prefixes {
			seen = append(seen, prefix.Name)
		}
		for _, b := range p.Blobs {
			seen = append(seen, b.Name+map[bool]string{true: "@snapshot", false: ""}[b.Snapshot != ""])
		}
		if p.NextMarker == "" {
			break
		}
		marker = p.NextMarker
	}
	if got := strings.Join(seen, ","); got != "dir/,snap.txt,snap.txt@snapshot,z" {
		t.Fatalf("paged listing = %s", got)
	}
}

// Put Blob and Put Block check a stated Content-MD5 against what arrived.
func TestBlobWritesRefuseAContentMD5Mismatch(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container = "md5acct", "checked"
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusCreated, "CreateContainer")
	wrong := base64.StdEncoding.EncodeToString(make([]byte, 16))
	rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/b", []byte("data"),
		map[string]string{"x-ms-blob-type": "BlockBlob", "Content-MD5": wrong})
	assertStatus(t, rec, http.StatusBadRequest, "PutBlob with a wrong Content-MD5")
	if !strings.Contains(rec.Body.String(), "Md5Mismatch") {
		t.Fatalf("PutBlob error = %s", rec.Body.String())
	}
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"/b", nil, nil),
		http.StatusNotFound, "GetBlob after a refused PutBlob")
	right := blobstore.Digest([]byte("data")).MD5Base64()
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/b", []byte("data"),
		map[string]string{"x-ms-blob-type": "BlockBlob", "Content-MD5": right}), http.StatusCreated, "PutBlob")
	blockID := base64.StdEncoding.EncodeToString([]byte("block-1"))
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/c?comp=block&blockid="+url.QueryEscape(blockID),
		[]byte("data"), map[string]string{"Content-MD5": wrong}), http.StatusBadRequest, "PutBlock with a wrong Content-MD5")
}

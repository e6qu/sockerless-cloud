package main

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"testing"
)

// TestBlobStoresContentEncodedBytesAsSent proves Blob Storage keeps an upload's
// bytes as sent. Content-Encoding on Put Blob names the encoding the payload
// already carries: the service records it on the blob and returns it on Get
// Blob so the reader decodes, and it never decodes the body itself.
func TestBlobStoresContentEncodedBytesAsSent(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container = "encodingacct", "encoded-container"

	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write([]byte(`{"report":"quarterly"}`)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	payload := compressed.Bytes()

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusCreated, "CreateContainer")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/report.json", payload,
		map[string]string{
			"x-ms-blob-type":   "BlockBlob",
			"Content-Type":     "application/json",
			"Content-Encoding": "gzip",
			"Content-Language": "en-GB",
			"Cache-Control":    "max-age=300",
		}), http.StatusCreated, "PutBlob")

	rec := storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"/report.json", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetBlob")
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("GetBlob body = %q, want the gzip bytes as uploaded", rec.Body.Bytes())
	}
	for header, want := range map[string]string{
		"Content-Encoding": "gzip",
		"Content-Language": "en-GB",
		"Cache-Control":    "max-age=300",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Fatalf("GetBlob %s = %q, want %q", header, got, want)
		}
	}

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/log", nil,
		map[string]string{"x-ms-blob-type": "AppendBlob"}), http.StatusCreated, "PutBlob (append blob)")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/log?comp=appendblock", payload,
		map[string]string{"Content-Encoding": "gzip"}), http.StatusCreated, "AppendBlock")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"/log", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetBlob (append blob)")
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("appended bytes = %q, want the gzip bytes as sent", rec.Body.Bytes())
	}
}

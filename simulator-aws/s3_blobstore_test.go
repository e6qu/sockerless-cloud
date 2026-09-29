package main

import (
	"crypto/md5"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func s3TestServer(t *testing.T) func(method, target string, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	srv := buildResourcePolicySim(t)
	return func(method, target string, headers map[string]string, body string) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, reader)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return doReq(t, srv, req)
	}
}

// GetObject and HeadObject evaluate the conditional headers and a single
// Range the way S3 does, answering errors in S3's XML shape.
func TestS3GetObjectConditionsAndRanges(t *testing.T) {
	do := s3TestServer(t)
	if rr := do("PUT", "/reads", nil, ""); rr.Code != http.StatusOK {
		t.Fatalf("CreateBucket: %d %s", rr.Code, rr.Body.String())
	}
	put := do("PUT", "/reads/obj.txt", nil, "0123456789")
	etag := put.Header().Get("ETag")
	if put.Code != http.StatusOK || put.Body.Len() != 0 || etag != fmt.Sprintf(`"%x"`, md5.Sum([]byte("0123456789"))) {
		t.Fatalf("PutObject: %d ETag %s body %q", put.Code, etag, put.Body.String())
	}

	if rr := do("GET", "/reads/obj.txt", map[string]string{"If-None-Match": etag}, ""); rr.Code != http.StatusNotModified || rr.Body.Len() != 0 {
		t.Fatalf("If-None-Match on the current ETag: %d %q", rr.Code, rr.Body.String())
	}
	rr := do("GET", "/reads/obj.txt", map[string]string{"If-Match": `"other"`}, "")
	if rr.Code != http.StatusPreconditionFailed || !strings.Contains(rr.Body.String(), "<Code>PreconditionFailed</Code>") {
		t.Fatalf("If-Match on another ETag: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do("HEAD", "/reads/obj.txt", map[string]string{"If-None-Match": etag}, ""); rr.Code != http.StatusNotModified {
		t.Fatalf("HeadObject If-None-Match: %d", rr.Code)
	}
	rr = do("GET", "/reads/obj.txt", map[string]string{"Range": "bytes=-3"}, "")
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "789" || rr.Header().Get("Content-Range") != "bytes 7-9/10" {
		t.Fatalf("suffix range: %d %q %s", rr.Code, rr.Body.String(), rr.Header().Get("Content-Range"))
	}
	rr = do("GET", "/reads/obj.txt", map[string]string{"Range": "bytes=20-"}, "")
	if rr.Code != http.StatusRequestedRangeNotSatisfiable || !strings.Contains(rr.Body.String(), "<Code>InvalidRange</Code>") {
		t.Fatalf("unsatisfiable range: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do("GET", "/reads/obj.txt", map[string]string{"Range": "bytes=0-1,4-5"}, ""); rr.Code != http.StatusOK || rr.Body.String() != "0123456789" {
		t.Fatalf("a list of ranges: %d %q", rr.Code, rr.Body.String())
	}
	if rr := do("HEAD", "/reads/obj.txt", map[string]string{"Range": "bytes=2-3"}, ""); rr.Code != http.StatusPartialContent || rr.Header().Get("Content-Length") != "2" {
		t.Fatalf("HeadObject range: %d %v", rr.Code, rr.Header())
	}
}

// The SDKs and the CLI URL-encode the key in x-amz-copy-source; a copy of a
// key with a space in it names the key, not its encoding.
func TestS3CopySourceKeysAreURLDecoded(t *testing.T) {
	do := s3TestServer(t)
	do("PUT", "/copies", nil, "")
	if rr := do("PUT", "/copies/some%20key", nil, "payload"); rr.Code != http.StatusOK {
		t.Fatalf("PutObject: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do("PUT", "/copies/copied", map[string]string{"x-amz-copy-source": "/copies/some%20key"}, ""); rr.Code != http.StatusOK {
		t.Fatalf("CopyObject: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do("GET", "/copies/copied", nil, ""); rr.Body.String() != "payload" {
		t.Fatalf("the copy reads %q", rr.Body.String())
	}

	create := do("POST", "/copies/assembled?uploads", nil, "")
	var initiated struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(create.Body.Bytes(), &initiated); err != nil || initiated.UploadID == "" {
		t.Fatalf("CreateMultipartUpload: %d %s", create.Code, create.Body.String())
	}
	part := do("PUT", "/copies/assembled?partNumber=1&uploadId="+initiated.UploadID,
		map[string]string{"x-amz-copy-source": "/copies/some%20key", "x-amz-copy-source-range": "bytes=1-3"}, "")
	if part.Code != http.StatusOK || !strings.Contains(part.Body.String(), fmt.Sprintf("%x", md5.Sum([]byte("ayl")))) {
		t.Fatalf("UploadPartCopy: %d %s", part.Code, part.Body.String())
	}
	second := do("PUT", "/copies/assembled?partNumber=2&uploadId="+initiated.UploadID, nil, "tail")
	complete := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber></Part><Part><PartNumber>2</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`,
		second.Header().Get("ETag"))
	rr := do("POST", "/copies/assembled?uploadId="+initiated.UploadID, nil, complete)
	partMD5s := md5.Sum([]byte("ayl"))
	tailMD5 := md5.Sum([]byte("tail"))
	want := fmt.Sprintf(`%x-2`, md5.Sum(append(partMD5s[:], tailMD5[:]...)))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), want) {
		t.Fatalf("CompleteMultipartUpload: %d %s, want ETag %s", rr.Code, rr.Body.String(), want)
	}
	if rr := do("GET", "/copies/assembled", nil, ""); rr.Body.String() != "ayltail" {
		t.Fatalf("the assembled object reads %q", rr.Body.String())
	}
}

// A listing resumed after a key does not skip the longer keys it prefixes,
// and one resumed after a common prefix does not return it again.
func TestS3ListingResumesPastItsMarker(t *testing.T) {
	do := s3TestServer(t)
	do("PUT", "/listing", nil, "")
	for _, key := range []string{"a/1", "a/2", "abc", "abcd", "b"} {
		do("PUT", "/listing/"+key, nil, key)
	}
	rr := do("GET", "/listing?list-type=2&delimiter=/&start-after=abc", nil, "")
	if !strings.Contains(rr.Body.String(), "<Key>abcd</Key>") {
		t.Fatalf("start-after=abc skipped abcd: %s", rr.Body.String())
	}
	var seen []string
	marker := ""
	for pages := 0; pages < 10; pages++ {
		rr := do("GET", "/listing?delimiter=/&max-keys=1&marker="+marker, nil, "")
		var page s3ListBucketResultV1
		if err := xml.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, p := range page.CommonPrefixes {
			seen = append(seen, p.Prefix)
		}
		for _, c := range page.Contents {
			seen = append(seen, c.Key)
		}
		if !page.IsTruncated {
			break
		}
		marker = page.NextMarker
	}
	if got := strings.Join(seen, ","); got != "a/,abc,abcd,b" {
		t.Fatalf("paged V1 listing = %s", got)
	}
}

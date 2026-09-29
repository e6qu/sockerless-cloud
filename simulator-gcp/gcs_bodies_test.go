package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type gcsTestClient struct {
	t     *testing.T
	base  string
	token string
	root  string
}

func newGCSTestClient(t *testing.T) *gcsTestClient {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SIM_GCS_DATA_DIR", root)
	_, base := arTestServer(t)
	now := time.Now()
	return &gcsTestClient{t: t, base: base, root: root,
		token: "Bearer " + arTestAccessToken("storage-tester@example.com", now, now.Add(time.Hour))}
}

func (c *gcsTestClient) do(method, path string, headers map[string]string, body []byte) (*http.Response, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", c.token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp, out
}

func (c *gcsTestClient) createBucket(name string, retentionSeconds string) {
	c.t.Helper()
	spec := map[string]any{"name": name}
	if retentionSeconds != "" {
		spec["softDeletePolicy"] = map[string]any{"retentionDurationSeconds": retentionSeconds}
	}
	body, _ := json.Marshal(spec)
	if resp, out := c.do(http.MethodPost, "/storage/v1/b?project=p", map[string]string{"Content-Type": "application/json"}, body); resp.StatusCode != http.StatusOK {
		c.t.Fatalf("create bucket: %d %s", resp.StatusCode, out)
	}
}

func (c *gcsTestClient) upload(bucket, name, data string) map[string]any {
	c.t.Helper()
	resp, out := c.do(http.MethodPost, "/upload/storage/v1/b/"+bucket+"/o?uploadType=media&name="+url.QueryEscape(name),
		map[string]string{"Content-Type": "text/plain"}, []byte(data))
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("upload %s: %d %s", name, resp.StatusCode, out)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		c.t.Fatal(err)
	}
	return obj
}

func (c *gcsTestClient) download(bucket, name string) string {
	c.t.Helper()
	resp, out := c.do(http.MethodGet, "/storage/v1/b/"+bucket+"/o/"+url.PathEscape(name)+"?alt=media", nil, nil)
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("download %s: %d %s", name, resp.StatusCode, out)
	}
	return string(out)
}

func (c *gcsTestClient) remove(bucket, name string) {
	c.t.Helper()
	if resp, out := c.do(http.MethodDelete, "/storage/v1/b/"+bucket+"/o/"+url.PathEscape(name), nil, nil); resp.StatusCode != http.StatusNoContent {
		c.t.Fatalf("delete %s: %d %s", name, resp.StatusCode, out)
	}
}

// Every generation keeps bytes of its own, so restoring a soft-deleted
// generation serves what it held even after later generations of the name
// were written and deleted.
func TestGCSRestoreServesTheRestoredGenerationsBytes(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("restore-bytes", "")
	first := c.upload("restore-bytes", "report.txt", "first generation")
	c.remove("restore-bytes", "report.txt")
	c.upload("restore-bytes", "report.txt", "second generation, longer")
	c.remove("restore-bytes", "report.txt")

	resp, out := c.do(http.MethodPost, fmt.Sprintf("/storage/v1/b/restore-bytes/o/report.txt/restore?generation=%s", first["generation"]), nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore: %d %s", resp.StatusCode, out)
	}
	if got := c.download("restore-bytes", "report.txt"); got != "first generation" {
		t.Fatalf("the restored generation serves %q", got)
	}
	mirrored, err := os.ReadFile(filepath.Join(c.root, "restore-bytes", "report.txt"))
	if err != nil || string(mirrored) != "first generation" {
		t.Fatalf("the bucket's host directory holds %q, %v", mirrored, err)
	}
}

// An overwrite under a soft-delete policy retires the generation it
// replaces, as a delete does.
func TestGCSOverwriteSoftDeletesTheReplacedGeneration(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("overwrite-retires", "")
	first := c.upload("overwrite-retires", "a.txt", "one")
	c.upload("overwrite-retires", "a.txt", "two")
	resp, out := c.do(http.MethodGet, "/storage/v1/b/overwrite-retires/o?softDeleted=true", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list soft-deleted: %d %s", resp.StatusCode, out)
	}
	var listing struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out, &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Items) != 1 || listing.Items[0]["generation"] != first["generation"] {
		t.Fatalf("soft-deleted listing = %+v", listing.Items)
	}
}

// A reader holding a row an overwrite replaced serves the bytes of the row it
// ends up describing, never the new bytes under the old digests.
func TestGCSOpenObjectFollowsAnOverwriteItRaced(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("overwrite-race", "0")
	c.upload("overwrite-race", "race.bin", "old contents")
	stale, ok := gcsObjects.Get("overwrite-race/race.bin")
	if !ok {
		t.Fatal("the object is not stored")
	}
	c.upload("overwrite-race", "race.bin", "the new, longer contents")
	current, reader, err := gcsOpenObject(stale)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "the new, longer contents" || current.Size != fmt.Sprint(len(data)) || current.Generation == stale.Generation {
		t.Fatalf("served %q as generation %s of size %s", data, current.Generation, current.Size)
	}
}

// The bucket's host directory, which Cloud Run mounts, shows the live
// generation of each object and nothing that resolves outside it.
func TestGCSHostDirectoryMirrorsLiveObjects(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("mirror", "0")
	c.upload("mirror", "dir/file.txt", "v1")
	c.upload("mirror", "dir/file.txt", "v2")
	path := filepath.Join(c.root, "mirror", "dir", "file.txt")
	if data, err := os.ReadFile(path); err != nil || string(data) != "v2" {
		t.Fatalf("mirror = %q, %v", data, err)
	}
	// A workload writing through the mount changes its copy only.
	if err := os.WriteFile(path, []byte("written by a workload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := c.download("mirror", "dir/file.txt"); got != "v2" {
		t.Fatalf("a write through the mount reached the object: %q", got)
	}
	c.remove("mirror", "dir/file.txt")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a deleted object stayed in the host directory: %v", err)
	}
	c.upload("mirror", "../escape.txt", "outside")
	if _, err := os.Stat(filepath.Join(c.root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("an object name wrote outside its bucket: %v", err)
	}
	if got := c.download("mirror", "../escape.txt"); got != "outside" {
		t.Fatalf("the object reads %q", got)
	}
}

// maxResults bounds items and prefixes together, and the page token resumes
// past both.
func TestGCSListPagesPrefixesWithItems(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("paging", "0")
	for _, name := range []string{"a/1", "a/2", "b", "c/1", "d"} {
		c.upload("paging", name, name)
	}
	var seen []string
	token := ""
	for pages := 0; ; pages++ {
		path := "/storage/v1/b/paging/o?delimiter=/&maxResults=2"
		if token != "" {
			path += "&pageToken=" + url.QueryEscape(token)
		}
		resp, out := c.do(http.MethodGet, path, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list: %d %s", resp.StatusCode, out)
		}
		var page struct {
			Items         []map[string]any `json:"items"`
			Prefixes      []string         `json:"prefixes"`
			NextPageToken string           `json:"nextPageToken"`
		}
		if err := json.Unmarshal(out, &page); err != nil {
			t.Fatal(err)
		}
		if n := len(page.Items) + len(page.Prefixes); n > 2 {
			t.Fatalf("a page of maxResults=2 held %d entries", n)
		}
		seen = append(seen, page.Prefixes...)
		for _, item := range page.Items {
			seen = append(seen, item["name"].(string))
		}
		if page.NextPageToken == "" {
			break
		}
		if pages > 5 {
			t.Fatal("paging did not end")
		}
		token = page.NextPageToken
	}
	if got := strings.Join(seen, ","); got != "a/,b,c/,d" {
		t.Fatalf("listed %s", got)
	}
}

// A resumable upload stages its chunks in a payload; the status query before
// any byte arrives names no range, and the final chunk makes the object.
func TestGCSResumableUploadStagesChunks(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("resumable", "0")
	resp, out := c.do(http.MethodPost, "/upload/storage/v1/b/resumable/o?uploadType=resumable&name=big.bin",
		map[string]string{"Content-Type": "application/json"}, []byte(`{}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initiate: %d %s", resp.StatusCode, out)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	session := location.RequestURI()
	resp, out = c.do(http.MethodPut, session, map[string]string{"Content-Range": "bytes */10"}, nil)
	if resp.StatusCode != 308 || resp.Header.Get("Range") != "" {
		t.Fatalf("status query: %d Range=%q %s", resp.StatusCode, resp.Header.Get("Range"), out)
	}
	resp, out = c.do(http.MethodPut, session, map[string]string{"Content-Range": "bytes 0-5/10"}, []byte("012345"))
	if resp.StatusCode != 308 || resp.Header.Get("Range") != "bytes=0-5" {
		t.Fatalf("first chunk: %d Range=%q %s", resp.StatusCode, resp.Header.Get("Range"), out)
	}
	resp, out = c.do(http.MethodPut, session, map[string]string{"Content-Range": "bytes 6-9/10"}, []byte("6789"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("last chunk: %d %s", resp.StatusCode, out)
	}
	if got := c.download("resumable", "big.bin"); got != "0123456789" {
		t.Fatalf("the object holds %q", got)
	}

	resp, out = c.do(http.MethodPost, "/upload/storage/v1/b/resumable/o?uploadType=resumable&name=cancelled.bin",
		map[string]string{"Content-Type": "application/json"}, []byte(`{}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initiate: %d %s", resp.StatusCode, out)
	}
	location, _ = url.Parse(resp.Header.Get("Location"))
	c.do(http.MethodPut, location.RequestURI(), map[string]string{"Content-Range": "bytes 0-2/10"}, []byte("abc"))
	if resp, out := c.do(http.MethodDelete, location.RequestURI(), nil, nil); resp.StatusCode != 499 {
		t.Fatalf("cancel: %d %s", resp.StatusCode, out)
	}
	if resp, _ := c.do(http.MethodPut, location.RequestURI(), map[string]string{"Content-Range": "bytes */10"}, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a cancelled session answered %d", resp.StatusCode)
	}
}

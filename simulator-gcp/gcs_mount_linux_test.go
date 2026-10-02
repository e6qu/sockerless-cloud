//go:build linux

package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// mount acquires bucket the way a Cloud Run workload mounting it writable
// does, and returns the bucket's host directory.
func (c *gcsTestClient) mount(bucket string) string {
	c.t.Helper()
	if err := gcsMountAcquire(bucket); err != nil {
		c.t.Fatalf("mount %s: %v", bucket, err)
	}
	c.t.Cleanup(func() { gcsMountRelease(bucket) })
	return filepath.Join(c.root, bucket)
}

func (c *gcsTestClient) object(bucket, name string) (map[string]any, bool) {
	c.t.Helper()
	resp, out := c.do(http.MethodGet, "/storage/v1/b/"+bucket+"/o/"+url.PathEscape(name), nil, nil)
	if resp.StatusCode == http.StatusNotFound {
		return nil, false
	}
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("get %s: %d %s", name, resp.StatusCode, out)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		c.t.Fatal(err)
	}
	return obj, true
}

func (c *gcsTestClient) names(bucket string) []string {
	c.t.Helper()
	resp, out := c.do(http.MethodGet, "/storage/v1/b/"+bucket+"/o", nil, nil)
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("list %s: %d %s", bucket, resp.StatusCode, out)
	}
	var listing struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &listing); err != nil {
		c.t.Fatal(err)
	}
	names := make([]string, 0, len(listing.Items))
	for _, item := range listing.Items {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	return names
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A file a workload closes after writing becomes a new generation, an
// overwrite keeps the object's metadata, a directory it makes gets Cloud
// Storage FUSE's placeholder object, and every one records the file's
// modification time.
func TestGCSMountIngestsWritesOnClose(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("mount-writes", "0")
	seed := c.upload("mount-writes", "seed.txt", "from the API")
	dir := c.mount("mount-writes")

	writeFile(t, filepath.Join(dir, "seed.txt"), "rewritten by the workload")
	if err := os.Mkdir(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "out", "result.json"), `{"ok":true}`)

	if got := c.download("mount-writes", "seed.txt"); got != "rewritten by the workload" {
		t.Fatalf("seed.txt reads %q", got)
	}
	rewritten, _ := c.object("mount-writes", "seed.txt")
	if rewritten["generation"] == seed["generation"] {
		t.Fatalf("the overwrite kept generation %v", seed["generation"])
	}
	if rewritten["contentType"] != "text/plain" {
		t.Fatalf("the overwrite changed contentType to %v", rewritten["contentType"])
	}
	metadata, _ := rewritten["metadata"].(map[string]any)
	if _, err := time.Parse(time.RFC3339Nano, metadata[gcsMtimeMetadataKey].(string)); err != nil {
		t.Fatalf("metadata %v: %v", metadata, err)
	}
	if got := c.download("mount-writes", "out/result.json"); got != `{"ok":true}` {
		t.Fatalf("out/result.json reads %q", got)
	}
	result, _ := c.object("mount-writes", "out/result.json")
	if result["contentType"] != "application/json" {
		t.Fatalf("a new file's contentType is %v, want the one its extension names", result["contentType"])
	}
	placeholder, ok := c.object("mount-writes", "out/")
	if !ok || placeholder["size"] != "0" {
		t.Fatalf("the directory's placeholder is %v", placeholder)
	}
	if got := strings.Join(c.names("mount-writes"), ","); got != "out/,out/result.json,seed.txt" {
		t.Fatalf("the bucket lists %s", got)
	}
}

// Cloud Storage FUSE writes the object when the file is closed, never part
// way through a write, and a close of a file nobody wrote to writes nothing.
func TestGCSMountWritesOnlyOnCloseAfterWrite(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("mount-close", "0")
	kept := c.upload("mount-close", "kept.txt", "unchanged")
	dir := c.mount("mount-close")

	file, err := os.Create(filepath.Join(dir, "partial.log"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("first half, "); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.object("mount-close", "partial.log"); ok {
		t.Fatal("a file still open for writing is already an object")
	}
	if _, err := file.WriteString("second half"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if got := c.download("mount-close", "partial.log"); got != "first half, second half" {
		t.Fatalf("partial.log reads %q", got)
	}

	untouched, err := os.OpenFile(filepath.Join(dir, "kept.txt"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := untouched.Close(); err != nil {
		t.Fatal(err)
	}
	if obj, _ := c.object("mount-close", "kept.txt"); obj["generation"] != kept["generation"] {
		t.Fatalf("closing kept.txt unwritten moved it from generation %v to %v", kept["generation"], obj["generation"])
	}
}

// Removing a file deletes its object, removing a directory deletes its
// placeholder, and renames copy objects to their new names.
func TestGCSMountRemovesAndRenames(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("mount-renames", "0")
	c.upload("mount-renames", "gone.txt", "doomed")
	c.upload("mount-renames", "old-name.txt", "renamed")
	c.upload("mount-renames", "tree/a.txt", "a")
	c.upload("mount-renames", "tree/sub/b.txt", "b")
	dir := c.mount("mount-renames")

	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "old-name.txt"), filepath.Join(dir, "new-name.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "tree"), filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.names("mount-renames"), ","); got != "empty/,moved/a.txt,moved/sub/b.txt,new-name.txt" {
		t.Fatalf("the bucket lists %s", got)
	}
	renamed, _ := c.object("mount-renames", "new-name.txt")
	if renamed["contentType"] != "text/plain" || c.download("mount-renames", "new-name.txt") != "renamed" {
		t.Fatalf("the renamed object is %v", renamed)
	}
	if err := os.Remove(filepath.Join(dir, "empty")); err != nil {
		t.Fatal(err)
	}
	// A file written in a renamed directory lands under its new name.
	writeFile(t, filepath.Join(dir, "moved", "sub", "c.txt"), "c")
	if got := strings.Join(c.names("mount-renames"), ","); got != "moved/a.txt,moved/sub/b.txt,moved/sub/c.txt,new-name.txt" {
		t.Fatalf("the bucket lists %s", got)
	}
}

// A file renamed out of the mount is gone from the bucket; one renamed in is
// written to it.
func TestGCSMountMovesInAndOut(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("mount-moves", "0")
	c.upload("mount-moves", "leaving.txt", "leaving")
	dir := c.mount("mount-moves")
	outside := filepath.Join(c.root, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(outside, "arriving.txt"), "arriving")

	if err := os.Rename(filepath.Join(dir, "leaving.txt"), filepath.Join(outside, "leaving.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(outside, "arriving.txt"), filepath.Join(dir, "arriving.txt")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.names("mount-moves"), ","); got != "arriving.txt" {
		t.Fatalf("the bucket lists %s", got)
	}
	if got := c.download("mount-moves", "arriving.txt"); got != "arriving" {
		t.Fatalf("arriving.txt reads %q", got)
	}
}

// An object written through the API while the bucket is mounted updates the
// mounted file and is not written back as another generation; a symbolic link
// a workload makes is never followed.
func TestGCSMountKeepsAPIWritesAndIgnoresLinks(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("mount-api", "0")
	dir := c.mount("mount-api")

	written := c.upload("mount-api", "docs/api.txt", "through the API")
	if data, err := os.ReadFile(filepath.Join(dir, "docs", "api.txt")); err != nil || string(data) != "through the API" {
		t.Fatalf("the mount shows %q, %v", data, err)
	}
	secret := filepath.Join(c.root, "secret.txt")
	writeFile(t, secret, "outside the bucket")
	if err := os.Symlink(secret, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if obj, _ := c.object("mount-api", "docs/api.txt"); obj["generation"] != written["generation"] {
		t.Fatalf("the API's generation %v became %v", written["generation"], obj["generation"])
	}
	if got := strings.Join(c.names("mount-api"), ","); got != "docs/api.txt" {
		t.Fatalf("the bucket lists %s", got)
	}
}

// A write made while no workload mounts the bucket stays out of it; the next
// mount writes the file no live object accounts for.
func TestGCSMountAdoptsStrayFilesWhenMounted(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("mount-adopt", "0")
	c.upload("mount-adopt", "live.txt", "live")
	dir := filepath.Join(c.root, "mount-adopt")
	writeFile(t, filepath.Join(dir, "stray.txt"), "stray")
	if got := strings.Join(c.names("mount-adopt"), ","); got != "live.txt" {
		t.Fatalf("an unmounted write reached the bucket: %s", got)
	}
	c.mount("mount-adopt")
	if got := c.download("mount-adopt", "stray.txt"); got != "stray" {
		t.Fatalf("stray.txt reads %q", got)
	}
	if got := strings.Join(c.names("mount-adopt"), ","); got != "live.txt,stray.txt" {
		t.Fatalf("the bucket lists %s", got)
	}
}

// After an event overflow, the mount is reconciled with its directory.
func TestGCSMountResyncReconcilesTheDirectory(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("mount-resync", "0")
	c.upload("mount-resync", "kept.txt", "kept")
	c.upload("mount-resync", "dropped.txt", "dropped")
	dir := c.mount("mount-resync")
	gcsMountMu.Lock()
	gcsMountW.unwatchLocked(gcsMountPath{"mount-resync", ""})
	gcsMountMu.Unlock()
	if err := os.Remove(filepath.Join(dir, "dropped.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "unseen", "new.txt"), "new")
	gcsMountW.resync()
	if got := strings.Join(c.names("mount-resync"), ","); got != "kept.txt,unseen/,unseen/new.txt" {
		t.Fatalf("the bucket lists %s", got)
	}
}

func TestGCSMountOnlyDir(t *testing.T) {
	for _, tc := range []struct {
		options []string
		want    string
		fails   bool
	}{
		{nil, "", false},
		{[]string{"implicit-dirs", "only-dir=images"}, "images", false},
		{[]string{"only-dir=a/b/"}, "a/b", false},
		{[]string{"only-dir=../escape"}, "", true},
		{[]string{"only-dir="}, "", true},
	} {
		got, err := gcsMountOnlyDir(tc.options)
		if (err != nil) != tc.fails || got != tc.want {
			t.Errorf("gcsMountOnlyDir(%q) = %q, %v", tc.options, got, err)
		}
	}
}

// A volume binds the bucket's directory, or the only-dir directory in it,
// read-only when the volume is, and refuses a bucket that does not exist.
func TestCloudRunGCSBinds(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("bind-source", "0")
	volumes := map[string]Volume{
		"whole":   {Name: "whole", Gcs: &GcsVolumeSource{Bucket: "bind-source", ReadOnly: true}},
		"images":  {Name: "images", Gcs: &GcsVolumeSource{Bucket: "bind-source", MountOptions: []string{"only-dir=images"}}},
		"missing": {Name: "missing", Gcs: &GcsVolumeSource{Bucket: "no-such-bucket"}},
	}
	binds, writable, err := cloudRunGCSBinds(volumes, Container{VolumeMounts: []VolumeMount{
		{Name: "whole", MountPath: "/mnt/whole"},
		{Name: "images", MountPath: "/mnt/images"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(c.root, "bind-source")
	want := []string{root + ":/mnt/whole:ro", filepath.Join(root, "images") + ":/mnt/images"}
	if strings.Join(binds, " ") != strings.Join(want, " ") || strings.Join(writable, ",") != "bind-source" {
		t.Fatalf("binds %q, writable %q", binds, writable)
	}
	if info, err := os.Stat(filepath.Join(root, "images")); err != nil || !info.IsDir() {
		t.Fatalf("the only-dir directory is missing: %v", err)
	}
	if _, _, err := cloudRunGCSBinds(volumes, Container{VolumeMounts: []VolumeMount{{Name: "missing", MountPath: "/mnt/x"}}}); err == nil {
		t.Fatal("a volume of a missing bucket mounted")
	}
}

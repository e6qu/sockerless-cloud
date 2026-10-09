package main

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// A Cloud Run workload mounts a bucket's host directory the way Cloud Storage
// FUSE mounts the bucket. While a workload mounts it writable, the simulator
// keeps a view of what each path in the directory stands for, and a watch on
// the directory ingests what the workload writes, removes and renames as the
// object changes Cloud Storage FUSE makes.

// gcsMountMu guards gcsMountViews and orders every change the simulator makes
// to a bucket's host directory against the watch reading it: the simulator
// changes a path and records what it put there as one step, so the watch
// never mistakes the simulator's own change for a workload's.
var gcsMountMu sync.Mutex

// gcsMountViews holds the view of each bucket a workload mounts writable.
var gcsMountViews = map[string]*gcsMountView{}

// gcsMountsWatched counts the buckets in gcsMountViews, so a request that
// touches no mounted bucket costs nothing.
var gcsMountsWatched atomic.Int32

// gcsMountEntry is what a path in a mounted bucket's directory stands for: a
// file holding generation of the object of its name, or a directory, whose
// generation is that of its placeholder object (the name with a trailing
// slash), or 0 when no object but those under it implies the directory.
type gcsMountEntry struct {
	dir        bool
	generation int64
	ino        uint64
	size       int64
	mtime      time.Time
}

func gcsMountEntryOf(info os.FileInfo, generation int64) gcsMountEntry {
	entry := gcsMountEntry{dir: info.IsDir(), generation: generation, size: info.Size(), mtime: info.ModTime()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		entry.ino = stat.Ino
	}
	return entry
}

// gcsMountRecordFileLocked records that the simulator put generation of the
// object name at path.
func gcsMountRecordFileLocked(bucket, name string, generation int64, path string) {
	view := gcsMountViews[bucket]
	if view == nil {
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		delete(view.entries, name)
		return
	}
	view.entries[name] = gcsMountEntryOf(info, generation)
}

// gcsMountRecordDirLocked records that rel is a directory, keeping the
// generation of its placeholder when the record already holds this directory.
func gcsMountRecordDirLocked(bucket, rel, path string) {
	view := gcsMountViews[bucket]
	if view == nil {
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return
	}
	entry := gcsMountEntryOf(info, 0)
	if existing, ok := view.entries[rel]; ok && existing.dir && existing.ino == entry.ino {
		entry.generation = existing.generation
	}
	view.entries[rel] = entry
}

func gcsMountSetDirGenerationLocked(bucket, rel string, generation int64) {
	view := gcsMountViews[bucket]
	if view == nil {
		return
	}
	if entry, ok := view.entries[rel]; ok && entry.dir {
		entry.generation = generation
		view.entries[rel] = entry
	}
}

func gcsMountForgetLocked(bucket, rel string) {
	if view := gcsMountViews[bucket]; view != nil {
		delete(view.entries, rel)
	}
}

// gcsMountResetLocked forgets everything under a bucket's directory, which
// gcsResetBucketHostDir has just emptied.
func gcsMountResetLocked(bucket string) {
	view := gcsMountViews[bucket]
	if view == nil {
		return
	}
	root := view.entries[""]
	view.entries = map[string]gcsMountEntry{"": root}
}

// gcsMountOnlyDir reads the only-dir Cloud Storage FUSE option from a
// volume's mountOptions: the directory of the bucket the volume mounts in
// place of the whole bucket. Cloud Run passes every other option to Cloud
// Storage FUSE, whose file system the host directory stands in for.
func gcsMountOnlyDir(options []string) (string, error) {
	onlyDir := ""
	for _, option := range options {
		name, value, _ := strings.Cut(strings.TrimSpace(option), "=")
		if strings.TrimLeft(name, "-") != "only-dir" {
			continue
		}
		dir := strings.Trim(value, "/")
		if dir == "" || !filepath.IsLocal(filepath.FromSlash(dir)) || path.Clean(dir) != dir {
			return "", fmt.Errorf("mount option %q names no directory of the bucket", option)
		}
		onlyDir = dir
	}
	return onlyDir, nil
}

// cloudRunGCSBinds returns the binds that mount c's Cloud Storage volumes
// and the buckets c mounts writable.
func cloudRunGCSBinds(volumes map[string]Volume, c Container) (binds, writable []string, err error) {
	for _, mp := range c.VolumeMounts {
		v, ok := volumes[mp.Name]
		if !ok || v.Gcs == nil || v.Gcs.Bucket == "" {
			continue
		}
		if _, ok := gcsBuckets.Get(v.Gcs.Bucket); !ok {
			return nil, nil, fmt.Errorf("volume %q mounts Cloud Storage bucket %q, which does not exist", v.Name, v.Gcs.Bucket)
		}
		onlyDir, err := gcsMountOnlyDir(v.Gcs.MountOptions)
		if err != nil {
			return nil, nil, fmt.Errorf("volume %q: %w", v.Name, err)
		}
		source := GCSBucketHostDir(v.Gcs.Bucket)
		if onlyDir != "" {
			source = filepath.Join(source, filepath.FromSlash(onlyDir))
			gcsMountMu.Lock()
			err := gcsMirrorDirectory(v.Gcs.Bucket, source)
			gcsMountMu.Unlock()
			if err != nil {
				return nil, nil, fmt.Errorf("volume %q: %w", v.Name, err)
			}
		}
		bind := source + ":" + mp.MountPath
		if v.Gcs.ReadOnly {
			bind += ":ro"
		} else {
			writable = append(writable, v.Gcs.Bucket)
		}
		binds = append(binds, bind)
	}
	return binds, writable, nil
}

// gcsAcquireMounts starts ingesting writes to each bucket for a workload
// about to mount them writable. The release it returns ends that once the
// workload has stopped.
func gcsAcquireMounts(buckets []string) (release func(), err error) {
	seen := map[string]bool{}
	var acquired []string
	release = func() {
		for _, bucket := range acquired {
			gcsMountRelease(bucket)
		}
	}
	for _, bucket := range buckets {
		if seen[bucket] {
			continue
		}
		seen[bucket] = true
		if err := gcsMountAcquire(bucket); err != nil {
			release()
			return nil, fmt.Errorf("mount Cloud Storage bucket %q: %w", bucket, err)
		}
		acquired = append(acquired, bucket)
	}
	return release, nil
}

// gcsStorageRequest reports whether a request addresses the Cloud Storage
// JSON API.
func gcsStorageRequest(urlPath string) bool {
	for _, prefix := range []string{"/storage/v1/", "/upload/storage/v1/", "/download/storage/v1/", "/resumable/upload/storage/v1/", "/batch/storage/v1"} {
		if strings.HasPrefix(urlPath, prefix) {
			return true
		}
	}
	return false
}

// gcsMountSyncMiddleware has a Cloud Storage request wait until every write a
// workload finished through a mounted bucket before the request arrived is an
// object: Cloud Storage FUSE writes the object before close returns.
func gcsMountSyncMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gcsMountsWatched.Load() > 0 && gcsStorageRequest(r.URL.Path) {
			if err := gcsMountBarrier(r.Context()); err != nil {
				GCPErrorf(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cloud storage volume mounts: %v", err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

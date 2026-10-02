package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// gcsBodies holds the contents of every object generation, live or
// soft-deleted, and of every resumable upload in progress. Each generation
// has a payload of its own, so a generation retired by a delete or an
// overwrite keeps its bytes whatever later generations of the name hold.
var gcsBodies *blobstore.Payloads

func gcsOpenBodies(srv *sim.Server) {
	bodies, err := srv.Payloads("gcs")
	if err != nil {
		log.Fatalf("cloud storage object contents: %v", err)
	}
	if err := gcsAdoptBodies(bodies); err != nil {
		log.Fatalf("cloud storage object contents: %v", err)
	}
}

// gcsAdoptBodies makes bodies the payload store. An object written before
// generations had payloads of their own kept its bytes at its path in the
// bucket's host directory; adoption moves them into a payload. A soft-deleted
// generation's bytes are only there when no live object took the name over.
func gcsAdoptBodies(bodies *blobstore.Payloads) error {
	gcsBodies = bodies
	return bodies.Adopt(func(adoption *blobstore.Adoption) error {
		live := map[string]bool{}
		for _, row := range gcsObjects.ListPrefix("") {
			obj := row.Item
			live[row.ID] = true
			if obj.Body == "" && gcsObjectSize(obj) > 0 {
				ref, _, err := adoption.MoveFrom(gcsMirrorPath(obj.Bucket, obj.Name))
				if err != nil {
					return fmt.Errorf("move the contents of %s into a payload: %w", row.ID, err)
				}
				obj.Body = ref
				gcsObjects.Put(row.ID, obj)
			}
			adoption.Keep(obj.Body)
			if info, err := os.Stat(gcsMirrorPath(obj.Bucket, obj.Name)); err != nil || info.Size() != gcsObjectSize(obj) {
				gcsMirror(obj)
			}
		}
		for _, entry := range gcsSoftDeletedObjects.List() {
			key := gcsSoftDeleteKey(entry.Object.Bucket, entry.Object.Name, entry.Object.Generation)
			if entry.Body == "" && gcsObjectSize(entry.Object) > 0 {
				// The generation's bytes shared a path with every other
				// generation of the name; once a live object took the path
				// over, or a purge removed it, they are gone, and a generation
				// that cannot be restored is not listed as restorable.
				var ref string
				err := os.ErrNotExist
				if !live[entry.Object.Bucket+"/"+entry.Object.Name] {
					ref, _, err = adoption.MoveFrom(gcsMirrorPath(entry.Object.Bucket, entry.Object.Name))
				}
				if errors.Is(err, os.ErrNotExist) {
					log.Printf("cloud storage: soft-deleted %s generation %s kept no bytes; dropping it",
						entry.Object.Name, entry.Object.Generation)
					gcsSoftDeletedObjects.Delete(key)
					continue
				}
				if err != nil {
					return fmt.Errorf("move the contents of soft-deleted %s into a payload: %w", key, err)
				}
				entry.Body = ref
				gcsSoftDeletedObjects.Put(key, entry)
				gcsUnmirror(entry.Object.Bucket, entry.Object.Name)
			}
			adoption.Keep(entry.Body)
		}
		for _, row := range gcsResumableSessions.ListPrefix("") {
			session := row.Item
			if len(session.LegacyData) > 0 {
				ref, _, err := adoption.Move(session.LegacyData)
				if err != nil {
					return fmt.Errorf("move resumable upload %s into a payload: %w", row.ID, err)
				}
				session.Body, session.LegacyData = ref, nil
				gcsResumableSessions.Put(row.ID, session)
			}
			adoption.Keep(session.Body)
		}
		return nil
	})
}

func gcsObjectSize(obj GCSObject) int64 {
	size, _ := strconv.ParseInt(obj.Size, 10, 64)
	return size
}

// gcsOpenObject opens obj's contents, returning the object they belong to,
// which is a newer generation when obj was overwritten before its payload was
// opened.
func gcsOpenObject(obj GCSObject) (GCSObject, blobstore.Reader, error) {
	return blobstore.OpenCurrent(gcsBodies, obj,
		func(o GCSObject) string { return o.Body },
		func(o GCSObject) (GCSObject, bool) { return gcsObjects.Get(o.Bucket + "/" + o.Name) },
		obj.Bucket+"/"+obj.Name)
}

// gcsObjectBytes returns the whole contents of obj.
func gcsObjectBytes(obj GCSObject) ([]byte, error) {
	_, reader, err := gcsOpenObject(obj)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(reader)
}

// GCSObjectBytes returns the contents of a live object, for the slices that
// read one (Cloud Build's source fetch).
func GCSObjectBytes(bucket, object string) ([]byte, error) {
	obj, ok := gcsObjects.Get(bucket + "/" + object)
	if !ok {
		return nil, fmt.Errorf("object %s/%s not found", bucket, object)
	}
	return gcsObjectBytes(obj)
}

// gcsCopyContents gives the contents of obj a payload of their own.
func gcsCopyContents(obj GCSObject) (GCSObject, string, blobstore.Digests, error) {
	current, reader, err := gcsOpenObject(obj)
	if err != nil {
		return obj, "", blobstore.Digests{}, err
	}
	defer func() { _ = reader.Close() }()
	ref, digests, err := gcsBodies.WriteFrom(reader)
	return current, ref, digests, err
}

// gcsConcatContents stores the contents of objects, one after another, in a
// payload of its own.
func gcsConcatContents(objects []GCSObject) (string, blobstore.Digests, error) {
	readers := make([]io.Reader, 0, len(objects))
	for _, obj := range objects {
		_, reader, err := gcsOpenObject(obj)
		if err != nil {
			return "", blobstore.Digests{}, err
		}
		defer func() { _ = reader.Close() }()
		readers = append(readers, reader)
	}
	return gcsBodies.WriteFrom(io.MultiReader(readers...))
}

// persistGCSObjectBytes stores data as a new generation of the object.
func persistGCSObjectBytes(bucketName, objectName string, data []byte, attrs GCSObject, pre gcsPreconditions) (GCSObject, error) {
	ref, digests, err := gcsBodies.Write(data)
	if err != nil {
		return GCSObject{}, fmt.Errorf("store %s/%s: %w", bucketName, objectName, err)
	}
	return persistGCSObject(gcsObjects, bucketName, objectName, ref, digests, attrs, pre, gcsMirror)
}

// gcsReleaseBody releases a payload no row references any more. A failure
// leaves an unreferenced file, which the next start's sweep removes.
func gcsReleaseBody(ref string) {
	if err := gcsBodies.Remove(ref); err != nil {
		log.Printf("cloud storage: release contents %s: %v", ref, err)
	}
}

func gcsMirrorPath(bucket, name string) string {
	return filepath.Join(GCSBucketHostDir(bucket), filepath.FromSlash(name))
}

// gcsMirror copies the live generation of an object to its path in the
// bucket's host directory, the directory a Cloud Run volume mounts as the
// bucket. The copy is the mount's own: a workload's write through the mount
// reaches Cloud Storage only as the new generation the mount's watch writes.
// Cloud Storage FUSE shows a directory where a file and
// a directory share a name, and so does the host directory; a name that would
// resolve outside the bucket has no path in it at all.
//
// The copy is staged outside every bucket's directory and renamed into place,
// so a watch on a mounted bucket sees the finished file arrive and never a
// write of the simulator's own.
func gcsMirror(obj GCSObject) {
	if !filepath.IsLocal(filepath.FromSlash(obj.Name)) {
		return
	}
	path := gcsMirrorPath(obj.Bucket, obj.Name)
	if strings.HasSuffix(obj.Name, "/") {
		gcsMountMu.Lock()
		defer gcsMountMu.Unlock()
		if err := gcsMirrorDirectory(obj.Bucket, path); err != nil {
			log.Printf("cloud storage: mirror %s/%s: %v", obj.Bucket, obj.Name, err)
			return
		}
		gcsMountSetDirGenerationLocked(obj.Bucket, strings.TrimSuffix(obj.Name, "/"), gcsGenerationNumber(obj.Generation))
		return
	}
	staged, err := gcsStageMirror(obj)
	if err != nil {
		log.Printf("cloud storage: mirror %s/%s: %v", obj.Bucket, obj.Name, err)
		return
	}
	gcsMountMu.Lock()
	defer gcsMountMu.Unlock()
	discard := func(err error) {
		_ = os.Remove(staged)
		if err != nil {
			log.Printf("cloud storage: mirror %s/%s: %v", obj.Bucket, obj.Name, err)
		}
	}
	if err := gcsMirrorDirectory(obj.Bucket, filepath.Dir(path)); err != nil {
		discard(err)
		return
	}
	if info, err := os.Lstat(path); err == nil && info.IsDir() {
		discard(nil)
		return
	}
	if err := os.Rename(staged, path); err != nil {
		discard(err)
		return
	}
	gcsMountRecordFileLocked(obj.Bucket, obj.Name, gcsGenerationNumber(obj.Generation), path)
}

// gcsStageMirror writes obj's contents to a file of their own beside the
// bucket directories, on the same file system, for gcsMirror to rename into
// place.
func gcsStageMirror(obj GCSObject) (string, error) {
	staging := filepath.Join(gcsDataRoot(), gcsMirrorStagingDir)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	source, err := gcsBodies.Open(obj.Body)
	if err != nil {
		return "", err
	}
	defer func() { _ = source.Close() }()
	temp, err := os.CreateTemp(staging, "mirror-*")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
		return "", err
	}
	if _, err := io.Copy(temp, source); err != nil {
		return fail(err)
	}
	if err := temp.Chmod(0o644); err != nil {
		return fail(err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(temp.Name())
		return "", err
	}
	return temp.Name(), nil
}

// gcsMirrorStagingDir names the staging directory under the data root. A
// bucket name starts with a letter or digit, so no bucket directory takes it.
const gcsMirrorStagingDir = ".mirror-staging"

func gcsGenerationNumber(generation string) int64 {
	n, _ := strconv.ParseInt(generation, 10, 64)
	return n
}

// gcsMirrorDirectory makes dir a directory of the bucket's host directory,
// taking the place of any object file on the way: the directory wins. The
// caller holds gcsMountMu.
func gcsMirrorDirectory(bucket, dir string) error {
	root := GCSBucketHostDir(bucket)
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	current := root
	var made []string
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		made = append(made, current)
		if info, err := os.Lstat(current); err == nil && !info.IsDir() {
			if err := os.Remove(current); err != nil {
				return err
			}
			gcsMountForgetLocked(bucket, filepath.ToSlash(mustRel(root, current)))
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, path := range made {
		gcsMountRecordDirLocked(bucket, filepath.ToSlash(mustRel(root, path)), path)
	}
	return nil
}

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		panic(fmt.Sprintf("%s is not under %s: %v", path, root, err))
	}
	return rel
}

// gcsUnmirror removes an object's file from the bucket's host directory. A
// directory at the path belongs to other objects and stays; a deleted
// directory placeholder leaves its directory implied by what is under it.
func gcsUnmirror(bucket, name string) {
	if !filepath.IsLocal(filepath.FromSlash(name)) {
		return
	}
	gcsMountMu.Lock()
	defer gcsMountMu.Unlock()
	if strings.HasSuffix(name, "/") {
		gcsMountSetDirGenerationLocked(bucket, strings.TrimSuffix(name, "/"), 0)
		return
	}
	path := gcsMirrorPath(bucket, name)
	if info, err := os.Lstat(path); err != nil || info.IsDir() {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("cloud storage: unmirror %s/%s: %v", bucket, name, err)
		return
	}
	gcsMountForgetLocked(bucket, name)
}

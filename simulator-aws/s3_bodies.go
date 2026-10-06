package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// s3Bodies holds the contents of every object and every uploaded part. The
// rows in s3Objects and s3MultipartUploads reference them.
var s3Bodies *blobstore.Payloads

// s3OpenBodies opens the payload store and adopts it; it runs once, as the S3
// slice registers and before anything is served.
func s3OpenBodies(srv *sim.Server) {
	bodies, err := srv.Payloads("s3")
	if err != nil {
		log.Fatalf("s3 object bodies: %v", err)
	}
	if err := s3AdoptBodies(bodies); err != nil {
		log.Fatalf("s3 object bodies: %v", err)
	}
}

// s3AdoptBodies makes bodies the payload store, moves the contents rows
// written before it out of them, and removes the files no row references.
func s3AdoptBodies(bodies *blobstore.Payloads) error {
	s3Bodies = bodies
	return bodies.Adopt(func(adoption *blobstore.Adoption) error {
		for _, row := range s3Objects.ListPrefix("") {
			obj := row.Item
			if len(obj.LegacyData) > 0 {
				ref, _, err := adoption.Move(obj.LegacyData)
				if err != nil {
					return fmt.Errorf("move the contents of %s out of its row: %w", row.ID, err)
				}
				obj.Body, obj.LegacyData = ref, nil
				s3Objects.Put(row.ID, obj)
			}
			adoption.Keep(obj.Body)
		}
		s3KeepVersionBodies(adoption)
		for _, upload := range s3MultipartUploads.List() {
			moved := false
			for number, part := range upload.Parts {
				if len(part.LegacyData) > 0 {
					ref, digests, err := adoption.Move(part.LegacyData)
					if err != nil {
						return fmt.Errorf("move part %d of upload %s out of its row: %w", number, upload.UploadID, err)
					}
					part.Body, part.Size, part.LegacyData = ref, digests.Size, nil
					upload.Parts[number] = part
					moved = true
				}
				adoption.Keep(part.Body)
			}
			if moved {
				s3MultipartUploads.Put(upload.UploadID, upload)
			}
		}
		return nil
	})
}

// s3StoreObject stores obj as the current object under obj.Key with the
// contents body references, which the row takes over. In a
// versioning-enabled bucket the object gets a version id of its own and the
// object it supersedes becomes a noncurrent version; otherwise it is the null
// version, and releases the contents of the null version it replaces.
func s3StoreObject(obj S3Object, body string, digests blobstore.Digests) (S3Object, error) {
	obj.Body, obj.LegacyData, obj.Size = body, nil, digests.Size
	obj.VersionID, obj.VersionSeq = "", s3NextVersionSeq()
	bucket, _, _ := strings.Cut(obj.Key, "/")
	var released []string
	switch s3VersioningStatus(bucket) {
	case s3VersioningEnabled:
		obj.VersionID = s3NewVersionID()
		s3ArchiveCurrent(obj.Key)
	case s3VersioningSuspended:
		released = append(released, s3DropNoncurrentNull(obj.Key))
		if current, ok := s3Objects.Get(obj.Key); ok && current.VersionID != "" {
			s3ArchiveCurrent(obj.Key)
		}
	}
	s3Objects.Upsert(obj.Key, func(current *S3Object) {
		released = append(released, current.Body)
		*current = obj
	})
	var errs []error
	for _, ref := range released {
		errs = append(errs, s3Bodies.Release(ref, body))
	}
	return obj, errors.Join(errs...)
}

// s3StoreObjectData stores obj with data as its contents. An object stated
// without an ETag takes the one S3 gives a single-part upload, the hex MD5 of
// its contents.
func s3StoreObjectData(obj S3Object, data []byte) (S3Object, error) {
	ref, digests, err := s3Bodies.Write(data)
	if err != nil {
		return S3Object{}, fmt.Errorf("store %s: %w", obj.Key, err)
	}
	if obj.ETag == "" {
		obj.ETag = `"` + digests.MD5Hex() + `"`
	}
	return s3StoreObject(obj, ref, digests)
}

// s3CopyContents gives the contents of src a payload of their own, returning
// the object they were read from, which is a newer one when src was
// overwritten before its file was opened.
func s3CopyContents(src S3Object) (S3Object, string, blobstore.Digests, error) {
	current, reader, err := s3OpenObject(src)
	if err != nil {
		return src, "", blobstore.Digests{}, err
	}
	defer func() { _ = reader.Close() }()
	ref, digests, err := s3Bodies.WriteFrom(reader)
	return current, ref, digests, err
}

// s3DeleteObjectRow deletes the object stored under key and releases its
// contents. It reports whether there was one.
func s3DeleteObjectRow(key string) bool {
	defer s3ObjectWriters.Lock(key)()
	obj, ok := s3Objects.Get(key)
	if !ok || !s3Objects.Delete(key) {
		return false
	}
	if err := s3Bodies.Remove(obj.Body); err != nil {
		// The row is gone and nothing references the file any more; the next
		// start's sweep removes it. The deletion itself succeeded.
		log.Printf("s3: release the contents of %s: %v", key, err)
	}
	return true
}

// s3OpenObject opens the contents of obj, one version of an object, reading
// that version again when an overwrite or a delete removed the file first: an
// unversioned overwrite replaces the null version, so the contents opened are
// the newer object's.
func s3OpenObject(obj S3Object) (S3Object, blobstore.Reader, error) {
	return blobstore.OpenCurrent(s3Bodies, obj,
		func(o S3Object) string { return o.Body },
		func(o S3Object) (S3Object, bool) {
			version, ok := s3LookupVersion(o.Key, o.VersionID)
			return version.Object, ok && !version.DeleteMarker
		},
		obj.Key)
}

// s3ObjectData returns obj's whole contents, for the callers that parse or
// copy them.
func s3ObjectData(obj S3Object) ([]byte, error) {
	_, data, err := s3OpenObjectData(obj)
	return data, err
}

// s3StorePart stores the contents body references as part number of the
// upload and releases the contents of the part it replaces. It reports false,
// and releases body, when the upload is gone.
func s3StorePart(uploadID string, number int, body string, digests blobstore.Digests, etag string) (bool, error) {
	replaced := ""
	stored := s3MultipartUploads.Update(uploadID, func(upload *S3MultipartUpload) {
		replaced = upload.Parts[number].Body
		upload.Parts[number] = s3MultipartPart{Body: body, Size: digests.Size, ETag: etag}
	})
	if !stored {
		return false, s3Bodies.Remove(body)
	}
	return true, s3Bodies.Release(replaced, body)
}

// s3PartData returns a part's whole contents.
func s3PartData(part s3MultipartPart) ([]byte, error) {
	return s3Bodies.Read(part.Body)
}

// s3DeleteUpload deletes the upload and releases its parts' contents. It
// reports whether there was one.
func s3DeleteUpload(uploadID string) bool {
	upload, ok := s3MultipartUploads.Get(uploadID)
	if !ok || !s3MultipartUploads.Delete(uploadID) {
		return false
	}
	for number, part := range upload.Parts {
		if err := s3Bodies.Remove(part.Body); err != nil {
			log.Printf("s3: release part %d of upload %s: %v", number, uploadID, err)
		}
	}
	return true
}

// s3OpenObjectData returns obj's whole contents together with the object they
// belong to.
func s3OpenObjectData(obj S3Object) (S3Object, []byte, error) {
	current, reader, err := s3OpenObject(obj)
	if err != nil {
		return obj, nil, err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	return current, data, err
}

var errS3NoSuchKey = errors.New("the specified key does not exist")

// s3MoveObject moves the object stored under from to to, contents and all,
// when condition holds for what to holds now; a nil condition always does. The
// contents are not copied: the moved row keeps its Body. The object it
// replaces at to releases its own.
func s3MoveObject(from, to string, condition func(existing S3Object, exists bool) bool) (S3Object, error) {
	first, second := from, to
	if second < first {
		first, second = second, first
	}
	defer s3ObjectWriters.Lock(first)()
	if second != first {
		defer s3ObjectWriters.Lock(second)()
	}
	obj, ok := s3Objects.Get(from)
	if !ok {
		return S3Object{}, errS3NoSuchKey
	}
	if condition != nil {
		existing, exists := s3Objects.Get(to)
		if !condition(existing, exists) {
			return S3Object{}, errS3PreconditionFailed
		}
	}
	obj.Key = to
	obj.LastModified = time.Now()
	replaced := ""
	s3Objects.Upsert(to, func(current *S3Object) {
		replaced = current.Body
		*current = obj
	})
	if from != to {
		s3Objects.Delete(from)
	}
	return obj, s3Bodies.Release(replaced, obj.Body)
}

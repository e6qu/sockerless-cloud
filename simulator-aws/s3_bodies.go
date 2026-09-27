package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// s3Bodies holds the contents of every object and every uploaded part. The
// rows in s3Objects and s3MultipartUploads reference them; see sim.Payloads.
var s3Bodies *sim.Payloads

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
func s3AdoptBodies(bodies *sim.Payloads) error {
	s3Bodies = bodies
	referenced := map[string]bool{}
	for _, row := range s3Objects.ListPrefix("") {
		obj := row.Item
		if len(obj.LegacyData) > 0 {
			ref, err := s3Bodies.Write(obj.LegacyData)
			if err != nil {
				return fmt.Errorf("move the contents of %s out of its row: %w", row.ID, err)
			}
			obj.Body, obj.LegacyData = ref, nil
			s3Objects.Put(row.ID, obj)
		}
		referenced[obj.Body] = true
	}
	for _, upload := range s3MultipartUploads.List() {
		moved := false
		for number, part := range upload.Parts {
			if len(part.LegacyData) > 0 {
				ref, err := s3Bodies.Write(part.LegacyData)
				if err != nil {
					return fmt.Errorf("move part %d of upload %s out of its row: %w", number, upload.UploadID, err)
				}
				part.Body, part.Size, part.LegacyData = ref, int64(len(part.LegacyData)), nil
				upload.Parts[number] = part
				moved = true
			}
			referenced[part.Body] = true
		}
		if moved {
			s3MultipartUploads.Put(upload.UploadID, upload)
		}
	}
	_, err := s3Bodies.Sweep(func(ref string) bool { return referenced[ref] })
	return err
}

// s3WriteBody stores data and returns its reference; an empty body has none.
func s3WriteBody(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	return s3Bodies.Write(data)
}

// s3StoreObject stores obj under obj.Key with data as its contents, sets its
// Body and Size, and releases the contents of the object it replaces.
func s3StoreObject(obj S3Object, data []byte) (S3Object, error) {
	ref, err := s3WriteBody(data)
	if err != nil {
		return S3Object{}, fmt.Errorf("store %s: %w", obj.Key, err)
	}
	obj.Body, obj.LegacyData, obj.Size = ref, nil, int64(len(data))
	var replaced string
	s3Objects.Upsert(obj.Key, func(current *S3Object) {
		replaced = current.Body
		*current = obj
	})
	if replaced != "" && replaced != ref {
		if err := s3Bodies.Remove(replaced); err != nil {
			return obj, err
		}
	}
	return obj, nil
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

// s3OpenObject opens obj's contents for reading, so a ranged read touches only
// its range. An overwrite between reading the row and opening its file
// removes the file; the row is read again then, and the new object is what it
// returns, so a caller describes the contents it serves.
func s3OpenObject(obj S3Object) (S3Object, io.ReadSeeker, func(), error) {
	for attempt := 0; attempt < 2; attempt++ {
		if obj.Body == "" {
			return obj, bytes.NewReader(nil), func() {}, nil
		}
		file, err := s3Bodies.Open(obj.Body)
		if err == nil {
			return obj, file, func() { _ = file.Close() }, nil
		}
		if !errors.Is(err, sim.ErrPayloadGone) {
			return obj, nil, nil, err
		}
		current, ok := s3Objects.Get(obj.Key)
		if !ok || current.Body == obj.Body {
			return obj, nil, nil, fmt.Errorf("the contents of %s: %w", obj.Key, err)
		}
		obj = current
	}
	return obj, nil, nil, fmt.Errorf("the contents of %s changed twice while being read", obj.Key)
}

// s3ObjectData returns obj's whole contents, for the callers that parse or
// copy them.
func s3ObjectData(obj S3Object) ([]byte, error) {
	_, reader, closeBody, err := s3OpenObject(obj)
	if err != nil {
		return nil, err
	}
	defer closeBody()
	return io.ReadAll(reader)
}

// s3StorePart stores body as part number of the upload and releases the
// contents of the part it replaces. It reports false when the upload is gone.
func s3StorePart(uploadID string, number int, body []byte, etag string) (bool, error) {
	ref, err := s3WriteBody(body)
	if err != nil {
		return false, fmt.Errorf("part %d of upload %s: %w", number, uploadID, err)
	}
	replaced := ""
	stored := s3MultipartUploads.Update(uploadID, func(upload *S3MultipartUpload) {
		replaced = upload.Parts[number].Body
		upload.Parts[number] = s3MultipartPart{Body: ref, Size: int64(len(body)), ETag: etag}
	})
	if !stored {
		// The upload was aborted while the part was being written.
		return false, s3Bodies.Remove(ref)
	}
	return true, s3Bodies.Remove(replaced)
}

// s3PartData returns a part's whole contents.
func s3PartData(part s3MultipartPart) ([]byte, error) {
	if part.Body == "" {
		return nil, nil
	}
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
// belong to, which is a newer one when obj was overwritten before its file was
// opened.
func s3OpenObjectData(obj S3Object) (S3Object, []byte, error) {
	current, reader, closeBody, err := s3OpenObject(obj)
	if err != nil {
		return obj, nil, err
	}
	defer closeBody()
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
	if replaced != obj.Body {
		return obj, s3Bodies.Remove(replaced)
	}
	return obj, nil
}

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A delete under a soft-delete policy retires the object and keeps its bytes
// until retention expires; a delete without one destroys both. Every path
// that frees a payload goes through gcsRemoveObjectPayload.

// What Cloud Storage gives a bucket that declares no policy of its own.
const gcsDefaultSoftDeleteRetention = 7 * 24 * time.Hour

type gcsSoftDeleted struct {
	Object         GCSObject `json:"object"`
	SoftDeleteTime string    `json:"softDeleteTime"`
	HardDeleteTime string    `json:"hardDeleteTime"`
}

var gcsSoftDeletedObjects sim.Store[gcsSoftDeleted]

// The generated Go client sends `softDeleted=true`; gcloud renders Python's
// bool and sends `softDeleted=True`. Matching one spelling silently ignores
// the other client.
func gcpQueryBool(r *http.Request, name string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(r.URL.Query().Get(name)))
	return err == nil && value
}

func gcsSoftDeleteKey(bucket, object, generation string) string {
	return bucket + "\x00" + object + "\x00" + generation
}

// A zero duration is how the service spells soft delete turned off.
func gcsSoftDeleteRetention(bucket Bucket) time.Duration {
	policy, ok := bucket.Data["softDeletePolicy"].(map[string]any)
	if !ok {
		return gcsDefaultSoftDeleteRetention
	}
	var seconds int64
	switch raw := policy["retentionDurationSeconds"].(type) {
	case string:
		seconds, _ = strconv.ParseInt(raw, 10, 64)
	case float64:
		seconds = int64(raw)
	default:
		return gcsDefaultSoftDeleteRetention
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func gcsApplyDefaultSoftDeletePolicy(data map[string]any) {
	if _, ok := data["softDeletePolicy"]; ok {
		return
	}
	data["softDeletePolicy"] = map[string]any{
		"retentionDurationSeconds": strconv.FormatInt(int64(gcsDefaultSoftDeleteRetention/time.Second), 10),
		"effectiveTime":            gcsTimestamp(),
	}
}

// Call only where the object is destroyed, never where it is retired: restore
// reads this same path.
func gcsRemoveObjectPayload(bucket, object string) {
	_ = os.Remove(filepath.Join(GCSBucketHostDir(bucket), object))
}

// Reports whether the object was retained.
func gcsRetireObject(bucket Bucket, bucketName string, obj GCSObject) bool {
	retention := gcsSoftDeleteRetention(bucket)
	if retention == 0 {
		gcsRemoveObjectPayload(bucketName, obj.Name)
		gcsDropObjectACL(bucketName, obj.Name)
		return false
	}
	now := time.Now().UTC()
	gcsSoftDeletedObjects.Put(gcsSoftDeleteKey(bucketName, obj.Name, obj.Generation), gcsSoftDeleted{
		Object:         obj,
		SoftDeleteTime: now.Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"),
		HardDeleteTime: now.Add(retention).Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"),
	})
	return true
}

// Past hardDeleteTime the service has deleted them permanently, so neither
// the listing nor the bytes may survive.
func gcsPurgeExpiredSoftDeletes(bucketName string) {
	now := time.Now().UTC()
	for _, entry := range gcsSoftDeletedObjects.Filter(func(e gcsSoftDeleted) bool {
		return e.Object.Bucket == bucketName
	}) {
		hard, err := time.Parse("2006-01-02T15:04:05.000Z", entry.HardDeleteTime)
		if err != nil || now.Before(hard) {
			continue
		}
		gcsSoftDeletedObjects.Delete(gcsSoftDeleteKey(bucketName, entry.Object.Name, entry.Object.Generation))
		gcsRemoveObjectPayload(bucketName, entry.Object.Name)
		gcsDropObjectACL(bucketName, entry.Object.Name)
	}
}

func gcsSoftDeletedListing(bucketName, prefix string) []gcsSoftDeleted {
	gcsPurgeExpiredSoftDeletes(bucketName)
	items := gcsSoftDeletedObjects.Filter(func(e gcsSoftDeleted) bool {
		return e.Object.Bucket == bucketName && (prefix == "" || strings.HasPrefix(e.Object.Name, prefix))
	})
	sort.Slice(items, func(i, j int) bool {
		if items[i].Object.Name != items[j].Object.Name {
			return items[i].Object.Name < items[j].Object.Name
		}
		return items[i].Object.Generation < items[j].Object.Generation
	})
	return items
}

func registerGCSObjectRestore(srv *sim.Server, buckets sim.Store[Bucket], objects sim.Store[GCSObject]) {
	bucketOr404 := func(w http.ResponseWriter, name string) (Bucket, bool) {
		bucket, ok := buckets.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", name)
			return Bucket{}, false
		}
		return bucket, true
	}

	srv.HandleFunc("POST /storage/v1/b/{bucket}/o/{object}/restore", func(w http.ResponseWriter, r *http.Request) {
		bucketName, objectName := sim.PathParam(r, "bucket"), sim.PathParam(r, "object")
		if _, ok := bucketOr404(w, bucketName); !ok {
			return
		}
		generation := strings.TrimSpace(r.URL.Query().Get("generation"))
		if generation == "" {
			GCPError(w, http.StatusBadRequest, "generation is required to restore an object", "INVALID_ARGUMENT")
			return
		}
		gcsPurgeExpiredSoftDeletes(bucketName)
		entry, ok := gcsSoftDeletedObjects.Get(gcsSoftDeleteKey(bucketName, objectName, generation))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no soft-deleted object %q with generation %s in bucket %q", objectName, generation, bucketName)
			return
		}
		if _, live := objects.Get(bucketName + "/" + objectName); live {
			GCPErrorf(w, http.StatusPreconditionFailed, "FAILED_PRECONDITION",
				"object %q already exists in bucket %q", objectName, bucketName)
			return
		}
		restored := entry.Object
		restored.Updated = gcsTimestamp()
		objects.Put(bucketName+"/"+objectName, restored)
		gcsIndexAdd(bucketName, objectName)
		gcsSoftDeletedObjects.Delete(gcsSoftDeleteKey(bucketName, objectName, generation))
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, restored))
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/o/bulkRestore", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		if _, ok := bucketOr404(w, bucketName); !ok {
			return
		}
		var request struct {
			MatchGlobs            []string `json:"matchGlobs"`
			AllowOverwrite        bool     `json:"allowOverwrite"`
			CopySourceAcl         bool     `json:"copySourceAcl"`
			CreatedAfterTime      string   `json:"createdAfterTime"`
			CreatedBeforeTime     string   `json:"createdBeforeTime"`
			SoftDeletedAfterTime  string   `json:"softDeletedAfterTime"`
			SoftDeletedBeforeTime string   `json:"softDeletedBeforeTime"`
		}
		if err := sim.ReadJSON(r, &request); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		restored := 0
		for _, entry := range gcsSoftDeletedListing(bucketName, "") {
			name := entry.Object.Name
			if !gcsMatchesAnyGlob(name, request.MatchGlobs) {
				continue
			}
			if !gcsTimeWindowContains(entry.SoftDeleteTime, request.SoftDeletedAfterTime, request.SoftDeletedBeforeTime) {
				continue
			}
			if !gcsTimeWindowContains(entry.Object.TimeCreated, request.CreatedAfterTime, request.CreatedBeforeTime) {
				continue
			}
			if _, live := objects.Get(bucketName + "/" + name); live && !request.AllowOverwrite {
				continue
			}
			object := entry.Object
			object.Updated = gcsTimestamp()
			objects.Put(bucketName+"/"+name, object)
			gcsIndexAdd(bucketName, name)
			gcsSoftDeletedObjects.Delete(gcsSoftDeleteKey(bucketName, name, entry.Object.Generation))
			// Without copySourceAcl the restored object takes the bucket
			// default, the rule a freshly written object follows.
			if !request.CopySourceAcl {
				gcsDropObjectACL(bucketName, name)
				gcsSeedObjectACL(bucketName, name, object.Generation)
			}
			restored++
		}
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucketName, map[string]any{
			"@type":           "type.googleapis.com/google.storage.v2.BulkRestoreObjectsResponse",
			"objectsRestored": strconv.Itoa(restored),
			"bucket":          bucketName,
		}))
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/o/{sourceObject}/moveTo/o/{destinationObject}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		source, destination := sim.PathParam(r, "sourceObject"), sim.PathParam(r, "destinationObject")
		bucket, ok := bucketOr404(w, bucketName)
		if !ok {
			return
		}
		if !gcsHierarchicalNamespace(bucket) {
			GCPError(w, http.StatusBadRequest,
				"The bucket does not have hierarchical namespace enabled, which objects.move requires.",
				"INVALID_ARGUMENT")
			return
		}
		obj, found := objects.Get(bucketName + "/" + source)
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", source, bucketName)
			return
		}
		if _, exists := objects.Get(bucketName + "/" + destination); exists {
			GCPErrorf(w, http.StatusPreconditionFailed, "FAILED_PRECONDITION",
				"object %q already exists in bucket %q", destination, bucketName)
			return
		}
		data, err := gcsObjectBytes(obj, bucketName, source)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read source object: %v", err)
			return
		}
		moved, err := persistGCSObject(objects, bucketName, destination, data, obj)
		if err != nil {
			writeGCSPersistError(w, "move object", err)
			return
		}
		objects.Delete(bucketName + "/" + source)
		gcsIndexRemove(bucketName, source)
		gcsRemoveObjectPayload(bucketName, source)
		gcsDropObjectACL(bucketName, source)
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, moved))
	})
}

func gcsHierarchicalNamespace(bucket Bucket) bool {
	hns, ok := bucket.Data["hierarchicalNamespace"].(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := hns["enabled"].(bool)
	return enabled
}

// An unspecified list restores every object in range.
func gcsMatchesAnyGlob(name string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, glob := range globs {
		if pattern, err := gcsGlobPattern(glob); err == nil && pattern.MatchString(name) {
			return true
		}
	}
	return false
}

// Cloud Storage wildcards are not path.Match's: `**` crosses "/" and `*` does
// not, `{a,b}` alternates, `[…]` is a character class.
func gcsGlobPattern(glob string) (*regexp.Regexp, error) {
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				out.WriteString(".*")
				i++
				continue
			}
			out.WriteString("[^/]*")
		case '?':
			out.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(glob[i:], ']')
			if end < 0 {
				out.WriteString(regexp.QuoteMeta(string(c)))
				continue
			}
			out.WriteString(glob[i : i+end+1])
			i += end
		case '{':
			end := strings.IndexByte(glob[i:], '}')
			if end < 0 {
				out.WriteString(regexp.QuoteMeta(string(c)))
				continue
			}
			alternatives := strings.Split(glob[i+1:i+end], ",")
			for j := range alternatives {
				alternatives[j] = regexp.QuoteMeta(alternatives[j])
			}
			out.WriteString("(?:" + strings.Join(alternatives, "|") + ")")
			i += end
		default:
			out.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	out.WriteString("$")
	return regexp.Compile(out.String())
}

// An absent bound does not constrain the selection.
func gcsTimeWindowContains(stamp, after, before string) bool {
	at, err := time.Parse("2006-01-02T15:04:05.000Z", stamp)
	if err != nil {
		return true
	}
	if after != "" {
		if bound, err := time.Parse(time.RFC3339, after); err == nil && at.Before(bound) {
			return false
		}
	}
	if before != "" {
		if bound, err := time.Parse(time.RFC3339, before); err == nil && at.After(bound) {
			return false
		}
	}
	return true
}

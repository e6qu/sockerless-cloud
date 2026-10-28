package main

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

func registerGCSObjects(srv *sim.Server, buckets sim.Store[Bucket], objects sim.PrefixStore[GCSObject]) {
	srv.HandleFunc("GET /storage/v1/b/{bucket}/o", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		prefix := r.URL.Query().Get("prefix")
		delimiter := r.URL.Query().Get("delimiter")

		if _, ok := buckets.Get(bucketName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		filter, err := gcsParseContextsFilter(r.URL.Query().Get("filter"))
		if err != nil {
			GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
			return
		}

		// softDeleted=true lists the retired generations restore can bring back,
		// instead of the live objects.
		if gcpQueryBool(r, "softDeleted") {
			retired := gcsSoftDeletedListing(bucketName, prefix)
			items := make([]map[string]any, 0, len(retired))
			for _, entry := range retired {
				if !filter.Eval(gcsContextsFilterDoc(entry.Object)) {
					continue
				}
				resource := gcsObjectMetadata(r, entry.Object)
				resource["softDeleteTime"] = entry.SoftDeleteTime
				resource["hardDeleteTime"] = entry.HardDeleteTime
				items = append(items, resource)
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "storage#objects", "items": items})
			return
		}

		limit := gcsListLimit
		if raw := r.URL.Query().Get("maxResults"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid maxResults %q", raw)
				return
			}
			if n > 0 && n < limit {
				limit = n
			}
		}
		marker := ""
		if token := r.URL.Query().Get("pageToken"); token != "" {
			var ok bool
			if marker, ok = gcsPageMarker(token); !ok {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageToken %q", token)
				return
			}
		}
		// maxResults bounds items and prefixes together, and a page token
		// resumes past both.
		entries := blobstore.RollUp(gcsBucketObjects(bucketName, prefix),
			func(o GCSObject) string { return o.Name }, nil, prefix, delimiter)
		// The filter leaves the common prefixes alone.
		entries = slices.DeleteFunc(entries, func(e blobstore.Entry[GCSObject]) bool {
			return !e.Prefix && !filter.Eval(gcsContextsFilterDoc(e.Item))
		})
		page, _, next := blobstore.PageAfter(entries, marker, limit)
		items := []map[string]any{}
		var prefixes []string
		for _, entry := range page {
			if entry.Prefix {
				prefixes = append(prefixes, entry.Key)
				continue
			}
			items = append(items, gcsObjectMetadata(r, entry.Item))
		}
		resp := map[string]any{"kind": "storage#objects", "items": items}
		if len(prefixes) > 0 {
			resp["prefixes"] = prefixes
		}
		if next != "" {
			resp["nextPageToken"] = gcsPageToken(next)
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// The JSON API serves metadata and, with alt=media, the bytes on one path.
	srv.HandleFunc("GET /storage/v1/b/{bucket}/o/{object...}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		key := bucketName + "/" + objectName

		obj, ok := objects.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", objectName, bucketName)
			return
		}
		if !gcsJSONReadPreconditionsMet(w, r, obj) {
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			serveGCSObjectMedia(w, r, obj)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, obj))
	})

	// objects.patch merges the body onto the stored metadata and
	// objects.update replaces it; neither re-uploads the payload.
	patchObject := func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		key := bucketName + "/" + objectName
		pre, err := parseGCSPreconditions(r.URL.Query(), false)
		if err != nil {
			GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
			return
		}
		defer gcsObjectWriters.Lock(bucketName + "/" + objectName)()
		obj, ok := objects.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", objectName, bucketName)
			return
		}
		if !pre.holds(obj, true) {
			writeGCSPreconditionFailed(w)
			return
		}
		var res gcsObjectResource
		if err := sim.ReadJSON(r, &res); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if r.Method == http.MethodPut {
			// objects.update replaces the writable metadata, so a field the
			// body leaves out is cleared. customTime stays: Cloud Storage never
			// lets it be removed once set, and storageClass changes only by
			// rewrite.
			obj.ContentType, obj.ContentEncoding, obj.ContentLanguage = "", "", ""
			obj.ContentDisposition, obj.CacheControl = "", ""
			obj.Metadata, obj.metadataCloned = nil, true
		}
		obj = res.applyTo(obj)
		if err := validateGCSObjectAttrs(obj); err != nil {
			writeGCSPersistError(w, "patch object", err)
			return
		}
		obj.Updated = gcsTimestamp()
		// objects.update replaces the resource, so contexts it leaves out go.
		switch {
		case r.Method == http.MethodPut:
			obj.CustomContexts = res.Contexts.replacement(obj.CustomContexts, obj.Updated)
		case res.Contexts.present:
			obj.CustomContexts = res.Contexts.merged(obj.CustomContexts, obj.Updated)
		}
		if mg, err := strconv.ParseInt(obj.Metageneration, 10, 64); err == nil {
			obj.Metageneration = strconv.FormatInt(mg+1, 10)
		}
		persistGCSObjectMetadata(objects, key, obj)
		if res.Acl != nil {
			gcsReplaceObjectACL(bucketName, objectName, obj.Generation, *res.Acl)
		}
		gcsNotify(gcsEventMetadataUpdate, obj, nil)
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, obj))
	}

	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/o/{object...}", patchObject)

	srv.HandleFunc("PUT /storage/v1/b/{bucket}/o/{object...}", patchObject)

	srv.HandleFunc("DELETE /storage/v1/b/{bucket}/o/{object...}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		key := bucketName + "/" + objectName
		pre, err := parseGCSPreconditions(r.URL.Query(), false)
		if err != nil {
			GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
			return
		}

		defer gcsObjectWriters.Lock(bucketName + "/" + objectName)()
		obj, found := objects.Get(key)
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", objectName, bucketName)
			return
		}
		if !pre.holds(obj, true) {
			writeGCSPreconditionFailed(w)
			return
		}
		bucket, ok := buckets.Get(bucketName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		objects.Delete(key)
		gcsUnmirror(bucketName, objectName)
		gcsRetireDeletedObject(bucket, obj)

		w.WriteHeader(http.StatusNoContent)
	})

	// objects.compose concatenates up to 32 source objects, in order, into the
	// destination.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/o/{destObject...}", func(w http.ResponseWriter, r *http.Request) {
		if handled := handleGCSObjectCopyRequest(w, r, buckets, objects); handled {
			return
		}
		// Past copyTo and rewriteTo, only a /compose suffix is served here.
		destObject := sim.PathParam(r, "destObject")
		if !strings.HasSuffix(destObject, "/compose") {
			http.NotFound(w, r)
			return
		}
		destObject = strings.TrimSuffix(destObject, "/compose")
		bucketName := sim.PathParam(r, "bucket")
		if _, ok := buckets.Get(bucketName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucketName)
			return
		}
		var req struct {
			SourceObjects []struct {
				Name       string `json:"name"`
				Generation string `json:"generation,omitempty"`
			} `json:"sourceObjects"`
			Destination *gcsObjectResource `json:"destination"`
		}
		// Clients send the compose body unencoded, so it needs no openStreamingBody.
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"failed to parse compose request: %v", err)
			return
		}
		if len(req.SourceObjects) == 0 {
			GCPError(w, http.StatusBadRequest,
				"compose requires at least one sourceObject", "INVALID_ARGUMENT")
			return
		}
		// A composite is built from at most 32 sources in one request.
		if len(req.SourceObjects) > 32 {
			GCPError(w, http.StatusBadRequest,
				"The number of source components provided (more than 32) exceeds the maximum (32).", "INVALID_ARGUMENT")
			return
		}
		sources := make([]GCSObject, 0, len(req.SourceObjects))
		var componentCount int64
		for _, src := range req.SourceObjects {
			srcObj, ok := objects.Get(bucketName + "/" + src.Name)
			// A source that names a generation names that generation only.
			if !ok || (src.Generation != "" && src.Generation != srcObj.Generation) {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"source object %q not found in bucket %q", src.Name, bucketName)
				return
			}
			if srcObj.ComponentCount > 0 {
				componentCount += srcObj.ComponentCount
			} else {
				componentCount++
			}
			sources = append(sources, srcObj)
		}
		// A composite object counts at most 1024 components.
		if componentCount > 1024 {
			GCPError(w, http.StatusBadRequest,
				"The number of components in the composite object would exceed the maximum (1024).", "INVALID_ARGUMENT")
			return
		}
		objAttrs := GCSObject{}
		if req.Destination != nil {
			objAttrs = req.Destination.applyTo(objAttrs)
		}
		objAttrs.ComponentCount = componentCount
		contexts, err := gcsDestinationContexts(r, req.Destination, sources...)
		if err != nil {
			GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
			return
		}
		objAttrs.CustomContexts = contexts
		pre, err := parseGCSPreconditions(r.URL.Query(), false)
		if err != nil {
			GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
			return
		}
		composed, digests, err := gcsConcatContents(sources)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
			return
		}
		composedObj, err := persistGCSObject(objects, bucketName, destObject, composed, digests, objAttrs, pre, gcsMirror)
		if err != nil {
			writeGCSPersistError(w, "write composed object", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcsObjectMetadata(r, composedObj))
	})

	// The XML API object read (cloud.google.com/go/storage reads through it),
	// mounted without a method so the literal "/v2/" routes outrank it. It answers
	// 404 for a first segment that names no bucket, so it does not shadow
	// unrelated top-level paths with a Cloud Storage-shaped 404. The credential
	// and IAM gates do not cover a pattern that matches every URI, so it verifies
	// the bearer and storage.objects.get itself once the bucket resolves.
	srv.HandleFunc("/{bucket}/{object...}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		if objectName == "" {
			http.NotFound(w, r)
			return
		}
		if _, ok := buckets.Get(bucketName); !ok {
			http.NotFound(w, r)
			return
		}
		// A V4 signed URL authorizes the request in place of a bearer token,
		// with its signer's permissions.
		var principal string
		if signed, authorized := gcsSignedURLAuthorized(w, r); signed {
			if !authorized {
				return
			}
			email, _, _ := strings.Cut(r.URL.Query().Get("X-Goog-Credential"), "/")
			principal = "serviceAccount:" + email
		} else if !verifyRequestBearer(w, r) {
			return
		} else {
			principal = gcpRequestPrincipal(r)
		}
		check := gateCheck{permission: "storage.objects.get", bucket: bucketName, object: objectName}
		if !gateHolds(principal, check) {
			w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(xml.Header))
			_ = xml.NewEncoder(w).Encode(gcsXMLErrorBody{Code: "AccessDenied", Message: "Access denied.",
				Details: gateDeniedMessage(principal, gateDecision{api: "storage"}, check)})
			return
		}
		key := bucketName + "/" + objectName

		obj, ok := objects.Get(key)
		if !ok {
			writeGCSXMLError(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		if !gcsXMLReadConditionsMet(w, r, obj) {
			return
		}
		serveGCSObjectMedia(w, r, obj)
	})

	srv.HandleFunc("GET /download/storage/v1/b/{bucket}/o/{object...}", func(w http.ResponseWriter, r *http.Request) {
		bucketName := sim.PathParam(r, "bucket")
		objectName := sim.PathParam(r, "object")
		key := bucketName + "/" + objectName

		obj, ok := objects.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "object %q not found in bucket %q", objectName, bucketName)
			return
		}
		if !gcsJSONReadPreconditionsMet(w, r, obj) {
			return
		}
		serveGCSObjectMedia(w, r, obj)
	})
}

package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerGCSAnywhereCaches(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	caches := sim.MakeStore[GCSAnywhereCache](srv.DB(), "gcs_anywhere_caches")
	key := func(bucket, id string) string { return bucket + "\x00" + id }

	srv.HandleFunc("GET /storage/v1/b/{bucket}/anywhereCaches", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		items := caches.Filter(func(c GCSAnywhereCache) bool { return c.Bucket == bucket })
		gcsWriteMapList(w, r, items, "storage#anywhereCaches", func(v GCSAnywhereCache) string { return v.AnywhereCacheID })
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "anywhereCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "anywhere cache %q not found in bucket %q", id, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, c)
	})

	// insert / update are long-running operations returning a
	// GoogleLongrunningOperation whose response carries the cache.
	srv.HandleFunc("POST /storage/v1/b/{bucket}/anywhereCaches", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSAnywhereCache
		if err := sim.ReadJSON(r, &in); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		id := "anywhere-cache-" + gcsRandHex(6)
		now := gcsTimestamp()
		c := GCSAnywhereCache{
			Kind:            "storage#anywhereCache",
			ID:              bucket + "/" + id,
			SelfLink:        gcpSelfLink(r, "/storage/v1/b/"+bucket+"/anywhereCaches/"+id),
			Bucket:          bucket,
			AnywhereCacheID: id,
			Zone:            in.Zone,
			State:           "running",
			CreateTime:      now,
			UpdateTime:      now,
			Ttl:             in.Ttl,
			AdmissionPolicy: in.AdmissionPolicy,
		}
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})

	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "anywhereCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "anywhere cache %q not found in bucket %q", id, bucket)
			return
		}
		var in GCSAnywhereCache
		if err := sim.ReadJSON(r, &in); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Ttl != "" {
			c.Ttl = in.Ttl
		}
		if in.AdmissionPolicy != "" {
			c.AdmissionPolicy = in.AdmissionPolicy
		}
		c.UpdateTime = gcsTimestamp()
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})

	stateVerb := func(state string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "anywhereCacheId")
			c, ok := caches.Get(key(bucket, id))
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "anywhere cache %q not found in bucket %q", id, bucket)
				return
			}
			c.State = state
			c.UpdateTime = gcsTimestamp()
			caches.Put(key(bucket, id), c)
			sim.WriteJSON(w, http.StatusOK, c)
		}
	}
	srv.HandleFunc("POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/pause", stateVerb("paused"))
	srv.HandleFunc("POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/resume", stateVerb("running"))
	srv.HandleFunc("POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/disable", stateVerb("disabled"))
}

// registerGCSRapidCaches mounts the rapidCaches collection: a rapid cache is
// bucket-scoped control-plane state (zone, TTL, admission policy, cache
// type), managed here exactly as the schema describes it and carrying no
// data-plane behavior claim beyond its states. Nothing in this deployment
// provisions real cache hardware, so a cache settles synchronously — created
// running, and the long-running operations insert/update/disable return are
// already done, the same convention the anywhere-caches collection follows.
func registerGCSRapidCaches(srv *sim.Server, buckets sim.Store[Bucket], bucketExists func(http.ResponseWriter, string) bool) {
	caches := sim.MakeStore[GCSRapidCache](srv.DB(), "gcs_rapid_caches")
	key := func(bucket, id string) string { return bucket + "\x00" + id }

	srv.HandleFunc("GET /storage/v1/b/{bucket}/rapidCaches", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		items := caches.Filter(func(c GCSRapidCache) bool { return c.Bucket == bucket })
		gcsWriteMapList(w, r, items, "storage#rapidCaches", func(v GCSRapidCache) string { return v.RapidCacheID })
	})

	srv.HandleFunc("GET /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "rapidCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rapid cache %q not found in bucket %q", id, bucket)
			return
		}
		sim.WriteJSON(w, http.StatusOK, c)
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/rapidCaches", func(w http.ResponseWriter, r *http.Request) {
		bucket := sim.PathParam(r, "bucket")
		if !bucketExists(w, bucket) {
			return
		}
		var in GCSRapidCache
		if err := sim.ReadJSON(r, &in); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		id := in.RapidCacheID
		if id == "" {
			id = "rapid-cache-" + gcsRandHex(6)
		}
		if _, exists := caches.Get(key(bucket, id)); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "rapid cache %q already exists in bucket %q", id, bucket)
			return
		}
		now := gcsTimestamp()
		c := GCSRapidCache{
			Kind:            "storage#rapidCache",
			ID:              bucket + "/" + id,
			SelfLink:        gcpSelfLink(r, "/storage/v1/b/"+bucket+"/rapidCaches/"+id),
			Bucket:          bucket,
			RapidCacheID:    id,
			Zone:            in.Zone,
			State:           "running",
			CreateTime:      now,
			UpdateTime:      now,
			Ttl:             in.Ttl,
			AdmissionPolicy: in.AdmissionPolicy,
			IngestOnWrite:   in.IngestOnWrite,
			CacheType:       in.CacheType,
		}
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})

	// rapidCaches.update (PATCH) — the mutable members are the TTL, the
	// admission policy and the ingest-on-write flag; zone and cacheType are
	// fixed at creation. The ingest flag rides a pointer so an absent member
	// is left alone while an explicit false lands.
	srv.HandleFunc("PATCH /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "rapidCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rapid cache %q not found in bucket %q", id, bucket)
			return
		}
		var in struct {
			Ttl             string `json:"ttl"`
			AdmissionPolicy string `json:"admissionPolicy"`
			IngestOnWrite   *bool  `json:"ingestOnWrite"`
		}
		if err := sim.ReadJSON(r, &in); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.Ttl != "" {
			c.Ttl = in.Ttl
		}
		if in.AdmissionPolicy != "" {
			c.AdmissionPolicy = in.AdmissionPolicy
		}
		if in.IngestOnWrite != nil {
			c.IngestOnWrite = *in.IngestOnWrite
		}
		c.UpdateTime = gcsTimestamp()
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})

	srv.HandleFunc("POST /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}/disable", func(w http.ResponseWriter, r *http.Request) {
		bucket, id := sim.PathParam(r, "bucket"), sim.PathParam(r, "rapidCacheId")
		c, ok := caches.Get(key(bucket, id))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rapid cache %q not found in bucket %q", id, bucket)
			return
		}
		c.State = "disabled"
		c.UpdateTime = gcsTimestamp()
		caches.Put(key(bucket, id), c)
		sim.WriteJSON(w, http.StatusOK, gcsRecordDoneOperation(r, bucket, gcsStructToMap(c)))
	})
}

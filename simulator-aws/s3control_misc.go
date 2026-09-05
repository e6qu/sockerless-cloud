package main

import (
	"encoding/xml"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The rest of the S3 control plane: an access point's scope, and the buckets
// and access points that live outside the standard regional surface — on an
// Outpost, or on a directory bucket.

// s3AccessPointScope narrows what an access point admits: the prefixes it
// reaches, and the operations it allows on them.
type s3AccessPointScope struct {
	Prefixes    []string `xml:"Prefixes>Prefix,omitempty" json:"prefixes,omitempty"`
	Permissions []string `xml:"Permissions>Permission,omitempty" json:"permissions,omitempty"`
}

func registerS3ControlMisc(srv *sim.Server) {
	srv.HandleFunc("PUT /v20180820/accesspoint/{name}/scope", handleS3PutAccessPointScope)
	srv.HandleFunc("GET /v20180820/accesspoint/{name}/scope", handleS3GetAccessPointScope)
	srv.HandleFunc("DELETE /v20180820/accesspoint/{name}/scope", handleS3DeleteAccessPointScope)

	srv.HandleFunc("GET /v20180820/bucket", handleS3ListRegionalBuckets)
	srv.HandleFunc("DELETE /v20180820/bucket/{bucket}/lifecycleconfiguration", handleS3DeleteBucketLifecycleConfiguration)
	srv.HandleFunc("GET /v20180820/accesspointfordirectory", handleS3ListAccessPointsForDirectoryBuckets)
}

func handleS3PutAccessPointScope(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	body, ok := s3ControlReadXMLBody(w, r, "PutAccessPointScopeRequest")
	if !ok {
		return
	}
	scopeNode, hasScope := body.Child("Scope")
	if !hasScope {
		s3ControlError(w, "InvalidRequest", "Scope is required", http.StatusBadRequest)
		return
	}
	scope := s3AccessPointScope{
		Prefixes:    s3ControlChildTexts(scopeNode, "Prefixes", "Prefix"),
		Permissions: s3ControlChildTexts(scopeNode, "Permissions", "Permission"),
	}
	// A scope that names neither prefixes nor permissions restricts nothing,
	// which is the same as having no scope — the service rejects it rather
	// than storing a restriction that admits everything.
	if len(scope.Prefixes) == 0 && len(scope.Permissions) == 0 {
		s3ControlError(w, "InvalidRequest",
			"the scope must name at least one prefix or permission", http.StatusBadRequest)
		return
	}
	if !s3AccessPoints.Update(s3AccessPointKey(account, name),
		func(ap *S3AccessPoint) { ap.Scope = &scope }) {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// s3ControlChildTexts reads a list of leaf values out of a stored document.
func s3ControlChildTexts(node s3ControlXMLNode, listElement, itemElement string) []string {
	list, ok := node.Child(listElement)
	if !ok {
		return nil
	}
	var out []string
	for _, child := range list.Children {
		if child.Name == itemElement && child.Text != "" {
			out = append(out, child.Text)
		}
	}
	return out
}

func handleS3GetAccessPointScope(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	ap, ok := s3AccessPoints.Get(s3AccessPointKey(account, name))
	if !ok {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	scope := s3AccessPointScope{}
	if ap.Scope != nil {
		scope = *ap.Scope
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name           `xml:"GetAccessPointScopeResult"`
		Scope   s3AccessPointScope `xml:"Scope"`
	}{Scope: scope})
}

func handleS3DeleteAccessPointScope(w http.ResponseWriter, r *http.Request) {
	account, name := s3ControlAccountID(r), sim.PathParam(r, "name")
	if !s3AccessPoints.Update(s3AccessPointKey(account, name),
		func(ap *S3AccessPoint) { ap.Scope = nil }) {
		s3ControlError(w, "NoSuchAccessPoint", "The specified accesspoint does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3ListRegionalBuckets(w http.ResponseWriter, r *http.Request) {
	// The listing covers the account's regional buckets. A request that names
	// an Outpost asks for that Outpost's buckets instead, and this simulator
	// serves no Outposts, so the answer is an empty list rather than the
	// regional buckets under an Outpost's name.
	outpostID := r.Header.Get("x-amz-outpost-id")
	type entry struct {
		Bucket                   string `xml:"Bucket"`
		BucketArn                string `xml:"BucketArn"`
		PublicAccessBlockEnabled bool   `xml:"PublicAccessBlockEnabled"`
		CreationDate             string `xml:"CreationDate"`
		OutpostID                string `xml:"OutpostId,omitempty"`
	}
	var items []entry
	if outpostID == "" {
		for _, bucket := range s3Buckets_.List() {
			items = append(items, entry{
				Bucket:                   bucket.Name,
				BucketArn:                s3BucketARN(bucket.Name),
				PublicAccessBlockEnabled: s3BucketPublicAccessBlocked(bucket.Name),
				CreationDate:             bucket.CreationDate,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Bucket < items[j].Bucket })
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"ListRegionalBucketsResult"`
		Buckets []entry  `xml:"RegionalBucketList>RegionalBucket"`
	}{Buckets: items})
}

// s3BucketPublicAccessBlocked reports whether a bucket has a public access
// block configured, which is what this listing reports per bucket.
func s3BucketPublicAccessBlocked(bucket string) bool {
	_, _, _, ok := getStoredBucketSubresource(bucket, "publicAccessBlock")
	return ok
}

func handleS3DeleteBucketLifecycleConfiguration(w http.ResponseWriter, r *http.Request) {
	bucket := sim.PathParam(r, "bucket")
	if _, ok := s3Buckets_.Get(bucket); !ok {
		s3ControlError(w, "NoSuchBucket", "The specified bucket does not exist", http.StatusNotFound)
		return
	}
	s3BucketConfigs.Delete(s3BucketConfigKeyID(bucket, "lifecycle", ""))
	w.WriteHeader(http.StatusOK)
}

func handleS3ListAccessPointsForDirectoryBuckets(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	wanted := r.URL.Query().Get("directoryBucket")
	type entry struct {
		Name           string `xml:"Name"`
		NetworkOrigin  string `xml:"NetworkOrigin"`
		Bucket         string `xml:"Bucket"`
		AccessPointArn string `xml:"AccessPointArn"`
		Alias          string `xml:"Alias"`
	}
	var items []entry
	for _, ap := range s3AccessPoints.List() {
		if ap.AccountID != account {
			continue
		}
		if wanted != "" && ap.Bucket != wanted {
			continue
		}
		// A directory bucket is the one this listing covers; an access point
		// over a general-purpose bucket belongs to ListAccessPoints.
		bucket, ok := s3Buckets_.Get(ap.Bucket)
		if !ok || !s3BucketIsDirectory(bucket) {
			continue
		}
		items = append(items, entry{
			Name: ap.Name, NetworkOrigin: s3AccessPointNetworkOrigin(ap), Bucket: ap.Bucket,
			AccessPointArn: s3AccessPointARN(ap.AccountID, ap.Name),
			Alias:          s3AccessPointAlias(ap.Name, ap.AccountID),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	WriteXML(w, http.StatusOK, struct {
		XMLName      xml.Name `xml:"ListAccessPointsForDirectoryBucketsResult"`
		AccessPoints []entry  `xml:"AccessPointList>AccessPoint"`
	}{AccessPoints: items})
}

// s3BucketIsDirectory reports whether a bucket is a directory bucket, which
// S3 Express One Zone names with the zonal `--x-s3` suffix.
func s3BucketIsDirectory(bucket S3Bucket) bool {
	return strings.HasSuffix(bucket.Name, "--x-s3")
}

// s3ControlResourceTags holds the tags of every s3control resource that is
// tagged through the shared TagResource / UntagResource / ListTagsForResource
// trio rather than through its own operation, keyed by the resource's ARN.
var s3ControlResourceTags sim.Store[map[string]string]

func registerS3ControlTagging(srv *sim.Server) {
	s3ControlResourceTags = sim.MakeStore[map[string]string](srv.DB(), "s3_control_resource_tags")

	srv.HandleFunc("POST /v20180820/tags/{resourceArn...}", handleS3ControlTagResource)
	srv.HandleFunc("DELETE /v20180820/tags/{resourceArn...}", handleS3ControlUntagResource)
	srv.HandleFunc("GET /v20180820/tags/{resourceArn...}", handleS3ControlListTagsForResource)
}

// s3ControlTaggedResourceExists reports whether the ARN names something this
// simulator holds. Tagging a resource that does not exist is refused the way
// the service refuses it, rather than accumulating tags nothing can read back.
func s3ControlTaggedResourceExists(account, arn string) bool {
	switch {
	case strings.HasPrefix(arn, "arn:aws:s3:::"):
		_, ok := s3Buckets_.Get(strings.TrimPrefix(arn, "arn:aws:s3:::"))
		return ok
	case strings.Contains(arn, ":storage-lens/"):
		_, ok := s3StorageLensConfigurations.Get(
			s3AccessPointKey(account, arn[strings.LastIndex(arn, "/")+1:]))
		return ok
	case strings.Contains(arn, ":storage-lens-group/"):
		_, ok := s3StorageLensGroups.Get(s3AccessPointKey(account, arn[strings.LastIndex(arn, "/")+1:]))
		return ok
	case strings.Contains(arn, ":accesspoint/"):
		name := arn[strings.LastIndex(arn, "/")+1:]
		if _, ok := s3AccessPoints.Get(s3AccessPointKey(account, name)); ok {
			return true
		}
		_, ok := s3ObjectLambdaAccessPoints.Get(s3AccessPointKey(account, name))
		return ok
	case strings.Contains(arn, ":job/"):
		_, ok := s3BatchJobs.Get(s3AccessPointKey(account, arn[strings.LastIndex(arn, "/")+1:]))
		return ok
	case strings.Contains(arn, ":access-grants/"):
		_, ok := s3AccessGrantsInstances.Get(account)
		return ok
	}
	return false
}

func handleS3ControlTagResource(w http.ResponseWriter, r *http.Request) {
	account, arn := s3ControlAccountID(r), sim.PathParam(r, "resourceArn")
	if !s3ControlTaggedResourceExists(account, arn) {
		s3ControlError(w, "NotFoundException", "The specified resource does not exist", http.StatusNotFound)
		return
	}
	body, ok := s3ControlReadXMLBody(w, r, "TagResourceRequest")
	if !ok {
		return
	}
	tags := s3ControlTagsFrom(body, "Tags", "Tag")
	if len(tags) == 0 {
		s3ControlError(w, "InvalidRequest", "Tags is required", http.StatusBadRequest)
		return
	}
	existing, _ := s3ControlResourceTags.Get(arn)
	merged := map[string]string{}
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range tags {
		merged[k] = v
	}
	s3ControlResourceTags.Put(arn, merged)
	w.WriteHeader(http.StatusNoContent)
}

func handleS3ControlUntagResource(w http.ResponseWriter, r *http.Request) {
	account, arn := s3ControlAccountID(r), sim.PathParam(r, "resourceArn")
	if !s3ControlTaggedResourceExists(account, arn) {
		s3ControlError(w, "NotFoundException", "The specified resource does not exist", http.StatusNotFound)
		return
	}
	keys := r.URL.Query()["tagKeys"]
	if len(keys) == 0 {
		s3ControlError(w, "InvalidRequest", "tagKeys is required", http.StatusBadRequest)
		return
	}
	existing, _ := s3ControlResourceTags.Get(arn)
	remaining := map[string]string{}
	for k, v := range existing {
		remaining[k] = v
	}
	for _, list := range keys {
		for _, key := range strings.Split(list, ",") {
			delete(remaining, strings.TrimSpace(key))
		}
	}
	s3ControlResourceTags.Put(arn, remaining)
	w.WriteHeader(http.StatusNoContent)
}

func handleS3ControlListTagsForResource(w http.ResponseWriter, r *http.Request) {
	account, arn := s3ControlAccountID(r), sim.PathParam(r, "resourceArn")
	if !s3ControlTaggedResourceExists(account, arn) {
		s3ControlError(w, "NotFoundException", "The specified resource does not exist", http.StatusNotFound)
		return
	}
	tags, _ := s3ControlResourceTags.Get(arn)
	s3ControlWriteTags(w, "ListTagsForResourceResult", "Tag", tags)
}

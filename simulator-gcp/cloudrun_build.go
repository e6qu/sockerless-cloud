package main

import (
	"fmt"
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Cloud Run v2's hosted build path. Both verbs ride the /v2 locations segment
// that Cloud Functions also claims, so they dispatch from its fan-in and fall
// through when the verb is not theirs.
//
// The export family beside them stays unserved: exportImage, exportMetadata,
// exportImageMetadata, exportProjectMetadata and exportStatus report Google's
// own image-export pipeline, which this simulator does not run — there is no
// export to report the status of.
func cloudRunLocationVerbHandled(w http.ResponseWriter, r *http.Request, collection, verb string) bool {
	switch {
	case collection == "builds" && verb == "submit":
		cloudRunSubmitBuild(w, r)
	case verb == "uploadSource":
		cloudRunUploadSource(w, r)
	default:
		return false
	}
	return true
}

// cloudRunSubmitBuild hands the request to the Cloud Build this simulator
// serves, so the returned operation names a build that really ran.
func cloudRunSubmitBuild(w http.ResponseWriter, r *http.Request) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	var req struct {
		ImageURI      string `json:"imageUri"`
		ServiceAcct   string `json:"serviceAccount"`
		WorkerPool    string `json:"workerPool"`
		StorageSource *struct {
			Bucket     string `json:"bucket"`
			Object     string `json:"object"`
			Generation string `json:"generation"`
		} `json:"storageSource"`
		DockerBuild    *struct{} `json:"dockerBuild"`
		BuildpackBuild *struct {
			Runtime      string `json:"runtime"`
			FunctionName string `json:"functionTarget"`
		} `json:"buildpackBuild"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid submitBuild body: %v", err)
		return
	}
	if req.ImageURI == "" {
		sim.GCPError(w, http.StatusBadRequest, "imageUri is required", "INVALID_ARGUMENT")
		return
	}
	if req.StorageSource == nil || req.StorageSource.Bucket == "" || req.StorageSource.Object == "" {
		sim.GCPError(w, http.StatusBadRequest,
			"storageSource.bucket and storageSource.object are required", "INVALID_ARGUMENT")
		return
	}
	if _, ok := gcsObjects.Get(req.StorageSource.Bucket + "/" + req.StorageSource.Object); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"source object %q not found in bucket %q", req.StorageSource.Object, req.StorageSource.Bucket)
		return
	}

	build := Build{
		ID:        generateUUID(),
		ProjectID: project,
		Status:    "QUEUED",
		Images:    []string{req.ImageURI},
		Source: &BuildSource{StorageSource: &StorageSource{
			Bucket: req.StorageSource.Bucket,
			Object: req.StorageSource.Object,
		}},
	}
	build.Name = fmt.Sprintf("projects/%s/locations/%s/builds/%s", project, location, build.ID)
	cbBuilds.Put(build.ID, build)
	result := executeCancellableBuild(r.Context(), build)

	operation := CloudBuildOperation{
		Name: fmt.Sprintf("operations/build/%s/%s", project, result.ID),
		Done: true,
		Metadata: map[string]any{
			"@type": "type.googleapis.com/google.devtools.cloudbuild.v1.BuildOperationMetadata",
			"build": result,
		},
	}
	if result.Status == "SUCCESS" {
		operation.Response = map[string]any{"@type": "type.googleapis.com/google.devtools.cloudbuild.v1.Build"}
		for k, v := range structToMap(result) {
			operation.Response[k] = v
		}
	} else {
		operation.Error = cloudBuildOperationError(result)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"buildOperation": operation})
}

// cloudRunUploadSource names the Cloud Storage location a client uploads its
// source to before submitting a build. The bucket is the one Cloud Run uses per
// project and region.
func cloudRunUploadSource(w http.ResponseWriter, r *http.Request) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"cloudStorageSource": map[string]any{
			"bucket": fmt.Sprintf("run-sources-%s-%s", project, location),
			"object": "services/source-" + generateUUID() + ".zip",
		},
	})
}

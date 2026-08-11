package gcp_sdk_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runv2 "google.golang.org/api/run/v2"
)

// Cloud Run worker-pool automatic scaling.
//
// google.cloud.run.v2.WorkerPoolScaling carries scalingMode (AUTOMATIC/MANUAL),
// minInstanceCount and maxInstanceCount alongside manualInstanceCount. The
// pinned Cloud Run Discovery revision publishes only manualInstanceCount, so
// the Discovery-generated Go client below cannot carry the other three — but
// gcloud's own generated Cloud Run v2 client and the google Terraform provider
// both send and read them, and a simulator that dropped them would make every
// automatically-scaled worker pool re-plan (see
// specs/SIM_SURFACE_TABLES/gcp-cloudrun.md). The wire round trip is asserted
// here against the same authenticated data plane the official clients use; the
// end-to-end no-drift proof lives in the terraform-tests worker pool.

func TestSDK_RunV2_WorkerPoolAutomaticScaling(t *testing.T) {
	const id = "sdk-wp-autoscaling"
	base := "/v2/projects/" + crV2Project + "/locations/" + crV2Location + "/workerPools"

	status, body := gcpDataPlaneRequest(t, http.MethodPost, "", base+"?workerPoolId="+id, `{
		"template": {"containers": [{"image": "gcr.io/`+crV2Project+`/`+id+`"}]},
		"scaling": {"scalingMode": "AUTOMATIC", "minInstanceCount": 2, "maxInstanceCount": 9}
	}`)
	require.Equal(t, http.StatusOK, status, body)
	t.Cleanup(func() {
		gcpDataPlaneRequest(t, http.MethodDelete, "", base+"/"+id, "")
	})

	var pool struct {
		Scaling struct {
			ScalingMode         string `json:"scalingMode"`
			MinInstanceCount    int    `json:"minInstanceCount"`
			MaxInstanceCount    int    `json:"maxInstanceCount"`
			ManualInstanceCount int    `json:"manualInstanceCount"`
		} `json:"scaling"`
	}
	status, body = gcpDataPlaneRequest(t, http.MethodGet, "", base+"/"+id, "")
	require.Equal(t, http.StatusOK, status, body)
	require.NoError(t, json.Unmarshal([]byte(body), &pool))
	assert.Equal(t, "AUTOMATIC", pool.Scaling.ScalingMode)
	assert.Equal(t, 2, pool.Scaling.MinInstanceCount)
	assert.Equal(t, 9, pool.Scaling.MaxInstanceCount)
	assert.Zero(t, pool.Scaling.ManualInstanceCount,
		"manualInstanceCount is unset under automatic scaling and must not be invented")

	// The list response carries the same members as the get response.
	status, body = gcpDataPlaneRequest(t, http.MethodGet, "", base, "")
	require.Equal(t, http.StatusOK, status, body)
	var list struct {
		WorkerPools []struct {
			Name    string `json:"name"`
			Scaling struct {
				ScalingMode      string `json:"scalingMode"`
				MinInstanceCount int    `json:"minInstanceCount"`
				MaxInstanceCount int    `json:"maxInstanceCount"`
			} `json:"scaling"`
		} `json:"workerPools"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	var listed bool
	for _, wp := range list.WorkerPools {
		if wp.Name == crV2Parent+"/workerPools/"+id {
			listed = true
			assert.Equal(t, "AUTOMATIC", wp.Scaling.ScalingMode)
			assert.Equal(t, 2, wp.Scaling.MinInstanceCount)
			assert.Equal(t, 9, wp.Scaling.MaxInstanceCount)
		}
	}
	assert.True(t, listed, "ListWorkerPools must include the automatically-scaled pool")

	// The official Discovery client reads the same worker pool without error:
	// members its pinned revision does not declare are ignored, not fatal.
	svc := newRunV2RESTService(t)
	got, err := svc.Projects.Locations.WorkerPools.Get(crV2Parent + "/workerPools/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, crV2Parent+"/workerPools/"+id, got.Name)
	require.NotNil(t, got.Scaling)
	assert.Equal(t, int64(0), got.Scaling.ManualInstanceCount)

	// A manual-scaling pool the official client can express round-trips through
	// that client end to end.
	const manualID = "sdk-wp-manual-scaling"
	manualName := crV2Parent + "/workerPools/" + manualID
	op, err := svc.Projects.Locations.WorkerPools.Create(crV2Parent, &runv2.GoogleCloudRunV2WorkerPool{
		Scaling: &runv2.GoogleCloudRunV2WorkerPoolScaling{ManualInstanceCount: 4},
		Template: &runv2.GoogleCloudRunV2WorkerPoolRevisionTemplate{
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: "gcr.io/" + crV2Project + "/" + manualID}},
		},
	}).WorkerPoolId(manualID).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		if delOp, err := svc.Projects.Locations.WorkerPools.Delete(manualName).Do(); err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})
	manual, err := svc.Projects.Locations.WorkerPools.Get(manualName).Do()
	require.NoError(t, err)
	require.NotNil(t, manual.Scaling)
	assert.Equal(t, int64(4), manual.Scaling.ManualInstanceCount)
}

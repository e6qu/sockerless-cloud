package gcp_sdk_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runv2 "google.golang.org/api/run/v2"
)

// google.cloud.run.v2.WorkerPoolScaling carries scalingMode,
// minInstanceCount and maxInstanceCount beside manualInstanceCount. The pinned
// Discovery revision publishes only manualInstanceCount, so the generated Go
// client cannot carry the other three, but gcloud's own generated client and
// the Terraform provider send and read them; dropping them makes every
// automatically scaled pool re-plan. The Terraform suite holds the no-drift
// proof.

func TestSDK_RunV2_WorkerPoolAutomaticScaling(t *testing.T) {
	t.Parallel()
	id := uniquename.New("sdk-wp-autoscaling")
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
			Containers: []*runv2.GoogleCloudRunV2Container{holdingContainerREST()},
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

package gcp_sdk_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runv2 "google.golang.org/api/run/v2"
)

// A Cloud Run instance's urls reach its ingress container through the Cloud
// Run front end, which admits a caller only with an ID token for the URL
// whose principal holds run.routes.invoke on the instance.
func TestSDK_CloudRun_InstanceURLServesThroughTheFrontEnd(t *testing.T) {
	name := createRunV2Instance(t, "rest-inst-url")
	svc := newRunV2RESTService(t)
	inst, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	require.Len(t, inst.Urls, 1)
	instanceURL := inst.Urls[0]

	answer := invokeServiceRaw(t, instanceURL, "/", nil)
	assert.Equal(t, http.StatusForbidden, answer.status, "a request without a credential is refused: %s", answer.body)
	assert.Contains(t, answer.body, cloudRunForbiddenRoot)

	_, err = svc.Projects.Locations.Instances.SetIamPolicy(name, &runv2.GoogleIamV1SetIamPolicyRequest{
		Policy: &runv2.GoogleIamV1Policy{Bindings: []*runv2.GoogleIamV1Binding{{
			Role:    "roles/run.invoker",
			Members: []string{"serviceAccount:" + sdkInvokerEmail(t)},
		}}},
	}).Do()
	require.NoError(t, err)

	answer = invokeServiceRaw(t, instanceURL, "/", http.Header{
		"Authorization": {"Bearer " + idTokenFor(t, sdkInvokerEmail(t), "https://other-service-abcdefghij-us-central1.a.run.app")},
	})
	assert.Equal(t, http.StatusUnauthorized, answer.status, "an ID token for another URL is refused: %s", answer.body)

	status, _, body := invokeService(t, invokerIDToken(t, instanceURL), instanceURL, http.MethodPut, "/items/7?source=sdk", "")
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "PUT /items/7?source=sdk", body, "the instance's ingress container answers the request")
}

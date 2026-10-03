package gcp_sdk_test

import (
	"net/http"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A service's ingress container that dependsOn a sidecar starts only once the
// sidecar has passed its startup probe: the ingress checks once, as it starts,
// that the sidecar listens, and exits if not; the sidecar opens its port three
// seconds after it starts. The request reaches the ingress only when the
// instance started its containers in that order.
func TestSDK_CloudRunV2Services_DependsOnWaitsForTheSidecarsStartupProbe(t *testing.T) {
	client := newServicesClient(t)
	svc := createInvokableService(t, client, "v2-svc-depends-on", &runpb.Service{
		Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{
			{
				Name:      "ingress",
				Image:     httpProbeImageName,
				Args:      []string{"after-sidecar", "cloudrun-sidecar-started-first"},
				Ports:     []*runpb.ContainerPort{{ContainerPort: 8080}},
				DependsOn: []string{"sidecar"},
			},
			{
				Name:  "sidecar",
				Image: commandImageName,
				Args:  []string{"http", "9090", "ready", "3"},
				StartupProbe: &runpb.Probe{
					PeriodSeconds:    1,
					TimeoutSeconds:   1,
					FailureThreshold: 30,
					ProbeType:        &runpb.Probe_TcpSocket{TcpSocket: &runpb.TCPSocketAction{Port: 9090}},
				},
			},
		}},
	})

	status, _, body := invokeService(t, invokerIDToken(t, svc.Uri), svc.Uri, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "cloudrun-sidecar-started-first", body)
}

// A dependsOn that names no container of the template, or that forms a cycle,
// is refused when the service is created.
func TestSDK_CloudRunV2Services_CreateRefusesUnresolvableDependsOn(t *testing.T) {
	client := newServicesClient(t)
	for _, containers := range [][]*runpb.Container{
		{{Name: "ingress", Image: httpProbeImageName, DependsOn: []string{"absent"}}},
		{
			{Name: "a", Image: httpProbeImageName, DependsOn: []string{"b"}},
			{Name: "b", Image: httpProbeImageName, DependsOn: []string{"a"}},
		},
	} {
		_, err := client.CreateService(ctx, &runpb.CreateServiceRequest{
			Parent:    "projects/test-project/locations/us-central1",
			ServiceId: uniqueName("v2-svc-bad-depends"),
			Service:   &runpb.Service{Template: &runpb.RevisionTemplate{Containers: containers}},
		})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "CreateService error: %v", err)
	}
}

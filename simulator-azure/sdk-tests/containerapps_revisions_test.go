package azure_sdk_test

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// servingContainerApp is a container app whose one container answers every
// request on port 80 with response, behind internal ingress.
func servingContainerApp(rg, response string, configuration *armappcontainers.Configuration) armappcontainers.ContainerApp {
	if configuration == nil {
		configuration = &armappcontainers.Configuration{}
	}
	configuration.Ingress = &armappcontainers.Ingress{
		External:   to.Ptr(false),
		TargetPort: to.Ptr[int32](80),
	}
	return armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
				"/providers/Microsoft.App/managedEnvironments/sim-env"),
			Configuration: configuration,
			Template:      servingTemplate(response),
		},
	}
}

func servingTemplate(response string) *armappcontainers.Template {
	return &armappcontainers.Template{
		Containers: []*armappcontainers.Container{{
			Name:  to.Ptr("main"),
			Image: to.Ptr(commandImageName),
			Args:  []*string{to.Ptr("serve"), to.Ptr("80"), to.Ptr(response)},
		}},
		Scale: &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
	}
}

// containerAppIngressGet sends a request to the simulator carrying host, the
// way a client resolving an app's or a revision's FQDN to the platform's front
// end does, and returns the body the replica answered with.
func containerAppIngressGet(t *testing.T, host string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "ingress for %s: %s", host, body)
	return strings.TrimSpace(string(body))
}

func listContainerAppRevisions(t *testing.T, revisions *armappcontainers.ContainerAppsRevisionsClient, rg, app string) map[string]*armappcontainers.Revision {
	t.Helper()
	out := map[string]*armappcontainers.Revision{}
	pager := revisions.NewListRevisionsPager(rg, app, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, rev := range page.Value {
			out[ptrVal(rev.Name)] = rev
		}
	}
	return out
}

func updateContainerApp(t *testing.T, client *armappcontainers.ContainerAppsClient, rg, name string, patch armappcontainers.ContainerApp) armappcontainers.ContainerApp {
	t.Helper()
	poller, err := client.BeginUpdate(ctx, rg, name, patch, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	got, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	return got.ContainerApp
}

// A single-revision app turns each template change into a new revision named
// after the one before it, sends all its traffic there, and keeps the previous
// revision as an inactive one, up to maxInactiveRevisions.
func TestSDK_ContainerAppsRevisions_SingleModeTemplateChangesCreateRevisions(t *testing.T) {
	rg, name := uniqueName("sdk-aca-rev-single-rg"), uniqueName("revsingle")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	revisions, err := armappcontainers.NewContainerAppsRevisionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, servingContainerApp(rg, "v1", nil), nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	first := ptrVal(created.Properties.LatestRevisionName)
	assert.Regexp(t, regexp.MustCompile("^"+name+"--[a-z0-9]{7}$"), first, "the first revision gets a random suffix")
	cfg := created.Properties.Configuration
	assert.Equal(t, armappcontainers.ActiveRevisionsModeSingle, ptrVal(cfg.ActiveRevisionsMode))
	assert.EqualValues(t, 100, ptrVal(cfg.MaxInactiveRevisions))
	require.Len(t, cfg.Ingress.Traffic, 1)
	assert.True(t, ptrVal(cfg.Ingress.Traffic[0].LatestRevision))
	assert.EqualValues(t, 100, ptrVal(cfg.Ingress.Traffic[0].Weight))
	appFqdn := ptrVal(cfg.Ingress.Fqdn)
	assert.True(t, strings.HasPrefix(appFqdn, name+".internal."), "the app answers on its own FQDN: %s", appFqdn)
	assert.Equal(t, first+strings.TrimPrefix(appFqdn, name), ptrVal(created.Properties.LatestRevisionFqdn))
	assert.Equal(t, "v1", containerAppIngressGet(t, appFqdn))

	updated := updateContainerApp(t, client, rg, name, armappcontainers.ContainerApp{
		Properties: &armappcontainers.ContainerAppProperties{Template: servingTemplate("v2")},
	})
	second := ptrVal(updated.Properties.LatestRevisionName)
	assert.Equal(t, name+"--0000001", second, "a template change creates the next numbered revision")
	assert.Equal(t, second, ptrVal(updated.Properties.LatestReadyRevisionName))
	assert.Equal(t, "v2", containerAppIngressGet(t, appFqdn), "a single-revision app sends its traffic to the latest revision")

	listed := listContainerAppRevisions(t, revisions, rg, name)
	require.Len(t, listed, 2)
	old, latest := listed[first].Properties, listed[second].Properties
	assert.False(t, ptrVal(old.Active), "the previous revision is deactivated")
	assert.EqualValues(t, 0, ptrVal(old.Replicas))
	assert.EqualValues(t, 0, ptrVal(old.TrafficWeight))
	require.NotNil(t, old.LastActiveTime, "an inactive revision records when it was last active")
	assert.True(t, ptrVal(latest.Active))
	assert.EqualValues(t, 1, ptrVal(latest.Replicas))
	assert.EqualValues(t, 100, ptrVal(latest.TrafficWeight))
	assert.False(t, latest.CreatedTime.Before(*old.CreatedTime))
	assert.Equal(t, "v1", ptrVal(old.Template.Containers[0].Args[2]), "a revision keeps the template it was created with")

	// A configuration change applies to the revisions the app has.
	updated = updateContainerApp(t, client, rg, name, armappcontainers.ContainerApp{
		Properties: &armappcontainers.ContainerAppProperties{
			Configuration: &armappcontainers.Configuration{MaxInactiveRevisions: to.Ptr[int32](0)},
		},
	})
	assert.Equal(t, second, ptrVal(updated.Properties.LatestRevisionName), "a configuration change creates no revision")
	listed = listContainerAppRevisions(t, revisions, rg, name)
	require.Len(t, listed, 1, "maxInactiveRevisions 0 keeps no inactive revision")
	assert.Contains(t, listed, second)

	// A revisionSuffix names the revision; reusing one names a revision the app already has.
	updated = updateContainerApp(t, client, rg, name, armappcontainers.ContainerApp{
		Properties: &armappcontainers.ContainerAppProperties{Template: func() *armappcontainers.Template {
			tmpl := servingTemplate("v3")
			tmpl.RevisionSuffix = to.Ptr("blue")
			return tmpl
		}()},
	})
	assert.Equal(t, name+"--blue", ptrVal(updated.Properties.LatestRevisionName))
	reused := servingTemplate("v4")
	reused.RevisionSuffix = to.Ptr("blue")
	refused, err := client.BeginUpdate(ctx, rg, name, armappcontainers.ContainerApp{
		Properties: &armappcontainers.ContainerAppProperties{Template: reused},
	}, nil)
	if err == nil {
		_, err = refused.PollUntilDone(ctx, nil)
	}
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "a reused revisionSuffix is refused: %v", err)
	assert.Equal(t, http.StatusBadRequest, respErr.StatusCode)
}

// A multiple-revision app keeps its revisions active, splits its ingress
// traffic by the weights its configuration gives them, and lets a revision be
// deactivated, activated and restarted on its own.
func TestSDK_ContainerAppsRevisions_MultipleModeSplitsTraffic(t *testing.T) {
	rg, name := uniqueName("sdk-aca-rev-multi-rg"), uniqueName("revmulti")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	revisions, err := armappcontainers.NewContainerAppsRevisionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	replicas, err := armappcontainers.NewContainerAppsRevisionReplicasClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, servingContainerApp(rg, "blue", &armappcontainers.Configuration{
		ActiveRevisionsMode: to.Ptr(armappcontainers.ActiveRevisionsModeMultiple),
	}), nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	blue := ptrVal(created.Properties.LatestRevisionName)
	appFqdn := ptrVal(created.Properties.Configuration.Ingress.Fqdn)

	split := []*armappcontainers.TrafficWeight{
		{RevisionName: to.Ptr(blue), Weight: to.Ptr[int32](50)},
		{LatestRevision: to.Ptr(true), Weight: to.Ptr[int32](50)},
	}
	updated := updateContainerApp(t, client, rg, name, armappcontainers.ContainerApp{
		Properties: &armappcontainers.ContainerAppProperties{
			Configuration: &armappcontainers.Configuration{Ingress: &armappcontainers.Ingress{Traffic: split}},
			Template:      servingTemplate("green"),
		},
	})
	green := ptrVal(updated.Properties.LatestRevisionName)
	require.NotEqual(t, blue, green)

	listed := listContainerAppRevisions(t, revisions, rg, name)
	require.Len(t, listed, 2)
	for _, rev := range []string{blue, green} {
		assert.True(t, ptrVal(listed[rev].Properties.Active), "%s stays active", rev)
		assert.EqualValues(t, 50, ptrVal(listed[rev].Properties.TrafficWeight), "%s carries half the traffic", rev)
	}

	answers := map[string]int{}
	for range 40 {
		answers[containerAppIngressGet(t, appFqdn)]++
	}
	assert.Positive(t, answers["blue"], "the split sends traffic to %s: %v", blue, answers)
	assert.Positive(t, answers["green"], "the split sends traffic to %s: %v", green, answers)
	assert.Equal(t, "blue", containerAppIngressGet(t, ptrVal(listed[blue].Properties.Fqdn)),
		"a revision's own FQDN reaches that revision")

	refused, err := client.BeginUpdate(ctx, rg, name, armappcontainers.ContainerApp{
		Properties: &armappcontainers.ContainerAppProperties{
			Configuration: &armappcontainers.Configuration{Ingress: &armappcontainers.Ingress{Traffic: []*armappcontainers.TrafficWeight{
				{RevisionName: to.Ptr(blue), Weight: to.Ptr[int32](40)},
				{LatestRevision: to.Ptr(true), Weight: to.Ptr[int32](50)},
			}}},
		},
	}, nil)
	if err == nil {
		_, err = refused.PollUntilDone(ctx, nil)
	}
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "weights that do not add up to 100 are refused: %v", err)
	assert.Equal(t, http.StatusBadRequest, respErr.StatusCode)

	updateContainerApp(t, client, rg, name, armappcontainers.ContainerApp{
		Properties: &armappcontainers.ContainerAppProperties{
			Configuration: &armappcontainers.Configuration{Ingress: &armappcontainers.Ingress{Traffic: []*armappcontainers.TrafficWeight{
				{LatestRevision: to.Ptr(true), Weight: to.Ptr[int32](100)},
			}}},
		},
	})
	_, err = revisions.DeactivateRevision(ctx, rg, name, blue, nil)
	require.NoError(t, err)
	got, err := revisions.GetRevision(ctx, rg, name, blue, nil)
	require.NoError(t, err)
	assert.False(t, ptrVal(got.Properties.Active))
	assert.EqualValues(t, 0, ptrVal(got.Properties.Replicas))
	assert.NotNil(t, got.Properties.LastActiveTime)
	inactiveReplicas, err := replicas.ListReplicas(ctx, rg, name, blue, nil)
	require.NoError(t, err)
	assert.Empty(t, inactiveReplicas.Value, "an inactive revision runs no replica")

	_, err = revisions.ActivateRevision(ctx, rg, name, blue, nil)
	require.NoError(t, err)
	got, err = revisions.GetRevision(ctx, rg, name, blue, nil)
	require.NoError(t, err)
	assert.True(t, ptrVal(got.Properties.Active))
	assert.EqualValues(t, 1, ptrVal(got.Properties.Replicas))
	assert.EqualValues(t, 0, ptrVal(got.Properties.TrafficWeight), "an activated revision carries only the traffic the split gives it")
	assert.Equal(t, "blue", containerAppIngressGet(t, ptrVal(got.Properties.Fqdn)))

	before, err := replicas.ListReplicas(ctx, rg, name, green, nil)
	require.NoError(t, err)
	require.Len(t, before.Value, 1)
	_, err = revisions.RestartRevision(ctx, rg, name, green, nil)
	require.NoError(t, err)
	after, err := replicas.ListReplicas(ctx, rg, name, green, nil)
	require.NoError(t, err)
	require.Len(t, after.Value, 1)
	assert.NotEqual(t, ptrVal(before.Value[0].Name), ptrVal(after.Value[0].Name), "a restarted revision runs a new replica")
	assert.Equal(t, "green", containerAppIngressGet(t, appFqdn))
}

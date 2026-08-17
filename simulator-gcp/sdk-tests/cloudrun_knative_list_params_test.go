package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	run "google.golang.org/api/run/v1"
	runv2 "google.golang.org/api/run/v2"
)

// The Cloud Run Admin v1 (Knative) list methods take `labelSelector`, `limit`
// and `continue`, and answer with a ListMeta carrying the cursor that
// continues a truncated page. These tests drive the parameters through the
// canonical google.golang.org/api/run/v1 client over the five collections that
// serve Services and the resources Cloud Run reconciles alongside them —
// services, configurations, revisions, routes and domain mappings — because a
// client that pages or selects and silently receives the whole collection
// cannot tell that it did.

// knativeServiceBody builds a Service whose ObjectMeta carries the labels a
// labelSelector selects on. Cloud Run copies a Service's labels onto the
// Configuration, Route and Revision it reconciles, so one set of labels
// reaches every collection under test.
func knativeServiceBody(name string, labels map[string]string) *run.Service {
	return &run.Service{
		Metadata: &run.ObjectMeta{Name: name, Labels: labels},
		Spec: &run.ServiceSpec{Template: &run.RevisionTemplate{
			Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: "gcr.io/x/" + name}}},
		}},
	}
}

// knativeListContinue returns the continue cursor a Knative list response
// carries, which is the empty string when the response carries no ListMeta at
// all. Reading the cursor through it keeps "there is no cursor" one assertion
// instead of a check a nil ListMeta skips.
func knativeListContinue(meta *run.ListMeta) string {
	if meta == nil {
		return ""
	}
	return meta.Continue
}

func TestCloudRunV1_ServicesList_LabelSelectorAndPaging(t *testing.T) {
	svc := newRunV1(t)
	const namespace = "test-project-knative-list-params"
	parent := "namespaces/" + namespace

	names := []string{"list-svc-a", "list-svc-b", "list-svc-c"}
	tiers := []string{"frontend", "frontend", "batch"}
	for i, name := range names {
		_, err := svc.Namespaces.Services.Create(parent,
			knativeServiceBody(name, map[string]string{"tier": tiers[i]})).Do()
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = svc.Namespaces.Services.Delete(parent + "/services/" + name).Do()
		})
	}

	// An unparameterised list is the whole namespace, is labelled as the kind
	// it is, and continues nowhere — a cursor here would send a client back
	// for a page that does not exist.
	all, err := svc.Namespaces.Services.List(parent).Do()
	require.NoError(t, err)
	require.Len(t, all.Items, 3)
	assert.Equal(t, "serving.knative.dev/v1", all.ApiVersion)
	assert.Equal(t, "ServiceList", all.Kind)
	assert.Empty(t, knativeListContinue(all.Metadata), "an untruncated list carries no continue cursor")

	// limit + continue walk the collection in disjoint pages that cover it
	// exactly once.
	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		call := svc.Namespaces.Services.List(parent).Limit(2)
		if cursor != "" {
			call = call.Continue(cursor)
		}
		page, err := call.Do()
		require.NoError(t, err)
		pages++
		require.LessOrEqual(t, len(page.Items), 2, "a page never exceeds the limit")
		for _, item := range page.Items {
			seen[item.Metadata.Name]++
		}
		if page.Metadata == nil || page.Metadata.Continue == "" {
			break
		}
		cursor = page.Metadata.Continue
		require.LessOrEqual(t, pages, 4, "paging must terminate")
	}
	assert.Equal(t, 2, pages, "three services page two at a time in two pages")
	assert.Equal(t, map[string]int{names[0]: 1, names[1]: 1, names[2]: 1}, seen,
		"every service appears exactly once across the pages")

	// labelSelector narrows the collection.
	frontend, err := svc.Namespaces.Services.List(parent).LabelSelector("tier=frontend").Do()
	require.NoError(t, err)
	require.Len(t, frontend.Items, 2)
	for _, item := range frontend.Items {
		assert.Equal(t, "frontend", item.Metadata.Labels["tier"])
	}

	batch, err := svc.Namespaces.Services.List(parent).LabelSelector("tier!=frontend").Do()
	require.NoError(t, err)
	require.Len(t, batch.Items, 1)
	assert.Equal(t, names[2], batch.Items[0].Metadata.Name)

	// The selector and the cursor compose: the selected subset pages too.
	selectedPage, err := svc.Namespaces.Services.List(parent).LabelSelector("tier=frontend").Limit(1).Do()
	require.NoError(t, err)
	require.Len(t, selectedPage.Items, 1)
	require.NotNil(t, selectedPage.Metadata)
	require.NotEmpty(t, selectedPage.Metadata.Continue)
	selectedRest, err := svc.Namespaces.Services.List(parent).
		LabelSelector("tier=frontend").Continue(selectedPage.Metadata.Continue).Do()
	require.NoError(t, err)
	require.Len(t, selectedRest.Items, 1)
	assert.NotEqual(t, selectedPage.Items[0].Metadata.Name, selectedRest.Items[0].Metadata.Name)

	// A malformed cursor is refused rather than silently reset to the start.
	_, err = svc.Namespaces.Services.List(parent).Continue("not-a-cursor").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-cursor")
}

func TestCloudRunV1_ReconciledChildrenList_LabelSelectorAndPaging(t *testing.T) {
	svc := newRunV1(t)
	const namespace = "test-project-knative-children-list-params"
	parent := "namespaces/" + namespace

	for _, name := range []string{"child-svc-a", "child-svc-b"} {
		_, err := svc.Namespaces.Services.Create(parent,
			knativeServiceBody(name, map[string]string{"tier": "frontend"})).Do()
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = svc.Namespaces.Services.Delete(parent + "/services/" + name).Do()
		})
	}
	// A second deploy of one service adds a second revision, so the revision
	// collection holds three members while the others hold two.
	_, err := svc.Namespaces.Services.ReplaceService(parent+"/services/child-svc-a",
		knativeServiceBody("child-svc-a", map[string]string{"tier": "frontend"})).Do()
	require.NoError(t, err)

	// Configurations.
	cfgPage, err := svc.Namespaces.Configurations.List(parent).Limit(1).Do()
	require.NoError(t, err)
	require.Len(t, cfgPage.Items, 1)
	require.NotNil(t, cfgPage.Metadata)
	require.NotEmpty(t, cfgPage.Metadata.Continue)
	cfgRest, err := svc.Namespaces.Configurations.List(parent).Continue(cfgPage.Metadata.Continue).Do()
	require.NoError(t, err)
	require.Len(t, cfgRest.Items, 1)
	assert.NotEqual(t, cfgPage.Items[0].Metadata.Name, cfgRest.Items[0].Metadata.Name)
	cfgSelected, err := svc.Namespaces.Configurations.List(parent).LabelSelector("tier=frontend").Do()
	require.NoError(t, err)
	assert.Len(t, cfgSelected.Items, 2)
	cfgUnselected, err := svc.Namespaces.Configurations.List(parent).LabelSelector("tier=batch").Do()
	require.NoError(t, err)
	assert.Empty(t, cfgUnselected.Items)

	// Routes.
	rtPage, err := svc.Namespaces.Routes.List(parent).Limit(1).Do()
	require.NoError(t, err)
	require.Len(t, rtPage.Items, 1)
	require.NotNil(t, rtPage.Metadata)
	require.NotEmpty(t, rtPage.Metadata.Continue)
	rtRest, err := svc.Namespaces.Routes.List(parent).Continue(rtPage.Metadata.Continue).Do()
	require.NoError(t, err)
	require.Len(t, rtRest.Items, 1)
	assert.NotEqual(t, rtPage.Items[0].Metadata.Name, rtRest.Items[0].Metadata.Name)
	rtSelected, err := svc.Namespaces.Routes.List(parent).LabelSelector("tier=batch").Do()
	require.NoError(t, err)
	assert.Empty(t, rtSelected.Items)

	// Revisions: three across the two services, walked two at a time.
	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		call := svc.Namespaces.Revisions.List(parent).Limit(2)
		if cursor != "" {
			call = call.Continue(cursor)
		}
		page, err := call.Do()
		require.NoError(t, err)
		pages++
		require.LessOrEqual(t, len(page.Items), 2)
		for _, item := range page.Items {
			seen[item.Metadata.Name]++
		}
		if page.Metadata == nil || page.Metadata.Continue == "" {
			break
		}
		cursor = page.Metadata.Continue
		require.LessOrEqual(t, pages, 4, "paging must terminate")
	}
	assert.Equal(t, 2, pages)
	require.Len(t, seen, 3, "every revision appears across the pages")
	for name, count := range seen {
		assert.Equal(t, 1, count, "revision %s appeared more than once", name)
	}
	revSelected, err := svc.Namespaces.Revisions.List(parent).LabelSelector("tier").Do()
	require.NoError(t, err)
	assert.Len(t, revSelected.Items, 3, "a bare key selects every revision carrying the label")
	revUnselected, err := svc.Namespaces.Revisions.List(parent).LabelSelector("absent-key").Do()
	require.NoError(t, err)
	assert.Empty(t, revUnselected.Items)
}

func TestCloudRunV1_DomainMappingsList_LabelSelectorAndPaging(t *testing.T) {
	svc := newRunV1(t)
	const namespace = "test-project-knative-domainmappings-list-params"
	parent := "namespaces/" + namespace

	_, err := svc.Namespaces.Services.Create(parent,
		knativeServiceBody("dm-target", nil)).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.Namespaces.Services.Delete(parent + "/services/dm-target").Do()
	})

	domains := []string{"a.list-params.example.com", "b.list-params.example.com"}
	envs := []string{"prod", "staging"}
	for i, domain := range domains {
		_, err := svc.Namespaces.Domainmappings.Create(parent, &run.DomainMapping{
			Metadata: &run.ObjectMeta{Name: domain, Labels: map[string]string{"env": envs[i]}},
			Spec:     &run.DomainMappingSpec{RouteName: "dm-target"},
		}).Do()
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = svc.Namespaces.Domainmappings.Delete(parent + "/domainmappings/" + domain).Do()
		})
	}

	page, err := svc.Namespaces.Domainmappings.List(parent).Limit(1).Do()
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.Metadata)
	require.NotEmpty(t, page.Metadata.Continue)
	rest, err := svc.Namespaces.Domainmappings.List(parent).Continue(page.Metadata.Continue).Do()
	require.NoError(t, err)
	require.Len(t, rest.Items, 1)
	assert.NotEqual(t, page.Items[0].Metadata.Name, rest.Items[0].Metadata.Name)

	selected, err := svc.Namespaces.Domainmappings.List(parent).LabelSelector("env=prod").Do()
	require.NoError(t, err)
	require.Len(t, selected.Items, 1)
	assert.Equal(t, domains[0], selected.Items[0].Metadata.Name)
}

// TestSDK_RunV2REST_Job_RunEtagOptimisticConcurrency drives the etag the
// Cloud Run v2 Job resource reports and the RunJobRequest carries. A run
// naming the etag the client last read proceeds; one naming an etag the job
// has moved past is a modification conflict, which Cloud Run answers ABORTED.
func TestSDK_RunV2REST_Job_RunEtagOptimisticConcurrency(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "etag-job"
	name := crV2Parent + "/jobs/" + id

	body := func(image string) *runv2.GoogleCloudRunV2Job {
		return &runv2.GoogleCloudRunV2Job{
			Template: &runv2.GoogleCloudRunV2ExecutionTemplate{
				Template: &runv2.GoogleCloudRunV2TaskTemplate{
					Containers: []*runv2.GoogleCloudRunV2Container{{Image: image}},
				},
			},
		}
	}

	op, err := svc.Projects.Locations.Jobs.Create(crV2Parent, body("alpine:latest")).JobId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		if delOp, err := svc.Projects.Locations.Jobs.Delete(name).Do(); err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	created, err := svc.Projects.Locations.Jobs.Get(name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, created.Etag, "the Job reports the fingerprint a client sends back")

	// The etag the client just read runs the job.
	runOp, err := svc.Projects.Locations.Jobs.Run(name, &runv2.GoogleCloudRunV2RunJobRequest{
		Etag: created.Etag,
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, runOp.Name)

	// Running the job moved it on, so the etag the client still holds is stale.
	moved, err := svc.Projects.Locations.Jobs.Get(name).Do()
	require.NoError(t, err)
	require.NotEqual(t, created.Etag, moved.Etag, "a write mints a new fingerprint")

	_, err = svc.Projects.Locations.Jobs.Run(name, &runv2.GoogleCloudRunV2RunJobRequest{
		Etag: created.Etag,
	}).Do()
	require.Error(t, err, "a stale etag must not silently run the job")
	assert.Contains(t, err.Error(), "409")

	// An update naming the stale etag is refused the same way, and the same
	// update naming the current one succeeds.
	stale := body("alpine:3.20")
	stale.Etag = created.Etag
	_, err = svc.Projects.Locations.Jobs.Patch(name, stale).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")

	current := body("alpine:3.20")
	current.Etag = moved.Etag
	patchOp, err := svc.Projects.Locations.Jobs.Patch(name, current).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, patchOp)

	// A request that names no etag is unconditional, which is how every
	// client that does not track the fingerprint keeps working.
	unconditional, err := svc.Projects.Locations.Jobs.Run(name, &runv2.GoogleCloudRunV2RunJobRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, unconditional.Name)
}

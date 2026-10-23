package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	run "google.golang.org/api/run/v1"
)

// A Knative write deploys a revision under the rules a v2 write does: the
// template names it or the service names it for the generation, a write that
// leaves the template alone deploys none, and a template naming a revision
// the service holds is refused.
func TestCloudRunV1_ReplaceDeploysARevisionOnlyForAChangedTemplate(t *testing.T) {
	t.Parallel()
	svc := newRunV1(t)
	const namespace = "test-project-knative-revision-rules"
	parent := "namespaces/" + namespace
	name := "rules-svc"
	created, err := svc.Namespaces.Services.Create(parent, knativeServiceBody(name, nil)).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Namespaces.Services.Delete(parent + "/services/" + name).Do() })
	first := created.Status.LatestCreatedRevisionName
	assert.Regexp(t, `^rules-svc-00001-[a-z0-9]{3}$`, first)

	relabelled := knativeServiceBody(name, map[string]string{"team": "web"})
	relabelled.Metadata.ResourceVersion = created.Metadata.ResourceVersion
	same, err := svc.Namespaces.Services.ReplaceService(parent+"/services/"+name, relabelled).Do()
	require.NoError(t, err)
	assert.Equal(t, first, same.Status.LatestCreatedRevisionName, "a write that leaves the template alone deploys no revision")
	assert.Equal(t, int64(2), same.Metadata.Generation)

	named := knativeServiceBody(name, nil)
	named.Spec.Template.Metadata = &run.ObjectMeta{Name: name + "-blue"}
	named.Spec.Template.Spec.Containers[0].Image = "gcr.io/x/rules-svc-blue"
	blue, err := svc.Namespaces.Services.ReplaceService(parent+"/services/"+name, named).Do()
	require.NoError(t, err)
	assert.Equal(t, name+"-blue", blue.Status.LatestCreatedRevisionName, "the template names its revision")
	assert.Equal(t, name+"-blue", blue.Spec.Template.Metadata.Name)

	revisions, err := svc.Namespaces.Revisions.List(parent).Do()
	require.NoError(t, err)
	var names []string
	for _, rev := range revisions.Items {
		names = append(names, rev.Metadata.Name)
	}
	assert.ElementsMatch(t, []string{first, name + "-blue"}, names)

	named.Spec.Template.Spec.Containers[0].Image = "gcr.io/x/rules-svc-green"
	_, err = svc.Namespaces.Services.ReplaceService(parent+"/services/"+name, named).Do()
	require.Error(t, err, "a changed template cannot reuse a revision name the service holds")
}

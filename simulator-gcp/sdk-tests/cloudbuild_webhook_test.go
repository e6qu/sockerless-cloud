package gcp_sdk_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cloudbuild "google.golang.org/api/cloudbuild/v1"
	secretmanager "google.golang.org/api/secretmanager/v1"
)

// TestCloudBuild_TriggerWebhookStartsTheBuild covers the webhook a trigger
// exposes: the caller presents the secret the trigger declares, and the
// trigger's build starts. The response carries no members, so the build itself
// is the only evidence the delivery did anything — which is why the listing
// below is the assertion rather than the status code.
func TestCloudBuild_TriggerWebhookStartsTheBuild(t *testing.T) {
	svc := cloudbuildService(t)
	secrets := secretManagerService(t)
	const project = "cb-webhook"
	parent := "projects/" + project + "/locations/us-central1"

	secretParent := "projects/" + project
	_, err := secrets.Projects.Secrets.Create(secretParent, &secretmanager.Secret{}).
		SecretId("webhook-secret").Do()
	require.NoError(t, err)
	version, err := secrets.Projects.Secrets.AddVersion(secretParent+"/secrets/webhook-secret",
		&secretmanager.AddSecretVersionRequest{
			Payload: &secretmanager.SecretPayload{
				Data: base64.StdEncoding.EncodeToString([]byte("s3cr3t")),
			},
		}).Do()
	require.NoError(t, err)

	trigger, err := svc.Projects.Locations.Triggers.Create(parent, &cloudbuild.BuildTrigger{
		Name:          "webhook-trigger",
		WebhookConfig: &cloudbuild.WebhookConfig{Secret: version.Name},
		Build: &cloudbuild.Build{
			Steps: []*cloudbuild.BuildStep{{Name: "alpine", Args: []string{"true"}}},
		},
	}).Do()
	require.NoError(t, err)
	before := len(buildsForTrigger(t, svc, project, trigger.Id))

	// The wrong secret is refused, and nothing starts.
	_, err = svc.Projects.Locations.Triggers.Webhook(parent+"/triggers/"+trigger.Id,
		&cloudbuild.HttpBody{}).Secret("wrong").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
	assert.Len(t, buildsForTrigger(t, svc, project, trigger.Id), before,
		"a refused delivery starts no build")

	_, err = svc.Projects.Locations.Triggers.Webhook(parent+"/triggers/"+trigger.Id,
		&cloudbuild.HttpBody{}).Secret("s3cr3t").Do()
	require.NoError(t, err)
	after := buildsForTrigger(t, svc, project, trigger.Id)
	require.Len(t, after, before+1, "the delivery started the trigger's build")
}

// TestCloudBuild_SharedWebhookStartsTheTriggersWatchingTheRepository covers the
// receiver a source host posts to: the delivery is authenticated by the host's
// webhook key, and the triggers watching the repository it names build.
func TestCloudBuild_SharedWebhookStartsTheTriggersWatchingTheRepository(t *testing.T) {
	svc := cloudbuildService(t)
	const project = "cb-shared-webhook"
	parent := "projects/" + project + "/locations/us-central1"

	// A source host has to be configured for its key to be one Cloud Build
	// knows, so this is the host whose deliveries the receiver accepts.
	_, err := svc.Projects.Locations.GithubEnterpriseConfigs.Create(parent,
		&cloudbuild.GitHubEnterpriseConfig{
			HostUrl:    "https://github.example.com",
			WebhookKey: "host-webhook-key",
		}).GheConfigId("ghe").Do()
	require.NoError(t, err)

	watching, err := svc.Projects.Locations.Triggers.Create(parent, &cloudbuild.BuildTrigger{
		Name: "watching-main",
		Github: &cloudbuild.GitHubEventsConfig{
			Owner: "acme", Name: "widgets",
			Push: &cloudbuild.PushFilter{Branch: "^main$"},
		},
		Build: &cloudbuild.Build{
			Steps: []*cloudbuild.BuildStep{{Name: "alpine", Args: []string{"true"}}},
		},
	}).Do()
	require.NoError(t, err)

	elsewhere, err := svc.Projects.Locations.Triggers.Create(parent, &cloudbuild.BuildTrigger{
		Name: "watching-another-repo",
		Github: &cloudbuild.GitHubEventsConfig{
			Owner: "acme", Name: "gadgets",
		},
		Build: &cloudbuild.Build{
			Steps: []*cloudbuild.BuildStep{{Name: "alpine", Args: []string{"true"}}},
		},
	}).Do()
	require.NoError(t, err)

	delivery := `{"ref":"refs/heads/main","after":"cafe1234",` +
		`"repository":{"name":"widgets","owner":{"login":"acme"}}}`

	// A key no configured host was issued is not from a host Cloud Build knows.
	require.Equal(t, http.StatusUnauthorized,
		postWebhook(t, "/v1/webhook?webhookKey=not-a-key", delivery))
	assert.Empty(t, buildsForTrigger(t, svc, project, watching.Id))

	require.Equal(t, http.StatusOK,
		postWebhook(t, "/v1/webhook?webhookKey=host-webhook-key", delivery))
	started := buildsForTrigger(t, svc, project, watching.Id)
	require.Len(t, started, 1, "the trigger watching this repository built")
	assert.Equal(t, "cafe1234", started[0].Substitutions["COMMIT_SHA"],
		"the build carries the revision the delivery named")
	assert.Equal(t, "main", started[0].Substitutions["BRANCH_NAME"])
	assert.Empty(t, buildsForTrigger(t, svc, project, elsewhere.Id),
		"a trigger watching another repository did not build")

	// A ref the trigger's push filter does not admit is not its event.
	require.Equal(t, http.StatusOK, postWebhook(t, "/v1/webhook?webhookKey=host-webhook-key",
		`{"ref":"refs/heads/topic","after":"beef5678",`+
			`"repository":{"name":"widgets","owner":{"login":"acme"}}}`))
	assert.Len(t, buildsForTrigger(t, svc, project, watching.Id), 1,
		"the push filter kept a branch it does not match from building")

	// A delivery naming no repository is for no trigger, and says so.
	require.Equal(t, http.StatusBadRequest,
		postWebhook(t, "/v1/webhook?webhookKey=host-webhook-key", `{"ref":"refs/heads/main"}`))

	// The other two receivers are the same delivery on the path their host was
	// given: "POST /v1/githubDotComWebhook:receive" for a github.com app, and
	// "POST /v1/locations/{location}/regionalWebhook" for a regional host. Each
	// starts the trigger the same way, which is what makes them the same
	// receiver rather than three that happen to answer alike.
	require.Equal(t, http.StatusOK,
		postWebhook(t, "/v1/githubDotComWebhook:receive?webhookKey=host-webhook-key", delivery))
	assert.Len(t, buildsForTrigger(t, svc, project, watching.Id), 2,
		"the github.com receiver started the trigger too")

	require.Equal(t, http.StatusOK, postWebhook(t,
		"/v1/locations/us-central1/regionalWebhook?webhookKey=host-webhook-key", delivery))
	assert.Len(t, buildsForTrigger(t, svc, project, watching.Id), 3,
		"the regional receiver started the trigger too")

	// And both refuse a key no configured host was issued.
	require.Equal(t, http.StatusUnauthorized,
		postWebhook(t, "/v1/githubDotComWebhook:receive?webhookKey=not-a-key", delivery))
	require.Equal(t, http.StatusUnauthorized, postWebhook(t,
		"/v1/locations/us-central1/regionalWebhook?webhookKey=not-a-key", delivery))
	assert.Len(t, buildsForTrigger(t, svc, project, watching.Id), 3,
		"a refused delivery starts nothing on any receiver")
}

// postWebhook posts a delivery to one of the shared receivers and returns the
// status. The receivers take an opaque HttpBody, so the generated client would
// wrap the delivery rather than send it — a source host posts its own event.
func postWebhook(t *testing.T, path, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

// buildsForTrigger lists the project's builds that a trigger started.
func buildsForTrigger(t *testing.T, svc *cloudbuild.Service, project, triggerID string) []*cloudbuild.Build {
	t.Helper()
	listed, err := svc.Projects.Builds.List(project).Do()
	require.NoError(t, err)
	var out []*cloudbuild.Build
	for _, build := range listed.Builds {
		if build.BuildTriggerId == triggerID {
			out = append(out, build)
		}
	}
	return out
}

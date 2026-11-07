package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type acrDelivery struct {
	authorization string
	contentType   string
	body          map[string]any
}

// acrPushImage pushes a one-layer image monolithically and returns its
// manifest.
func acrPushImage(t *testing.T, f acrRegistryFixture, repo, tag, content string) []byte {
	t.Helper()
	auth := f.basic(f.passwords[0])
	layer := []byte(content)
	digest := acrDigest(layer)
	resp := acrDo(t, http.MethodPost, f.endpoint()+"/v2/"+repo+"/blobs/uploads/", nil, "", auth)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	resp = acrDo(t, http.MethodPut, f.endpoint()+loc+"?digest="+digest, layer, "application/octet-stream", auth)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"%s","size":%d},"layers":[{"digest":"%s","size":%d}]}`, digest, len(layer), digest, len(layer)))
	resp = acrDo(t, http.MethodPut, f.endpoint()+"/v2/"+repo+"/manifests/"+tag, manifest, "application/vnd.docker.distribution.manifest.v2+json", auth)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	return manifest
}

// A webhook POSTs each push its actions and scope select, with its custom
// headers, and records the exchange with Authorization masked; ping sends a
// ping event. importImage copies an image from another registry under the
// target tag, and the usages count the stored bytes and webhooks against the
// SKU's limits.
func TestACR_WebhooksImportAndUsages(t *testing.T) {
	t.Parallel()
	source := newACRRegistryFixture(t, "acr-import-rg-6b21", "acrimportsource6b21", &armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})
	target := newACRRegistryFixture(t, "acr-import-rg-6b21", "acrimporttarget6b21", &armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})

	deliveries := make(chan acrDelivery, 8)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		deliveries <- acrDelivery{authorization: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), body: body}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(listener.Close)
	next := func() acrDelivery {
		select {
		case d := <-deliveries:
			return d
		case <-time.After(30 * time.Second):
			t.Fatal("the webhook delivered nothing")
			return acrDelivery{}
		}
	}

	webhooks, err := armcontainerregistry.NewWebhooksClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	whPoller, err := webhooks.BeginCreate(ctx, target.resourceGroup, target.name, "onpush", armcontainerregistry.WebhookCreateParameters{
		Location: to.Ptr("eastus"),
		Properties: &armcontainerregistry.WebhookPropertiesCreateParameters{
			ServiceURI:    to.Ptr(listener.URL),
			Actions:       []*armcontainerregistry.WebhookAction{to.Ptr(armcontainerregistry.WebhookActionPush)},
			Scope:         to.Ptr("app:*"),
			CustomHeaders: map[string]*string{"Authorization": to.Ptr("Bearer hook-secret")},
		},
	}, nil)
	require.NoError(t, err)
	_, err = whPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	acrPushImage(t, target, "other", "v1", "out of scope")
	manifest := acrPushImage(t, target, "app", "v1", "in scope")
	push := next()
	assert.Equal(t, "Bearer hook-secret", push.authorization, "the custom header reaches the endpoint")
	assert.Equal(t, "application/json", push.contentType)
	assert.Equal(t, "push", push.body["action"])
	pushTarget, _ := push.body["target"].(map[string]any)
	assert.Equal(t, "app", pushTarget["repository"], "the scope app:* leaves the push to other out")
	assert.Equal(t, "v1", pushTarget["tag"])
	assert.Equal(t, acrDigest(manifest), pushTarget["digest"])

	pinged, err := webhooks.Ping(ctx, target.resourceGroup, target.name, "onpush", nil)
	require.NoError(t, err)
	ping := next()
	assert.Equal(t, "ping", ping.body["action"])
	assert.Equal(t, *pinged.ID, ping.body["id"])

	var actions []string
	pager := webhooks.NewListEventsPager(target.resourceGroup, target.name, "onpush", nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, e := range page.Value {
			actions = append(actions, *e.EventRequestMessage.Content.Action)
			assert.Equal(t, "*****", *e.EventRequestMessage.Headers["Authorization"], "the history masks Authorization")
			assert.Equal(t, "200", *e.EventResponseMessage.StatusCode)
		}
	}
	assert.Equal(t, []string{"ping", "push"}, actions, "the history lists the newest event first")

	registries, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	sourceManifest := acrPushImage(t, source, "base", "v7", "imported layer")
	sourceReg, err := registries.Get(ctx, source.resourceGroup, source.name, nil)
	require.NoError(t, err)
	importPoller, err := registries.BeginImportImage(ctx, target.resourceGroup, target.name, armcontainerregistry.ImportImageParameters{
		Source:     &armcontainerregistry.ImportSource{ResourceID: sourceReg.ID, SourceImage: to.Ptr("base:v7")},
		TargetTags: []*string{to.Ptr("mirrored/base:stable")},
	}, nil)
	require.NoError(t, err)
	_, err = importPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	resp := acrDo(t, http.MethodGet, target.endpoint()+"/v2/mirrored/base/manifests/stable", nil, "", target.basic(target.passwords[0]))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	imported, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, sourceManifest, imported, "the import copies the manifest byte for byte")

	_, err = registries.BeginImportImage(ctx, target.resourceGroup, target.name, armcontainerregistry.ImportImageParameters{
		Source:     &armcontainerregistry.ImportSource{ResourceID: sourceReg.ID, SourceImage: to.Ptr("base:missing")},
		TargetTags: []*string{to.Ptr("mirrored/base:other")},
	}, nil)
	require.Error(t, err, "an image the source does not hold cannot be imported")

	usages, err := registries.ListUsages(ctx, target.resourceGroup, target.name, nil)
	require.NoError(t, err)
	got := map[string][2]int64{}
	for _, u := range usages.Value {
		got[*u.Name] = [2]int64{*u.CurrentValue, *u.Limit}
	}
	assert.Equal(t, [2]int64{1, 10}, got["Webhooks"], "a Standard registry allows 10 webhooks")
	assert.Equal(t, int64(100<<30), got["Size"][1], "a Standard registry includes 100 GiB")
	assert.Positive(t, got["Size"][0], "the stored layers and manifests count")
}

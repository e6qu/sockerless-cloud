package gcp_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	storageapi "google.golang.org/api/storage/v1"
)

// objectContextsAPI calls objects.viewFullContext, which neither
// cloud.google.com/go/storage nor google.golang.org/api/storage/v1 wraps yet,
// over the JSON API with the client's own credentials.
type objectContextsAPI struct{ client *http.Client }

type objectFullContext struct {
	Kind       string `json:"kind"`
	Type       string `json:"type"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	CreateTime string `json:"createTime"`
	UpdateTime string `json:"updateTime"`
}

func (a objectContextsAPI) ViewFullContext(bucket, object, contextKey string) (objectFullContext, int, error) {
	u := fmt.Sprintf("%s/storage/v1/b/%s/o/%s/viewFullContext?contextKey=%s",
		baseURL, url.PathEscape(bucket), url.PathEscape(object), url.QueryEscape(contextKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return objectFullContext{}, 0, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return objectFullContext{}, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return objectFullContext{}, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return objectFullContext{}, resp.StatusCode, nil
	}
	var out objectFullContext
	return out, resp.StatusCode, json.Unmarshal(body, &out)
}

func contextValues(c *storage.ObjectContexts) map[string]string {
	out := map[string]string{}
	if c == nil {
		return out
	}
	for key, payload := range c.Custom {
		out[key] = payload.Value
	}
	return out
}

func writeWithContexts(t *testing.T, obj *storage.ObjectHandle, custom map[string]string) *storage.ObjectAttrs {
	t.Helper()
	w := obj.NewWriter(ctx)
	if custom != nil {
		w.Contexts = &storage.ObjectContexts{Custom: map[string]storage.ObjectCustomContextPayload{}}
		for key, value := range custom {
			w.Contexts.Custom[key] = storage.ObjectCustomContextPayload{Value: value}
		}
	}
	_, err := w.Write([]byte("contents"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return w.Attrs()
}

func TestGCS_ObjectContextsWriteUpdateAndView(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucket := uniqueName("contexts-bucket")
	require.NoError(t, client.Bucket(bucket).Create(ctx, "test-project", nil))
	obj := client.Bucket(bucket).Object("dir/report.txt")

	written := writeWithContexts(t, obj, map[string]string{
		"basekey-unicode-å": "baseval-unicode-é",
		"keyToModify":       "oldValue",
		"keyToRemove":       "valueToRemove",
	})
	assert.Equal(t, map[string]string{
		"basekey-unicode-å": "baseval-unicode-é",
		"keyToModify":       "oldValue",
		"keyToRemove":       "valueToRemove",
	}, contextValues(written.Contexts))
	created := written.Contexts.Custom["keyToModify"].CreateTime
	assert.Equal(t, written.Created, created)

	updated, err := obj.Update(ctx, storage.ObjectAttrsToUpdate{Contexts: &storage.ObjectContexts{
		Custom: map[string]storage.ObjectCustomContextPayload{
			"keyToModify": {Value: "newValue"},
			"newKey":      {Value: "newValue"},
			"keyToRemove": {Delete: true},
		},
	}})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"basekey-unicode-å": "baseval-unicode-é",
		"keyToModify":       "newValue",
		"newKey":            "newValue",
	}, contextValues(updated.Contexts))
	assert.Equal(t, created, updated.Contexts.Custom["keyToModify"].CreateTime)
	assert.Equal(t, updated.Updated, updated.Contexts.Custom["keyToModify"].UpdateTime)

	api := objectContextsAPI{client: simAuthHTTPClient()}
	full, status, err := api.ViewFullContext(bucket, "dir/report.txt", "keyToModify")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "storage#objectFullContext", full.Kind)
	assert.Equal(t, "CUSTOM", full.Type)
	assert.Equal(t, "keyToModify", full.Key)
	assert.Equal(t, "newValue", full.Value)
	_, status, err = api.ViewFullContext(bucket, "dir/report.txt", "keyToRemove")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, status)

	cleared, err := obj.Update(ctx, storage.ObjectAttrsToUpdate{Contexts: &storage.ObjectContexts{
		Custom: map[string]storage.ObjectCustomContextPayload{},
	}})
	require.NoError(t, err)
	assert.Empty(t, contextValues(cleared.Contexts))
}

func TestGCS_ObjectContextsCopyAndCompose(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucket := uniqueName("contexts-copy-bucket")
	b := client.Bucket(bucket)
	require.NoError(t, b.Create(ctx, "test-project", nil))
	writeWithContexts(t, b.Object("src-0"), map[string]string{"key_0": "val_0"})
	writeWithContexts(t, b.Object("src-1"), map[string]string{"key_1": "val_1"})

	inherited, err := b.Object("copy-inherited").CopierFrom(b.Object("src-0")).Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"key_0": "val_0"}, contextValues(inherited.Contexts))

	copier := b.Object("copy-overridden").CopierFrom(b.Object("src-0"))
	copier.Contexts = &storage.ObjectContexts{Custom: map[string]storage.ObjectCustomContextPayload{"newKey": {Value: "newValue"}}}
	overridden, err := copier.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"newKey": "newValue"}, contextValues(overridden.Contexts))

	composed, err := b.Object("composed").ComposerFrom(b.Object("src-0"), b.Object("src-1")).Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"key_0": "val_0", "key_1": "val_1"}, contextValues(composed.Contexts))

	svc := storageService(t)
	dropped, err := svc.Objects.Rewrite(bucket, "src-0", bucket, "rewrite-dropped", &storageapi.Object{}).
		DropContextGroups("custom").Do()
	require.NoError(t, err)
	assert.Nil(t, dropped.Resource.Contexts)
	composedDropped, err := svc.Objects.Compose(bucket, "composed-dropped", &storageapi.ComposeRequest{
		SourceObjects: []*storageapi.ComposeRequestSourceObjects{{Name: "src-0"}, {Name: "src-1"}},
	}).DropContextGroups("custom").Do()
	require.NoError(t, err)
	assert.Nil(t, composedDropped.Contexts)
}

func TestGCS_ListObjectsFilteredByContexts(t *testing.T) {
	client := storageClient(t)
	defer client.Close()
	bucket := uniqueName("contexts-list-bucket")
	b := client.Bucket(bucket)
	require.NoError(t, b.Create(ctx, "test-project", nil))
	writeWithContexts(t, b.Object("ctx1"), map[string]string{"keyA": "valueA", "keyB": "valueB", "key-unicode-á": "value-unicode-é"})
	writeWithContexts(t, b.Object("ctx2"), map[string]string{"keyA": "valueX", "keyC": "valueC"})
	writeWithContexts(t, b.Object("no-ctx"), nil)

	for filter, want := range map[string][]string{
		`contexts."keyA"="valueA"`:                   {"ctx1"},
		`-contexts."keyB"="valueB"`:                  {"ctx2", "no-ctx"},
		`contexts."keyA":*`:                          {"ctx1", "ctx2"},
		`-contexts."keyD":*`:                         {"ctx1", "ctx2", "no-ctx"},
		`contexts."key-unicode-á"="value-unicode-é"`: {"ctx1"},
		``: {"ctx1", "ctx2", "no-ctx"},
	} {
		it := b.Objects(ctx, &storage.Query{Filter: filter})
		var got []string
		for {
			attrs, err := it.Next()
			if err == iterator.Done {
				break
			}
			require.NoError(t, err, filter)
			got = append(got, attrs.Name)
		}
		sort.Strings(got)
		assert.Equal(t, want, got, filter)
	}
}

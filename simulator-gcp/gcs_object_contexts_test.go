package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"
)

func (c *gcsTestClient) sendJSON(method, path, body string) (int, map[string]any) {
	c.t.Helper()
	resp, out := c.do(method, path, map[string]string{"Content-Type": "application/json"}, []byte(body))
	var doc map[string]any
	if len(out) > 0 {
		if err := json.Unmarshal(out, &doc); err != nil {
			c.t.Fatalf("%s %s: %d %s: %v", method, path, resp.StatusCode, out, err)
		}
	}
	return resp.StatusCode, doc
}

func (c *gcsTestClient) mustJSON(method, path, body string) map[string]any {
	c.t.Helper()
	status, doc := c.sendJSON(method, path, body)
	if status != http.StatusOK {
		c.t.Fatalf("%s %s: %d %v", method, path, status, doc)
	}
	return doc
}

func gcsCustomContexts(t *testing.T, obj map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	contexts, ok := obj["contexts"].(map[string]any)
	if !ok {
		return out
	}
	custom, _ := contexts["custom"].(map[string]any)
	for key, payload := range custom {
		out[key] = payload.(map[string]any)
	}
	return out
}

func gcsContextValues(t *testing.T, obj map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for key, payload := range gcsCustomContexts(t, obj) {
		out[key], _ = payload["value"].(string)
	}
	return out
}

func assertContextValues(t *testing.T, obj map[string]any, want map[string]string) {
	t.Helper()
	got := gcsContextValues(t, obj)
	if len(got) != len(want) {
		t.Fatalf("contexts = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("contexts = %v, want %v", got, want)
		}
	}
}

// objects.insert stores the custom contexts the resource carries, stamped with
// the object's creation time, and every object resource renders them.
func TestGCSObjectContextsInsertAndRender(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("ctx-insert", "")
	created := c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-insert/o",
		`{"name":"a.txt","contexts":{"custom":{"team":{"value":"storage"},"tier":{"value":"gold"}}}}`)
	assertContextValues(t, created, map[string]string{"team": "storage", "tier": "gold"})
	for key, payload := range gcsCustomContexts(t, created) {
		if payload["createTime"] != created["timeCreated"] || payload["updateTime"] != created["timeCreated"] {
			t.Fatalf("context %q times %v, want the object's timeCreated %v", key, payload, created["timeCreated"])
		}
	}
	got := c.objectJSON(http.MethodGet, "/storage/v1/b/ctx-insert/o/a.txt")
	assertContextValues(t, got, map[string]string{"team": "storage", "tier": "gold"})

	plain := c.upload("ctx-insert", "plain.txt", "x")
	if _, ok := plain["contexts"]; ok {
		t.Fatalf("an object without contexts renders %v", plain["contexts"])
	}
}

// objects.patch merges keys, removes a key sent as null or without a value,
// and clears them all for "contexts": null; objects.update replaces the set.
func TestGCSObjectContextsPatchAndUpdate(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("ctx-patch", "")
	created := c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-patch/o",
		`{"name":"a.txt","contexts":{"custom":{"keep":{"value":"1"},"change":{"value":"old"},"gone":{"value":"x"},"alsoGone":{"value":"y"}}}}`)
	createdAt := gcsCustomContexts(t, created)["change"]["createTime"]

	patched := c.mustJSON(http.MethodPatch, "/storage/v1/b/ctx-patch/o/a.txt",
		`{"contexts":{"custom":{"change":{"value":"new"},"added":{"value":"2"},"gone":null,"alsoGone":{}}}}`)
	assertContextValues(t, patched, map[string]string{"keep": "1", "change": "new", "added": "2"})
	change := gcsCustomContexts(t, patched)["change"]
	if change["createTime"] != createdAt || change["updateTime"] != patched["updated"] {
		t.Fatalf("patched context times %v, want createTime %v and updateTime %v", change, createdAt, patched["updated"])
	}
	if keep := gcsCustomContexts(t, patched)["keep"]; keep["updateTime"] != createdAt {
		t.Fatalf("an untouched context's updateTime moved to %v", keep["updateTime"])
	}

	untouched := c.mustJSON(http.MethodPatch, "/storage/v1/b/ctx-patch/o/a.txt", `{"contentType":"text/plain"}`)
	assertContextValues(t, untouched, map[string]string{"keep": "1", "change": "new", "added": "2"})

	updated := c.mustJSON(http.MethodPut, "/storage/v1/b/ctx-patch/o/a.txt",
		`{"contexts":{"custom":{"keep":{"value":"1"},"only":{"value":"3"}}}}`)
	assertContextValues(t, updated, map[string]string{"keep": "1", "only": "3"})
	if keep := gcsCustomContexts(t, updated)["keep"]; keep["createTime"] != createdAt {
		t.Fatalf("a key update kept lost its createTime: %v", keep)
	}

	cleared := c.mustJSON(http.MethodPatch, "/storage/v1/b/ctx-patch/o/a.txt", `{"contexts":null}`)
	if _, ok := cleared["contexts"]; ok {
		t.Fatalf(`"contexts": null left %v`, cleared["contexts"])
	}

	c.mustJSON(http.MethodPatch, "/storage/v1/b/ctx-patch/o/a.txt", `{"contexts":{"custom":{"k":{"value":"v"}}}}`)
	replaced := c.mustJSON(http.MethodPut, "/storage/v1/b/ctx-patch/o/a.txt", `{"contentType":"text/plain"}`)
	if _, ok := replaced["contexts"]; ok {
		t.Fatalf("objects.update without contexts kept %v", replaced["contexts"])
	}
}

// objects.viewFullContext answers one context as an ObjectFullContext, and
// 404s a missing object, generation or key.
func TestGCSObjectViewFullContext(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("ctx-view", "")
	created := c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-view/o",
		`{"name":"dir/a.txt","contexts":{"custom":{"team":{"value":"storage"}}}}`)
	full := c.objectJSON(http.MethodGet, "/storage/v1/b/ctx-view/o/dir%2Fa.txt/viewFullContext?contextKey=team")
	want := map[string]any{
		"kind": "storage#objectFullContext", "type": "CUSTOM", "key": "team", "value": "storage",
		"createTime": created["timeCreated"], "updateTime": created["timeCreated"],
	}
	if len(full) != len(want) {
		t.Fatalf("viewFullContext = %v, want %v", full, want)
	}
	for k, v := range want {
		if full[k] != v {
			t.Fatalf("viewFullContext = %v, want %v", full, want)
		}
	}
	c.objectJSON(http.MethodGet, "/storage/v1/b/ctx-view/o/dir%2Fa.txt/viewFullContext?contextKey=team&generation="+created["generation"].(string))

	for path, wantStatus := range map[string]int{
		"/storage/v1/b/ctx-view/o/dir%2Fa.txt/viewFullContext?contextKey=absent":            http.StatusNotFound,
		"/storage/v1/b/ctx-view/o/missing.txt/viewFullContext?contextKey=team":              http.StatusNotFound,
		"/storage/v1/b/ctx-view/o/dir%2Fa.txt/viewFullContext?contextKey=team&generation=1": http.StatusNotFound,
		"/storage/v1/b/ctx-view/o/dir%2Fa.txt/viewFullContext":                              http.StatusBadRequest,
	} {
		status, doc := c.sendJSON(http.MethodGet, path, "")
		if status != wantStatus {
			t.Fatalf("GET %s = %d %v, want %d", path, status, doc, wantStatus)
		}
		errBody, _ := doc["error"].(map[string]any)
		if code, _ := errBody["code"].(float64); int(code) != wantStatus {
			t.Fatalf("GET %s error envelope %v", path, doc)
		}
	}
}

// objects.list's filter selects objects by their contexts and leaves the
// common prefixes alone.
func TestGCSObjectListFilterByContexts(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("ctx-list", "")
	c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-list/o",
		`{"name":"one","contexts":{"custom":{"keyA":{"value":"valueA"},"keyB":{"value":"valueB"},"key-unicode-á":{"value":"value-unicode-é"}}}}`)
	c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-list/o",
		`{"name":"two","contexts":{"custom":{"keyA":{"value":"valueX"},"keyC":{"value":"valueC"},"a b:(c)=\"d":{"value":"x y"}}}}`)
	c.upload("ctx-list", "three", "x")
	c.upload("ctx-list", "dir/four", "x")

	names := func(filter string, delimiter string) ([]string, []any) {
		t.Helper()
		listing := c.objectJSON(http.MethodGet, "/storage/v1/b/ctx-list/o?filter="+url.QueryEscape(filter)+"&delimiter="+delimiter)
		var out []string
		items, _ := listing["items"].([]any)
		for _, item := range items {
			out = append(out, item.(map[string]any)["name"].(string))
		}
		slices.Sort(out)
		prefixes, _ := listing["prefixes"].([]any)
		return out, prefixes
	}
	for filter, want := range map[string][]string{
		`contexts."keyA"="valueA"`:                    {"one"},
		`-contexts."keyB"="valueB"`:                   {"dir/four", "three", "two"},
		`contexts."keyA":*`:                           {"one", "two"},
		`-contexts."keyD":*`:                          {"dir/four", "one", "three", "two"},
		`contexts."key-unicode-á"="value-unicode-é"`:  {"one"},
		`contexts."keyA":* AND NOT contexts."keyC":*`: {"one"},
		`contexts."keyB":* OR contexts."keyC":*`:      {"one", "two"},
		`contexts."a b:(c)=\"d"="x y"`:                {"two"},
	} {
		if got, _ := names(filter, ""); !slices.Equal(got, want) {
			t.Fatalf("filter %s listed %v, want %v", filter, got, want)
		}
	}
	got, prefixes := names(`contexts."keyA":*`, "/")
	if !slices.Equal(got, []string{"one", "two"}) || len(prefixes) != 1 || prefixes[0] != "dir/" {
		t.Fatalf("filter with delimiter listed %v and prefixes %v", got, prefixes)
	}

	for _, bad := range []string{`name="one"`, `contexts."keyA">"a"`, `contexts."keyA"=`} {
		if status, doc := c.sendJSON(http.MethodGet, "/storage/v1/b/ctx-list/o?filter="+url.QueryEscape(bad), ""); status != http.StatusBadRequest {
			t.Fatalf("filter %s = %d %v, want 400", bad, status, doc)
		}
	}
}

// A copy, rewrite or compose carries the sources' contexts to the destination
// unless the request body brings its own or dropContextGroups=custom drops them.
func TestGCSObjectContextsCopyRewriteCompose(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("ctx-copy", "")
	c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-copy/o", `{"name":"src","contexts":{"custom":{"s":{"value":"1"}}}}`)
	c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-copy/o", `{"name":"src2","contexts":{"custom":{"t":{"value":"2"}}}}`)

	copied := c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-copy/o/src/copyTo/b/ctx-copy/o/copied", `{}`)
	assertContextValues(t, copied, map[string]string{"s": "1"})
	if payload := gcsCustomContexts(t, copied)["s"]; payload["createTime"] != copied["timeCreated"] {
		t.Fatalf("a copied context's createTime %v, want the copy's timeCreated %v", payload["createTime"], copied["timeCreated"])
	}

	overridden := c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-copy/o/src/rewriteTo/b/ctx-copy/o/overridden",
		`{"contexts":{"custom":{"n":{"value":"new"}}}}`)
	assertContextValues(t, overridden["resource"].(map[string]any), map[string]string{"n": "new"})

	dropped := c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-copy/o/src/rewriteTo/b/ctx-copy/o/dropped?dropContextGroups=custom", `{}`)
	assertContextValues(t, dropped["resource"].(map[string]any), map[string]string{})

	if status, doc := c.sendJSON(http.MethodPost, "/storage/v1/b/ctx-copy/o/src/rewriteTo/b/ctx-copy/o/bad?dropContextGroups=system", `{}`); status != http.StatusBadRequest {
		t.Fatalf("dropContextGroups=system = %d %v, want 400", status, doc)
	}

	composed := c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-copy/o/composed/compose",
		`{"sourceObjects":[{"name":"src"},{"name":"src2"}],"destination":{}}`)
	assertContextValues(t, composed, map[string]string{"s": "1", "t": "2"})
	composedOwn := c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-copy/o/composed2/compose",
		`{"sourceObjects":[{"name":"src"},{"name":"src2"}],"destination":{"contexts":{"custom":{"own":{"value":"o"}}}}}`)
	assertContextValues(t, composedOwn, map[string]string{"own": "o"})
	composedDropped := c.mustJSON(http.MethodPost, "/storage/v1/b/ctx-copy/o/composed3/compose?dropContextGroups=custom",
		`{"sourceObjects":[{"name":"src"},{"name":"src2"}]}`)
	assertContextValues(t, composedDropped, map[string]string{})
}

// A resumable upload stores the contexts its session's resource carried.
func TestGCSObjectContextsResumableUpload(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("ctx-resumable", "")
	resp, out := c.do(http.MethodPost, "/upload/storage/v1/b/ctx-resumable/o?uploadType=resumable",
		map[string]string{"Content-Type": "application/json"},
		[]byte(`{"name":"big.bin","contexts":{"custom":{"origin":{"value":"resumable"}}}}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resumable init: %d %s", resp.StatusCode, out)
	}
	session, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	resp, out = c.do(http.MethodPut, session.RequestURI(),
		map[string]string{"Content-Range": "bytes 0-4/5"}, []byte("hello"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resumable chunk: %d %s", resp.StatusCode, out)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	assertContextValues(t, obj, map[string]string{"origin": "resumable"})
}

// objects.update replaces the writable metadata that objects.patch merges.
func TestGCSObjectUpdateReplacesWritableMetadata(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("update-replaces", "")
	c.mustJSON(http.MethodPost, "/storage/v1/b/update-replaces/o", `{"name":"a.txt","contentType":"text/csv"}`)
	c.mustJSON(http.MethodPatch, "/storage/v1/b/update-replaces/o/a.txt",
		`{"cacheControl":"no-cache","contentLanguage":"en","metadata":{"k":"v"},"customTime":"2026-01-01T00:00:00Z"}`)

	merged := c.mustJSON(http.MethodPatch, "/storage/v1/b/update-replaces/o/a.txt", `{"contentDisposition":"inline"}`)
	if merged["cacheControl"] != "no-cache" || merged["contentType"] != "text/csv" {
		t.Fatalf("objects.patch dropped fields it did not name: %v", merged)
	}

	replaced := c.mustJSON(http.MethodPut, "/storage/v1/b/update-replaces/o/a.txt", `{"contentType":"text/plain"}`)
	if replaced["contentType"] != "text/plain" {
		t.Fatalf("contentType %v, want the body's text/plain", replaced["contentType"])
	}
	for _, field := range []string{"cacheControl", "contentLanguage", "contentDisposition", "metadata"} {
		if value, ok := replaced[field]; ok {
			t.Fatalf("objects.update kept %s = %v the body left out", field, value)
		}
	}
	if replaced["customTime"] == nil {
		t.Fatal("objects.update removed customTime, which Cloud Storage never removes")
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func (c *gcsTestClient) objectJSON(method, path string) map[string]any {
	c.t.Helper()
	resp, out := c.do(method, path, map[string]string{"Content-Type": "application/json"}, nil)
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, out)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		c.t.Fatal(err)
	}
	return obj
}

func gcsGenerationOf(t *testing.T, obj map[string]any) int64 {
	t.Helper()
	text, _ := obj["generation"].(string)
	generation, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		t.Fatalf("generation %q of %v: %v", text, obj, err)
	}
	return generation
}

// objects.restore writes the soft-deleted generation's contents back as a new
// live object: the object answers under a generation of its own, newer than
// every generation the name has had, at metageneration 1, and the soft-deleted
// generation it came from is gone from the soft-deleted listing.
func TestGCSRestoreWritesANewGeneration(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("restore-generation", "")
	first := c.upload("restore-generation", "ledger.txt", "first")
	c.remove("restore-generation", "ledger.txt")
	second := c.upload("restore-generation", "ledger.txt", "second")
	c.remove("restore-generation", "ledger.txt")

	restored := c.objectJSON(http.MethodPost,
		fmt.Sprintf("/storage/v1/b/restore-generation/o/ledger.txt/restore?generation=%s", first["generation"]))
	generation := gcsGenerationOf(t, restored)
	if generation <= gcsGenerationOf(t, second) {
		t.Fatalf("restored generation %d is not newer than the name's latest generation %d",
			generation, gcsGenerationOf(t, second))
	}
	if restored["metageneration"] != "1" {
		t.Fatalf("restored metageneration = %v, want 1", restored["metageneration"])
	}
	if restored["md5Hash"] != first["md5Hash"] || restored["size"] != first["size"] {
		t.Fatalf("restored %v does not carry the soft-deleted generation's contents %v", restored, first)
	}
	if restored["etag"] == first["etag"] {
		t.Fatalf("the restored object reuses the soft-deleted generation's etag %v", first["etag"])
	}

	live := c.objectJSON(http.MethodGet, "/storage/v1/b/restore-generation/o/ledger.txt")
	if live["generation"] != restored["generation"] {
		t.Fatalf("objects.get answers generation %v, restore answered %v", live["generation"], restored["generation"])
	}
	if got := c.download("restore-generation", "ledger.txt"); got != "first" {
		t.Fatalf("the restored object serves %q", got)
	}

	listing := c.objectJSON(http.MethodGet, "/storage/v1/b/restore-generation/o?softDeleted=true")
	items, _ := listing["items"].([]any)
	for _, item := range items {
		if item.(map[string]any)["generation"] == first["generation"] {
			t.Fatalf("the restored generation %v is still listed as soft-deleted", first["generation"])
		}
	}
	if len(items) != 1 || items[0].(map[string]any)["generation"] != second["generation"] {
		t.Fatalf("soft-deleted listing = %v, want only generation %v", items, second["generation"])
	}

	resp, out := c.do(http.MethodPost,
		fmt.Sprintf("/storage/v1/b/restore-generation/o/ledger.txt/restore?generation=%s", first["generation"]), nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("restoring the same soft-deleted generation twice answered %d %s, want 404", resp.StatusCode, out)
	}
}

// copySourceAcl keeps the source object's access controls on the restored
// object; without it the restored object takes the bucket's default object ACL.
// Either way the entries name the restored generation.
func TestGCSRestoreCopySourceACL(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("restore-acl", "")
	const grant = "user-auditor@example.com"
	for _, copyACL := range []bool{true, false} {
		name := fmt.Sprintf("copy-%t.txt", copyACL)
		c.upload("restore-acl", name, "payload")
		body, _ := json.Marshal(map[string]string{"entity": grant, "role": "READER"})
		if resp, out := c.do(http.MethodPost, "/storage/v1/b/restore-acl/o/"+name+"/acl",
			map[string]string{"Content-Type": "application/json"}, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("grant: %d %s", resp.StatusCode, out)
		}
		deleted := c.objectJSON(http.MethodGet, "/storage/v1/b/restore-acl/o/"+name)
		c.remove("restore-acl", name)

		restored := c.objectJSON(http.MethodPost, fmt.Sprintf(
			"/storage/v1/b/restore-acl/o/%s/restore?generation=%s&copySourceAcl=%t", name, deleted["generation"], copyACL))
		acl := c.objectJSON(http.MethodGet, "/storage/v1/b/restore-acl/o/"+name+"/acl")
		entries, _ := acl["items"].([]any)
		if len(entries) == 0 {
			t.Fatalf("copySourceAcl=%t: the restored object has no access controls", copyACL)
		}
		granted := false
		for _, raw := range entries {
			entry := raw.(map[string]any)
			if entry["generation"] != restored["generation"] {
				t.Errorf("copySourceAcl=%t: entry %v names generation %v, the object is %v",
					copyACL, entry["entity"], entry["generation"], restored["generation"])
			}
			if entry["entity"] == grant {
				granted = true
			}
		}
		if granted != copyACL {
			t.Fatalf("copySourceAcl=%t: the source's grant to %s survived = %t", copyACL, grant, granted)
		}
	}
}

// objects.restore judges its preconditions against the live object it would
// replace: ifGenerationMatch=0 asks that there be none, a matching generation
// lets the restore replace it, and ifGenerationNotMatch fails when no live
// object exists.
func TestGCSRestorePreconditionsJudgeTheLiveObject(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("restore-preconditions", "")
	first := c.upload("restore-preconditions", "doc.txt", "first")
	c.remove("restore-preconditions", "doc.txt")
	second := c.upload("restore-preconditions", "doc.txt", "second")
	restorePath := func(query string) string {
		return fmt.Sprintf("/storage/v1/b/restore-preconditions/o/doc.txt/restore?generation=%s&%s", first["generation"], query)
	}
	refused := func(query string) {
		t.Helper()
		resp, out := c.do(http.MethodPost, restorePath(query), nil, nil)
		if resp.StatusCode != http.StatusPreconditionFailed || !strings.Contains(string(out), "conditionNotMet") {
			t.Fatalf("restore with %s answered %d %s, want 412 conditionNotMet", query, resp.StatusCode, out)
		}
	}

	refused("ifGenerationMatch=0")
	refused(fmt.Sprintf("ifGenerationNotMatch=%s", second["generation"]))
	if got := c.download("restore-preconditions", "doc.txt"); got != "second" {
		t.Fatalf("a refused restore changed the live object to %q", got)
	}

	restored := c.objectJSON(http.MethodPost, restorePath(fmt.Sprintf("ifGenerationMatch=%s", second["generation"])))
	if gcsGenerationOf(t, restored) <= gcsGenerationOf(t, second) {
		t.Fatalf("restored generation %v is not newer than the live one it replaced %v", restored["generation"], second["generation"])
	}
	if got := c.download("restore-preconditions", "doc.txt"); got != "first" {
		t.Fatalf("the restore that held its precondition serves %q", got)
	}
	listing := c.objectJSON(http.MethodGet, "/storage/v1/b/restore-preconditions/o?softDeleted=true")
	items, _ := listing["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["generation"] != second["generation"] {
		t.Fatalf("soft-deleted listing = %v, want the replaced generation %v", items, second["generation"])
	}

	// With no live object, ifGenerationNotMatch fails and ifGenerationMatch=0 holds.
	c.remove("restore-preconditions", "doc.txt")
	resp, out := c.do(http.MethodPost, fmt.Sprintf(
		"/storage/v1/b/restore-preconditions/o/doc.txt/restore?generation=%s&ifGenerationNotMatch=1", second["generation"]), nil, nil)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("ifGenerationNotMatch with no live object answered %d %s, want 412", resp.StatusCode, out)
	}
	c.objectJSON(http.MethodPost, fmt.Sprintf(
		"/storage/v1/b/restore-preconditions/o/doc.txt/restore?generation=%s&ifGenerationMatch=0", second["generation"]))
	if got := c.download("restore-preconditions", "doc.txt"); got != "second" {
		t.Fatalf("ifGenerationMatch=0 with no live object restored %q", got)
	}
}

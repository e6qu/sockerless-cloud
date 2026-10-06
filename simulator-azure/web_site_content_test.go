package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWebSiteNameOfASlot(t *testing.T) {
	const app = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/app"
	for id, want := range map[string]string{app: "app", app + "/slots/staging": "app/staging"} {
		if got := webSiteNameOf(id); got != want {
			t.Errorf("webSiteNameOf(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSiteContentFunctionConfigsReadsTheWWWRoot(t *testing.T) {
	t.Setenv("SIM_DATA_DIR", t.TempDir())
	site := &Site{ID: "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/fn-app", Name: "fn-app"}
	root := webWWWRootDir(site.ID)
	for name, body := range map[string]string{
		"hello/function.json":  "\xef\xbb\xbf" + `{"bindings":[{"type":"httpTrigger","name":"req"}]}`,
		"broken/function.json": `{"bindings":`,
		"function.json":        `{}`,
		"hello/index.js":       `module.exports = () => {}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := siteContentFunctionConfigs(site)
	if len(got) != 1 || got["hello"] == nil {
		t.Fatalf("functions = %v, want only hello", got)
	}
}

//go:build doctest

package doctest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDocs runs every tutorial document, one subtest per document and part:
// `go test -tags doctest -run 'TestDocs/docs/tutorial.md/aws'`. The only
// coordinate it adds to the reader's environment is where the repository's
// clone URL points: at this checkout's committed HEAD instead of GitHub, so
// the tutorial runs against the change under test. gcloud keeps its
// configuration in a directory of the test's own, as on a fresh machine.
func TestDocs(t *testing.T) {
	root := repoRoot(t)
	env := append(os.Environ(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url."+root+".insteadOf",
		"GIT_CONFIG_VALUE_0=https://github.com/e6qu/sockerless-cloud.git",
		"CLOUDSDK_CONFIG="+t.TempDir(),
	)
	for _, d := range Documents {
		t.Run(d, func(t *testing.T) {
			doc, err := Parse(filepath.Join(root, d))
			if err != nil {
				t.Fatal(err)
			}
			RunDoc(t, doc, env)
		})
	}
}

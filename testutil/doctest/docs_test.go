package doctest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestEveryShellBlockIsAnnotated holds every Markdown document in the
// repository to the annotation rule, so a document gains no command its doc
// test does not run or explicitly skip.
func TestEveryShellBlockIsAnnotated(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "*.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range strings.Fields(string(out)) {
		// Vendored and generated files, and the review checklists agents load,
		// are not documents a reader follows.
		if strings.HasPrefix(f, "specs/cloud-api/") || strings.HasPrefix(f, "specs/SIM_SURFACE_TABLES/") ||
			strings.HasPrefix(f, ".claude/") || f == "CHANGELOG.md" {
			continue
		}
		if _, err := Parse(filepath.Join(root, f)); err != nil {
			t.Error(err)
		}
	}
}

// TestDocsSelfCheck runs nothing external: it proves the documents the doc
// tests run parse, so a typo in an annotation fails the unit tests rather than
// a doc test job.
func TestDocsSelfCheck(t *testing.T) {
	root := repoRoot(t)
	for _, d := range Documents {
		if _, err := Parse(filepath.Join(root, d)); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "docs/tutorial.md")); err != nil {
		t.Fatal(err)
	}
}

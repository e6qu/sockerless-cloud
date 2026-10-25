package githttp

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoServesPushedCommits(t *testing.T) {
	repo := StartRepo(t, "site")
	first := repo.Commit("main", "First", map[string]string{"index.html": "first", "notes.txt": "gone later"})
	repo.Remove("notes.txt")
	second := repo.Commit("main", "Second", map[string]string{"index.html": "second"})
	if first == second {
		t.Fatalf("both commits report ID %s", first)
	}

	clone := filepath.Join(t.TempDir(), "clone")
	if out, err := exec.Command("git", "clone", "--quiet", "--branch", "main", repo.URL, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone %s: %v: %s", repo.URL, err, out)
	}
	head, err := exec.Command("git", "-C", clone, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if got := strings.TrimSpace(string(head)); got != second {
		t.Fatalf("clone HEAD = %s, want %s", got, second)
	}
	files, err := exec.Command("git", "-C", clone, "ls-files").Output()
	if err != nil {
		t.Fatalf("ls-files: %v", err)
	}
	if got := strings.TrimSpace(string(files)); got != "index.html" {
		t.Fatalf("clone files = %q, want index.html alone", got)
	}
}

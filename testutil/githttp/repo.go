package githttp

import (
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a bare repository served over Git's smart HTTP protocol by
// git http-backend, with a working clone the test commits to and pushes from.
type Repo struct {
	t    *testing.T
	bare string
	work string
	URL  string
}

// StartRepo creates an empty repository whose default branch is main and
// serves it at the returned Repo's URL.
func StartRepo(t *testing.T, name string) *Repo {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required to serve the source repository; install git: %v", err)
	}
	root := t.TempDir()
	repo := &Repo{t: t, bare: filepath.Join(root, name+".git"), work: filepath.Join(root, "work")}
	repo.git("", "init", "--quiet", "--bare", "--initial-branch=main", repo.bare)
	repo.git("", "init", "--quiet", "--initial-branch=main", repo.work)
	repo.git(repo.work, "config", "user.name", "Ada Lovelace")
	repo.git(repo.work, "config", "user.email", "ada@example.com")
	server := httptest.NewServer(&cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(server.Close)
	repo.URL = server.URL + "/" + name + ".git"
	return repo
}

// Commit writes files into the working clone, commits them on branch with
// message, pushes the branch, and returns the commit's ID.
func (g *Repo) Commit(branch, message string, files map[string]string) string {
	g.t.Helper()
	g.git(g.work, "checkout", "--quiet", "-B", branch)
	for name, content := range files {
		p := filepath.Join(g.work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			g.t.Fatalf("create source directory for %s: %v", name, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			g.t.Fatalf("write source file %s: %v", name, err)
		}
	}
	g.git(g.work, "add", "--all")
	g.git(g.work, "commit", "--quiet", "--allow-empty", "-m", message)
	g.git(g.work, "push", "--quiet", "--force", g.bare, branch+":"+branch)
	return g.git(g.work, "rev-parse", "HEAD")
}

// Remove deletes files from the working clone's next commit.
func (g *Repo) Remove(names ...string) {
	g.t.Helper()
	for _, name := range names {
		g.git(g.work, "rm", "--quiet", name)
	}
}

func (g *Repo) git(dir string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

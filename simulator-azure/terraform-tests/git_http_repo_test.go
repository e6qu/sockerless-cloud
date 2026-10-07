package azure_tf_test

import (
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// gitHTTPRepo is a Git repository served over the smart HTTP protocol by
// git http-backend, as a Git host serves one, with a working clone the test
// commits to and pushes from.
type gitHTTPRepo struct {
	t    *testing.T
	bare string
	work string
	URL  string
}

// startGitHTTPRepo creates an empty repository whose default branch is main
// and serves it at URL.
func startGitHTTPRepo(t *testing.T, name string) *gitHTTPRepo {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	require.NoError(t, err, "git is required")
	root := t.TempDir()
	repo := &gitHTTPRepo{t: t, bare: filepath.Join(root, name+".git"), work: filepath.Join(root, "work")}
	repo.git("", "init", "--quiet", "--bare", "--initial-branch=main", repo.bare)
	repo.git("", "init", "--quiet", "--initial-branch=main", repo.work)
	repo.git(repo.work, "config", "user.name", "Ada Lovelace")
	repo.git(repo.work, "config", "user.email", "ada@example.com")
	srv := httptest.NewServer(&cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	repo.URL = srv.URL + "/" + name + ".git"
	return repo
}

// commit writes files into the working clone, commits them on branch with
// message, pushes the branch, and returns the commit's ID.
func (g *gitHTTPRepo) commit(branch, message string, files map[string]string) string {
	g.t.Helper()
	g.git(g.work, "checkout", "--quiet", "-B", branch)
	for name, content := range files {
		p := filepath.Join(g.work, filepath.FromSlash(name))
		require.NoError(g.t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(g.t, os.WriteFile(p, []byte(content), 0o644))
	}
	g.git(g.work, "add", "--all")
	g.git(g.work, "commit", "--quiet", "--allow-empty", "-m", message)
	g.git(g.work, "push", "--quiet", "--force", g.bare, branch+":"+branch)
	return g.git(g.work, "rev-parse", "HEAD")
}

func (g *gitHTTPRepo) git(dir string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	require.NoError(g.t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

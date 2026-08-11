// Package githttp serves real Git repositories over Git's smart HTTP protocol
// for cross-surface simulator integration tests.
package githttp

import (
	"crypto/subtle"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Serve creates a single-commit repository and serves it through git
// http-backend. The returned URL is directly clonable by a real Git client.
func Serve(t *testing.T, branch string, files map[string]string) string {
	return serve(t, branch, files, "", "")
}

// ServeBasicAuth creates and serves a repository whose smart HTTP data plane
// requires the supplied username and password on every request.
func ServeBasicAuth(t *testing.T, branch string, files map[string]string, username, password string) string {
	t.Helper()
	return serve(t, branch, files, username, password)
}

func serve(t *testing.T, branch string, files map[string]string, username, password string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required to serve the source repository; install git: %v", err)
	}

	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("create Git work tree: %v", err)
	}
	run := func(directory string, args ...string) {
		t.Helper()
		command := exec.Command(gitPath, args...)
		command.Dir = directory
		output, runErr := command.CombinedOutput()
		if runErr != nil {
			t.Fatalf("git %v: %v: %s", args, runErr, output)
		}
	}

	run(workDir, "init", "-q", "-b", branch)
	for name, content := range files {
		destination := filepath.Join(workDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatalf("create source directory for %s: %v", name, err)
		}
		if err := os.WriteFile(destination, []byte(content), 0o644); err != nil {
			t.Fatalf("write source file %s: %v", name, err)
		}
	}
	run(workDir, "add", "-A")
	run(workDir, "-c", "user.email=test@sockerless.local", "-c", "user.name=sockerless", "commit", "-q", "-m", "initial")
	run(root, "clone", "-q", "--bare", workDir, filepath.Join(root, "repo.git"))

	handler := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	var gitHandler = http.Handler(handler)
	if username != "" || password != "" {
		gitHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUsername, gotPassword, ok := r.BasicAuth()
			usernameOK := subtle.ConstantTimeCompare([]byte(gotUsername), []byte(username)) == 1
			passwordOK := subtle.ConstantTimeCompare([]byte(gotPassword), []byte(password)) == 1
			if !ok || !usernameOK || !passwordOK {
				w.Header().Set("WWW-Authenticate", `Basic realm="Git"`)
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
			handler.ServeHTTP(w, r)
		})
	}
	server := httptest.NewServer(gitHandler)
	t.Cleanup(server.Close)
	return server.URL + "/repo.git"
}

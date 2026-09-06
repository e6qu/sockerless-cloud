package sim

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The helper answers the hosts its patterns name, with or without a port,
// and gives the protocol's not-found answer for any other host.
func TestWriteDockerConfigCredentialHelper(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	dir, err := WriteDockerConfig(DockerConfigSpec{
		HostPatterns: []string{"*.example.test"},
		Hosts:        []string{"registry.local:5000"},
		Credential:   DockerCredential{Username: DockerIdentityTokenUsername, Secret: "refresh-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config["credsStore"] != dockerCredentialHelperName {
		t.Fatalf("credsStore = %v", config["credsStore"])
	}
	helpers, _ := config["credHelpers"].(map[string]any)
	if helpers["registry.local:5000"] != dockerCredentialHelperName {
		t.Fatalf("credHelpers = %v: a named host is a credHelpers entry the legacy builder enumerates", config["credHelpers"])
	}
	helper := filepath.Join(dir, "bin", "docker-credential-"+dockerCredentialHelperName)

	run := func(command, server string) (string, int) {
		cmd := exec.Command(helper, command)
		cmd.Stdin = strings.NewReader(server + "\n")
		out, err := cmd.CombinedOutput()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out)), code
	}
	for _, server := range []string{"acr.example.test", "acr.example.test:4568", "https://acr.example.test/v2/", "registry.local:5000"} {
		out, code := run("get", server)
		var answer struct{ ServerURL, Username, Secret string }
		if code != 0 || json.Unmarshal([]byte(out), &answer) != nil || answer.Username != DockerIdentityTokenUsername || answer.Secret != "refresh-token" {
			t.Fatalf("get %s: exit %d, out %q", server, code, out)
		}
	}
	for _, server := range []string{"registry.local", "index.docker.io", "https://index.docker.io/v1/"} {
		out, code := run("get", server)
		if code != 1 || out != "credentials not found in native keychain" {
			t.Fatalf("get %s: exit %d, out %q", server, code, out)
		}
	}
	if out, code := run("list", ""); code != 0 || out != "{}" {
		t.Fatalf("list: exit %d, out %q", code, out)
	}
	env := DockerConfigEnv(dir)
	if env[0] != "DOCKER_CONFIG="+dir || !strings.HasPrefix(env[1], "PATH="+filepath.Join(dir, "bin")) {
		t.Fatalf("env %v", env)
	}
}

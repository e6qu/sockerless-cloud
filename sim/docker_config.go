package sim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DockerCredential is what a credential helper answers for a registry host:
// a username and a secret. With DockerIdentityTokenUsername as the username
// the secret is an identity token the Docker client exchanges through the
// registry's OAuth 2.0 refresh-token grant, the way `az acr login` stores
// one.
type DockerCredential struct {
	Username string
	Secret   string
}

// DockerIdentityTokenUsername is the username under which the Docker client
// treats a stored secret as an identity token.
const DockerIdentityTokenUsername = "<token>"

// WriteDockerConfig writes the Docker client configuration a build service's
// docker steps run with: the host's own configuration — its CLI plugins,
// contexts and every setting — with a credential helper in front that
// answers the registry hosts matching hostPatterns (shell patterns, matched
// against the host with and without its port) with credential, and hands any
// other host to the helper the host configured, or to nothing, which the
// client then reaches anonymously. The returned directory is DOCKER_CONFIG
// for the steps and its bin/ joins their PATH (DockerConfigEnv); the caller
// removes it after the build.
func WriteDockerConfig(hostPatterns []string, credential DockerCredential) (string, error) {
	if len(hostPatterns) == 0 {
		return "", fmt.Errorf("docker configuration: no registry host pattern")
	}
	dir, err := os.MkdirTemp("", "sim-docker-config-*")
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	hostConfig, delegate, err := hostDockerConfig()
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	for _, entry := range []string{"cli-plugins", "contexts"} {
		if src := filepath.Join(HostDockerConfigDir(), entry); pathExists(src) {
			if err := os.Symlink(src, filepath.Join(dir, entry)); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		}
	}
	helper := strings.NewReplacer(
		"@PATTERNS@", strings.Join(hostPatterns, "|"),
		"@USERNAME@", credential.Username,
		"@SECRET@", credential.Secret,
		"@DELEGATE@", delegate,
	).Replace(dockerCredentialHelper)
	if err := os.WriteFile(filepath.Join(binDir, "docker-credential-"+dockerCredentialHelperName), []byte(helper), 0o700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	hostConfig["credsStore"] = dockerCredentialHelperName
	config, err := json.MarshalIndent(hostConfig, "", "  ")
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// DockerConfigEnv is the environment that points a docker invocation at the
// configuration WriteDockerConfig wrote.
func DockerConfigEnv(configDir string) []string {
	return []string{
		"DOCKER_CONFIG=" + configDir,
		"PATH=" + filepath.Join(configDir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
}

// HostDockerConfigDir is the directory the host's docker CLI reads its
// configuration from.
func HostDockerConfigDir() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".docker"
	}
	return filepath.Join(home, ".docker")
}

// hostDockerConfig reads the host's docker configuration and the credential
// store it names, if any; a host without one has an empty configuration.
func hostDockerConfig() (map[string]any, string, error) {
	config := map[string]any{}
	raw, err := os.ReadFile(filepath.Join(HostDockerConfigDir(), "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return config, "", nil
		}
		return nil, "", err
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, "", fmt.Errorf("host docker configuration: %w", err)
	}
	delegate, _ := config["credsStore"].(string)
	return config, delegate, nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

const dockerCredentialHelperName = "sockerless-sim"

// dockerCredentialHelper is the credential helper the docker steps consult:
// the credential for the hosts its patterns name, the host's own helper for
// every other host, and the protocol's not-found answer — its text on
// standard output with exit status 1 — when there is none.
const dockerCredentialHelper = `#!/bin/sh
# Credential helper of the simulator's build service.
case "$1" in
  get) ;;
  list) echo '{}'; exit 0 ;;
  *) exit 0 ;;
esac
read -r server
host="${server#*://}"
host="${host%%/*}"
bare="${host%:*}"
matched=no
for candidate in "$host" "$bare"; do
  case "$candidate" in
    @PATTERNS@) matched=yes ;;
  esac
done
if [ "$matched" = yes ]; then
  printf '{"ServerURL":"%s","Username":"@USERNAME@","Secret":"@SECRET@"}\n' "$server"
  exit 0
fi
if [ -n "@DELEGATE@" ]; then
  printf '%s\n' "$server" | exec "docker-credential-@DELEGATE@" get
fi
echo "credentials not found in native keychain"
exit 1
`

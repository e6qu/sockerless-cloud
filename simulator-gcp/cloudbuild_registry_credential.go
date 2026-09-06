package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cloudBuildServiceAccount is the identity a build runs as: the service
// account the build names, otherwise the project's Cloud Build service
// account, PROJECT_NUMBER@cloudbuild.gserviceaccount.com.
func cloudBuildServiceAccount(b Build) string {
	if sa := b.ServiceAccount; sa != "" {
		// A build names it as projects/{project}/serviceAccounts/{email}.
		if i := strings.LastIndex(sa, "/serviceAccounts/"); i >= 0 {
			return sa[i+len("/serviceAccounts/"):]
		}
		return sa
	}
	return projectNumber(b.ProjectID) + "@cloudbuild.gserviceaccount.com"
}

// cloudBuildDockerConfig writes the Docker client configuration a build's
// docker steps run with: the host's own configuration — its CLI plugins,
// contexts and every setting — with a credential helper in front that
// answers Artifact Registry and Container Registry — by their hosts, or by
// this simulator's own port, at which a coordinate that relocates the
// registry reaches it — with an access token of the build's service account
// as the `oauth2accesstoken` password, and hands any other registry to the
// helper the host configured, or to nothing, which the step then reaches
// anonymously. Real Cloud Build's docker builder carries the same credential
// through the gcloud helper. The directory is DOCKER_CONFIG for the steps;
// its bin/ joins their PATH. The caller removes it after the build.
func cloudBuildDockerConfig(b Build) (string, error) {
	dir, err := os.MkdirTemp("", "sim-cloudbuild-docker-*")
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
		if src := filepath.Join(hostDockerConfigDir(), entry); pathExists(src) {
			if err := os.Symlink(src, filepath.Join(dir, entry)); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		}
	}
	now := time.Now()
	token := signAccessToken(cloudBuildServiceAccount(b), now, now.Add(time.Hour))
	ownPort := ""
	if port, err := hostMetadataPort(); err == nil {
		ownPort = fmt.Sprintf("|*:%d", port)
	}
	helper := strings.NewReplacer(
		"@OWN_PORT@", ownPort,
		"@USERNAME@", arUserAccessToken,
		"@SECRET@", token,
		"@DELEGATE@", delegate,
	).Replace(cloudBuildCredentialHelper)
	if err := os.WriteFile(filepath.Join(binDir, "docker-credential-"+cloudBuildCredentialHelperName), []byte(helper), 0o700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	hostConfig["credsStore"] = cloudBuildCredentialHelperName
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

// hostDockerConfigDir is the directory the host's docker CLI reads its
// configuration from.
func hostDockerConfigDir() string {
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
	raw, err := os.ReadFile(filepath.Join(hostDockerConfigDir(), "config.json"))
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

// cloudBuildDockerEnv is the environment that points a docker step at the
// configuration cloudBuildDockerConfig wrote.
func cloudBuildDockerEnv(configDir string) []string {
	return []string{
		"DOCKER_CONFIG=" + configDir,
		"PATH=" + filepath.Join(configDir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
}

const cloudBuildCredentialHelperName = "sockerless-cloudbuild"

// cloudBuildCredentialHelper is the docker credential helper the build's
// steps consult. The registry hosts it answers are the ones
// imageOnGoogleRegistry names.
const cloudBuildCredentialHelper = `#!/bin/sh
# Credential helper of the simulator's Cloud Build: the build's service
# account for Google's registries, nothing anywhere else.
case "$1" in
  get) ;;
  list) echo '{}'; exit 0 ;;
  *) exit 0 ;;
esac
read -r server
host="${server#*://}"
host="${host%%/*}"
case "$host" in
  *-docker.pkg.dev|*-docker.pkg.dev:*|gcr.io|gcr.io:*|*.gcr.io|*.gcr.io:*@OWN_PORT@)
    printf '{"ServerURL":"%s","Username":"@USERNAME@","Secret":"@SECRET@"}\n' "$server" ;;
  *)
    if [ -n "@DELEGATE@" ]; then
      printf '%s\n' "$server" | exec "docker-credential-@DELEGATE@" get
    fi
    # The not-found answer of the credential helper protocol: this text on
    # standard output with exit status 1, which the client reads as "no
    # credential" rather than as a failure.
    echo "credentials not found in native keychain"
    exit 1 ;;
esac
`

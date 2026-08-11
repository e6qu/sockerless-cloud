package aws_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCloudFront_Function_Lifecycle(t *testing.T) {
	dir := t.TempDir()
	codeFile := filepath.Join(dir, "fn.js")
	code := `function handler(event) { return event.request; }`
	require.NoError(t, os.WriteFile(codeFile, []byte(code), 0o644))

	name := "cli-fn-" + time.Now().Format("150405.000000")
	out := runCLI(t, awsCLI("cloudfront", "create-function",
		"--name", name,
		"--function-code", "fileb://"+codeFile,
		"--function-config", `{"Comment": "cli test", "Runtime": "cloudfront-js-2.0"}`,
		"--output", "json",
	))
	var createResult struct {
		FunctionSummary struct {
			Name             string `json:"Name"`
			FunctionMetadata struct {
				Stage string `json:"Stage"`
			} `json:"FunctionMetadata"`
		} `json:"FunctionSummary"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.Equal(t, name, createResult.FunctionSummary.Name)
	require.Equal(t, "DEVELOPMENT", createResult.FunctionSummary.FunctionMetadata.Stage)
	etag := createResult.ETag

	runCLI(t, awsCLI("cloudfront", "describe-function", "--name", name, "--output", "json"))

	// publish
	runCLI(t, awsCLI("cloudfront", "publish-function",
		"--name", name, "--if-match", etag, "--output", "json"))

	// describe again to get fresh ETag
	descOut := runCLI(t, awsCLI("cloudfront", "describe-function", "--name", name, "--output", "json"))
	var descResult struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(descOut), &descResult))

	runCLI(t, awsCLI("cloudfront", "delete-function",
		"--name", name, "--if-match", descResult.ETag))
}

func TestCloudFront_PublicKey_Lifecycle(t *testing.T) {
	name := "cli-pk-" + time.Now().Format("150405.000000")
	encoded := base64.StdEncoding.EncodeToString([]byte("test-key-bytes"))
	pem := "-----BEGIN PUBLIC KEY-----\n" + encoded + "\n-----END PUBLIC KEY-----\n"
	cfgJSON := fmt.Sprintf(`{
		"CallerReference": "%s",
		"Name": "%s",
		"EncodedKey": %q,
		"Comment": "cli test"
	}`, name+"-ref", name, pem)

	out := runCLI(t, awsCLI("cloudfront", "create-public-key",
		"--public-key-config", cfgJSON, "--output", "json"))
	var createResult struct {
		PublicKey struct {
			Id string `json:"Id"`
		} `json:"PublicKey"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.PublicKey.Id)

	runCLI(t, awsCLI("cloudfront", "delete-public-key",
		"--id", createResult.PublicKey.Id, "--if-match", createResult.ETag))
}

func TestCloudFront_KeyGroup_Lifecycle(t *testing.T) {
	pkName := "cli-kg-pk-" + time.Now().Format("150405.000000")
	encoded := base64.StdEncoding.EncodeToString([]byte("kg-key-bytes"))
	pem := "-----BEGIN PUBLIC KEY-----\n" + encoded + "\n-----END PUBLIC KEY-----\n"
	pkCfg := fmt.Sprintf(`{
		"CallerReference": "%s",
		"Name": "%s",
		"EncodedKey": %q
	}`, pkName+"-ref", pkName, pem)
	pkOut := runCLI(t, awsCLI("cloudfront", "create-public-key",
		"--public-key-config", pkCfg, "--output", "json"))
	var pkResult struct {
		PublicKey struct {
			Id string `json:"Id"`
		} `json:"PublicKey"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(pkOut), &pkResult))
	pkID := pkResult.PublicKey.Id

	kgName := "cli-kg-" + time.Now().Format("150405.000000")
	kgCfg := fmt.Sprintf(`{
		"Name": "%s",
		"Items": ["%s"],
		"Comment": "cli test"
	}`, kgName, pkID)
	kgOut := runCLI(t, awsCLI("cloudfront", "create-key-group",
		"--key-group-config", kgCfg, "--output", "json"))
	var kgResult struct {
		KeyGroup struct {
			Id string `json:"Id"`
		} `json:"KeyGroup"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(kgOut), &kgResult))
	require.NotEmpty(t, kgResult.KeyGroup.Id)

	runCLI(t, awsCLI("cloudfront", "delete-key-group",
		"--id", kgResult.KeyGroup.Id, "--if-match", kgResult.ETag))
	runCLI(t, awsCLI("cloudfront", "delete-public-key",
		"--id", pkID, "--if-match", pkResult.ETag))
}

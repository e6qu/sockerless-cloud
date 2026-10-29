package gcp_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IAM decides each gcloud call: a service account activated from its own key
// reads an object once it holds roles/storage.objectViewer on the bucket, is
// refused the delete, and deletes once it holds roles/storage.objectAdmin.
func TestIAMEnforcementObjectViewerCLI(t *testing.T) {
	t.Parallel()
	accountID := uniquename.New("cli-enforce-sa")
	email := accountID + "@" + project + ".iam.gserviceaccount.com"
	bucket := uniquename.New("cli-enforce")

	runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID, "--format=json"))
	keyFile := filepath.Join(tmpDir, accountID+".json")
	runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "create", keyFile, "--iam-account="+email))
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--uniform-bucket-level-access"))
	source := filepath.Join(tmpDir, accountID+"-object.txt")
	require.NoError(t, os.WriteFile(source, []byte("guarded\n"), 0o600))
	runCLI(t, gcloudCLI("storage", "cp", source, "gs://"+bucket+"/object.txt"))

	saConfig := filepath.Join(tmpDir, "gcloud-"+accountID)
	runCLI(t, gcloudSAActivatedCLI(saConfig, "config", "set", "auth/token_host", baseURL+"/token"))
	runCLI(t, gcloudSAActivatedCLI(saConfig, "config", "set", "api_endpoint_overrides/storage", baseURL+"/"))
	runCLI(t, gcloudSAActivatedCLI(saConfig, "auth", "activate-service-account", "--key-file="+keyFile))

	out := gcloudCLIFails(t, gcloudSAActivatedCLI(saConfig, "storage", "cat", "gs://"+bucket+"/object.txt"))
	assert.Contains(t, out, "403", "an account bound to nothing reads the object: %s", out)

	runCLI(t, gcloudCLI("storage", "buckets", "add-iam-policy-binding", "gs://"+bucket,
		"--member=serviceAccount:"+email, "--role=roles/storage.objectViewer"))
	assert.Equal(t, "guarded\n", runCLI(t, gcloudSAActivatedCLI(saConfig, "storage", "cat", "gs://"+bucket+"/object.txt")))

	gcloudCLIFails(t, gcloudSAActivatedCLI(saConfig, "storage", "rm", "gs://"+bucket+"/object.txt"))
	assert.Equal(t, "guarded\n", runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/object.txt")),
		"the refused delete leaves the object")

	runCLI(t, gcloudCLI("storage", "buckets", "add-iam-policy-binding", "gs://"+bucket,
		"--member=serviceAccount:"+email, "--role=roles/storage.objectAdmin"))
	runCLI(t, gcloudSAActivatedCLI(saConfig, "storage", "rm", "gs://"+bucket+"/object.txt"))
}

// A project's creator becomes its Owner: a service account that holds only
// Project Creator on a folder creates a project there with gcloud, then
// deletes it, which only an Owner may.
func TestIAMEnforcementProjectCreatorOwnsProjectCLI(t *testing.T) {
	t.Parallel()
	accountID := uniquename.New("cli-creator-sa")
	email := accountID + "@" + project + ".iam.gserviceaccount.com"
	runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID, "--format=json"))
	keyFile := filepath.Join(tmpDir, accountID+".json")
	runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "create", keyFile, "--iam-account="+email))

	out := runCLI(t, gcloudCLI("resource-manager", "folders", "create",
		"--display-name=cli-creator-owns", "--organization="+cliOrganizationID, "--format=json"))
	var folder cliV2Folder
	parseJSONObject(t, out, &folder)
	folderID := strings.TrimPrefix(folder.Name, "folders/")
	runCLI(t, gcloudCLI("resource-manager", "folders", "add-iam-policy-binding", folderID,
		"--member=serviceAccount:"+email, "--role=roles/resourcemanager.projectCreator"))

	saConfig := filepath.Join(tmpDir, "gcloud-"+accountID)
	runCLI(t, gcloudSAActivatedCLI(saConfig, "config", "set", "auth/token_host", baseURL+"/token"))
	for _, api := range []string{"cloudresourcemanager", "serviceusage", "cloudbilling"} {
		runCLI(t, gcloudSAActivatedCLI(saConfig, "config", "set", "api_endpoint_overrides/"+api, baseURL+"/"))
	}
	runCLI(t, gcloudSAActivatedCLI(saConfig, "auth", "activate-service-account", "--key-file="+keyFile))

	id := uniquename.New("cli-creator")
	runCLI(t, gcloudSAActivatedCLI(saConfig, "projects", "create", id, "--folder="+folderID))
	policy := runCLI(t, gcloudCLI("projects", "get-iam-policy", id, "--format=json"))
	assert.Contains(t, policy, `"serviceAccount:`+email+`"`)
	runCLI(t, gcloudSAActivatedCLI(saConfig, "projects", "delete", id, "--quiet"))
}

// A method's REST reference page names its permission: Compute Viewer holds
// compute.instanceGroupManagers.get, which listManagedInstances requires, and
// not compute.instanceGroupManagers.update, which resize requires.
func TestIAMEnforcementReferencePagePermissionsCLI(t *testing.T) {
	t.Parallel()
	accountID := uniquename.New("cli-mig-viewer")
	email := accountID + "@" + project + ".iam.gserviceaccount.com"
	template := uniquename.New("cli-mig-tmpl")
	group := uniquename.New("cli-mig")
	const zone = "us-central1-a"

	runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID, "--format=json"))
	keyFile := filepath.Join(tmpDir, accountID+".json")
	runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "create", keyFile, "--iam-account="+email))
	runCLI(t, gcloudCLI("compute", "instance-templates", "create", template, "--machine-type=n1-standard-1", "--quiet"))
	runCLI(t, gcloudCLI("compute", "instance-groups", "managed", "create", group,
		"--template="+template, "--size=0", "--zone="+zone))
	runCLI(t, gcloudCLI("projects", "add-iam-policy-binding", project,
		"--member=serviceAccount:"+email, "--role=roles/compute.viewer"))

	saConfig := filepath.Join(tmpDir, "gcloud-"+accountID)
	runCLI(t, gcloudSAActivatedCLI(saConfig, "config", "set", "auth/token_host", baseURL+"/token"))
	runCLI(t, gcloudSAActivatedCLI(saConfig, "config", "set", "api_endpoint_overrides/compute", baseURL+"/"))
	runCLI(t, gcloudSAActivatedCLI(saConfig, "auth", "activate-service-account", "--key-file="+keyFile))

	runCLI(t, gcloudSAActivatedCLI(saConfig, "compute", "instance-groups", "managed", "list-instances", group,
		"--zone="+zone, "--format=json"))
	out := gcloudCLIFails(t, gcloudSAActivatedCLI(saConfig, "compute", "instance-groups", "managed", "resize", group,
		"--size=1", "--zone="+zone))
	assert.Contains(t, out, "compute.instanceGroupManagers.update", "a viewer resizes the group: %s", out)
}

package azure_tf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformComputeGalleryCapturesAMachine captures a running machine into
// an azurerm_shared_image_version of a specialized azurerm_shared_image. The
// provider reads the machine back as the version's managed_image_id, so a
// version that lost its source shows as a diff on the plan after the apply;
// the destroy deletes the version, the definition and the gallery in the order
// Azure requires.
func TestTerraformComputeGalleryCapturesAMachine(t *testing.T) {
	dir := tfWorkspaceFrom(t, mustAbs("gallery"))
	run := func(name string, args ...string) {
		t.Helper()
		out, err := runTimed(t, name, terraformCmd(dir, args...))
		require.NoError(t, err, "%s failed:\n%s", name, out)
	}
	run("terraform init", "init")
	run("terraform apply", "apply", "-auto-approve")
	run("terraform plan", "plan", "-detailed-exitcode")

	outputs := readOutputs(t, dir)
	require.Equal(t, "00000000-0000-0000-0000-000000000001-TF_AZRM_GALLERY", outputs.must(t, "gallery_unique_name"),
		"a gallery's unique name is its subscription and its upper-cased name")
	imageID := outputs.must(t, "image_id")
	require.Contains(t, imageID, "/resourceGroups/tf-azrm-gallery-rg/providers/Microsoft.Compute/galleries/tf_azrm_gallery/images/tf-azrm-debian",
		"the image definition id must carry the canonical ARM path; got %s", imageID)
	versionID := outputs.must(t, "version_id")
	require.True(t, strings.HasSuffix(versionID, "/images/tf-azrm-debian/versions/1.0.0"),
		"the version id names the definition and the version; got %s", versionID)

	run("terraform destroy", "destroy", "-auto-approve")
}

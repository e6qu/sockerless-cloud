package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudWatchLogsCLI_FilterMissingGroupRNFE expects filter-log-events on a
// missing group to fail with ResourceNotFoundException, not return an empty
// list. runCLI fails the test on a non-zero exit, so the call uses
// CombinedOutput.
func TestCloudWatchLogsCLI_FilterMissingGroupRNFE(t *testing.T) {
	t.Parallel()
	out, err := awsCLI("logs", "filter-log-events", "--log-group-name", "/cli/does-not-exist-483").CombinedOutput()
	if err == nil {
		t.Fatalf("filter-log-events on a missing group should fail; got success:\n%s", out)
	}
	if !strings.Contains(string(out), "ResourceNotFoundException") {
		t.Fatalf("expected ResourceNotFoundException; got:\n%s", out)
	}
	if !strings.Contains(string(out), "does not exist") {
		t.Fatalf("expected 'does not exist' message; got:\n%s", out)
	}
}

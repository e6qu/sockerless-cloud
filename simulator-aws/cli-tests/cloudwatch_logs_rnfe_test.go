package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudWatchLogsCLI_FilterMissingGroupRNFE asserts `aws logs
// filter-log-events` against a non-existent group fails with
// ResourceNotFoundException rather than returning an empty event list
// that masks misconfiguration. Uses CombinedOutput directly since the call is
// expected to exit non-zero (runCLI fatals on non-zero exit).
func TestCloudWatchLogsCLI_FilterMissingGroupRNFE(t *testing.T) {
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

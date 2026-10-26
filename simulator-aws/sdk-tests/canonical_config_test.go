package aws_sdk_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCanonicalConfig_NoSimQuirkBaseEndpoint scans test sources and Terraform
// provider configs for endpoints reconfigured around a simulator wire-protocol
// bug:
//
//	o.BaseEndpoint = aws.String(baseURL + "/<service>")
//	endpoints { s3 = "${var.endpoint}/<service>" }
//	AWS_ENDPOINT_URL=<base>/<service>
//
// A test passing against such a quirk hides a bug an unmodified consumer meets
// at the documented AWS_ENDPOINT_URL. Allowed:
//
//	o.BaseEndpoint = aws.String(baseURL)
//	endpoints { s3 = var.endpoint }
//	AWS_ENDPOINT_URL=<base>
//
// See `.claude/skills/sim-canonical-config-test/SKILL.md`.
func TestCanonicalConfig_NoSimQuirkBaseEndpoint(t *testing.T) {
	t.Parallel()

	sdkPattern := regexp.MustCompile(`BaseEndpoint\s*=\s*aws\.String\(\s*baseURL\s*\+\s*`)
	tfPattern := regexp.MustCompile(`"\$\{var\.endpoint\}/`)
	shellPattern := regexp.MustCompile(`"AWS_ENDPOINT_URL=" *\+ *baseURL *\+ *"/`)

	// Scan from the parent so sibling dirs (cli-tests, terraform-tests, scripts)
	// count.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root := filepath.Dir(cwd)

	var hits []string
	scanned := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		// The regex literals in this file match themselves.
		if base == "canonical_config_test.go" {
			return nil
		}
		if !strings.HasSuffix(base, "_test.go") && !strings.HasSuffix(base, ".tf") && base != "main.tf" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		isTF := strings.HasSuffix(base, ".tf")
		for lineNo, line := range strings.Split(string(body), "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, "//") {
				continue
			}
			matched := false
			if isTF {
				if tfPattern.MatchString(line) {
					matched = true
				}
			} else {
				if sdkPattern.MatchString(line) || shellPattern.MatchString(line) {
					matched = true
				}
			}
			if matched {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, rel+":"+itoa(lineNo+1)+": "+trim)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}

	// A scan that reached nothing reports clean, so assert the file count first.
	// The floor sits far below the hundreds of files under simulator-aws/ and only
	// catches a walk that lost its root or filter.
	require.Greaterf(t, scanned, 100,
		"the scan read only %d files under %s — it cannot report clean without having looked", scanned, root)

	if len(hits) > 0 {
		t.Errorf("sim-quirk endpoint pattern found — fix the sim's routing instead of reconfiguring clients around it (see .claude/skills/sim-canonical-config-test/SKILL.md):\n%s",
			strings.Join(hits, "\n"))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

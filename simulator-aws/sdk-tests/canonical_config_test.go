package aws_sdk_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCanonicalConfig_NoSimQuirkBaseEndpoint scans test sources and
// Terraform provider configs for the known shape:
//
//	o.BaseEndpoint = aws.String(baseURL + "/<service>")
//	endpoints { s3 = "${var.endpoint}/<service>" }
//	AWS_ENDPOINT_URL=<base>/<service>
//
// All three forms reconfigure a client around a sim wire-protocol
// bug instead of fixing the sim. The S3 case (`baseURL + "/s3"`)
// was load-bearing because tests passed against the quirk and the bug
// only surfaced when an unmodified consumer pointed at the documented
// AWS_ENDPOINT_URL.
//
// Allowed:
//
//	o.BaseEndpoint = aws.String(baseURL)
//	endpoints { s3 = var.endpoint }
//	AWS_ENDPOINT_URL=<base>
//
// Codified by skill `.claude/skills/sim-canonical-config-test/SKILL.md`.
func TestCanonicalConfig_NoSimQuirkBaseEndpoint(t *testing.T) {
	t.Parallel()

	// Go-SDK quirk: BaseEndpoint = aws.String(baseURL + ...)
	sdkPattern := regexp.MustCompile(`BaseEndpoint\s*=\s*aws\.String\(\s*baseURL\s*\+\s*`)
	// Terraform: endpoints { <svc> = "${var.endpoint}/<suffix>" }
	tfPattern := regexp.MustCompile(`"\$\{var\.endpoint\}/`)
	// Shell / env: AWS_ENDPOINT_URL=<base>/<suffix>  (string-concat in shell scripts or Cmd.Env)
	shellPattern := regexp.MustCompile(`"AWS_ENDPOINT_URL=" *\+ *baseURL *\+ *"/`)

	// Scan one level up from cwd so we cover sibling dirs (cli-tests,
	// terraform-tests, scripts).
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root := filepath.Dir(cwd)

	var hits []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip vendor + .git + build artifacts.
			name := d.Name()
			if name == "vendor" || name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		// Skip the self-test file (regex literals match themselves).
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
		isTF := strings.HasSuffix(base, ".tf")
		for lineNo, line := range strings.Split(string(body), "\n") {
			trim := strings.TrimSpace(line)
			// Allow .tf comments and Go comments — skip a line that
			// is entirely a comment.
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

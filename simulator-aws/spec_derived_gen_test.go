package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// These tables are compiled from the vendored Smithy models, so they must be
// the models' own: a specification refresh regenerates them through
// scripts/gen-spec-derived.sh.
func TestSpecDerivedTablesMatchTheVendoredModels(t *testing.T) {
	for _, table := range []struct{ generator, file string }{
		{"scripts/gen-cloudwatch-cbor-shapes.go", "cloudwatch_cbor_shapes_gen.go"},
		{"scripts/gen-aws-error-message-members.go", "aws_error_members_gen.go"},
		{"scripts/generate-sfn-rest-integrations.go", "stepfunctions_rest_integrations_gen.go"},
	} {
		t.Run(table.file, func(t *testing.T) {
			t.Parallel()
			regenerated := filepath.Join(t.TempDir(), table.file+".generated")
			command := exec.Command("go", "run", table.generator, regenerated)
			command.Dir = ".."
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("generator: %v\n%s", err, out)
			}
			want, err := os.ReadFile(regenerated)
			if err != nil {
				t.Fatal(err)
			}
			have, err := os.ReadFile(table.file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(have, want) {
				t.Fatalf("%s is stale; run: bash scripts/gen-spec-derived.sh", table.file)
			}
		})
	}
}

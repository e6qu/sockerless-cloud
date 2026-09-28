package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The CBOR bridge converts by these shapes, so they must be the vendored
// model's own.
func TestCloudWatchCBORShapesMatchTheVendoredModel(t *testing.T) {
	regenerated := filepath.Join(t.TempDir(), "cloudwatch_cbor_shapes.generated")
	command := exec.Command("go", "run", "scripts/gen-cloudwatch-cbor-shapes.go", regenerated)
	command.Dir = ".."
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generator: %v\n%s", err, out)
	}
	want, err := os.ReadFile(regenerated)
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile("cloudwatch_cbor_shapes_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(have, want) {
		t.Fatal("cloudwatch_cbor_shapes_gen.go is stale; run: go run scripts/gen-cloudwatch-cbor-shapes.go")
	}
}

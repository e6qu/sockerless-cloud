package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeLambdaDeploymentPackageIsReadableBySandboxUser(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("exports.handler = async () => 'ok';")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	root, err := materializeLambdaDeploymentPackage(LambdaFunction{
		Code: &LambdaFunctionCode{
			ZipFile: base64.StdEncoding.EncodeToString(archive.Bytes()),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0755); got != want {
		t.Fatalf("deployment-package root mode = %04o, want %04o", got, want)
	}
	if _, err := os.ReadFile(filepath.Join(root, "index.js")); err != nil {
		t.Fatalf("read extracted AWS Lambda handler: %v", err)
	}
}

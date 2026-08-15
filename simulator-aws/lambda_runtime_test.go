package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// The REPORT entry reports the memory the execution environment reached, in the
// megabytes the field is expressed in, and reports nothing where the container
// engine accounted for nothing.
func TestLambdaReportLineReportsMeasuredMemory(t *testing.T) {
	const requestID = "8f4c6d02-1c6a-4c2b-8f2e-2a7b9d4e5f60"
	measured := lambdaReportLine(requestID, 1500*time.Millisecond, 0, 1024, 254*1024*1024)
	if !strings.Contains(measured, "\tMax Memory Used: 254 MB") {
		t.Fatalf("REPORT must state the measured memory:\n%s", measured)
	}
	if !strings.Contains(measured, "\tMemory Size: 1024 MB\tMax Memory Used: 254 MB") {
		t.Fatalf("Max Memory Used must follow Memory Size:\n%s", measured)
	}

	// A partly used megabyte is a used megabyte.
	rounded := lambdaReportLine(requestID, time.Millisecond, 0, 1024, 254*1024*1024+1)
	if !strings.Contains(rounded, "\tMax Memory Used: 255 MB") {
		t.Fatalf("a partly used megabyte must round up:\n%s", rounded)
	}

	// Nothing measured is reported as nothing, never as a stand-in figure.
	unmeasured := lambdaReportLine(requestID, time.Millisecond, 0, 1024, 0)
	if strings.Contains(unmeasured, "Max Memory Used") {
		t.Fatalf("an unmeasured invocation must omit Max Memory Used:\n%s", unmeasured)
	}
	if !strings.Contains(unmeasured, "\tMemory Size: 1024 MB") {
		t.Fatalf("REPORT must still state the configured memory size:\n%s", unmeasured)
	}

	// The Init Duration an initialisation adds stays last.
	withInit := lambdaReportLine(requestID, time.Millisecond, 120*time.Millisecond, 512, 64*1024*1024)
	if !strings.Contains(withInit, "\tMax Memory Used: 64 MB\tInit Duration: 120.00 ms") {
		t.Fatalf("Init Duration must close the REPORT entry:\n%s", withInit)
	}
}

package gcp_cli_test

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resumablePayloadSize is past the 5 MiB at which apitools sends an upload
// through the resumable protocol, and past gcloud storage's 8 MiB default
// resumable threshold.
const resumablePayloadSize = 9 << 20

// writeResumablePayload writes resumablePayloadSize random bytes to a file in
// the test's temporary directory.
func writeResumablePayload(t *testing.T, name string) (string, []byte) {
	t.Helper()
	payload := make([]byte, resumablePayloadSize)
	_, err := rand.Read(payload)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, payload, 0o644))
	return path, payload
}

// gcloudUploadCLI is gcloudCLI with a 256 KiB upload chunk size, so an upload
// past the resumable threshold travels as several chunks of one session.
func gcloudUploadCLI(args ...string) *exec.Cmd {
	cmd := gcloudCLI(args...)
	cmd.Env = append(cmd.Env, "CLOUDSDK_STORAGE_UPLOAD_CHUNK_SIZE=262144")
	return cmd
}

// bqCLI runs the bq CLI that ships with gcloud, authenticated by gcloud's
// credentials, against the simulator's BigQuery API root.
func bqCLI(args ...string) *exec.Cmd {
	cmd := gcloudCLI()
	cmd.Path, cmd.Err = exec.LookPath("bq")
	cmd.Args = append([]string{"bq", "--api=" + baseURL, "--project_id=" + project}, args...)
	return cmd
}

// `gcloud storage cp` sends a file past its resumable threshold through
// /resumable/upload/storage/v1/b/{bucket}/o, and the object holds its bytes.
func TestGCSCLI_ResumableUpload(t *testing.T) {
	const bucket = "cli-resumable-bucket"
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us"))
	t.Cleanup(func() { runCLI(t, gcloudCLI("storage", "rm", "--recursive", "gs://"+bucket)) })
	source, payload := writeResumablePayload(t, "large.bin")

	runCLI(t, gcloudUploadCLI("storage", "cp", source, "gs://"+bucket+"/large.bin"))

	sum := md5.Sum(payload)
	described := runCLI(t, gcloudCLI("storage", "objects", "describe", "gs://"+bucket+"/large.bin", "--format=value(size,md5_hash)"))
	assert.Equal(t, []string{fmt.Sprint(len(payload)), base64.StdEncoding.EncodeToString(sum[:])}, strings.Fields(described))
}

// `gcloud artifacts generic upload` and `gcloud artifacts files upload` send a
// file past 5 MiB through the resumable upload paths of genericArtifacts.upload
// and files.upload, and the downloaded file holds its bytes.
func TestArtifactRegistryCLI_ResumableUploads(t *testing.T) {
	const repo = "cli-resumable-generic"
	runCLI(t, gcloudCLI("artifacts", "repositories", "create", repo,
		"--repository-format=generic", "--location="+location))
	t.Cleanup(func() {
		runCLI(t, gcloudCLI("artifacts", "repositories", "delete", repo, "--location="+location, "--quiet"))
	})
	source, payload := writeResumablePayload(t, "large.bin")

	runCLI(t, gcloudUploadCLI("artifacts", "generic", "upload", "--repository="+repo, "--location="+location,
		"--package=bundle", "--version=1.0.0", "--source="+source))
	genericDir := t.TempDir()
	runCLI(t, gcloudCLI("artifacts", "generic", "download", "--repository="+repo, "--location="+location,
		"--package=bundle", "--version=1.0.0", "--destination="+genericDir))
	downloaded, err := os.ReadFile(filepath.Join(genericDir, "large.bin"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(payload, downloaded), "the generic artifact holds the uploaded bytes")

	runCLI(t, gcloudUploadCLI("artifacts", "files", "upload", "--repository="+repo, "--location="+location,
		"--file=attached.bin", "--source="+source))
	attachedDir := t.TempDir()
	runCLI(t, gcloudCLI("artifacts", "files", "download", "attached.bin", "--repository="+repo, "--location="+location,
		"--destination="+attachedDir, "--local-filename=attached.bin"))
	downloaded, err = os.ReadFile(filepath.Join(attachedDir, "attached.bin"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(payload, downloaded), "the file holds the uploaded bytes")
}

// `bq load` sends a local file past 5 MiB through the resumable upload path of
// jobs.insert, waits for the load job, and the table holds every row.
func TestBigQueryCLI_ResumableLoad(t *testing.T) {
	const dataset = "cli_resumable_load"
	var csv bytes.Buffer
	rows := 0
	for csv.Len() < resumablePayloadSize {
		fmt.Fprintf(&csv, "%d,row-%d\n", rows, rows)
		rows++
	}
	source := filepath.Join(t.TempDir(), "rows.csv")
	require.NoError(t, os.WriteFile(source, csv.Bytes(), 0o644))

	// bq builds its client from the Discovery document its API root serves.
	var discovery struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	parseJSON(t, httpDoJSON(t, "GET", baseURL+"/$discovery/rest?version=v2", ""), &discovery)
	require.Equal(t, "bigquery", discovery.Name)
	require.Equal(t, "v2", discovery.Version)

	runCLI(t, bqCLI("mk", "--dataset", project+":"+dataset))
	t.Cleanup(func() { runCLI(t, bqCLI("rm", "-r", "-f", "--dataset", project+":"+dataset)) })
	runCLI(t, bqCLI("load", "--source_format=CSV", dataset+".rows", source, "id:INTEGER,name:STRING"))

	var table struct {
		NumRows string `json:"numRows"`
	}
	parseJSON(t, runCLI(t, bqCLI("show", "--format=json", dataset+".rows")), &table)
	assert.Equal(t, fmt.Sprint(rows), table.NumRows)
}

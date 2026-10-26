package aws_cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3CLI_SelectObjectContent expects select-object-content to decode the
// SelectObjectContentEventStream's Records into the outfile, holding the
// stored rows for both CSV and JSON Lines input.
func TestS3CLI_SelectObjectContent(t *testing.T) {
	t.Parallel()
	t.Run("CSV", func(t *testing.T) {
		bucket, key := "cli-select-csv-bucket", "people.csv"
		csvBody := "alice,30\nbob,25\ncarol,42\n"
		s3PutObjectCLI(t, bucket, key, csvBody)

		outfile := filepath.Join(tmpDir, "select-out.csv")
		runCLI(t, awsCLI("s3api", "select-object-content",
			"--bucket", bucket, "--key", key,
			"--expression", "SELECT * FROM S3Object",
			"--expression-type", "SQL",
			"--input-serialization", `{"CSV": {}}`,
			"--output-serialization", `{"CSV": {}}`,
			outfile,
		))

		got, err := os.ReadFile(outfile)
		require.NoError(t, err)
		assert.Equal(t, csvBody, string(got),
			"select outfile must echo the stored CSV rows faithfully")
	})

	t.Run("JSONLines", func(t *testing.T) {
		bucket, key := "cli-select-json-bucket", "people.jsonl"
		jsonBody := `{"name":"alice","age":30}` + "\n" + `{"name":"bob","age":25}` + "\n"
		s3PutObjectCLI(t, bucket, key, jsonBody)

		outfile := filepath.Join(tmpDir, "select-out.jsonl")
		runCLI(t, awsCLI("s3api", "select-object-content",
			"--bucket", bucket, "--key", key,
			"--expression", "SELECT * FROM S3Object s",
			"--expression-type", "SQL",
			"--input-serialization", `{"JSON": {"Type": "LINES"}}`,
			"--output-serialization", `{"JSON": {}}`,
			outfile,
		))

		got, err := os.ReadFile(outfile)
		require.NoError(t, err)
		assert.Equal(t, jsonBody, string(got),
			"select outfile must echo the stored JSON-Lines rows faithfully")
	})
}

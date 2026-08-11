package aws_sdk_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_SelectObjectContent exercises SelectObjectContent over both CSV and
// JSON-Lines input, decoding the SelectObjectContentEventStream with the real
// aws-sdk-go-v2 client. It asserts the Records payloads echo the stored rows
// faithfully and that a Stats event (with real byte counts) and a terminal
// End event arrive.
func TestS3_SelectObjectContent(t *testing.T) {
	client := s3Client()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("select-bucket"),
	})
	require.NoError(t, err)

	t.Run("CSV", func(t *testing.T) {
		csvBody := "alice,30\nbob,25\ncarol,42\n"
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("select-bucket"),
			Key:    aws.String("people.csv"),
			Body:   strings.NewReader(csvBody),
		})
		require.NoError(t, err)

		out, err := client.SelectObjectContent(ctx, &s3.SelectObjectContentInput{
			Bucket:         aws.String("select-bucket"),
			Key:            aws.String("people.csv"),
			Expression:     aws.String("SELECT * FROM S3Object"),
			ExpressionType: s3types.ExpressionTypeSql,
			InputSerialization: &s3types.InputSerialization{
				CSV: &s3types.CSVInput{},
			},
			OutputSerialization: &s3types.OutputSerialization{
				CSV: &s3types.CSVOutput{},
			},
		})
		require.NoError(t, err)
		defer out.GetStream().Close()

		records, sawStats, sawEnd, statsReturned := drainSelectStream(t, out.GetStream())

		assert.Equal(t, csvBody, string(records),
			"Records payload must echo the stored CSV rows faithfully")
		assert.True(t, sawStats, "a Stats event must arrive")
		assert.True(t, sawEnd, "an End event must arrive")
		assert.Equal(t, int64(len(csvBody)), statsReturned,
			"Stats.BytesReturned must equal the real returned record bytes")
	})

	t.Run("JSONLines", func(t *testing.T) {
		jsonBody := `{"name":"alice","age":30}` + "\n" + `{"name":"bob","age":25}` + "\n"
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("select-bucket"),
			Key:    aws.String("people.jsonl"),
			Body:   strings.NewReader(jsonBody),
		})
		require.NoError(t, err)

		out, err := client.SelectObjectContent(ctx, &s3.SelectObjectContentInput{
			Bucket:         aws.String("select-bucket"),
			Key:            aws.String("people.jsonl"),
			Expression:     aws.String("SELECT * FROM S3Object s"),
			ExpressionType: s3types.ExpressionTypeSql,
			InputSerialization: &s3types.InputSerialization{
				JSON: &s3types.JSONInput{Type: s3types.JSONTypeLines},
			},
			OutputSerialization: &s3types.OutputSerialization{
				JSON: &s3types.JSONOutput{},
			},
		})
		require.NoError(t, err)
		defer out.GetStream().Close()

		records, sawStats, sawEnd, _ := drainSelectStream(t, out.GetStream())

		// Each stored JSON record must round-trip (compacted, newline-delimited).
		want := `{"name":"alice","age":30}` + "\n" + `{"name":"bob","age":25}` + "\n"
		assert.Equal(t, want, string(records),
			"Records payload must echo the stored JSON-Lines rows faithfully")
		assert.True(t, sawStats, "a Stats event must arrive")
		assert.True(t, sawEnd, "an End event must arrive")
	})
}

// drainSelectStream consumes the SelectObjectContentEventStream, concatenating
// every Records payload and noting whether Stats / End events arrived. It
// returns the aggregated records bytes, the stats/end presence flags, and the
// Stats event's BytesReturned (0 if no Stats arrived).
func drainSelectStream(t *testing.T, stream *s3.SelectObjectContentEventStream) ([]byte, bool, bool, int64) {
	t.Helper()
	var records bytes.Buffer
	var sawStats, sawEnd bool
	var statsReturned int64
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case *s3types.SelectObjectContentEventStreamMemberRecords:
			records.Write(e.Value.Payload)
		case *s3types.SelectObjectContentEventStreamMemberStats:
			sawStats = true
			if e.Value.Details != nil && e.Value.Details.BytesReturned != nil {
				statsReturned = *e.Value.Details.BytesReturned
			}
		case *s3types.SelectObjectContentEventStreamMemberEnd:
			sawEnd = true
		}
	}
	require.NoError(t, stream.Err(), "event stream must close without error")
	return records.Bytes(), sawStats, sawEnd, statsReturned
}

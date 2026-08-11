package aws_cli_test

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKinesisCLI_StreamAndRecords(t *testing.T) {
	stream := "cli-kinesis-stream"

	runCLI(t, awsCLI("kinesis", "create-stream",
		"--stream-name", stream,
		"--shard-count", "1",
		"--tags", "env=cli"))
	t.Cleanup(func() {
		_ = awsCLI("kinesis", "delete-stream", "--stream-name", stream).Run()
	})

	describe := runCLI(t, awsCLI("kinesis", "describe-stream", "--stream-name", stream))
	var desc struct {
		StreamDescription struct {
			StreamName   string `json:"StreamName"`
			StreamStatus string `json:"StreamStatus"`
			Shards       []struct {
				ShardID string `json:"ShardId"`
			} `json:"Shards"`
		} `json:"StreamDescription"`
	}
	parseJSON(t, describe, &desc)
	assert.Equal(t, stream, desc.StreamDescription.StreamName)
	assert.Equal(t, "ACTIVE", desc.StreamDescription.StreamStatus)
	require.Len(t, desc.StreamDescription.Shards, 1)

	runCLI(t, awsCLI("kinesis", "put-record",
		"--stream-name", stream,
		"--partition-key", "cli-pk",
		"--data", "cli-body",
		"--cli-binary-format", "raw-in-base64-out"))

	iterJSON := runCLI(t, awsCLI("kinesis", "get-shard-iterator",
		"--stream-name", stream,
		"--shard-id", desc.StreamDescription.Shards[0].ShardID,
		"--shard-iterator-type", "TRIM_HORIZON"))
	var iter struct {
		ShardIterator string `json:"ShardIterator"`
	}
	parseJSON(t, iterJSON, &iter)
	require.NotEmpty(t, iter.ShardIterator)

	recordsJSON := runCLI(t, awsCLI("kinesis", "get-records",
		"--shard-iterator", iter.ShardIterator,
		"--limit", "10"))
	var records struct {
		Records []struct {
			Data string `json:"Data"`
		} `json:"Records"`
	}
	parseJSON(t, recordsJSON, &records)
	require.Len(t, records.Records, 1)
	body, err := base64.StdEncoding.DecodeString(records.Records[0].Data)
	require.NoError(t, err)
	assert.Equal(t, "cli-body", string(body))
}

// TestKinesisCLI_ListShardsPagination drives ListShards across pages over the
// CLI's --max-results / --next-token params and confirms the page cap, the
// truncation token, and that the NextToken-only resume recovers the stream.
func TestKinesisCLI_ListShardsPagination(t *testing.T) {
	stream := "cli-kinesis-shard-paging"
	runCLI(t, awsCLI("kinesis", "create-stream",
		"--stream-name", stream,
		"--shard-count", "4"))
	t.Cleanup(func() {
		_ = awsCLI("kinesis", "delete-stream", "--stream-name", stream).Run()
	})

	type listShardsOut struct {
		Shards []struct {
			ShardID string `json:"ShardId"`
		} `json:"Shards"`
		NextToken string `json:"NextToken"`
	}

	page1JSON := runCLI(t, awsCLI("kinesis", "list-shards",
		"--stream-name", stream,
		"--max-results", "2"))
	var page1 listShardsOut
	parseJSON(t, page1JSON, &page1)
	require.Len(t, page1.Shards, 2)
	require.NotEmpty(t, page1.NextToken, "list-shards must return NextToken when truncated")

	// Resume with NextToken ONLY (no --stream-name), as a real client paginates.
	page2JSON := runCLI(t, awsCLI("kinesis", "list-shards",
		"--max-results", "2",
		"--next-token", page1.NextToken))
	var page2 listShardsOut
	parseJSON(t, page2JSON, &page2)
	require.Len(t, page2.Shards, 2)
	assert.Empty(t, page2.NextToken, "no NextToken on the final page")

	seen := map[string]bool{}
	for _, s := range append(page1.Shards, page2.Shards...) {
		assert.False(t, seen[s.ShardID], "shard %s must not repeat across pages", s.ShardID)
		seen[s.ShardID] = true
	}
	assert.Len(t, seen, 4, "every shard appears exactly once across pages")
}

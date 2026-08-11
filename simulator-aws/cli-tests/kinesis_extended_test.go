package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kinesisStreamARNCLI creates a stream and returns its ARN via the CLI.
func kinesisStreamARNCLI(t *testing.T, stream string) string {
	t.Helper()
	runCLI(t, awsCLI("kinesis", "create-stream", "--stream-name", stream, "--shard-count", "1"))
	t.Cleanup(func() {
		_ = awsCLI("kinesis", "delete-stream", "--stream-name", stream).Run()
	})
	out := runCLI(t, awsCLI("kinesis", "describe-stream-summary", "--stream-name", stream))
	var sum struct {
		StreamDescriptionSummary struct {
			StreamARN string `json:"StreamARN"`
		} `json:"StreamDescriptionSummary"`
	}
	parseJSON(t, out, &sum)
	require.NotEmpty(t, sum.StreamDescriptionSummary.StreamARN)
	return sum.StreamDescriptionSummary.StreamARN
}

// TestKinesisCLI_Consumers drives the enhanced fan-out consumer lifecycle over
// the CLI: register, describe, list, deregister.
func TestKinesisCLI_Consumers(t *testing.T) {
	stream := "cli-kinesis-consumers"
	streamARN := kinesisStreamARNCLI(t, stream)

	regJSON := runCLI(t, awsCLI("kinesis", "register-stream-consumer",
		"--stream-arn", streamARN,
		"--consumer-name", "cli-consumer"))
	var reg struct {
		Consumer struct {
			ConsumerName string `json:"ConsumerName"`
			ConsumerARN  string `json:"ConsumerARN"`
		} `json:"Consumer"`
	}
	parseJSON(t, regJSON, &reg)
	assert.Equal(t, "cli-consumer", reg.Consumer.ConsumerName)
	require.Contains(t, reg.Consumer.ConsumerARN, ":stream/"+stream+"/consumer/cli-consumer:")
	consumerARN := reg.Consumer.ConsumerARN

	descJSON := runCLI(t, awsCLI("kinesis", "describe-stream-consumer",
		"--consumer-arn", consumerARN))
	var desc struct {
		ConsumerDescription struct {
			ConsumerStatus string `json:"ConsumerStatus"`
			StreamARN      string `json:"StreamARN"`
		} `json:"ConsumerDescription"`
	}
	parseJSON(t, descJSON, &desc)
	assert.Equal(t, "ACTIVE", desc.ConsumerDescription.ConsumerStatus)
	assert.Equal(t, streamARN, desc.ConsumerDescription.StreamARN)

	listJSON := runCLI(t, awsCLI("kinesis", "list-stream-consumers", "--stream-arn", streamARN))
	var list struct {
		Consumers []struct {
			ConsumerARN string `json:"ConsumerARN"`
		} `json:"Consumers"`
	}
	parseJSON(t, listJSON, &list)
	require.Len(t, list.Consumers, 1)
	assert.Equal(t, consumerARN, list.Consumers[0].ConsumerARN)

	runCLI(t, awsCLI("kinesis", "deregister-stream-consumer", "--consumer-arn", consumerARN))
	runCLIExpectError(t, awsCLI("kinesis", "describe-stream-consumer", "--consumer-arn", consumerARN))
}

// TestKinesisCLI_ResourcePolicy round-trips a stream resource policy.
func TestKinesisCLI_ResourcePolicy(t *testing.T) {
	stream := "cli-kinesis-resource-policy"
	streamARN := kinesisStreamARNCLI(t, stream)

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"s","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"kinesis:GetRecords","Resource":"` + streamARN + `"}]}`
	runCLI(t, awsCLI("kinesis", "put-resource-policy",
		"--resource-arn", streamARN,
		"--policy", policy))

	getJSON := runCLI(t, awsCLI("kinesis", "get-resource-policy", "--resource-arn", streamARN))
	var got struct {
		Policy string `json:"Policy"`
	}
	parseJSON(t, getJSON, &got)
	assert.JSONEq(t, policy, got.Policy)

	runCLI(t, awsCLI("kinesis", "delete-resource-policy", "--resource-arn", streamARN))
	runCLIExpectError(t, awsCLI("kinesis", "get-resource-policy", "--resource-arn", streamARN))
}

// TestKinesisCLI_MergeAndSplitShards exercises split-then-merge over the CLI.
func TestKinesisCLI_MergeAndSplitShards(t *testing.T) {
	stream := "cli-kinesis-merge-split"
	runCLI(t, awsCLI("kinesis", "create-stream", "--stream-name", stream, "--shard-count", "1"))
	t.Cleanup(func() { _ = awsCLI("kinesis", "delete-stream", "--stream-name", stream).Run() })

	type shard struct {
		ShardID             string `json:"ShardId"`
		SequenceNumberRange struct {
			EndingSequenceNumber string `json:"EndingSequenceNumber"`
		} `json:"SequenceNumberRange"`
	}
	type streamDesc struct {
		StreamDescription struct {
			Shards []shard `json:"Shards"`
		} `json:"StreamDescription"`
	}
	openOf := func(shards []shard) []shard {
		var out []shard
		for _, s := range shards {
			if s.SequenceNumberRange.EndingSequenceNumber == "" {
				out = append(out, s)
			}
		}
		return out
	}

	var d streamDesc
	parseJSON(t, runCLI(t, awsCLI("kinesis", "describe-stream", "--stream-name", stream)), &d)
	require.Len(t, d.StreamDescription.Shards, 1)
	parent := d.StreamDescription.Shards[0].ShardID

	runCLI(t, awsCLI("kinesis", "split-shard",
		"--stream-name", stream,
		"--shard-to-split", parent,
		"--new-starting-hash-key", "170141183460469231731687303715884105728"))

	var afterSplit streamDesc
	parseJSON(t, runCLI(t, awsCLI("kinesis", "describe-stream", "--stream-name", stream)), &afterSplit)
	open := openOf(afterSplit.StreamDescription.Shards)
	require.Len(t, open, 2, "split yields two open children")

	runCLI(t, awsCLI("kinesis", "merge-shards",
		"--stream-name", stream,
		"--shard-to-merge", open[0].ShardID,
		"--adjacent-shard-to-merge", open[1].ShardID))

	var afterMerge streamDesc
	parseJSON(t, runCLI(t, awsCLI("kinesis", "describe-stream", "--stream-name", stream)), &afterMerge)
	require.Len(t, openOf(afterMerge.StreamDescription.Shards), 1, "merge collapses to one open shard")
}

// TestKinesisCLI_TagResource covers the resource-ARN tagging trio.
func TestKinesisCLI_TagResource(t *testing.T) {
	stream := "cli-kinesis-tag-resource"
	streamARN := kinesisStreamARNCLI(t, stream)

	runCLI(t, awsCLI("kinesis", "tag-resource",
		"--resource-arn", streamARN,
		"--tags", "team=data,env=test"))

	listTags := func() map[string]string {
		out := runCLI(t, awsCLI("kinesis", "list-tags-for-resource", "--resource-arn", streamARN))
		var parsed struct {
			Tags []struct {
				Key   string `json:"Key"`
				Value string `json:"Value"`
			} `json:"Tags"`
		}
		parseJSON(t, out, &parsed)
		m := map[string]string{}
		for _, tag := range parsed.Tags {
			m[tag.Key] = tag.Value
		}
		return m
	}
	assert.Equal(t, map[string]string{"team": "data", "env": "test"}, listTags())

	runCLI(t, awsCLI("kinesis", "untag-resource",
		"--resource-arn", streamARN,
		"--tag-keys", "env"))
	assert.Equal(t, map[string]string{"team": "data"}, listTags())
}

// TestKinesisCLI_UpdateStreamMode toggles capacity mode over the CLI.
func TestKinesisCLI_UpdateStreamMode(t *testing.T) {
	stream := "cli-kinesis-stream-mode"
	streamARN := kinesisStreamARNCLI(t, stream)

	runCLI(t, awsCLI("kinesis", "update-stream-mode",
		"--stream-arn", streamARN,
		"--stream-mode-details", "StreamMode=ON_DEMAND"))

	out := runCLI(t, awsCLI("kinesis", "describe-stream-summary", "--stream-name", stream))
	var sum struct {
		StreamDescriptionSummary struct {
			StreamModeDetails struct {
				StreamMode string `json:"StreamMode"`
			} `json:"StreamModeDetails"`
		} `json:"StreamDescriptionSummary"`
	}
	parseJSON(t, out, &sum)
	assert.Equal(t, "ON_DEMAND", sum.StreamDescriptionSummary.StreamModeDetails.StreamMode)
}

// TestKinesisCLI_AccountSettings round-trips the billing commitment.
func TestKinesisCLI_AccountSettings(t *testing.T) {
	updJSON := runCLI(t, awsCLI("kinesis", "update-account-settings",
		"--minimum-throughput-billing-commitment", "Status=ENABLED"))
	var upd struct {
		MinimumThroughputBillingCommitment struct {
			Status string `json:"Status"`
		} `json:"MinimumThroughputBillingCommitment"`
	}
	parseJSON(t, updJSON, &upd)
	assert.Equal(t, "ENABLED", upd.MinimumThroughputBillingCommitment.Status)

	descJSON := runCLI(t, awsCLI("kinesis", "describe-account-settings"))
	var desc struct {
		MinimumThroughputBillingCommitment struct {
			Status string `json:"Status"`
		} `json:"MinimumThroughputBillingCommitment"`
	}
	parseJSON(t, descJSON, &desc)
	assert.Equal(t, "ENABLED", desc.MinimumThroughputBillingCommitment.Status)

	// Reset to disabled.
	runCLI(t, awsCLI("kinesis", "update-account-settings",
		"--minimum-throughput-billing-commitment", "Status=DISABLED"))
}

// TestKinesisCLI_UpdateMaxRecordSize sets the per-record size and rejects
// out-of-range values.
func TestKinesisCLI_UpdateMaxRecordSize(t *testing.T) {
	stream := "cli-kinesis-max-record-size"
	streamARN := kinesisStreamARNCLI(t, stream)

	runCLI(t, awsCLI("kinesis", "update-max-record-size",
		"--stream-arn", streamARN,
		"--max-record-size-in-ki-b", "2048"))

	runCLIExpectError(t, awsCLI("kinesis", "update-max-record-size",
		"--stream-arn", streamARN,
		"--max-record-size-in-ki-b", "99999"))
}

// TestKinesisCLI_UpdateStreamWarmThroughput sets a warm-throughput target.
func TestKinesisCLI_UpdateStreamWarmThroughput(t *testing.T) {
	stream := "cli-kinesis-warm-throughput"
	streamARN := kinesisStreamARNCLI(t, stream)

	out := runCLI(t, awsCLI("kinesis", "update-stream-warm-throughput",
		"--stream-arn", streamARN,
		"--warm-throughput-mi-bps", "64"))
	var parsed struct {
		StreamName     string `json:"StreamName"`
		WarmThroughput struct {
			TargetMiBps int `json:"TargetMiBps"`
		} `json:"WarmThroughput"`
	}
	parseJSON(t, out, &parsed)
	assert.Equal(t, stream, parsed.StreamName)
	assert.Equal(t, 64, parsed.WarmThroughput.TargetMiBps)
}

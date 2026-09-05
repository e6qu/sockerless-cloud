package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/stretchr/testify/require"
)

func TestFirehoseEncryptedBufferPersistsCiphertextAndDestination(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	kmsKeyMaterial = sim.MakeStore[[]byte](nil, "kms_key_material")
	plaintext := []byte("sensitive Firehose payload")
	record, err := firehoseEncryptBufferedRecord(FirehoseEncryption{
		KeyType: "AWS_OWNED_CMK",
		Status:  "ENABLED",
	}, plaintext)
	require.NoError(t, err)
	require.True(t, record.Encrypted)
	require.NotContains(t, record.Data, plaintext)

	stream := FirehoseDeliveryStream{
		Name:          "durable-stream",
		DestinationID: "destinationId-000000000001",
		S3: FirehoseS3Destination{
			RoleARN:   "arn:aws:iam::123456789012:role/firehose",
			BucketARN: "arn:aws:s3:::durable-bucket",
		},
		Encryption:      FirehoseEncryption{KeyType: "AWS_OWNED_CMK", Status: "ENABLED"},
		Tags:            map[string]string{"environment": "test"},
		BufferedRecords: []FirehoseBufferedRecord{record},
		BufferedBytes:   len(plaintext),
	}
	encoded, err := json.Marshal(stream)
	require.NoError(t, err)
	require.False(t, bytes.Contains(encoded, plaintext), "durable state must not store encrypted buffer plaintext")

	var restored FirehoseDeliveryStream
	require.NoError(t, json.Unmarshal(encoded, &restored))
	require.Equal(t, stream.S3, restored.S3)
	require.Equal(t, stream.Encryption, restored.Encryption)
	require.Equal(t, stream.Tags, restored.Tags)
	require.Equal(t, stream.BufferedBytes, restored.BufferedBytes)
	require.Len(t, restored.BufferedRecords, 1)
	decrypted, err := firehoseDecryptBufferedRecord(restored.BufferedRecords[0])
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted)
}

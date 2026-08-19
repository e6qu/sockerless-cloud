package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/golang/snappy"
)

type FirehoseBufferingHints struct {
	SizeInMBs         int `json:"SizeInMBs"`
	IntervalInSeconds int `json:"IntervalInSeconds"`
}

type FirehoseS3Destination struct {
	RoleARN           string                 `json:"RoleARN"`
	BucketARN         string                 `json:"BucketARN"`
	Prefix            string                 `json:"Prefix,omitempty"`
	ErrorOutputPrefix string                 `json:"ErrorOutputPrefix,omitempty"`
	BufferingHints    FirehoseBufferingHints `json:"BufferingHints"`
	CompressionFormat string                 `json:"CompressionFormat"`
	FileExtension     string                 `json:"FileExtension,omitempty"`
	S3BackupMode      string                 `json:"S3BackupMode"`
}

type FirehoseEncryption struct {
	KeyARN  string `json:"KeyARN,omitempty"`
	KeyType string `json:"KeyType,omitempty"`
	Status  string `json:"Status"`
}

type FirehoseBufferedRecord struct {
	Data      []byte `json:"Data"`
	Encrypted bool   `json:"Encrypted"`
}

const firehoseAWSOwnedKeyID = "aws-owned-firehose"

func firehoseNormalizeEncryption(keyType, keyARN string) (FirehoseEncryption, error) {
	if keyType == "" || keyType == "AWS_OWNED_CMK" {
		return FirehoseEncryption{KeyType: "AWS_OWNED_CMK", Status: "ENABLED"}, nil
	}
	if keyType != "CUSTOMER_MANAGED_CMK" || keyARN == "" {
		return FirehoseEncryption{}, fmt.Errorf("a customer-managed encryption key requires KeyARN")
	}
	keyID, ok := resolveKMSKey(keyARN)
	if !ok {
		return FirehoseEncryption{}, fmt.Errorf("KMS key %q does not exist", keyARN)
	}
	key, ok := kmsKeys.Get(keyID)
	if !ok || key.KeyState != "Enabled" {
		return FirehoseEncryption{}, fmt.Errorf("KMS key %q is not enabled", keyARN)
	}
	return FirehoseEncryption{KeyARN: key.Arn, KeyType: keyType, Status: "ENABLED"}, nil
}

func firehoseEncryptBufferedRecord(encryption FirehoseEncryption, plaintext []byte) (FirehoseBufferedRecord, error) {
	if encryption.Status != "ENABLED" {
		return FirehoseBufferedRecord{Data: append([]byte(nil), plaintext...)}, nil
	}
	keyID := firehoseAWSOwnedKeyID
	if encryption.KeyType == "CUSTOMER_MANAGED_CMK" {
		var ok bool
		keyID, ok = resolveKMSKey(encryption.KeyARN)
		if !ok {
			return FirehoseBufferedRecord{}, fmt.Errorf("KMS key %q does not exist", encryption.KeyARN)
		}
	} else if _, ok := kmsGetKeyMaterial(keyID); !ok {
		if _, err := kmsGenerateKeyMaterial(keyID); err != nil {
			return FirehoseBufferedRecord{}, fmt.Errorf("AWS owned KMS key material could not be generated: %w", err)
		}
	}
	ciphertext, ok := kmsEncryptBytes(keyID, plaintext)
	if !ok {
		return FirehoseBufferedRecord{}, fmt.Errorf("KMS key %q has no usable key material", keyID)
	}
	return FirehoseBufferedRecord{Data: ciphertext, Encrypted: true}, nil
}

func firehoseDecryptBufferedRecord(record FirehoseBufferedRecord) ([]byte, error) {
	if !record.Encrypted {
		return append([]byte(nil), record.Data...), nil
	}
	_, plaintext, ok := kmsDecryptBytes(record.Data)
	if !ok {
		return nil, fmt.Errorf("buffered record could not be decrypted")
	}
	return plaintext, nil
}

type FirehoseDeliveryStream struct {
	Name            string                   `json:"DeliveryStreamName"`
	ARN             string                   `json:"DeliveryStreamARN"`
	Status          string                   `json:"DeliveryStreamStatus"`
	Type            string                   `json:"DeliveryStreamType"`
	VersionID       string                   `json:"VersionId"`
	CreatedAt       float64                  `json:"CreateTimestamp"`
	UpdatedAt       float64                  `json:"LastUpdateTimestamp"`
	DestinationID   string                   `json:"DestinationID"`
	S3              FirehoseS3Destination    `json:"S3"`
	Encryption      FirehoseEncryption       `json:"Encryption"`
	Tags            map[string]string        `json:"Tags"`
	BufferedRecords []FirehoseBufferedRecord `json:"BufferedRecords,omitempty"`
	BufferedBytes   int                      `json:"BufferedBytes,omitempty"`
	BufferDeadline  time.Time                `json:"BufferDeadline,omitempty"`
}

type firehoseS3Create struct {
	RoleARN                 string                  `json:"RoleARN"`
	BucketARN               string                  `json:"BucketARN"`
	Prefix                  string                  `json:"Prefix"`
	ErrorOutputPrefix       string                  `json:"ErrorOutputPrefix"`
	BufferingHints          *FirehoseBufferingHints `json:"BufferingHints"`
	CompressionFormat       string                  `json:"CompressionFormat"`
	ProcessingConfiguration struct {
		Enabled bool `json:"Enabled"`
	} `json:"ProcessingConfiguration"`
	DynamicPartitioningConfiguration struct {
		Enabled bool `json:"Enabled"`
	} `json:"DynamicPartitioningConfiguration"`
	FileExtension string `json:"FileExtension"`
	S3BackupMode  string `json:"S3BackupMode"`
}

type firehoseTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value,omitempty"`
}

var (
	firehoseStreams   sim.Store[FirehoseDeliveryStream]
	firehoseRecordsMu sync.Mutex
	firehoseTimersMu  sync.Mutex
	firehoseTimers    = map[string]*simTimer{}
)

func registerFirehose(r *sim.AWSRouter, srv *sim.Server) {
	firehoseStreams = sim.MakeStore[FirehoseDeliveryStream](srv.DB(), "firehose_delivery_streams")
	r.Register("Firehose_20150804.CreateDeliveryStream", handleFirehoseCreateDeliveryStream)
	r.Register("Firehose_20150804.DeleteDeliveryStream", handleFirehoseDeleteDeliveryStream)
	r.Register("Firehose_20150804.DescribeDeliveryStream", handleFirehoseDescribeDeliveryStream)
	r.Register("Firehose_20150804.ListDeliveryStreams", handleFirehoseListDeliveryStreams)
	r.Register("Firehose_20150804.ListTagsForDeliveryStream", handleFirehoseListTags)
	r.Register("Firehose_20150804.PutRecord", handleFirehosePutRecord)
	r.Register("Firehose_20150804.PutRecordBatch", handleFirehosePutRecordBatch)
	r.Register("Firehose_20150804.StartDeliveryStreamEncryption", handleFirehoseStartEncryption)
	r.Register("Firehose_20150804.StopDeliveryStreamEncryption", handleFirehoseStopEncryption)
	r.Register("Firehose_20150804.TagDeliveryStream", handleFirehoseTagDeliveryStream)
	r.Register("Firehose_20150804.UntagDeliveryStream", handleFirehoseUntagDeliveryStream)
	r.Register("Firehose_20150804.UpdateDestination", handleFirehoseUpdateDestination)

	for _, stream := range firehoseStreams.List() {
		if len(stream.BufferedRecords) > 0 && !stream.BufferDeadline.IsZero() {
			firehoseScheduleFlush(stream.Name, time.Until(stream.BufferDeadline))
		}
	}
}

func firehoseARN(name string) string {
	return "arn:aws:firehose:" + awsRegion() + ":" + awsAccountID() + ":deliverystream/" + name
}

func firehoseStreamByARN(arn string) (FirehoseDeliveryStream, bool) {
	for _, stream := range firehoseStreams.List() {
		if stream.ARN == arn {
			return stream, true
		}
	}
	return FirehoseDeliveryStream{}, false
}

func firehosePutServiceRecord(arn string, data []byte) error {
	stream, ok := firehoseStreamByARN(arn)
	if !ok {
		return fmt.Errorf("delivery stream %q does not exist", arn)
	}
	_, _, err := firehoseAddRecord(stream.Name, data)
	return err
}

func firehoseBucketName(arn string) string {
	return strings.TrimPrefix(arn, "arn:aws:s3:::")
}

func firehoseError(w http.ResponseWriter, code, message string) {
	sim.AWSError(w, code, message, http.StatusBadRequest)
}

func firehoseGet(w http.ResponseWriter, name string) (FirehoseDeliveryStream, bool) {
	stream, ok := firehoseStreams.Get(name)
	if !ok {
		firehoseError(w, "ResourceNotFoundException", "Firehose "+name+" under account "+awsAccountID()+" not found.")
	}
	return stream, ok
}

func firehoseNormalizeS3(input firehoseS3Create) (FirehoseS3Destination, error) {
	if input.BucketARN == "" || !strings.HasPrefix(input.BucketARN, "arn:aws:s3:::") {
		return FirehoseS3Destination{}, fmt.Errorf("BucketARN must identify an Amazon S3 bucket")
	}
	bucketName := firehoseBucketName(input.BucketARN)
	if _, ok := s3Buckets_.Get(bucketName); !ok {
		return FirehoseS3Destination{}, fmt.Errorf("the destination bucket %q does not exist", bucketName)
	}
	if input.RoleARN == "" {
		return FirehoseS3Destination{}, fmt.Errorf("RoleARN is required")
	}
	if err := iamValidateServiceRole(input.RoleARN, "firehose.amazonaws.com", map[string]string{
		"s3:GetBucketLocation": input.BucketARN,
		"s3:ListBucket":        input.BucketARN,
		"s3:PutObject":         input.BucketARN + "/*",
	}); err != nil {
		return FirehoseS3Destination{}, err
	}
	if input.ProcessingConfiguration.Enabled {
		return FirehoseS3Destination{}, fmt.Errorf("an enabled processing configuration requires a configured AWS Lambda processor")
	}
	if input.DynamicPartitioningConfiguration.Enabled {
		return FirehoseS3Destination{}, fmt.Errorf("dynamic partitioning requires an enabled record processor")
	}
	hints := FirehoseBufferingHints{SizeInMBs: 5, IntervalInSeconds: 300}
	if input.BufferingHints != nil {
		if input.BufferingHints.SizeInMBs < 1 || input.BufferingHints.SizeInMBs > 128 ||
			input.BufferingHints.IntervalInSeconds < 0 || input.BufferingHints.IntervalInSeconds > 900 {
			return FirehoseS3Destination{}, fmt.Errorf("BufferingHints values are outside the documented range")
		}
		hints = *input.BufferingHints
	}
	compression := input.CompressionFormat
	if compression == "" {
		compression = "UNCOMPRESSED"
	}
	if compression == "SNAPPY" {
		compression = "Snappy"
	}
	switch compression {
	case "UNCOMPRESSED", "GZIP", "ZIP", "Snappy", "HADOOP_SNAPPY":
	default:
		return FirehoseS3Destination{}, fmt.Errorf("compression format %q is not available for this destination", compression)
	}
	return FirehoseS3Destination{
		RoleARN:           input.RoleARN,
		BucketARN:         input.BucketARN,
		Prefix:            input.Prefix,
		ErrorOutputPrefix: input.ErrorOutputPrefix,
		BufferingHints:    hints,
		CompressionFormat: compression,
		FileExtension:     input.FileExtension,
		S3BackupMode:      firstNonEmpty(input.S3BackupMode, "Disabled"),
	}, nil
}

func handleFirehoseCreateDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName                         string            `json:"DeliveryStreamName"`
		DeliveryStreamType                         string            `json:"DeliveryStreamType"`
		ExtendedS3DestinationConfiguration         *firehoseS3Create `json:"ExtendedS3DestinationConfiguration"`
		S3DestinationConfiguration                 *firehoseS3Create `json:"S3DestinationConfiguration"`
		Tags                                       []firehoseTag     `json:"Tags"`
		DeliveryStreamEncryptionConfigurationInput *struct {
			KeyARN  string `json:"KeyARN"`
			KeyType string `json:"KeyType"`
		} `json:"DeliveryStreamEncryptionConfigurationInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	if req.DeliveryStreamName == "" {
		firehoseError(w, "InvalidArgumentException", "DeliveryStreamName is required.")
		return
	}
	if _, exists := firehoseStreams.Get(req.DeliveryStreamName); exists {
		firehoseError(w, "ResourceInUseException", "Firehose "+req.DeliveryStreamName+" already exists.")
		return
	}
	streamType := req.DeliveryStreamType
	if streamType == "" {
		streamType = "DirectPut"
	}
	if streamType != "DirectPut" {
		firehoseError(w, "InvalidArgumentException", "This Firehose stream requires a source configuration for "+streamType+".")
		return
	}
	destination := req.ExtendedS3DestinationConfiguration
	if destination == nil {
		destination = req.S3DestinationConfiguration
	}
	if destination == nil {
		firehoseError(w, "InvalidArgumentException", "Exactly one supported destination configuration is required.")
		return
	}
	s3, err := firehoseNormalizeS3(*destination)
	if err != nil {
		firehoseError(w, "InvalidArgumentException", err.Error())
		return
	}
	now := float64(time.Now().UTC().UnixMilli()) / 1000
	stream := FirehoseDeliveryStream{
		Name:          req.DeliveryStreamName,
		ARN:           firehoseARN(req.DeliveryStreamName),
		Status:        "ACTIVE",
		Type:          streamType,
		VersionID:     "1",
		CreatedAt:     now,
		UpdatedAt:     now,
		DestinationID: "destinationId-000000000001",
		S3:            s3,
		Encryption:    FirehoseEncryption{Status: "DISABLED"},
		Tags:          map[string]string{},
	}
	if req.DeliveryStreamEncryptionConfigurationInput != nil {
		encryption, err := firehoseNormalizeEncryption(
			req.DeliveryStreamEncryptionConfigurationInput.KeyType,
			req.DeliveryStreamEncryptionConfigurationInput.KeyARN,
		)
		if err != nil {
			firehoseError(w, "InvalidArgumentException", err.Error())
			return
		}
		stream.Encryption = encryption
	}
	for _, tag := range req.Tags {
		stream.Tags[tag.Key] = tag.Value
	}
	firehoseStreams.Put(stream.Name, stream)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"DeliveryStreamARN": stream.ARN})
}

func handleFirehoseDeleteDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName string `json:"DeliveryStreamName"`
		AllowForceDelete   bool   `json:"AllowForceDelete"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	if stream, ok := firehoseStreams.Get(req.DeliveryStreamName); ok && len(stream.BufferedRecords) > 0 && !req.AllowForceDelete {
		firehoseError(w, "ResourceInUseException", "The Firehose stream still has buffered records.")
		return
	}
	firehoseCancelTimer(req.DeliveryStreamName)
	firehoseStreams.Delete(req.DeliveryStreamName)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func firehoseDescription(stream FirehoseDeliveryStream) map[string]any {
	s3 := map[string]any{
		"RoleARN": stream.S3.RoleARN, "BucketARN": stream.S3.BucketARN,
		"BufferingHints":          stream.S3.BufferingHints,
		"CompressionFormat":       stream.S3.CompressionFormat,
		"S3BackupMode":            stream.S3.S3BackupMode,
		"EncryptionConfiguration": map[string]any{"NoEncryptionConfig": "NoEncryption"},
	}
	if stream.S3.Prefix != "" {
		s3["Prefix"] = stream.S3.Prefix
	}
	if stream.S3.ErrorOutputPrefix != "" {
		s3["ErrorOutputPrefix"] = stream.S3.ErrorOutputPrefix
	}
	if stream.S3.FileExtension != "" {
		s3["FileExtension"] = stream.S3.FileExtension
	}
	out := map[string]any{
		"DeliveryStreamName":   stream.Name,
		"DeliveryStreamARN":    stream.ARN,
		"DeliveryStreamStatus": stream.Status,
		"DeliveryStreamType":   stream.Type,
		"VersionId":            stream.VersionID,
		"CreateTimestamp":      stream.CreatedAt,
		"LastUpdateTimestamp":  stream.UpdatedAt,
		"Destinations": []map[string]any{{
			"DestinationId":                    stream.DestinationID,
			"ExtendedS3DestinationDescription": s3,
		}},
		"HasMoreDestinations":                   false,
		"DeliveryStreamEncryptionConfiguration": stream.Encryption,
	}
	return out
}

func handleFirehoseDescribeDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName string `json:"DeliveryStreamName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	stream, ok := firehoseGet(w, req.DeliveryStreamName)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"DeliveryStreamDescription": firehoseDescription(stream)})
}

func handleFirehoseListDeliveryStreams(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExclusiveStartDeliveryStreamName string `json:"ExclusiveStartDeliveryStreamName"`
		Limit                            int    `json:"Limit"`
		DeliveryStreamType               string `json:"DeliveryStreamType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	var names []string
	for _, stream := range firehoseStreams.List() {
		if req.DeliveryStreamType == "" || stream.Type == req.DeliveryStreamType {
			names = append(names, stream.Name)
		}
	}
	sort.Strings(names)
	start := 0
	if req.ExclusiveStartDeliveryStreamName != "" {
		start = sort.SearchStrings(names, req.ExclusiveStartDeliveryStreamName)
		for start < len(names) && names[start] <= req.ExclusiveStartDeliveryStreamName {
			start++
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	end := min(start+limit, len(names))
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"DeliveryStreamNames":    names[start:end],
		"HasMoreDeliveryStreams": end < len(names),
	})
}

func handleFirehoseListTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName   string `json:"DeliveryStreamName"`
		ExclusiveStartTagKey string `json:"ExclusiveStartTagKey"`
		Limit                int    `json:"Limit"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	stream, ok := firehoseGet(w, req.DeliveryStreamName)
	if !ok {
		return
	}
	keys := make([]string, 0, len(stream.Tags))
	for key := range stream.Tags {
		if key > req.ExclusiveStartTagKey {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	limit := req.Limit
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	end := min(limit, len(keys))
	tags := make([]firehoseTag, 0, end)
	for _, key := range keys[:end] {
		tags = append(tags, firehoseTag{Key: key, Value: stream.Tags[key]})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Tags": tags, "HasMoreTags": end < len(keys)})
}

func firehoseAddRecord(name string, data []byte) (string, bool, error) {
	if len(data) == 0 || len(data) > 1000*1024 {
		return "", false, fmt.Errorf("record data must contain between 1 and 1,024,000 bytes")
	}
	firehoseRecordsMu.Lock()
	defer firehoseRecordsMu.Unlock()

	stream, ok := firehoseStreams.Get(name)
	if !ok {
		return "", false, fmt.Errorf("not found")
	}
	if stream.Encryption.Status == "ENABLED" {
		if _, err := firehoseNormalizeEncryption(stream.Encryption.KeyType, stream.Encryption.KeyARN); err != nil {
			return "", true, err
		}
	}
	buffered, err := firehoseEncryptBufferedRecord(stream.Encryption, data)
	if err != nil {
		return "", stream.Encryption.Status == "ENABLED", err
	}
	recordID := generateUUID()
	if len(stream.BufferedRecords) == 0 {
		stream.BufferDeadline = time.Now().Add(time.Duration(stream.S3.BufferingHints.IntervalInSeconds) * time.Second)
	}
	stream.BufferedRecords = append(stream.BufferedRecords, buffered)
	stream.BufferedBytes += len(data)
	firehoseStreams.Put(name, stream)
	threshold := stream.S3.BufferingHints.SizeInMBs * 1024 * 1024
	if stream.S3.BufferingHints.IntervalInSeconds == 0 || stream.BufferedBytes >= threshold {
		if err := firehoseFlushLocked(name); err != nil {
			return "", stream.Encryption.Status == "ENABLED", err
		}
	} else {
		firehoseScheduleFlush(name, time.Until(stream.BufferDeadline))
	}
	return recordID, stream.Encryption.Status == "ENABLED", nil
}

func handleFirehosePutRecord(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName string `json:"DeliveryStreamName"`
		Record             struct {
			Data []byte `json:"Data"`
		} `json:"Record"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	id, encrypted, err := firehoseAddRecord(req.DeliveryStreamName, req.Record.Data)
	if err != nil {
		code := "InvalidArgumentException"
		if err.Error() == "not found" {
			code = "ResourceNotFoundException"
		} else if strings.Contains(err.Error(), "delivery failed") {
			code = "ServiceUnavailableException"
		}
		firehoseError(w, code, err.Error())
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"RecordId": id, "Encrypted": encrypted})
}

func handleFirehosePutRecordBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName string `json:"DeliveryStreamName"`
		Records            []struct {
			Data []byte `json:"Data"`
		} `json:"Records"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	responses := make([]map[string]any, 0, len(req.Records))
	failed := 0
	encrypted := false
	for _, record := range req.Records {
		id, enc, err := firehoseAddRecord(req.DeliveryStreamName, record.Data)
		encrypted = encrypted || enc
		if err != nil {
			failed++
			responses = append(responses, map[string]any{"ErrorCode": "ServiceUnavailableException", "ErrorMessage": err.Error()})
			continue
		}
		responses = append(responses, map[string]any{"RecordId": id})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"FailedPutCount": failed, "Encrypted": encrypted, "RequestResponses": responses,
	})
}

func firehoseScheduleFlush(name string, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	firehoseTimersMu.Lock()
	defer firehoseTimersMu.Unlock()
	if _, exists := firehoseTimers[name]; exists {
		return
	}
	firehoseTimers[name] = simAfterFunc(delay, func() {
		firehoseTimersMu.Lock()
		delete(firehoseTimers, name)
		firehoseTimersMu.Unlock()
		_ = firehoseFlush(name)
	})
}

func firehoseCancelTimer(name string) {
	firehoseTimersMu.Lock()
	if timer := firehoseTimers[name]; timer != nil {
		timer.Stop()
		delete(firehoseTimers, name)
	}
	firehoseTimersMu.Unlock()
}

func firehoseFlush(name string) error {
	firehoseRecordsMu.Lock()
	defer firehoseRecordsMu.Unlock()
	return firehoseFlushLocked(name)
}

func firehoseFlushLocked(name string) error {
	firehoseCancelTimer(name)
	stream, ok := firehoseStreams.Get(name)
	if !ok || len(stream.BufferedRecords) == 0 {
		return nil
	}
	if err := iamValidateServiceRole(stream.S3.RoleARN, "firehose.amazonaws.com", map[string]string{
		"s3:GetBucketLocation": stream.S3.BucketARN,
		"s3:ListBucket":        stream.S3.BucketARN,
		"s3:PutObject":         stream.S3.BucketARN + "/*",
	}); err != nil {
		return fmt.Errorf("delivery failed: %w", err)
	}
	var raw bytes.Buffer
	for _, record := range stream.BufferedRecords {
		data, err := firehoseDecryptBufferedRecord(record)
		if err != nil {
			return fmt.Errorf("delivery failed: %w", err)
		}
		_, _ = raw.Write(data)
	}
	data := raw.Bytes()
	extension := stream.S3.FileExtension
	switch stream.S3.CompressionFormat {
	case "GZIP":
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(data); err != nil {
			return fmt.Errorf("delivery failed: %w", err)
		}
		if err := writer.Close(); err != nil {
			return fmt.Errorf("delivery failed: %w", err)
		}
		data = compressed.Bytes()
		if extension == "" {
			extension = ".gz"
		}
	case "ZIP":
		var compressed bytes.Buffer
		writer := zip.NewWriter(&compressed)
		entry, err := writer.Create("records")
		if err != nil {
			return fmt.Errorf("delivery failed: %w", err)
		}
		if _, err := entry.Write(data); err != nil {
			return fmt.Errorf("delivery failed: %w", err)
		}
		if err := writer.Close(); err != nil {
			return fmt.Errorf("delivery failed: %w", err)
		}
		data = compressed.Bytes()
		if extension == "" {
			extension = ".zip"
		}
	case "Snappy":
		var compressed bytes.Buffer
		writer := snappy.NewBufferedWriter(&compressed)
		if _, err := writer.Write(data); err != nil {
			return fmt.Errorf("delivery failed: %w", err)
		}
		if err := writer.Close(); err != nil {
			return fmt.Errorf("delivery failed: %w", err)
		}
		data = compressed.Bytes()
		if extension == "" {
			extension = ".snappy"
		}
	case "HADOOP_SNAPPY":
		data = firehoseHadoopSnappy(data)
		if extension == "" {
			extension = ".snappy"
		}
	}
	now := time.Now().UTC()
	prefix := stream.S3.Prefix
	if prefix == "" {
		prefix = now.Format("2006/01/02/15/")
	}
	key := path.Clean(prefix + "/" + name + "-" + strconv.FormatInt(now.UnixNano(), 10) + extension)
	key = strings.TrimPrefix(key, "/")
	if _, err := s3PutServiceObject(firehoseBucketName(stream.S3.BucketARN), key, data, "application/octet-stream", map[string]string{
		"firehose-delivery-stream": name,
	}); err != nil {
		return fmt.Errorf("delivery failed: %w", err)
	}
	stream.BufferedRecords = nil
	stream.BufferedBytes = 0
	stream.BufferDeadline = time.Time{}
	stream.UpdatedAt = float64(now.UnixMilli()) / 1000
	firehoseStreams.Put(name, stream)
	return nil
}

// firehoseHadoopSnappy emits the framing used by Hadoop's SnappyCodec: each
// block starts with its uncompressed length and contains one or more raw
// Snappy chunks prefixed by their compressed length, all big-endian uint32.
func firehoseHadoopSnappy(data []byte) []byte {
	const blockSize = 64 * 1024
	var out bytes.Buffer
	for len(data) > 0 {
		size := min(blockSize, len(data))
		block := data[:size]
		data = data[size:]
		compressed := snappy.Encode(nil, block)
		_ = binary.Write(&out, binary.BigEndian, uint32(len(block)))
		_ = binary.Write(&out, binary.BigEndian, uint32(len(compressed)))
		_, _ = out.Write(compressed)
	}
	return out.Bytes()
}

func handleFirehoseStartEncryption(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName                         string `json:"DeliveryStreamName"`
		DeliveryStreamEncryptionConfigurationInput struct {
			KeyARN  string `json:"KeyARN"`
			KeyType string `json:"KeyType"`
		} `json:"DeliveryStreamEncryptionConfigurationInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	if _, ok := firehoseGet(w, req.DeliveryStreamName); !ok {
		return
	}
	encryption, err := firehoseNormalizeEncryption(
		req.DeliveryStreamEncryptionConfigurationInput.KeyType,
		req.DeliveryStreamEncryptionConfigurationInput.KeyARN,
	)
	if err != nil {
		firehoseError(w, "InvalidArgumentException", err.Error())
		return
	}
	firehoseStreams.Update(req.DeliveryStreamName, func(stream *FirehoseDeliveryStream) {
		stream.Encryption = encryption
		stream.UpdatedAt = float64(time.Now().UTC().UnixMilli()) / 1000
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleFirehoseStopEncryption(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName string `json:"DeliveryStreamName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	if _, ok := firehoseGet(w, req.DeliveryStreamName); !ok {
		return
	}
	firehoseStreams.Update(req.DeliveryStreamName, func(stream *FirehoseDeliveryStream) {
		stream.Encryption = FirehoseEncryption{Status: "DISABLED"}
		stream.UpdatedAt = float64(time.Now().UTC().UnixMilli()) / 1000
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleFirehoseTagDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName string        `json:"DeliveryStreamName"`
		Tags               []firehoseTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	if _, ok := firehoseGet(w, req.DeliveryStreamName); !ok {
		return
	}
	firehoseStreams.Update(req.DeliveryStreamName, func(stream *FirehoseDeliveryStream) {
		if stream.Tags == nil {
			stream.Tags = map[string]string{}
		}
		for _, tag := range req.Tags {
			stream.Tags[tag.Key] = tag.Value
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleFirehoseUntagDeliveryStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName string   `json:"DeliveryStreamName"`
		TagKeys            []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	if _, ok := firehoseGet(w, req.DeliveryStreamName); !ok {
		return
	}
	firehoseStreams.Update(req.DeliveryStreamName, func(stream *FirehoseDeliveryStream) {
		for _, key := range req.TagKeys {
			delete(stream.Tags, key)
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleFirehoseUpdateDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryStreamName             string            `json:"DeliveryStreamName"`
		CurrentDeliveryStreamVersionID string            `json:"CurrentDeliveryStreamVersionId"`
		DestinationID                  string            `json:"DestinationId"`
		ExtendedS3DestinationUpdate    *firehoseS3Create `json:"ExtendedS3DestinationUpdate"`
		S3DestinationUpdate            *firehoseS3Create `json:"S3DestinationUpdate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		firehoseError(w, "InvalidArgumentException", "The request body could not be parsed.")
		return
	}
	stream, ok := firehoseGet(w, req.DeliveryStreamName)
	if !ok {
		return
	}
	if req.CurrentDeliveryStreamVersionID != stream.VersionID || req.DestinationID != stream.DestinationID {
		firehoseError(w, "ConcurrentModificationException", "The delivery stream version or destination changed.")
		return
	}
	update := req.ExtendedS3DestinationUpdate
	if update == nil {
		update = req.S3DestinationUpdate
	}
	if update == nil {
		firehoseError(w, "InvalidArgumentException", "A supported destination update is required.")
		return
	}
	merged := stream.S3
	if update.RoleARN != "" {
		merged.RoleARN = update.RoleARN
	}
	if update.BucketARN != "" {
		merged.BucketARN = update.BucketARN
	}
	if update.Prefix != "" {
		merged.Prefix = update.Prefix
	}
	if update.ErrorOutputPrefix != "" {
		merged.ErrorOutputPrefix = update.ErrorOutputPrefix
	}
	if update.BufferingHints != nil {
		merged.BufferingHints = *update.BufferingHints
	}
	if update.CompressionFormat != "" {
		merged.CompressionFormat = update.CompressionFormat
	}
	if update.FileExtension != "" {
		merged.FileExtension = update.FileExtension
	}
	normalized, err := firehoseNormalizeS3(firehoseS3Create{
		RoleARN: merged.RoleARN, BucketARN: merged.BucketARN, Prefix: merged.Prefix,
		ErrorOutputPrefix: merged.ErrorOutputPrefix, BufferingHints: &merged.BufferingHints,
		CompressionFormat: merged.CompressionFormat, FileExtension: merged.FileExtension,
	})
	if err != nil {
		firehoseError(w, "InvalidArgumentException", err.Error())
		return
	}
	version, _ := strconv.Atoi(stream.VersionID)
	stream.VersionID = strconv.Itoa(version + 1)
	stream.S3 = normalized
	stream.UpdatedAt = float64(time.Now().UTC().UnixMilli()) / 1000
	firehoseStreams.Put(stream.Name, stream)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

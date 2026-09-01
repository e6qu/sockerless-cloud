package main

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

type KinesisStream struct {
	StreamName           string            `json:"StreamName"`
	StreamARN            string            `json:"StreamARN"`
	StreamStatus         string            `json:"StreamStatus"`
	StreamModeDetails    map[string]string `json:"StreamModeDetails,omitempty"`
	Shards               []KinesisShard    `json:"Shards"`
	RetentionPeriodHours int64             `json:"RetentionPeriodHours"`
	EnhancedMonitoring   []map[string]any  `json:"EnhancedMonitoring"`
	EncryptionType       string            `json:"EncryptionType,omitempty"`
	KeyId                string            `json:"KeyId,omitempty"`
	CreationTimestamp    float64           `json:"-"`
	Tags                 map[string]string `json:"-"`
	OpenShardCount       int64             `json:"-"`
	MaxRecordSizeInKiB   int64             `json:"-"`
	WarmThroughputMiBps  int64             `json:"-"`
}

type KinesisShard struct {
	ShardId             string            `json:"ShardId"`
	HashKeyRange        map[string]string `json:"HashKeyRange"`
	SequenceNumberRange map[string]string `json:"SequenceNumberRange"`
	ParentShardId       string            `json:"ParentShardId,omitempty"`
}

type kinesisRecord struct {
	SequenceNumber              string
	ApproximateArrivalTimestamp float64
	Data                        []byte
	PartitionKey                string
	ExplicitHashKey             string
}

type kinesisIterator struct {
	StreamName string
	ShardID    string
	Index      int
}

// KinesisConsumer is an enhanced fan-out consumer registered against a stream
// ARN. Its ARN embeds the creation timestamp
// (arn:...:stream/Name/consumer/CName:<ts>) exactly as real Kinesis does, so
// re-registering after a delete yields a distinct ARN.
type KinesisConsumer struct {
	ConsumerName              string  `json:"ConsumerName"`
	ConsumerARN               string  `json:"ConsumerARN"`
	ConsumerStatus            string  `json:"ConsumerStatus"`
	ConsumerCreationTimestamp float64 `json:"ConsumerCreationTimestamp"`
	StreamARN                 string  `json:"StreamARN"`
}

// KinesisAccountSettings holds the account-level minimum-throughput billing
// commitment (DescribeAccountSettings / UpdateAccountSettings). A single row
// keyed by the account id.
type KinesisAccountSettings struct {
	Status               string  `json:"Status"`
	StartedAt            float64 `json:"StartedAt"`
	EarliestAllowedEndAt float64 `json:"EarliestAllowedEndAt"`
	EndedAt              float64 `json:"EndedAt"`
}

var (
	kinesisStreams   sim.Store[KinesisStream]
	kinesisRecords   sim.Store[[]kinesisRecord]
	kinesisIterators sim.Store[kinesisIterator]
	kinesisConsumers sim.Store[KinesisConsumer]
	kinesisAccount   sim.Store[KinesisAccountSettings]
	kinesisMu        sync.Mutex
)

func registerKinesis(r *sim.AWSRouter, srv *sim.Server) {
	// Record-level ops are CloudTrail DATA events (excluded from LookupEvents).
	cloudTrailDeclareDataEvents("kinesis.amazonaws.com",
		"PutRecord", "PutRecords", "GetRecords", "GetShardIterator", "SubscribeToShard")
	kinesisStreams = sim.MakeStore[KinesisStream](srv.DB(), "kinesis_streams")
	kinesisRecords = sim.MakeStore[[]kinesisRecord](srv.DB(), "kinesis_records")
	kinesisIterators = sim.MakeStore[kinesisIterator](srv.DB(), "kinesis_iterators")
	kinesisConsumers = sim.MakeStore[KinesisConsumer](srv.DB(), "kinesis_consumers")
	kinesisAccount = sim.MakeStore[KinesisAccountSettings](srv.DB(), "kinesis_account_settings")

	registerKinesisChannels(r, srv)

	r.Register("Kinesis_20131202.CreateStream", handleKinesisCreateStream)
	r.Register("Kinesis_20131202.DeleteStream", handleKinesisDeleteStream)
	r.Register("Kinesis_20131202.DescribeStream", handleKinesisDescribeStream)
	r.Register("Kinesis_20131202.DescribeStreamSummary", handleKinesisDescribeStreamSummary)
	r.Register("Kinesis_20131202.ListStreams", handleKinesisListStreams)
	r.Register("Kinesis_20131202.ListShards", handleKinesisListShards)
	r.Register("Kinesis_20131202.PutRecord", handleKinesisPutRecord)
	r.Register("Kinesis_20131202.PutRecords", handleKinesisPutRecords)
	r.Register("Kinesis_20131202.GetShardIterator", handleKinesisGetShardIterator)
	r.Register("Kinesis_20131202.GetRecords", handleKinesisGetRecords)
	r.Register("Kinesis_20131202.AddTagsToStream", handleKinesisAddTagsToStream)
	r.Register("Kinesis_20131202.RemoveTagsFromStream", handleKinesisRemoveTagsFromStream)
	r.Register("Kinesis_20131202.ListTagsForStream", handleKinesisListTagsForStream)
	r.Register("Kinesis_20131202.IncreaseStreamRetentionPeriod", handleKinesisIncreaseStreamRetentionPeriod)
	r.Register("Kinesis_20131202.DecreaseStreamRetentionPeriod", handleKinesisDecreaseStreamRetentionPeriod)
	r.Register("Kinesis_20131202.EnableEnhancedMonitoring", handleKinesisEnableEnhancedMonitoring)
	r.Register("Kinesis_20131202.DisableEnhancedMonitoring", handleKinesisDisableEnhancedMonitoring)
	r.Register("Kinesis_20131202.StartStreamEncryption", handleKinesisStartStreamEncryption)
	r.Register("Kinesis_20131202.StopStreamEncryption", handleKinesisStopStreamEncryption)
	r.Register("Kinesis_20131202.UpdateShardCount", handleKinesisUpdateShardCount)
	r.Register("Kinesis_20131202.DescribeLimits", handleKinesisDescribeLimits)
	r.Register("Kinesis_20131202.RegisterStreamConsumer", handleKinesisRegisterStreamConsumer)
	r.Register("Kinesis_20131202.DeregisterStreamConsumer", handleKinesisDeregisterStreamConsumer)
	r.Register("Kinesis_20131202.DescribeStreamConsumer", handleKinesisDescribeStreamConsumer)
	r.Register("Kinesis_20131202.ListStreamConsumers", handleKinesisListStreamConsumers)
	r.Register("Kinesis_20131202.PutResourcePolicy", handleKinesisPutResourcePolicy)
	r.Register("Kinesis_20131202.GetResourcePolicy", handleKinesisGetResourcePolicy)
	r.Register("Kinesis_20131202.DeleteResourcePolicy", handleKinesisDeleteResourcePolicy)
	r.Register("Kinesis_20131202.MergeShards", handleKinesisMergeShards)
	r.Register("Kinesis_20131202.SplitShard", handleKinesisSplitShard)
	r.Register("Kinesis_20131202.TagResource", handleKinesisTagResource)
	r.Register("Kinesis_20131202.UntagResource", handleKinesisUntagResource)
	r.Register("Kinesis_20131202.ListTagsForResource", handleKinesisListTagsForResource)
	r.Register("Kinesis_20131202.UpdateStreamMode", handleKinesisUpdateStreamMode)
	r.Register("Kinesis_20131202.DescribeAccountSettings", handleKinesisDescribeAccountSettings)
	r.Register("Kinesis_20131202.UpdateAccountSettings", handleKinesisUpdateAccountSettings)
	r.Register("Kinesis_20131202.UpdateMaxRecordSize", handleKinesisUpdateMaxRecordSize)
	r.Register("Kinesis_20131202.UpdateStreamWarmThroughput", handleKinesisUpdateStreamWarmThroughput)
}

func writeKinesisJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func kinesisStreamARN(name string) string {
	return fmt.Sprintf("arn:aws:kinesis:%s:%s:stream/%s", awsRegion(), awsAccountID(), name)
}

func kinesisShardRecordKey(streamName, shardID string) string {
	return streamName + "/" + shardID
}

func kinesisMakeShards(count int64) []KinesisShard {
	if count < 1 {
		count = 1
	}
	maxHash := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	denom := big.NewInt(count)
	shards := make([]KinesisShard, 0, count)
	for i := int64(0); i < count; i++ {
		start := new(big.Int).Div(new(big.Int).Mul(maxHash, big.NewInt(i)), denom)
		end := new(big.Int).Div(new(big.Int).Mul(maxHash, big.NewInt(i+1)), denom)
		if i > 0 {
			start.Add(start, big.NewInt(1))
		}
		shards = append(shards, KinesisShard{
			ShardId: fmt.Sprintf("shardId-%012d", i),
			HashKeyRange: map[string]string{
				"StartingHashKey": start.String(),
				"EndingHashKey":   end.String(),
			},
			SequenceNumberRange: map[string]string{
				"StartingSequenceNumber": "1",
			},
		})
	}
	return shards
}

func kinesisStreamDescription(s KinesisStream) map[string]any {
	out := map[string]any{
		"StreamName":              s.StreamName,
		"StreamARN":               s.StreamARN,
		"StreamStatus":            s.StreamStatus,
		"StreamModeDetails":       s.StreamModeDetails,
		"Shards":                  s.Shards,
		"HasMoreShards":           false,
		"RetentionPeriodHours":    s.RetentionPeriodHours,
		"StreamCreationTimestamp": s.CreationTimestamp,
		"EnhancedMonitoring":      s.EnhancedMonitoring,
	}
	kinesisSetEncryption(out, s)
	return out
}

// kinesisStreamDescriptionSummary backs DescribeStreamSummary, whose
// StreamDescriptionSummary shape carries the shard/retention/monitoring
// and encryption members.
func kinesisStreamDescriptionSummary(s KinesisStream) map[string]any {
	open := s.OpenShardCount
	if open == 0 {
		open = int64(len(s.Shards))
	}
	out := map[string]any{
		"StreamName":              s.StreamName,
		"StreamARN":               s.StreamARN,
		"StreamStatus":            s.StreamStatus,
		"RetentionPeriodHours":    s.RetentionPeriodHours,
		"StreamCreationTimestamp": s.CreationTimestamp,
		"EnhancedMonitoring":      s.EnhancedMonitoring,
		"OpenShardCount":          open,
		"StreamModeDetails":       s.StreamModeDetails,
	}
	kinesisSetEncryption(out, s)
	return out
}

// kinesisListStreamSummary backs ListStreams' StreamSummaries[], whose
// StreamSummary shape is the identity slice only — retention, shard
// counts, monitoring, and encryption are describe-only members.
func kinesisListStreamSummary(s KinesisStream) map[string]any {
	return map[string]any{
		"StreamName":              s.StreamName,
		"StreamARN":               s.StreamARN,
		"StreamStatus":            s.StreamStatus,
		"StreamModeDetails":       s.StreamModeDetails,
		"StreamCreationTimestamp": s.CreationTimestamp,
	}
}

// kinesisSetEncryption mirrors real Kinesis: an unencrypted stream reports
// EncryptionType=NONE and omits KeyId; a KMS-encrypted one reports its type
// and key id.
func kinesisSetEncryption(out map[string]any, s KinesisStream) {
	if s.EncryptionType == "" {
		out["EncryptionType"] = "NONE"
		return
	}
	out["EncryptionType"] = s.EncryptionType
	if s.KeyId != "" {
		out["KeyId"] = s.KeyId
	}
}

func kinesisStreamByNameOrARN(streamName, streamARN string) (KinesisStream, bool) {
	if streamName != "" {
		return kinesisStreams.Get(streamName)
	}
	if streamARN != "" {
		const sep = ":stream/"
		if idx := strings.Index(streamARN, sep); idx >= 0 {
			return kinesisStreams.Get(streamARN[idx+len(sep):])
		}
	}
	return KinesisStream{}, false
}

func handleKinesisCreateStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName        string            `json:"StreamName"`
		ShardCount        int64             `json:"ShardCount"`
		StreamModeDetails map[string]string `json:"StreamModeDetails"`
		Tags              map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.StreamName == "" {
		sim.AWSError(w, "InvalidArgumentException", "StreamName is required", http.StatusBadRequest)
		return
	}
	if _, ok := kinesisStreams.Get(req.StreamName); ok {
		sim.AWSError(w, "ResourceInUseException", "Stream already exists", http.StatusBadRequest)
		return
	}
	mode := req.StreamModeDetails
	if mode == nil {
		mode = map[string]string{"StreamMode": "PROVISIONED"}
	}
	shardCount := req.ShardCount
	if strings.EqualFold(mode["StreamMode"], "ON_DEMAND") && shardCount == 0 {
		shardCount = 4
	}
	if shardCount == 0 {
		shardCount = 1
	}
	stream := KinesisStream{
		StreamName:           req.StreamName,
		StreamARN:            kinesisStreamARN(req.StreamName),
		StreamStatus:         "ACTIVE",
		StreamModeDetails:    mode,
		Shards:               kinesisMakeShards(shardCount),
		RetentionPeriodHours: 24,
		EnhancedMonitoring:   []map[string]any{{"ShardLevelMetrics": []string{}}},
		CreationTimestamp:    float64(time.Now().Unix()),
		Tags:                 map[string]string{},
		OpenShardCount:       shardCount,
	}
	for k, v := range req.Tags {
		stream.Tags[k] = v
	}
	kinesisStreams.Put(req.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisDeleteStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string `json:"StreamName"`
		StreamARN  string `json:"StreamARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		writeKinesisJSON(w, http.StatusOK, map[string]any{})
		return
	}
	kinesisStreams.Delete(stream.StreamName)
	for _, shard := range stream.Shards {
		kinesisRecords.Delete(kinesisShardRecordKey(stream.StreamName, shard.ShardId))
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisDescribeStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName            string `json:"StreamName"`
		StreamARN             string `json:"StreamARN"`
		Limit                 int    `json:"Limit"`
		ExclusiveStartShardId string `json:"ExclusiveStartShardId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	desc := kinesisStreamDescription(stream)
	// Real DescribeStream paginates shards by Limit (default/max 100/10000) +
	// ExclusiveStartShardId, and sets HasMoreShards when more remain — mirror
	// the correct ListShards sibling rather than returning every shard.
	shards := append([]KinesisShard(nil), stream.Shards...)
	sort.Slice(shards, func(i, j int) bool { return shards[i].ShardId < shards[j].ShardId })
	if req.ExclusiveStartShardId != "" {
		for i, sh := range shards {
			if sh.ShardId == req.ExclusiveStartShardId {
				shards = shards[i+1:]
				break
			}
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 10000 {
		limit = 100
	}
	hasMore := false
	if len(shards) > limit {
		shards = shards[:limit]
		hasMore = true
	}
	desc["Shards"] = shards
	desc["HasMoreShards"] = hasMore
	writeKinesisJSON(w, http.StatusOK, map[string]any{"StreamDescription": desc})
}

func handleKinesisDescribeStreamSummary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string `json:"StreamName"`
		StreamARN  string `json:"StreamARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{"StreamDescriptionSummary": kinesisStreamDescriptionSummary(stream)})
}

func handleKinesisListStreams(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Limit                    int    `json:"Limit"`
		ExclusiveStartStreamName string `json:"ExclusiveStartStreamName"`
		NextToken                string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}

	var names []string
	for _, stream := range kinesisStreams.List() {
		names = append(names, stream.StreamName)
	}
	sort.Strings(names)

	// Real Kinesis caps the page at Limit (default/max 100). Both
	// ExclusiveStartStreamName (legacy cursor) and NextToken (modern cursor,
	// which the SDK paginator uses) resume after a stream name; NextToken wins
	// if both are present, matching the service's precedence.
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	start := req.ExclusiveStartStreamName
	if req.NextToken != "" {
		// NextToken is opaque in real Kinesis; decode it back to the
		// last-returned stream name. ExclusiveStartStreamName is the plain-name
		// legacy cursor and is used as-is.
		if name, ok := kinesisDecodeStreamToken(req.NextToken); ok {
			start = name
		} else {
			sim.AWSError(w, "InvalidArgumentException", "Invalid NextToken", http.StatusBadRequest)
			return
		}
	}
	if start != "" {
		idx := sort.SearchStrings(names, start)
		// SearchStrings returns the position of start (or insertion point); skip
		// past an exact match so we resume strictly after it.
		for idx < len(names) && names[idx] <= start {
			idx++
		}
		names = names[idx:]
	}

	hasMore := false
	if len(names) > limit {
		names = names[:limit]
		hasMore = true
	}

	out := map[string]any{
		"StreamNames":     names,
		"HasMoreStreams":  hasMore,
		"StreamSummaries": kinesisStreamSummaries(names),
	}
	if hasMore && len(names) > 0 {
		out["NextToken"] = kinesisEncodeStreamToken(names[len(names)-1])
	}
	writeKinesisJSON(w, http.StatusOK, out)
}

func kinesisEncodeStreamToken(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(name))
}

func kinesisDecodeStreamToken(token string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func kinesisStreamSummaries(names []string) []map[string]any {
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		if stream, ok := kinesisStreams.Get(name); ok {
			out = append(out, kinesisListStreamSummary(stream))
		}
	}
	return out
}

func handleKinesisListShards(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName            string `json:"StreamName"`
		StreamARN             string `json:"StreamARN"`
		MaxResults            int    `json:"MaxResults"`
		NextToken             string `json:"NextToken"`
		ExclusiveStartShardId string `json:"ExclusiveStartShardId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}

	// NextToken is an opaque cursor; in real Kinesis it encodes the stream
	// identity and the resume position, so a paginating client supplies only
	// NextToken (never StreamName/StreamARN). Decode it to recover both.
	streamName, streamARN, start := req.StreamName, req.StreamARN, req.ExclusiveStartShardId
	if req.NextToken != "" {
		tokStream, tokShard, ok := kinesisDecodeShardToken(req.NextToken)
		if !ok {
			sim.AWSError(w, "InvalidArgumentException", "Invalid NextToken", http.StatusBadRequest)
			return
		}
		streamName, start = tokStream, tokShard
		streamARN = ""
	}

	stream, ok := kinesisStreamByNameOrARN(streamName, streamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}

	// Shards are returned in ShardId order so paging is deterministic.
	shards := make([]KinesisShard, len(stream.Shards))
	copy(shards, stream.Shards)
	sort.Slice(shards, func(i, j int) bool { return shards[i].ShardId < shards[j].ShardId })

	// MaxResults caps a page (real Kinesis allows up to 10000; default 10000).
	maxResults := req.MaxResults
	if maxResults <= 0 || maxResults > 10000 {
		maxResults = 10000
	}
	if start != "" {
		idx := 0
		for idx < len(shards) && shards[idx].ShardId <= start {
			idx++
		}
		shards = shards[idx:]
	}

	var nextToken string
	if len(shards) > maxResults {
		shards = shards[:maxResults]
		nextToken = kinesisEncodeShardToken(stream.StreamName, shards[len(shards)-1].ShardId)
	}

	out := map[string]any{"Shards": shards}
	if nextToken != "" {
		out["NextToken"] = nextToken
	}
	writeKinesisJSON(w, http.StatusOK, out)
}

// kinesisEncodeShardToken / kinesisDecodeShardToken make ListShards' NextToken
// an opaque cursor carrying the stream name and the last-returned shard id, so a
// paginating client resumes by passing only NextToken — matching real Kinesis,
// whose token must not be combined with StreamName/StreamARN.
func kinesisEncodeShardToken(streamName, shardID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(streamName + "\x00" + shardID))
}

func kinesisDecodeShardToken(token string) (streamName, shardID string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(raw), "\x00", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func handleKinesisPutRecord(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName      string `json:"StreamName"`
		StreamARN       string `json:"StreamARN"`
		Data            []byte `json:"Data"`
		PartitionKey    string `json:"PartitionKey"`
		ExplicitHashKey string `json:"ExplicitHashKey"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	shardID, seq, err := kinesisAppendRecord(req.StreamName, req.StreamARN, req.Data, req.PartitionKey, req.ExplicitHashKey)
	if err != nil {
		sim.AWSError(w, "ResourceNotFoundException", err.Error(), http.StatusBadRequest)
		return
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"ShardId":        shardID,
		"SequenceNumber": seq,
	})
}

func handleKinesisPutRecords(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string `json:"StreamName"`
		StreamARN  string `json:"StreamARN"`
		Records    []struct {
			Data            []byte `json:"Data"`
			PartitionKey    string `json:"PartitionKey"`
			ExplicitHashKey string `json:"ExplicitHashKey"`
		} `json:"Records"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, 0, len(req.Records))
	for _, rec := range req.Records {
		shardID, seq, err := kinesisAppendRecord(req.StreamName, req.StreamARN, rec.Data, rec.PartitionKey, rec.ExplicitHashKey)
		if err != nil {
			out = append(out, map[string]any{"ErrorCode": "ResourceNotFoundException", "ErrorMessage": err.Error()})
			continue
		}
		out = append(out, map[string]any{"ShardId": shardID, "SequenceNumber": seq})
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"FailedRecordCount": 0,
		"Records":           out,
	})
}

func kinesisAppendRecord(streamName, streamARN string, data []byte, partitionKey, explicitHashKey string) (string, string, error) {
	kinesisMu.Lock()
	defer kinesisMu.Unlock()
	stream, ok := kinesisStreamByNameOrARN(streamName, streamARN)
	if !ok {
		return "", "", fmt.Errorf("stream not found")
	}
	shard := kinesisSelectShard(stream, partitionKey, explicitHashKey)
	key := kinesisShardRecordKey(stream.StreamName, shard.ShardId)
	records, _ := kinesisRecords.Get(key)
	seq := strconv.FormatInt(int64(len(records)+1), 10)
	records = append(records, kinesisRecord{
		SequenceNumber:              seq,
		ApproximateArrivalTimestamp: float64(time.Now().Unix()),
		Data:                        data,
		PartitionKey:                partitionKey,
		ExplicitHashKey:             explicitHashKey,
	})
	kinesisRecords.Put(key, records)
	return shard.ShardId, seq, nil
}

func kinesisSelectShard(stream KinesisStream, partitionKey, explicitHashKey string) KinesisShard {
	hash := new(big.Int)
	if explicitHashKey != "" {
		hash.SetString(explicitHashKey, 10)
	} else {
		sum := md5.Sum([]byte(partitionKey))
		hash.SetString(hex.EncodeToString(sum[:]), 16)
	}
	for _, shard := range stream.Shards {
		start := new(big.Int)
		end := new(big.Int)
		start.SetString(shard.HashKeyRange["StartingHashKey"], 10)
		end.SetString(shard.HashKeyRange["EndingHashKey"], 10)
		if hash.Cmp(start) >= 0 && hash.Cmp(end) <= 0 {
			return shard
		}
	}
	return stream.Shards[0]
}

func handleKinesisGetShardIterator(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName             string `json:"StreamName"`
		StreamARN              string `json:"StreamARN"`
		ShardId                string `json:"ShardId"`
		ShardIteratorType      string `json:"ShardIteratorType"`
		StartingSequenceNumber string `json:"StartingSequenceNumber"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	if !kinesisHasShard(stream, req.ShardId) {
		sim.AWSError(w, "ResourceNotFoundException", "Shard not found", http.StatusBadRequest)
		return
	}
	records, _ := kinesisRecords.Get(kinesisShardRecordKey(stream.StreamName, req.ShardId))
	index := 0
	switch req.ShardIteratorType {
	case "LATEST":
		index = len(records)
	case "AT_SEQUENCE_NUMBER", "AFTER_SEQUENCE_NUMBER":
		if seq, err := strconv.Atoi(req.StartingSequenceNumber); err == nil && seq > 0 {
			index = seq - 1
			if req.ShardIteratorType == "AFTER_SEQUENCE_NUMBER" {
				index = seq
			}
		}
	case "", "TRIM_HORIZON":
		index = 0
	default:
		index = 0
	}
	token := generateUUID()
	kinesisIterators.Put(token, kinesisIterator{StreamName: stream.StreamName, ShardID: req.ShardId, Index: index})
	writeKinesisJSON(w, http.StatusOK, map[string]any{"ShardIterator": token})
}

func kinesisHasShard(stream KinesisStream, shardID string) bool {
	for _, shard := range stream.Shards {
		if shard.ShardId == shardID {
			return true
		}
	}
	return false
}

func handleKinesisGetRecords(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShardIterator string `json:"ShardIterator"`
		Limit         int    `json:"Limit"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	it, ok := kinesisIterators.Get(req.ShardIterator)
	if !ok {
		sim.AWSError(w, "ExpiredIteratorException", "Shard iterator expired", http.StatusBadRequest)
		return
	}
	records, _ := kinesisRecords.Get(kinesisShardRecordKey(it.StreamName, it.ShardID))
	limit := req.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	end := it.Index + limit
	if end > len(records) {
		end = len(records)
	}
	out := make([]map[string]any, 0, end-it.Index)
	for _, rec := range records[it.Index:end] {
		out = append(out, map[string]any{
			"SequenceNumber":              rec.SequenceNumber,
			"ApproximateArrivalTimestamp": rec.ApproximateArrivalTimestamp,
			"Data":                        rec.Data,
			"PartitionKey":                rec.PartitionKey,
			"EncryptionType":              "NONE",
		})
	}
	next := generateUUID()
	kinesisIterators.Put(next, kinesisIterator{StreamName: it.StreamName, ShardID: it.ShardID, Index: end})
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"Records":            out,
		"NextShardIterator":  next,
		"MillisBehindLatest": 0,
	})
}

func handleKinesisAddTagsToStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string            `json:"StreamName"`
		StreamARN  string            `json:"StreamARN"`
		Tags       map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	if stream.Tags == nil {
		stream.Tags = map[string]string{}
	}
	for k, v := range req.Tags {
		stream.Tags[k] = v
	}
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisRemoveTagsFromStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string   `json:"StreamName"`
		StreamARN  string   `json:"StreamARN"`
		TagKeys    []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	for _, key := range req.TagKeys {
		delete(stream.Tags, key)
	}
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisListTagsForStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string `json:"StreamName"`
		StreamARN  string `json:"StreamARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	keys := make([]string, 0, len(stream.Tags))
	for key := range stream.Tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tags := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		tags = append(tags, map[string]string{"Key": key, "Value": stream.Tags[key]})
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"Tags":        tags,
		"HasMoreTags": false,
	})
}

func handleKinesisIncreaseStreamRetentionPeriod(w http.ResponseWriter, r *http.Request) {
	kinesisUpdateRetention(w, r)
}

func handleKinesisDecreaseStreamRetentionPeriod(w http.ResponseWriter, r *http.Request) {
	kinesisUpdateRetention(w, r)
}

func kinesisUpdateRetention(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName           string `json:"StreamName"`
		StreamARN            string `json:"StreamARN"`
		RetentionPeriodHours int64  `json:"RetentionPeriodHours"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	stream.RetentionPeriodHours = req.RetentionPeriodHours
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisEnableEnhancedMonitoring(w http.ResponseWriter, r *http.Request) {
	kinesisUpdateMonitoring(w, r, true)
}

func handleKinesisDisableEnhancedMonitoring(w http.ResponseWriter, r *http.Request) {
	kinesisUpdateMonitoring(w, r, false)
}

func kinesisUpdateMonitoring(w http.ResponseWriter, r *http.Request, enable bool) {
	var req struct {
		StreamName        string   `json:"StreamName"`
		StreamARN         string   `json:"StreamARN"`
		ShardLevelMetrics []string `json:"ShardLevelMetrics"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	current := []string{}
	if len(stream.EnhancedMonitoring) > 0 {
		if v, ok := stream.EnhancedMonitoring[0]["ShardLevelMetrics"].([]string); ok {
			current = append(current, v...)
		}
	}
	if enable {
		seen := map[string]bool{}
		for _, metric := range current {
			seen[metric] = true
		}
		for _, metric := range req.ShardLevelMetrics {
			if !seen[metric] {
				current = append(current, metric)
			}
		}
	} else {
		remove := map[string]bool{}
		for _, metric := range req.ShardLevelMetrics {
			remove[metric] = true
		}
		filtered := current[:0]
		for _, metric := range current {
			if !remove[metric] {
				filtered = append(filtered, metric)
			}
		}
		current = filtered
	}
	sort.Strings(current)
	stream.EnhancedMonitoring = []map[string]any{{"ShardLevelMetrics": current}}
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"StreamName":               stream.StreamName,
		"CurrentShardLevelMetrics": current,
		"DesiredShardLevelMetrics": current,
	})
}

func handleKinesisStartStreamEncryption(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName     string `json:"StreamName"`
		StreamARN      string `json:"StreamARN"`
		EncryptionType string `json:"EncryptionType"`
		KeyId          string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	stream.EncryptionType = req.EncryptionType
	stream.KeyId = req.KeyId
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisStopStreamEncryption(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName string `json:"StreamName"`
		StreamARN  string `json:"StreamARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	stream.EncryptionType = ""
	stream.KeyId = ""
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisUpdateShardCount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName       string `json:"StreamName"`
		StreamARN        string `json:"StreamARN"`
		TargetShardCount int64  `json:"TargetShardCount"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	current := int64(len(stream.Shards))
	stream.Shards = kinesisMakeShards(req.TargetShardCount)
	stream.OpenShardCount = req.TargetShardCount
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"StreamName":        stream.StreamName,
		"CurrentShardCount": current,
		"TargetShardCount":  req.TargetShardCount,
	})
}

func handleKinesisDescribeLimits(w http.ResponseWriter, r *http.Request) {
	open := 0
	for _, stream := range kinesisStreams.List() {
		open += len(stream.Shards)
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"ShardLimit":               10000,
		"OpenShardCount":           open,
		"OnDemandStreamCount":      0,
		"OnDemandStreamCountLimit": 50,
	})
}

func kinesisStreamByARN(streamARN string) (KinesisStream, bool) {
	const sep = ":stream/"
	idx := strings.Index(streamARN, sep)
	if idx < 0 {
		return KinesisStream{}, false
	}
	name := streamARN[idx+len(sep):]
	// A stream ARN ends at the stream name; never the consumer suffix.
	if slash := strings.IndexByte(name, '/'); slash >= 0 {
		name = name[:slash]
	}
	return kinesisStreams.Get(name)
}

// kinesisConsumerKey is the store key for a consumer: its ARN, which is unique
// per registration (carries the creation timestamp).
func kinesisConsumerKey(consumerARN string) string { return consumerARN }

func kinesisConsumerByStreamAndName(streamARN, consumerName string) (KinesisConsumer, bool) {
	for _, c := range kinesisConsumers.List() {
		if c.StreamARN == streamARN && c.ConsumerName == consumerName {
			return c, true
		}
	}
	return KinesisConsumer{}, false
}

func handleKinesisRegisterStreamConsumer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamARN    string            `json:"StreamARN"`
		ConsumerName string            `json:"ConsumerName"`
		Tags         map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.StreamARN == "" || req.ConsumerName == "" {
		sim.AWSError(w, "InvalidArgumentException", "StreamARN and ConsumerName are required", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByARN(req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	if _, exists := kinesisConsumerByStreamAndName(req.StreamARN, req.ConsumerName); exists {
		sim.AWSError(w, "ResourceInUseException", "Consumer already exists", http.StatusBadRequest)
		return
	}
	ts := time.Now().Unix()
	consumerARN := fmt.Sprintf("%s/consumer/%s:%d", stream.StreamARN, req.ConsumerName, ts)
	consumer := KinesisConsumer{
		ConsumerName:              req.ConsumerName,
		ConsumerARN:               consumerARN,
		ConsumerStatus:            "ACTIVE",
		ConsumerCreationTimestamp: float64(ts),
		StreamARN:                 stream.StreamARN,
	}
	kinesisConsumers.Put(kinesisConsumerKey(consumerARN), consumer)
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"Consumer": map[string]any{
			"ConsumerName":              consumer.ConsumerName,
			"ConsumerARN":               consumer.ConsumerARN,
			"ConsumerStatus":            consumer.ConsumerStatus,
			"ConsumerCreationTimestamp": consumer.ConsumerCreationTimestamp,
		},
	})
}

func handleKinesisDeregisterStreamConsumer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamARN    string `json:"StreamARN"`
		ConsumerName string `json:"ConsumerName"`
		ConsumerARN  string `json:"ConsumerARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	consumer, ok := kinesisResolveConsumer(req.StreamARN, req.ConsumerName, req.ConsumerARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Consumer not found", http.StatusBadRequest)
		return
	}
	kinesisConsumers.Delete(kinesisConsumerKey(consumer.ConsumerARN))
	iamDeleteResourcePolicy(consumer.ConsumerARN)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

// kinesisResolveConsumer locates a consumer either by its ARN (preferred) or by
// the (StreamARN, ConsumerName) pair — the two addressing modes the API accepts.
func kinesisResolveConsumer(streamARN, consumerName, consumerARN string) (KinesisConsumer, bool) {
	if consumerARN != "" {
		return kinesisConsumers.Get(kinesisConsumerKey(consumerARN))
	}
	if streamARN != "" && consumerName != "" {
		return kinesisConsumerByStreamAndName(streamARN, consumerName)
	}
	return KinesisConsumer{}, false
}

func handleKinesisDescribeStreamConsumer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamARN    string `json:"StreamARN"`
		ConsumerName string `json:"ConsumerName"`
		ConsumerARN  string `json:"ConsumerARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	consumer, ok := kinesisResolveConsumer(req.StreamARN, req.ConsumerName, req.ConsumerARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Consumer not found", http.StatusBadRequest)
		return
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"ConsumerDescription": map[string]any{
			"ConsumerName":              consumer.ConsumerName,
			"ConsumerARN":               consumer.ConsumerARN,
			"ConsumerStatus":            consumer.ConsumerStatus,
			"ConsumerCreationTimestamp": consumer.ConsumerCreationTimestamp,
			"StreamARN":                 consumer.StreamARN,
		},
	})
}

func handleKinesisListStreamConsumers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamARN  string `json:"StreamARN"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}

	// NextToken is opaque and (per the API) unambiguously identifies the stream,
	// so a paginating client omits StreamARN when supplying it. Decode it to
	// recover the stream ARN and the resume position (last-returned consumer ARN).
	streamARN, start := req.StreamARN, ""
	if req.NextToken != "" {
		tokStream, tokConsumer, ok := kinesisDecodeShardToken(req.NextToken)
		if !ok {
			sim.AWSError(w, "InvalidArgumentException", "Invalid NextToken", http.StatusBadRequest)
			return
		}
		streamARN, start = tokStream, tokConsumer
	}
	if streamARN == "" {
		sim.AWSError(w, "InvalidArgumentException", "StreamARN is required", http.StatusBadRequest)
		return
	}
	if _, ok := kinesisStreamByARN(streamARN); !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}

	var consumers []KinesisConsumer
	for _, c := range kinesisConsumers.List() {
		if c.StreamARN == streamARN {
			consumers = append(consumers, c)
		}
	}
	sort.Slice(consumers, func(i, j int) bool { return consumers[i].ConsumerARN < consumers[j].ConsumerARN })

	if start != "" {
		idx := 0
		for idx < len(consumers) && consumers[idx].ConsumerARN <= start {
			idx++
		}
		consumers = consumers[idx:]
	}

	maxResults := req.MaxResults
	if maxResults <= 0 || maxResults > 100 {
		maxResults = 100
	}
	var nextToken string
	if len(consumers) > maxResults {
		consumers = consumers[:maxResults]
		nextToken = kinesisEncodeShardToken(streamARN, consumers[len(consumers)-1].ConsumerARN)
	}

	out := make([]map[string]any, 0, len(consumers))
	for _, c := range consumers {
		out = append(out, map[string]any{
			"ConsumerName":              c.ConsumerName,
			"ConsumerARN":               c.ConsumerARN,
			"ConsumerStatus":            c.ConsumerStatus,
			"ConsumerCreationTimestamp": c.ConsumerCreationTimestamp,
		})
	}
	resp := map[string]any{"Consumers": out}
	if nextToken != "" {
		resp["NextToken"] = nextToken
	}
	writeKinesisJSON(w, http.StatusOK, resp)
}

// kinesisResolveResourceARN validates that the policy ARN names an existing
// Kinesis stream or consumer, returning the canonical ARN to key the policy on.
func kinesisResolveResourceARN(resourceARN string) (string, bool) {
	if resourceARN == "" {
		return "", false
	}
	if strings.Contains(resourceARN, "/consumer/") {
		if _, ok := kinesisConsumers.Get(kinesisConsumerKey(resourceARN)); ok {
			return resourceARN, true
		}
		return "", false
	}
	if stream, ok := kinesisStreamByARN(resourceARN); ok {
		return stream.StreamARN, true
	}
	return "", false
}

func handleKinesisPutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
		Policy      string `json:"Policy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Policy == "" {
		sim.AWSError(w, "InvalidArgumentException", "Policy is required", http.StatusBadRequest)
		return
	}
	arn, ok := kinesisResolveResourceARN(req.ResourceARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Resource not found", http.StatusBadRequest)
		return
	}
	iamPutResourcePolicy(arn, req.Policy)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisGetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	arn, ok := kinesisResolveResourceARN(req.ResourceARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Resource not found", http.StatusBadRequest)
		return
	}
	policy, ok := iamGetResourcePolicy(arn)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "No resource policy attached", http.StatusBadRequest)
		return
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{"Policy": policy})
}

func handleKinesisDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	arn, ok := kinesisResolveResourceARN(req.ResourceARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Resource not found", http.StatusBadRequest)
		return
	}
	iamDeleteResourcePolicy(arn)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisMergeShards(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName           string `json:"StreamName"`
		StreamARN            string `json:"StreamARN"`
		ShardToMerge         string `json:"ShardToMerge"`
		AdjacentShardToMerge string `json:"AdjacentShardToMerge"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	kinesisMu.Lock()
	defer kinesisMu.Unlock()
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	left, lok := kinesisFindShard(stream, req.ShardToMerge)
	right, rok := kinesisFindShard(stream, req.AdjacentShardToMerge)
	if !lok || !rok {
		sim.AWSError(w, "ResourceNotFoundException", "Shard not found", http.StatusBadRequest)
		return
	}
	// Adjacency: the two shards' hash-key ranges must be contiguous. Order them
	// so left precedes right, then require left.End+1 == right.Start.
	lStart, _ := new(big.Int).SetString(left.HashKeyRange["StartingHashKey"], 10)
	rStart, _ := new(big.Int).SetString(right.HashKeyRange["StartingHashKey"], 10)
	if lStart.Cmp(rStart) > 0 {
		left, right = right, left
	}
	lEnd, _ := new(big.Int).SetString(left.HashKeyRange["EndingHashKey"], 10)
	rStart, _ = new(big.Int).SetString(right.HashKeyRange["StartingHashKey"], 10)
	if new(big.Int).Add(lEnd, big.NewInt(1)).Cmp(rStart) != 0 {
		sim.AWSError(w, "InvalidArgumentException", "Shards are not adjacent", http.StatusBadRequest)
		return
	}

	// The merged child spans both parents' ranges; both parents close (their
	// SequenceNumberRange gets an EndingSequenceNumber) and name the child via no
	// further records. The child's ParentShardId/AdjacentParentShardId record the
	// lineage exactly as real Kinesis does.
	childID := kinesisNextShardID(stream)
	child := KinesisShard{
		ShardId: childID,
		HashKeyRange: map[string]string{
			"StartingHashKey": left.HashKeyRange["StartingHashKey"],
			"EndingHashKey":   right.HashKeyRange["EndingHashKey"],
		},
		SequenceNumberRange: map[string]string{"StartingSequenceNumber": "1"},
		ParentShardId:       left.ShardId,
	}
	stream.Shards = kinesisCloseParents(stream.Shards, left.ShardId, right.ShardId)
	stream.Shards = append(stream.Shards, child)
	stream.OpenShardCount = kinesisOpenShardCount(stream.Shards)
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisSplitShard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamName         string `json:"StreamName"`
		StreamARN          string `json:"StreamARN"`
		ShardToSplit       string `json:"ShardToSplit"`
		NewStartingHashKey string `json:"NewStartingHashKey"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	kinesisMu.Lock()
	defer kinesisMu.Unlock()
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	parent, ok := kinesisFindShard(stream, req.ShardToSplit)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Shard not found", http.StatusBadRequest)
		return
	}
	newStart, valid := new(big.Int).SetString(req.NewStartingHashKey, 10)
	if !valid {
		sim.AWSError(w, "InvalidArgumentException", "NewStartingHashKey must be a valid integer", http.StatusBadRequest)
		return
	}
	start, _ := new(big.Int).SetString(parent.HashKeyRange["StartingHashKey"], 10)
	end, _ := new(big.Int).SetString(parent.HashKeyRange["EndingHashKey"], 10)
	// NewStartingHashKey must fall strictly inside (start, end].
	if newStart.Cmp(start) <= 0 || newStart.Cmp(end) > 0 {
		sim.AWSError(w, "InvalidArgumentException", "NewStartingHashKey is out of range for the shard", http.StatusBadRequest)
		return
	}

	lowID := kinesisNextShardID(stream)
	low := KinesisShard{
		ShardId: lowID,
		HashKeyRange: map[string]string{
			"StartingHashKey": parent.HashKeyRange["StartingHashKey"],
			"EndingHashKey":   new(big.Int).Sub(newStart, big.NewInt(1)).String(),
		},
		SequenceNumberRange: map[string]string{"StartingSequenceNumber": "1"},
		ParentShardId:       parent.ShardId,
	}
	stream.Shards = append(stream.Shards, low)
	highID := kinesisNextShardID(stream)
	high := KinesisShard{
		ShardId: highID,
		HashKeyRange: map[string]string{
			"StartingHashKey": newStart.String(),
			"EndingHashKey":   parent.HashKeyRange["EndingHashKey"],
		},
		SequenceNumberRange: map[string]string{"StartingSequenceNumber": "1"},
		ParentShardId:       parent.ShardId,
	}
	stream.Shards = kinesisCloseParents(stream.Shards, parent.ShardId)
	stream.Shards = append(stream.Shards, high)
	stream.OpenShardCount = kinesisOpenShardCount(stream.Shards)
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func kinesisFindShard(stream KinesisStream, shardID string) (KinesisShard, bool) {
	for _, sh := range stream.Shards {
		if sh.ShardId == shardID {
			return sh, true
		}
	}
	return KinesisShard{}, false
}

// kinesisNextShardID returns the next sequential shardId-NNNNNNNNNNNN, one past
// the highest currently present, so merge/split children get fresh ids.
func kinesisNextShardID(stream KinesisStream) string {
	maxN := int64(-1)
	for _, sh := range stream.Shards {
		if n, err := strconv.ParseInt(strings.TrimPrefix(sh.ShardId, "shardId-"), 10, 64); err == nil && n > maxN {
			maxN = n
		}
	}
	return fmt.Sprintf("shardId-%012d", maxN+1)
}

// kinesisCloseParents marks the named parent shards closed by stamping an
// EndingSequenceNumber on their SequenceNumberRange. A closed shard remains in
// the shard list (lineage is queryable) but is no longer open.
func kinesisCloseParents(shards []KinesisShard, parentIDs ...string) []KinesisShard {
	closed := map[string]bool{}
	for _, id := range parentIDs {
		closed[id] = true
	}
	for i := range shards {
		if closed[shards[i].ShardId] {
			if shards[i].SequenceNumberRange == nil {
				shards[i].SequenceNumberRange = map[string]string{}
			}
			if _, has := shards[i].SequenceNumberRange["EndingSequenceNumber"]; !has {
				shards[i].SequenceNumberRange["EndingSequenceNumber"] = strconv.FormatInt(time.Now().UnixNano(), 10)
			}
		}
	}
	return shards
}

// kinesisOpenShardCount counts shards with no EndingSequenceNumber (still open).
func kinesisOpenShardCount(shards []KinesisShard) int64 {
	var open int64
	for _, sh := range shards {
		if sh.SequenceNumberRange == nil {
			open++
			continue
		}
		if _, closed := sh.SequenceNumberRange["EndingSequenceNumber"]; !closed {
			open++
		}
	}
	return open
}

// kinesisTagTarget resolves a resource ARN to the stream that owns the tags.
// Kinesis tag operations address a stream (consumers are not separately
// taggable through TagResource in this slice), so the ARN must name a stream.
func kinesisTagStream(resourceARN string) (KinesisStream, bool) {
	if resourceARN == "" || strings.Contains(resourceARN, "/consumer/") {
		return KinesisStream{}, false
	}
	return kinesisStreamByARN(resourceARN)
}

func handleKinesisTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string            `json:"ResourceARN"`
		Tags        map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisTagStream(req.ResourceARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Resource not found", http.StatusBadRequest)
		return
	}
	if stream.Tags == nil {
		stream.Tags = map[string]string{}
	}
	for k, v := range req.Tags {
		stream.Tags[k] = v
	}
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisTagStream(req.ResourceARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Resource not found", http.StatusBadRequest)
		return
	}
	for _, key := range req.TagKeys {
		delete(stream.Tags, key)
	}
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisTagStream(req.ResourceARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Resource not found", http.StatusBadRequest)
		return
	}
	keys := make([]string, 0, len(stream.Tags))
	for key := range stream.Tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tags := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		tags = append(tags, map[string]string{"Key": key, "Value": stream.Tags[key]})
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{"Tags": tags})
}

func handleKinesisUpdateStreamMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamARN         string            `json:"StreamARN"`
		StreamModeDetails map[string]string `json:"StreamModeDetails"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.StreamModeDetails == nil {
		sim.AWSError(w, "InvalidArgumentException", "StreamModeDetails is required", http.StatusBadRequest)
		return
	}
	mode := req.StreamModeDetails["StreamMode"]
	if mode != "PROVISIONED" && mode != "ON_DEMAND" {
		sim.AWSError(w, "InvalidArgumentException", "StreamMode must be PROVISIONED or ON_DEMAND", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByARN(req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	stream.StreamModeDetails = map[string]string{"StreamMode": mode}
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

const kinesisAccountSettingsKey = "account"

func handleKinesisDescribeAccountSettings(w http.ResponseWriter, r *http.Request) {
	settings, ok := kinesisAccount.Get(kinesisAccountSettingsKey)
	if !ok {
		// A never-configured account reports the disabled commitment.
		settings = KinesisAccountSettings{Status: "DISABLED"}
	}
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"MinimumThroughputBillingCommitment": kinesisAccountSettingsBody(settings),
	})
}

func handleKinesisUpdateAccountSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MinimumThroughputBillingCommitment struct {
			Status string `json:"Status"`
		} `json:"MinimumThroughputBillingCommitment"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	status := req.MinimumThroughputBillingCommitment.Status
	if status != "ENABLED" && status != "DISABLED" {
		sim.AWSError(w, "InvalidArgumentException", "Status must be ENABLED or DISABLED", http.StatusBadRequest)
		return
	}
	settings := KinesisAccountSettings{Status: status}
	if status == "ENABLED" {
		now := time.Now()
		settings.StartedAt = float64(now.Unix())
		// The commitment has a 24h minimum before it may be disabled.
		settings.EarliestAllowedEndAt = float64(now.Add(24 * time.Hour).Unix())
	}
	kinesisAccount.Put(kinesisAccountSettingsKey, settings)
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"MinimumThroughputBillingCommitment": kinesisAccountSettingsBody(settings),
	})
}

func kinesisAccountSettingsBody(s KinesisAccountSettings) map[string]any {
	out := map[string]any{"Status": s.Status}
	if s.StartedAt != 0 {
		out["StartedAt"] = s.StartedAt
	}
	if s.EarliestAllowedEndAt != 0 {
		out["EarliestAllowedEndAt"] = s.EarliestAllowedEndAt
	}
	if s.EndedAt != 0 {
		out["EndedAt"] = s.EndedAt
	}
	return out
}

func handleKinesisUpdateMaxRecordSize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamARN          string `json:"StreamARN"`
		StreamName         string `json:"StreamName"`
		MaxRecordSizeInKiB int64  `json:"MaxRecordSizeInKiB"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.MaxRecordSizeInKiB < 1024 || req.MaxRecordSizeInKiB > 10240 {
		sim.AWSError(w, "ValidationException", "MaxRecordSizeInKiB must be between 1024 and 10240", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	stream.MaxRecordSizeInKiB = req.MaxRecordSizeInKiB
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisUpdateStreamWarmThroughput(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamARN           string `json:"StreamARN"`
		StreamName          string `json:"StreamName"`
		WarmThroughputMiBps int64  `json:"WarmThroughputMiBps"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.WarmThroughputMiBps < 0 {
		sim.AWSError(w, "InvalidArgumentException", "WarmThroughputMiBps must be non-negative", http.StatusBadRequest)
		return
	}
	stream, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Stream not found", http.StatusBadRequest)
		return
	}
	stream.WarmThroughputMiBps = req.WarmThroughputMiBps
	kinesisStreams.Put(stream.StreamName, stream)
	writeKinesisJSON(w, http.StatusOK, map[string]any{
		"StreamARN":  stream.StreamARN,
		"StreamName": stream.StreamName,
		"WarmThroughput": map[string]any{
			"TargetMiBps":  req.WarmThroughputMiBps,
			"CurrentMiBps": req.WarmThroughputMiBps,
		},
	})
}

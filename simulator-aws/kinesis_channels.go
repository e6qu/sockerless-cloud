package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Amazon Kinesis Data Streams channels: a managed delivery of one or more
// streams' records into Amazon S3, or into S3 Tables. A channel names the
// streams it reads, the role it reads and writes as, and the destination it
// writes to — so it exists only over streams and a role that exist, and its
// description answers from what the channel was created with.

// KinesisChannel is one delivery channel.
type KinesisChannel struct {
	ChannelName             string                 `json:"channelName"`
	ChannelID               string                 `json:"channelId"`
	ChannelARN              string                 `json:"channelArn"`
	Status                  string                 `json:"status"`
	StatusReason            string                 `json:"statusReason,omitempty"`
	CreationTimestamp       float64                `json:"creationTimestamp"`
	ServiceExecutionRoleARN string                 `json:"serviceExecutionRoleArn"`
	Streams                 []KinesisChannelStream `json:"streams"`
	S3Destination           map[string]any         `json:"s3Destination,omitempty"`
	S3TablesDestination     map[string]any         `json:"s3TablesDestination,omitempty"`
	Encryption              map[string]any         `json:"encryption,omitempty"`
	Logging                 map[string]any         `json:"logging"`
	Tags                    map[string]string      `json:"tags,omitempty"`
}

// KinesisChannelStream is one stream the channel delivers, and the record
// configuration it delivers it with.
type KinesisChannelStream struct {
	StreamARN               string         `json:"streamArn"`
	StreamCreationTimestamp float64        `json:"streamCreationTimestamp"`
	RecordConfiguration     map[string]any `json:"recordConfiguration"`
}

var kinesisChannels sim.Store[KinesisChannel]

func registerKinesisChannels(r *sim.AWSRouter, srv *sim.Server) {
	kinesisChannels = sim.MakeStore[KinesisChannel](srv.DB(), "kinesis_channels")

	r.Register("Kinesis_20131202.CreateChannel", handleKinesisCreateChannel)
	r.Register("Kinesis_20131202.DescribeChannel", handleKinesisDescribeChannel)
	r.Register("Kinesis_20131202.UpdateChannel", handleKinesisUpdateChannel)
	r.Register("Kinesis_20131202.DeleteChannel", handleKinesisDeleteChannel)
	r.Register("Kinesis_20131202.ListChannels", handleKinesisListChannels)
}

func kinesisChannelARN(name string) string {
	return fmt.Sprintf("arn:aws:kinesis:%s:%s:channel/%s", awsRegion(), awsAccountID(), name)
}

// kinesisChannelStreamName reads the stream an ARN names, so a channel's
// stream configuration resolves to the stream this simulator holds.
func kinesisChannelStreamName(arn string) string {
	if i := strings.LastIndex(arn, ":stream/"); i >= 0 {
		return arn[i+len(":stream/"):]
	}
	return arn
}

// kinesisChannelNameFromARN reads the channel a request addresses. Every
// channel operation but the create and the list is addressed by ARN.
func kinesisChannelNameFromARN(arn string) string {
	if i := strings.LastIndex(arn, ":channel/"); i >= 0 {
		return arn[i+len(":channel/"):]
	}
	return ""
}

func handleKinesisCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChannelName             string `json:"ChannelName"`
		ServiceExecutionRoleARN string `json:"ServiceExecutionRoleARN"`
		StreamConfigurationList []struct {
			StreamARN           string         `json:"StreamARN"`
			RecordConfiguration map[string]any `json:"RecordConfiguration"`
		} `json:"StreamConfigurationList"`
		S3DestinationConfiguration       map[string]any    `json:"S3DestinationConfiguration"`
		S3TablesDestinationConfiguration map[string]any    `json:"S3TablesDestinationConfiguration"`
		EncryptionConfiguration          map[string]any    `json:"EncryptionConfiguration"`
		LoggingConfiguration             map[string]any    `json:"LoggingConfiguration"`
		Tags                             map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ChannelName == "" || req.ServiceExecutionRoleARN == "" || len(req.StreamConfigurationList) == 0 {
		sim.AWSError(w, "ValidationException",
			"ChannelName, ServiceExecutionRoleARN and StreamConfigurationList are required",
			http.StatusBadRequest)
		return
	}
	// A channel delivers to exactly one destination; neither and both are
	// requests the service rejects rather than a channel that writes nowhere.
	if (req.S3DestinationConfiguration == nil) == (req.S3TablesDestinationConfiguration == nil) {
		sim.AWSError(w, "ValidationException",
			"exactly one of S3DestinationConfiguration and S3TablesDestinationConfiguration is required",
			http.StatusBadRequest)
		return
	}
	if _, ok := iamRoles.Get(iamRoleNameFromArn(req.ServiceExecutionRoleARN)); !ok {
		sim.AWSError(w, "ValidationException",
			"The service execution role "+req.ServiceExecutionRoleARN+" does not exist",
			http.StatusBadRequest)
		return
	}
	if _, exists := kinesisChannels.Get(req.ChannelName); exists {
		sim.AWSError(w, "ResourceInUseException", "Channel already exists", http.StatusBadRequest)
		return
	}

	// The channel reads the streams it names, so each has to be there. Its
	// description reports each stream's creation time, which is the stream's
	// own — not a time invented at channel creation.
	streams := make([]KinesisChannelStream, 0, len(req.StreamConfigurationList))
	for _, cfg := range req.StreamConfigurationList {
		stream, ok := kinesisStreams.Get(kinesisChannelStreamName(cfg.StreamARN))
		if !ok {
			sim.AWSError(w, "ResourceNotFoundException",
				"Stream "+cfg.StreamARN+" not found", http.StatusBadRequest)
			return
		}
		if cfg.RecordConfiguration == nil {
			sim.AWSError(w, "ValidationException",
				"each StreamConfiguration must carry a RecordConfiguration", http.StatusBadRequest)
			return
		}
		streams = append(streams, KinesisChannelStream{
			StreamARN:               stream.StreamARN,
			StreamCreationTimestamp: stream.CreationTimestamp,
			RecordConfiguration:     cfg.RecordConfiguration,
		})
	}

	logging := req.LoggingConfiguration
	if logging == nil {
		// LoggingConfiguration is required on the way out, and a channel
		// created without one logs nothing.
		logging = map[string]any{"CloudWatchLogs": map[string]any{"Enabled": false}}
	}
	channel := KinesisChannel{
		ChannelName: req.ChannelName, ChannelID: generateUUID(),
		ChannelARN: kinesisChannelARN(req.ChannelName),
		// A channel is usable as soon as it is created here: there is no
		// provisioning to wait for behind it.
		Status:                  "ACTIVE",
		CreationTimestamp:       float64(time.Now().Unix()),
		ServiceExecutionRoleARN: req.ServiceExecutionRoleARN,
		Streams:                 streams,
		S3Destination:           kinesisChannelS3Description(req.S3DestinationConfiguration),
		S3TablesDestination:     kinesisChannelS3Description(req.S3TablesDestinationConfiguration),
		Encryption:              req.EncryptionConfiguration,
		Logging:                 logging,
		Tags:                    req.Tags,
	}
	kinesisChannels.Put(channel.ChannelName, channel)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ChannelDescription": kinesisChannelDescription(channel),
	})
}

// kinesisChannelS3Description is the destination as a description reports it:
// what the create configured, plus the two things a description states that a
// create may leave out. The freshness takes the service's default. The
// dead-letter queue takes the destination's own bucket — a description always
// names one, and records that cannot be delivered go beside the data they were
// meant to join rather than to a bucket the caller never named.
func kinesisChannelS3Description(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	described := map[string]any{}
	for k, v := range config {
		described[k] = v
	}
	if _, ok := described["DataFreshnessInSeconds"]; !ok {
		described["DataFreshnessInSeconds"] = 300
	}
	if _, ok := described["DeadLetterQueueS3Configuration"]; !ok {
		if storage, isMap := described["StorageConfiguration"].(map[string]any); isMap {
			described["DeadLetterQueueS3Configuration"] = map[string]any{
				"BucketARN":           storage["BucketARN"],
				"ExpectedBucketOwner": storage["ExpectedBucketOwner"],
				"ErrorOutputPrefix":   "errors/",
			}
		}
	}
	return described
}

func kinesisChannelDescription(c KinesisChannel) map[string]any {
	streams := make([]map[string]any, 0, len(c.Streams))
	for _, s := range c.Streams {
		streams = append(streams, map[string]any{
			"StreamARN":               s.StreamARN,
			"StreamCreationTimestamp": s.StreamCreationTimestamp,
			"RecordConfiguration":     s.RecordConfiguration,
		})
	}
	out := map[string]any{
		"ChannelName":              c.ChannelName,
		"ChannelARN":               c.ChannelARN,
		"ChannelId":                c.ChannelID,
		"ChannelStatus":            c.Status,
		"ChannelCreationTimestamp": c.CreationTimestamp,
		"ServiceExecutionRoleARN":  c.ServiceExecutionRoleARN,
		"StreamConfigurationList":  streams,
		"LoggingConfiguration":     c.Logging,
	}
	if c.StatusReason != "" {
		out["ChannelStatusReason"] = c.StatusReason
	}
	if c.S3Destination != nil {
		out["S3DestinationConfiguration"] = c.S3Destination
	}
	if c.S3TablesDestination != nil {
		out["S3TablesDestinationConfiguration"] = c.S3TablesDestination
	}
	if c.Encryption != nil {
		out["EncryptionConfiguration"] = c.Encryption
	}
	return out
}

// kinesisChannelForRequest resolves the channel a request addresses by ARN.
func kinesisChannelForRequest(w http.ResponseWriter, r *http.Request) (KinesisChannel, bool) {
	var req struct {
		ChannelARN string `json:"ChannelARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return KinesisChannel{}, false
	}
	if req.ChannelARN == "" {
		sim.AWSError(w, "ValidationException", "ChannelARN is required", http.StatusBadRequest)
		return KinesisChannel{}, false
	}
	channel, ok := kinesisChannels.Get(kinesisChannelNameFromARN(req.ChannelARN))
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException",
			"Channel "+req.ChannelARN+" not found", http.StatusBadRequest)
		return KinesisChannel{}, false
	}
	return channel, true
}

func handleKinesisDescribeChannel(w http.ResponseWriter, r *http.Request) {
	channel, ok := kinesisChannelForRequest(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ChannelDescription": kinesisChannelDescription(channel),
	})
}

func handleKinesisDeleteChannel(w http.ResponseWriter, r *http.Request) {
	channel, ok := kinesisChannelForRequest(w, r)
	if !ok {
		return
	}
	kinesisChannels.Delete(channel.ChannelName)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleKinesisUpdateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChannelARN                       string         `json:"ChannelARN"`
		S3DestinationConfiguration       map[string]any `json:"S3DestinationConfiguration"`
		S3TablesDestinationConfiguration map[string]any `json:"S3TablesDestinationConfiguration"`
		LoggingConfiguration             map[string]any `json:"LoggingConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidArgumentException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ChannelARN == "" {
		sim.AWSError(w, "ValidationException", "ChannelARN is required", http.StatusBadRequest)
		return
	}
	name := kinesisChannelNameFromARN(req.ChannelARN)
	channel, ok := kinesisChannels.Get(name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException",
			"Channel "+req.ChannelARN+" not found", http.StatusBadRequest)
		return
	}
	// The update carries only what changes, and only for the destination the
	// channel already has — an update naming the other one is asking to
	// change a destination this channel does not deliver to.
	if req.S3DestinationConfiguration != nil {
		if channel.S3Destination == nil {
			sim.AWSError(w, "ValidationException",
				"the channel does not deliver to Amazon S3", http.StatusBadRequest)
			return
		}
		for k, v := range req.S3DestinationConfiguration {
			channel.S3Destination[k] = v
		}
	}
	if req.S3TablesDestinationConfiguration != nil {
		if channel.S3TablesDestination == nil {
			sim.AWSError(w, "ValidationException",
				"the channel does not deliver to Amazon S3 Tables", http.StatusBadRequest)
			return
		}
		for k, v := range req.S3TablesDestinationConfiguration {
			channel.S3TablesDestination[k] = v
		}
	}
	if req.LoggingConfiguration != nil {
		channel.Logging = req.LoggingConfiguration
	}
	kinesisChannels.Put(name, channel)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ChannelDescription": kinesisChannelDescription(channel),
	})
}

func handleKinesisListChannels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StreamFilter []struct {
			StreamARN string `json:"StreamARN"`
		} `json:"StreamFilter"`
	}
	_ = sim.ReadJSON(r, &req)
	wanted := map[string]bool{}
	for _, f := range req.StreamFilter {
		wanted[f.StreamARN] = true
	}

	summaries := []map[string]any{}
	for _, channel := range kinesisChannels.List() {
		identifiers := make([]map[string]any, 0, len(channel.Streams))
		matches := len(wanted) == 0
		for _, s := range channel.Streams {
			if wanted[s.StreamARN] {
				matches = true
			}
			identifiers = append(identifiers, map[string]any{
				"StreamARN":               s.StreamARN,
				"StreamCreationTimestamp": s.StreamCreationTimestamp,
			})
		}
		if !matches {
			continue
		}
		summary := map[string]any{
			"ChannelName":              channel.ChannelName,
			"ChannelARN":               channel.ChannelARN,
			"ChannelId":                channel.ChannelID,
			"ChannelStatus":            channel.Status,
			"ChannelCreationTimestamp": channel.CreationTimestamp,
			"ChannelDestinationType":   kinesisChannelDestinationType(channel),
			"Streams":                  identifiers,
		}
		if channel.StatusReason != "" {
			summary["ChannelStatusReason"] = channel.StatusReason
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return kinesisChannelSummaryName(summaries[i]) < kinesisChannelSummaryName(summaries[j])
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ChannelSummaries": summaries})
}

// kinesisChannelSummaryName reads the name out of a summary, which is what the
// listing orders by.
func kinesisChannelSummaryName(summary map[string]any) string {
	name, _ := summary["ChannelName"].(string)
	return name
}

// kinesisChannelDestinationType names where a channel delivers, which is what
// the summary reports in place of the destination's own configuration.
func kinesisChannelDestinationType(c KinesisChannel) string {
	if c.S3TablesDestination != nil {
		return "S3_TABLES"
	}
	return "S3"
}

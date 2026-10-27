package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pspb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Cloud Pub/Sub v1 over gRPC, reading and writing the stores the REST slice
// owns and delivering through pubsub_delivery.go, so both surfaces see one
// state. The gRPC-only cloud.google.com/go/pubsub client reaches it at the
// simulator's gRPC endpoint over TLS with a bearer token.

type pubsubPublisherGRPC struct {
	pspb.UnimplementedPublisherServer
}

type pubsubSubscriberGRPC struct {
	pspb.UnimplementedSubscriberServer
}

type pubsubSchemaGRPC struct {
	pspb.UnimplementedSchemaServiceServer
}

func registerPubSubGRPC(gs *grpc.Server) {
	pspb.RegisterPublisherServer(gs, &pubsubPublisherGRPC{})
	pspb.RegisterSubscriberServer(gs, &pubsubSubscriberGRPC{})
	pspb.RegisterSchemaServiceServer(gs, &pubsubSchemaGRPC{})
}

// psSnapshotBacklogs holds the backlog each snapshot captured, so a Seek to it
// replays exactly the messages outstanding when it was taken. It is keyed like
// psSnapshots and deleted with the snapshot.
var psSnapshotBacklogs sim.Store[[]PSMessage]

func psTopicToProto(t PSTopic) *pspb.Topic {
	out := &pspb.Topic{
		Name:                     t.Name,
		Labels:                   t.Labels,
		KmsKeyName:               t.KmsKeyName,
		MessageRetentionDuration: psDurationToProto(t.MessageRetentionDuration),
	}
	if t.MessageStoragePolicy != nil {
		out.MessageStoragePolicy = &pspb.MessageStoragePolicy{
			AllowedPersistenceRegions: t.MessageStoragePolicy.AllowedPersistenceRegions,
		}
	}
	if len(t.SchemaSettings) > 0 {
		out.SchemaSettings = psSchemaSettingsFromJSON(t.SchemaSettings)
	}
	return out
}

func psTopicFromProto(t *pspb.Topic) PSTopic {
	out := PSTopic{
		Name:                     t.GetName(),
		Labels:                   t.GetLabels(),
		KmsKeyName:               t.GetKmsKeyName(),
		MessageRetentionDuration: psDurationFromProto(t.GetMessageRetentionDuration()),
	}
	if msp := t.GetMessageStoragePolicy(); msp != nil {
		out.MessageStoragePolicy = &PSMessageStoragePolicy{AllowedPersistenceRegions: msp.GetAllowedPersistenceRegions()}
	}
	if ss := t.GetSchemaSettings(); ss != nil {
		out.SchemaSettings = psSchemaSettingsToJSON(ss)
	}
	return out
}

func psSubscriptionToProto(s PSSubscription) *pspb.Subscription {
	out := &pspb.Subscription{
		Name:                     s.Name,
		Topic:                    s.Topic,
		AckDeadlineSeconds:       int32(s.AckDeadlineSeconds),
		Labels:                   s.Labels,
		MessageRetentionDuration: psDurationToProto(s.MessageRetentionDuration),
		RetainAckedMessages:      s.RetainAckedMessages,
		EnableMessageOrdering:    s.EnableMessageOrdering,
		Filter:                   s.Filter,
		Detached:                 s.Detached,
	}
	if s.PushConfig != nil {
		out.PushConfig = &pspb.PushConfig{
			PushEndpoint: s.PushConfig.PushEndpoint,
			Attributes:   s.PushConfig.Attributes,
		}
		if s.PushConfig.OidcToken != nil {
			out.PushConfig.AuthenticationMethod = &pspb.PushConfig_OidcToken_{
				OidcToken: &pspb.PushConfig_OidcToken{
					ServiceAccountEmail: s.PushConfig.OidcToken.ServiceAccountEmail,
					Audience:            s.PushConfig.OidcToken.Audience,
				},
			}
		}
	}
	if s.ExpirationPolicy != nil {
		out.ExpirationPolicy = &pspb.ExpirationPolicy{Ttl: psDurationToProto(s.ExpirationPolicy.Ttl)}
	}
	if s.DeadLetterPolicy != nil {
		out.DeadLetterPolicy = &pspb.DeadLetterPolicy{
			DeadLetterTopic:     s.DeadLetterPolicy.DeadLetterTopic,
			MaxDeliveryAttempts: int32(s.DeadLetterPolicy.MaxDeliveryAttempts),
		}
	}
	if s.RetryPolicy != nil {
		out.RetryPolicy = &pspb.RetryPolicy{
			MinimumBackoff: psDurationToProto(s.RetryPolicy.MinimumBackoff),
			MaximumBackoff: psDurationToProto(s.RetryPolicy.MaximumBackoff),
		}
	}
	return out
}

func psSubscriptionFromProto(s *pspb.Subscription) PSSubscription {
	out := PSSubscription{
		Name:                     s.GetName(),
		Topic:                    s.GetTopic(),
		AckDeadlineSeconds:       int(s.GetAckDeadlineSeconds()),
		Labels:                   s.GetLabels(),
		MessageRetentionDuration: psDurationFromProto(s.GetMessageRetentionDuration()),
		RetainAckedMessages:      s.GetRetainAckedMessages(),
		EnableMessageOrdering:    s.GetEnableMessageOrdering(),
		Filter:                   s.GetFilter(),
		Detached:                 s.GetDetached(),
	}
	if pc := s.GetPushConfig(); pc != nil {
		out.PushConfig = &PSPushConfig{
			PushEndpoint: pc.GetPushEndpoint(),
			Attributes:   pc.GetAttributes(),
		}
		if ot := pc.GetOidcToken(); ot != nil {
			out.PushConfig.OidcToken = &PSOidcToken{
				ServiceAccountEmail: ot.GetServiceAccountEmail(),
				Audience:            ot.GetAudience(),
			}
		}
	}
	if ep := s.GetExpirationPolicy(); ep != nil {
		out.ExpirationPolicy = &PSExpirationPolicy{Ttl: psDurationFromProto(ep.GetTtl())}
	}
	if dlp := s.GetDeadLetterPolicy(); dlp != nil {
		out.DeadLetterPolicy = &PSDeadLetterPolicy{
			DeadLetterTopic:     dlp.GetDeadLetterTopic(),
			MaxDeliveryAttempts: int(dlp.GetMaxDeliveryAttempts()),
		}
	}
	if rp := s.GetRetryPolicy(); rp != nil {
		out.RetryPolicy = &PSRetryPolicy{
			MinimumBackoff: psDurationFromProto(rp.GetMinimumBackoff()),
			MaximumBackoff: psDurationFromProto(rp.GetMaximumBackoff()),
		}
	}
	return out
}

func psMessageToProto(m PSMessage) *pspb.PubsubMessage {
	var data []byte
	if m.Data != "" {
		if b, err := base64.StdEncoding.DecodeString(m.Data); err == nil {
			data = b
		}
	}
	out := &pspb.PubsubMessage{
		Data:        data,
		Attributes:  m.Attributes,
		MessageId:   m.MessageId,
		OrderingKey: m.OrderingKey,
	}
	if m.PublishTime != "" {
		out.PublishTime = psRFC3339ToProto(m.PublishTime)
	}
	return out
}

func psMessageFromProto(m *pspb.PubsubMessage) PSMessage {
	out := PSMessage{
		MessageId:   m.GetMessageId(),
		Attributes:  m.GetAttributes(),
		OrderingKey: m.GetOrderingKey(),
	}
	if len(m.GetData()) > 0 {
		out.Data = base64.StdEncoding.EncodeToString(m.GetData())
	}
	if m.GetPublishTime() != nil {
		out.PublishTime = psRFC3339FromProto(m.GetPublishTime())
	}
	return out
}

func psReceivedToProto(d psDelivered) *pspb.ReceivedMessage {
	return &pspb.ReceivedMessage{
		AckId:           d.AckID,
		Message:         psMessageToProto(d.Message),
		DeliveryAttempt: int32(d.DeliveryAttempt),
	}
}

func psSnapshotToProto(s PSSnapshot) *pspb.Snapshot {
	return &pspb.Snapshot{
		Name:       s.Name,
		Topic:      s.Topic,
		ExpireTime: psRFC3339ToProto(s.ExpireTime),
		Labels:     s.Labels,
	}
}

func psSnapshotFromProto(s *pspb.Snapshot) PSSnapshot {
	return PSSnapshot{
		Name:       s.GetName(),
		Topic:      s.GetTopic(),
		ExpireTime: psRFC3339FromProto(s.GetExpireTime()),
		Labels:     s.GetLabels(),
	}
}

func psSchemaToProto(s PSSchema) *pspb.Schema {
	return &pspb.Schema{
		Name:               s.Name,
		Type:               psSchemaTypeProto(s.Type),
		Definition:         s.Definition,
		RevisionId:         s.RevisionId,
		RevisionCreateTime: psRFC3339ToProto(s.RevisionCreateTime),
	}
}

func psSchemaFromProto(s *pspb.Schema) PSSchema {
	return PSSchema{
		Name:               s.GetName(),
		Type:               psSchemaTypeString(s.GetType()),
		Definition:         s.GetDefinition(),
		RevisionId:         s.GetRevisionId(),
		RevisionCreateTime: psRFC3339FromProto(s.GetRevisionCreateTime()),
	}
}

func psSchemaTypeProto(s string) pspb.Schema_Type {
	switch s {
	case "PROTOCOL_BUFFER":
		return pspb.Schema_PROTOCOL_BUFFER
	case "AVRO":
		return pspb.Schema_AVRO
	}
	return pspb.Schema_TYPE_UNSPECIFIED
}

func psSchemaTypeString(t pspb.Schema_Type) string {
	switch t {
	case pspb.Schema_PROTOCOL_BUFFER:
		return "PROTOCOL_BUFFER"
	case pspb.Schema_AVRO:
		return "AVRO"
	}
	return ""
}

// psRFC3339ToProto parses a REST RFC3339 timestamp and returns its proto form.
func psRFC3339ToProto(s string) *timestamppb.Timestamp {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return nil
		}
	}
	return timestamppb.New(t.UTC())
}

func psRFC3339FromProto(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339Nano)
}

// psDurationToProto parses a stored duration in the canonical "Ns" form or
// Go's Duration string.
func psDurationToProto(s string) *durationpb.Duration {
	if s == "" {
		return nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return durationpb.New(d)
	}
	return nil
}

func psDurationFromProto(d *durationpb.Duration) string {
	if d == nil {
		return ""
	}
	return d.AsDuration().String()
}

// psSchemaSettingsToJSON stores a proto SchemaSettings as canonical proto
// JSON in the REST store's raw field.
func psSchemaSettingsToJSON(ss *pspb.SchemaSettings) json.RawMessage {
	if ss == nil {
		return nil
	}
	b, err := protojson.Marshal(ss)
	if err != nil {
		return nil
	}
	return b
}

func psSchemaSettingsFromJSON(raw json.RawMessage) *pspb.SchemaSettings {
	if len(raw) == 0 {
		return nil
	}
	ss := &pspb.SchemaSettings{}
	if err := protojson.Unmarshal(raw, ss); err != nil {
		return nil
	}
	return ss
}

func (s *pubsubPublisherGRPC) CreateTopic(_ context.Context, req *pspb.Topic) (*pspb.Topic, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if project := psResourceProject(name); !psProjectActive(project) {
		return nil, status.Errorf(codes.NotFound, psProjectNotFound, project)
	}
	if _, exists := psTopics.Get(name); exists {
		return nil, status.Errorf(codes.AlreadyExists, "Resource already exists (resource=%s)", name)
	}
	t := psTopicFromProto(req)
	psTopics.Put(name, t)
	return psTopicToProto(t), nil
}

// psMaskField spells a FieldMask path the way the REST door does. A protobuf
// FieldMask names fields in snake_case, as the client libraries send it.
func psMaskField(path string) string {
	return kebabToCamel(strings.ReplaceAll(path, "_", "-"))
}

func (s *pubsubPublisherGRPC) UpdateTopic(_ context.Context, req *pspb.UpdateTopicRequest) (*pspb.Topic, error) {
	topic := req.GetTopic()
	name := topic.GetName()
	existing, ok := psTopics.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	updated := psTopicFromProto(topic)
	mask := req.GetUpdateMask()
	if mask != nil && len(mask.GetPaths()) > 0 {
		for _, p := range mask.GetPaths() {
			switch psMaskField(p) {
			case "labels":
				existing.Labels = updated.Labels
			case "kmsKeyName":
				existing.KmsKeyName = updated.KmsKeyName
			case "messageRetentionDuration":
				existing.MessageRetentionDuration = updated.MessageRetentionDuration
			case "messageStoragePolicy":
				existing.MessageStoragePolicy = updated.MessageStoragePolicy
			case "schemaSettings":
				existing.SchemaSettings = updated.SchemaSettings
			default:
				return nil, status.Errorf(codes.InvalidArgument, "invalid update_mask provided in the UpdateTopicRequest: the field %s is not updatable", p)
			}
		}
	} else {
		existing = updated
	}
	psTopics.Put(name, existing)
	return psTopicToProto(existing), nil
}

func (s *pubsubPublisherGRPC) Publish(_ context.Context, req *pspb.PublishRequest) (*pspb.PublishResponse, error) {
	var msgs []PSMessage
	for _, m := range req.GetMessages() {
		msgs = append(msgs, psMessageFromProto(m))
	}
	ids, err := psPublishMessages(req.GetTopic(), msgs)
	if err != nil {
		return nil, err
	}
	return &pspb.PublishResponse{MessageIds: ids}, nil
}

func (s *pubsubPublisherGRPC) GetTopic(_ context.Context, req *pspb.GetTopicRequest) (*pspb.Topic, error) {
	t, ok := psTopics.Get(req.GetTopic())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", req.GetTopic())
	}
	return psTopicToProto(t), nil
}

func (s *pubsubPublisherGRPC) ListTopics(_ context.Context, req *pspb.ListTopicsRequest) (*pspb.ListTopicsResponse, error) {
	page, next, err := psGRPCPage(psTopics, fmt.Sprintf("projects/%s/topics/", psNormalizeProject(req.GetProject())),
		func(v PSTopic) string { return v.Name }, req.GetPageSize(), req.GetPageToken(), psTopicToProto)
	if err != nil {
		return nil, err
	}
	return &pspb.ListTopicsResponse{Topics: page, NextPageToken: next}, nil
}

func (s *pubsubPublisherGRPC) ListTopicSubscriptions(_ context.Context, req *pspb.ListTopicSubscriptionsRequest) (*pspb.ListTopicSubscriptionsResponse, error) {
	page, next, err := psTopicAttachedPage(req.GetTopic(), psSubscriptions, func(v PSSubscription) (string, string) { return v.Topic, v.Name }, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	return &pspb.ListTopicSubscriptionsResponse{Subscriptions: page, NextPageToken: next}, nil
}

func (s *pubsubPublisherGRPC) ListTopicSnapshots(_ context.Context, req *pspb.ListTopicSnapshotsRequest) (*pspb.ListTopicSnapshotsResponse, error) {
	page, next, err := psTopicAttachedPage(req.GetTopic(), psSnapshots, func(v PSSnapshot) (string, string) { return v.Topic, v.Name }, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	return &pspb.ListTopicSnapshotsResponse{Snapshots: page, NextPageToken: next}, nil
}

func (s *pubsubPublisherGRPC) DeleteTopic(_ context.Context, req *pspb.DeleteTopicRequest) (*emptypb.Empty, error) {
	name := req.GetTopic()
	if !psTopics.Delete(name) {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	return &emptypb.Empty{}, nil
}

func (s *pubsubPublisherGRPC) DetachSubscription(_ context.Context, req *pspb.DetachSubscriptionRequest) (*pspb.DetachSubscriptionResponse, error) {
	name := req.GetSubscription()
	if _, ok := psSubscriptions.Get(name); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	psSubscriptions.Update(name, func(s *PSSubscription) { s.Detached = true })
	psQueues.Delete(name)
	psSignalSubscription(name)
	return &pspb.DetachSubscriptionResponse{}, nil
}

func (s *pubsubSubscriberGRPC) CreateSubscription(_ context.Context, req *pspb.Subscription) (*pspb.Subscription, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	topic := req.GetTopic()
	if topic == "" {
		return nil, status.Error(codes.InvalidArgument, "topic is required")
	}
	if project := psResourceProject(name); !psProjectActive(project) {
		return nil, status.Errorf(codes.NotFound, psProjectNotFound, project)
	}
	if _, ok := psTopics.Get(topic); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", topic)
	}
	if _, exists := psSubscriptions.Get(name); exists {
		return nil, status.Errorf(codes.AlreadyExists, "Resource already exists (resource=%s)", name)
	}
	sub := psSubscriptionFromProto(req)
	if sub.AckDeadlineSeconds == 0 {
		sub.AckDeadlineSeconds = 10
	}
	if sub.MessageRetentionDuration == "" {
		sub.MessageRetentionDuration = "604800s"
	}
	if err := psValidateSubscription(sub); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	psSubscriptions.Put(name, sub)
	if _, ok := psQueues.Get(name); !ok {
		psQueues.Put(name, psQueue{Subscription: name})
	}
	return psSubscriptionToProto(sub), nil
}

func (s *pubsubSubscriberGRPC) GetSubscription(_ context.Context, req *pspb.GetSubscriptionRequest) (*pspb.Subscription, error) {
	sub, ok := psSubscriptions.Get(req.GetSubscription())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", req.GetSubscription())
	}
	return psSubscriptionToProto(sub), nil
}

func (s *pubsubSubscriberGRPC) UpdateSubscription(_ context.Context, req *pspb.UpdateSubscriptionRequest) (*pspb.Subscription, error) {
	sub := req.GetSubscription()
	name := sub.GetName()
	existing, ok := psSubscriptions.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	updated := psSubscriptionFromProto(sub)
	mask := req.GetUpdateMask()
	if mask != nil && len(mask.GetPaths()) > 0 {
		for _, p := range mask.GetPaths() {
			switch psMaskField(p) {
			case "ackDeadlineSeconds":
				existing.AckDeadlineSeconds = updated.AckDeadlineSeconds
			case "labels":
				existing.Labels = updated.Labels
			case "pushConfig":
				existing.PushConfig = updated.PushConfig
			case "messageRetentionDuration":
				existing.MessageRetentionDuration = updated.MessageRetentionDuration
			case "retainAckedMessages":
				existing.RetainAckedMessages = updated.RetainAckedMessages
			case "expirationPolicy":
				existing.ExpirationPolicy = updated.ExpirationPolicy
			case "enableMessageOrdering":
				existing.EnableMessageOrdering = updated.EnableMessageOrdering
			case "filter":
				existing.Filter = updated.Filter
			case "deadLetterPolicy":
				existing.DeadLetterPolicy = updated.DeadLetterPolicy
			case "retryPolicy":
				existing.RetryPolicy = updated.RetryPolicy
			default:
				return nil, status.Errorf(codes.InvalidArgument, "invalid update_mask provided in the UpdateSubscriptionRequest: the field %s is not updatable", p)
			}
		}
	} else {
		existing = updated
	}
	if existing.AckDeadlineSeconds == 0 {
		existing.AckDeadlineSeconds = psMinAckDeadlineSeconds
	}
	if err := psValidateSubscription(existing); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	psSubscriptions.Put(name, existing)
	psSignalSubscription(name)
	return psSubscriptionToProto(existing), nil
}

func (s *pubsubSubscriberGRPC) ListSubscriptions(_ context.Context, req *pspb.ListSubscriptionsRequest) (*pspb.ListSubscriptionsResponse, error) {
	page, next, err := psGRPCPage(psSubscriptions, fmt.Sprintf("projects/%s/subscriptions/", psNormalizeProject(req.GetProject())),
		func(v PSSubscription) string { return v.Name }, req.GetPageSize(), req.GetPageToken(), psSubscriptionToProto)
	if err != nil {
		return nil, err
	}
	return &pspb.ListSubscriptionsResponse{Subscriptions: page, NextPageToken: next}, nil
}

func (s *pubsubSubscriberGRPC) DeleteSubscription(_ context.Context, req *pspb.DeleteSubscriptionRequest) (*emptypb.Empty, error) {
	name := req.GetSubscription()
	if !psSubscriptions.Delete(name) {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	psQueues.Delete(name)
	psSignalSubscription(name)
	return &emptypb.Empty{}, nil
}

func (s *pubsubSubscriberGRPC) ModifyAckDeadline(_ context.Context, req *pspb.ModifyAckDeadlineRequest) (*emptypb.Empty, error) {
	sub := req.GetSubscription()
	if _, ok := psSubscriptions.Get(sub); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", sub)
	}
	if !psValidAckDeadline(req.GetAckDeadlineSeconds()) {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid ack deadline given: %d", req.GetAckDeadlineSeconds())
	}
	psModifyAckDeadline(sub, req.GetAckIds(), req.GetAckDeadlineSeconds())
	return &emptypb.Empty{}, nil
}

func (s *pubsubSubscriberGRPC) Acknowledge(_ context.Context, req *pspb.AcknowledgeRequest) (*emptypb.Empty, error) {
	sub := req.GetSubscription()
	if _, ok := psSubscriptions.Get(sub); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", sub)
	}
	psAcknowledge(sub, req.GetAckIds())
	return &emptypb.Empty{}, nil
}

func (s *pubsubSubscriberGRPC) ModifyPushConfig(_ context.Context, req *pspb.ModifyPushConfigRequest) (*emptypb.Empty, error) {
	sub := req.GetSubscription()
	if _, ok := psSubscriptions.Get(sub); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", sub)
	}
	pc := req.GetPushConfig()
	var rest *PSPushConfig
	if pc != nil {
		rest = &PSPushConfig{
			PushEndpoint: pc.GetPushEndpoint(),
			Attributes:   pc.GetAttributes(),
		}
		if ot := pc.GetOidcToken(); ot != nil {
			rest.OidcToken = &PSOidcToken{
				ServiceAccountEmail: ot.GetServiceAccountEmail(),
				Audience:            ot.GetAudience(),
			}
		}
	}
	psSubscriptions.Update(sub, func(s *PSSubscription) { s.PushConfig = rest })
	psSignalSubscription(sub)
	return &emptypb.Empty{}, nil
}

func (s *pubsubSubscriberGRPC) Pull(ctx context.Context, req *pspb.PullRequest) (*pspb.PullResponse, error) {
	delivered, err := psPull(ctx, req.GetSubscription(), int(req.GetMaxMessages()), req.GetReturnImmediately())
	if err != nil {
		return nil, err
	}
	resp := &pspb.PullResponse{ReceivedMessages: make([]*pspb.ReceivedMessage, 0, len(delivered))}
	for _, d := range delivered {
		resp.ReceivedMessages = append(resp.ReceivedMessages, psReceivedToProto(d))
	}
	return resp, nil
}

// StreamingPull is the high-level client's default delivery path: the first
// request names the subscription and its flow control and deadline, later
// ones carry acks and modacks, and the stream delivers as messages arrive
// until the client cancels.
func (s *pubsubSubscriberGRPC) StreamingPull(stream pspb.Subscriber_StreamingPullServer) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	subName := first.GetSubscription()
	sub, ok := psSubscriptions.Get(subName)
	if !ok {
		return status.Errorf(codes.NotFound, "Resource not found (resource=%s)", subName)
	}
	streamDeadline := first.GetStreamAckDeadlineSeconds()
	if streamDeadline < 10 || streamDeadline > psMaxAckDeadlineSeconds {
		return status.Errorf(codes.InvalidArgument, "Invalid stream ack deadline given: %d", streamDeadline)
	}
	if ids := first.GetAckIds(); len(ids) > 0 {
		psAcknowledge(subName, ids)
	}

	maxOutstanding := int(first.GetMaxOutstandingMessages())
	if maxOutstanding <= 0 {
		maxOutstanding = 1000
	}

	// Reader loop: applies acks and modacks embedded in client messages, and
	// ends the stream with the error that closed the client's side.
	ctx, closeStream := context.WithCancelCause(ctx)
	defer closeStream(nil)
	bg.JoinedGo(func() {
		closeStream(psStreamingPullReadLoop(stream, subName))
	})

	for {
		// A select that finds both the client's cancellation and an arrival
		// ready picks either, so check the stream before leasing into it.
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		arrival := psArrivals.Changed(subName)
		// Honour flow control: cap delivery by the subscription's outstanding
		// messages.
		outstanding := psOutstanding(sub)
		budget := maxOutstanding - outstanding
		if budget > 0 {
			delivered, derr := psDequeue(subName, budget, time.Duration(streamDeadline)*time.Second)
			if derr != nil {
				return derr
			}
			if len(delivered) > 0 {
				rm := make([]*pspb.ReceivedMessage, 0, len(delivered))
				for _, d := range delivered {
					rm = append(rm, psReceivedToProto(d))
				}
				if err := stream.Send(&pspb.StreamingPullResponse{ReceivedMessages: rm}); err != nil {
					// The stream closed before the messages left: nothing
					// received them, so they are not deliveries.
					for _, d := range delivered {
						psRelease(subName, d.AckID)
					}
					return err
				}
			}
		}
		if psArrivals.Wait(ctx, subName, arrival, time.Time{}) != nil {
			return context.Cause(ctx)
		}
	}
}

// psStreamingPullReadLoop consumes client-side StreamingPullRequests and applies
// the ack and modack batches they carry. It returns when the client closes the
// stream or the context is cancelled.
func psStreamingPullReadLoop(stream pspb.Subscriber_StreamingPullServer, subName string) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		if ids := req.GetAckIds(); len(ids) > 0 {
			psAcknowledge(subName, ids)
		}
		secs := req.GetModifyDeadlineSeconds()
		ackIDs := req.GetModifyDeadlineAckIds()
		for i, id := range ackIDs {
			sec := int32(0)
			if i < len(secs) {
				sec = secs[i]
			}
			if psValidAckDeadline(sec) {
				psModifyAckDeadline(subName, []string{id}, sec)
			}
		}
	}
}

func (s *pubsubSubscriberGRPC) Seek(_ context.Context, req *pspb.SeekRequest) (*pspb.SeekResponse, error) {
	subName := req.GetSubscription()
	if _, ok := psSubscriptions.Get(subName); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", subName)
	}
	switch target := req.Target.(type) {
	case *pspb.SeekRequest_Snapshot:
		key := psSnapshotKeyFromName(target.Snapshot)
		if _, ok := psSnapshots.Get(key); !ok {
			return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", target.Snapshot)
		}
		backlog, _ := psSnapshotBacklogs.Get(key)
		psSeekSnapshot(subName, backlog)
		return &pspb.SeekResponse{}, nil
	case *pspb.SeekRequest_Time:
		if target.Time == nil {
			return nil, status.Error(codes.InvalidArgument, "seek time is required")
		}
		psSeekTime(subName, target.Time.AsTime())
		return &pspb.SeekResponse{}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "seek target (snapshot or time) is required")
	}
}

// psSnapshotKeyFromName derives a snapshot's store key, project/name, as the
// REST slice does.
func psSnapshotKeyFromName(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) >= 4 {
		return parts[1] + "/" + parts[3]
	}
	return name
}

func (s *pubsubSubscriberGRPC) CreateSnapshot(_ context.Context, req *pspb.CreateSnapshotRequest) (*pspb.Snapshot, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	sub := req.GetSubscription()
	if sub == "" {
		return nil, status.Error(codes.InvalidArgument, "subscription is required")
	}
	if project := psResourceProject(name); !psProjectActive(project) {
		return nil, status.Errorf(codes.NotFound, psProjectNotFound, project)
	}
	if _, ok := psSubscriptions.Get(sub); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", sub)
	}
	if _, exists := psSnapshots.Get(psSnapshotKeyFromName(name)); exists {
		return nil, status.Errorf(codes.AlreadyExists, "Resource already exists (resource=%s)", name)
	}
	topic := ""
	if sb, ok := psSubscriptions.Get(sub); ok {
		topic = sb.Topic
	}
	snap := PSSnapshot{
		Name:       name,
		Topic:      topic,
		ExpireTime: time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339),
		Labels:     req.GetLabels(),
	}
	// Capture the subscription's outstanding backlog so a later Seek to this
	// snapshot replays exactly these messages.
	key := psSnapshotKeyFromName(name)
	if backlog := psBacklog(sub); len(backlog) > 0 {
		psSnapshotBacklogs.Put(key, backlog)
	}
	psSnapshots.Put(key, snap)
	return psSnapshotToProto(snap), nil
}

func (s *pubsubSubscriberGRPC) GetSnapshot(_ context.Context, req *pspb.GetSnapshotRequest) (*pspb.Snapshot, error) {
	snap, ok := psSnapshots.Get(psSnapshotKeyFromName(req.GetSnapshot()))
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", req.GetSnapshot())
	}
	return psSnapshotToProto(snap), nil
}

func (s *pubsubSubscriberGRPC) UpdateSnapshot(_ context.Context, req *pspb.UpdateSnapshotRequest) (*pspb.Snapshot, error) {
	snapReq := req.GetSnapshot()
	name := snapReq.GetName()
	existing, ok := psSnapshots.Get(psSnapshotKeyFromName(name))
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	updated := psSnapshotFromProto(snapReq)
	mask := req.GetUpdateMask()
	if mask != nil {
		for _, p := range mask.GetPaths() {
			switch psMaskField(p) {
			case "labels":
				existing.Labels = updated.Labels
			case "expireTime":
				existing.ExpireTime = updated.ExpireTime
			default:
				return nil, status.Errorf(codes.InvalidArgument, "invalid update_mask provided in the UpdateSnapshotRequest: the field %s is not updatable", p)
			}
		}
	} else {
		existing.Labels = updated.Labels
		if updated.ExpireTime != "" {
			existing.ExpireTime = updated.ExpireTime
		}
	}
	psSnapshots.Put(psSnapshotKeyFromName(name), existing)
	return psSnapshotToProto(existing), nil
}

func (s *pubsubSubscriberGRPC) ListSnapshots(_ context.Context, req *pspb.ListSnapshotsRequest) (*pspb.ListSnapshotsResponse, error) {
	page, next, err := psGRPCPage(psSnapshots, fmt.Sprintf("projects/%s/snapshots/", psNormalizeProject(req.GetProject())),
		func(v PSSnapshot) string { return v.Name }, req.GetPageSize(), req.GetPageToken(), psSnapshotToProto)
	if err != nil {
		return nil, err
	}
	return &pspb.ListSnapshotsResponse{Snapshots: page, NextPageToken: next}, nil
}

func (s *pubsubSubscriberGRPC) DeleteSnapshot(_ context.Context, req *pspb.DeleteSnapshotRequest) (*emptypb.Empty, error) {
	key := psSnapshotKeyFromName(req.GetSnapshot())
	if !psSnapshots.Delete(key) {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", req.GetSnapshot())
	}
	psSnapshotBacklogs.Delete(key)
	return &emptypb.Empty{}, nil
}

func (s *pubsubSchemaGRPC) CreateSchema(_ context.Context, req *pspb.CreateSchemaRequest) (*pspb.Schema, error) {
	project := psNormalizeProject(req.GetParent())
	schemaID := req.GetSchemaId()
	sch := psSchemaFromProto(req.GetSchema())
	if schemaID == "" {
		schemaID = psSchemaShortName(sch.Name)
	}
	if schemaID == "" {
		return nil, status.Error(codes.InvalidArgument, "schemaId is required")
	}
	name := psSchemaName(project, schemaID)
	if _, ok := psSchemaHead(name); ok {
		return nil, status.Errorf(codes.AlreadyExists, "Resource already exists (resource=%s)", name)
	}
	rev := PSSchema{
		Name:               name,
		Type:               sch.Type,
		Definition:         sch.Definition,
		RevisionId:         psNewRevisionID(),
		RevisionCreateTime: nowTimestamp(),
	}
	psSchemaRevisions.Put(psSchemaRevisionKey(name, rev.RevisionId), rev)
	return psSchemaToProto(rev), nil
}

func (s *pubsubSchemaGRPC) GetSchema(_ context.Context, req *pspb.GetSchemaRequest) (*pspb.Schema, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	head, ok := psSchemaHead(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	if req.GetView() == pspb.SchemaView_BASIC {
		head.Definition = ""
	}
	return psSchemaToProto(head), nil
}

func (s *pubsubSchemaGRPC) ListSchemas(_ context.Context, req *pspb.ListSchemasRequest) (*pspb.ListSchemasResponse, error) {
	project := psNormalizeProject(req.GetParent())
	prefix := "projects/" + project + "/schemas/"
	seen := map[string]PSSchema{}
	for _, sc := range psSchemaRevisions.List() {
		if !strings.HasPrefix(sc.Name, prefix) {
			continue
		}
		cur, ok := seen[sc.Name]
		if !ok || sc.RevisionCreateTime > cur.RevisionCreateTime {
			seen[sc.Name] = sc
		}
	}
	all := make([]PSSchema, 0, len(seen))
	for _, sc := range seen {
		if req.GetView() == pspb.SchemaView_BASIC {
			sc.Definition = ""
		}
		all = append(all, sc)
	}
	page, next, err := grpcKeyPage(all, req.GetPageSize(), req.GetPageToken(), gcpAsc(func(v PSSchema) string { return v.Name }))
	if err != nil {
		return nil, err
	}
	resp := &pspb.ListSchemasResponse{Schemas: make([]*pspb.Schema, 0, len(page))}
	for _, sc := range page {
		resp.Schemas = append(resp.Schemas, psSchemaToProto(sc))
	}
	resp.NextPageToken = next
	return resp, nil
}

func (s *pubsubSchemaGRPC) DeleteSchema(_ context.Context, req *pspb.DeleteSchemaRequest) (*emptypb.Empty, error) {
	name := req.GetName()
	revs := psSchemaRevisionsFor(name)
	if len(revs) == 0 {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	for _, r := range revs {
		psSchemaRevisions.Delete(psSchemaRevisionKey(name, r.RevisionId))
	}
	return &emptypb.Empty{}, nil
}

func (s *pubsubSchemaGRPC) ValidateSchema(_ context.Context, req *pspb.ValidateSchemaRequest) (*pspb.ValidateSchemaResponse, error) {
	sch := psSchemaFromProto(req.GetSchema())
	if sch.Definition == "" {
		return nil, status.Error(codes.InvalidArgument, "schema definition is required")
	}
	switch sch.Type {
	case "PROTOCOL_BUFFER", "AVRO":
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unsupported schema type: %s", sch.Type)
	}
	return &pspb.ValidateSchemaResponse{}, nil
}

func (s *pubsubSchemaGRPC) ValidateMessage(_ context.Context, req *pspb.ValidateMessageRequest) (*pspb.ValidateMessageResponse, error) {
	if name := req.GetName(); name != "" {
		if _, ok := psSchemaHead(name); !ok {
			return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
		}
	} else if sch := req.GetSchema(); sch != nil {
		if sch.GetDefinition() == "" {
			return nil, status.Error(codes.InvalidArgument, "schema is required")
		}
	} else {
		return nil, status.Error(codes.InvalidArgument, "either name or schema must be provided")
	}
	if len(req.GetMessage()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "message is required")
	}
	return &pspb.ValidateMessageResponse{}, nil
}

func (s *pubsubSchemaGRPC) ListSchemaRevisions(_ context.Context, req *pspb.ListSchemaRevisionsRequest) (*pspb.ListSchemaRevisionsResponse, error) {
	name := req.GetName()
	revs := psSchemaRevisionsFor(name)
	if len(revs) == 0 {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	if req.GetView() == pspb.SchemaView_BASIC {
		for i := range revs {
			revs[i].Definition = ""
		}
	}
	page, next, err := grpcKeyPage(revs, req.GetPageSize(), req.GetPageToken(),
		gcpDesc(func(v PSSchema) string { return v.RevisionCreateTime }), gcpDesc(func(v PSSchema) string { return v.RevisionId }))
	if err != nil {
		return nil, err
	}
	resp := &pspb.ListSchemaRevisionsResponse{Schemas: make([]*pspb.Schema, 0, len(page))}
	for _, sc := range page {
		resp.Schemas = append(resp.Schemas, psSchemaToProto(sc))
	}
	resp.NextPageToken = next
	return resp, nil
}

func (s *pubsubSchemaGRPC) CommitSchema(_ context.Context, req *pspb.CommitSchemaRequest) (*pspb.Schema, error) {
	name := req.GetName()
	if _, ok := psSchemaHead(name); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	sch := psSchemaFromProto(req.GetSchema())
	rev := PSSchema{
		Name:               name,
		Type:               sch.Type,
		Definition:         sch.Definition,
		RevisionId:         psNewRevisionID(),
		RevisionCreateTime: nowTimestamp(),
	}
	psSchemaRevisions.Put(psSchemaRevisionKey(name, rev.RevisionId), rev)
	return psSchemaToProto(rev), nil
}

func (s *pubsubSchemaGRPC) RollbackSchema(_ context.Context, req *pspb.RollbackSchemaRequest) (*pspb.Schema, error) {
	name := req.GetName()
	src, ok := psSchemaRevisions.Get(psSchemaRevisionKey(name, req.GetRevisionId()))
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s@%s)", name, req.GetRevisionId())
	}
	rev := PSSchema{
		Name:               name,
		Type:               src.Type,
		Definition:         src.Definition,
		RevisionId:         psNewRevisionID(),
		RevisionCreateTime: nowTimestamp(),
	}
	psSchemaRevisions.Put(psSchemaRevisionKey(name, rev.RevisionId), rev)
	return psSchemaToProto(rev), nil
}

func (s *pubsubSchemaGRPC) DeleteSchemaRevision(_ context.Context, req *pspb.DeleteSchemaRevisionRequest) (*pspb.Schema, error) {
	name := req.GetName()
	base, revID := psSplitSchemaRef(name)
	revs := psSchemaRevisionsFor(base)
	if len(revs) == 0 {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", base)
	}
	if revID == "" {
		return nil, status.Error(codes.InvalidArgument, "revision id is required in the schema name")
	}
	if len(revs) == 1 {
		return nil, status.Error(codes.FailedPrecondition, "cannot delete the last revision of a schema")
	}
	if _, ok := psSchemaRevisions.Get(psSchemaRevisionKey(base, revID)); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s@%s)", base, revID)
	}
	psSchemaRevisions.Delete(psSchemaRevisionKey(base, revID))
	head, _ := psSchemaHead(base)
	return psSchemaToProto(head), nil
}

// psNormalizeProject returns the bare project ID from "proj" or the
// "projects/proj" the gRPC client sends.
func psNormalizeProject(s string) string {
	s = strings.TrimPrefix(s, "projects/")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	return s
}

// psGRPCPage pages the resources of store named under prefix in name order,
// each converted by toProto.
func psGRPCPage[T, P any](store sim.Store[T], prefix string, name func(T) string, pageSize int32, token string, toProto func(T) P) ([]P, string, error) {
	var all []T
	for _, v := range store.List() {
		if strings.HasPrefix(name(v), prefix) {
			all = append(all, v)
		}
	}
	page, next, err := grpcKeyPage(all, pageSize, token, gcpAsc(name))
	if err != nil {
		return nil, "", err
	}
	out := make([]P, 0, len(page))
	for _, v := range page {
		out = append(out, toProto(v))
	}
	return out, next, nil
}

// psTopicAttachedPage pages the names of the resources of store attached to
// topic, which ref reads with each resource's name, in name order.
func psTopicAttachedPage[T any](topic string, store sim.Store[T], ref func(T) (topic, name string), pageSize int32, token string) ([]string, string, error) {
	if _, ok := psTopics.Get(topic); !ok {
		return nil, "", status.Errorf(codes.NotFound, "Resource not found (resource=%s)", topic)
	}
	var names []string
	for _, v := range store.List() {
		if t, name := ref(v); t == topic {
			names = append(names, name)
		}
	}
	return grpcKeyPage(names, pageSize, token, gcpAsc(func(name string) string { return name }))
}

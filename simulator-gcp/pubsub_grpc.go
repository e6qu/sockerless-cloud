package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	pspb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Cloud Pub/Sub v1 gRPC data plane. The REST slice owns the stores (topics,
// subscriptions, queues, inflight, snapshots, schema revisions); every RPC here
// reads and writes those same stores so the REST and gRPC surfaces observe one
// consistent cloud state. The fan-out, dequeue, ack, and modack mechanics are
// shared — no delivery logic is duplicated here.
//
// The high-level cloud.google.com/go/pubsub client is gRPC-only and reaches the
// simulator through PUBSUB_EMULATOR_HOST, the same coordinate it uses to reach
// Google's own Pub/Sub emulator. Pull, StreamingPull, Acknowledge,
// ModifyAckDeadline, and the background ack-deadline sweeper together model
// real at-least-once delivery: a message that is not acknowledged before its
// deadline returns to the subscription's queue and is redelivered.

// ---------------------------------------------------------------------------
// server types + registration
// ---------------------------------------------------------------------------

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

// pubsubStartAckDeadlineSweeper starts the sweeper, once per process.
//
// It runs from the point the simulator begins serving rather than from
// registration, because registration must only mount handlers. Anything that
// enumerates the mounted surface without serving it — the gRPC coverage
// ratchet does exactly that, and the route conformance tests build a
// simulator for the same reason — would otherwise set another sweeper running
// against the same stores, racing the first.
var pubsubSweeperOnce sync.Once

func pubsubStartAckDeadlineSweeper() {
	pubsubSweeperOnce.Do(func() { go pubsubAckDeadlineSweeper() })
}

// pubsubAckDeadlineSweeper periodically returns inflight messages whose ack
// deadline has elapsed to their subscription's queue, implementing at-least-once
// delivery. It serves both the REST and gRPC surfaces, since they share the
// inflight store.
func pubsubAckDeadlineSweeper() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		for _, m := range psInFlight.List() {
			if m.AckDeadline.After(now) {
				continue
			}
			// Return the message to the head of its subscription's queue so the
			// next pull (or streaming pull) redelivers it.
			if !psQueues.Update(m.Subscription, func(q *psQueue) {
				q.Messages = append([]PSMessage{m.Message}, q.Messages...)
			}) {
				psQueues.Put(m.Subscription, psQueue{Subscription: m.Subscription, Messages: []PSMessage{m.Message}})
			}
			psInFlight.Delete(m.AckId)
		}
	}
}

// psSnapshotBacklogs holds the per-snapshot message backlog captured at
// CreateSnapshot time, so a Seek to that snapshot replays exactly the messages
// that were outstanding in the subscription when the snapshot was taken. This
// mirrors real Pub/Sub's seek-replay semantics. Snapshots are durable
// resources, so the captured backlog is stored alongside them (registerPubSub
// wires the store, keyed like psSnapshots) and is deleted with its snapshot.
var psSnapshotBacklogs sim.Store[[]PSMessage]

// ---------------------------------------------------------------------------
// proto <-> REST-store converters
// ---------------------------------------------------------------------------

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
		Data:       data,
		Attributes: m.Attributes,
		MessageId:  m.MessageId,
	}
	if m.PublishTime != "" {
		out.PublishTime = psRFC3339ToProto(m.PublishTime)
	}
	return out
}

func psMessageFromProto(m *pspb.PubsubMessage) PSMessage {
	out := PSMessage{
		MessageId:  m.GetMessageId(),
		Attributes: m.GetAttributes(),
	}
	if len(m.GetData()) > 0 {
		out.Data = base64.StdEncoding.EncodeToString(m.GetData())
	}
	if m.GetPublishTime() != nil {
		out.PublishTime = psRFC3339FromProto(m.GetPublishTime())
	}
	return out
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

// psDurationToProto parses a REST "604800s"-style duration string. The REST
// slice stores durations verbatim as Go time.Duration string forms; this helper
// accepts both the "Ns" canonical form and Go's default Duration string.
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

// psSchemaSettingsToJSON persists a proto SchemaSettings as canonical proto JSON
// inside the REST store's json.RawMessage field, preserving the field verbatim
// across create→get round-trips.
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

// ---------------------------------------------------------------------------
// shared delivery mechanics (operate on the REST-owned stores)
// ---------------------------------------------------------------------------

// psPublishMessages fans a batch of messages out to every subscription on the
// topic, assigning each a fresh messageId + publishTime. Returns the assigned
// ids in publish order.
func psPublishMessages(tName string, msgs []PSMessage) ([]string, error) {
	if _, ok := psTopics.Get(tName); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", tName)
	}
	var ids []string
	now := nowTimestamp()
	for _, m := range msgs {
		id := generateUUIDLocal()
		m.MessageId = id
		m.PublishTime = now
		ids = append(ids, id)
		for _, sub := range psSubscriptions.List() {
			if sub.Topic != tName {
				continue
			}
			if !psQueues.Update(sub.Name, func(q *psQueue) {
				q.Subscription = sub.Name
				q.Messages = append(q.Messages, m)
			}) {
				psQueues.Put(sub.Name, psQueue{Subscription: sub.Name, Messages: []PSMessage{m}})
			}
		}
	}
	return ids, nil
}

// psDequeue delivers up to max messages from the subscription's queue, recording
// each as inflight with a fresh ackId and the subscription's ack deadline.
func psDequeue(subName string, max int) (out []psDelivered, err error) {
	s, ok := psSubscriptions.Get(subName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", subName)
	}
	if s.Detached {
		return nil, status.Errorf(codes.FailedPrecondition, "subscription %s is detached", subName)
	}
	if max <= 0 {
		max = 1
	}
	q, _ := psQueues.Get(subName)
	if len(q.Messages) == 0 {
		return nil, nil
	}
	n := max
	if n > len(q.Messages) {
		n = len(q.Messages)
	}
	picked := q.Messages[:n]
	rest := q.Messages[n:]
	q.Messages = rest
	psQueues.Put(subName, q)

	now := time.Now()
	deadline := now.Add(time.Duration(s.AckDeadlineSeconds) * time.Second)
	for _, m := range picked {
		ackID := generateUUIDLocal()
		psInFlight.Put(ackID, PSDeliveredMessage{
			AckId:        ackID,
			Subscription: subName,
			Message:      m,
			DeliveredAt:  now,
			AckDeadline:  deadline,
		})
		out = append(out, psDelivered{AckID: ackID, Message: m})
	}
	return out, nil
}

type psDelivered struct {
	AckID   string
	Message PSMessage
}

// psAcknowledge removes the given ackIds from the inflight store.
func psAcknowledge(ackIds []string) {
	for _, id := range ackIds {
		psInFlight.Delete(id)
	}
}

// psModifyAckDeadline adjusts the deadline of each inflight message named by
// ackIds relative to now.
func psModifyAckDeadline(ackIds []string, seconds int32) {
	dur := time.Duration(seconds) * time.Second
	newDeadline := time.Now().Add(dur)
	for _, id := range ackIds {
		psInFlight.Update(id, func(m *PSDeliveredMessage) {
			m.AckDeadline = newDeadline
		})
	}
}

// ---------------------------------------------------------------------------
// Publisher RPCs
// ---------------------------------------------------------------------------

func (s *pubsubPublisherGRPC) CreateTopic(_ context.Context, req *pspb.Topic) (*pspb.Topic, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if _, exists := psTopics.Get(name); exists {
		return nil, status.Errorf(codes.AlreadyExists, "Resource already exists (resource=%s)", name)
	}
	t := psTopicFromProto(req)
	psTopics.Put(name, t)
	return psTopicToProto(t), nil
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
			switch p {
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
	project := psNormalizeProject(req.GetProject())
	prefix := fmt.Sprintf("projects/%s/topics/", project)
	var all []PSTopic
	for _, t := range psTopics.List() {
		if strings.HasPrefix(t.Name, prefix) {
			all = append(all, t)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	start, end := psPaging(len(all), req.GetPageSize(), req.GetPageToken())
	page := all[start:end]
	resp := &pspb.ListTopicsResponse{Topics: make([]*pspb.Topic, 0, len(page))}
	for _, t := range page {
		resp.Topics = append(resp.Topics, psTopicToProto(t))
	}
	if end < len(all) {
		resp.NextPageToken = fmt.Sprintf("%d", end)
	}
	return resp, nil
}

func (s *pubsubPublisherGRPC) ListTopicSubscriptions(_ context.Context, req *pspb.ListTopicSubscriptionsRequest) (*pspb.ListTopicSubscriptionsResponse, error) {
	tName := req.GetTopic()
	if _, ok := psTopics.Get(tName); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", tName)
	}
	var names []string
	for _, sub := range psSubscriptions.List() {
		if sub.Topic == tName {
			names = append(names, sub.Name)
		}
	}
	sort.Strings(names)
	start, end := psPaging(len(names), req.GetPageSize(), req.GetPageToken())
	resp := &pspb.ListTopicSubscriptionsResponse{Subscriptions: names[start:end]}
	if end < len(names) {
		resp.NextPageToken = fmt.Sprintf("%d", end)
	}
	return resp, nil
}

func (s *pubsubPublisherGRPC) ListTopicSnapshots(_ context.Context, req *pspb.ListTopicSnapshotsRequest) (*pspb.ListTopicSnapshotsResponse, error) {
	tName := req.GetTopic()
	if _, ok := psTopics.Get(tName); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", tName)
	}
	var names []string
	for _, snap := range psSnapshots.List() {
		if snap.Topic == tName {
			names = append(names, snap.Name)
		}
	}
	sort.Strings(names)
	start, end := psPaging(len(names), req.GetPageSize(), req.GetPageToken())
	resp := &pspb.ListTopicSnapshotsResponse{Snapshots: names[start:end]}
	if end < len(names) {
		resp.NextPageToken = fmt.Sprintf("%d", end)
	}
	return resp, nil
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
	return &pspb.DetachSubscriptionResponse{}, nil
}

// ---------------------------------------------------------------------------
// Subscriber RPCs
// ---------------------------------------------------------------------------

func (s *pubsubSubscriberGRPC) CreateSubscription(_ context.Context, req *pspb.Subscription) (*pspb.Subscription, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	topic := req.GetTopic()
	if topic == "" {
		return nil, status.Error(codes.InvalidArgument, "topic is required")
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
			switch p {
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
			}
		}
	} else {
		existing = updated
	}
	psSubscriptions.Put(name, existing)
	return psSubscriptionToProto(existing), nil
}

func (s *pubsubSubscriberGRPC) ListSubscriptions(_ context.Context, req *pspb.ListSubscriptionsRequest) (*pspb.ListSubscriptionsResponse, error) {
	project := psNormalizeProject(req.GetProject())
	prefix := fmt.Sprintf("projects/%s/subscriptions/", project)
	var all []PSSubscription
	for _, sub := range psSubscriptions.List() {
		if strings.HasPrefix(sub.Name, prefix) {
			all = append(all, sub)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	start, end := psPaging(len(all), req.GetPageSize(), req.GetPageToken())
	page := all[start:end]
	resp := &pspb.ListSubscriptionsResponse{Subscriptions: make([]*pspb.Subscription, 0, len(page))}
	for _, sub := range page {
		resp.Subscriptions = append(resp.Subscriptions, psSubscriptionToProto(sub))
	}
	if end < len(all) {
		resp.NextPageToken = fmt.Sprintf("%d", end)
	}
	return resp, nil
}

func (s *pubsubSubscriberGRPC) DeleteSubscription(_ context.Context, req *pspb.DeleteSubscriptionRequest) (*emptypb.Empty, error) {
	name := req.GetSubscription()
	if !psSubscriptions.Delete(name) {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", name)
	}
	psQueues.Delete(name)
	return &emptypb.Empty{}, nil
}

func (s *pubsubSubscriberGRPC) ModifyAckDeadline(_ context.Context, req *pspb.ModifyAckDeadlineRequest) (*emptypb.Empty, error) {
	sub := req.GetSubscription()
	if _, ok := psSubscriptions.Get(sub); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", sub)
	}
	psModifyAckDeadline(req.GetAckIds(), req.GetAckDeadlineSeconds())
	return &emptypb.Empty{}, nil
}

func (s *pubsubSubscriberGRPC) Acknowledge(_ context.Context, req *pspb.AcknowledgeRequest) (*emptypb.Empty, error) {
	sub := req.GetSubscription()
	if _, ok := psSubscriptions.Get(sub); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", sub)
	}
	psAcknowledge(req.GetAckIds())
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
	return &emptypb.Empty{}, nil
}

func (s *pubsubSubscriberGRPC) Pull(_ context.Context, req *pspb.PullRequest) (*pspb.PullResponse, error) {
	delivered, err := psDequeue(req.GetSubscription(), int(req.GetMaxMessages()))
	if err != nil {
		return nil, err
	}
	// Real Pub/Sub blocks until a message is available (or the deadline elapses)
	// when returnImmediately is false. The high-level client polls in a loop, so
	// returning promptly when the queue is empty is faithful to the emulator's
	// observable behaviour and avoids holding an RPC open.
	resp := &pspb.PullResponse{ReceivedMessages: make([]*pspb.ReceivedMessage, 0, len(delivered))}
	for _, d := range delivered {
		resp.ReceivedMessages = append(resp.ReceivedMessages, &pspb.ReceivedMessage{
			AckId:   d.AckID,
			Message: psMessageToProto(d.Message),
		})
	}
	return resp, nil
}

// StreamingPull is the high-level client's default delivery path. The first
// request on the stream names the subscription and its flow-control / deadline
// parameters; subsequent requests carry ack and modack batches. The server keeps
// the stream open, delivering messages as they appear in the queue, until the
// client cancels the context.
func (s *pubsubSubscriberGRPC) StreamingPull(stream pspb.Subscriber_StreamingPullServer) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	subName := first.GetSubscription()
	if _, ok := psSubscriptions.Get(subName); !ok {
		return status.Errorf(codes.NotFound, "Resource not found (resource=%s)", subName)
	}
	if _, ok := psQueues.Get(subName); !ok {
		psQueues.Put(subName, psQueue{Subscription: subName})
	}

	maxOutstanding := int(first.GetMaxOutstandingMessages())
	if maxOutstanding <= 0 {
		maxOutstanding = 1000
	}

	// Reader loop: applies acks and modacks embedded in client messages, and
	// signals shutdown when the stream closes.
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- psStreamingPullReadLoop(stream)
	}()

	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	for {
		// Count messages currently inflight for this subscription so flow control
		// is honoured — the client will not ack faster than its handlers run, so
		// capping outstanding delivery prevents flooding it.
		outstanding := 0
		for _, m := range psInFlight.List() {
			if m.Subscription == subName {
				outstanding++
			}
		}
		budget := maxOutstanding - outstanding
		if budget > 0 {
			delivered, derr := psDequeue(subName, budget)
			if derr != nil {
				return derr
			}
			if len(delivered) > 0 {
				rm := make([]*pspb.ReceivedMessage, 0, len(delivered))
				for _, d := range delivered {
					rm = append(rm, &pspb.ReceivedMessage{
						AckId:   d.AckID,
						Message: psMessageToProto(d.Message),
					})
				}
				if err := stream.Send(&pspb.StreamingPullResponse{ReceivedMessages: rm}); err != nil {
					return err
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-clientErr:
			return e
		case <-tick.C:
		}
	}
}

// psStreamingPullReadLoop consumes client-side StreamingPullRequests and applies
// the ack and modack batches they carry. It returns when the client closes the
// stream or the context is cancelled.
func psStreamingPullReadLoop(stream pspb.Subscriber_StreamingPullServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		if ids := req.GetAckIds(); len(ids) > 0 {
			psAcknowledge(ids)
		}
		secs := req.GetModifyDeadlineSeconds()
		ackIDs := req.GetModifyDeadlineAckIds()
		for i, id := range ackIDs {
			sec := int32(0)
			if i < len(secs) {
				sec = secs[i]
			}
			psModifyAckDeadline([]string{id}, sec)
		}
	}
}

// ---------------------------------------------------------------------------
// Seek + Snapshots
// ---------------------------------------------------------------------------

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
		// Replay the backlog captured when the snapshot was created: requeue every
		// captured message ahead of the subscription's current queue so a
		// subsequent pull redelivers them (at-least-once replay).
		backlog, _ := psSnapshotBacklogs.Get(key)
		if len(backlog) > 0 {
			restored := make([]PSMessage, len(backlog))
			copy(restored, backlog)
			if q, ok := psQueues.Get(subName); ok {
				restored = append(restored, q.Messages...)
			}
			psQueues.Put(subName, psQueue{Subscription: subName, Messages: restored})
		}
		return &pspb.SeekResponse{}, nil
	case *pspb.SeekRequest_Time:
		// A time seek to the epoch replays the full captured backlog of any
		// snapshot on the subscription, modelling "seek to earliest"; a future
		// time is a no-op replay of the current backlog.
		if target.Time != nil && target.Time.AsTime().Before(time.Unix(0, 0)) || (target.Time != nil && target.Time.AsTime().Equal(time.Unix(0, 0))) {
			var backlog []PSMessage
			for _, snap := range psSnapshots.List() {
				k := psSnapshotKeyFromName(snap.Name)
				if b, ok := psSnapshotBacklogs.Get(k); ok && len(b) > 0 {
					backlog = append(backlog, b...)
				}
			}
			if len(backlog) > 0 {
				if q, ok := psQueues.Get(subName); ok {
					backlog = append(backlog, q.Messages...)
				}
				psQueues.Put(subName, psQueue{Subscription: subName, Messages: backlog})
			}
		}
		return &pspb.SeekResponse{}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "seek target (snapshot or time) is required")
	}
}

// psSnapshotKeyFromName derives the store key for a snapshot resource name.
// Snapshot store keys are project/name, mirroring the REST slice's helper.
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
	if q, ok := psQueues.Get(sub); ok && len(q.Messages) > 0 {
		captured := make([]PSMessage, len(q.Messages))
		copy(captured, q.Messages)
		psSnapshotBacklogs.Put(key, captured)
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
			switch p {
			case "labels":
				existing.Labels = updated.Labels
			case "expireTime":
				existing.ExpireTime = updated.ExpireTime
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
	project := psNormalizeProject(req.GetProject())
	prefix := fmt.Sprintf("projects/%s/snapshots/", project)
	var all []PSSnapshot
	for _, snap := range psSnapshots.List() {
		if strings.HasPrefix(snap.Name, prefix) {
			all = append(all, snap)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	start, end := psPaging(len(all), req.GetPageSize(), req.GetPageToken())
	page := all[start:end]
	resp := &pspb.ListSnapshotsResponse{Snapshots: make([]*pspb.Snapshot, 0, len(page))}
	for _, snap := range page {
		resp.Snapshots = append(resp.Snapshots, psSnapshotToProto(snap))
	}
	if end < len(all) {
		resp.NextPageToken = fmt.Sprintf("%d", end)
	}
	return resp, nil
}

func (s *pubsubSubscriberGRPC) DeleteSnapshot(_ context.Context, req *pspb.DeleteSnapshotRequest) (*emptypb.Empty, error) {
	key := psSnapshotKeyFromName(req.GetSnapshot())
	if !psSnapshots.Delete(key) {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", req.GetSnapshot())
	}
	psSnapshotBacklogs.Delete(key)
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// SchemaService RPCs
// ---------------------------------------------------------------------------

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
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	start, end := psPaging(len(all), req.GetPageSize(), req.GetPageToken())
	page := all[start:end]
	resp := &pspb.ListSchemasResponse{Schemas: make([]*pspb.Schema, 0, len(page))}
	for _, sc := range page {
		resp.Schemas = append(resp.Schemas, psSchemaToProto(sc))
	}
	if end < len(all) {
		resp.NextPageToken = fmt.Sprintf("%d", end)
	}
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
	start, end := psPaging(len(revs), req.GetPageSize(), req.GetPageToken())
	page := revs[start:end]
	resp := &pspb.ListSchemaRevisionsResponse{Schemas: make([]*pspb.Schema, 0, len(page))}
	for _, sc := range page {
		resp.Schemas = append(resp.Schemas, psSchemaToProto(sc))
	}
	if end < len(revs) {
		resp.NextPageToken = fmt.Sprintf("%d", end)
	}
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

// psNormalizeProject accepts either a bare project id ("proj") or the full
// resource path form ("projects/proj") the gRPC client sends in the
// project/parent fields, and returns the bare id.
func psNormalizeProject(s string) string {
	s = strings.TrimPrefix(s, "projects/")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------
// paging helper
// ---------------------------------------------------------------------------

func psPaging(total int, pageSize int32, pageToken string) (start, end int) {
	end = total
	if pageToken != "" {
		var n int
		if _, err := fmt.Sscanf(pageToken, "%d", &n); err == nil && n >= 0 && n <= total {
			start = n
		} else {
			start = 0
		}
	}
	if size := int(pageSize); size > 0 && start+size < end {
		end = start + size
	}
	return start, end
}

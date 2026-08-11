package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Cloud Pub/Sub v1 — REST surface scoped to topic + subscription
// CRUD plus publish / pull / acknowledge / modifyAckDeadline. The
// real API exposes ~30 ops; sim implements the load-bearing slice
// for fan-out / fan-in integration tests. No LRO — every op
// returns synchronously.
//
// Wire paths (per discovery doc, https://pubsub.googleapis.com/$discovery/rest):
//
//	PUT    /v1/projects/{p}/topics/{t}                            CreateTopic
//	GET    /v1/projects/{p}/topics/{t}                            GetTopic
//	GET    /v1/projects/{p}/topics                                ListTopics
//	DELETE /v1/projects/{p}/topics/{t}                            DeleteTopic
//	POST   /v1/projects/{p}/topics/{t}:publish                    Publish
//	PUT    /v1/projects/{p}/subscriptions/{s}                     CreateSubscription
//	GET    /v1/projects/{p}/subscriptions/{s}                     GetSubscription
//	GET    /v1/projects/{p}/subscriptions                         ListSubscriptions
//	DELETE /v1/projects/{p}/subscriptions/{s}                     DeleteSubscription
//	POST   /v1/projects/{p}/subscriptions/{s}:pull                Pull
//	POST   /v1/projects/{p}/subscriptions/{s}:acknowledge         Acknowledge
//	POST   /v1/projects/{p}/subscriptions/{s}:modifyAckDeadline   ModifyAckDeadline
//
// The `:verb` suffix dispatch uses the same pattern as
// `simulator-gcp/secretmanager.go`: register one POST route with
// a wildcard, then strip-and-switch inside the handler.

type PSTopic struct {
	Name                     string                  `json:"name"` // projects/{p}/topics/{t}
	Labels                   map[string]string       `json:"labels,omitempty"`
	KmsKeyName               string                  `json:"kmsKeyName,omitempty"`
	MessageRetentionDuration string                  `json:"messageRetentionDuration,omitempty"`
	MessageStoragePolicy     *PSMessageStoragePolicy `json:"messageStoragePolicy,omitempty"`
	// SchemaSettings is a nested writable object the sim persists
	// verbatim so create→get round-trips byte-exact.
	SchemaSettings json.RawMessage `json:"schemaSettings,omitempty"`
}

type PSMessageStoragePolicy struct {
	AllowedPersistenceRegions []string `json:"allowedPersistenceRegions,omitempty"`
}

type PSSubscription struct {
	Name                     string              `json:"name"` // projects/{p}/subscriptions/{s}
	Topic                    string              `json:"topic"`
	AckDeadlineSeconds       int                 `json:"ackDeadlineSeconds,omitempty"`
	Labels                   map[string]string   `json:"labels,omitempty"`
	PushConfig               *PSPushConfig       `json:"pushConfig,omitempty"`
	MessageRetentionDuration string              `json:"messageRetentionDuration,omitempty"`
	RetainAckedMessages      bool                `json:"retainAckedMessages,omitempty"`
	ExpirationPolicy         *PSExpirationPolicy `json:"expirationPolicy,omitempty"`
	EnableMessageOrdering    bool                `json:"enableMessageOrdering,omitempty"`
	Filter                   string              `json:"filter,omitempty"`
	DeadLetterPolicy         *PSDeadLetterPolicy `json:"deadLetterPolicy,omitempty"`
	RetryPolicy              *PSRetryPolicy      `json:"retryPolicy,omitempty"`
	Detached                 bool                `json:"detached,omitempty"`
}

type PSExpirationPolicy struct {
	Ttl string `json:"ttl,omitempty"`
}

type PSDeadLetterPolicy struct {
	DeadLetterTopic     string `json:"deadLetterTopic,omitempty"`
	MaxDeliveryAttempts int    `json:"maxDeliveryAttempts,omitempty"`
}

type PSRetryPolicy struct {
	MinimumBackoff string `json:"minimumBackoff,omitempty"`
	MaximumBackoff string `json:"maximumBackoff,omitempty"`
}

type PSPushConfig struct {
	PushEndpoint string            `json:"pushEndpoint,omitempty"` // external (operator-supplied): webhook target for Push subscriptions; sim doesn't deliver
	Attributes   map[string]string `json:"attributes,omitempty"`
	OidcToken    *PSOidcToken      `json:"oidcToken,omitempty"`
}

type PSOidcToken struct {
	ServiceAccountEmail string `json:"serviceAccountEmail,omitempty"`
	Audience            string `json:"audience,omitempty"`
}

type PSMessage struct {
	MessageId   string            `json:"messageId"`
	PublishTime string            `json:"publishTime"`
	Data        string            `json:"data,omitempty"` // base64 (per API)
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// PSDeliveredMessage tracks an in-flight pulled message awaiting
// acknowledge. AckId is unique per pull.
type PSDeliveredMessage struct {
	AckId        string
	Subscription string
	Message      PSMessage
	DeliveredAt  time.Time
	AckDeadline  time.Time
}

var (
	psTopics        sim.Store[PSTopic]
	psSubscriptions sim.Store[PSSubscription]
	// Per-subscription queues (FIFO, in-memory).
	psQueues sim.Store[psQueue]
	// In-flight pulled messages keyed by ackId.
	psInFlight sim.Store[PSDeliveredMessage]
)

// psQueue holds the pending messages for a subscription. Wrapping
// in a struct so it round-trips through the JSON-serializing Store.
type psQueue struct {
	Subscription string
	Messages     []PSMessage
}

func registerPubSub(srv *sim.Server) {
	psTopics = sim.MakeStore[PSTopic](srv.DB(), "pubsub_topics")
	psSubscriptions = sim.MakeStore[PSSubscription](srv.DB(), "pubsub_subscriptions")
	psQueues = sim.MakeStore[psQueue](srv.DB(), "pubsub_queues")
	psInFlight = sim.MakeStore[PSDeliveredMessage](srv.DB(), "pubsub_inflight")
	psSnapshots = sim.MakeStore[PSSnapshot](srv.DB(), "pubsub_snapshots")
	psSnapshotBacklogs = sim.MakeStore[[]PSMessage](srv.DB(), "pubsub_snapshot_backlogs")
	psSchemaRevisions = sim.MakeStore[PSSchema](srv.DB(), "pubsub_schema_revisions")

	// Topics.
	srv.HandleFunc("PUT /v1/projects/{project}/topics/{topic}", handlePSCreateTopic)
	srv.HandleFunc("GET /v1/projects/{project}/topics/{topic}", handlePSGetTopic)
	srv.HandleFunc("GET /v1/projects/{project}/topics", handlePSListTopics)
	srv.HandleFunc("DELETE /v1/projects/{project}/topics/{topic}", handlePSDeleteTopic)
	srv.HandleFunc("POST /v1/projects/{project}/topics/{topicVerb}", handlePSTopicVerb)

	// Subscriptions.
	srv.HandleFunc("PUT /v1/projects/{project}/subscriptions/{sub}", handlePSCreateSubscription)
	srv.HandleFunc("PATCH /v1/projects/{project}/subscriptions/{sub}", handlePSPatchSubscription)
	srv.HandleFunc("GET /v1/projects/{project}/subscriptions/{sub}", handlePSGetSubscription)
	srv.HandleFunc("GET /v1/projects/{project}/subscriptions", handlePSListSubscriptions)
	srv.HandleFunc("DELETE /v1/projects/{project}/subscriptions/{sub}", handlePSDeleteSubscription)
	srv.HandleFunc("POST /v1/projects/{project}/subscriptions/{subVerb}", handlePSSubscriptionVerb)

	// Topic PATCH is wired alongside Subscription PATCH per the
	// `projects.topics.patch` REST verb. Less commonly hit (the only
	// mutable field today is `labels`) but the SDK + terraform provider
	// both emit it on update.
	srv.HandleFunc("PATCH /v1/projects/{project}/topics/{topic}", handlePSPatchTopic)

	// Topic sub-collections — the names of the snapshots and
	// subscriptions that reference a topic. Derived from the existing
	// snapshot and subscription stores. Registered before the
	// `{topic}` GET so the more specific path wins.
	srv.HandleFunc("GET /v1/projects/{project}/topics/{topic}/snapshots", handlePSListTopicSnapshots)
	srv.HandleFunc("GET /v1/projects/{project}/topics/{topic}/subscriptions", handlePSListTopicSubscriptions)

	// Snapshots — per-subscription point-in-time markers used for
	// Seek operations. The sim doesn't replay messages from the
	// snapshot point, but the CRUD round-trips so terraform-provider-google's
	// google_pubsub_subscription with snapshot tracking + SDK code
	// that creates snapshots between deploys both work.
	srv.HandleFunc("PUT /v1/projects/{project}/snapshots/{snap}", handlePSCreateSnapshot)
	srv.HandleFunc("PATCH /v1/projects/{project}/snapshots/{snap}", handlePSPatchSnapshot)
	srv.HandleFunc("GET /v1/projects/{project}/snapshots/{snap}", handlePSGetSnapshot)
	srv.HandleFunc("GET /v1/projects/{project}/snapshots", handlePSListSnapshots)
	srv.HandleFunc("DELETE /v1/projects/{project}/snapshots/{snap}", handlePSDeleteSnapshot)
	srv.HandleFunc("POST /v1/projects/{project}/snapshots/{snapVerb}", handlePSSnapshotVerb)

	// Schemas — a first-class resource (Protocol Buffer / Avro schema
	// definitions) with its own revision history. CRUD plus the
	// revision verbs (commit / rollback / listRevisions / deleteRevision),
	// the two collection-level validation verbs (validate / validateMessage),
	// and the AIP-141 IAM cluster.
	srv.HandleFunc("POST /v1/projects/{project}/schemas", handlePSCreateSchema)
	srv.HandleFunc("POST /v1/projects/{project}/schemas:validate", handlePSValidateSchema)
	srv.HandleFunc("POST /v1/projects/{project}/schemas:validateMessage", handlePSValidateMessage)
	srv.HandleFunc("GET /v1/projects/{project}/schemas", handlePSListSchemas)
	srv.HandleFunc("GET /v1/projects/{project}/schemas/{schemaVerb}", handlePSGetSchemaOrVerb)
	srv.HandleFunc("POST /v1/projects/{project}/schemas/{schemaVerb}", handlePSSchemaPostVerb)
	srv.HandleFunc("DELETE /v1/projects/{project}/schemas/{schemaVerb}", handlePSDeleteSchemaOrRevision)
}

// PSSnapshot is the per-snapshot wire shape. ExpireTime is set
// inline to (creation + 7d) because real Pub/Sub holds snapshots
// for at most 7 days; the field is load-bearing on real consumers'
// Seek logic.
type PSSnapshot struct {
	Name       string            `json:"name"`
	Topic      string            `json:"topic"`
	ExpireTime string            `json:"expireTime"`
	Labels     map[string]string `json:"labels,omitempty"`
}

var psSnapshots sim.Store[PSSnapshot]

func psSnapshotKey(project, name string) string {
	return project + "/" + name
}

func handlePSCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	snap := sim.PathParam(r, "snap")
	var req struct {
		Subscription string            `json:"subscription"`
		Labels       map[string]string `json:"labels,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	// Real Pub/Sub validates the subscription exists; sim defers
	// (sub may not be in the store yet for a brand-new project).
	now := time.Now().UTC()
	expire := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)
	// A snapshot records the TOPIC of the subscription it is taken from, not
	// the subscription name; resolve it from the subscription store.
	topic := req.Subscription
	if sub, ok := psSubscriptions.Get(req.Subscription); ok {
		topic = sub.Topic
	}
	s := PSSnapshot{
		Name:       "projects/" + project + "/snapshots/" + snap,
		Topic:      topic,
		ExpireTime: expire,
		Labels:     req.Labels,
	}
	// Capture the subscription's outstanding backlog so a later Seek to this
	// snapshot replays exactly these messages — the same semantics as the
	// gRPC CreateSnapshot and real Pub/Sub.
	if q, ok := psQueues.Get(req.Subscription); ok && len(q.Messages) > 0 {
		captured := make([]PSMessage, len(q.Messages))
		copy(captured, q.Messages)
		psSnapshotBacklogs.Put(psSnapshotKey(project, snap), captured)
	}
	psSnapshots.Put(psSnapshotKey(project, snap), s)
	sim.WriteJSON(w, http.StatusOK, s)
}

func handlePSGetSnapshot(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	snap := sim.PathParam(r, "snap")
	if id, verb, ok := strings.Cut(snap, ":"); ok {
		if verb == "getIamPolicy" {
			handleResourceIAM(w, r, gcpResourceIAMStore(),
				"projects/"+project+"/snapshots/"+id, verb)
			return
		}
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown verb: "+verb)
		return
	}
	s, ok := psSnapshots.Get(psSnapshotKey(project, snap))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"snapshot %q not found", snap)
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handlePSListSnapshots(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	all := psSnapshots.Filter(func(s PSSnapshot) bool {
		return strings.HasPrefix(s.Name, "projects/"+project+"/snapshots/")
	})
	if all == nil {
		all = []PSSnapshot{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"snapshots": all})
}

func handlePSDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	snap := sim.PathParam(r, "snap")
	if !psSnapshots.Delete(psSnapshotKey(project, snap)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"snapshot %q not found", snap)
		return
	}
	psSnapshotBacklogs.Delete(psSnapshotKey(project, snap))
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handlePSListTopicSnapshots lists the names of the snapshots whose
// source topic is this topic. Per the Discovery contract the response
// carries snapshot *names* (strings), not full resources.
func handlePSListTopicSnapshots(w http.ResponseWriter, r *http.Request) {
	tName := psTopicName(sim.PathParam(r, "project"), sim.PathParam(r, "topic"))
	if _, ok := psTopics.Get(tName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Topic not found: "+tName)
		return
	}
	var names []string
	for _, s := range psSnapshots.List() {
		if s.Topic == tName {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	page, next, ok := paginateList(w, r, names)
	if !ok {
		return
	}
	if page == nil {
		page = []string{}
	}
	resp := map[string]any{"snapshots": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handlePSListTopicSubscriptions lists the names of the subscriptions
// attached to this topic. Response carries subscription *names*.
func handlePSListTopicSubscriptions(w http.ResponseWriter, r *http.Request) {
	tName := psTopicName(sim.PathParam(r, "project"), sim.PathParam(r, "topic"))
	if _, ok := psTopics.Get(tName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Topic not found: "+tName)
		return
	}
	var names []string
	for _, s := range psSubscriptions.List() {
		if s.Topic == tName {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	page, next, ok := paginateList(w, r, names)
	if !ok {
		return
	}
	if page == nil {
		page = []string{}
	}
	resp := map[string]any{"subscriptions": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handlePSPatchSnapshot is `projects.snapshots.patch`. Labels and
// expireTime are the mutable fields; the request wraps the resource in
// a `{"snapshot": {...}, "updateMask": "..."}` envelope.
func handlePSPatchSnapshot(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	snap := sim.PathParam(r, "snap")
	key := psSnapshotKey(project, snap)
	existing, ok := psSnapshots.Get(key)
	if !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "snapshot not found: "+snap)
		return
	}
	var req struct {
		Snapshot   PSSnapshot `json:"snapshot"`
		UpdateMask string     `json:"updateMask"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	for _, path := range splitMask(req.UpdateMask) {
		switch path {
		case "labels":
			existing.Labels = req.Snapshot.Labels
		case "expireTime":
			existing.ExpireTime = req.Snapshot.ExpireTime
		default:
			gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"unknown updateMask path: "+path)
			return
		}
	}
	psSnapshots.Put(key, existing)
	sim.WriteJSON(w, http.StatusOK, existing)
}

// handlePSSnapshotVerb routes the snapshot IAM verb cluster.
func handlePSSnapshotVerb(w http.ResponseWriter, r *http.Request) {
	sv := sim.PathParam(r, "snapVerb")
	parts := strings.SplitN(sv, ":", 2)
	if len(parts) != 2 {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Expected <snapshot>:<verb>")
		return
	}
	snap, verb := parts[0], parts[1]
	name := "projects/" + sim.PathParam(r, "project") + "/snapshots/" + snap
	switch verb {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, gcpResourceIAMStore(), name, verb)
	default:
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown verb: "+verb)
	}
}

func psTopicName(project, topic string) string {
	return fmt.Sprintf("projects/%s/topics/%s", project, topic)
}
func psSubName(project, sub string) string {
	return fmt.Sprintf("projects/%s/subscriptions/%s", project, sub)
}

func handlePSCreateTopic(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	topic := sim.PathParam(r, "topic")
	var req PSTopic
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	t := PSTopic{
		Name:                     psTopicName(project, topic),
		Labels:                   req.Labels,
		KmsKeyName:               req.KmsKeyName,
		MessageRetentionDuration: req.MessageRetentionDuration,
		MessageStoragePolicy:     req.MessageStoragePolicy,
		SchemaSettings:           req.SchemaSettings,
	}
	if _, exists := psTopics.Get(t.Name); exists {
		gcpError(w, http.StatusConflict, "ALREADY_EXISTS", "Topic already exists: "+t.Name)
		return
	}
	psTopics.Put(t.Name, t)
	sim.WriteJSON(w, http.StatusOK, t)
}

func handlePSGetTopic(w http.ResponseWriter, r *http.Request) {
	// projects.topics.getIamPolicy is a GET colon-verb that fans into the
	// same `{topic}` segment; dispatch it here before the resource read.
	if id, verb, ok := strings.Cut(sim.PathParam(r, "topic"), ":"); ok {
		if verb == "getIamPolicy" {
			handleResourceIAM(w, r, gcpResourceIAMStore(),
				psTopicName(sim.PathParam(r, "project"), id), verb)
			return
		}
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown verb: "+verb)
		return
	}
	name := psTopicName(sim.PathParam(r, "project"), sim.PathParam(r, "topic"))
	t, ok := psTopics.Get(name)
	if !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Topic not found: "+name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, t)
}

func handlePSListTopics(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	prefix := fmt.Sprintf("projects/%s/topics/", project)
	var all []PSTopic
	for _, t := range psTopics.List() {
		if strings.HasPrefix(t.Name, prefix) {
			all = append(all, t)
		}
	}
	if all == nil {
		all = []PSTopic{}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	page, next, ok := paginateList(w, r, all)
	if !ok {
		return
	}
	resp := map[string]any{"topics": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handlePSDeleteTopic(w http.ResponseWriter, r *http.Request) {
	name := psTopicName(sim.PathParam(r, "project"), sim.PathParam(r, "topic"))
	if !psTopics.Delete(name) {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Topic not found: "+name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePSTopicVerb(w http.ResponseWriter, r *http.Request) {
	tv := sim.PathParam(r, "topicVerb")
	parts := strings.SplitN(tv, ":", 2)
	if len(parts) != 2 {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Expected <topic>:<verb>")
		return
	}
	topic, verb := parts[0], parts[1]
	switch verb {
	case "publish":
		handlePSPublish(w, r, sim.PathParam(r, "project"), topic)
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		// Canonical AIP-141 three-verb cluster. Resource ID matches
		// the full topic name so policies set against the topic
		// round-trip via :getIamPolicy.
		topicName := psTopicName(sim.PathParam(r, "project"), topic)
		handleResourceIAM(w, r, gcpResourceIAMStore(), topicName, verb)
	default:
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown verb: "+verb)
	}
}

func handlePSPublish(w http.ResponseWriter, r *http.Request, project, topic string) {
	tName := psTopicName(project, topic)
	if _, ok := psTopics.Get(tName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Topic not found: "+tName)
		return
	}
	var req struct {
		Messages []PSMessage `json:"messages"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	var msgIds []string
	now := nowTimestamp()
	for _, m := range req.Messages {
		msgID := generateUUIDLocal()
		m.MessageId = msgID
		m.PublishTime = now
		msgIds = append(msgIds, msgID)
		// Fan out to every subscription on this topic.
		for _, sub := range psSubscriptions.List() {
			if sub.Topic != tName {
				continue
			}
			// The queue entry is created when the subscription is created, so
			// Update normally succeeds and appends atomically under the store
			// write lock. If the entry is ever absent (e.g. created before this
			// invariant existed), seed it with this message.
			if !psQueues.Update(sub.Name, func(q *psQueue) {
				q.Subscription = sub.Name
				q.Messages = append(q.Messages, m)
			}) {
				psQueues.Put(sub.Name, psQueue{Subscription: sub.Name, Messages: []PSMessage{m}})
			}
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"messageIds": msgIds})
}

func handlePSCreateSubscription(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	sub := sim.PathParam(r, "sub")
	var req PSSubscription
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if req.Topic == "" {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "topic is required")
		return
	}
	if _, ok := psTopics.Get(req.Topic); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Topic not found: "+req.Topic)
		return
	}
	if _, exists := psSubscriptions.Get(psSubName(project, sub)); exists {
		gcpError(w, http.StatusConflict, "ALREADY_EXISTS", "Subscription already exists: "+psSubName(project, sub))
		return
	}
	if req.AckDeadlineSeconds == 0 {
		req.AckDeadlineSeconds = 10
	}
	// Default messageRetentionDuration to real-GCP's 7-day default
	// when omitted. Other optional fields stay zero-valued (omitempty
	// JSON tag elides them on response).
	if req.MessageRetentionDuration == "" {
		req.MessageRetentionDuration = "604800s"
	}
	s := PSSubscription{
		Name:                     psSubName(project, sub),
		Topic:                    req.Topic,
		AckDeadlineSeconds:       req.AckDeadlineSeconds,
		Labels:                   req.Labels,
		PushConfig:               req.PushConfig,
		MessageRetentionDuration: req.MessageRetentionDuration,
		RetainAckedMessages:      req.RetainAckedMessages,
		ExpirationPolicy:         req.ExpirationPolicy,
		EnableMessageOrdering:    req.EnableMessageOrdering,
		Filter:                   req.Filter,
		DeadLetterPolicy:         req.DeadLetterPolicy,
		RetryPolicy:              req.RetryPolicy,
	}
	psSubscriptions.Put(s.Name, s)
	// Seed the delivery queue so concurrent publishes append atomically via
	// Update (which is a no-op when the key is absent).
	if _, ok := psQueues.Get(s.Name); !ok {
		psQueues.Put(s.Name, psQueue{Subscription: s.Name})
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handlePSGetSubscription(w http.ResponseWriter, r *http.Request) {
	if id, verb, ok := strings.Cut(sim.PathParam(r, "sub"), ":"); ok {
		if verb == "getIamPolicy" {
			handleResourceIAM(w, r, gcpResourceIAMStore(),
				psSubName(sim.PathParam(r, "project"), id), verb)
			return
		}
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown verb: "+verb)
		return
	}
	name := psSubName(sim.PathParam(r, "project"), sim.PathParam(r, "sub"))
	s, ok := psSubscriptions.Get(name)
	if !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

// handlePSPatchSubscription is `projects.subscriptions.patch` —
// the canonical update verb. Real GCP wraps the resource in a
// `{"subscription": {...}, "updateMask": "ackDeadlineSeconds,..."}`
// envelope where updateMask is a comma-separated list of field
// paths to update. Fields not in the mask retain prior values.
func handlePSPatchSubscription(w http.ResponseWriter, r *http.Request) {
	name := psSubName(sim.PathParam(r, "project"), sim.PathParam(r, "sub"))
	existing, ok := psSubscriptions.Get(name)
	if !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+name)
		return
	}
	var req struct {
		Subscription PSSubscription `json:"subscription"`
		UpdateMask   string         `json:"updateMask"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	// Apply each mask path. Unknown paths return 400 InvalidArgument
	// to match real GCP — a silent skip would hide caller bugs (a
	// typo in updateMask would persist as a no-op the caller assumed
	// applied).
	for _, path := range splitMask(req.UpdateMask) {
		switch path {
		case "ackDeadlineSeconds":
			existing.AckDeadlineSeconds = req.Subscription.AckDeadlineSeconds
		case "labels":
			existing.Labels = req.Subscription.Labels
		case "pushConfig":
			existing.PushConfig = req.Subscription.PushConfig
		case "messageRetentionDuration":
			existing.MessageRetentionDuration = req.Subscription.MessageRetentionDuration
		case "retainAckedMessages":
			existing.RetainAckedMessages = req.Subscription.RetainAckedMessages
		case "expirationPolicy":
			existing.ExpirationPolicy = req.Subscription.ExpirationPolicy
		case "enableMessageOrdering":
			existing.EnableMessageOrdering = req.Subscription.EnableMessageOrdering
		case "filter":
			existing.Filter = req.Subscription.Filter
		case "deadLetterPolicy":
			existing.DeadLetterPolicy = req.Subscription.DeadLetterPolicy
		case "retryPolicy":
			existing.RetryPolicy = req.Subscription.RetryPolicy
		default:
			gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"unknown updateMask path: "+path)
			return
		}
	}
	psSubscriptions.Put(name, existing)
	sim.WriteJSON(w, http.StatusOK, existing)
}

// handlePSPatchTopic is `projects.topics.patch`. Labels is the only
// mutable field today on real Pub/Sub; the sim accepts the same mask
// shape and applies labels accordingly.
func handlePSPatchTopic(w http.ResponseWriter, r *http.Request) {
	name := psTopicName(sim.PathParam(r, "project"), sim.PathParam(r, "topic"))
	existing, ok := psTopics.Get(name)
	if !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Topic not found: "+name)
		return
	}
	var req struct {
		Topic      PSTopic `json:"topic"`
		UpdateMask string  `json:"updateMask"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	for _, path := range splitMask(req.UpdateMask) {
		switch path {
		case "labels":
			existing.Labels = req.Topic.Labels
		case "kmsKeyName":
			existing.KmsKeyName = req.Topic.KmsKeyName
		case "messageRetentionDuration":
			existing.MessageRetentionDuration = req.Topic.MessageRetentionDuration
		case "messageStoragePolicy":
			existing.MessageStoragePolicy = req.Topic.MessageStoragePolicy
		default:
			gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"unknown updateMask path: "+path)
			return
		}
	}
	psTopics.Put(name, existing)
	sim.WriteJSON(w, http.StatusOK, existing)
}

// splitMask parses a comma-separated updateMask into trimmed paths.
func splitMask(mask string) []string {
	if mask == "" {
		return nil
	}
	parts := strings.Split(mask, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func handlePSListSubscriptions(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	prefix := fmt.Sprintf("projects/%s/subscriptions/", project)
	var all []PSSubscription
	for _, s := range psSubscriptions.List() {
		if strings.HasPrefix(s.Name, prefix) {
			all = append(all, s)
		}
	}
	if all == nil {
		all = []PSSubscription{}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	page, next, ok := paginateList(w, r, all)
	if !ok {
		return
	}
	resp := map[string]any{"subscriptions": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handlePSDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	name := psSubName(sim.PathParam(r, "project"), sim.PathParam(r, "sub"))
	if !psSubscriptions.Delete(name) {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+name)
		return
	}
	psQueues.Delete(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePSSubscriptionVerb(w http.ResponseWriter, r *http.Request) {
	sv := sim.PathParam(r, "subVerb")
	parts := strings.SplitN(sv, ":", 2)
	if len(parts) != 2 {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Expected <sub>:<verb>")
		return
	}
	sub, verb := parts[0], parts[1]
	name := psSubName(sim.PathParam(r, "project"), sub)
	switch verb {
	case "pull":
		handlePSPull(w, r, name)
	case "acknowledge":
		handlePSAck(w, r, name)
	case "modifyAckDeadline":
		handlePSModifyAck(w, r, name)
	case "modifyPushConfig":
		handlePSModifyPushConfig(w, r, name)
	case "detach":
		handlePSDetach(w, r, name)
	case "seek":
		handlePSSeek(w, r, name)
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, gcpResourceIAMStore(), name, verb)
	default:
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown verb: "+verb)
	}
}

// handlePSModifyPushConfig replaces the subscription's PushConfig.
// An empty PushConfig converts a push subscription to a pull one; a
// populated one sets/changes the push endpoint. Messages continue to
// accumulate regardless, so the delivery queue is untouched.
func handlePSModifyPushConfig(w http.ResponseWriter, r *http.Request, subName string) {
	if _, ok := psSubscriptions.Get(subName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+subName)
		return
	}
	var req struct {
		PushConfig *PSPushConfig `json:"pushConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	psSubscriptions.Update(subName, func(s *PSSubscription) {
		s.PushConfig = req.PushConfig
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handlePSDetach detaches a subscription from its topic. Real Pub/Sub
// drops the retained messages and makes subsequent Pull requests fail
// with FAILED_PRECONDITION; the sim marks the subscription detached by
// clearing its topic association and dropping the delivery queue. The
// DetachSubscription response is reserved-for-future-use (empty).
func handlePSDetach(w http.ResponseWriter, r *http.Request, subName string) {
	if _, ok := psSubscriptions.Get(subName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+subName)
		return
	}
	psSubscriptions.Update(subName, func(s *PSSubscription) {
		s.Detached = true
	})
	psQueues.Delete(subName)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handlePSSeek resets a subscription to a snapshot or timestamp. The sim does
// not model per-message ack cursors, so seek validates the subscription exists
// and returns the empty SeekResponse the API contract specifies.
func handlePSSeek(w http.ResponseWriter, r *http.Request, subName string) {
	if _, ok := psSubscriptions.Get(subName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+subName)
		return
	}
	var req struct {
		Snapshot string `json:"snapshot,omitempty"`
		Time     string `json:"time,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePSPull(w http.ResponseWriter, r *http.Request, subName string) {
	sub, ok := psSubscriptions.Get(subName)
	if !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+subName)
		return
	}
	var req struct {
		MaxMessages int `json:"maxMessages"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if req.MaxMessages <= 0 {
		req.MaxMessages = 1
	}
	q, _ := psQueues.Get(subName)
	if len(q.Messages) == 0 {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"receivedMessages": []any{}})
		return
	}
	n := req.MaxMessages
	if n > len(q.Messages) {
		n = len(q.Messages)
	}
	picked := q.Messages[:n]
	rest := q.Messages[n:]
	q.Messages = rest
	psQueues.Put(subName, q)

	now := time.Now()
	deadline := now.Add(time.Duration(sub.AckDeadlineSeconds) * time.Second)
	out := make([]map[string]any, 0, n)
	for _, m := range picked {
		ackID := generateUUIDLocal()
		psInFlight.Put(ackID, PSDeliveredMessage{
			AckId:        ackID,
			Subscription: subName,
			Message:      m,
			DeliveredAt:  now,
			AckDeadline:  deadline,
		})
		out = append(out, map[string]any{
			"ackId":   ackID,
			"message": m,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"receivedMessages": out})
}

func handlePSAck(w http.ResponseWriter, r *http.Request, subName string) {
	if _, ok := psSubscriptions.Get(subName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+subName)
		return
	}
	var req struct {
		AckIds []string `json:"ackIds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	for _, id := range req.AckIds {
		psInFlight.Delete(id)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePSModifyAck(w http.ResponseWriter, r *http.Request, subName string) {
	if _, ok := psSubscriptions.Get(subName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+subName)
		return
	}
	var req struct {
		AckIds             []string `json:"ackIds"`
		AckDeadlineSeconds int      `json:"ackDeadlineSeconds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	now := time.Now()
	for _, id := range req.AckIds {
		psInFlight.Update(id, func(m *PSDeliveredMessage) {
			m.AckDeadline = now.Add(time.Duration(req.AckDeadlineSeconds) * time.Second)
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// PSSchema is the wire shape of a Pub/Sub schema resource. Type is one
// of TYPE_UNSPECIFIED / PROTOCOL_BUFFER / AVRO; RevisionId + RevisionCreateTime
// are output-only fields the sim populates on every committed revision.
type PSSchema struct {
	Name               string `json:"name"` // projects/{p}/schemas/{s}
	Type               string `json:"type,omitempty"`
	Definition         string `json:"definition,omitempty"`
	RevisionId         string `json:"revisionId,omitempty"`
	RevisionCreateTime string `json:"revisionCreateTime,omitempty"`
}

// psSchemaRevisions stores every committed revision of every schema,
// keyed by "project/schema/revisionId". The latest revision of a schema
// is the one with the most recent RevisionCreateTime. This mirrors real
// Pub/Sub's revision model: Create yields revision 1, Commit appends a
// new revision, Rollback commits a copy of an older revision as the new
// head, and DeleteRevision removes one (never the last) revision.
var psSchemaRevisions sim.Store[PSSchema]

func psSchemaName(project, schema string) string {
	return "projects/" + project + "/schemas/" + schema
}

// psSchemaShortName extracts the bare schema id from a full resource name.
func psSchemaShortName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// psSchemaRevisionKey is the store key for one revision.
func psSchemaRevisionKey(name, revisionID string) string {
	return name + "/" + revisionID
}

// psSchemaRevisionsFor returns every revision of the named schema, newest
// first (by RevisionCreateTime, RevisionId as a tiebreaker).
func psSchemaRevisionsFor(name string) []PSSchema {
	var revs []PSSchema
	for _, s := range psSchemaRevisions.List() {
		if s.Name == name {
			revs = append(revs, s)
		}
	}
	sort.Slice(revs, func(i, j int) bool {
		if revs[i].RevisionCreateTime != revs[j].RevisionCreateTime {
			return revs[i].RevisionCreateTime > revs[j].RevisionCreateTime
		}
		return revs[i].RevisionId > revs[j].RevisionId
	})
	return revs
}

// psSchemaHead returns the latest revision of the named schema.
func psSchemaHead(name string) (PSSchema, bool) {
	revs := psSchemaRevisionsFor(name)
	if len(revs) == 0 {
		return PSSchema{}, false
	}
	return revs[0], true
}

// psNewRevisionID derives a short hex revision id (real Pub/Sub uses an
// 8-hex-char id, e.g. "c7cfa2a8").
func psNewRevisionID() string {
	return randHex(8)
}

func handlePSCreateSchema(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	schemaID := r.URL.Query().Get("schemaId")
	var req PSSchema
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	// The schema id can come from the schemaId query param (canonical) or,
	// for an ad-hoc create, from the trailing component of req.Name.
	if schemaID == "" {
		schemaID = psSchemaShortName(req.Name)
	}
	if schemaID == "" {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "schemaId is required")
		return
	}
	name := psSchemaName(project, schemaID)
	if _, ok := psSchemaHead(name); ok {
		gcpError(w, http.StatusConflict, "ALREADY_EXISTS", "Schema already exists: "+name)
		return
	}
	rev := PSSchema{
		Name:               name,
		Type:               req.Type,
		Definition:         req.Definition,
		RevisionId:         psNewRevisionID(),
		RevisionCreateTime: nowTimestamp(),
	}
	psSchemaRevisions.Put(psSchemaRevisionKey(name, rev.RevisionId), rev)
	sim.WriteJSON(w, http.StatusOK, rev)
}

func handlePSListSchemas(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	prefix := "projects/" + project + "/schemas/"
	// One entry per schema (its head revision).
	seen := map[string]PSSchema{}
	for _, s := range psSchemaRevisions.List() {
		if !strings.HasPrefix(s.Name, prefix) {
			continue
		}
		cur, ok := seen[s.Name]
		if !ok || s.RevisionCreateTime > cur.RevisionCreateTime {
			seen[s.Name] = s
		}
	}
	all := make([]PSSchema, 0, len(seen))
	for _, s := range seen {
		all = append(all, applySchemaView(s, r))
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	page, next, ok := paginateList(w, r, all)
	if !ok {
		return
	}
	resp := map[string]any{"schemas": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// applySchemaView honors the `view` query param: BASIC omits the
// definition; FULL (the default) returns it.
func applySchemaView(s PSSchema, r *http.Request) PSSchema {
	if r.URL.Query().Get("view") == "BASIC" {
		s.Definition = ""
	}
	return s
}

// handlePSGetSchemaOrVerb serves projects.schemas.get, .getIamPolicy,
// and .listRevisions — all GET methods fanning into the `{schemaVerb}`
// wildcard.
func handlePSGetSchemaOrVerb(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	sv := sim.PathParam(r, "schemaVerb")
	id, verb, hasVerb := strings.Cut(sv, ":")
	if !hasVerb {
		// schemas.get — may reference a specific revision via name@revisionId.
		name, revID := psSplitSchemaRef(psSchemaName(project, sv))
		var got PSSchema
		var ok bool
		if revID != "" {
			got, ok = psSchemaRevisions.Get(psSchemaRevisionKey(name, revID))
		} else {
			got, ok = psSchemaHead(name)
		}
		if !ok {
			gcpError(w, http.StatusNotFound, "NOT_FOUND", "Schema not found: "+name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, applySchemaView(got, r))
		return
	}
	name := psSchemaName(project, id)
	switch verb {
	case "getIamPolicy":
		handleResourceIAM(w, r, gcpResourceIAMStore(), name, verb)
	case "listRevisions":
		revs := psSchemaRevisionsFor(name)
		if len(revs) == 0 {
			gcpError(w, http.StatusNotFound, "NOT_FOUND", "Schema not found: "+name)
			return
		}
		out := make([]PSSchema, 0, len(revs))
		for _, s := range revs {
			out = append(out, applySchemaView(s, r))
		}
		page, next, ok := paginateList(w, r, out)
		if !ok {
			return
		}
		resp := map[string]any{"schemas": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	default:
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown verb: "+verb)
	}
}

// psSplitSchemaRef splits a schema name of the form
// "projects/p/schemas/s@revisionId" into the base name and revision id.
func psSplitSchemaRef(ref string) (name, revisionID string) {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// handlePSSchemaPostVerb serves projects.schemas commit / rollback /
// setIamPolicy / testIamPermissions (all POST colon-verbs on a named schema).
func handlePSSchemaPostVerb(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	sv := sim.PathParam(r, "schemaVerb")
	id, verb, ok := strings.Cut(sv, ":")
	if !ok {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Expected <schema>:<verb>")
		return
	}
	name := psSchemaName(project, id)
	switch verb {
	case "commit":
		handlePSCommitSchema(w, r, name)
	case "rollback":
		handlePSRollbackSchema(w, r, name)
	case "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, gcpResourceIAMStore(), name, verb)
	default:
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown verb: "+verb)
	}
}

// handlePSCommitSchema appends a new revision to an existing schema.
func handlePSCommitSchema(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := psSchemaHead(name); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Schema not found: "+name)
		return
	}
	var req struct {
		Schema PSSchema `json:"schema"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	rev := PSSchema{
		Name:               name,
		Type:               req.Schema.Type,
		Definition:         req.Schema.Definition,
		RevisionId:         psNewRevisionID(),
		RevisionCreateTime: nowTimestamp(),
	}
	psSchemaRevisions.Put(psSchemaRevisionKey(name, rev.RevisionId), rev)
	sim.WriteJSON(w, http.StatusOK, rev)
}

// handlePSRollbackSchema creates a new revision that is a copy of an
// existing revision identified by revisionId.
func handlePSRollbackSchema(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		RevisionId string `json:"revisionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	src, ok := psSchemaRevisions.Get(psSchemaRevisionKey(name, req.RevisionId))
	if !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND",
			"Revision not found: "+name+"@"+req.RevisionId)
		return
	}
	rev := PSSchema{
		Name:               name,
		Type:               src.Type,
		Definition:         src.Definition,
		RevisionId:         psNewRevisionID(),
		RevisionCreateTime: nowTimestamp(),
	}
	psSchemaRevisions.Put(psSchemaRevisionKey(name, rev.RevisionId), rev)
	sim.WriteJSON(w, http.StatusOK, rev)
}

// handlePSDeleteSchemaOrRevision serves projects.schemas.delete and
// .deleteRevision. Plain delete removes every revision of the schema;
// deleteRevision removes the one revision named by the revisionId query
// param and returns the new head revision.
func handlePSDeleteSchemaOrRevision(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	sv := sim.PathParam(r, "schemaVerb")
	id, verb, hasVerb := strings.Cut(sv, ":")
	if hasVerb && verb != "deleteRevision" {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown verb: "+verb)
		return
	}
	name := psSchemaName(project, id)
	if !hasVerb {
		revs := psSchemaRevisionsFor(name)
		if len(revs) == 0 {
			gcpError(w, http.StatusNotFound, "NOT_FOUND", "Schema not found: "+name)
			return
		}
		for _, s := range revs {
			psSchemaRevisions.Delete(psSchemaRevisionKey(name, s.RevisionId))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
		return
	}
	// deleteRevision — the revision id rides the `revisionId` query param.
	revID := r.URL.Query().Get("revisionId")
	revs := psSchemaRevisionsFor(name)
	if len(revs) == 0 {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Schema not found: "+name)
		return
	}
	if len(revs) == 1 {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"cannot delete the last revision of a schema")
		return
	}
	if _, ok := psSchemaRevisions.Get(psSchemaRevisionKey(name, revID)); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND",
			"Revision not found: "+name+"@"+revID)
		return
	}
	psSchemaRevisions.Delete(psSchemaRevisionKey(name, revID))
	head, _ := psSchemaHead(name)
	sim.WriteJSON(w, http.StatusOK, head)
}

// handlePSValidateSchema validates a candidate schema definition. The
// real API returns an empty ValidateSchemaResponse on success; the sim
// requires a non-empty definition + a recognized type, matching the
// real service's INVALID_ARGUMENT on a malformed schema.
func handlePSValidateSchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Schema PSSchema `json:"schema"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if req.Schema.Definition == "" {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "schema definition is required")
		return
	}
	switch req.Schema.Type {
	case "PROTOCOL_BUFFER", "AVRO":
	default:
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"unsupported schema type: "+req.Schema.Type)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handlePSValidateMessage validates a message against a named or ad-hoc
// schema. Returns an empty ValidateMessageResponse on success.
func handlePSValidateMessage(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var req struct {
		Name     string   `json:"name"`
		Schema   PSSchema `json:"schema"`
		Message  string   `json:"message"`
		Encoding string   `json:"encoding"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	// Resolve the schema: a named schema must exist; an ad-hoc schema must
	// carry a definition. One of the two must be supplied.
	switch {
	case req.Name != "":
		// req.Name is a full resource name; normalize a bare id too.
		name := req.Name
		if !strings.HasPrefix(name, "projects/") {
			name = psSchemaName(project, name)
		}
		if _, ok := psSchemaHead(name); !ok {
			gcpError(w, http.StatusNotFound, "NOT_FOUND", "Schema not found: "+name)
			return
		}
	case req.Schema.Definition != "":
		// ad-hoc schema, accepted as-is
	default:
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"either name or schema must be provided")
		return
	}
	if req.Message == "" {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "message is required")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// gcpError mirrors the canonical Google API error envelope.
func gcpError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"code":%d,"message":%q,"status":%q,"details":[]}}`, status, message, code)
}

// generateUUIDLocal is a Pub/Sub-scoped UUID helper that produces
// short opaque IDs for messageId / ackId. Independent of the GCS
// generateUUID helper to avoid a cross-file dependency tangle.
func generateUUIDLocal() string {
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), randHex(8))
}

func randHex(n int) string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, n)
	t := time.Now().UnixNano()
	for i := 0; i < n; i++ {
		out[i] = hexChars[(t>>uint(i*4))&0xf]
	}
	return string(out)
}

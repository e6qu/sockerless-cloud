package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Pub/Sub v1 over REST: topics, subscriptions, snapshots and schemas,
// with publish, pull, acknowledge and the other subscription verbs. Each
// `:verb` route registers one wildcard and dispatches on the suffix.

type PSTopic struct {
	Name                     string                  `json:"name"` // projects/{p}/topics/{t}
	Labels                   map[string]string       `json:"labels,omitempty"`
	KmsKeyName               string                  `json:"kmsKeyName,omitempty"`
	MessageRetentionDuration string                  `json:"messageRetentionDuration,omitempty"`
	MessageStoragePolicy     *PSMessageStoragePolicy `json:"messageStoragePolicy,omitempty"`
	// SchemaSettings is stored verbatim.
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
	PushEndpoint string            `json:"pushEndpoint,omitempty"`
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
	OrderingKey string            `json:"orderingKey,omitempty"`
}

var (
	psTopics        sim.Store[PSTopic]
	psSubscriptions sim.Store[PSSubscription]
	psQueues        sim.Store[psQueue]
)

func registerPubSub(srv *sim.Server) {
	psTopics = sim.MakeStore[PSTopic](srv.DB(), "pubsub_topics")
	psSubscriptions = sim.MakeStore[PSSubscription](srv.DB(), "pubsub_subscriptions")
	psQueues = sim.MakeStore[psQueue](srv.DB(), "pubsub_queues")
	if err := psMigrateQueues(srv.DB()); err != nil {
		log.Fatalf("pubsub: %v", err)
	}
	psSnapshots = sim.MakeStore[PSSnapshot](srv.DB(), "pubsub_snapshots")
	psSnapshotBacklogs = sim.MakeStore[[]PSMessage](srv.DB(), "pubsub_snapshot_backlogs")
	psSchemaRevisions = sim.MakeStore[PSSchema](srv.DB(), "pubsub_schema_revisions")

	srv.HandleFunc("PUT /v1/projects/{project}/topics/{topic}", handlePSCreateTopic)
	srv.HandleFunc("GET /v1/projects/{project}/topics/{topic}", handlePSGetTopic)
	srv.HandleFunc("GET /v1/projects/{project}/topics", handlePSListTopics)
	srv.HandleFunc("DELETE /v1/projects/{project}/topics/{topic}", handlePSDeleteTopic)
	srv.HandleFunc("POST /v1/projects/{project}/topics/{topicVerb}", handlePSTopicVerb)

	srv.HandleFunc("PUT /v1/projects/{project}/subscriptions/{sub}", handlePSCreateSubscription)
	srv.HandleFunc("PATCH /v1/projects/{project}/subscriptions/{sub}", handlePSPatchSubscription)
	srv.HandleFunc("GET /v1/projects/{project}/subscriptions/{sub}", handlePSGetSubscription)
	srv.HandleFunc("GET /v1/projects/{project}/subscriptions", handlePSListSubscriptions)
	srv.HandleFunc("DELETE /v1/projects/{project}/subscriptions/{sub}", handlePSDeleteSubscription)
	srv.HandleFunc("POST /v1/projects/{project}/subscriptions/{subVerb}", handlePSSubscriptionVerb)

	// projects.topics.patch changes labels; the SDK and the Terraform provider
	// both send it on update.
	srv.HandleFunc("PATCH /v1/projects/{project}/topics/{topic}", handlePSPatchTopic)

	// A topic's snapshots and subscriptions derive from those stores; register
	// them before the `{topic}` GET.
	srv.HandleFunc("GET /v1/projects/{project}/topics/{topic}/snapshots", handlePSListTopicSnapshots)
	srv.HandleFunc("GET /v1/projects/{project}/topics/{topic}/subscriptions", handlePSListTopicSubscriptions)

	// Snapshots capture a subscription's backlog for Seek to replay.
	srv.HandleFunc("PUT /v1/projects/{project}/snapshots/{snap}", handlePSCreateSnapshot)
	srv.HandleFunc("PATCH /v1/projects/{project}/snapshots/{snap}", handlePSPatchSnapshot)
	srv.HandleFunc("GET /v1/projects/{project}/snapshots/{snap}", handlePSGetSnapshot)
	srv.HandleFunc("GET /v1/projects/{project}/snapshots", handlePSListSnapshots)
	srv.HandleFunc("DELETE /v1/projects/{project}/snapshots/{snap}", handlePSDeleteSnapshot)
	srv.HandleFunc("POST /v1/projects/{project}/snapshots/{snapVerb}", handlePSSnapshotVerb)

	// Schemas carry their own revision history: CRUD, commit, rollback,
	// listRevisions, deleteRevision, validate, validateMessage and the AIP-141 IAM
	// verbs.
	srv.HandleFunc("POST /v1/projects/{project}/schemas", handlePSCreateSchema)
	srv.HandleFunc("POST /v1/projects/{project}/schemas:validate", handlePSValidateSchema)
	srv.HandleFunc("POST /v1/projects/{project}/schemas:validateMessage", handlePSValidateMessage)
	srv.HandleFunc("GET /v1/projects/{project}/schemas", handlePSListSchemas)
	srv.HandleFunc("GET /v1/projects/{project}/schemas/{schemaVerb}", handlePSGetSchemaOrVerb)
	srv.HandleFunc("POST /v1/projects/{project}/schemas/{schemaVerb}", handlePSSchemaPostVerb)
	srv.HandleFunc("DELETE /v1/projects/{project}/schemas/{schemaVerb}", handlePSDeleteSchemaOrRevision)
}

// PSSnapshot's ExpireTime is creation plus 7 days, the longest Pub/Sub keeps a
// snapshot.
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

// psProjectNotFound is what Pub/Sub answers for a resource in a project that
// does not exist or is not active.
const psProjectNotFound = "Requested project not found or user does not have access to it (project=%s). " +
	"Make sure to specify the unique project identifier and not the Google Cloud Console display name."

// psProjectActive reports whether Cloud Resource Manager holds project, by ID
// or number, as an active project.
func psProjectActive(project string) bool {
	p, ok := crmResolveProject(project)
	return ok && p.State == "ACTIVE"
}

// psResourceProject is the project a projects/{project}/... resource name
// names.
func psResourceProject(name string) string {
	rest := strings.TrimPrefix(name, "projects/")
	project, _, _ := strings.Cut(rest, "/")
	return project
}

func handlePSCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	snap := sim.PathParam(r, "snap")
	var req struct {
		Subscription string            `json:"subscription"`
		Labels       map[string]string `json:"labels,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if !psProjectActive(project) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", psProjectNotFound, project)
		return
	}
	if _, ok := psSubscriptions.Get(req.Subscription); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Resource not found (resource=%s)", req.Subscription)
		return
	}
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
	// Capture the subscription's backlog so a later Seek to this snapshot replays
	// exactly these messages, as the gRPC CreateSnapshot does.
	if backlog := psBacklog(req.Subscription); len(backlog) > 0 {
		psSnapshotBacklogs.Put(psSnapshotKey(project, snap), backlog)
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"snapshot %q not found", snap)
		return
	}
	psSnapshotBacklogs.Delete(psSnapshotKey(project, snap))
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handlePSListTopicSnapshots lists the names of the snapshots of this topic;
// the Discovery response carries names, not resources.
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
	psWriteNames(w, r, "snapshots", names)
}

// handlePSListTopicSubscriptions lists the names of this topic's
// subscriptions.
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
	psWriteNames(w, r, "subscriptions", names)
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
	if !psProjectActive(project) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", psProjectNotFound, project)
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
	// getIamPolicy is a GET colon-verb on the `{topic}` segment.
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
	prefix := fmt.Sprintf("projects/%s/topics/", sim.PathParam(r, "project"))
	psWriteList(w, r, psTopics, prefix, "topics", func(v PSTopic) string { return v.Name })
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
		// The policy is keyed by the full topic name, so :getIamPolicy reads what
		// :setIamPolicy wrote.
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
	ids, err := psPublishMessages(tName, req.Messages)
	if err != nil {
		psWriteRPCError(w, err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"messageIds": ids})
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
	if !psProjectActive(project) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", psProjectNotFound, project)
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
	// messageRetentionDuration defaults to Pub/Sub's 7 days.
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
	if err := psValidateSubscription(s); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	psSubscriptions.Put(s.Name, s)
	// Seed the delivery queue: Update, which publishes append through, is a no-op
	// for an absent key.
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

// handlePSPatchSubscription serves projects.subscriptions.patch, whose body is
// `{"subscription": {...}, "updateMask": "ackDeadlineSeconds,..."}`; fields
// outside the mask keep their values.
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
	// Pub/Sub answers an unknown mask path with 400 INVALID_ARGUMENT.
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
	if existing.AckDeadlineSeconds == 0 {
		existing.AckDeadlineSeconds = psMinAckDeadlineSeconds
	}
	if err := psValidateSubscription(existing); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	psSubscriptions.Put(name, existing)
	psSignalSubscription(name)
	sim.WriteJSON(w, http.StatusOK, existing)
}

// handlePSPatchTopic serves projects.topics.patch; labels is the mutable
// field.
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
	prefix := fmt.Sprintf("projects/%s/subscriptions/", sim.PathParam(r, "project"))
	psWriteList(w, r, psSubscriptions, prefix, "subscriptions", func(v PSSubscription) string { return v.Name })
}

func handlePSDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	name := psSubName(sim.PathParam(r, "project"), sim.PathParam(r, "sub"))
	if !psSubscriptions.Delete(name) {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+name)
		return
	}
	psQueues.Delete(name)
	psSignalSubscription(name)
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

// handlePSModifyPushConfig replaces the PushConfig: an empty one makes the
// subscription pull, a populated one sets the push endpoint. The queue is
// untouched.
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
	psSignalSubscription(subName)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handlePSDetach detaches a subscription: Pub/Sub drops its retained messages
// and fails later Pulls with FAILED_PRECONDITION, so clear the topic and drop
// the queue. DetachSubscriptionResponse is empty.
func handlePSDetach(w http.ResponseWriter, r *http.Request, subName string) {
	if _, ok := psSubscriptions.Get(subName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+subName)
		return
	}
	psSubscriptions.Update(subName, func(s *PSSubscription) {
		s.Detached = true
	})
	psQueues.Delete(subName)
	psSignalSubscription(subName)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handlePSSeek resets a subscription's acknowledgement state to a snapshot or
// a time and answers the empty SeekResponse.
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
	switch {
	case req.Snapshot != "":
		key := psSnapshotKeyFromName(req.Snapshot)
		if _, ok := psSnapshots.Get(key); !ok {
			gcpError(w, http.StatusNotFound, "NOT_FOUND", "Snapshot not found: "+req.Snapshot)
			return
		}
		backlog, _ := psSnapshotBacklogs.Get(key)
		psSeekSnapshot(subName, backlog)
	case req.Time != "":
		t, err := time.Parse(time.RFC3339Nano, req.Time)
		if err != nil {
			gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid time: "+req.Time)
			return
		}
		psSeekTime(subName, t)
	default:
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "seek target (snapshot or time) is required")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePSPull(w http.ResponseWriter, r *http.Request, subName string) {
	var req struct {
		MaxMessages       int  `json:"maxMessages"`
		ReturnImmediately bool `json:"returnImmediately"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	delivered, err := psPull(r.Context(), subName, req.MaxMessages, req.ReturnImmediately)
	if err != nil {
		psWriteRPCError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(delivered))
	for _, d := range delivered {
		rm := map[string]any{"ackId": d.AckID, "message": d.Message}
		if d.DeliveryAttempt > 0 {
			rm["deliveryAttempt"] = d.DeliveryAttempt
		}
		out = append(out, rm)
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
	psAcknowledge(subName, req.AckIds)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePSModifyAck(w http.ResponseWriter, r *http.Request, subName string) {
	if _, ok := psSubscriptions.Get(subName); !ok {
		gcpError(w, http.StatusNotFound, "NOT_FOUND", "Subscription not found: "+subName)
		return
	}
	var req struct {
		AckIds             []string `json:"ackIds"`
		AckDeadlineSeconds int32    `json:"ackDeadlineSeconds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if !psValidAckDeadline(req.AckDeadlineSeconds) {
		gcpError(w, http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf("Invalid ack deadline given: %d", req.AckDeadlineSeconds))
		return
	}
	psModifyAckDeadline(subName, req.AckIds, req.AckDeadlineSeconds)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// PSSchema is a Pub/Sub schema; Type is TYPE_UNSPECIFIED, PROTOCOL_BUFFER or
// AVRO, and RevisionId and RevisionCreateTime are output-only.
type PSSchema struct {
	Name               string `json:"name"` // projects/{p}/schemas/{s}
	Type               string `json:"type,omitempty"`
	Definition         string `json:"definition,omitempty"`
	RevisionId         string `json:"revisionId,omitempty"`
	RevisionCreateTime string `json:"revisionCreateTime,omitempty"`
}

// psSchemaRevisions stores every revision of every schema, keyed by
// "project/schema/revisionId"; the head is the newest RevisionCreateTime. As
// in Pub/Sub, create yields the first revision, commit appends one, rollback
// commits a copy of an older one, and deleteRevision removes any but the last.
var psSchemaRevisions sim.Store[PSSchema]

func psSchemaName(project, schema string) string {
	return "projects/" + project + "/schemas/" + schema
}

func psSchemaShortName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

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

func psSchemaHead(name string) (PSSchema, bool) {
	revs := psSchemaRevisionsFor(name)
	if len(revs) == 0 {
		return PSSchema{}, false
	}
	return revs[0], true
}

// psNewRevisionID mints an 8-hex-character revision ID, as Pub/Sub's are
// ("c7cfa2a8").
func psNewRevisionID() string {
	return sim.RandomHex(8)
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
	page, next, ok := paginateList(w, r, all, gcpAsc(func(v PSSchema) string { return v.Name }))
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
		page, next, ok := paginateList(w, r, out, gcpDesc(func(s PSSchema) string { return s.RevisionCreateTime }), gcpDesc(func(s PSSchema) string { return s.RevisionId }))
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

// handlePSValidateSchema answers an empty ValidateSchemaResponse for a
// non-empty definition of a recognized type, INVALID_ARGUMENT otherwise.
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

// gcpError writes Google's error envelope.
func gcpError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"code":%d,"message":%q,"status":%q,"details":[]}}`, status, message, code)
}

// generateUUIDLocal returns an opaque, unique messageId or ackId.
func generateUUIDLocal() string {
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), sim.RandomHex(8))
}

// psWriteNames answers a list of resource names, paged in name order, under
// member.
func psWriteNames(w http.ResponseWriter, r *http.Request, member string, names []string) {
	sort.Strings(names)
	page, next, ok := paginateList(w, r, names, gcpAsc(func(v string) string { return v }))
	if !ok {
		return
	}
	if page == nil {
		page = []string{}
	}
	resp := map[string]any{member: page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// psWriteList answers a list of the resources of store named under prefix,
// paged in name order, under member.
func psWriteList[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], prefix, member string, name func(T) string) {
	all := []T{}
	for _, v := range store.List() {
		if strings.HasPrefix(name(v), prefix) {
			all = append(all, v)
		}
	}
	sort.Slice(all, func(i, j int) bool { return name(all[i]) < name(all[j]) })
	page, next, ok := paginateList(w, r, all, gcpAsc(name))
	if !ok {
		return
	}
	resp := map[string]any{member: page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

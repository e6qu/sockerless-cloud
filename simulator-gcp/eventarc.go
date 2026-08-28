package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Eventarc v1 REST surface. Triggers are regional resources and
// mutating operations return AIP-151 long-running operations.

// writeEventarcList sorts items by name, paginates by pageSize/pageToken, and
// writes the AIP-132 list response under itemsKey with a nextPageToken when more
// items remain.
func writeEventarcList[T any](w http.ResponseWriter, r *http.Request, items []T, name func(T) string, itemsKey string) {
	sort.Slice(items, func(i, j int) bool { return name(items[i]) < name(items[j]) })
	page, next, ok := paginateList(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{itemsKey: page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

type EventarcTrigger struct {
	Name                 string                `json:"name"`
	Uid                  string                `json:"uid,omitempty"`
	CreateTime           string                `json:"createTime,omitempty"`
	UpdateTime           string                `json:"updateTime,omitempty"`
	Labels               map[string]string     `json:"labels,omitempty"`
	EventFilters         []EventarcEventFilter `json:"eventFilters,omitempty"`
	Destination          map[string]any        `json:"destination,omitempty"`
	Transport            map[string]any        `json:"transport,omitempty"`
	ServiceAccount       string                `json:"serviceAccount,omitempty"`
	EventDataContentType string                `json:"eventDataContentType,omitempty"`
	Channel              string                `json:"channel,omitempty"`
	Conditions           map[string]any        `json:"conditions,omitempty"`
}

type EventarcEventFilter struct {
	Attribute string `json:"attribute"`
	Value     string `json:"value"`
	Operator  string `json:"operator,omitempty"`
}

type EventarcChannel struct {
	Name            string            `json:"name"`
	Uid             string            `json:"uid,omitempty"`
	CreateTime      string            `json:"createTime,omitempty"`
	UpdateTime      string            `json:"updateTime,omitempty"`
	Provider        string            `json:"provider,omitempty"`
	PubsubTopic     string            `json:"pubsubTopic,omitempty"`
	State           string            `json:"state,omitempty"`
	ActivationToken string            `json:"activationToken,omitempty"`
	CryptoKeyName   string            `json:"cryptoKeyName,omitempty"`
	SatisfiesPzs    bool              `json:"satisfiesPzs,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

type EventarcChannelConnection struct {
	Name            string            `json:"name"`
	Uid             string            `json:"uid,omitempty"`
	Channel         string            `json:"channel,omitempty"`
	CreateTime      string            `json:"createTime,omitempty"`
	UpdateTime      string            `json:"updateTime,omitempty"`
	ActivationToken string            `json:"activationToken,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

type EventarcProvider struct {
	Name        string                  `json:"name"`
	DisplayName string                  `json:"displayName,omitempty"`
	EventTypes  []EventarcProviderEvent `json:"eventTypes,omitempty"`
}

type EventarcProviderEvent struct {
	Type                string                       `json:"type"`
	Description         string                       `json:"description,omitempty"`
	FilteringAttributes []EventarcFilteringAttribute `json:"filteringAttributes,omitempty"`
	EventSchemaURI      string                       `json:"eventSchemaUri,omitempty"`
}

type EventarcFilteringAttribute struct {
	Attribute string `json:"attribute"`
	Required  bool   `json:"required,omitempty"`
}

var (
	eventarcTriggers           sim.Store[EventarcTrigger]
	eventarcChannels           sim.Store[EventarcChannel]
	eventarcChannelConnections sim.Store[EventarcChannelConnection]
	eventarcEnrollments        sim.Store[EventarcEnrollment]
	eventarcMessageBuses       sim.Store[EventarcMessageBus]
	eventarcPipelines          sim.Store[EventarcPipeline]
	eventarcGoogleAPISources   sim.Store[EventarcGoogleAPISource]
	eventarcChannelConfigs     sim.Store[EventarcGoogleChannelConfig]
)

func registerEventarc(srv *sim.Server) {
	eventarcTriggers = sim.MakeStore[EventarcTrigger](srv.DB(), "eventarc_triggers")
	eventarcChannels = sim.MakeStore[EventarcChannel](srv.DB(), "eventarc_channels")
	eventarcChannelConnections = sim.MakeStore[EventarcChannelConnection](srv.DB(), "eventarc_channel_connections")
	eventarcEnrollments = sim.MakeStore[EventarcEnrollment](srv.DB(), "eventarc_enrollments")
	eventarcMessageBuses = sim.MakeStore[EventarcMessageBus](srv.DB(), "eventarc_message_buses")
	eventarcPipelines = sim.MakeStore[EventarcPipeline](srv.DB(), "eventarc_pipelines")
	eventarcGoogleAPISources = sim.MakeStore[EventarcGoogleAPISource](srv.DB(), "eventarc_google_api_sources")
	eventarcChannelConfigs = sim.MakeStore[EventarcGoogleChannelConfig](srv.DB(), "eventarc_channel_configs")

	// Triggers. The GET/POST {trigger}/{triggerAction} handlers also fan in
	// the AIP-141 IAM colon-verbs (getIamPolicy on GET, setIamPolicy /
	// testIamPermissions on POST) since Go's mux can't spell `{id}:verb`.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/triggers", handleGCPRegionalTriggerCreate)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/triggers", handleGCPRegionalTriggerList)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/triggers/{trigger}", handleGCPRegionalTriggerGet)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/triggers/{trigger}", handleGCPRegionalTriggerPatch)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/triggers/{trigger}", handleGCPRegionalTriggerDelete)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/triggers/{triggerAction}", handleEventarcTriggerIAMAction)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/channels", handleEventarcCreateChannel)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/channels", handleEventarcListChannels)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/channels/{channel}", handleEventarcGetChannel)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/channels/{channel}", handleEventarcPatchChannel)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/channels/{channel}", handleEventarcDeleteChannel)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/channels/{channelAction}", handleEventarcChannelIAMAction)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/providers", handleEventarcListProviders)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/providers/{provider}", handleEventarcGetProvider)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/channelConnections", handleEventarcCreateChannelConnection)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/channelConnections", handleEventarcListChannelConnections)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/channelConnections/{connection}", handleEventarcGetChannelConnection)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/channelConnections/{connection}", handleEventarcDeleteChannelConnection)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/channelConnections/{connectionAction}", handleEventarcChannelConnectionIAMAction)

	// Enrollments.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/enrollments", handleEventarcCreateEnrollment)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/enrollments", handleEventarcListEnrollments)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/enrollments/{enrollment}", handleEventarcGetEnrollment)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/enrollments/{enrollment}", handleEventarcPatchEnrollment)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/enrollments/{enrollment}", handleEventarcDeleteEnrollment)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/enrollments/{enrollmentAction}", handleEventarcEnrollmentIAMAction)

	// Message buses.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/messageBuses", handleEventarcCreateMessageBus)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/messageBuses", handleEventarcListMessageBuses)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/messageBuses/{bus}", handleEventarcMessageBusGet)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/messageBuses/{bus}", handleEventarcPatchMessageBus)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/messageBuses/{bus}", handleEventarcDeleteMessageBus)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/messageBuses/{busAction}", handleEventarcMessageBusIAMAction)

	// Pipelines.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/pipelines", handleEventarcCreatePipeline)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/pipelines", handleEventarcListPipelines)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/pipelines/{pipeline}", handleEventarcGetPipeline)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/pipelines/{pipeline}", handleEventarcPatchPipeline)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/pipelines/{pipeline}", handleEventarcDeletePipeline)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/pipelines/{pipelineAction}", handleEventarcPipelineIAMAction)

	// Google API sources.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/googleApiSources", handleEventarcCreateGoogleAPISource)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/googleApiSources", handleEventarcListGoogleAPISources)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/googleApiSources/{source}", handleEventarcGetGoogleAPISource)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/googleApiSources/{source}", handleEventarcPatchGoogleAPISource)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/googleApiSources/{source}", handleEventarcDeleteGoogleAPISource)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/googleApiSources/{sourceAction}", handleEventarcGoogleAPISourceIAMAction)

	// googleChannelConfig singleton.
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/googleChannelConfig", handleEventarcGetGoogleChannelConfig)
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/googleChannelConfig", handleEventarcUpdateGoogleChannelConfig)

	// Locations meta-API + operations list/delete.
	srv.HandleFunc("GET /v1/projects/{project}/locations", handleEventarcListLocations)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}", handleEventarcGetLocation)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/operations", handleEventarcListOperations)
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/operations/{operation}", handleEventarcDeleteOperation)
}

func isCloudBuildRequest(r *http.Request) bool {
	if strings.Contains(strings.ToLower(r.Host), "cloudbuild") {
		return true
	}
	if r.Method == http.MethodPost && r.URL.Query().Get("triggerId") == "" {
		return true
	}
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	if location == "global" {
		return true
	}
	trigger := sim.PathParam(r, "trigger")
	if project == "" || location == "" || trigger == "" {
		return false
	}
	_, ok := cbTriggers.Get(buildTriggerKey(project, location, trigger))
	return ok
}

func handleGCPRegionalTriggerCreate(w http.ResponseWriter, r *http.Request) {
	if isCloudBuildRequest(r) {
		handleCreateBuildTrigger(w, r)
		return
	}
	handleEventarcCreateTrigger(w, r)
}

func handleGCPRegionalTriggerList(w http.ResponseWriter, r *http.Request) {
	if isCloudBuildRequest(r) {
		handleListBuildTriggers(w, r)
		return
	}
	handleEventarcListTriggers(w, r)
}

func handleGCPRegionalTriggerGet(w http.ResponseWriter, r *http.Request) {
	if isCloudBuildRequest(r) {
		handleGetBuildTrigger(w, r)
		return
	}
	handleEventarcGetTrigger(w, r)
}

func handleGCPRegionalTriggerPatch(w http.ResponseWriter, r *http.Request) {
	if isCloudBuildRequest(r) {
		handleUpdateBuildTrigger(w, r)
		return
	}
	handleEventarcPatchTrigger(w, r)
}

func handleGCPRegionalTriggerDelete(w http.ResponseWriter, r *http.Request) {
	if isCloudBuildRequest(r) {
		handleDeleteBuildTrigger(w, r)
		return
	}
	handleEventarcDeleteTrigger(w, r)
}

func eventarcTriggerName(project, location, trigger string) string {
	return fmt.Sprintf("projects/%s/locations/%s/triggers/%s", project, location, trigger)
}

func eventarcTriggerKey(project, location, trigger string) string {
	return project + "/" + location + "/" + trigger
}

func eventarcChannelName(project, location, channel string) string {
	return fmt.Sprintf("projects/%s/locations/%s/channels/%s", project, location, channel)
}

func eventarcChannelKey(project, location, channel string) string {
	return project + "/" + location + "/" + channel
}

func eventarcChannelConnectionName(project, location, connection string) string {
	return fmt.Sprintf("projects/%s/locations/%s/channelConnections/%s", project, location, connection)
}

func eventarcChannelConnectionKey(project, location, connection string) string {
	return project + "/" + location + "/" + connection
}

func handleEventarcCreateTrigger(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	triggerID := r.URL.Query().Get("triggerId")
	if triggerID == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "triggerId is required")
		return
	}
	var req EventarcTrigger
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if _, exists := eventarcTriggers.Get(eventarcTriggerKey(project, location, triggerID)); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "trigger %q already exists", eventarcTriggerName(project, location, triggerID))
		return
	}
	now := nowTimestamp()
	req.Name = eventarcTriggerName(project, location, triggerID)
	req.Uid = generateUUID()
	req.CreateTime = now
	req.UpdateTime = now
	eventarcTriggers.Put(eventarcTriggerKey(project, location, triggerID), req)
	op := newLRO(project, location, req, "type.googleapis.com/google.cloud.eventarc.v1.Trigger")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcGetTrigger(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	trigger := sim.PathParam(r, "trigger")
	if id, action, found := strings.Cut(trigger, ":"); found {
		if action == "getIamPolicy" {
			handleResourceIAM(w, r, gcpResourceIAMStore(), eventarcTriggerName(project, location, id), action)
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on trigger %q", action, id)
		return
	}
	t, ok := eventarcTriggers.Get(eventarcTriggerKey(project, location, trigger))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "trigger %q not found", trigger)
		return
	}
	sim.WriteJSON(w, http.StatusOK, t)
}

func handleEventarcListTriggers(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/triggers/", project, location)
	out := make([]EventarcTrigger, 0)
	for _, t := range eventarcTriggers.List() {
		if strings.HasPrefix(t.Name, prefix) {
			out = append(out, t)
		}
	}
	writeEventarcList(w, r, out, func(t EventarcTrigger) string { return t.Name }, "triggers")
}

func handleEventarcPatchTrigger(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	trigger := sim.PathParam(r, "trigger")
	key := eventarcTriggerKey(project, location, trigger)
	existing, ok := eventarcTriggers.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "trigger %q not found", trigger)
		return
	}
	var req EventarcTrigger
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if req.Labels != nil {
		existing.Labels = req.Labels
	}
	if req.EventFilters != nil {
		existing.EventFilters = req.EventFilters
	}
	if req.Destination != nil {
		existing.Destination = req.Destination
	}
	if req.Transport != nil {
		existing.Transport = req.Transport
	}
	if req.ServiceAccount != "" {
		existing.ServiceAccount = req.ServiceAccount
	}
	if req.EventDataContentType != "" {
		existing.EventDataContentType = req.EventDataContentType
	}
	existing.UpdateTime = nowTimestamp()
	eventarcTriggers.Put(key, existing)
	op := newLRO(project, location, existing, "type.googleapis.com/google.cloud.eventarc.v1.Trigger")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcDeleteTrigger(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	trigger := sim.PathParam(r, "trigger")
	key := eventarcTriggerKey(project, location, trigger)
	t, ok := eventarcTriggers.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "trigger %q not found", trigger)
		return
	}
	eventarcTriggers.Delete(key)
	op := newLRO(project, location, t, "type.googleapis.com/google.cloud.eventarc.v1.Trigger")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcCreateChannel(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	channelID := r.URL.Query().Get("channelId")
	if channelID == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "channelId is required")
		return
	}
	var req EventarcChannel
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if _, exists := eventarcChannels.Get(eventarcChannelKey(project, location, channelID)); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "channel %q already exists", eventarcChannelName(project, location, channelID))
		return
	}
	now := nowTimestamp()
	req.Name = eventarcChannelName(project, location, channelID)
	req.Uid = generateUUID()
	req.CreateTime = now
	req.UpdateTime = now
	req.State = "ACTIVE"
	req.ActivationToken = generateUUID()
	eventarcChannels.Put(eventarcChannelKey(project, location, channelID), req)
	op := newLRO(project, location, req, "type.googleapis.com/google.cloud.eventarc.v1.Channel")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcGetChannel(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	channel := sim.PathParam(r, "channel")
	if id, action, found := strings.Cut(channel, ":"); found {
		if action == "getIamPolicy" {
			handleResourceIAM(w, r, gcpResourceIAMStore(), eventarcChannelName(project, location, id), action)
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on channel %q", action, id)
		return
	}
	c, ok := eventarcChannels.Get(eventarcChannelKey(project, location, channel))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "channel %q not found", channel)
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleEventarcListChannels(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/channels/", project, location)
	out := make([]EventarcChannel, 0)
	for _, c := range eventarcChannels.List() {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	writeEventarcList(w, r, out, func(c EventarcChannel) string { return c.Name }, "channels")
}

func handleEventarcPatchChannel(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	channel := sim.PathParam(r, "channel")
	key := eventarcChannelKey(project, location, channel)
	existing, ok := eventarcChannels.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "channel %q not found", channel)
		return
	}
	var req EventarcChannel
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if req.Provider != "" {
		existing.Provider = req.Provider
	}
	if req.PubsubTopic != "" {
		existing.PubsubTopic = req.PubsubTopic
	}
	if req.CryptoKeyName != "" {
		existing.CryptoKeyName = req.CryptoKeyName
	}
	if req.Labels != nil {
		existing.Labels = req.Labels
	}
	existing.UpdateTime = nowTimestamp()
	eventarcChannels.Put(key, existing)
	op := newLRO(project, location, existing, "type.googleapis.com/google.cloud.eventarc.v1.Channel")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcDeleteChannel(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	channel := sim.PathParam(r, "channel")
	key := eventarcChannelKey(project, location, channel)
	c, ok := eventarcChannels.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "channel %q not found", channel)
		return
	}
	eventarcChannels.Delete(key)
	op := newLRO(project, location, c, "type.googleapis.com/google.cloud.eventarc.v1.Channel")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcListProviders(w http.ResponseWriter, r *http.Request) {
	parent := fmt.Sprintf("projects/%s/locations/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	sim.WriteJSON(w, http.StatusOK, map[string]any{"providers": eventarcProviders(parent)})
}

func handleEventarcGetProvider(w http.ResponseWriter, r *http.Request) {
	parent := fmt.Sprintf("projects/%s/locations/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	name := parent + "/providers/" + sim.PathParam(r, "provider")
	for _, provider := range eventarcProviders(parent) {
		if provider.Name == name {
			sim.WriteJSON(w, http.StatusOK, provider)
			return
		}
	}
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "provider %q not found", name)
}

func eventarcProviders(parent string) []EventarcProvider {
	return []EventarcProvider{
		{
			Name:        parent + "/providers/cloud.pubsub",
			DisplayName: "Cloud Pub/Sub",
			EventTypes: []EventarcProviderEvent{{
				Type:        "google.cloud.pubsub.topic.v1.messagePublished",
				Description: "A Pub/Sub message was published.",
				FilteringAttributes: []EventarcFilteringAttribute{{
					Attribute: "type",
					Required:  true,
				}},
			}},
		},
		{
			Name:        parent + "/providers/cloud.storage",
			DisplayName: "Cloud Storage",
			EventTypes: []EventarcProviderEvent{{
				Type:        "google.cloud.storage.object.v1.finalized",
				Description: "A Cloud Storage object was finalized.",
				FilteringAttributes: []EventarcFilteringAttribute{{
					Attribute: "type",
					Required:  true,
				}},
			}},
		},
	}
}

func handleEventarcCreateChannelConnection(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	connectionID := r.URL.Query().Get("channelConnectionId")
	if connectionID == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "channelConnectionId is required")
		return
	}
	var req EventarcChannelConnection
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if _, exists := eventarcChannelConnections.Get(eventarcChannelConnectionKey(project, location, connectionID)); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "channel connection %q already exists", eventarcChannelConnectionName(project, location, connectionID))
		return
	}
	now := nowTimestamp()
	req.Name = eventarcChannelConnectionName(project, location, connectionID)
	req.Uid = generateUUID()
	req.CreateTime = now
	req.UpdateTime = now
	eventarcChannelConnections.Put(eventarcChannelConnectionKey(project, location, connectionID), req)
	op := newLRO(project, location, req, "type.googleapis.com/google.cloud.eventarc.v1.ChannelConnection")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcGetChannelConnection(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	connection := sim.PathParam(r, "connection")
	if id, action, found := strings.Cut(connection, ":"); found {
		if action == "getIamPolicy" {
			handleResourceIAM(w, r, gcpResourceIAMStore(), eventarcChannelConnectionName(project, location, id), action)
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on channel connection %q", action, id)
		return
	}
	cc, ok := eventarcChannelConnections.Get(eventarcChannelConnectionKey(project, location, connection))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "channel connection %q not found", connection)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cc)
}

func handleEventarcListChannelConnections(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/channelConnections/", project, location)
	out := make([]EventarcChannelConnection, 0)
	for _, cc := range eventarcChannelConnections.List() {
		if strings.HasPrefix(cc.Name, prefix) {
			out = append(out, cc)
		}
	}
	writeEventarcList(w, r, out, func(cc EventarcChannelConnection) string { return cc.Name }, "channelConnections")
}

func handleEventarcDeleteChannelConnection(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	connection := sim.PathParam(r, "connection")
	key := eventarcChannelConnectionKey(project, location, connection)
	cc, ok := eventarcChannelConnections.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "channel connection %q not found", connection)
		return
	}
	eventarcChannelConnections.Delete(key)
	op := newLRO(project, location, cc, "type.googleapis.com/google.cloud.eventarc.v1.ChannelConnection")
	sim.WriteJSON(w, http.StatusOK, op)
}

// eventarcIAMVerb dispatches the AIP-141 POST IAM verbs (setIamPolicy /
// testIamPermissions) captured by a "{id}:verb" action wildcard against the
// shared GCP resource-IAM store, keyed on the resource's full name.
func eventarcIAMVerb(w http.ResponseWriter, r *http.Request, actionParam string, fullName func(id string) string, kind string) {
	id, action, found := strings.Cut(actionParam, ":")
	if !found {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on %s %q", kind, actionParam)
		return
	}
	switch action {
	case "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, gcpResourceIAMStore(), fullName(id), action)
	default:
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on %s %q", action, kind, id)
	}
}

func handleEventarcTriggerIAMAction(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	eventarcIAMVerb(w, r, sim.PathParam(r, "triggerAction"),
		func(id string) string { return eventarcTriggerName(project, location, id) }, "trigger")
}

func handleEventarcChannelIAMAction(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	eventarcIAMVerb(w, r, sim.PathParam(r, "channelAction"),
		func(id string) string { return eventarcChannelName(project, location, id) }, "channel")
}

func handleEventarcChannelConnectionIAMAction(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	eventarcIAMVerb(w, r, sim.PathParam(r, "connectionAction"),
		func(id string) string { return eventarcChannelConnectionName(project, location, id) }, "channel connection")
}

// LoggingConfig mirrors google.cloud.eventarc.v1.LoggingConfig.
type EventarcLoggingConfig struct {
	LogSeverity string `json:"logSeverity,omitempty"`
}

type EventarcEnrollment struct {
	Name        string            `json:"name"`
	Uid         string            `json:"uid,omitempty"`
	Etag        string            `json:"etag,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	UpdateTime  string            `json:"updateTime,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	DisplayName string            `json:"displayName,omitempty"`
	CelMatch    string            `json:"celMatch,omitempty"`
	MessageBus  string            `json:"messageBus,omitempty"`
	Destination string            `json:"destination,omitempty"`
}

type EventarcMessageBus struct {
	Name          string                 `json:"name"`
	Uid           string                 `json:"uid,omitempty"`
	Etag          string                 `json:"etag,omitempty"`
	CreateTime    string                 `json:"createTime,omitempty"`
	UpdateTime    string                 `json:"updateTime,omitempty"`
	Labels        map[string]string      `json:"labels,omitempty"`
	Annotations   map[string]string      `json:"annotations,omitempty"`
	DisplayName   string                 `json:"displayName,omitempty"`
	CryptoKeyName string                 `json:"cryptoKeyName,omitempty"`
	LoggingConfig *EventarcLoggingConfig `json:"loggingConfig,omitempty"`
}

type EventarcPipeline struct {
	Name               string                 `json:"name"`
	Uid                string                 `json:"uid,omitempty"`
	Etag               string                 `json:"etag,omitempty"`
	CreateTime         string                 `json:"createTime,omitempty"`
	UpdateTime         string                 `json:"updateTime,omitempty"`
	Labels             map[string]string      `json:"labels,omitempty"`
	Annotations        map[string]string      `json:"annotations,omitempty"`
	DisplayName        string                 `json:"displayName,omitempty"`
	Destinations       []map[string]any       `json:"destinations,omitempty"`
	Mediations         []map[string]any       `json:"mediations,omitempty"`
	CryptoKeyName      string                 `json:"cryptoKeyName,omitempty"`
	InputPayloadFormat map[string]any         `json:"inputPayloadFormat,omitempty"`
	LoggingConfig      *EventarcLoggingConfig `json:"loggingConfig,omitempty"`
	RetryPolicy        map[string]any         `json:"retryPolicy,omitempty"`
	SatisfiesPzs       bool                   `json:"satisfiesPzs,omitempty"`
}

type EventarcGoogleAPISource struct {
	Name                     string                 `json:"name"`
	Uid                      string                 `json:"uid,omitempty"`
	Etag                     string                 `json:"etag,omitempty"`
	CreateTime               string                 `json:"createTime,omitempty"`
	UpdateTime               string                 `json:"updateTime,omitempty"`
	Labels                   map[string]string      `json:"labels,omitempty"`
	Annotations              map[string]string      `json:"annotations,omitempty"`
	DisplayName              string                 `json:"displayName,omitempty"`
	Destination              string                 `json:"destination,omitempty"`
	CryptoKeyName            string                 `json:"cryptoKeyName,omitempty"`
	LoggingConfig            *EventarcLoggingConfig `json:"loggingConfig,omitempty"`
	OrganizationSubscription map[string]any         `json:"organizationSubscription,omitempty"`
	ProjectSubscriptions     map[string]any         `json:"projectSubscriptions,omitempty"`
}

type EventarcGoogleChannelConfig struct {
	Name          string            `json:"name"`
	UpdateTime    string            `json:"updateTime,omitempty"`
	CryptoKeyName string            `json:"cryptoKeyName,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

func eventarcEnrollmentName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/enrollments/%s", project, location, id)
}

func eventarcMessageBusName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/messageBuses/%s", project, location, id)
}

func eventarcPipelineName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/pipelines/%s", project, location, id)
}

func eventarcGoogleAPISourceName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/googleApiSources/%s", project, location, id)
}

func eventarcResKey(project, location, id string) string {
	return project + "/" + location + "/" + id
}

func handleEventarcCreateEnrollment(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("enrollmentId")
	if id == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "enrollmentId is required")
		return
	}
	var req EventarcEnrollment
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	key := eventarcResKey(project, location, id)
	if _, exists := eventarcEnrollments.Get(key); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "enrollment %q already exists", eventarcEnrollmentName(project, location, id))
		return
	}
	now := nowTimestamp()
	req.Name = eventarcEnrollmentName(project, location, id)
	req.Uid = generateUUID()
	req.Etag = generateUUID()
	req.CreateTime = now
	req.UpdateTime = now
	eventarcEnrollments.Put(key, req)
	op := newLRO(project, location, req, "type.googleapis.com/google.cloud.eventarc.v1.Enrollment")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcGetEnrollment(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "enrollment")
	if rid, action, found := strings.Cut(id, ":"); found {
		if action == "getIamPolicy" {
			handleResourceIAM(w, r, gcpResourceIAMStore(), eventarcEnrollmentName(project, location, rid), action)
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on enrollment %q", action, rid)
		return
	}
	e, ok := eventarcEnrollments.Get(eventarcResKey(project, location, id))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "enrollment %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, e)
}

func handleEventarcListEnrollments(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/enrollments/", project, location)
	out := make([]EventarcEnrollment, 0)
	for _, e := range eventarcEnrollments.List() {
		if strings.HasPrefix(e.Name, prefix) {
			out = append(out, e)
		}
	}
	writeEventarcList(w, r, out, func(e EventarcEnrollment) string { return e.Name }, "enrollments")
}

func handleEventarcPatchEnrollment(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "enrollment")
	key := eventarcResKey(project, location, id)
	existing, ok := eventarcEnrollments.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "enrollment %q not found", id)
		return
	}
	var req EventarcEnrollment
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if req.Labels != nil {
		existing.Labels = req.Labels
	}
	if req.Annotations != nil {
		existing.Annotations = req.Annotations
	}
	if req.DisplayName != "" {
		existing.DisplayName = req.DisplayName
	}
	if req.CelMatch != "" {
		existing.CelMatch = req.CelMatch
	}
	if req.Destination != "" {
		existing.Destination = req.Destination
	}
	existing.UpdateTime = nowTimestamp()
	eventarcEnrollments.Put(key, existing)
	op := newLRO(project, location, existing, "type.googleapis.com/google.cloud.eventarc.v1.Enrollment")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcDeleteEnrollment(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "enrollment")
	key := eventarcResKey(project, location, id)
	e, ok := eventarcEnrollments.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "enrollment %q not found", id)
		return
	}
	eventarcEnrollments.Delete(key)
	op := newLRO(project, location, e, "type.googleapis.com/google.cloud.eventarc.v1.Enrollment")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcEnrollmentIAMAction(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	eventarcIAMVerb(w, r, sim.PathParam(r, "enrollmentAction"),
		func(id string) string { return eventarcEnrollmentName(project, location, id) }, "enrollment")
}

func handleEventarcCreateMessageBus(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("messageBusId")
	if id == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "messageBusId is required")
		return
	}
	var req EventarcMessageBus
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	key := eventarcResKey(project, location, id)
	if _, exists := eventarcMessageBuses.Get(key); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "messageBus %q already exists", eventarcMessageBusName(project, location, id))
		return
	}
	now := nowTimestamp()
	req.Name = eventarcMessageBusName(project, location, id)
	req.Uid = generateUUID()
	req.Etag = generateUUID()
	req.CreateTime = now
	req.UpdateTime = now
	eventarcMessageBuses.Put(key, req)
	op := newLRO(project, location, req, "type.googleapis.com/google.cloud.eventarc.v1.MessageBus")
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleEventarcMessageBusGet serves the plain Get plus the two colon-verbs
// the messageBus resource exposes on GET: getIamPolicy and listEnrollments.
func handleEventarcMessageBusGet(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	bus := sim.PathParam(r, "bus")
	if id, action, found := strings.Cut(bus, ":"); found {
		switch action {
		case "getIamPolicy":
			handleResourceIAM(w, r, gcpResourceIAMStore(), eventarcMessageBusName(project, location, id), action)
		case "listEnrollments":
			handleEventarcMessageBusListEnrollments(w, r, project, location, id)
		default:
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on messageBus %q", action, id)
		}
		return
	}
	mb, ok := eventarcMessageBuses.Get(eventarcResKey(project, location, bus))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "messageBus %q not found", bus)
		return
	}
	sim.WriteJSON(w, http.StatusOK, mb)
}

// handleEventarcMessageBusListEnrollments returns the names of every enrollment
// whose messageBus field references this bus (ListMessageBusEnrollmentsResponse:
// enrollments is an array of resource-name strings).
func handleEventarcMessageBusListEnrollments(w http.ResponseWriter, r *http.Request, project, location, id string) {
	busName := eventarcMessageBusName(project, location, id)
	if _, ok := eventarcMessageBuses.Get(eventarcResKey(project, location, id)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "messageBus %q not found", id)
		return
	}
	names := make([]string, 0)
	for _, e := range eventarcEnrollments.List() {
		if e.MessageBus == busName {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"enrollments": names})
}

func handleEventarcListMessageBuses(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/messageBuses/", project, location)
	out := make([]EventarcMessageBus, 0)
	for _, mb := range eventarcMessageBuses.List() {
		if strings.HasPrefix(mb.Name, prefix) {
			out = append(out, mb)
		}
	}
	writeEventarcList(w, r, out, func(mb EventarcMessageBus) string { return mb.Name }, "messageBuses")
}

func handleEventarcPatchMessageBus(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "bus")
	key := eventarcResKey(project, location, id)
	existing, ok := eventarcMessageBuses.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "messageBus %q not found", id)
		return
	}
	var req EventarcMessageBus
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if req.Labels != nil {
		existing.Labels = req.Labels
	}
	if req.Annotations != nil {
		existing.Annotations = req.Annotations
	}
	if req.DisplayName != "" {
		existing.DisplayName = req.DisplayName
	}
	if req.CryptoKeyName != "" {
		existing.CryptoKeyName = req.CryptoKeyName
	}
	if req.LoggingConfig != nil {
		existing.LoggingConfig = req.LoggingConfig
	}
	existing.UpdateTime = nowTimestamp()
	eventarcMessageBuses.Put(key, existing)
	op := newLRO(project, location, existing, "type.googleapis.com/google.cloud.eventarc.v1.MessageBus")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcDeleteMessageBus(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "bus")
	key := eventarcResKey(project, location, id)
	mb, ok := eventarcMessageBuses.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "messageBus %q not found", id)
		return
	}
	eventarcMessageBuses.Delete(key)
	op := newLRO(project, location, mb, "type.googleapis.com/google.cloud.eventarc.v1.MessageBus")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcMessageBusIAMAction(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	eventarcIAMVerb(w, r, sim.PathParam(r, "busAction"),
		func(id string) string { return eventarcMessageBusName(project, location, id) }, "messageBus")
}

func handleEventarcCreatePipeline(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("pipelineId")
	if id == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "pipelineId is required")
		return
	}
	var req EventarcPipeline
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	key := eventarcResKey(project, location, id)
	if _, exists := eventarcPipelines.Get(key); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "pipeline %q already exists", eventarcPipelineName(project, location, id))
		return
	}
	now := nowTimestamp()
	req.Name = eventarcPipelineName(project, location, id)
	req.Uid = generateUUID()
	req.Etag = generateUUID()
	req.CreateTime = now
	req.UpdateTime = now
	eventarcPipelines.Put(key, req)
	op := newLRO(project, location, req, "type.googleapis.com/google.cloud.eventarc.v1.Pipeline")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcGetPipeline(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "pipeline")
	if rid, action, found := strings.Cut(id, ":"); found {
		if action == "getIamPolicy" {
			handleResourceIAM(w, r, gcpResourceIAMStore(), eventarcPipelineName(project, location, rid), action)
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on pipeline %q", action, rid)
		return
	}
	p, ok := eventarcPipelines.Get(eventarcResKey(project, location, id))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "pipeline %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleEventarcListPipelines(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/pipelines/", project, location)
	out := make([]EventarcPipeline, 0)
	for _, p := range eventarcPipelines.List() {
		if strings.HasPrefix(p.Name, prefix) {
			out = append(out, p)
		}
	}
	writeEventarcList(w, r, out, func(p EventarcPipeline) string { return p.Name }, "pipelines")
}

func handleEventarcPatchPipeline(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "pipeline")
	key := eventarcResKey(project, location, id)
	existing, ok := eventarcPipelines.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "pipeline %q not found", id)
		return
	}
	var req EventarcPipeline
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if req.Labels != nil {
		existing.Labels = req.Labels
	}
	if req.Annotations != nil {
		existing.Annotations = req.Annotations
	}
	if req.DisplayName != "" {
		existing.DisplayName = req.DisplayName
	}
	if req.Destinations != nil {
		existing.Destinations = req.Destinations
	}
	if req.Mediations != nil {
		existing.Mediations = req.Mediations
	}
	if req.CryptoKeyName != "" {
		existing.CryptoKeyName = req.CryptoKeyName
	}
	if req.InputPayloadFormat != nil {
		existing.InputPayloadFormat = req.InputPayloadFormat
	}
	if req.LoggingConfig != nil {
		existing.LoggingConfig = req.LoggingConfig
	}
	if req.RetryPolicy != nil {
		existing.RetryPolicy = req.RetryPolicy
	}
	existing.UpdateTime = nowTimestamp()
	eventarcPipelines.Put(key, existing)
	op := newLRO(project, location, existing, "type.googleapis.com/google.cloud.eventarc.v1.Pipeline")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcDeletePipeline(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "pipeline")
	key := eventarcResKey(project, location, id)
	p, ok := eventarcPipelines.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "pipeline %q not found", id)
		return
	}
	eventarcPipelines.Delete(key)
	op := newLRO(project, location, p, "type.googleapis.com/google.cloud.eventarc.v1.Pipeline")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcPipelineIAMAction(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	eventarcIAMVerb(w, r, sim.PathParam(r, "pipelineAction"),
		func(id string) string { return eventarcPipelineName(project, location, id) }, "pipeline")
}

func handleEventarcCreateGoogleAPISource(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := r.URL.Query().Get("googleApiSourceId")
	if id == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "googleApiSourceId is required")
		return
	}
	var req EventarcGoogleAPISource
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	key := eventarcResKey(project, location, id)
	if _, exists := eventarcGoogleAPISources.Get(key); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "googleApiSource %q already exists", eventarcGoogleAPISourceName(project, location, id))
		return
	}
	now := nowTimestamp()
	req.Name = eventarcGoogleAPISourceName(project, location, id)
	req.Uid = generateUUID()
	req.Etag = generateUUID()
	req.CreateTime = now
	req.UpdateTime = now
	eventarcGoogleAPISources.Put(key, req)
	op := newLRO(project, location, req, "type.googleapis.com/google.cloud.eventarc.v1.GoogleApiSource")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcGetGoogleAPISource(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "source")
	if rid, action, found := strings.Cut(id, ":"); found {
		if action == "getIamPolicy" {
			handleResourceIAM(w, r, gcpResourceIAMStore(), eventarcGoogleAPISourceName(project, location, rid), action)
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on googleApiSource %q", action, rid)
		return
	}
	s, ok := eventarcGoogleAPISources.Get(eventarcResKey(project, location, id))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "googleApiSource %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleEventarcListGoogleAPISources(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/googleApiSources/", project, location)
	out := make([]EventarcGoogleAPISource, 0)
	for _, s := range eventarcGoogleAPISources.List() {
		if strings.HasPrefix(s.Name, prefix) {
			out = append(out, s)
		}
	}
	writeEventarcList(w, r, out, func(s EventarcGoogleAPISource) string { return s.Name }, "googleApiSources")
}

func handleEventarcPatchGoogleAPISource(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "source")
	key := eventarcResKey(project, location, id)
	existing, ok := eventarcGoogleAPISources.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "googleApiSource %q not found", id)
		return
	}
	var req EventarcGoogleAPISource
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	if req.Labels != nil {
		existing.Labels = req.Labels
	}
	if req.Annotations != nil {
		existing.Annotations = req.Annotations
	}
	if req.DisplayName != "" {
		existing.DisplayName = req.DisplayName
	}
	if req.Destination != "" {
		existing.Destination = req.Destination
	}
	if req.CryptoKeyName != "" {
		existing.CryptoKeyName = req.CryptoKeyName
	}
	if req.LoggingConfig != nil {
		existing.LoggingConfig = req.LoggingConfig
	}
	existing.UpdateTime = nowTimestamp()
	eventarcGoogleAPISources.Put(key, existing)
	op := newLRO(project, location, existing, "type.googleapis.com/google.cloud.eventarc.v1.GoogleApiSource")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcDeleteGoogleAPISource(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "source")
	key := eventarcResKey(project, location, id)
	s, ok := eventarcGoogleAPISources.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "googleApiSource %q not found", id)
		return
	}
	eventarcGoogleAPISources.Delete(key)
	op := newLRO(project, location, s, "type.googleapis.com/google.cloud.eventarc.v1.GoogleApiSource")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleEventarcGoogleAPISourceIAMAction(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	eventarcIAMVerb(w, r, sim.PathParam(r, "sourceAction"),
		func(id string) string { return eventarcGoogleAPISourceName(project, location, id) }, "googleApiSource")
}

func eventarcGoogleChannelConfigName(project, location string) string {
	return fmt.Sprintf("projects/%s/locations/%s/googleChannelConfig", project, location)
}

func handleEventarcGetGoogleChannelConfig(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := eventarcGoogleChannelConfigName(project, location)
	cfg, ok := eventarcChannelConfigs.Get(name)
	if !ok {
		// The singleton always exists; return the default empty config.
		cfg = EventarcGoogleChannelConfig{Name: name, UpdateTime: nowTimestamp()}
		eventarcChannelConfigs.Put(name, cfg)
	}
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleEventarcUpdateGoogleChannelConfig(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	name := eventarcGoogleChannelConfigName(project, location)
	var req EventarcGoogleChannelConfig
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad request body: %v", err)
		return
	}
	cfg, ok := eventarcChannelConfigs.Get(name)
	if !ok {
		cfg = EventarcGoogleChannelConfig{Name: name}
	}
	if req.CryptoKeyName != "" {
		cfg.CryptoKeyName = req.CryptoKeyName
	}
	if req.Labels != nil {
		cfg.Labels = req.Labels
	}
	cfg.Name = name
	cfg.UpdateTime = nowTimestamp()
	eventarcChannelConfigs.Put(name, cfg)
	// PATCH on the singleton returns the updated resource directly (no LRO).
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleEventarcListLocations(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	locs := []map[string]any{}
	for _, id := range []string{"us-central1", "us-east1", "europe-west1", "asia-east1"} {
		locs = append(locs, map[string]any{
			"name":        fmt.Sprintf("projects/%s/locations/%s", project, id),
			"locationId":  id,
			"displayName": id,
		})
	}
	page, next, ok := paginateList(w, r, locs)
	if !ok {
		return
	}
	resp := map[string]any{"locations": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleEventarcGetLocation(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":        fmt.Sprintf("projects/%s/locations/%s", project, location),
		"locationId":  location,
		"displayName": location,
	})
}

func handleEventarcListOperations(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	prefix := fmt.Sprintf("projects/%s/locations/%s/operations/", project, location)
	out := make([]Operation, 0)
	for _, op := range crOperations.List() {
		if strings.HasPrefix(op.Name, prefix) {
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	page, next, ok := paginateList(w, r, out)
	if !ok {
		return
	}
	resp := map[string]any{"operations": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleEventarcDeleteOperation(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	opID := sim.PathParam(r, "operation")
	name := fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, opID)
	if _, ok := crOperations.Get(name); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
		return
	}
	crOperations.Delete(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

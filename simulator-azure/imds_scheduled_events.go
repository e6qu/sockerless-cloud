package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Scheduled Events, from "Azure Metadata Service: Scheduled Events for Linux
// VMs" (specs/cloud-api/azure/imds-scheduled-events.md.gz). A machine turns
// them on by asking for its events and off by not asking for 24 hours. While
// they are on, a restart, a redeploy or a Spot eviction first schedules an
// event: the operation waits until the guest approves the event or its
// NotBefore passes, the event reads Started while the operation runs, and it
// leaves the document when the operation ends.

// azureScheduledEventVersions lists the page's versions, newest first.
var azureScheduledEventVersions = []string{
	"2020-07-01", "2019-08-01", "2019-04-01", "2019-01-01", "2017-11-01", "2017-08-01", "2017-03-01",
}

// The page turns Scheduled Events off for a machine that has not asked for
// them in this long.
const azureScheduledEventsIdle = 24 * time.Hour

// azureScheduledEventKind is what an operation's event announces: its type,
// who started it, the minimum notice the page's event-scheduling table gives
// the type, and a description where the page states one.
type azureScheduledEventKind struct {
	eventType, source, description string
	notice                         time.Duration
}

var (
	azureRebootEvent = azureScheduledEventKind{
		eventType: "Reboot", source: "User", notice: 15 * time.Minute,
		description: "Virtual machine is going to be restarted as requested by authorized user.",
	}
	azureRedeployEvent = azureScheduledEventKind{eventType: "Redeploy", source: "User", notice: 10 * time.Minute}
	azurePreemptEvent  = azureScheduledEventKind{eventType: "Preempt", source: "Platform", notice: 30 * time.Second}
)

type azureScheduledEvent struct {
	id        string
	kind      azureScheduledEventKind
	resources []string
	status    string
	notBefore time.Time
	approved  chan struct{}
	approve   sync.Once
}

type azureScheduledEventsDocument struct {
	incarnation int
	lastRequest time.Time
	events      []*azureScheduledEvent
}

// The events live with the operations that wait on them, in this process.
var azureScheduledEvents = struct {
	sync.Mutex
	byVM map[string]*azureScheduledEventsDocument
}{byVM: map[string]*azureScheduledEventsDocument{}}

func azureScheduledEventsKey(vmID string) string { return strings.ToLower(vmID) }

// azureScheduledEventsEnabled reports whether the machine asked for its events
// within the last 24 hours.
func azureScheduledEventsEnabled(vmID string) bool {
	azureScheduledEvents.Lock()
	defer azureScheduledEvents.Unlock()
	doc, ok := azureScheduledEvents.byVM[azureScheduledEventsKey(vmID)]
	return ok && time.Since(doc.lastRequest) < azureScheduledEventsIdle
}

// azureScheduleVMEvent schedules the event an operation on the machine raises,
// or returns nil when the machine has Scheduled Events off.
func azureScheduleVMEvent(vm VirtualMachine, kind azureScheduledEventKind) *azureScheduledEvent {
	azureScheduledEvents.Lock()
	defer azureScheduledEvents.Unlock()
	doc, ok := azureScheduledEvents.byVM[azureScheduledEventsKey(vm.ID)]
	if !ok || time.Since(doc.lastRequest) >= azureScheduledEventsIdle {
		return nil
	}
	event := &azureScheduledEvent{
		id:        sim.NewUUID(),
		kind:      kind,
		resources: []string{vm.Name},
		status:    "Scheduled",
		notBefore: time.Now().UTC().Add(kind.notice).Truncate(time.Second),
		approved:  make(chan struct{}),
	}
	doc.events = append(doc.events, event)
	doc.incarnation++
	return event
}

// azureStartScheduledEvent holds the operation until the guest approves the
// event or its NotBefore passes, then marks it Started.
func azureStartScheduledEvent(ctx context.Context, vmID string, event *azureScheduledEvent) {
	timer := time.NewTimer(time.Until(event.notBefore))
	defer timer.Stop()
	select {
	case <-event.approved:
	case <-timer.C:
	case <-ctx.Done():
	}
	azureScheduledEvents.Lock()
	defer azureScheduledEvents.Unlock()
	event.status = "Started"
	event.notBefore = time.Time{}
	if doc, ok := azureScheduledEvents.byVM[azureScheduledEventsKey(vmID)]; ok {
		doc.incarnation++
	}
}

// azureEndScheduledEvent removes the event once its operation has ended.
func azureEndScheduledEvent(vmID string, event *azureScheduledEvent) {
	azureScheduledEvents.Lock()
	defer azureScheduledEvents.Unlock()
	doc, ok := azureScheduledEvents.byVM[azureScheduledEventsKey(vmID)]
	if !ok {
		return
	}
	for i, candidate := range doc.events {
		if candidate == event {
			doc.events = append(doc.events[:i], doc.events[i+1:]...)
			doc.incarnation++
			return
		}
	}
}

func azureForgetScheduledEvents(vmID string) {
	azureScheduledEvents.Lock()
	defer azureScheduledEvents.Unlock()
	delete(azureScheduledEvents.byVM, azureScheduledEventsKey(vmID))
}

// azureScheduledEventsRequested records a request from the machine, which
// turns its Scheduled Events on, and returns its document.
func azureScheduledEventsRequested(vmID string) *azureScheduledEventsDocument {
	key := azureScheduledEventsKey(vmID)
	doc, ok := azureScheduledEvents.byVM[key]
	if !ok {
		doc = &azureScheduledEventsDocument{incarnation: 1}
		azureScheduledEvents.byVM[key] = doc
	}
	doc.lastRequest = time.Now()
	return doc
}

// azureScheduledEventsAt renders the document as a version shows it:
// Description from 2019-04-01, EventSource from 2019-08-01, DurationInSeconds
// from 2020-07-01, and Preempt events from 2017-11-01.
func azureScheduledEventsAt(doc *azureScheduledEventsDocument, version string) map[string]any {
	events := []map[string]any{}
	for _, event := range doc.events {
		if event.kind.eventType == "Preempt" && version < "2017-11-01" {
			continue
		}
		notBefore := ""
		if !event.notBefore.IsZero() {
			notBefore = event.notBefore.Format(http.TimeFormat)
		}
		rendered := map[string]any{
			"EventId":      event.id,
			"EventType":    event.kind.eventType,
			"ResourceType": "VirtualMachine",
			"Resources":    event.resources,
			"EventStatus":  event.status,
			"NotBefore":    notBefore,
		}
		if version >= "2019-04-01" {
			rendered["Description"] = event.kind.description
		}
		if version >= "2019-08-01" {
			rendered["EventSource"] = event.kind.source
		}
		if version >= "2020-07-01" {
			// The page's value for an impact whose duration is unknown.
			rendered["DurationInSeconds"] = -1
		}
		events = append(events, rendered)
	}
	return map[string]any{"DocumentIncarnation": doc.incarnation, "Events": events}
}

func registerAzureScheduledEvents(srv *sim.Server) {
	srv.HandleFunc("GET /metadata/scheduledevents", func(w http.ResponseWriter, r *http.Request) {
		vm, version, ok := azureScheduledEventsRequest(w, r)
		if !ok {
			return
		}
		azureScheduledEvents.Lock()
		body := azureScheduledEventsAt(azureScheduledEventsRequested(vm.VM.ID), version)
		azureScheduledEvents.Unlock()
		sim.WriteJSON(w, http.StatusOK, body)
	})

	// The guest approves events by EventId. The page answers 200 for a valid
	// event, even one already approved, and 400 for a malformed header or
	// payload.
	srv.HandleFunc("POST /metadata/scheduledevents", func(w http.ResponseWriter, r *http.Request) {
		vm, _, ok := azureScheduledEventsRequest(w, r)
		if !ok {
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			azureIMDSBadRequest(w, "Bad request. The request body could not be read.")
			return
		}
		var payload struct {
			StartRequests []struct {
				EventID string `json:"EventId"`
			} `json:"StartRequests"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil || len(payload.StartRequests) == 0 {
			azureIMDSBadRequest(w, "Bad request. The body must carry StartRequests, each with an EventId.")
			return
		}
		approve, unknown := azureScheduledEventsToApprove(vm.VM.ID, payload.StartRequests)
		if unknown != "" {
			azureIMDSBadRequest(w, "Bad request. EventId '"+unknown+"' is not a current event.")
			return
		}
		// Answer before the approved operation can stop the guest that asked.
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		for _, event := range approve {
			event.approve.Do(func() { close(event.approved) })
		}
	})
}

// azureScheduledEventsToApprove finds each requested event among the
// machine's current ones, or names the first that is not.
func azureScheduledEventsToApprove(vmID string, requests []struct {
	EventID string `json:"EventId"`
}) ([]*azureScheduledEvent, string) {
	azureScheduledEvents.Lock()
	defer azureScheduledEvents.Unlock()
	doc := azureScheduledEventsRequested(vmID)
	var found []*azureScheduledEvent
	for _, request := range requests {
		var match *azureScheduledEvent
		for _, event := range doc.events {
			if strings.EqualFold(event.id, request.EventID) {
				match = event
			}
		}
		if match == nil {
			return nil, request.EventID
		}
		found = append(found, match)
	}
	return found, ""
}

func azureScheduledEventsRequest(w http.ResponseWriter, r *http.Request) (azureMetadataVM, string, bool) {
	if !mustMetadataHeader(w, r) {
		return azureMetadataVM{}, "", false
	}
	version, ok := azureIMDSQuery(w, r, azureScheduledEventVersions)
	if !ok {
		return azureMetadataVM{}, "", false
	}
	vm, ok := azureIMDSRequest(w, r)
	return vm, version, ok
}

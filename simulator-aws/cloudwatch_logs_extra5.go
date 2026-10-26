package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// StartLiveTail and GetLogObject arrive as awsJson1.1 requests but answer with
// AWS event streams; the handler writes one framed event per union member with
// the awsEventStreamMessage framing AWS Lambda's InvokeWithResponseStream
// uses.
func registerCloudWatchLogsExtra5(r *AWSRouter, srv *sim.Server) {
	_ = srv
	r.Register("Logs_20140328.StartLiveTail", handleCWStartLiveTail)
	r.Register("Logs_20140328.GetLogObject", handleCWGetLogObject)
}

// StartLiveTail requires ARNs, but the simulator also resolves a bare name.
func cwResolveLogGroupName(identifier string) (string, bool) {
	name := identifier
	if strings.HasPrefix(identifier, "arn:") {
		if idx := strings.Index(identifier, ":log-group:"); idx >= 0 {
			name = identifier[idx+len(":log-group:"):]
			name = strings.TrimSuffix(name, ":*")
		}
	}
	if _, ok := cwLogGroups.Get(name); ok {
		return name, true
	}
	return name, false
}

// After sessionStart, stream events ingested from then on, one sessionUpdate a
// second, until the client closes or three hours pass, as CloudWatch Logs
// does. Stored events are not replayed.
func handleCWStartLiveTail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifiers   []string `json:"logGroupIdentifiers"`
		LogStreamNames        []string `json:"logStreamNames"`
		LogStreamNamePrefixes []string `json:"logStreamNamePrefixes"`
		LogEventFilterPattern string   `json:"logEventFilterPattern"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.LogGroupIdentifiers) == 0 {
		AWSError(w, "InvalidParameterException",
			"logGroupIdentifiers is required", http.StatusBadRequest)
		return
	}

	// Refuse an unknown identifier before the stream opens.
	groupARNs := make(map[string]string, len(req.LogGroupIdentifiers))
	echoedIdentifiers := make([]string, 0, len(req.LogGroupIdentifiers))
	for _, id := range req.LogGroupIdentifiers {
		name, ok := cwResolveLogGroupName(id)
		if !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"The specified log group does not exist: %s", id)
			return
		}
		groupARNs[name] = cwLogGroupArn(name)
		echoedIdentifiers = append(echoedIdentifiers, groupARNs[name])
	}

	pattern, err := cwCompileLogPattern(req.LogEventFilterPattern)
	if err != nil {
		AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
		return
	}
	session := &cwLiveTailSession{
		groupARNs:      groupARNs,
		streamNames:    req.LogStreamNames,
		streamPrefixes: req.LogStreamNamePrefixes,
		pattern:        pattern,
	}
	cwLiveTailSubscribe(session)
	defer cwLiveTailUnsubscribe(session)

	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	// The controller reaches the connection through the wrapping middleware
	// writers, so each frame leaves as it is written.
	controller := http.NewResponseController(w)
	send := func(frame []byte) bool {
		if _, err := w.Write(frame); err != nil {
			return false
		}
		return controller.Flush() == nil
	}

	// The SDK's eventstream deserializer blocks until the initial-response
	// message; sending a data event first deadlocks its reader goroutine.
	if !send(awsEventStreamInitialResponse([]byte("{}"))) {
		return
	}
	streamPrefixes := req.LogStreamNamePrefixes
	if streamPrefixes == nil {
		streamPrefixes = []string{}
	}
	if !send(cwEventStreamFrame("sessionStart", map[string]any{
		"requestId":             sim.NewUUID(),
		"sessionId":             sim.NewUUID(),
		"logGroupIdentifiers":   echoedIdentifiers,
		"logStreamNames":        req.LogStreamNames,
		"logStreamNamePrefixes": streamPrefixes,
		"logEventFilterPattern": req.LogEventFilterPattern,
	})) {
		return
	}

	sim.DeclareWait(r.Context(), cwLiveTailSessionLimit)
	limit := time.NewTimer(cwLiveTailSessionLimit)
	defer limit.Stop()
	ticker := time.NewTicker(cwLiveTailUpdateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-limit.C:
			send(cwEventStreamException("SessionTimeoutException", map[string]any{
				"message": "Live Tail session has ended as the session timeout of 3 hours has been reached.",
			}))
			return
		case <-ticker.C:
			results, sampled := session.drain()
			if !send(cwEventStreamFrame("sessionUpdate", map[string]any{
				"sessionMetadata": map[string]any{"sampled": sampled},
				"sessionResults":  results,
			})) {
				return
			}
		}
	}
}

// One update a second, at most 500 events each (sampling the rest), three
// hours per session.
const (
	cwLiveTailUpdateInterval  = time.Second
	cwLiveTailMaxUpdateEvents = 500
	cwLiveTailSessionLimit    = 3 * time.Hour
)

type cwLiveTailEvent struct {
	LogStreamName      string `json:"logStreamName"`
	LogGroupIdentifier string `json:"logGroupIdentifier"`
	Message            string `json:"message"`
	Timestamp          int64  `json:"timestamp"`
	IngestionTime      int64  `json:"ingestionTime"`
}

type cwLiveTailSession struct {
	groupARNs      map[string]string
	streamNames    []string
	streamPrefixes []string
	pattern        *cwCompiledPattern

	mu      sync.Mutex
	pending []cwLiveTailEvent
	sampled bool
}

var cwLiveTails = struct {
	mu       sync.Mutex
	sessions map[*cwLiveTailSession]struct{}
}{sessions: map[*cwLiveTailSession]struct{}{}}

func cwLiveTailSubscribe(s *cwLiveTailSession) {
	cwLiveTails.mu.Lock()
	defer cwLiveTails.mu.Unlock()
	cwLiveTails.sessions[s] = struct{}{}
}

func cwLiveTailUnsubscribe(s *cwLiveTailSession) {
	cwLiveTails.mu.Lock()
	defer cwLiveTails.mu.Unlock()
	delete(cwLiveTails.sessions, s)
}

func cwLiveTailPublish(logGroup, logStream string, events []CWLogEvent) {
	cwLiveTails.mu.Lock()
	sessions := make([]*cwLiveTailSession, 0, len(cwLiveTails.sessions))
	for s := range cwLiveTails.sessions {
		sessions = append(sessions, s)
	}
	cwLiveTails.mu.Unlock()
	for _, s := range sessions {
		s.offer(logGroup, logStream, events)
	}
}

func (s *cwLiveTailSession) covers(logStream string) bool {
	if len(s.streamNames) > 0 {
		return slices.Contains(s.streamNames, logStream)
	}
	if len(s.streamPrefixes) > 0 {
		return slices.ContainsFunc(s.streamPrefixes, func(p string) bool { return strings.HasPrefix(logStream, p) })
	}
	return true
}

func (s *cwLiveTailSession) offer(logGroup, logStream string, events []CWLogEvent) {
	groupARN, ok := s.groupARNs[logGroup]
	if !ok || !s.covers(logStream) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range events {
		if !s.pattern.match(ev.Message) {
			continue
		}
		if len(s.pending) >= cwLiveTailMaxUpdateEvents {
			s.sampled = true
			continue
		}
		s.pending = append(s.pending, cwLiveTailEvent{
			LogStreamName:      logStream,
			LogGroupIdentifier: groupARN,
			Message:            ev.Message,
			Timestamp:          ev.Timestamp,
			IngestionTime:      ev.IngestionTime,
		})
	}
}

func (s *cwLiveTailSession) drain() ([]cwLiveTailEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	results, sampled := s.pending, s.sampled
	if results == nil {
		results = []cwLiveTailEvent{}
	}
	s.pending, s.sampled = nil, false
	return results, sampled
}

// The response union carries a single `fields` event taken from the stored log
// event the pointer names.
func handleCWGetLogObject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Unmask           bool   `json:"unmask"`
		LogObjectPointer string `json:"logObjectPointer"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogObjectPointer == "" {
		AWSError(w, "InvalidParameterException",
			"logObjectPointer is required", http.StatusBadRequest)
		return
	}

	// The simulator's logObjectPointer is "<logGroupName>:<logStreamName>",
	// optionally followed by ":<index>".
	data, ok := cwResolveLogObject(req.LogObjectPointer)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log object does not exist: %s", req.LogObjectPointer)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	// The SDK's eventstream deserializer blocks until the initial-response
	// message; sending the data event first deadlocks its reader goroutine.
	_, _ = w.Write(awsEventStreamInitialResponse([]byte("{}")))
	if flusher != nil {
		flusher.Flush()
	}

	_, _ = w.Write(cwEventStreamFrame("fields", map[string]any{
		"data": data,
	}))
	if flusher != nil {
		flusher.Flush()
	}
}

// Without an index, return the stream's newest event.
func cwResolveLogObject(pointer string) ([]byte, bool) {
	parts := strings.Split(pointer, ":")
	if len(parts) < 2 {
		return nil, false
	}
	group := parts[0]
	stream := parts[1]
	index := -1
	if len(parts) >= 3 {
		if n, err := parsePositiveInt(parts[2]); err == nil {
			index = n
		}
	}
	if _, ok := cwLogGroups.Get(group); !ok {
		return nil, false
	}
	events, ok := cwStreamEvents(cwEventsKey(group, stream))
	if !ok || len(events) == 0 {
		return nil, false
	}
	if index < 0 {
		index = len(events) - 1
	}
	if index >= len(events) {
		return nil, false
	}
	return []byte(events[index].Message), true
}

func parsePositiveInt(s string) (int, error) {
	if s == "" {
		return 0, errNotAnInt
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNotAnInt
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var errNotAnInt = &cwParseError{}

type cwParseError struct{}

func (*cwParseError) Error() string { return "not an integer" }

// The :event-type header carries the union member name as the Smithy model
// spells it; the SDK's decoder dispatches on it.
func cwEventStreamException(exceptionType string, payload any) []byte {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte("{}")
	}
	return awsEventStreamMessage(map[string]string{
		":message-type":   "exception",
		":exception-type": exceptionType,
		":content-type":   "application/json",
	}, body)
}

func cwEventStreamFrame(eventType string, payload any) []byte {
	body, err := json.Marshal(payload)
	if err != nil {
		// A marshal failure here is a simulator bug; send an empty body rather than a
		// corrupt frame.
		body = []byte("{}")
	}
	return awsEventStreamMessage(map[string]string{
		":message-type": "event",
		":event-type":   eventType,
		":content-type": "application/json",
	}, body)
}

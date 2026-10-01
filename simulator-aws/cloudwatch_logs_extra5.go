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

// CloudWatch Logs event-stream operations.
//
// StartLiveTail and GetLogObject are awsJson1.1 requests (dispatched off the
// X-Amz-Target header through the shared AWSRouter) whose *responses* are AWS
// event streams (Content-Type application/vnd.amazon.eventstream). The handler
// takes over the raw http.ResponseWriter and writes one framed event per
// member of the response-stream union, using the same awsEventStreamMessage
// framing Lambda's InvokeWithResponseStream uses, so aws-sdk-go-v2's
// eventstream decoder reassembles them natively.
func registerCloudWatchLogsExtra5(r *AWSRouter, srv *sim.Server) {
	_ = srv
	r.Register("Logs_20140328.StartLiveTail", handleCWStartLiveTail)
	r.Register("Logs_20140328.GetLogObject", handleCWGetLogObject)
}

// cwResolveLogGroupName maps a logGroupIdentifier — which may be a log-group
// ARN or a bare log-group name — to the stored log-group name. StartLiveTail's
// request requires ARNs, but the sim accepts either so a caller that passes a
// name still resolves.
func cwResolveLogGroupName(identifier string) (string, bool) {
	name := identifier
	if strings.HasPrefix(identifier, "arn:") {
		// arn:aws:logs:<region>:<acct>:log-group:<name>[:*]
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

// handleCWStartLiveTail opens a Live Tail session over the AWS event stream.
// After the sessionStart frame the session streams the log events ingested
// into the requested log groups from then on, one sessionUpdate a second, until
// the client closes the stream or the session reaches its three-hour limit, as
// CloudWatch Logs does. Events already stored are not replayed.
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

	// An unknown identifier is refused before the stream opens, with the
	// ResourceNotFoundException the real operation returns.
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
	// The controller reaches the connection through the middleware writers
	// that wrap w, so each frame leaves as it is written.
	controller := http.NewResponseController(w)
	send := func(frame []byte) bool {
		if _, err := w.Write(frame); err != nil {
			return false
		}
		return controller.Flush() == nil
	}

	// The aws-sdk-go-v2 eventstream deserializer blocks the StartLiveTail call
	// until it reads the initial-response message (StartLiveTailResponse has no
	// non-event members, so its payload is an empty document). It must precede
	// any data event, or the reader goroutine deadlocks pushing the first event
	// before a consumer attaches.
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

// CloudWatch Logs sends a Live Tail session one update a second, carries at
// most 500 events in it (sampling the rest), and ends a session after three
// hours.
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

// cwLiveTailPublish hands newly ingested events to every open Live Tail
// session that covers their log group and stream.
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

// handleCWGetLogObject streams a large logging object back over the AWS event
// stream. The GetLogObjectResponseStream union carries a single `fields` event
// member (FieldsData{ data }); the sim derives that data honestly from the
// stored log event the logObjectPointer references.
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

	// The logObjectPointer the sim honors is "<logGroupName>:<logStreamName>" —
	// the same key the event store uses — optionally followed by ":<index>" to
	// select a specific stored event. This is the deterministic pointer a prior
	// FilterLogEvents/Live Tail result over the sim would carry.
	data, ok := cwResolveLogObject(req.LogObjectPointer)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log object does not exist: %s", req.LogObjectPointer)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	// The aws-sdk-go-v2 eventstream deserializer blocks the GetLogObject call
	// until it reads the initial-response message (GetLogObjectResponse has no
	// non-event members, so its payload is an empty document). It must precede
	// the data event, or the reader goroutine deadlocks pushing it before a
	// consumer attaches.
	_, _ = w.Write(awsEventStreamInitialResponse([]byte("{}")))
	if flusher != nil {
		flusher.Flush()
	}

	// A single `fields` event carrying the object's data, then the stream ends.
	_, _ = w.Write(cwEventStreamFrame("fields", map[string]any{
		"data": data,
	}))
	if flusher != nil {
		flusher.Flush()
	}
}

// cwResolveLogObject maps a logObjectPointer of the form
// "<logGroupName>:<logStreamName>[:<index>]" to the stored log event's message
// bytes. Without a trailing index, the most recent event in the stream is
// returned. The returned bytes are the FieldsData `data` blob.
func cwResolveLogObject(pointer string) ([]byte, bool) {
	parts := strings.Split(pointer, ":")
	if len(parts) < 2 {
		return nil, false
	}
	group := parts[0]
	stream := parts[1]
	index := -1
	if len(parts) >= 3 {
		// A trailing numeric segment selects a specific event index.
		if n, err := parsePositiveInt(parts[2]); err == nil {
			index = n
		}
	}
	if _, ok := cwLogGroups.Get(group); !ok {
		return nil, false
	}
	events, ok := cwLogEvents.Get(cwEventsKey(group, stream))
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

// parsePositiveInt parses a non-negative base-10 integer, rejecting any
// non-digit input.
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

// cwEventStreamFrame encodes one event-stream frame for the given union member
// name and JSON payload, using the shared awsEventStreamMessage framing. The
// :event-type header carries the union member name exactly as the smithy model
// spells it, which is how aws-sdk-go-v2's eventstream decoder dispatches to the
// matching response-stream member type.
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
		// A response-stream payload assembled from sim-owned types must always
		// marshal; an error here is a sim bug, so surface an honest empty frame
		// body rather than silently corrupting the wire shape.
		body = []byte("{}")
	}
	return awsEventStreamMessage(map[string]string{
		":message-type": "event",
		":event-type":   eventType,
		":content-type": "application/json",
	}, body)
}

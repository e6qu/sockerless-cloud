package main

import (
	"encoding/json"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
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
func registerCloudWatchLogsExtra5(r *sim.AWSRouter, srv *sim.Server) {
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
// A single LiveTailSessionStart frame is emitted first, then a single
// LiveTailSessionUpdate frame carrying the log events currently stored across
// the requested log groups' streams (the sim's stored history is what a Live
// Tail session would surface), then the stream is closed. The session settles
// deterministically so the SDK reader completes rather than hanging.
func handleCWStartLiveTail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifiers   []string `json:"logGroupIdentifiers"`
		LogStreamNames        []string `json:"logStreamNames"`
		LogStreamNamePrefixes []string `json:"logStreamNamePrefixes"`
		LogEventFilterPattern string   `json:"logEventFilterPattern"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.LogGroupIdentifiers) == 0 {
		sim.AWSError(w, "InvalidParameterException",
			"logGroupIdentifiers is required", http.StatusBadRequest)
		return
	}

	// Resolve every identifier to a stored log group; an unknown one is a 404,
	// matching the ResourceNotFoundException the real op returns before the
	// stream opens.
	resolvedNames := make([]string, 0, len(req.LogGroupIdentifiers))
	for _, id := range req.LogGroupIdentifiers {
		name, ok := cwResolveLogGroupName(id)
		if !ok {
			sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"The specified log group does not exist: %s", id)
			return
		}
		resolvedNames = append(resolvedNames, name)
	}

	// streamFilter constrains which log streams contribute events, honoring the
	// optional logStreamNames / logStreamNamePrefixes request fields.
	streamFilter := func(streamName string) bool {
		if len(req.LogStreamNames) > 0 {
			for _, n := range req.LogStreamNames {
				if n == streamName {
					return true
				}
			}
			return false
		}
		if len(req.LogStreamNamePrefixes) > 0 {
			for _, p := range req.LogStreamNamePrefixes {
				if strings.HasPrefix(streamName, p) {
					return true
				}
			}
			return false
		}
		return true
	}

	sessionID := generateUUID()
	requestID := generateUUID()

	// The logGroupIdentifiers echoed in the sessionStart are the canonical ARNs
	// of the resolved groups (real Live Tail reports names+ARNs of included
	// groups).
	echoedIdentifiers := make([]string, 0, len(resolvedNames))
	for _, name := range resolvedNames {
		echoedIdentifiers = append(echoedIdentifiers, cwLogGroupArn(name))
	}

	// Gather the matching stored log events across the resolved groups, tagging
	// each with its source group ARN and stream name exactly as a
	// LiveTailSessionLogEvent carries them.
	type liveEvent struct {
		LogStreamName      string `json:"logStreamName"`
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		Message            string `json:"message"`
		Timestamp          int64  `json:"timestamp"`
		IngestionTime      int64  `json:"ingestionTime"`
	}
	results := make([]liveEvent, 0)
	for _, name := range resolvedNames {
		groupArn := cwLogGroupArn(name)
		for _, stream := range cwLogStreams.Filter(func(s CWLogStream) bool { return s.LogGroupName == name }) {
			if !streamFilter(stream.LogStreamName) {
				continue
			}
			events, ok := cwLogEvents.Get(cwEventsKey(name, stream.LogStreamName))
			if !ok {
				continue
			}
			for _, ev := range events {
				if req.LogEventFilterPattern != "" && !strings.Contains(ev.Message, req.LogEventFilterPattern) {
					continue
				}
				results = append(results, liveEvent{
					LogStreamName:      stream.LogStreamName,
					LogGroupIdentifier: groupArn,
					Message:            ev.Message,
					Timestamp:          ev.Timestamp,
					IngestionTime:      ev.IngestionTime,
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	// The aws-sdk-go-v2 eventstream deserializer blocks the StartLiveTail call
	// until it reads the initial-response message (StartLiveTailResponse has no
	// non-event members, so its payload is an empty document). It must precede
	// any data event, or the reader goroutine deadlocks pushing the first event
	// before a consumer attaches.
	_, _ = w.Write(awsEventStreamInitialResponse([]byte("{}")))
	if flusher != nil {
		flusher.Flush()
	}

	// sessionStart: identifies the session and the included log groups.
	_, _ = w.Write(cwEventStreamFrame("sessionStart", map[string]any{
		"requestId":           requestID,
		"sessionId":           sessionID,
		"logGroupIdentifiers": echoedIdentifiers,
		"logStreamNames":      req.LogStreamNames,
		"logStreamNamePrefixes": func() []string {
			if req.LogStreamNamePrefixes == nil {
				return []string{}
			}
			return req.LogStreamNamePrefixes
		}(),
		"logEventFilterPattern": req.LogEventFilterPattern,
	}))
	if flusher != nil {
		flusher.Flush()
	}

	// sessionUpdate: one update carrying the matched log events. Real Live Tail
	// emits an update every second; the sim emits a single deterministic update
	// over the stored history (an empty sessionResults array when nothing
	// matched — the honest-empty case) and then closes.
	_, _ = w.Write(cwEventStreamFrame("sessionUpdate", map[string]any{
		"sessionMetadata": map[string]any{"sampled": false},
		"sessionResults":  results,
	}))
	if flusher != nil {
		flusher.Flush()
	}
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogObjectPointer == "" {
		sim.AWSError(w, "InvalidParameterException",
			"logObjectPointer is required", http.StatusBadRequest)
		return
	}

	// The logObjectPointer the sim honors is "<logGroupName>:<logStreamName>" —
	// the same key the event store uses — optionally followed by ":<index>" to
	// select a specific stored event. This is the deterministic pointer a prior
	// FilterLogEvents/Live Tail result over the sim would carry.
	data, ok := cwResolveLogObject(req.LogObjectPointer)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
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

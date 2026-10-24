package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	ltype "google.golang.org/genproto/googleapis/logging/type"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/e6qu/sockerless-cloud/sim"
)

// cloudRunLogEntry is the Cloud Logging entry for one line a Cloud Run
// container wrote to stdout or stderr, stamped with the moment the line was
// written. A line that is a JSON object is a structured log: it becomes the
// entry's jsonPayload with the special fields the Logging agent documents
// moved out of it (specs/cloud-api/gcp/logging-agent-configuration.md.gz,
// "Special fields in structured payloads"), which Cloud Run's logging page
// refers container logs to. Any other line is the textPayload.
func cloudRunLogEntry(line sim.LogLine) LogEntry {
	entry := LogEntry{}
	if !line.Timestamp.IsZero() {
		entry.Timestamp = line.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	payload, ok := cloudRunStructuredLine(line.Text)
	if !ok {
		entry.TextPayload = line.Text
		return entry
	}
	liftCloudRunSpecialFields(&entry, payload)
	entry.JsonPayload = payload
	return entry
}

// cloudRunStructuredLine decodes a line that is one JSON object.
func cloudRunStructuredLine(text string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

// liftCloudRunSpecialFields moves the payload's special fields into the entry.
// The message stays in the payload: the agent moves it to textPayload only
// when it is the last field left and JSON detection is off, which the page
// says does not apply to a managed environment.
func liftCloudRunSpecialFields(entry *LogEntry, payload map[string]any) {
	if severity, ok := payload["severity"].(string); ok {
		if _, known := severityRank(severity); known {
			entry.Severity = strings.ToUpper(severity)
			delete(payload, "severity")
		}
	}
	if request, ok := payload["httpRequest"].(map[string]any); ok {
		if decoded, err := cloudRunDecodeHttpRequest(request); err == nil {
			entry.HttpRequest = decoded
			delete(payload, "httpRequest")
		}
	}
	if at, used := cloudRunStructuredTimestamp(payload); used != "" {
		entry.Timestamp = at.UTC().Format(time.RFC3339Nano)
		delete(payload, used)
		if used == "timestampSeconds" {
			delete(payload, "timestampNanos")
		}
	}
	if id, ok := payload["logging.googleapis.com/insertId"].(string); ok {
		entry.InsertID = id
		delete(payload, "logging.googleapis.com/insertId")
	}
	if labels, ok := cloudRunStringMap(payload["logging.googleapis.com/labels"]); ok {
		entry.Labels = labels
		delete(payload, "logging.googleapis.com/labels")
	}
	if operation, ok := cloudRunDecodeOperation(payload["logging.googleapis.com/operation"]); ok {
		entry.Operation = operation
		delete(payload, "logging.googleapis.com/operation")
	}
	if location, ok := payload["logging.googleapis.com/sourceLocation"].(map[string]any); ok {
		if decoded, err := cloudRunDecodeSourceLocation(location); err == nil {
			entry.SourceLocation = decoded
			delete(payload, "logging.googleapis.com/sourceLocation")
		}
	}
	if span, ok := payload["logging.googleapis.com/spanId"].(string); ok {
		entry.SpanID = span
		delete(payload, "logging.googleapis.com/spanId")
	}
	if trace, ok := payload["logging.googleapis.com/trace"].(string); ok {
		entry.Trace = trace
		delete(payload, "logging.googleapis.com/trace")
	}
	if sampled, ok := payload["logging.googleapis.com/trace_sampled"].(bool); ok {
		entry.TraceSampled = sampled
		delete(payload, "logging.googleapis.com/trace_sampled")
	}
}

// cloudRunStructuredTimestamp reads the payload's time in the order the agent
// searches: a timestamp object of seconds and nanos, the timestampSeconds and
// timestampNanos pair, then an RFC 3339 time string. It returns the key that
// held it, or "" when the payload states no time.
func cloudRunStructuredTimestamp(payload map[string]any) (time.Time, string) {
	if object, ok := payload["timestamp"].(map[string]any); ok {
		if seconds, ok := cloudRunJSONInt(object["seconds"]); ok {
			if nanos, ok := cloudRunJSONInt(object["nanos"]); ok {
				return time.Unix(seconds, nanos), "timestamp"
			}
		}
	}
	if seconds, ok := cloudRunJSONInt(payload["timestampSeconds"]); ok {
		if nanos, ok := cloudRunJSONInt(payload["timestampNanos"]); ok {
			return time.Unix(seconds, nanos), "timestampSeconds"
		}
	}
	if text, ok := payload["time"].(string); ok {
		if at, err := parseTimestamp(text); err == nil {
			return at, "time"
		}
	}
	return time.Time{}, ""
}

// cloudRunJSONInt reads an integer a JSON document spells as a number or as a
// string of digits, the two forms the sized fields take.
func cloudRunJSONInt(v any) (int64, bool) {
	switch v := v.(type) {
	case float64:
		if v != float64(int64(v)) {
			return 0, false
		}
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	}
	return 0, false
}

func cloudRunStringMap(v any) (map[string]string, bool) {
	object, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(object))
	for k, value := range object {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out[k] = text
	}
	return out, true
}

// cloudRunDecodeOperation reads a LogEntryOperation whose first and last the
// agent's own example spells as the strings "true" and "false".
func cloudRunDecodeOperation(v any) (*LogEntryOperation, bool) {
	object, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	flag := func(v any) (bool, bool) {
		switch v := v.(type) {
		case nil:
			return false, true
		case bool:
			return v, true
		case string:
			b, err := strconv.ParseBool(v)
			return b, err == nil
		}
		return false, false
	}
	first, ok := flag(object["first"])
	if !ok {
		return nil, false
	}
	last, ok := flag(object["last"])
	if !ok {
		return nil, false
	}
	id, _ := object["id"].(string)
	producer, _ := object["producer"].(string)
	return &LogEntryOperation{ID: id, Producer: producer, First: first, Last: last}, true
}

// cloudRunDecodeSourceLocation and cloudRunDecodeHttpRequest read the field
// through its protobuf JSON mapping, which takes the int64 members as either
// numbers or strings and latency as a Duration string.
func cloudRunDecodeSourceLocation(object map[string]any) (*LogEntrySourceLocation, error) {
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	var location loggingpb.LogEntrySourceLocation
	if err := protojson.Unmarshal(raw, &location); err != nil {
		return nil, err
	}
	return &LogEntrySourceLocation{File: location.GetFile(), Line: location.GetLine(), Function: location.GetFunction()}, nil
}

func cloudRunDecodeHttpRequest(object map[string]any) (*LogEntryHttpRequest, error) {
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	var request ltype.HttpRequest
	if err := protojson.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	return logEntryHttpRequestFromProto(&request), nil
}

func logEntryHttpRequestFromProto(req *ltype.HttpRequest) *LogEntryHttpRequest {
	out := &LogEntryHttpRequest{
		RequestMethod:                  req.GetRequestMethod(),
		RequestUrl:                     req.GetRequestUrl(),
		RequestSize:                    req.GetRequestSize(),
		Status:                         req.GetStatus(),
		ResponseSize:                   req.GetResponseSize(),
		UserAgent:                      req.GetUserAgent(),
		RemoteIp:                       req.GetRemoteIp(),
		ServerIp:                       req.GetServerIp(),
		Referer:                        req.GetReferer(),
		CacheLookup:                    req.GetCacheLookup(),
		CacheHit:                       req.GetCacheHit(),
		CacheValidatedWithOriginServer: req.GetCacheValidatedWithOriginServer(),
		CacheFillBytes:                 req.GetCacheFillBytes(),
		Protocol:                       req.GetProtocol(),
	}
	if req.Latency != nil {
		out.Latency = strconv.FormatFloat(req.Latency.AsDuration().Seconds(), 'f', -1, 64) + "s"
	}
	return out
}

func logEntryHttpRequestToProto(req *LogEntryHttpRequest) (*ltype.HttpRequest, error) {
	out := &ltype.HttpRequest{
		RequestMethod:                  req.RequestMethod,
		RequestUrl:                     req.RequestUrl,
		RequestSize:                    req.RequestSize,
		Status:                         req.Status,
		ResponseSize:                   req.ResponseSize,
		UserAgent:                      req.UserAgent,
		RemoteIp:                       req.RemoteIp,
		ServerIp:                       req.ServerIp,
		Referer:                        req.Referer,
		CacheLookup:                    req.CacheLookup,
		CacheHit:                       req.CacheHit,
		CacheValidatedWithOriginServer: req.CacheValidatedWithOriginServer,
		CacheFillBytes:                 req.CacheFillBytes,
		Protocol:                       req.Protocol,
	}
	if req.Latency != "" {
		latency, err := time.ParseDuration(req.Latency)
		if err != nil {
			return nil, fmt.Errorf("latency %q: %w", req.Latency, err)
		}
		out.Latency = durationpb.New(latency)
	}
	return out, nil
}

// cloudRunLogName is the log a Cloud Run container's stream lands in.
func cloudRunLogName(project, stream string) string {
	if stream != "stderr" {
		stream = "stdout"
	}
	return fmt.Sprintf("projects/%s/logs/run.googleapis.com%%2F%s", project, stream)
}

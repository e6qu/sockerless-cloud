package main

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Azure Monitor Activity Log: the subscription's administrative events.
// Azure Resource Manager writes a BeginRequest event when a write, delete or
// action reaches it and an EndRequest event with the outcome when it answers,
// and Activity Logs - List (GET
// /subscriptions/{subscriptionId}/providers/Microsoft.Insights/eventtypes/management/values)
// reads them back newest first, filtered by time span and by resource group,
// resource, provider, correlation ID, caller, status or level. Events older
// than the service's 90-day retention are dropped.

const activityLogRetention = 90 * 24 * time.Hour

type activityLogValue struct {
	Value          string `json:"value"`
	LocalizedValue string `json:"localizedValue"`
}

// ActivityLogEvent is the EventData shape Activity Logs - List returns.
type ActivityLogEvent struct {
	Authorization        map[string]string `json:"authorization"`
	Caller               string            `json:"caller,omitempty"`
	Channels             string            `json:"channels"`
	CorrelationID        string            `json:"correlationId"`
	Description          string            `json:"description"`
	EventDataID          string            `json:"eventDataId"`
	EventName            activityLogValue  `json:"eventName"`
	Category             activityLogValue  `json:"category"`
	HTTPRequest          map[string]string `json:"httpRequest"`
	ID                   string            `json:"id"`
	Level                string            `json:"level"`
	ResourceGroupName    string            `json:"resourceGroupName,omitempty"`
	ResourceProviderName activityLogValue  `json:"resourceProviderName"`
	ResourceID           string            `json:"resourceId"`
	ResourceType         activityLogValue  `json:"resourceType"`
	OperationID          string            `json:"operationId"`
	OperationName        activityLogValue  `json:"operationName"`
	Properties           map[string]string `json:"properties"`
	Status               activityLogValue  `json:"status"`
	SubStatus            activityLogValue  `json:"subStatus"`
	EventTimestamp       string            `json:"eventTimestamp"`
	SubmissionTimestamp  string            `json:"submissionTimestamp"`
	SubscriptionID       string            `json:"subscriptionId"`
	TenantID             string            `json:"tenantId"`
}

var (
	azureActivityLog     sim.Store[ActivityLogEvent]
	activityLogLastPrune atomic.Int64
)

func registerActivityLog(srv *sim.Server) {
	azureActivityLog = sim.MakeStore[ActivityLogEvent](srv.DB(), "azure_activity_log")
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/eventtypes/management/values", handleActivityLogList)
}

// activityLogOperation names what a request does in Azure RBAC action form,
// with the resource it acts on.
type activityLogOperation struct {
	resourceID    string
	resourceGroup string
	provider      string
	resourceType  string
	action        string
}

// activityLogOperationOf maps an ARM request to its operation, or reports
// false for a request the Activity Log does not record: a read, or a query
// that changes nothing.
func activityLogOperationOf(r *http.Request) (activityLogOperation, bool) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete && r.Method != http.MethodPost {
		return activityLogOperation{}, false
	}
	// The log names a resource by its canonical ID, whatever casing the
	// client spelled the path in.
	canonical := r.Clone(r.Context())
	azureNormalizeRequestPath(canonical)
	segments := strings.Split(strings.Trim(canonical.URL.Path, "/"), "/")
	if len(segments) < 2 || !strings.EqualFold(segments[0], "subscriptions") {
		return activityLogOperation{}, false
	}
	resourceSegments := segments
	verb := "write"
	if r.Method == http.MethodDelete {
		verb = "delete"
	}
	if r.Method == http.MethodPost {
		last := segments[len(segments)-1]
		if strings.EqualFold(last, "checkNameAvailability") || strings.EqualFold(last, "validateMoveResources") {
			return activityLogOperation{}, false
		}
		resourceSegments = segments[:len(segments)-1]
		verb = last + "/action"
	}
	op := activityLogOperation{resourceID: "/" + strings.Join(resourceSegments, "/")}
	switch {
	case len(resourceSegments) >= 4 && strings.EqualFold(resourceSegments[2], "resourceGroups"):
		op.resourceGroup = resourceSegments[3]
		if len(resourceSegments) >= 7 && strings.EqualFold(resourceSegments[4], "providers") {
			op.provider = resourceSegments[5]
			op.resourceType = activityLogType(resourceSegments[5:])
		} else {
			op.provider = "Microsoft.Resources"
			op.resourceType = "Microsoft.Resources/subscriptions/resourceGroups"
		}
	case len(resourceSegments) >= 5 && strings.EqualFold(resourceSegments[2], "providers"):
		op.provider = resourceSegments[3]
		op.resourceType = activityLogType(resourceSegments[3:])
	default:
		return activityLogOperation{}, false
	}
	op.action = op.resourceType + "/" + verb
	return op, true
}

// activityLogType reads `Namespace/type/name[/childType/childName...]` as the
// type path `Namespace/type[/childType...]`.
func activityLogType(provider []string) string {
	parts := []string{provider[0]}
	for i := 1; i < len(provider); i += 2 {
		parts = append(parts, provider[i])
	}
	return strings.Join(parts, "/")
}

// activityLogDisplay is the operation's localized name: "Create or Update
// Storage Account", "Delete Resource Group", "List Keys Storage Account".
func activityLogDisplay(op activityLogOperation) string {
	typeParts := strings.Split(op.resourceType, "/")
	noun := activityLogWords(activityLogSingular(typeParts[len(typeParts)-1]))
	verb := op.action[len(op.resourceType)+1:]
	switch verb {
	case "write":
		return "Create or Update " + noun
	case "delete":
		return "Delete " + noun
	default:
		return activityLogWords(strings.TrimSuffix(verb, "/action")) + " " + noun
	}
}

// activityLogSingular names one of a resource type: storageAccounts is a
// storage account, publicIPAddresses a public IP address, and Redis stays
// Redis.
func activityLogSingular(plural string) string {
	lower := strings.ToLower(plural)
	switch {
	case strings.HasSuffix(lower, "sses"):
		return plural[:len(plural)-2]
	case strings.HasSuffix(lower, "ies"):
		return plural[:len(plural)-3] + "y"
	case strings.HasSuffix(lower, "is"), strings.HasSuffix(lower, "ss"):
		return plural
	}
	return strings.TrimSuffix(plural, "s")
}

// activityLogWords spells an identifier as title-cased words, keeping an
// acronym together: publicIPAddress is "Public IP Address".
func activityLogWords(identifier string) string {
	runes := []rune(identifier)
	var out []rune
	for i, char := range runes {
		if i == 0 {
			out = append(out, unicode.ToUpper(char))
			continue
		}
		previous := runes[i-1]
		nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if unicode.IsUpper(char) && (unicode.IsLower(previous) || (unicode.IsUpper(previous) && nextLower)) {
			out = append(out, ' ')
		}
		out = append(out, char)
	}
	return string(out)
}

// activityLogCaller names who made the request from its bearer token: a
// user's principal name, or an application's object ID.
func activityLogCaller(r *http.Request) (caller, tenant string) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", simTenantID
	}
	claims, err := verifyAzureSimJWT(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
	if err != nil {
		return "", simTenantID
	}
	tenant, _ = claims["tid"].(string)
	if tenant == "" {
		tenant = simTenantID
	}
	for _, claim := range []string{"upn", "unique_name", "preferred_username", "oid", "appid"} {
		if value, ok := claims[claim].(string); ok && value != "" {
			return value, tenant
		}
	}
	return "", tenant
}

func activityLogSubStatus(code int) activityLogValue {
	text := http.StatusText(code)
	value := strings.ReplaceAll(text, " ", "")
	return activityLogValue{Value: value, LocalizedValue: fmt.Sprintf("%s (HTTP Status Code: %d)", text, code)}
}

type activityLogRecorder struct {
	http.ResponseWriter
	status int
}

func (w *activityLogRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *activityLogRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *activityLogRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// AzureActivityLogMiddleware records the BeginRequest and EndRequest events
// of every ARM write, delete and action that reaches the resource providers.
func AzureActivityLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op, ok := activityLogOperationOf(r)
		if !ok || azureActivityLog == nil {
			next.ServeHTTP(w, r)
			return
		}
		caller, tenant := activityLogCaller(r)
		subscription := strings.Split(strings.Trim(r.URL.Path, "/"), "/")[1]
		correlationID := r.Header.Get("x-ms-correlation-request-id")
		if correlationID == "" {
			correlationID = sim.NewUUID()
		}
		clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		operationID := sim.NewUUID()
		event := func(name, status string, subStatus activityLogValue, level string, properties map[string]string) {
			now := time.Now().UTC()
			id := sim.NewUUID()
			azureActivityLog.Put(id, ActivityLogEvent{
				Authorization: map[string]string{"action": op.action, "scope": op.resourceID},
				Caller:        caller,
				Channels:      "Operation",
				CorrelationID: correlationID,
				EventDataID:   id,
				EventName:     activityLogValue{Value: name, LocalizedValue: activityLogWords(strings.Replace(name, "Request", " request", 1))},
				Category:      activityLogValue{Value: "Administrative", LocalizedValue: "Administrative"},
				HTTPRequest: map[string]string{
					"clientRequestId": r.Header.Get("x-ms-client-request-id"),
					"clientIpAddress": clientIP,
					"method":          r.Method,
				},
				ID:                   fmt.Sprintf("%s/events/%s/ticks/%d", op.resourceID, id, now.UnixNano()/100+621355968000000000),
				Level:                level,
				ResourceGroupName:    op.resourceGroup,
				ResourceProviderName: activityLogValue{Value: op.provider, LocalizedValue: op.provider},
				ResourceID:           op.resourceID,
				ResourceType:         activityLogValue{Value: op.resourceType, LocalizedValue: op.resourceType},
				OperationID:          operationID,
				OperationName:        activityLogValue{Value: op.action, LocalizedValue: activityLogDisplay(op)},
				Properties:           properties,
				Status:               activityLogValue{Value: status, LocalizedValue: status},
				SubStatus:            subStatus,
				EventTimestamp:       now.Format(time.RFC3339Nano),
				SubmissionTimestamp:  now.Format(time.RFC3339Nano),
				SubscriptionID:       subscription,
				TenantID:             tenant,
			})
		}
		event("BeginRequest", "Started", activityLogValue{}, "Informational", map[string]string{})
		recorder := &activityLogRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		code := recorder.status
		if code == 0 {
			code = http.StatusOK
		}
		status, level := "Succeeded", "Informational"
		switch {
		case code == http.StatusAccepted:
			status = "Accepted"
		case code >= 400:
			status, level = "Failed", "Error"
		}
		event("EndRequest", status, activityLogSubStatus(code), level, map[string]string{"statusCode": activityLogSubStatus(code).Value})
		activityLogPrune()
	})
}

// activityLogPrune drops events past the retention period, at most once an
// hour so a busy subscription does not rescan its log on every write.
func activityLogPrune() {
	now := time.Now()
	last := activityLogLastPrune.Load()
	if now.UnixNano()-last < int64(time.Hour) || !activityLogLastPrune.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	cutoff := now.Add(-activityLogRetention)
	for _, event := range azureActivityLog.List() {
		if at, err := time.Parse(time.RFC3339Nano, event.EventTimestamp); err == nil && at.Before(cutoff) {
			azureActivityLog.Delete(event.EventDataID)
		}
	}
}

type activityLogFilter struct {
	from, to                                            time.Time
	resourceGroup, resourceURI, provider, correlationID string
	caller, status, subStatus                           string
	levels                                              []string
}

// parseActivityLogFilter reads the documented $filter grammar: a time span
// (eventTimestamp ge … and eventTimestamp le …) and at most one of the other
// conditions, joined by `and`.
func parseActivityLogFilter(raw string) (activityLogFilter, error) {
	var filter activityLogFilter
	if strings.TrimSpace(raw) == "" {
		return filter, fmt.Errorf("the $filter query parameter is required and must name an eventTimestamp range")
	}
	for _, term := range splitActivityLogAnd(raw) {
		fields := strings.SplitN(strings.TrimSpace(term), " ", 3)
		if len(fields) != 3 {
			return filter, fmt.Errorf("the filter condition %q is not valid", term)
		}
		field, operator := strings.ToLower(fields[0]), strings.ToLower(fields[1])
		value := strings.TrimSpace(fields[2])
		// The Azure CLI sends the timestamps bare and the documentation quotes
		// them; the service takes either.
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
		}
		switch {
		case field == "eventtimestamp" && (operator == "ge" || operator == "le"):
			at, err := activityLogTime(value)
			if err != nil {
				return filter, fmt.Errorf("the eventTimestamp %q is not an ISO 8601 time", value)
			}
			if operator == "ge" {
				filter.from = at
			} else {
				filter.to = at
			}
		case operator != "eq":
			return filter, fmt.Errorf("the operator %q is not supported for %s", fields[1], fields[0])
		case field == "resourcegroupname":
			filter.resourceGroup = value
		case field == "resourceuri" || field == "resourceid":
			filter.resourceURI = value
		case field == "resourceprovider":
			filter.provider = value
		case field == "correlationid":
			filter.correlationID = value
		case field == "caller":
			filter.caller = value
		case field == "status":
			filter.status = value
		case field == "substatus":
			filter.subStatus = value
		case field == "levels":
			filter.levels = strings.Split(value, ",")
		case field == "eventchannels":
		default:
			return filter, fmt.Errorf("the filter field %q is not supported", fields[0])
		}
	}
	if filter.from.IsZero() {
		return filter, fmt.Errorf("the $filter must include eventTimestamp ge")
	}
	if filter.to.IsZero() {
		filter.to = time.Now().UTC()
	}
	return filter, nil
}

// activityLogTime reads an ISO 8601 time; one with no offset is UTC, the form
// the Azure CLI sends.
func activityLogTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02"} {
		if at, err := time.Parse(layout, value); err == nil {
			return at.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not an ISO 8601 time", value)
}

func splitActivityLogAnd(raw string) []string {
	var terms []string
	lower := strings.ToLower(raw)
	start := 0
	inQuote := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\'' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && strings.HasPrefix(lower[i:], " and ") {
			terms = append(terms, raw[start:i])
			start = i + len(" and ")
			i = start - 1
		}
	}
	return append(terms, raw[start:])
}

func (f activityLogFilter) matches(event ActivityLogEvent) bool {
	at, err := time.Parse(time.RFC3339Nano, event.EventTimestamp)
	if err != nil || at.Before(f.from) || at.After(f.to) {
		return false
	}
	switch {
	case f.resourceGroup != "" && !strings.EqualFold(event.ResourceGroupName, f.resourceGroup):
		return false
	case f.resourceURI != "" && !strings.EqualFold(event.ResourceID, f.resourceURI) && !strings.HasPrefix(strings.ToLower(event.ResourceID), strings.ToLower(f.resourceURI)+"/"):
		return false
	case f.provider != "" && !strings.EqualFold(event.ResourceProviderName.Value, f.provider):
		return false
	case f.correlationID != "" && !strings.EqualFold(event.CorrelationID, f.correlationID):
		return false
	case f.caller != "" && !strings.EqualFold(event.Caller, f.caller):
		return false
	case f.status != "" && !strings.EqualFold(event.Status.Value, f.status):
		return false
	case f.subStatus != "" && !strings.EqualFold(event.SubStatus.Value, f.subStatus):
		return false
	}
	if len(f.levels) > 0 {
		for _, level := range f.levels {
			if strings.EqualFold(strings.TrimSpace(level), event.Level) {
				return true
			}
		}
		return false
	}
	return true
}

func handleActivityLogList(w http.ResponseWriter, r *http.Request) {
	subscription := sim.PathParam(r, "subscriptionId")
	filter, err := parseActivityLogFilter(r.URL.Query().Get("$filter"))
	if err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	var events []ActivityLogEvent
	for _, event := range azureActivityLog.List() {
		if strings.EqualFold(event.SubscriptionID, subscription) && filter.matches(event) {
			events = append(events, event)
		}
	}
	// Newest first: the key inverts the event's time.
	page, next, ok := armPage(w, r, events, func(event ActivityLogEvent) string {
		at, _ := time.Parse(time.RFC3339Nano, event.EventTimestamp)
		return strconv.FormatInt(math.MaxInt64-at.UnixNano(), 10) + event.EventDataID
	})
	if !ok {
		return
	}
	value := make([]any, 0, len(page))
	selected := activityLogSelect(r.URL.Query().Get("$select"))
	for _, event := range page {
		value = append(value, activityLogProject(event, selected))
	}
	body := map[string]any{"value": value}
	if next != "" {
		body["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func activityLogSelect(raw string) map[string]bool {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := map[string]bool{}
	for _, field := range strings.Split(raw, ",") {
		out[strings.ToLower(strings.TrimSpace(field))] = true
	}
	return out
}

// activityLogProject keeps the fields $select names, or every field when it
// names none.
func activityLogProject(event ActivityLogEvent, selected map[string]bool) any {
	if selected == nil {
		return event
	}
	full := map[string]any{
		"authorization":        event.Authorization,
		"caller":               event.Caller,
		"channels":             event.Channels,
		"correlationId":        event.CorrelationID,
		"description":          event.Description,
		"eventDataId":          event.EventDataID,
		"eventName":            event.EventName,
		"category":             event.Category,
		"httpRequest":          event.HTTPRequest,
		"id":                   event.ID,
		"level":                event.Level,
		"resourceGroupName":    event.ResourceGroupName,
		"resourceProviderName": event.ResourceProviderName,
		"resourceId":           event.ResourceID,
		"resourceType":         event.ResourceType,
		"operationId":          event.OperationID,
		"operationName":        event.OperationName,
		"properties":           event.Properties,
		"status":               event.Status,
		"subStatus":            event.SubStatus,
		"eventTimestamp":       event.EventTimestamp,
		"submissionTimestamp":  event.SubmissionTimestamp,
		"subscriptionId":       event.SubscriptionID,
		"tenantId":             event.TenantID,
	}
	out := map[string]any{}
	for key, value := range full {
		if selected[strings.ToLower(key)] {
			out[key] = value
		}
	}
	return out
}

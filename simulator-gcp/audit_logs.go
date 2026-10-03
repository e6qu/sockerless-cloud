package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"time"
)

// Cloud Audit Logs: every service writes an Admin Activity entry for each call
// that changes a resource's configuration or metadata, and a Data Access entry
// for a configuration read or a data read or write when the project's IAM
// policy enables that log type for the service. The entries land in the
// project's cloudaudit.googleapis.com logs and reach Eventarc's
// google.cloud.audit.log.v1.written triggers.

const (
	auditLogPayloadType = "type.googleapis.com/google.cloud.audit.AuditLog"
	auditLogActivity    = "activity"
	auditLogDataAccess  = "data_access"
)

// The audit log types of the IAM AuditLogConfig.LogType enum, plus the type
// of an Admin Activity call, which no auditConfig can switch off.
const (
	auditAdminWrite = "ADMIN_WRITE"
	auditAdminRead  = "ADMIN_READ"
	auditDataRead   = "DATA_READ"
	auditDataWrite  = "DATA_WRITE"
)

// auditRecord is one audited call.
type auditRecord struct {
	project      string
	serviceName  string
	methodName   string
	resourceName string
	logType      string
	resource     *MonitoredResource
	// location is where the audited resource lives, "global" for a resource
	// with no location; an Eventarc trigger receives the entries of its own
	// location.
	location string
	// currentLocations fills protoPayload.resourceLocation for the services
	// that report it.
	currentLocations []string
	permission       string
	request          map[string]any
	response         map[string]any
	// serviceData is the service's own google.protobuf.Any, such as the
	// policy delta of a SetIamPolicy call.
	serviceData map[string]any
	status      map[string]any
	// operation names the long-running operation the entry starts or ends.
	operation *LogEntryOperation
	// longRunning is the long-running operation the call started.
	longRunning *auditOperation
	caller      auditCaller
	at          time.Time
}

// auditCaller is who made the call and from where.
type auditCaller struct {
	authenticationInfo map[string]any
	// member is the IAM member the credential names, "" for the account's
	// operator; Data Access exemptions name members.
	member    string
	ip        string
	userAgent string
}

// auditCallerFromCredential resolves the bearer token a call presented to the
// identity audit logs record: a service account by its email, any other
// principal by the subject its token names.
func auditCallerFromCredential(authorization, remoteAddr, userAgent string) auditCaller {
	caller := auditCaller{authenticationInfo: map[string]any{}, userAgent: userAgent}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		caller.ip = host
	} else {
		caller.ip = remoteAddr
	}
	raw := strings.TrimSpace(authorization)
	if !strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		return caller
	}
	claims, err := verifiedAccessTokenClaims(strings.TrimSpace(raw[len("bearer "):]))
	if err != nil || claims.Sub == "" {
		return caller
	}
	caller.member, _ = gcpSubjectPrincipal(claims.Sub)
	if strings.Contains(claims.Sub, "@") {
		caller.authenticationInfo["principalEmail"] = claims.Sub
		caller.authenticationInfo["principalSubject"] = caller.member
	} else {
		caller.authenticationInfo["principalSubject"] = claims.Sub
	}
	return caller
}

// auditDataAccessEnabled reports whether the project's IAM policy has the
// service write logType Data Access entries for member: an auditConfig for
// the service or for allServices enables the type, and an exemption in either
// leaves the member's calls out.
func auditDataAccessEnabled(project, service, logType, member string) bool {
	if gcpProjectPolicies == nil {
		return false
	}
	policy, ok := gcpProjectPolicies.Get("project/" + project)
	if !ok {
		return false
	}
	enabled := false
	for _, config := range policy.AuditConfigs {
		if config.Service != service && config.Service != "allServices" {
			continue
		}
		for _, logConfig := range config.AuditLogConfigs {
			if logConfig.LogType != logType {
				continue
			}
			if member != "" && slices.Contains(logConfig.ExemptedMembers, member) {
				return false
			}
			enabled = true
		}
	}
	return enabled
}

// auditStatus is the google.rpc.Status JSON of a call's outcome: empty for
// success.
func auditStatus(code int, message string) map[string]any {
	if code == 0 {
		return map[string]any{}
	}
	return map[string]any{"code": code, "message": message}
}

func auditInsertID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// emitAuditLog writes the call's entry to the project's audit log, when the
// project's audit configuration has the service write it, and hands the entry
// to the Eventarc triggers that route it.
func emitAuditLog(rec auditRecord) {
	if rec.project == "" {
		return
	}
	logID := auditLogActivity
	if rec.logType != auditAdminWrite {
		if !auditDataAccessEnabled(rec.project, rec.serviceName, rec.logType, rec.caller.member) {
			return
		}
		logID = auditLogDataAccess
	}
	at := rec.at.UTC()
	payload := map[string]any{
		"@type":              auditLogPayloadType,
		"serviceName":        rec.serviceName,
		"methodName":         rec.methodName,
		"resourceName":       rec.resourceName,
		"authenticationInfo": rec.caller.authenticationInfo,
		"requestMetadata": map[string]any{
			"callerIp":                rec.caller.ip,
			"callerSuppliedUserAgent": rec.caller.userAgent,
			"requestAttributes":       map[string]any{"time": at.Format(time.RFC3339Nano), "auth": map[string]any{}},
			"destinationAttributes":   map[string]any{},
		},
		"status": rec.status,
	}
	if rec.permission != "" {
		payload["authorizationInfo"] = []any{map[string]any{
			"resource":           rec.resourceName,
			"permission":         rec.permission,
			"granted":            true,
			"resourceAttributes": map[string]any{},
		}}
	}
	if len(rec.currentLocations) > 0 {
		payload["resourceLocation"] = map[string]any{"currentLocations": rec.currentLocations}
	}
	if rec.request != nil {
		payload["request"] = rec.request
	}
	if rec.response != nil {
		payload["response"] = rec.response
	}
	if rec.serviceData != nil {
		payload["serviceData"] = rec.serviceData
	}
	severity := "NOTICE"
	if logID == auditLogDataAccess {
		severity = "INFO"
	}
	if len(rec.status) > 0 {
		severity = "ERROR"
	}
	entry := LogEntry{
		LogName:          "projects/" + rec.project + "/logs/cloudaudit.googleapis.com%2F" + logID,
		Resource:         rec.resource,
		Timestamp:        at.Format(time.RFC3339Nano),
		ReceiveTimestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Severity:         severity,
		InsertID:         auditInsertID(),
		ProtoPayload:     payload,
		Operation:        rec.operation,
	}
	writeLogEntries(entry.LogName, entry.Resource, nil, []LogEntry{entry})
	eventarcRouteAuditLog(entry, rec.project, rec.location)
}

// auditPathSegmentAfter is the segment that follows collection in a resource
// name: "svc" for "services" in projects/p/locations/l/services/svc.
func auditPathSegmentAfter(resourceName, collection string) string {
	segments := strings.Split(resourceName, "/")
	for i := 0; i+1 < len(segments); i++ {
		if segments[i] == collection {
			return segments[i+1]
		}
	}
	return ""
}

// auditLocation is the location a resource name places its resource in.
func auditLocation(resourceName string) string {
	if location := auditPathSegmentAfter(resourceName, "locations"); location != "" {
		return location
	}
	return "global"
}

// auditOperation is the long-running operation a call started.
type auditOperation struct {
	id   string
	done bool
	// settled reads the operation's outcome: the response and status its last
	// entry records, and whether it is done.
	settled func() (response, status map[string]any, done bool)
}

// emitAuditCall writes the entries of an audited call. A call that started a
// long-running operation writes one entry marked first and, when the
// operation ends, one marked last; one entry carries both when the operation
// was done by the time the call answered.
func emitAuditCall(rec auditRecord) {
	op := rec.longRunning
	if op == nil || op.id == "" {
		emitAuditLog(rec)
		return
	}
	rec.operation = &LogEntryOperation{ID: op.id, Producer: rec.serviceName, First: true, Last: op.done}
	emitAuditLog(rec)
	if op.done || op.settled == nil || len(rec.status) > 0 {
		return
	}
	last := func() {
		response, status, done := op.settled()
		if !done {
			return
		}
		end := rec
		end.request = nil
		end.response = response
		end.status = status
		end.at = time.Now()
		end.operation = &LogEntryOperation{ID: op.id, Producer: rec.serviceName, Last: true}
		emitAuditLog(end)
	}
	signal := gcpOperationDoneSignal(op.id)
	if _, _, done := op.settled(); done {
		gcpOperationSignalDone(op.id)
		last()
		return
	}
	go func() {
		<-signal
		last()
	}()
}

// auditLongRunningOperation is the google.longrunning.Operation a call
// answered, read back from the store that records it until it is done.
func auditLongRunningOperation(response map[string]any) *auditOperation {
	id, _ := response["name"].(string)
	done, _ := response["done"].(bool)
	return &auditOperation{id: id, done: done, settled: func() (map[string]any, map[string]any, bool) {
		op, ok := gcpLookupOperation(id)
		if !ok || !op.Done {
			return nil, nil, false
		}
		var result map[string]any
		if op.Response != nil {
			if raw, err := json.Marshal(op.Response); err == nil {
				_ = json.Unmarshal(raw, &result)
			}
		}
		if op.Error != nil {
			return result, auditStatus(op.Error.Code, op.Error.Message), true
		}
		return result, auditStatus(0, ""), true
	}}
}

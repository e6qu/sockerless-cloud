package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// EventBridge connectivity slice: API destinations, connections, global
// endpoints, partner event sources, and the consumer-side event sources they
// offer. These are real control-plane CRUD resources, modeled at the same
// AWS-JSON (X-Amz-Target: AWSEvents.<Op>) fidelity as the rule/target/archive
// slice in eventbridge.go.

// EBConnection models an EventBridge connection. The authorization parameters
// carry secrets (API key value, basic password, OAuth client secret); these are
// stored so the connection is functional but never echoed on read, matching real
// EventBridge — DescribeConnection returns only the non-secret descriptors
// (ApiKeyName, Username) plus a SecretArn referencing the stored secret.
type EBConnection struct {
	Name              string          `json:"Name"`
	Arn               string          `json:"Arn"`
	AuthorizationType string          `json:"AuthorizationType"`
	State             string          `json:"State"`
	StateReason       string          `json:"StateReason,omitempty"`
	Description       string          `json:"Description,omitempty"`
	KmsKeyIdentifier  string          `json:"KmsKeyIdentifier,omitempty"`
	SecretArn         string          `json:"SecretArn,omitempty"`
	AuthParameters    json.RawMessage `json:"-"`
	CreationTime      int64           `json:"CreationTime"`
	LastModifiedTime  int64           `json:"LastModifiedTime"`
	LastAuthorized    int64           `json:"LastAuthorizedTime"`
}

// EBApiDestination models an EventBridge API destination — the HTTP invocation
// target (endpoint + method + rate limit) bound to a connection.
type EBApiDestination struct {
	Name                string `json:"Name"`
	Arn                 string `json:"Arn"`
	ConnectionArn       string `json:"ConnectionArn"`
	InvocationEndpoint  string `json:"InvocationEndpoint"`
	HttpMethod          string `json:"HttpMethod"`
	State               string `json:"State"`
	Description         string `json:"Description,omitempty"`
	InvocationRateLimit *int32 `json:"InvocationRateLimitPerSecond,omitempty"`
	CreationTime        int64  `json:"CreationTime"`
	LastModifiedTime    int64  `json:"LastModifiedTime"`
}

// EBEndpoint models an EventBridge global endpoint. EventBuses / RoutingConfig /
// ReplicationConfig round-trip byte-exact as raw JSON so the describe/list shapes
// return exactly what create/update received (the terraform/SDK reader keys on
// the nested FailoverConfig / Primary / Secondary sub-objects).
type EBEndpoint struct {
	Name              string          `json:"Name"`
	Arn               string          `json:"Arn"`
	EndpointID        string          `json:"EndpointId"`
	EndpointURL       string          `json:"EndpointUrl"`
	Description       string          `json:"Description,omitempty"`
	RoleArn           string          `json:"RoleArn,omitempty"`
	State             string          `json:"State"`
	StateReason       string          `json:"StateReason,omitempty"`
	EventBuses        json.RawMessage `json:"-"`
	RoutingConfig     json.RawMessage `json:"-"`
	ReplicationConfig json.RawMessage `json:"-"`
	CreationTime      int64           `json:"CreationTime"`
	LastModifiedTime  int64           `json:"LastModifiedTime"`
}

// EBPartnerEventSource models a partner event source created by a SaaS partner
// (PartnerName/namespace/name) offered to a customer account. Creating one also
// offers it to that account as a consumer-side event source (PENDING) until the
// account creates a matching partner event bus (ACTIVE) or it is deleted.
type EBPartnerEventSource struct {
	Name         string `json:"Name"`
	Arn          string `json:"Arn"`
	Account      string `json:"Account"`
	CreatedBy    string `json:"CreatedBy"`
	State        string `json:"State"`
	CreationTime int64  `json:"CreationTime"`
	Expiration   int64  `json:"ExpirationTime"`
}

var (
	ebConnections    sim.Store[EBConnection]
	ebApiDest        sim.Store[EBApiDestination]
	ebEndpoints      sim.Store[EBEndpoint]
	ebPartnerSources sim.Store[EBPartnerEventSource]
)

func registerEventBridgeConnectivity(r *sim.AWSRouter, srv *sim.Server) {
	ebConnections = sim.MakeStore[EBConnection](srv.DB(), "eventbridge_connections")
	ebApiDest = sim.MakeStore[EBApiDestination](srv.DB(), "eventbridge_api_destinations")
	ebEndpoints = sim.MakeStore[EBEndpoint](srv.DB(), "eventbridge_endpoints")
	ebPartnerSources = sim.MakeStore[EBPartnerEventSource](srv.DB(), "eventbridge_partner_sources")

	r.Register("AWSEvents.CreateApiDestination", handleEBCreateApiDestination)
	r.Register("AWSEvents.DescribeApiDestination", handleEBDescribeApiDestination)
	r.Register("AWSEvents.ListApiDestinations", handleEBListApiDestinations)
	r.Register("AWSEvents.UpdateApiDestination", handleEBUpdateApiDestination)
	r.Register("AWSEvents.DeleteApiDestination", handleEBDeleteApiDestination)

	r.Register("AWSEvents.CreateConnection", handleEBCreateConnection)
	r.Register("AWSEvents.DescribeConnection", handleEBDescribeConnection)
	r.Register("AWSEvents.ListConnections", handleEBListConnections)
	r.Register("AWSEvents.UpdateConnection", handleEBUpdateConnection)
	r.Register("AWSEvents.DeauthorizeConnection", handleEBDeauthorizeConnection)
	r.Register("AWSEvents.DeleteConnection", handleEBDeleteConnection)

	r.Register("AWSEvents.CreateEndpoint", handleEBCreateEndpoint)
	r.Register("AWSEvents.DescribeEndpoint", handleEBDescribeEndpoint)
	r.Register("AWSEvents.ListEndpoints", handleEBListEndpoints)
	r.Register("AWSEvents.UpdateEndpoint", handleEBUpdateEndpoint)
	r.Register("AWSEvents.DeleteEndpoint", handleEBDeleteEndpoint)

	r.Register("AWSEvents.CreatePartnerEventSource", handleEBCreatePartnerEventSource)
	r.Register("AWSEvents.DescribePartnerEventSource", handleEBDescribePartnerEventSource)
	r.Register("AWSEvents.ListPartnerEventSources", handleEBListPartnerEventSources)
	r.Register("AWSEvents.ListPartnerEventSourceAccounts", handleEBListPartnerEventSourceAccounts)
	r.Register("AWSEvents.DeletePartnerEventSource", handleEBDeletePartnerEventSource)
	r.Register("AWSEvents.PutPartnerEvents", handleEBPutPartnerEvents)

	r.Register("AWSEvents.ActivateEventSource", handleEBActivateEventSource)
	r.Register("AWSEvents.DeactivateEventSource", handleEBDeactivateEventSource)
	r.Register("AWSEvents.DescribeEventSource", handleEBDescribeEventSource)
	r.Register("AWSEvents.ListEventSources", handleEBListEventSources)
}

func ebConnectionArn(name string) string {
	return fmt.Sprintf("arn:aws:events:%s:%s:connection/%s/%s", awsRegion(), awsAccountID(), name, generateUUID())
}

func ebApiDestArn(name string) string {
	return fmt.Sprintf("arn:aws:events:%s:%s:api-destination/%s/%s", awsRegion(), awsAccountID(), name, generateUUID())
}

func ebEndpointArn(name string) string {
	return fmt.Sprintf("arn:aws:events:%s:%s:endpoint/%s", awsRegion(), awsAccountID(), name)
}

func ebConnectionSecretArn(name string) string {
	return fmt.Sprintf("arn:aws:secretsmanager:%s:%s:secret:events!connection/%s/%s", awsRegion(), awsAccountID(), name, generateUUID())
}

func ebPartnerSourceArn(name string) string {
	return fmt.Sprintf("arn:aws:events:%s::event-source/aws.partner/%s", awsRegion(), name)
}

func handleEBCreateApiDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                         string `json:"Name"`
		ConnectionArn                string `json:"ConnectionArn"`
		InvocationEndpoint           string `json:"InvocationEndpoint"`
		HttpMethod                   string `json:"HttpMethod"`
		Description                  string `json:"Description"`
		InvocationRateLimitPerSecond *int32 `json:"InvocationRateLimitPerSecond"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.ConnectionArn == "" || req.InvocationEndpoint == "" || req.HttpMethod == "" {
		sim.AWSError(w, "ValidationException", "Name, ConnectionArn, InvocationEndpoint, and HttpMethod are required", http.StatusBadRequest)
		return
	}
	if _, ok := ebApiDest.Get(req.Name); ok {
		sim.AWSError(w, "ResourceAlreadyExistsException", "An api-destination with the name "+req.Name+" already exists.", http.StatusConflict)
		return
	}
	if !ebConnectionExistsByARN(req.ConnectionArn) {
		sim.AWSError(w, "ResourceNotFoundException", "Connection does not exist.", http.StatusNotFound)
		return
	}
	now := time.Now().Unix()
	dest := EBApiDestination{
		Name:                req.Name,
		Arn:                 ebApiDestArn(req.Name),
		ConnectionArn:       req.ConnectionArn,
		InvocationEndpoint:  req.InvocationEndpoint,
		HttpMethod:          req.HttpMethod,
		Description:         req.Description,
		InvocationRateLimit: req.InvocationRateLimitPerSecond,
		State:               "ACTIVE",
		CreationTime:        now,
		LastModifiedTime:    now,
	}
	ebApiDest.Put(dest.Name, dest)
	writeEBJSON(w, http.StatusOK, map[string]any{
		"ApiDestinationArn":   dest.Arn,
		"ApiDestinationState": dest.State,
		"CreationTime":        dest.CreationTime,
		"LastModifiedTime":    dest.LastModifiedTime,
	})
}

func handleEBDescribeApiDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	dest, ok := ebApiDest.Get(req.Name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "An api-destination "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	out := map[string]any{
		"ApiDestinationArn":   dest.Arn,
		"ApiDestinationState": dest.State,
		"ConnectionArn":       dest.ConnectionArn,
		"InvocationEndpoint":  dest.InvocationEndpoint,
		"HttpMethod":          dest.HttpMethod,
		"Name":                dest.Name,
		"CreationTime":        dest.CreationTime,
		"LastModifiedTime":    dest.LastModifiedTime,
	}
	if dest.Description != "" {
		out["Description"] = dest.Description
	}
	if dest.InvocationRateLimit != nil {
		out["InvocationRateLimitPerSecond"] = *dest.InvocationRateLimit
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBListApiDestinations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamePrefix    string `json:"NamePrefix"`
		ConnectionArn string `json:"ConnectionArn"`
		Limit         int    `json:"Limit"`
		NextToken     string `json:"NextToken"`
	}
	_ = sim.ReadJSON(r, &req)
	dests := make([]EBApiDestination, 0)
	for _, dest := range ebApiDest.List() {
		if req.NamePrefix != "" && !strings.HasPrefix(dest.Name, req.NamePrefix) {
			continue
		}
		if req.ConnectionArn != "" && dest.ConnectionArn != req.ConnectionArn {
			continue
		}
		dests = append(dests, dest)
	}
	sort.Slice(dests, func(i, j int) bool { return dests[i].Name < dests[j].Name })
	page, next := awsPageExplicit(dests, req.NextToken, req.Limit)
	entries := make([]map[string]any, 0, len(page))
	for _, dest := range page {
		entry := map[string]any{
			"ApiDestinationArn":   dest.Arn,
			"ApiDestinationState": dest.State,
			"ConnectionArn":       dest.ConnectionArn,
			"InvocationEndpoint":  dest.InvocationEndpoint,
			"HttpMethod":          dest.HttpMethod,
			"Name":                dest.Name,
			"CreationTime":        dest.CreationTime,
			"LastModifiedTime":    dest.LastModifiedTime,
		}
		if dest.InvocationRateLimit != nil {
			entry["InvocationRateLimitPerSecond"] = *dest.InvocationRateLimit
		}
		entries = append(entries, entry)
	}
	out := map[string]any{"ApiDestinations": entries}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBUpdateApiDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                         string  `json:"Name"`
		ConnectionArn                *string `json:"ConnectionArn"`
		InvocationEndpoint           *string `json:"InvocationEndpoint"`
		HttpMethod                   *string `json:"HttpMethod"`
		Description                  *string `json:"Description"`
		InvocationRateLimitPerSecond *int32  `json:"InvocationRateLimitPerSecond"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	dest, ok := ebApiDest.Get(req.Name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "An api-destination "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	if req.ConnectionArn != nil {
		if !ebConnectionExistsByARN(*req.ConnectionArn) {
			sim.AWSError(w, "ResourceNotFoundException", "Connection does not exist.", http.StatusNotFound)
			return
		}
		dest.ConnectionArn = *req.ConnectionArn
	}
	if req.InvocationEndpoint != nil {
		dest.InvocationEndpoint = *req.InvocationEndpoint
	}
	if req.HttpMethod != nil {
		dest.HttpMethod = *req.HttpMethod
	}
	if req.Description != nil {
		dest.Description = *req.Description
	}
	if req.InvocationRateLimitPerSecond != nil {
		dest.InvocationRateLimit = req.InvocationRateLimitPerSecond
	}
	dest.LastModifiedTime = time.Now().Unix()
	ebApiDest.Put(dest.Name, dest)
	writeEBJSON(w, http.StatusOK, map[string]any{
		"ApiDestinationArn":   dest.Arn,
		"ApiDestinationState": dest.State,
		"CreationTime":        dest.CreationTime,
		"LastModifiedTime":    dest.LastModifiedTime,
	})
}

func handleEBDeleteApiDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ebApiDest.Delete(req.Name) {
		sim.AWSError(w, "ResourceNotFoundException", "An api-destination "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func ebConnectionExistsByARN(arn string) bool {
	for _, c := range ebConnections.List() {
		if c.Arn == arn {
			return true
		}
	}
	return false
}

func handleEBCreateConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string          `json:"Name"`
		AuthorizationType string          `json:"AuthorizationType"`
		Description       string          `json:"Description"`
		KmsKeyIdentifier  string          `json:"KmsKeyIdentifier"`
		AuthParameters    json.RawMessage `json:"AuthParameters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.AuthorizationType == "" || len(req.AuthParameters) == 0 {
		sim.AWSError(w, "ValidationException", "Name, AuthorizationType, and AuthParameters are required", http.StatusBadRequest)
		return
	}
	if _, ok := ebConnections.Get(req.Name); ok {
		sim.AWSError(w, "ResourceAlreadyExistsException", "Connection "+req.Name+" already exists.", http.StatusConflict)
		return
	}
	now := time.Now().Unix()
	conn := EBConnection{
		Name:              req.Name,
		Arn:               ebConnectionArn(req.Name),
		AuthorizationType: req.AuthorizationType,
		State:             "AUTHORIZED",
		Description:       req.Description,
		KmsKeyIdentifier:  req.KmsKeyIdentifier,
		SecretArn:         ebConnectionSecretArn(req.Name),
		AuthParameters:    append(json.RawMessage(nil), req.AuthParameters...),
		CreationTime:      now,
		LastModifiedTime:  now,
		LastAuthorized:    now,
	}
	ebConnections.Put(conn.Name, conn)
	writeEBJSON(w, http.StatusOK, map[string]any{
		"ConnectionArn":    conn.Arn,
		"ConnectionState":  conn.State,
		"CreationTime":     conn.CreationTime,
		"LastModifiedTime": conn.LastModifiedTime,
	})
}

// ebConnectionAuthResponse projects the stored AuthParameters onto the
// DescribeConnection response shape: only the non-secret descriptors are echoed
// (the API key header name, the basic username, the OAuth client id + endpoint),
// never the secret value/password/client-secret, matching real EventBridge.
func ebConnectionAuthResponse(authType string, raw json.RawMessage) map[string]any {
	var params struct {
		ApiKeyAuthParameters *struct {
			ApiKeyName string `json:"ApiKeyName"`
		} `json:"ApiKeyAuthParameters"`
		BasicAuthParameters *struct {
			Username string `json:"Username"`
		} `json:"BasicAuthParameters"`
		OAuthParameters *struct {
			AuthorizationEndpoint string `json:"AuthorizationEndpoint"`
			HttpMethod            string `json:"HttpMethod"`
			ClientParameters      *struct {
				ClientID string `json:"ClientID"`
			} `json:"ClientParameters"`
		} `json:"OAuthParameters"`
		InvocationHttpParameters json.RawMessage `json:"InvocationHttpParameters"`
	}
	_ = json.Unmarshal(raw, &params)
	out := map[string]any{}
	if params.ApiKeyAuthParameters != nil {
		out["ApiKeyAuthParameters"] = map[string]any{"ApiKeyName": params.ApiKeyAuthParameters.ApiKeyName}
	}
	if params.BasicAuthParameters != nil {
		out["BasicAuthParameters"] = map[string]any{"Username": params.BasicAuthParameters.Username}
	}
	if params.OAuthParameters != nil {
		oauth := map[string]any{
			"AuthorizationEndpoint": params.OAuthParameters.AuthorizationEndpoint,
			"HttpMethod":            params.OAuthParameters.HttpMethod,
		}
		if params.OAuthParameters.ClientParameters != nil {
			oauth["ClientParameters"] = map[string]any{"ClientID": params.OAuthParameters.ClientParameters.ClientID}
		}
		out["OAuthParameters"] = oauth
	}
	if len(params.InvocationHttpParameters) > 0 {
		out["InvocationHttpParameters"] = params.InvocationHttpParameters
	}
	return out
}

func handleEBDescribeConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	conn, ok := ebConnections.Get(req.Name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Connection "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	out := map[string]any{
		"ConnectionArn":     conn.Arn,
		"ConnectionState":   conn.State,
		"AuthorizationType": conn.AuthorizationType,
		"AuthParameters":    ebConnectionAuthResponse(conn.AuthorizationType, conn.AuthParameters),
		"Name":              conn.Name,
		"SecretArn":         conn.SecretArn,
		"CreationTime":      conn.CreationTime,
		"LastModifiedTime":  conn.LastModifiedTime,
	}
	if conn.Description != "" {
		out["Description"] = conn.Description
	}
	if conn.KmsKeyIdentifier != "" {
		out["KmsKeyIdentifier"] = conn.KmsKeyIdentifier
	}
	if conn.StateReason != "" {
		out["StateReason"] = conn.StateReason
	}
	if conn.LastAuthorized != 0 {
		out["LastAuthorizedTime"] = conn.LastAuthorized
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBListConnections(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamePrefix      string `json:"NamePrefix"`
		ConnectionState string `json:"ConnectionState"`
		Limit           int    `json:"Limit"`
		NextToken       string `json:"NextToken"`
	}
	_ = sim.ReadJSON(r, &req)
	conns := make([]EBConnection, 0)
	for _, conn := range ebConnections.List() {
		if req.NamePrefix != "" && !strings.HasPrefix(conn.Name, req.NamePrefix) {
			continue
		}
		if req.ConnectionState != "" && conn.State != req.ConnectionState {
			continue
		}
		conns = append(conns, conn)
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].Name < conns[j].Name })
	page, next := awsPageExplicit(conns, req.NextToken, req.Limit)
	entries := make([]map[string]any, 0, len(page))
	for _, conn := range page {
		entry := map[string]any{
			"ConnectionArn":     conn.Arn,
			"ConnectionState":   conn.State,
			"AuthorizationType": conn.AuthorizationType,
			"Name":              conn.Name,
			"CreationTime":      conn.CreationTime,
			"LastModifiedTime":  conn.LastModifiedTime,
		}
		if conn.StateReason != "" {
			entry["StateReason"] = conn.StateReason
		}
		if conn.LastAuthorized != 0 {
			entry["LastAuthorizedTime"] = conn.LastAuthorized
		}
		entries = append(entries, entry)
	}
	out := map[string]any{"Connections": entries}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBUpdateConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string          `json:"Name"`
		AuthorizationType *string         `json:"AuthorizationType"`
		Description       *string         `json:"Description"`
		KmsKeyIdentifier  *string         `json:"KmsKeyIdentifier"`
		AuthParameters    json.RawMessage `json:"AuthParameters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	conn, ok := ebConnections.Get(req.Name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Connection "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	if req.AuthorizationType != nil {
		conn.AuthorizationType = *req.AuthorizationType
	}
	if req.Description != nil {
		conn.Description = *req.Description
	}
	if req.KmsKeyIdentifier != nil {
		conn.KmsKeyIdentifier = *req.KmsKeyIdentifier
	}
	if len(req.AuthParameters) > 0 {
		conn.AuthParameters = append(json.RawMessage(nil), req.AuthParameters...)
	}
	now := time.Now().Unix()
	conn.LastModifiedTime = now
	conn.LastAuthorized = now
	conn.State = "AUTHORIZED"
	conn.StateReason = ""
	ebConnections.Put(conn.Name, conn)
	writeEBJSON(w, http.StatusOK, map[string]any{
		"ConnectionArn":      conn.Arn,
		"ConnectionState":    conn.State,
		"CreationTime":       conn.CreationTime,
		"LastModifiedTime":   conn.LastModifiedTime,
		"LastAuthorizedTime": conn.LastAuthorized,
	})
}

func handleEBDeauthorizeConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	conn, ok := ebConnections.Get(req.Name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Connection "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	conn.State = "DEAUTHORIZED"
	conn.StateReason = "Connection was deauthorized."
	conn.LastModifiedTime = time.Now().Unix()
	ebConnections.Put(conn.Name, conn)
	writeEBJSON(w, http.StatusOK, map[string]any{
		"ConnectionArn":      conn.Arn,
		"ConnectionState":    conn.State,
		"CreationTime":       conn.CreationTime,
		"LastModifiedTime":   conn.LastModifiedTime,
		"LastAuthorizedTime": conn.LastAuthorized,
	})
}

func handleEBDeleteConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	conn, ok := ebConnections.Get(req.Name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Connection "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	ebConnections.Delete(req.Name)
	writeEBJSON(w, http.StatusOK, map[string]any{
		"ConnectionArn":      conn.Arn,
		"ConnectionState":    conn.State,
		"CreationTime":       conn.CreationTime,
		"LastModifiedTime":   conn.LastModifiedTime,
		"LastAuthorizedTime": conn.LastAuthorized,
	})
}

func handleEBCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string          `json:"Name"`
		Description       string          `json:"Description"`
		RoleArn           string          `json:"RoleArn"`
		EventBuses        json.RawMessage `json:"EventBuses"`
		RoutingConfig     json.RawMessage `json:"RoutingConfig"`
		ReplicationConfig json.RawMessage `json:"ReplicationConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || len(req.EventBuses) == 0 || len(req.RoutingConfig) == 0 {
		sim.AWSError(w, "ValidationException", "Name, EventBuses, and RoutingConfig are required", http.StatusBadRequest)
		return
	}
	if _, ok := ebEndpoints.Get(req.Name); ok {
		sim.AWSError(w, "ResourceAlreadyExistsException", "Endpoint "+req.Name+" already exists.", http.StatusConflict)
		return
	}
	now := time.Now().Unix()
	endpointID := strings.ToLower(req.Name) + ".veo"
	endpoint := EBEndpoint{
		Name:              req.Name,
		Arn:               ebEndpointArn(req.Name),
		EndpointID:        endpointID,
		EndpointURL:       "https://" + endpointID + ".endpoint.events.amazonaws.com",
		Description:       req.Description,
		RoleArn:           req.RoleArn,
		State:             "ACTIVE",
		EventBuses:        append(json.RawMessage(nil), req.EventBuses...),
		RoutingConfig:     append(json.RawMessage(nil), req.RoutingConfig...),
		ReplicationConfig: append(json.RawMessage(nil), req.ReplicationConfig...),
		CreationTime:      now,
		LastModifiedTime:  now,
	}
	ebEndpoints.Put(endpoint.Name, endpoint)
	out := map[string]any{
		"Arn":           endpoint.Arn,
		"Name":          endpoint.Name,
		"State":         endpoint.State,
		"EventBuses":    endpoint.EventBuses,
		"RoutingConfig": endpoint.RoutingConfig,
	}
	if endpoint.RoleArn != "" {
		out["RoleArn"] = endpoint.RoleArn
	}
	if len(endpoint.ReplicationConfig) > 0 {
		out["ReplicationConfig"] = endpoint.ReplicationConfig
	}
	writeEBJSON(w, http.StatusOK, out)
}

func ebEndpointDescribeShape(e EBEndpoint) map[string]any {
	out := map[string]any{
		"Arn":              e.Arn,
		"Name":             e.Name,
		"EndpointId":       e.EndpointID,
		"EndpointUrl":      e.EndpointURL,
		"State":            e.State,
		"EventBuses":       e.EventBuses,
		"RoutingConfig":    e.RoutingConfig,
		"CreationTime":     e.CreationTime,
		"LastModifiedTime": e.LastModifiedTime,
	}
	if e.Description != "" {
		out["Description"] = e.Description
	}
	if e.RoleArn != "" {
		out["RoleArn"] = e.RoleArn
	}
	if e.StateReason != "" {
		out["StateReason"] = e.StateReason
	}
	if len(e.ReplicationConfig) > 0 {
		out["ReplicationConfig"] = e.ReplicationConfig
	}
	return out
}

func handleEBDescribeEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"Name"`
		HomeRegion string `json:"HomeRegion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	endpoint, ok := ebEndpoints.Get(req.Name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Endpoint "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, ebEndpointDescribeShape(endpoint))
}

func handleEBListEndpoints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamePrefix string `json:"NamePrefix"`
		HomeRegion string `json:"HomeRegion"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	_ = sim.ReadJSON(r, &req)
	endpoints := make([]EBEndpoint, 0)
	for _, endpoint := range ebEndpoints.List() {
		if req.NamePrefix != "" && !strings.HasPrefix(endpoint.Name, req.NamePrefix) {
			continue
		}
		endpoints = append(endpoints, endpoint)
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Name < endpoints[j].Name })
	page, next := awsPageExplicit(endpoints, req.NextToken, req.MaxResults)
	entries := make([]map[string]any, 0, len(page))
	for _, endpoint := range page {
		entries = append(entries, ebEndpointDescribeShape(endpoint))
	}
	out := map[string]any{"Endpoints": entries}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBUpdateEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string          `json:"Name"`
		Description       *string         `json:"Description"`
		RoleArn           *string         `json:"RoleArn"`
		EventBuses        json.RawMessage `json:"EventBuses"`
		RoutingConfig     json.RawMessage `json:"RoutingConfig"`
		ReplicationConfig json.RawMessage `json:"ReplicationConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	endpoint, ok := ebEndpoints.Get(req.Name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Endpoint "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	if req.Description != nil {
		endpoint.Description = *req.Description
	}
	if req.RoleArn != nil {
		endpoint.RoleArn = *req.RoleArn
	}
	if len(req.EventBuses) > 0 {
		endpoint.EventBuses = append(json.RawMessage(nil), req.EventBuses...)
	}
	if len(req.RoutingConfig) > 0 {
		endpoint.RoutingConfig = append(json.RawMessage(nil), req.RoutingConfig...)
	}
	if len(req.ReplicationConfig) > 0 {
		endpoint.ReplicationConfig = append(json.RawMessage(nil), req.ReplicationConfig...)
	}
	endpoint.LastModifiedTime = time.Now().Unix()
	ebEndpoints.Put(endpoint.Name, endpoint)
	out := map[string]any{
		"Arn":           endpoint.Arn,
		"Name":          endpoint.Name,
		"EndpointId":    endpoint.EndpointID,
		"EndpointUrl":   endpoint.EndpointURL,
		"State":         endpoint.State,
		"EventBuses":    endpoint.EventBuses,
		"RoutingConfig": endpoint.RoutingConfig,
	}
	if endpoint.RoleArn != "" {
		out["RoleArn"] = endpoint.RoleArn
	}
	if len(endpoint.ReplicationConfig) > 0 {
		out["ReplicationConfig"] = endpoint.ReplicationConfig
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ebEndpoints.Delete(req.Name) {
		sim.AWSError(w, "ResourceNotFoundException", "Endpoint "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

// ebPartnerExpiry is the offered-but-unclaimed lifetime of a partner event
// source — real EventBridge expires an unmatched offer after a fixed window.
const ebPartnerExpiry = 14 * 24 * 60 * 60

func handleEBCreatePartnerEventSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"Name"`
		Account string `json:"Account"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Account == "" {
		sim.AWSError(w, "ValidationException", "Name and Account are required", http.StatusBadRequest)
		return
	}
	// Partner event source names must be partner_name/event_namespace/event_name.
	if strings.Count(req.Name, "/") < 2 {
		sim.AWSError(w, "ValidationException", "Event source name must be in the format partner_name/event_namespace/event_name.", http.StatusBadRequest)
		return
	}
	key := req.Account + "|" + req.Name
	if _, ok := ebPartnerSources.Get(key); ok {
		sim.AWSError(w, "ResourceAlreadyExistsException", "Event source "+req.Name+" already exists.", http.StatusConflict)
		return
	}
	now := time.Now().Unix()
	partnerName := req.Name
	if i := strings.Index(req.Name, "/"); i >= 0 {
		partnerName = req.Name[:i]
	}
	source := EBPartnerEventSource{
		Name:         req.Name,
		Arn:          ebPartnerSourceArn(req.Name),
		Account:      req.Account,
		CreatedBy:    partnerName,
		State:        "PENDING",
		CreationTime: now,
		Expiration:   now + ebPartnerExpiry,
	}
	ebPartnerSources.Put(key, source)
	writeEBJSON(w, http.StatusOK, map[string]any{"EventSourceArn": source.Arn})
}

// ebFindPartnerSource locates a partner event source by name. A partner-side op
// (DescribePartnerEventSource) keys on name only; a consumer-side op keys the
// offer made to this account.
func ebFindPartnerSource(name string) (EBPartnerEventSource, bool) {
	for _, s := range ebPartnerSources.List() {
		if s.Name == name {
			return s, true
		}
	}
	return EBPartnerEventSource{}, false
}

func ebFindPartnerSourceForAccount(name, account string) (EBPartnerEventSource, bool) {
	for _, s := range ebPartnerSources.List() {
		if s.Name == name && (account == "" || s.Account == account) {
			return s, true
		}
	}
	return EBPartnerEventSource{}, false
}

func handleEBDescribePartnerEventSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	source, ok := ebFindPartnerSource(req.Name)
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Event source "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, map[string]any{
		"Arn":  source.Arn,
		"Name": source.Name,
	})
}

func handleEBListPartnerEventSources(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamePrefix string `json:"NamePrefix"`
		Limit      int    `json:"Limit"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.NamePrefix == "" {
		sim.AWSError(w, "ValidationException", "NamePrefix is required", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	sources := make([]EBPartnerEventSource, 0)
	for _, s := range ebPartnerSources.List() {
		if !strings.HasPrefix(s.Name, req.NamePrefix) || seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		sources = append(sources, s)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	page, next := awsPageExplicit(sources, req.NextToken, req.Limit)
	entries := make([]map[string]any, 0, len(page))
	for _, s := range page {
		entries = append(entries, map[string]any{"Arn": s.Arn, "Name": s.Name})
	}
	out := map[string]any{"PartnerEventSources": entries}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBListPartnerEventSourceAccounts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventSourceName string `json:"EventSourceName"`
		Limit           int    `json:"Limit"`
		NextToken       string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.EventSourceName == "" {
		sim.AWSError(w, "ValidationException", "EventSourceName is required", http.StatusBadRequest)
		return
	}
	found := false
	accounts := make([]EBPartnerEventSource, 0)
	for _, s := range ebPartnerSources.List() {
		if s.Name == req.EventSourceName {
			found = true
			accounts = append(accounts, s)
		}
	}
	if !found {
		sim.AWSError(w, "ResourceNotFoundException", "Event source "+req.EventSourceName+" does not exist.", http.StatusNotFound)
		return
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Account < accounts[j].Account })
	page, next := awsPageExplicit(accounts, req.NextToken, req.Limit)
	entries := make([]map[string]any, 0, len(page))
	for _, s := range page {
		entry := map[string]any{
			"Account":      s.Account,
			"State":        s.State,
			"CreationTime": s.CreationTime,
		}
		if s.Expiration != 0 {
			entry["ExpirationTime"] = s.Expiration
		}
		entries = append(entries, entry)
	}
	out := map[string]any{"PartnerEventSourceAccounts": entries}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBDeletePartnerEventSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"Name"`
		Account string `json:"Account"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Account == "" {
		sim.AWSError(w, "ValidationException", "Name and Account are required", http.StatusBadRequest)
		return
	}
	key := req.Account + "|" + req.Name
	if _, ok := ebPartnerSources.Get(key); !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Event source "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	ebPartnerSources.Delete(key)
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func handleEBPutPartnerEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Entries []struct {
			Source     string   `json:"Source"`
			DetailType string   `json:"DetailType"`
			Detail     string   `json:"Detail"`
			Resources  []string `json:"Resources"`
			Time       float64  `json:"Time"`
		} `json:"Entries"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	entries := make([]map[string]string, 0, len(req.Entries))
	failed := 0
	for _, entry := range req.Entries {
		// Detail, DetailType, and Source are all required for a partner event;
		// a missing one fails that entry (the rest of the batch still succeeds).
		if entry.Source == "" || entry.DetailType == "" || entry.Detail == "" {
			failed++
			entries = append(entries, map[string]string{
				"ErrorCode":    "InvalidArgument",
				"ErrorMessage": "Detail, DetailType, and Source are required.",
			})
			continue
		}
		var detailObj any
		if err := json.Unmarshal([]byte(entry.Detail), &detailObj); err != nil {
			failed++
			entries = append(entries, map[string]string{
				"ErrorCode":    "MalformedDetail",
				"ErrorMessage": "Detail is malformed.",
			})
			continue
		}
		entries = append(entries, map[string]string{"EventId": generateUUID()})
	}
	writeEBJSON(w, http.StatusOK, map[string]any{"FailedEntryCount": failed, "Entries": entries})
}

// handleEBActivateEventSource transitions a consumer-side event source from
// PENDING to ACTIVE — the account has accepted the partner's offer (in real
// EventBridge by creating a matching partner event bus). DeactivateEventSource
// is the inverse.
func handleEBActivateEventSource(w http.ResponseWriter, r *http.Request) {
	ebSetEventSourceState(w, r, "ACTIVE")
}

func handleEBDeactivateEventSource(w http.ResponseWriter, r *http.Request) {
	ebSetEventSourceState(w, r, "PENDING")
}

func ebSetEventSourceState(w http.ResponseWriter, r *http.Request, state string) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	updated := false
	for _, s := range ebPartnerSources.List() {
		if s.Name != req.Name {
			continue
		}
		key := s.Account + "|" + s.Name
		if ebPartnerSources.Update(key, func(p *EBPartnerEventSource) { p.State = state }) {
			updated = true
		}
	}
	if !updated {
		sim.AWSError(w, "ResourceNotFoundException", "Event source "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func handleEBDescribeEventSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	source, ok := ebFindPartnerSourceForAccount(req.Name, "")
	if !ok {
		sim.AWSError(w, "ResourceNotFoundException", "Event source "+req.Name+" does not exist.", http.StatusNotFound)
		return
	}
	out := map[string]any{
		"Arn":          source.Arn,
		"Name":         source.Name,
		"CreatedBy":    source.CreatedBy,
		"State":        source.State,
		"CreationTime": source.CreationTime,
	}
	if source.Expiration != 0 {
		out["ExpirationTime"] = source.Expiration
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBListEventSources(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamePrefix string `json:"NamePrefix"`
		Limit      int    `json:"Limit"`
		NextToken  string `json:"NextToken"`
	}
	_ = sim.ReadJSON(r, &req)
	seen := map[string]bool{}
	sources := make([]EBPartnerEventSource, 0)
	for _, s := range ebPartnerSources.List() {
		if req.NamePrefix != "" && !strings.HasPrefix(s.Name, req.NamePrefix) {
			continue
		}
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		sources = append(sources, s)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	page, next := awsPageExplicit(sources, req.NextToken, req.Limit)
	entries := make([]map[string]any, 0, len(page))
	for _, s := range page {
		entry := map[string]any{
			"Arn":          s.Arn,
			"Name":         s.Name,
			"CreatedBy":    s.CreatedBy,
			"State":        s.State,
			"CreationTime": s.CreationTime,
		}
		if s.Expiration != 0 {
			entry["ExpirationTime"] = s.Expiration
		}
		entries = append(entries, entry)
	}
	out := map[string]any{"EventSources": entries}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/google/uuid"
)

// CloudWatch Logs control-plane operations for account policies, query
// definitions, resource policies, cross-account destinations, vended-log
// deliveries (deliveries + delivery sources + delivery destinations + their
// policies), log anomaly detectors, field-index policies, and the published
// delivery configuration templates. Each operates on a real sim.Store at
// CloudWatch Logs (awsJson1.1) API fidelity. There are no fakes: Put/Create
// returns the id/arn, Get/Describe read it back, Delete removes it.

// CWAccountPolicy mirrors the SDK AccountPolicy shape. Keyed by
// policyType+":"+policyName, since a name is unique per policy type.
type CWAccountPolicy struct {
	PolicyName        string `json:"policyName"`
	PolicyDocument    string `json:"policyDocument"`
	LastUpdatedTime   int64  `json:"lastUpdatedTime"`
	PolicyType        string `json:"policyType"`
	Scope             string `json:"scope,omitempty"`
	SelectionCriteria string `json:"selectionCriteria,omitempty"`
	AccountId         string `json:"accountId,omitempty"`
}

// CWQueryDefinition mirrors the SDK QueryDefinition shape. Keyed by id.
type CWQueryDefinition struct {
	QueryLanguage     string   `json:"queryLanguage,omitempty"`
	QueryDefinitionId string   `json:"queryDefinitionId"`
	Name              string   `json:"name"`
	QueryString       string   `json:"queryString"`
	LastModified      int64    `json:"lastModified"`
	LogGroupNames     []string `json:"logGroupNames,omitempty"`
}

// CWResourcePolicy mirrors the SDK ResourcePolicy shape. Keyed by policyName.
type CWResourcePolicy struct {
	PolicyName      string `json:"policyName"`
	PolicyDocument  string `json:"policyDocument"`
	LastUpdatedTime int64  `json:"lastUpdatedTime"`
}

// CWDestination mirrors the SDK Destination shape. Keyed by destinationName.
type CWDestination struct {
	DestinationName string            `json:"destinationName"`
	TargetArn       string            `json:"targetArn,omitempty"`
	RoleArn         string            `json:"roleArn,omitempty"`
	AccessPolicy    string            `json:"accessPolicy,omitempty"`
	Arn             string            `json:"arn"`
	CreationTime    int64             `json:"creationTime"`
	Tags            map[string]string `json:"-"`
}

// CWDelivery mirrors the SDK Delivery shape. Keyed by id.
type CWDelivery struct {
	Id                      string            `json:"id"`
	Arn                     string            `json:"arn"`
	DeliverySourceName      string            `json:"deliverySourceName,omitempty"`
	DeliveryDestinationArn  string            `json:"deliveryDestinationArn,omitempty"`
	DeliveryDestinationType string            `json:"deliveryDestinationType,omitempty"`
	RecordFields            []string          `json:"recordFields,omitempty"`
	FieldDelimiter          string            `json:"fieldDelimiter,omitempty"`
	Tags                    map[string]string `json:"tags,omitempty"`
}

// CWDeliverySource mirrors the SDK DeliverySource shape. Keyed by name.
type CWDeliverySource struct {
	Name         string            `json:"name"`
	Arn          string            `json:"arn"`
	ResourceArns []string          `json:"resourceArns,omitempty"`
	Service      string            `json:"service,omitempty"`
	LogType      string            `json:"logType,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// CWDeliveryDestination mirrors the SDK DeliveryDestination shape. Keyed by name.
type CWDeliveryDestination struct {
	Name                             string                              `json:"name"`
	Arn                              string                              `json:"arn"`
	DeliveryDestinationType          string                              `json:"deliveryDestinationType,omitempty"`
	OutputFormat                     string                              `json:"outputFormat,omitempty"`
	DeliveryDestinationConfiguration *CWDeliveryDestinationConfiguration `json:"deliveryDestinationConfiguration,omitempty"`
	Tags                             map[string]string                   `json:"tags,omitempty"`
	// Policy holds the IAM policy attached via PutDeliveryDestinationPolicy.
	Policy string `json:"-"`
}

// CWDeliveryDestinationConfiguration mirrors the SDK shape.
type CWDeliveryDestinationConfiguration struct {
	DestinationResourceArn string `json:"destinationResourceArn"`
}

// CWLogAnomalyDetector mirrors the SDK fields exposed by GetLogAnomalyDetector and
// ListLogAnomalyDetectors. Keyed by anomalyDetectorArn.
type CWLogAnomalyDetector struct {
	AnomalyDetectorArn    string   `json:"anomalyDetectorArn"`
	DetectorName          string   `json:"detectorName,omitempty"`
	LogGroupArnList       []string `json:"logGroupArnList,omitempty"`
	EvaluationFrequency   string   `json:"evaluationFrequency,omitempty"`
	FilterPattern         string   `json:"filterPattern,omitempty"`
	AnomalyDetectorStatus string   `json:"anomalyDetectorStatus,omitempty"`
	KmsKeyId              string   `json:"kmsKeyId,omitempty"`
	CreationTimeStamp     int64    `json:"creationTimeStamp"`
	LastModifiedTimeStamp int64    `json:"lastModifiedTimeStamp"`
	AnomalyVisibilityTime int64    `json:"anomalyVisibilityTime,omitempty"`
}

// CWIndexPolicy mirrors the SDK IndexPolicy shape. Keyed by logGroupIdentifier.
type CWIndexPolicy struct {
	LogGroupIdentifier string `json:"logGroupIdentifier"`
	LastUpdateTime     int64  `json:"lastUpdateTime"`
	PolicyDocument     string `json:"policyDocument"`
	Source             string `json:"source,omitempty"`
}

var (
	cwAccountPolicies     sim.Store[CWAccountPolicy]
	cwQueryDefinitions    sim.Store[CWQueryDefinition]
	cwResourcePolicies    sim.Store[CWResourcePolicy]
	cwDestinations        sim.Store[CWDestination]
	cwDeliveries          sim.Store[CWDelivery]
	cwDeliverySources     sim.Store[CWDeliverySource]
	cwDeliveryDests       sim.Store[CWDeliveryDestination]
	cwLogAnomalyDetectors sim.Store[CWLogAnomalyDetector]
	cwIndexPolicies       sim.Store[CWIndexPolicy]
)

func registerCloudWatchLogsExtra2(r *sim.AWSRouter, srv *sim.Server) {
	cwAccountPolicies = sim.MakeStore[CWAccountPolicy](srv.DB(), "cw_account_policies")
	cwQueryDefinitions = sim.MakeStore[CWQueryDefinition](srv.DB(), "cw_query_definitions")
	cwResourcePolicies = sim.MakeStore[CWResourcePolicy](srv.DB(), "cw_resource_policies")
	cwDestinations = sim.MakeStore[CWDestination](srv.DB(), "cw_destinations")
	cwDeliveries = sim.MakeStore[CWDelivery](srv.DB(), "cw_deliveries")
	cwDeliverySources = sim.MakeStore[CWDeliverySource](srv.DB(), "cw_delivery_sources")
	cwDeliveryDests = sim.MakeStore[CWDeliveryDestination](srv.DB(), "cw_delivery_destinations")
	cwLogAnomalyDetectors = sim.MakeStore[CWLogAnomalyDetector](srv.DB(), "cw_log_anomaly_detectors")
	cwIndexPolicies = sim.MakeStore[CWIndexPolicy](srv.DB(), "cw_index_policies")

	// Account policies.
	r.Register("Logs_20140328.PutAccountPolicy", handleCWPutAccountPolicy)
	r.Register("Logs_20140328.DescribeAccountPolicies", handleCWDescribeAccountPolicies)
	r.Register("Logs_20140328.DeleteAccountPolicy", handleCWDeleteAccountPolicy)

	// Query definitions.
	r.Register("Logs_20140328.PutQueryDefinition", handleCWPutQueryDefinition)
	r.Register("Logs_20140328.DescribeQueryDefinitions", handleCWDescribeQueryDefinitions)
	r.Register("Logs_20140328.DeleteQueryDefinition", handleCWDeleteQueryDefinition)

	// Resource policies.
	r.Register("Logs_20140328.PutResourcePolicy", handleCWPutResourcePolicy)
	r.Register("Logs_20140328.DescribeResourcePolicies", handleCWDescribeResourcePolicies)
	r.Register("Logs_20140328.DeleteResourcePolicy", handleCWDeleteResourcePolicy)

	// Cross-account destinations.
	r.Register("Logs_20140328.PutDestination", handleCWPutDestination)
	r.Register("Logs_20140328.DescribeDestinations", handleCWDescribeDestinations)
	r.Register("Logs_20140328.DeleteDestination", handleCWDeleteDestination)
	r.Register("Logs_20140328.PutDestinationPolicy", handleCWPutDestinationPolicy)

	// Vended-log deliveries.
	r.Register("Logs_20140328.CreateDelivery", handleCWCreateDelivery)
	r.Register("Logs_20140328.GetDelivery", handleCWGetDelivery)
	r.Register("Logs_20140328.DeleteDelivery", handleCWDeleteDelivery)
	r.Register("Logs_20140328.DescribeDeliveries", handleCWDescribeDeliveries)

	// Delivery sources.
	r.Register("Logs_20140328.PutDeliverySource", handleCWPutDeliverySource)
	r.Register("Logs_20140328.GetDeliverySource", handleCWGetDeliverySource)
	r.Register("Logs_20140328.DescribeDeliverySources", handleCWDescribeDeliverySources)
	r.Register("Logs_20140328.DeleteDeliverySource", handleCWDeleteDeliverySource)

	// Delivery destinations.
	r.Register("Logs_20140328.PutDeliveryDestination", handleCWPutDeliveryDestination)
	r.Register("Logs_20140328.GetDeliveryDestination", handleCWGetDeliveryDestination)
	r.Register("Logs_20140328.DescribeDeliveryDestinations", handleCWDescribeDeliveryDestinations)
	r.Register("Logs_20140328.DeleteDeliveryDestination", handleCWDeleteDeliveryDestination)
	r.Register("Logs_20140328.PutDeliveryDestinationPolicy", handleCWPutDeliveryDestinationPolicy)
	r.Register("Logs_20140328.GetDeliveryDestinationPolicy", handleCWGetDeliveryDestinationPolicy)
	r.Register("Logs_20140328.DeleteDeliveryDestinationPolicy", handleCWDeleteDeliveryDestinationPolicy)

	// Log anomaly detectors.
	r.Register("Logs_20140328.CreateLogAnomalyDetector", handleCWCreateLogAnomalyDetector)
	r.Register("Logs_20140328.GetLogAnomalyDetector", handleCWGetLogAnomalyDetector)
	r.Register("Logs_20140328.ListLogAnomalyDetectors", handleCWListLogAnomalyDetectors)
	r.Register("Logs_20140328.DeleteLogAnomalyDetector", handleCWDeleteLogAnomalyDetector)

	// Field-index policies.
	r.Register("Logs_20140328.PutIndexPolicy", handleCWPutIndexPolicy)
	r.Register("Logs_20140328.DeleteIndexPolicy", handleCWDeleteIndexPolicy)
	r.Register("Logs_20140328.DescribeIndexPolicies", handleCWDescribeIndexPolicies)
	r.Register("Logs_20140328.DescribeFieldIndexes", handleCWDescribeFieldIndexes)

	// Delivery configuration templates (published static list).
	r.Register("Logs_20140328.DescribeConfigurationTemplates", handleCWDescribeConfigurationTemplates)
}

func cwAccountPolicyKey(policyType, name string) string { return policyType + ":" + name }

func handleCWPutAccountPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyName        string `json:"policyName"`
		PolicyDocument    string `json:"policyDocument"`
		PolicyType        string `json:"policyType"`
		Scope             string `json:"scope"`
		SelectionCriteria string `json:"selectionCriteria"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PolicyName == "" || req.PolicyType == "" || req.PolicyDocument == "" {
		sim.AWSError(w, "InvalidParameterException", "policyName, policyType and policyDocument are required", http.StatusBadRequest)
		return
	}
	p := CWAccountPolicy{
		PolicyName:        req.PolicyName,
		PolicyDocument:    req.PolicyDocument,
		LastUpdatedTime:   time.Now().UnixMilli(),
		PolicyType:        req.PolicyType,
		Scope:             req.Scope,
		SelectionCriteria: req.SelectionCriteria,
		AccountId:         awsAccountID(),
	}
	cwAccountPolicies.Put(cwAccountPolicyKey(req.PolicyType, req.PolicyName), p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"accountPolicy": p})
}

func handleCWDescribeAccountPolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyType         string   `json:"policyType"`
		PolicyName         string   `json:"policyName"`
		AccountIdentifiers []string `json:"accountIdentifiers"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PolicyType == "" {
		sim.AWSError(w, "InvalidParameterException", "policyType is required", http.StatusBadRequest)
		return
	}
	policies := cwAccountPolicies.Filter(func(p CWAccountPolicy) bool {
		if p.PolicyType != req.PolicyType {
			return false
		}
		if req.PolicyName != "" && p.PolicyName != req.PolicyName {
			return false
		}
		return true
	})
	if policies == nil {
		policies = []CWAccountPolicy{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"accountPolicies": policies})
}

func handleCWDeleteAccountPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyName string `json:"policyName"`
		PolicyType string `json:"policyType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwAccountPolicies.Delete(cwAccountPolicyKey(req.PolicyType, req.PolicyName)) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified account policy does not exist: %s", req.PolicyName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWPutQueryDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string   `json:"name"`
		QueryString       string   `json:"queryString"`
		QueryDefinitionId string   `json:"queryDefinitionId"`
		QueryLanguage     string   `json:"queryLanguage"`
		LogGroupNames     []string `json:"logGroupNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.QueryString == "" {
		sim.AWSError(w, "InvalidParameterException", "name and queryString are required", http.StatusBadRequest)
		return
	}
	id := req.QueryDefinitionId
	if id == "" {
		id = uuid.New().String()
	} else if _, ok := cwQueryDefinitions.Get(id); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Query definition does not exist: %s", id)
		return
	}
	cwQueryDefinitions.Put(id, CWQueryDefinition{
		QueryLanguage:     req.QueryLanguage,
		QueryDefinitionId: id,
		Name:              req.Name,
		QueryString:       req.QueryString,
		LastModified:      time.Now().UnixMilli(),
		LogGroupNames:     req.LogGroupNames,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"queryDefinitionId": id})
}

func handleCWDescribeQueryDefinitions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryDefinitionNamePrefix string `json:"queryDefinitionNamePrefix"`
		QueryLanguage             string `json:"queryLanguage"`
	}
	_ = sim.ReadJSON(r, &req)
	defs := cwQueryDefinitions.Filter(func(d CWQueryDefinition) bool {
		if req.QueryDefinitionNamePrefix != "" && !strings.HasPrefix(d.Name, req.QueryDefinitionNamePrefix) {
			return false
		}
		return true
	})
	if defs == nil {
		defs = []CWQueryDefinition{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"queryDefinitions": defs})
}

func handleCWDeleteQueryDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryDefinitionId string `json:"queryDefinitionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	ok := cwQueryDefinitions.Delete(req.QueryDefinitionId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"success": ok})
}

func handleCWPutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyName     string `json:"policyName"`
		PolicyDocument string `json:"policyDocument"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PolicyName == "" {
		sim.AWSError(w, "InvalidParameterException", "policyName is required", http.StatusBadRequest)
		return
	}
	p := CWResourcePolicy{
		PolicyName:      req.PolicyName,
		PolicyDocument:  req.PolicyDocument,
		LastUpdatedTime: time.Now().UnixMilli(),
	}
	cwResourcePolicies.Put(req.PolicyName, p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"resourcePolicy": p})
}

func handleCWDescribeResourcePolicies(w http.ResponseWriter, r *http.Request) {
	policies := cwResourcePolicies.List()
	if policies == nil {
		policies = []CWResourcePolicy{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"resourcePolicies": policies})
}

func handleCWDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyName string `json:"policyName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwResourcePolicies.Delete(req.PolicyName) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified resource policy does not exist: %s", req.PolicyName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func cwDestinationArn(name string) string {
	return fmt.Sprintf("arn:aws:logs:%s:%s:destination:%s", awsRegion(), awsAccountID(), name)
}

func handleCWPutDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DestinationName string            `json:"destinationName"`
		TargetArn       string            `json:"targetArn"`
		RoleArn         string            `json:"roleArn"`
		Tags            map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.DestinationName == "" {
		sim.AWSError(w, "InvalidParameterException", "destinationName is required", http.StatusBadRequest)
		return
	}
	creation := time.Now().UnixMilli()
	access := ""
	if existing, ok := cwDestinations.Get(req.DestinationName); ok {
		creation = existing.CreationTime
		access = existing.AccessPolicy
	}
	d := CWDestination{
		DestinationName: req.DestinationName,
		TargetArn:       req.TargetArn,
		RoleArn:         req.RoleArn,
		AccessPolicy:    access,
		Arn:             cwDestinationArn(req.DestinationName),
		CreationTime:    creation,
		Tags:            req.Tags,
	}
	cwDestinations.Put(req.DestinationName, d)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"destination": d})
}

func handleCWDescribeDestinations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DestinationNamePrefix string `json:"DestinationNamePrefix"`
	}
	_ = sim.ReadJSON(r, &req)
	dests := cwDestinations.Filter(func(d CWDestination) bool {
		if req.DestinationNamePrefix != "" && !strings.HasPrefix(d.DestinationName, req.DestinationNamePrefix) {
			return false
		}
		return true
	})
	if dests == nil {
		dests = []CWDestination{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"destinations": dests})
}

func handleCWDeleteDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DestinationName string `json:"destinationName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwDestinations.Delete(req.DestinationName) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified destination does not exist: %s", req.DestinationName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWPutDestinationPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DestinationName string `json:"destinationName"`
		AccessPolicy    string `json:"accessPolicy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwDestinations.Update(req.DestinationName, func(d *CWDestination) { d.AccessPolicy = req.AccessPolicy }) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified destination does not exist: %s", req.DestinationName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func cwDeliveryArn(id string) string {
	return fmt.Sprintf("arn:aws:logs:%s:%s:delivery:%s", awsRegion(), awsAccountID(), id)
}

func cwDeliverySourceArn(name string) string {
	return fmt.Sprintf("arn:aws:logs:%s:%s:delivery-source:%s", awsRegion(), awsAccountID(), name)
}

func cwDeliveryDestArn(name string) string {
	return fmt.Sprintf("arn:aws:logs:%s:%s:delivery-destination:%s", awsRegion(), awsAccountID(), name)
}

func handleCWCreateDelivery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliverySourceName     string            `json:"deliverySourceName"`
		DeliveryDestinationArn string            `json:"deliveryDestinationArn"`
		RecordFields           []string          `json:"recordFields"`
		FieldDelimiter         string            `json:"fieldDelimiter"`
		Tags                   map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.DeliverySourceName == "" || req.DeliveryDestinationArn == "" {
		sim.AWSError(w, "InvalidParameterException", "deliverySourceName and deliveryDestinationArn are required", http.StatusBadRequest)
		return
	}
	if _, ok := cwDeliverySources.Get(req.DeliverySourceName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery source does not exist: %s", req.DeliverySourceName)
		return
	}
	// Resolve the delivery destination type from the matching destination, if any.
	destType := ""
	for _, dd := range cwDeliveryDests.List() {
		if dd.Arn == req.DeliveryDestinationArn {
			destType = dd.DeliveryDestinationType
			break
		}
	}
	// A delivery id is alphanumeric — the model admits no hyphen — so the UUID
	// is carried without its separators rather than in its dashed form, which a
	// client could not send back to GetDelivery.
	id := strings.ReplaceAll(uuid.New().String(), "-", "")
	d := CWDelivery{
		Id:                      id,
		Arn:                     cwDeliveryArn(id),
		DeliverySourceName:      req.DeliverySourceName,
		DeliveryDestinationArn:  req.DeliveryDestinationArn,
		DeliveryDestinationType: destType,
		RecordFields:            req.RecordFields,
		FieldDelimiter:          req.FieldDelimiter,
		Tags:                    req.Tags,
	}
	cwDeliveries.Put(id, d)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"delivery": d})
}

func handleCWGetDelivery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	d, ok := cwDeliveries.Get(req.Id)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery does not exist: %s", req.Id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"delivery": d})
}

func handleCWDeleteDelivery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwDeliveries.Delete(req.Id) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery does not exist: %s", req.Id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDescribeDeliveries(w http.ResponseWriter, r *http.Request) {
	deliveries := cwDeliveries.List()
	if deliveries == nil {
		deliveries = []CWDelivery{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"deliveries": deliveries})
}

func handleCWPutDeliverySource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string            `json:"name"`
		ResourceArn string            `json:"resourceArn"`
		LogType     string            `json:"logType"`
		Tags        map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		sim.AWSError(w, "InvalidParameterException", "name is required", http.StatusBadRequest)
		return
	}
	var resourceArns []string
	if req.ResourceArn != "" {
		resourceArns = []string{req.ResourceArn}
	}
	src := CWDeliverySource{
		Name:         req.Name,
		Arn:          cwDeliverySourceArn(req.Name),
		ResourceArns: resourceArns,
		LogType:      req.LogType,
		Tags:         req.Tags,
	}
	cwDeliverySources.Put(req.Name, src)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"deliverySource": src})
}

func handleCWGetDeliverySource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	src, ok := cwDeliverySources.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery source does not exist: %s", req.Name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"deliverySource": src})
}

func handleCWDescribeDeliverySources(w http.ResponseWriter, r *http.Request) {
	sources := cwDeliverySources.List()
	if sources == nil {
		sources = []CWDeliverySource{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"deliverySources": sources})
}

func handleCWDeleteDeliverySource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwDeliverySources.Delete(req.Name) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery source does not exist: %s", req.Name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWPutDeliveryDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                             string                              `json:"name"`
		OutputFormat                     string                              `json:"outputFormat"`
		DeliveryDestinationConfiguration *CWDeliveryDestinationConfiguration `json:"deliveryDestinationConfiguration"`
		DeliveryDestinationType          string                              `json:"deliveryDestinationType"`
		Tags                             map[string]string                   `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		sim.AWSError(w, "InvalidParameterException", "name is required", http.StatusBadRequest)
		return
	}
	destType := req.DeliveryDestinationType
	if destType == "" {
		destType = "S3"
	}
	policy := ""
	if existing, ok := cwDeliveryDests.Get(req.Name); ok {
		policy = existing.Policy
	}
	dd := CWDeliveryDestination{
		Name:                             req.Name,
		Arn:                              cwDeliveryDestArn(req.Name),
		DeliveryDestinationType:          destType,
		OutputFormat:                     req.OutputFormat,
		DeliveryDestinationConfiguration: req.DeliveryDestinationConfiguration,
		Tags:                             req.Tags,
		Policy:                           policy,
	}
	cwDeliveryDests.Put(req.Name, dd)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"deliveryDestination": dd})
}

func handleCWGetDeliveryDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	dd, ok := cwDeliveryDests.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery destination does not exist: %s", req.Name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"deliveryDestination": dd})
}

func handleCWDescribeDeliveryDestinations(w http.ResponseWriter, r *http.Request) {
	dests := cwDeliveryDests.List()
	if dests == nil {
		dests = []CWDeliveryDestination{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"deliveryDestinations": dests})
}

func handleCWDeleteDeliveryDestination(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwDeliveryDests.Delete(req.Name) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery destination does not exist: %s", req.Name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWPutDeliveryDestinationPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryDestinationName   string `json:"deliveryDestinationName"`
		DeliveryDestinationPolicy string `json:"deliveryDestinationPolicy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwDeliveryDests.Update(req.DeliveryDestinationName, func(dd *CWDeliveryDestination) { dd.Policy = req.DeliveryDestinationPolicy }) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery destination does not exist: %s", req.DeliveryDestinationName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"policy": map[string]any{"deliveryDestinationPolicy": req.DeliveryDestinationPolicy},
	})
}

func handleCWGetDeliveryDestinationPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryDestinationName string `json:"deliveryDestinationName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	dd, ok := cwDeliveryDests.Get(req.DeliveryDestinationName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery destination does not exist: %s", req.DeliveryDestinationName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"policy": map[string]any{"deliveryDestinationPolicy": dd.Policy},
	})
}

func handleCWDeleteDeliveryDestinationPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeliveryDestinationName string `json:"deliveryDestinationName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwDeliveryDests.Update(req.DeliveryDestinationName, func(dd *CWDeliveryDestination) { dd.Policy = "" }) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified delivery destination does not exist: %s", req.DeliveryDestinationName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func cwAnomalyDetectorArn(id string) string {
	return fmt.Sprintf("arn:aws:logs:%s:%s:anomaly-detector:%s", awsRegion(), awsAccountID(), id)
}

func handleCWCreateLogAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DetectorName          string   `json:"detectorName"`
		LogGroupArnList       []string `json:"logGroupArnList"`
		EvaluationFrequency   string   `json:"evaluationFrequency"`
		FilterPattern         string   `json:"filterPattern"`
		KmsKeyId              string   `json:"kmsKeyId"`
		AnomalyVisibilityTime int64    `json:"anomalyVisibilityTime"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.LogGroupArnList) == 0 {
		sim.AWSError(w, "InvalidParameterException", "logGroupArnList is required", http.StatusBadRequest)
		return
	}
	now := time.Now().UnixMilli()
	arn := cwAnomalyDetectorArn(uuid.New().String())
	freq := req.EvaluationFrequency
	if freq == "" {
		freq = "ONE_HOUR"
	}
	det := CWLogAnomalyDetector{
		AnomalyDetectorArn:    arn,
		DetectorName:          req.DetectorName,
		LogGroupArnList:       req.LogGroupArnList,
		EvaluationFrequency:   freq,
		FilterPattern:         req.FilterPattern,
		AnomalyDetectorStatus: "INITIALIZING",
		KmsKeyId:              req.KmsKeyId,
		CreationTimeStamp:     now,
		LastModifiedTimeStamp: now,
		AnomalyVisibilityTime: req.AnomalyVisibilityTime,
	}
	cwLogAnomalyDetectors.Put(arn, det)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"anomalyDetectorArn": arn})
}

func handleCWGetLogAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnomalyDetectorArn string `json:"anomalyDetectorArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	det, ok := cwLogAnomalyDetectors.Get(req.AnomalyDetectorArn)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified anomaly detector does not exist: %s", req.AnomalyDetectorArn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"detectorName":          det.DetectorName,
		"logGroupArnList":       det.LogGroupArnList,
		"evaluationFrequency":   det.EvaluationFrequency,
		"filterPattern":         det.FilterPattern,
		"anomalyDetectorStatus": det.AnomalyDetectorStatus,
		"kmsKeyId":              det.KmsKeyId,
		"creationTimeStamp":     det.CreationTimeStamp,
		"lastModifiedTimeStamp": det.LastModifiedTimeStamp,
		"anomalyVisibilityTime": det.AnomalyVisibilityTime,
	})
}

func handleCWListLogAnomalyDetectors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FilterLogGroupArn string `json:"filterLogGroupArn"`
	}
	_ = sim.ReadJSON(r, &req)
	detectors := cwLogAnomalyDetectors.Filter(func(d CWLogAnomalyDetector) bool {
		if req.FilterLogGroupArn == "" {
			return true
		}
		for _, lg := range d.LogGroupArnList {
			if lg == req.FilterLogGroupArn {
				return true
			}
		}
		return false
	})
	if detectors == nil {
		detectors = []CWLogAnomalyDetector{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"anomalyDetectors": detectors})
}

func handleCWDeleteLogAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnomalyDetectorArn string `json:"anomalyDetectorArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwLogAnomalyDetectors.Delete(req.AnomalyDetectorArn) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified anomaly detector does not exist: %s", req.AnomalyDetectorArn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWPutIndexPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		PolicyDocument     string `json:"policyDocument"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupIdentifier == "" || req.PolicyDocument == "" {
		sim.AWSError(w, "InvalidParameterException", "logGroupIdentifier and policyDocument are required", http.StatusBadRequest)
		return
	}
	p := CWIndexPolicy{
		LogGroupIdentifier: req.LogGroupIdentifier,
		LastUpdateTime:     time.Now().UnixMilli(),
		PolicyDocument:     req.PolicyDocument,
		Source:             "LOG_GROUP",
	}
	cwIndexPolicies.Put(req.LogGroupIdentifier, p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"indexPolicy": p})
}

func handleCWDeleteIndexPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !cwIndexPolicies.Delete(req.LogGroupIdentifier) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified index policy does not exist: %s", req.LogGroupIdentifier)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDescribeIndexPolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifiers []string `json:"logGroupIdentifiers"`
	}
	_ = sim.ReadJSON(r, &req)
	wanted := map[string]bool{}
	for _, id := range req.LogGroupIdentifiers {
		wanted[id] = true
	}
	policies := cwIndexPolicies.Filter(func(p CWIndexPolicy) bool {
		if len(wanted) == 0 {
			return true
		}
		return wanted[p.LogGroupIdentifier]
	})
	if policies == nil {
		policies = []CWIndexPolicy{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"indexPolicies": policies})
}

func handleCWDescribeFieldIndexes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifiers []string `json:"logGroupIdentifiers"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// Field indexes are realized once an index policy is in effect and the log
	// group has ingested matching events. With no ingested field-index events
	// the published list is empty, matching real CloudWatch Logs.
	sim.WriteJSON(w, http.StatusOK, map[string]any{"fieldIndexes": []any{}})
}

// handleCWDescribeConfigurationTemplates returns the delivery configuration
// templates AWS publishes for vended-log delivery. The shape and values mirror
// the real DescribeConfigurationTemplates response for two representative log
// sources (Amazon Bedrock model-invocation logging and AWS WAF web-ACL logs).
func handleCWDescribeConfigurationTemplates(w http.ResponseWriter, r *http.Request) {
	templates := []map[string]any{
		{
			"service":                 "bedrock",
			"logType":                 "APPLICATION_LOGS",
			"resourceType":            "AWS::Bedrock::ModelInvocationLogGroup",
			"deliveryDestinationType": "CWL",
			"defaultDeliveryConfigValues": map[string]any{
				"recordFields": []string{},
			},
			"allowedFields": []map[string]any{
				{"name": "timestamp", "mandatory": true},
			},
			"allowedOutputFormats":   []string{"json"},
			"allowedFieldDelimiters": []string{},
		},
		{
			"service":                 "wafv2",
			"logType":                 "WAFLogs",
			"resourceType":            "AWS::WAFv2::WebACL",
			"deliveryDestinationType": "S3",
			"defaultDeliveryConfigValues": map[string]any{
				"recordFields":   []string{},
				"fieldDelimiter": ",",
			},
			"allowedFields": []map[string]any{
				{"name": "timestamp", "mandatory": true},
			},
			"allowedOutputFormats":   []string{"json", "plain", "w3c", "raw", "parquet"},
			"allowedFieldDelimiters": []string{",", "\t", " ", ";", "|"},
		},
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"configurationTemplates": templates})
}

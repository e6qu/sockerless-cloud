package main

import (
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Service Quotas speaks AWS JSON 1.1 under the ServiceQuotasV20190624 target
// prefix. Its catalog is the generated sqCatalog: the quotas the vendored
// General Reference pages publish with a quota code.

type sqCatalogQuota struct {
	ServiceCode  string
	ServiceName  string
	QuotaCode    string
	QuotaName    string
	Value        float64
	Unit         string
	RegionValues map[string]float64
	PeriodUnit   string
	Global       bool
	Description  string
	Source       string
}

// sqRequest is one RequestedServiceQuotaChange.
type sqRequest struct {
	Id            string
	RequestType   string
	CaseId        string
	Account       string
	Region        string
	ServiceCode   string
	QuotaCode     string
	DesiredValue  float64
	Status        string
	Created       float64
	LastUpdated   float64
	Requester     string
	SupportCase   bool
	Seq           int64
	TemplateApply bool
}

type sqTemplateEntry struct {
	ServiceCode  string
	QuotaCode    string
	AwsRegion    string
	DesiredValue float64
}

type sqAutoConfig struct {
	OptInLevel      string
	OptInType       string
	NotificationArn string
	ExclusionList   map[string][]string
}

type sqReportQuota struct {
	QuotaCode, ServiceCode, QuotaName, ServiceName, Namespace string
	Utilization, DefaultValue, AppliedValue                   float64
	Adjustable                                                bool
}

type sqReport struct {
	ReportId    string
	Account     string
	Status      string
	GeneratedAt float64
	Quotas      []sqReportQuota
	Created     float64
}

const (
	sqTrustedPrincipal    = "servicequotas.amazonaws.com"
	sqTemplateMaxRequests = 10
	sqReportLifetime      = 15 * time.Minute
	sqMaxTags             = 200
)

var (
	sqApplied        sim.Store[float64]
	sqRequests       sim.Store[sqRequest]
	sqTemplate       sim.Store[sqTemplateEntry]
	sqAssociation    sim.Store[bool]
	sqTags           sim.Store[map[string]string]
	sqAutoManagement sim.Store[sqAutoConfig]
	sqReports        sim.Store[sqReport]

	sqCatalogIndex = func() map[string]sqCatalogQuota {
		out := make(map[string]sqCatalogQuota, len(sqCatalog))
		for _, q := range sqCatalog {
			out[q.ServiceCode+"/"+q.QuotaCode] = q
		}
		return out
	}()

	sqSeqMu sync.Mutex
	sqSeq   int64
)

func registerServiceQuotas(r *AWSRouter, srv *sim.Server) {
	sqApplied = sim.MakeStore[float64](srv.DB(), "servicequotas_applied")
	sqRequests = sim.MakeStore[sqRequest](srv.DB(), "servicequotas_requests")
	sqTemplate = sim.MakeStore[sqTemplateEntry](srv.DB(), "servicequotas_template")
	sqAssociation = sim.MakeStore[bool](srv.DB(), "servicequotas_template_association")
	sqTags = sim.MakeStore[map[string]string](srv.DB(), "servicequotas_tags")
	sqAutoManagement = sim.MakeStore[sqAutoConfig](srv.DB(), "servicequotas_auto_management")
	sqReports = sim.MakeStore[sqReport](srv.DB(), "servicequotas_reports")
	for _, req := range sqRequests.List() {
		sqSeqMu.Lock()
		sqSeq = max(sqSeq, req.Seq)
		sqSeqMu.Unlock()
	}

	r.Register("ServiceQuotasV20190624.AssociateServiceQuotaTemplate", handleSQAssociateServiceQuotaTemplate)
	r.Register("ServiceQuotasV20190624.CreateSupportCase", handleSQCreateSupportCase)
	r.Register("ServiceQuotasV20190624.DeleteServiceQuotaIncreaseRequestFromTemplate", handleSQDeleteServiceQuotaIncreaseRequestFromTemplate)
	r.Register("ServiceQuotasV20190624.DisassociateServiceQuotaTemplate", handleSQDisassociateServiceQuotaTemplate)
	r.Register("ServiceQuotasV20190624.GetAWSDefaultServiceQuota", handleSQGetAWSDefaultServiceQuota)
	r.Register("ServiceQuotasV20190624.GetAssociationForServiceQuotaTemplate", handleSQGetAssociationForServiceQuotaTemplate)
	r.Register("ServiceQuotasV20190624.GetAutoManagementConfiguration", handleSQGetAutoManagementConfiguration)
	r.Register("ServiceQuotasV20190624.GetQuotaUtilizationReport", handleSQGetQuotaUtilizationReport)
	r.Register("ServiceQuotasV20190624.GetRequestedServiceQuotaChange", handleSQGetRequestedServiceQuotaChange)
	r.Register("ServiceQuotasV20190624.GetServiceQuota", handleSQGetServiceQuota)
	r.Register("ServiceQuotasV20190624.GetServiceQuotaIncreaseRequestFromTemplate", handleSQGetServiceQuotaIncreaseRequestFromTemplate)
	r.Register("ServiceQuotasV20190624.ListAWSDefaultServiceQuotas", handleSQListAWSDefaultServiceQuotas)
	r.Register("ServiceQuotasV20190624.ListRequestedServiceQuotaChangeHistory", handleSQListRequestedServiceQuotaChangeHistory)
	r.Register("ServiceQuotasV20190624.ListRequestedServiceQuotaChangeHistoryByQuota", handleSQListRequestedServiceQuotaChangeHistoryByQuota)
	r.Register("ServiceQuotasV20190624.ListServiceQuotaIncreaseRequestsInTemplate", handleSQListServiceQuotaIncreaseRequestsInTemplate)
	r.Register("ServiceQuotasV20190624.ListServiceQuotas", handleSQListServiceQuotas)
	r.Register("ServiceQuotasV20190624.ListServices", handleSQListServices)
	r.Register("ServiceQuotasV20190624.ListTagsForResource", handleSQListTagsForResource)
	r.Register("ServiceQuotasV20190624.PutServiceQuotaIncreaseRequestIntoTemplate", handleSQPutServiceQuotaIncreaseRequestIntoTemplate)
	r.Register("ServiceQuotasV20190624.RequestServiceQuotaIncrease", handleSQRequestServiceQuotaIncrease)
	r.Register("ServiceQuotasV20190624.StartAutoManagement", handleSQStartAutoManagement)
	r.Register("ServiceQuotasV20190624.StartQuotaUtilizationReport", handleSQStartQuotaUtilizationReport)
	r.Register("ServiceQuotasV20190624.StopAutoManagement", handleSQStopAutoManagement)
	r.Register("ServiceQuotasV20190624.TagResource", handleSQTagResource)
	r.Register("ServiceQuotasV20190624.UntagResource", handleSQUntagResource)
	r.Register("ServiceQuotasV20190624.UpdateAutoManagement", handleSQUpdateAutoManagement)

}

func sqError(w http.ResponseWriter, code string, status int, format string, args ...any) {
	awsWriteError(w, awsErrorEnvelope{Protocol: awsJSON11, Model: "service-quotas"}, status, code, fmt.Sprintf(format, args...), "")
}

func sqIllegal(w http.ResponseWriter, format string, args ...any) {
	sqError(w, "IllegalArgumentException", http.StatusBadRequest, format, args...)
}

func sqNoSuchResource(w http.ResponseWriter, format string, args ...any) {
	sqError(w, "NoSuchResourceException", http.StatusNotFound, format, args...)
}

func sqBadToken(w http.ResponseWriter, _ string) {
	sqError(w, "InvalidPaginationTokenException", http.StatusBadRequest, "Invalid pagination token.")
}

func sqRead(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := sim.ReadJSON(r, v); err != nil {
		sqIllegal(w, "Invalid request body: %v", err)
		return false
	}
	return true
}

func sqRequired(w http.ResponseWriter, values map[string]string) bool {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if values[name] == "" {
			sqIllegal(w, "1 validation error detected: Value null at '%s' failed to satisfy constraint: Member must not be null", name)
			return false
		}
	}
	return true
}

func sqRegion(r *http.Request) string {
	if region := iamRequestedRegion(r); region != "" {
		return region
	}
	return awsRegion()
}

func sqNow() float64 { return float64(time.Now().UnixMilli()) / 1000 }

// sqQuotaDefault answers a quota's default in region: the value the page
// names for the Region, else the value for the other Regions.
func sqQuotaDefault(q sqCatalogQuota, region string) float64 {
	if v, ok := q.RegionValues[region]; ok {
		return v
	}
	return q.Value
}

func sqAppliedKey(account, region string, q sqCatalogQuota) string {
	if q.Global {
		region = "global"
	}
	return account + "|" + region + "|" + q.ServiceCode + "|" + q.QuotaCode
}

// sqAppliedQuota answers the quota value applied to account in region: the
// value an approved increase set, else the default.
func sqAppliedQuota(account, region string, q sqCatalogQuota) float64 {
	if v, ok := sqApplied.Get(sqAppliedKey(account, region, q)); ok {
		return v
	}
	return sqQuotaDefault(q, region)
}

// sqQuotaLimit answers, as a count, the value applied to the simulator's
// account in its Region for the quota serviceCode/quotaCode names. The
// services that enforce a quota read it here.
func sqQuotaLimit(serviceCode, quotaCode string) int {
	return sqRegionQuotaLimit(awsRegion(), serviceCode, quotaCode)
}

// sqRegionQuotaLimit answers, as a count, the value applied to the
// simulator's account in region for the quota serviceCode/quotaCode names.
func sqRegionQuotaLimit(region, serviceCode, quotaCode string) int {
	q, ok := sqCatalogIndex[serviceCode+"/"+quotaCode]
	if !ok {
		panic("servicequotas: no catalogued quota " + serviceCode + "/" + quotaCode)
	}
	value := sqQuotaDefault(q, region)
	if sqApplied != nil {
		value = sqAppliedQuota(awsAccountID(), region, q)
	}
	return int(math.Floor(value))
}

func sqQuotaARN(region, account string, q sqCatalogQuota) string {
	return "arn:aws:servicequotas:" + region + ":" + account + ":" + q.ServiceCode + "/" + q.QuotaCode
}

func sqServiceExists(service string) bool {
	for _, q := range sqCatalog {
		if q.ServiceCode == service {
			return true
		}
	}
	return false
}

// sqFind resolves a service and quota code, writing the error a missing one
// answers.
func sqFind(w http.ResponseWriter, service, code string) (sqCatalogQuota, bool) {
	if !sqServiceExists(service) {
		sqNoSuchResource(w, "The request failed because the specified service does not exist.")
		return sqCatalogQuota{}, false
	}
	q, ok := sqCatalogIndex[service+"/"+code]
	if !ok {
		sqNoSuchResource(w, "The request failed because the specified quota does not exist.")
		return sqCatalogQuota{}, false
	}
	return q, true
}

func sqQuotaJSON(q sqCatalogQuota, region, account string, value float64, applied bool) map[string]any {
	out := map[string]any{
		"ServiceCode": q.ServiceCode,
		"ServiceName": q.ServiceName,
		"QuotaArn":    sqQuotaARN(region, account, q),
		"QuotaCode":   q.QuotaCode,
		"QuotaName":   q.QuotaName,
		"Value":       cwJSONStat(value),
		"Unit":        q.Unit,
		"Adjustable":  true,
		"GlobalQuota": q.Global,
	}
	if q.Description != "" {
		out["Description"] = q.Description
	}
	if q.PeriodUnit != "" {
		out["Period"] = map[string]any{"PeriodValue": 1, "PeriodUnit": q.PeriodUnit}
	}
	if m, ok := sqUsageMetric(q); ok {
		dims := map[string]string{}
		for _, d := range m.Dimensions {
			dims[d.Name] = d.Value
		}
		out["UsageMetric"] = map[string]any{
			"MetricNamespace":               awsUsageNamespace,
			"MetricName":                    m.MetricName,
			"MetricDimensions":              dims,
			"MetricStatisticRecommendation": m.Statistic,
		}
	}
	if applied {
		out["QuotaAppliedAtLevel"] = "ACCOUNT"
	}
	return out
}

func sqServiceQuotas(service string) []sqCatalogQuota {
	var out []sqCatalogQuota
	for _, q := range sqCatalog {
		if q.ServiceCode == service {
			out = append(out, q)
		}
	}
	return out
}

func handleSQListServices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int32 `json:"MaxResults"`
	}
	if !sqRead(w, r, &req) || !sqCheckMaxResults(w, req.MaxResults, 100) {
		return
	}
	seen := map[string]bool{}
	var services []map[string]any
	for _, q := range sqCatalog {
		if seen[q.ServiceCode] {
			continue
		}
		seen[q.ServiceCode] = true
		services = append(services, map[string]any{"ServiceCode": q.ServiceCode, "ServiceName": q.ServiceName})
	}
	page, next, ok := awsKeyPage(w, sqBadToken, services, awsFieldKey("ServiceCode"), req.NextToken, awsMaxResults(req.MaxResults), 100)
	if !ok {
		return
	}
	out := map[string]any{"Services": page}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func sqCheckMaxResults(w http.ResponseWriter, v *int32, limit int32) bool {
	if v != nil && (*v < 1 || *v > limit) {
		sqIllegal(w, "1 validation error detected: Value '%d' at 'maxResults' failed to satisfy constraint: Member must have value less than or equal to %d", *v, limit)
		return false
	}
	return true
}

func handleSQGetAWSDefaultServiceQuota(w http.ResponseWriter, r *http.Request) {
	var req struct{ ServiceCode, QuotaCode string }
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"serviceCode": req.ServiceCode, "quotaCode": req.QuotaCode}) {
		return
	}
	q, ok := sqFind(w, req.ServiceCode, req.QuotaCode)
	if !ok {
		return
	}
	region := sqRegion(r)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Quota": sqQuotaJSON(q, region, iamCallerAccount(r), sqQuotaDefault(q, region), false)})
}

func handleSQGetServiceQuota(w http.ResponseWriter, r *http.Request) {
	var req struct{ ServiceCode, QuotaCode, ContextId string }
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"serviceCode": req.ServiceCode, "quotaCode": req.QuotaCode}) {
		return
	}
	q, ok := sqFind(w, req.ServiceCode, req.QuotaCode)
	if !ok {
		return
	}
	if req.ContextId != "" {
		sqNoSuchResource(w, "The request failed because the specified quota does not exist.")
		return
	}
	region, account := sqRegion(r), iamCallerAccount(r)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Quota": sqQuotaJSON(q, region, account, sqAppliedQuota(account, region, q), true)})
}

func sqListQuotas(w http.ResponseWriter, r *http.Request, applied bool) {
	var req struct {
		ServiceCode         string `json:"ServiceCode"`
		QuotaCode           string `json:"QuotaCode"`
		QuotaAppliedAtLevel string `json:"QuotaAppliedAtLevel"`
		NextToken           string `json:"NextToken"`
		MaxResults          *int32 `json:"MaxResults"`
	}
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"serviceCode": req.ServiceCode}) || !sqCheckMaxResults(w, req.MaxResults, 100) {
		return
	}
	if !sqServiceExists(req.ServiceCode) {
		sqNoSuchResource(w, "The request failed because the specified service does not exist.")
		return
	}
	switch req.QuotaAppliedAtLevel {
	case "", "ACCOUNT", "RESOURCE", "ALL":
	default:
		sqIllegal(w, "1 validation error detected: Value '%s' at 'quotaAppliedAtLevel' failed to satisfy constraint: Member must satisfy enum value set: [ACCOUNT, RESOURCE, ALL]", req.QuotaAppliedAtLevel)
		return
	}
	region, account := sqRegion(r), iamCallerAccount(r)
	var quotas []map[string]any
	if req.QuotaAppliedAtLevel != "RESOURCE" {
		for _, q := range sqServiceQuotas(req.ServiceCode) {
			if req.QuotaCode != "" && q.QuotaCode != req.QuotaCode {
				continue
			}
			value := sqQuotaDefault(q, region)
			if applied {
				value = sqAppliedQuota(account, region, q)
			}
			quotas = append(quotas, sqQuotaJSON(q, region, account, value, applied))
		}
	}
	page, next, ok := awsKeyPage(w, sqBadToken, quotas, awsFieldKey("QuotaCode"), req.NextToken, awsMaxResults(req.MaxResults), 100)
	if !ok {
		return
	}
	out := map[string]any{"Quotas": page}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSQListServiceQuotas(w http.ResponseWriter, r *http.Request) { sqListQuotas(w, r, true) }

func handleSQListAWSDefaultServiceQuotas(w http.ResponseWriter, r *http.Request) {
	sqListQuotas(w, r, false)
}

func sqNextSeq() int64 {
	sqSeqMu.Lock()
	defer sqSeqMu.Unlock()
	sqSeq++
	return sqSeq
}

func sqRequester(r *http.Request) string {
	return fmt.Sprintf(`{"accountId":"%s","callerArn":"%s"}`, iamCallerAccount(r), awsCallerArn(r))
}

func sqOpen(status string) bool { return status == "PENDING" || status == "CASE_OPENED" }

func handleSQRequestServiceQuotaIncrease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceCode        string   `json:"ServiceCode"`
		QuotaCode          string   `json:"QuotaCode"`
		DesiredValue       *float64 `json:"DesiredValue"`
		ContextId          string   `json:"ContextId"`
		SupportCaseAllowed *bool    `json:"SupportCaseAllowed"`
	}
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"serviceCode": req.ServiceCode, "quotaCode": req.QuotaCode}) {
		return
	}
	if req.DesiredValue == nil {
		sqIllegal(w, "1 validation error detected: Value null at 'desiredValue' failed to satisfy constraint: Member must not be null")
		return
	}
	q, ok := sqFind(w, req.ServiceCode, req.QuotaCode)
	if !ok {
		return
	}
	if req.ContextId != "" {
		sqIllegal(w, "The quota %s is not adjustable at the resource level.", q.QuotaCode)
		return
	}
	account, region := iamCallerAccount(r), sqRegion(r)
	current := sqAppliedQuota(account, region, q)
	if *req.DesiredValue <= current {
		sqIllegal(w, "The requested value %s must be greater than the current value %s.", cwFormatFloat(*req.DesiredValue), cwFormatFloat(current))
		return
	}
	if *req.DesiredValue > 10000000000 {
		sqIllegal(w, "1 validation error detected: Value '%s' at 'desiredValue' failed to satisfy constraint: Member must have value less than or equal to 1.0E10", cwFormatFloat(*req.DesiredValue))
		return
	}
	for _, existing := range sqRequests.List() {
		if existing.Account == account && existing.Region == sqRequestRegion(region, q) && existing.ServiceCode == q.ServiceCode &&
			existing.QuotaCode == q.QuotaCode && sqOpen(existing.Status) {
			sqError(w, "ResourceAlreadyExistsException", http.StatusBadRequest, "Only one open service quota increase request is allowed per quota.")
			return
		}
	}
	created := sqSubmit(account, region, q, *req.DesiredValue, sqRequester(r), req.SupportCaseAllowed == nil || *req.SupportCaseAllowed, "")
	sim.WriteJSON(w, http.StatusOK, map[string]any{"RequestedQuota": sqRequestJSON(created)})
}

func sqRequestRegion(region string, q sqCatalogQuota) string {
	if q.Global {
		return "global"
	}
	return region
}

// sqSubmit records a PENDING request and hands it to the processor.
func sqSubmit(account, region string, q sqCatalogQuota, desired float64, requester string, supportCase bool, requestType string) sqRequest {
	now := sqNow()
	req := sqRequest{
		Id:           strings.ReplaceAll(sim.NewUUID(), "-", ""),
		RequestType:  requestType,
		Account:      account,
		Region:       sqRequestRegion(region, q),
		ServiceCode:  q.ServiceCode,
		QuotaCode:    q.QuotaCode,
		DesiredValue: desired,
		Status:       "PENDING",
		Created:      now,
		LastUpdated:  now,
		Requester:    requester,
		SupportCase:  supportCase,
		Seq:          sqNextSeq(),
	}
	sqRequests.Put(req.Id, req)
	sqProcess(req.Id)
	return req
}

// sqProcess settles a PENDING request: the simulator stands in for the review
// AWS gives an increase, approves it and applies the desired value.
func sqProcess(id string) {
	var approved sqRequest
	if !sqRequests.Update(id, func(req *sqRequest) {
		if req.Status != "PENDING" {
			return
		}
		req.Status = "APPROVED"
		req.LastUpdated = sqNow()
		approved = *req
	}) || approved.Id == "" {
		return
	}
	q := sqCatalogIndex[approved.ServiceCode+"/"+approved.QuotaCode]
	region := approved.Region
	if region == "global" {
		region = awsRegion()
	}
	key := sqAppliedKey(approved.Account, region, q)
	sqApplied.Upsert(key, func(v *float64) {
		if approved.DesiredValue > *v {
			*v = approved.DesiredValue
		}
	})
	sqQuotaChanged(approved.ServiceCode, approved.QuotaCode)
}

// sqQuotaListeners lets an enforcing service learn that a quota it reads
// changed, so a limit it sized at startup can grow.
var (
	sqQuotaListenersMu sync.Mutex
	sqQuotaListeners   = map[string][]func(){}
)

func sqOnQuotaChange(serviceCode, quotaCode string, fn func()) {
	sqQuotaListenersMu.Lock()
	defer sqQuotaListenersMu.Unlock()
	key := serviceCode + "/" + quotaCode
	sqQuotaListeners[key] = append(sqQuotaListeners[key], fn)
}

func sqQuotaChanged(serviceCode, quotaCode string) {
	sqQuotaListenersMu.Lock()
	listeners := append([]func(){}, sqQuotaListeners[serviceCode+"/"+quotaCode]...)
	sqQuotaListenersMu.Unlock()
	for _, fn := range listeners {
		fn()
	}
}

func sqRequestJSON(req sqRequest) map[string]any {
	q := sqCatalogIndex[req.ServiceCode+"/"+req.QuotaCode]
	region := req.Region
	if region == "global" {
		region = awsRegion()
	}
	out := map[string]any{
		"Id":                    req.Id,
		"ServiceCode":           q.ServiceCode,
		"ServiceName":           q.ServiceName,
		"QuotaCode":             q.QuotaCode,
		"QuotaName":             q.QuotaName,
		"DesiredValue":          cwJSONStat(req.DesiredValue),
		"Status":                req.Status,
		"Created":               req.Created,
		"LastUpdated":           req.LastUpdated,
		"Requester":             req.Requester,
		"QuotaArn":              sqQuotaARN(region, req.Account, q),
		"GlobalQuota":           q.Global,
		"Unit":                  q.Unit,
		"QuotaRequestedAtLevel": "ACCOUNT",
	}
	if req.CaseId != "" {
		out["CaseId"] = req.CaseId
	}
	if req.RequestType != "" {
		out["RequestType"] = req.RequestType
	}
	return out
}

func sqOwnRequest(w http.ResponseWriter, r *http.Request, id string) (sqRequest, bool) {
	req, ok := sqRequests.Get(id)
	if !ok || req.Account != iamCallerAccount(r) {
		sqNoSuchResource(w, "The request ID %s could not be found.", id)
		return sqRequest{}, false
	}
	return req, true
}

func handleSQGetRequestedServiceQuotaChange(w http.ResponseWriter, r *http.Request) {
	var req struct{ RequestId string }
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"requestId": req.RequestId}) {
		return
	}
	found, ok := sqOwnRequest(w, r, req.RequestId)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"RequestedQuota": sqRequestJSON(found)})
}

func handleSQCreateSupportCase(w http.ResponseWriter, r *http.Request) {
	var req struct{ RequestId string }
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"requestId": req.RequestId}) {
		return
	}
	found, ok := sqOwnRequest(w, r, req.RequestId)
	if !ok {
		return
	}
	if found.CaseId != "" {
		sqError(w, "ResourceAlreadyExistsException", http.StatusBadRequest, "A support case already exists for request %s.", found.Id)
		return
	}
	if found.Status != "PENDING" {
		sqError(w, "InvalidResourceStateException", http.StatusMethodNotAllowed,
			"A support case can be created only for a request with the PENDING status; request %s is %s.", found.Id, found.Status)
		return
	}
	sqRequests.Update(found.Id, func(stored *sqRequest) {
		stored.CaseId = fmt.Sprintf("%d", 170000000000+stored.Seq)
		stored.Status = "CASE_OPENED"
		stored.LastUpdated = sqNow()
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

type sqHistoryRequest struct {
	ServiceCode           string `json:"ServiceCode"`
	QuotaCode             string `json:"QuotaCode"`
	Status                string `json:"Status"`
	QuotaRequestedAtLevel string `json:"QuotaRequestedAtLevel"`
	NextToken             string `json:"NextToken"`
	MaxResults            *int32 `json:"MaxResults"`
}

var sqStatuses = map[string]bool{"PENDING": true, "CASE_OPENED": true, "APPROVED": true, "DENIED": true, "CASE_CLOSED": true, "NOT_APPROVED": true, "INVALID_REQUEST": true}

func sqHistory(w http.ResponseWriter, r *http.Request, req sqHistoryRequest, byQuota bool) {
	if req.Status != "" && !sqStatuses[req.Status] {
		sqIllegal(w, "1 validation error detected: Value '%s' at 'status' failed to satisfy constraint: Member must satisfy enum value set: [PENDING, CASE_OPENED, APPROVED, DENIED, CASE_CLOSED, NOT_APPROVED, INVALID_REQUEST]", req.Status)
		return
	}
	if req.ServiceCode != "" && !sqServiceExists(req.ServiceCode) {
		sqNoSuchResource(w, "The request failed because the specified service does not exist.")
		return
	}
	if byQuota {
		if _, ok := sqFind(w, req.ServiceCode, req.QuotaCode); !ok {
			return
		}
	}
	account, region := iamCallerAccount(r), sqRegion(r)
	cutoff := sqNow() - 90*24*3600
	var matched []sqRequest
	for _, item := range sqRequests.List() {
		if item.Account != account || (item.Region != region && item.Region != "global") || item.Created < cutoff {
			continue
		}
		if req.ServiceCode != "" && item.ServiceCode != req.ServiceCode || byQuota && item.QuotaCode != req.QuotaCode {
			continue
		}
		if req.Status != "" && item.Status != req.Status || req.QuotaRequestedAtLevel == "RESOURCE" {
			continue
		}
		matched = append(matched, item)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Seq > matched[j].Seq })
	page, next, ok := awsSeqPage(w, sqBadToken, matched, false, req.NextToken, awsMaxResults(req.MaxResults), 20)
	if !ok {
		return
	}
	items := make([]map[string]any, 0, len(page))
	for _, item := range page {
		items = append(items, sqRequestJSON(item))
	}
	out := map[string]any{"RequestedQuotas": items}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSQListRequestedServiceQuotaChangeHistory(w http.ResponseWriter, r *http.Request) {
	var req sqHistoryRequest
	if !sqRead(w, r, &req) || !sqCheckMaxResults(w, req.MaxResults, 100) {
		return
	}
	req.QuotaCode = ""
	sqHistory(w, r, req, false)
}

func handleSQListRequestedServiceQuotaChangeHistoryByQuota(w http.ResponseWriter, r *http.Request) {
	var req sqHistoryRequest
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"serviceCode": req.ServiceCode, "quotaCode": req.QuotaCode}) || !sqCheckMaxResults(w, req.MaxResults, 100) {
		return
	}
	sqHistory(w, r, req, true)
}

// sqTemplateOrg checks what every template operation needs: an organization
// with all features, a caller in its management account and trusted access
// for Service Quotas.
func sqTemplateOrg(w http.ResponseWriter, r *http.Request) (OrgOrganization, bool) {
	org, ok := orgOrg.Get(orgSingletonKey)
	if !ok {
		sqError(w, "NoAvailableOrganizationException", http.StatusForbidden,
			"The Amazon Web Services account making this call is not a member of an organization.")
		return org, false
	}
	if org.FeatureSet != "ALL" {
		sqError(w, "OrganizationNotInAllFeaturesModeException", http.StatusBadRequest,
			"The organization that your Amazon Web Services account belongs to is not in All Features mode.")
		return org, false
	}
	if iamCallerAccount(r) != org.MasterAccountId {
		sqError(w, "DependencyAccessDeniedException", http.StatusForbidden,
			"You can't perform this action because you are not the management account of your organization.")
		return org, false
	}
	if _, enabled := orgServiceAccess.Get(sqTrustedPrincipal); !enabled {
		sqError(w, "AWSServiceAccessNotEnabledException", http.StatusForbidden,
			"The action you attempted is not allowed unless Service Access with Service Quotas is enabled in your organization.")
		return org, false
	}
	return org, true
}

func sqTemplateKey(region, service, code string) string { return region + "|" + service + "|" + code }

func sqTemplateJSON(e sqTemplateEntry) map[string]any {
	q := sqCatalogIndex[e.ServiceCode+"/"+e.QuotaCode]
	return map[string]any{
		"ServiceCode":  q.ServiceCode,
		"ServiceName":  q.ServiceName,
		"QuotaCode":    q.QuotaCode,
		"QuotaName":    q.QuotaName,
		"DesiredValue": cwJSONStat(e.DesiredValue),
		"AwsRegion":    e.AwsRegion,
		"Unit":         q.Unit,
		"GlobalQuota":  q.Global,
	}
}

var sqRegionPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{1,128}$`)

func handleSQPutServiceQuotaIncreaseRequestIntoTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuotaCode    string   `json:"QuotaCode"`
		ServiceCode  string   `json:"ServiceCode"`
		AwsRegion    string   `json:"AwsRegion"`
		DesiredValue *float64 `json:"DesiredValue"`
	}
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"quotaCode": req.QuotaCode, "serviceCode": req.ServiceCode, "awsRegion": req.AwsRegion}) {
		return
	}
	if req.DesiredValue == nil {
		sqIllegal(w, "1 validation error detected: Value null at 'desiredValue' failed to satisfy constraint: Member must not be null")
		return
	}
	if !sqRegionPattern.MatchString(req.AwsRegion) {
		sqIllegal(w, "Invalid Region %s.", req.AwsRegion)
		return
	}
	if _, ok := sqTemplateOrg(w, r); !ok {
		return
	}
	q, ok := sqFind(w, req.ServiceCode, req.QuotaCode)
	if !ok {
		return
	}
	if *req.DesiredValue <= sqQuotaDefault(q, req.AwsRegion) {
		sqIllegal(w, "The requested value %s must be greater than the default value %s.", cwFormatFloat(*req.DesiredValue), cwFormatFloat(sqQuotaDefault(q, req.AwsRegion)))
		return
	}
	key := sqTemplateKey(req.AwsRegion, q.ServiceCode, q.QuotaCode)
	if _, exists := sqTemplate.Get(key); !exists && sqTemplate.Len() >= sqTemplateMaxRequests {
		sqError(w, "QuotaExceededException", http.StatusConflict,
			"You have exceeded your service quota. A quota request template holds at most %d quota increase requests.", sqTemplateMaxRequests)
		return
	}
	entry := sqTemplateEntry{ServiceCode: q.ServiceCode, QuotaCode: q.QuotaCode, AwsRegion: req.AwsRegion, DesiredValue: *req.DesiredValue}
	sqTemplate.Put(key, entry)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ServiceQuotaIncreaseRequestInTemplate": sqTemplateJSON(entry)})
}

func sqTemplateEntryRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct{ ServiceCode, QuotaCode, AwsRegion string }
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"quotaCode": req.QuotaCode, "serviceCode": req.ServiceCode, "awsRegion": req.AwsRegion}) {
		return "", false
	}
	if _, ok := sqTemplateOrg(w, r); !ok {
		return "", false
	}
	if _, ok := sqFind(w, req.ServiceCode, req.QuotaCode); !ok {
		return "", false
	}
	key := sqTemplateKey(req.AwsRegion, req.ServiceCode, req.QuotaCode)
	if _, ok := sqTemplate.Get(key); !ok {
		sqNoSuchResource(w, "The quota request template doesn't contain a request for quota %s in %s.", req.QuotaCode, req.AwsRegion)
		return "", false
	}
	return key, true
}

func handleSQGetServiceQuotaIncreaseRequestFromTemplate(w http.ResponseWriter, r *http.Request) {
	key, ok := sqTemplateEntryRequest(w, r)
	if !ok {
		return
	}
	entry, _ := sqTemplate.Get(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ServiceQuotaIncreaseRequestInTemplate": sqTemplateJSON(entry)})
}

func handleSQDeleteServiceQuotaIncreaseRequestFromTemplate(w http.ResponseWriter, r *http.Request) {
	key, ok := sqTemplateEntryRequest(w, r)
	if !ok {
		return
	}
	sqTemplate.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQListServiceQuotaIncreaseRequestsInTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceCode string `json:"ServiceCode"`
		AwsRegion   string `json:"AwsRegion"`
		NextToken   string `json:"NextToken"`
		MaxResults  *int32 `json:"MaxResults"`
	}
	if !sqRead(w, r, &req) || !sqCheckMaxResults(w, req.MaxResults, 100) {
		return
	}
	if _, ok := sqTemplateOrg(w, r); !ok {
		return
	}
	var entries []sqTemplateEntry
	for _, e := range sqTemplate.List() {
		if req.ServiceCode != "" && e.ServiceCode != req.ServiceCode || req.AwsRegion != "" && e.AwsRegion != req.AwsRegion {
			continue
		}
		entries = append(entries, e)
	}
	page, next, ok := awsKeyPage(w, sqBadToken, entries, func(e sqTemplateEntry) string {
		return sqTemplateKey(e.AwsRegion, e.ServiceCode, e.QuotaCode)
	}, req.NextToken, awsMaxResults(req.MaxResults), 100)
	if !ok {
		return
	}
	items := make([]map[string]any, 0, len(page))
	for _, e := range page {
		items = append(items, sqTemplateJSON(e))
	}
	out := map[string]any{"ServiceQuotaIncreaseRequestInTemplateList": items}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSQAssociateServiceQuotaTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := sqTemplateOrg(w, r); !ok {
		return
	}
	sqAssociation.Put(orgSingletonKey, true)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQDisassociateServiceQuotaTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := sqTemplateOrg(w, r); !ok {
		return
	}
	if associated, _ := sqAssociation.Get(orgSingletonKey); !associated {
		sqError(w, "ServiceQuotaTemplateNotInUseException", http.StatusBadRequest,
			"The quota request template is not associated with your organization.")
		return
	}
	sqAssociation.Delete(orgSingletonKey)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQGetAssociationForServiceQuotaTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := sqTemplateOrg(w, r); !ok {
		return
	}
	status := "DISASSOCIATED"
	if associated, _ := sqAssociation.Get(orgSingletonKey); associated {
		status = "ASSOCIATED"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ServiceQuotaTemplateAssociationStatus": status})
}

// sqApplyTemplate requests the template's increases for an account the
// organization created while the template is associated.
func sqApplyTemplate(account string) {
	if sqAssociation == nil {
		return
	}
	if associated, _ := sqAssociation.Get(orgSingletonKey); !associated {
		return
	}
	requester := fmt.Sprintf(`{"accountId":"%s","callerArn":"arn:aws:iam::%s:root"}`, account, account)
	for _, e := range sqTemplate.List() {
		q, ok := sqCatalogIndex[e.ServiceCode+"/"+e.QuotaCode]
		if !ok || e.DesiredValue <= sqAppliedQuota(account, e.AwsRegion, q) {
			continue
		}
		sqSubmit(account, e.AwsRegion, q, e.DesiredValue, requester, true, "")
	}
}

func sqParseQuotaARN(arn string) (sqCatalogQuota, bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "servicequotas" {
		return sqCatalogQuota{}, false
	}
	service, code, ok := strings.Cut(parts[5], "/")
	if !ok {
		return sqCatalogQuota{}, false
	}
	q, found := sqCatalogIndex[service+"/"+code]
	return q, found
}

func sqTaggedQuota(w http.ResponseWriter, r *http.Request, arn string) bool {
	parts := strings.SplitN(arn, ":", 6)
	if _, ok := sqParseQuotaARN(arn); !ok || parts[4] != iamCallerAccount(r) {
		sqNoSuchResource(w, "The specified resource %s does not exist.", arn)
		return false
	}
	return true
}

func handleSQTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
		Tags        []struct{ Key, Value string }
	}
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"resourceARN": req.ResourceARN}) {
		return
	}
	if len(req.Tags) == 0 {
		sqIllegal(w, "1 validation error detected: Value '[]' at 'tags' failed to satisfy constraint: Member must have length greater than or equal to 1")
		return
	}
	if !sqTaggedQuota(w, r, req.ResourceARN) {
		return
	}
	for _, t := range req.Tags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			sqIllegal(w, "Invalid tag %q.", t.Key)
			return
		}
		if strings.HasPrefix(strings.ToLower(t.Key), "aws:") {
			sqIllegal(w, "Tag keys beginning with aws: are reserved for system use.")
			return
		}
	}
	existing, _ := sqTags.Get(req.ResourceARN)
	merged := map[string]string{}
	for k, v := range existing {
		merged[k] = v
	}
	for _, t := range req.Tags {
		merged[t.Key] = t.Value
	}
	if len(merged) > sqMaxTags {
		sqError(w, "TooManyTagsException", http.StatusBadRequest, "A resource can have at most %d tags.", sqMaxTags)
		return
	}
	sqTags.Put(req.ResourceARN, merged)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"resourceARN": req.ResourceARN}) {
		return
	}
	if !sqTaggedQuota(w, r, req.ResourceARN) {
		return
	}
	sqTags.Upsert(req.ResourceARN, func(tags *map[string]string) {
		for _, k := range req.TagKeys {
			delete(*tags, k)
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"resourceARN": req.ResourceARN}) {
		return
	}
	if !sqTaggedQuota(w, r, req.ResourceARN) {
		return
	}
	tags, _ := sqTags.Get(req.ResourceARN)
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]string{"Key": k, "Value": tags[k]})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Tags": out})
}

func sqExclusions(w http.ResponseWriter, raw map[string][]string) (map[string][]string, bool) {
	for service, codes := range raw {
		if !sqServiceExists(service) {
			sqNoSuchResource(w, "The request failed because the specified service does not exist.")
			return nil, false
		}
		for _, code := range codes {
			if _, ok := sqCatalogIndex[service+"/"+code]; !ok {
				sqNoSuchResource(w, "The request failed because the specified quota does not exist.")
				return nil, false
			}
		}
	}
	return raw, true
}

func sqCheckOptInType(w http.ResponseWriter, optInType string) bool {
	switch optInType {
	case "NotifyOnly":
		return true
	case "NotifyAndAdjust":
		sqIllegal(w, "Only the NotifyOnly opt-in type is available.")
	default:
		sqIllegal(w, "1 validation error detected: Value '%s' at 'optInType' failed to satisfy constraint: Member must satisfy enum value set: [NotifyOnly, NotifyAndAdjust]", optInType)
	}
	return false
}

func handleSQStartAutoManagement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OptInLevel      string              `json:"OptInLevel"`
		OptInType       string              `json:"OptInType"`
		NotificationArn string              `json:"NotificationArn"`
		ExclusionList   map[string][]string `json:"ExclusionList"`
	}
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"optInLevel": req.OptInLevel, "optInType": req.OptInType}) {
		return
	}
	if req.OptInLevel != "ACCOUNT" {
		sqIllegal(w, "1 validation error detected: Value '%s' at 'optInLevel' failed to satisfy constraint: Member must satisfy enum value set: [ACCOUNT]", req.OptInLevel)
		return
	}
	if !sqCheckOptInType(w, req.OptInType) {
		return
	}
	exclusions, ok := sqExclusions(w, req.ExclusionList)
	if !ok {
		return
	}
	sqAutoManagement.Put(iamCallerAccount(r), sqAutoConfig{OptInLevel: req.OptInLevel, OptInType: req.OptInType,
		NotificationArn: req.NotificationArn, ExclusionList: exclusions})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQUpdateAutoManagement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OptInType       string              `json:"OptInType"`
		NotificationArn *string             `json:"NotificationArn"`
		ExclusionList   map[string][]string `json:"ExclusionList"`
	}
	if !sqRead(w, r, &req) {
		return
	}
	account := iamCallerAccount(r)
	config, ok := sqAutoManagement.Get(account)
	if !ok {
		sqNoSuchResource(w, "Automatic Management is not started for this account.")
		return
	}
	if req.OptInType != "" {
		if !sqCheckOptInType(w, req.OptInType) {
			return
		}
		config.OptInType = req.OptInType
	}
	if req.NotificationArn != nil {
		config.NotificationArn = *req.NotificationArn
	}
	if req.ExclusionList != nil {
		exclusions, ok := sqExclusions(w, req.ExclusionList)
		if !ok {
			return
		}
		config.ExclusionList = exclusions
	}
	sqAutoManagement.Put(account, config)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQStopAutoManagement(w http.ResponseWriter, r *http.Request) {
	if !sqAutoManagement.Delete(iamCallerAccount(r)) {
		sqNoSuchResource(w, "Automatic Management is not started for this account.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQGetAutoManagementConfiguration(w http.ResponseWriter, r *http.Request) {
	config, ok := sqAutoManagement.Get(iamCallerAccount(r))
	if !ok {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"OptInStatus": "DISABLED"})
		return
	}
	excluded := map[string][]map[string]string{}
	for service, codes := range config.ExclusionList {
		items := []map[string]string{}
		for _, code := range codes {
			items = append(items, map[string]string{"QuotaCode": code, "QuotaName": sqCatalogIndex[service+"/"+code].QuotaName})
		}
		excluded[service] = items
	}
	out := map[string]any{
		"OptInLevel":    config.OptInLevel,
		"OptInType":     config.OptInType,
		"OptInStatus":   "ENABLED",
		"ExclusionList": excluded,
	}
	if config.NotificationArn != "" {
		out["NotificationArn"] = config.NotificationArn
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// sqUtilization measures every quota with a usage metric: its usage over the
// last hour by the metric's recommended statistic, against its applied value.
func sqUtilization(account, region string, now time.Time) []sqReportQuota {
	var out []sqReportQuota
	for _, q := range sqCatalog {
		m, ok := sqUsageMetric(q)
		if !ok {
			continue
		}
		applied := sqAppliedQuota(account, region, q)
		usage := awsUsageObserved(m, q.PeriodUnit == "SECOND", now.Add(-time.Hour), now)
		utilization := 0.0
		if applied > 0 {
			utilization = usage / applied * 100
		}
		out = append(out, sqReportQuota{
			QuotaCode: q.QuotaCode, ServiceCode: q.ServiceCode, QuotaName: q.QuotaName, ServiceName: q.ServiceName,
			Namespace: awsUsageNamespace, Utilization: utilization, DefaultValue: sqQuotaDefault(q, region),
			AppliedValue: applied, Adjustable: true,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Utilization != out[j].Utilization {
			return out[i].Utilization > out[j].Utilization
		}
		return out[i].ServiceCode+"/"+out[i].QuotaCode < out[j].ServiceCode+"/"+out[j].QuotaCode
	})
	return out
}

func handleSQStartQuotaUtilizationReport(w http.ResponseWriter, r *http.Request) {
	account, region := iamCallerAccount(r), sqRegion(r)
	now := time.Now()
	sqReports.Prune(func(rep sqReport) bool { return now.Sub(time.Unix(int64(rep.Created), 0)) > sqReportLifetime })
	report := sqReport{ReportId: sim.NewUUID(), Account: account, Status: "PENDING", Created: sqNow()}
	sqReports.Put(report.ReportId, report)
	quotas := sqUtilization(account, region, now)
	sqReports.Update(report.ReportId, func(stored *sqReport) {
		stored.Quotas = quotas
		stored.Status = "COMPLETED"
		stored.GeneratedAt = sqNow()
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ReportId": report.ReportId, "Status": report.Status})
}

func handleSQGetQuotaUtilizationReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReportId   string `json:"ReportId"`
		NextToken  string `json:"NextToken"`
		MaxResults *int32 `json:"MaxResults"`
	}
	if !sqRead(w, r, &req) || !sqRequired(w, map[string]string{"reportId": req.ReportId}) || !sqCheckMaxResults(w, req.MaxResults, 1000) {
		return
	}
	report, ok := sqReports.Get(req.ReportId)
	if !ok || report.Account != iamCallerAccount(r) || time.Since(time.Unix(int64(report.Created), 0)) > sqReportLifetime {
		sqNoSuchResource(w, "The report %s does not exist.", req.ReportId)
		return
	}
	page, next, ok := awsSeqPage(w, sqBadToken, report.Quotas, false, req.NextToken, awsMaxResults(req.MaxResults), 1000)
	if !ok {
		return
	}
	items := make([]map[string]any, 0, len(page))
	for _, q := range page {
		items = append(items, map[string]any{
			"QuotaCode": q.QuotaCode, "ServiceCode": q.ServiceCode, "QuotaName": q.QuotaName, "ServiceName": q.ServiceName,
			"Namespace": q.Namespace, "Utilization": cwJSONStat(q.Utilization), "DefaultValue": cwJSONStat(q.DefaultValue),
			"AppliedValue": cwJSONStat(q.AppliedValue), "Adjustable": q.Adjustable,
		})
	}
	out := map[string]any{"ReportId": report.ReportId, "Status": report.Status, "TotalCount": len(report.Quotas), "Quotas": items}
	if report.GeneratedAt > 0 {
		out["GeneratedAt"] = report.GeneratedAt
	}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

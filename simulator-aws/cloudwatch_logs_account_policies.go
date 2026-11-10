package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Keyed by policyType+":"+policyName; a name is unique per policy type.
type CWAccountPolicy struct {
	PolicyName        string `json:"policyName"`
	PolicyDocument    string `json:"policyDocument"`
	LastUpdatedTime   int64  `json:"lastUpdatedTime"`
	PolicyType        string `json:"policyType"`
	Scope             string `json:"scope,omitempty"`
	SelectionCriteria string `json:"selectionCriteria,omitempty"`
	AccountId         string `json:"accountId,omitempty"`
	CreatedTime       int64  `json:"createdTime"`
	// DisabledUntil is when a subscription filter policy whose destination
	// refused a delivery resumes forwarding.
	DisabledUntil int64 `json:"disabledUntil,omitempty"`
	// A retention policy's document and selection take effect EffectiveFrom;
	// until then the ones it replaced, if any, stay in effect.
	EffectiveFrom     int64  `json:"effectiveFrom,omitempty"`
	PreviousInEffect  bool   `json:"previousInEffect,omitempty"`
	PreviousDocument  string `json:"previousDocument,omitempty"`
	PreviousSelection string `json:"previousSelection,omitempty"`
}

type cwAccountPolicyWire struct {
	PolicyName        string `json:"policyName"`
	PolicyDocument    string `json:"policyDocument"`
	LastUpdatedTime   int64  `json:"lastUpdatedTime"`
	PolicyType        string `json:"policyType"`
	Scope             string `json:"scope,omitempty"`
	SelectionCriteria string `json:"selectionCriteria,omitempty"`
	AccountId         string `json:"accountId,omitempty"`
}

func (p CWAccountPolicy) wire() cwAccountPolicyWire {
	return cwAccountPolicyWire{
		PolicyName:        p.PolicyName,
		PolicyDocument:    p.PolicyDocument,
		LastUpdatedTime:   p.LastUpdatedTime,
		PolicyType:        p.PolicyType,
		Scope:             p.Scope,
		SelectionCriteria: p.SelectionCriteria,
		AccountId:         p.AccountId,
	}
}

const (
	cwPolicyDataProtection   = "DATA_PROTECTION_POLICY"
	cwPolicySubscription     = "SUBSCRIPTION_FILTER_POLICY"
	cwPolicyFieldIndex       = "FIELD_INDEX_POLICY"
	cwPolicyTransformer      = "TRANSFORMER_POLICY"
	cwPolicyMetricExtraction = "METRIC_EXTRACTION_POLICY"
	cwPolicyRetention        = "RETENTION_POLICY"

	cwSelectionCriteriaMaxBytes = 25600
	cwAccountTransformersMax    = 20
	cwAccountRetentionMax       = 20
	cwAccountFieldIndexMax      = 20
	cwRetentionPolicyDelay      = 24 * time.Hour
)

func cwAccountPolicyKey(policyType, name string) string { return policyType + ":" + name }

func cwAccountPoliciesOf(policyType string) []CWAccountPolicy {
	rows := cwAccountPolicies.ListPrefix(policyType + ":")
	out := make([]CWAccountPolicy, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedTime < out[j].CreatedTime })
	return out
}

// The API reference lists the selectionCriteria filter each policy type
// supports: LogGroupName NOT IN [] for subscription filters, LogGroupNamePrefix
// for transformers and retention, LogGroupNamePrefix or DataSourceName AND
// DataSourceType for field indexes, and LogGroupName or LogGroupNamePrefix
// under IN or NOT IN for metric extraction.
func cwParsePolicySelection(policyType, criteria string) (*cwSelection, error) {
	if criteria == "" {
		return nil, nil
	}
	if len(criteria) > cwSelectionCriteriaMaxBytes {
		return nil, fmt.Errorf("selectionCriteria must be at most %d bytes", cwSelectionCriteriaMaxBytes)
	}
	refuse := func(supported string) error {
		return cwtInvalid("Invalid selectionCriteria for %s: the only supported filter is %s", policyType, supported)
	}
	switch policyType {
	case cwPolicySubscription:
		sel, err := cwParseSelection(criteria, "LogGroupName")
		if err != nil || sel.op != "" || sel.cmp != "NOT IN" {
			return nil, refuse("LogGroupName NOT IN []")
		}
		return sel, nil
	case cwPolicyTransformer, cwPolicyRetention:
		sel, err := cwParseSelection(criteria, "LogGroupNamePrefix")
		if err != nil || sel.op != "" || (sel.cmp != "=" && sel.cmp != "IN") {
			return nil, refuse("LogGroupNamePrefix")
		}
		return sel, nil
	case cwPolicyFieldIndex:
		sel, err := cwParseSelection(criteria, "LogGroupNamePrefix", "DataSourceName", "DataSourceType")
		if err != nil {
			return nil, refuse("LogGroupNamePrefix, or DataSourceName AND DataSourceType")
		}
		conds := sel.conditions()
		switch {
		case len(conds) == 1 && conds[0].field == "LogGroupNamePrefix" && (conds[0].cmp == "=" || conds[0].cmp == "IN"):
		case len(conds) == 2 && sel.op == "AND" && conds[0].cmp == "=" && conds[1].cmp == "=" &&
			((conds[0].field == "DataSourceName" && conds[1].field == "DataSourceType") ||
				(conds[0].field == "DataSourceType" && conds[1].field == "DataSourceName")):
		default:
			return nil, refuse("LogGroupNamePrefix, or DataSourceName AND DataSourceType")
		}
		return sel, nil
	case cwPolicyMetricExtraction:
		sel, err := cwParseSelection(criteria, "LogGroupName", "LogGroupNamePrefix")
		if err != nil || sel.op != "" || (sel.cmp != "IN" && sel.cmp != "NOT IN") {
			return nil, refuse("LogGroupName or LogGroupNamePrefix with IN or NOT IN")
		}
		if len(sel.values) > 50 {
			return nil, cwtInvalid("A metric extraction policy's selectionCriteria takes at most 50 values")
		}
		return sel, nil
	}
	return nil, fmt.Errorf("selectionCriteria is valid only for SUBSCRIPTION_FILTER_POLICY, FIELD_INDEX_POLICY, TRANSFORMER_POLICY, METRIC_EXTRACTION_POLICY and RETENTION_POLICY")
}

// cwPolicySelects reports whether a stored policy's selection admits group.
func cwPolicySelects(policyType, criteria, group string) bool {
	sel, err := cwParsePolicySelection(policyType, criteria)
	if err != nil {
		return false
	}
	return sel == nil || sel.match(map[string]string{"LogGroupName": group})
}

// The prefixes a log group name selection scopes a policy to; a policy with
// no selection covers every name, the empty prefix.
func cwPolicyPrefixes(policyType, criteria string) []string {
	sel, err := cwParsePolicySelection(policyType, criteria)
	if err != nil || sel == nil {
		return []string{""}
	}
	if sel.field != "LogGroupNamePrefix" {
		return nil
	}
	return sel.values
}

// cwPolicyDataSource returns the data source a field index policy selects
// with DataSourceName AND DataSourceType.
func cwPolicyDataSource(policyType, criteria string) (cwDataSource, bool) {
	sel, err := cwParsePolicySelection(policyType, criteria)
	if err != nil || sel == nil {
		return cwDataSource{}, false
	}
	var ds cwDataSource
	for _, c := range sel.conditions() {
		switch {
		case c.field == "DataSourceName" && len(c.values) == 1:
			ds.Name = c.values[0]
		case c.field == "DataSourceType" && len(c.values) == 1:
			ds.Type = c.values[0]
		}
	}
	return ds, ds.Name != "" && ds.Type != ""
}

type cwSubscriptionPolicyDocument struct {
	DestinationArn string `json:"DestinationArn"`
	RoleArn        string `json:"RoleArn"`
	FilterPattern  string `json:"FilterPattern"`
	Distribution   string `json:"Distribution"`
}

// The API reference's example sends a transformer policy's document as the
// array of processors.
func cwParseTransformerPolicyDocument(doc string) ([]cwtProcessor, error) {
	var procs []cwtProcessor
	if err := json.Unmarshal([]byte(doc), &procs); err != nil {
		return nil, cwtInvalid("A transformer policy document must be a JSON array of processors: %v", err)
	}
	if _, err := cwCompileTransformer(procs); err != nil {
		return nil, err
	}
	return procs, nil
}

// cwRetention is an effective retention period in days; zero never expires.
type cwRetention int

func (r cwRetention) longer(o cwRetention) bool {
	if r == 0 || o == 0 {
		return r == 0 && o != 0
	}
	return r > o
}

func (r cwRetention) String() string {
	if r == 0 {
		return "NEVER_EXPIRE"
	}
	return strconv.Itoa(int(r))
}

type cwRetentionPolicyDocument struct {
	Days            cwRetention
	EnforcementMode string
}

var cwAccountRetentionDays = map[int]bool{
	30: true, 60: true, 90: true, 120: true, 150: true, 180: true, 365: true, 400: true, 545: true, 731: true,
	1096: true, 1827: true, 2192: true, 2557: true, 2922: true, 3288: true, 3653: true,
}

func cwParseRetentionPolicyDocument(doc string) (cwRetentionPolicyDocument, error) {
	var raw struct {
		RetentionInDays json.RawMessage `json:"retentionInDays"`
		EnforcementMode string          `json:"enforcementMode"`
	}
	if err := json.Unmarshal([]byte(doc), &raw); err != nil {
		return cwRetentionPolicyDocument{}, cwtInvalid("Invalid retention policy document: %v", err)
	}
	var out cwRetentionPolicyDocument
	var days int
	var never string
	switch {
	case len(raw.RetentionInDays) == 0:
		return out, cwtInvalid("A retention policy document must include retentionInDays")
	case json.Unmarshal(raw.RetentionInDays, &never) == nil && never == "NEVER_EXPIRE":
		out.Days = 0
	case json.Unmarshal(raw.RetentionInDays, &days) == nil && cwAccountRetentionDays[days]:
		out.Days = cwRetention(days)
	default:
		return out, cwtInvalid("Invalid retentionInDays %s: valid values are 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653 and NEVER_EXPIRE", raw.RetentionInDays)
	}
	if raw.EnforcementMode != "NEW_ONLY" && raw.EnforcementMode != "RETROACTIVE" {
		return out, cwtInvalid("Invalid enforcementMode %q: valid values are NEW_ONLY and RETROACTIVE", raw.EnforcementMode)
	}
	out.EnforcementMode = raw.EnforcementMode
	return out, nil
}

// cwRetentionVersion is a retention policy as it applies at one moment.
type cwRetentionVersion struct {
	Name      string
	Created   int64
	Document  cwRetentionPolicyDocument
	Selection string
}

func (v cwRetentionVersion) applies(g CWLogGroup) bool {
	if v.Document.EnforcementMode == "NEW_ONLY" && g.CreationTime < v.Created {
		return false
	}
	return cwPolicySelects(cwPolicyRetention, v.Selection, g.LogGroupName)
}

// documentAt is the document and selection of p in effect at now, if any.
func (p CWAccountPolicy) documentAt(now time.Time) (bool, string, string) {
	if now.UnixMilli() < p.EffectiveFrom {
		return p.PreviousInEffect, p.PreviousDocument, p.PreviousSelection
	}
	return true, p.PolicyDocument, p.SelectionCriteria
}

// inEffect is the version of p in effect at now, if any.
func (p CWAccountPolicy) inEffect(now time.Time) (cwRetentionVersion, bool) {
	ok, doc, selection := p.documentAt(now)
	if !ok {
		return cwRetentionVersion{}, false
	}
	parsed, err := cwParseRetentionPolicyDocument(doc)
	if err != nil {
		return cwRetentionVersion{}, false
	}
	return cwRetentionVersion{Name: p.PolicyName, Created: p.CreatedTime, Document: parsed, Selection: selection}, true
}

// cwEffectiveRetention is the retention that governs g: a Delivery group's
// fixed two days, else the longest account-level retention policy that
// applies (the first created among equals), else the group's own setting.
func cwEffectiveRetention(g CWLogGroup, versions []cwRetentionVersion) (cwRetention, string) {
	if g.class() == cwClassDelivery {
		return cwRetention(cwDeliveryRetentionDays), ""
	}
	var best *cwRetentionVersion
	for i := range versions {
		v := &versions[i]
		if !v.applies(g) {
			continue
		}
		if best == nil || v.Document.Days.longer(best.Document.Days) {
			best = v
		}
	}
	if best == nil {
		return cwRetention(g.RetentionInDays), ""
	}
	return best.Document.Days, best.Name
}

func cwRetentionVersionsAt(now time.Time) []cwRetentionVersion {
	var out []cwRetentionVersion
	for _, p := range cwAccountPoliciesOf(cwPolicyRetention) {
		if v, ok := p.inEffect(now); ok {
			out = append(out, v)
		}
	}
	return out
}

type cwLogGroupImpact struct {
	CurrentRetention  string `json:"currentRetention"`
	LogGroupName      string `json:"logGroupName"`
	ProposedRetention string `json:"proposedRetention"`
	StoredBytes       int64  `json:"storedBytes"`
}

type cwDryRunResult struct {
	DecreasedRetentionLogGroupCount int                `json:"decreasedRetentionLogGroupCount"`
	FailedAnalysisCount             int                `json:"failedAnalysisCount"`
	IncreasedRetentionLogGroupCount int                `json:"increasedRetentionLogGroupCount"`
	TopLogGroupsImpacted            []cwLogGroupImpact `json:"topLogGroupsImpacted"`
	TotalMatchedLogGroups           int                `json:"totalMatchedLogGroups"`
	UnchangedRetentionLogGroupCount int                `json:"unchangedRetentionLogGroupCount"`
}

// cwRetentionDryRun compares, over the log groups in the scope of a policy,
// the retention in effect now with the retention once the proposed versions
// are; a dry run reports the impact as if the change were already in effect.
func cwRetentionDryRun(scope func(CWLogGroup) bool, proposed []cwRetentionVersion) cwDryRunResult {
	now := time.Now()
	current := cwRetentionVersionsAt(now)
	result := cwDryRunResult{TopLogGroupsImpacted: []cwLogGroupImpact{}}
	var impacts []cwLogGroupImpact
	for _, g := range cwLogGroups.List() {
		if !scope(g) {
			continue
		}
		before, _ := cwEffectiveRetention(g, current)
		after, _ := cwEffectiveRetention(g, proposed)
		switch {
		case after.longer(before):
			result.IncreasedRetentionLogGroupCount++
		case before.longer(after):
			result.DecreasedRetentionLogGroupCount++
		default:
			result.UnchangedRetentionLogGroupCount++
		}
		impacts = append(impacts, cwLogGroupImpact{
			CurrentRetention:  before.String(),
			LogGroupName:      g.LogGroupName,
			ProposedRetention: after.String(),
			StoredBytes:       g.StoredBytes,
		})
	}
	result.TotalMatchedLogGroups = len(impacts)
	sort.SliceStable(impacts, func(i, j int) bool { return impacts[i].StoredBytes > impacts[j].StoredBytes })
	if len(impacts) > 20 {
		impacts = impacts[:20]
	}
	if impacts != nil {
		result.TopLogGroupsImpacted = impacts
	}
	return result
}

func cwPrefixesOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.HasPrefix(x, y) || strings.HasPrefix(y, x) {
				return true
			}
		}
	}
	return false
}

func handleCWPutAccountPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyName        string `json:"policyName"`
		PolicyDocument    string `json:"policyDocument"`
		PolicyType        string `json:"policyType"`
		Scope             string `json:"scope"`
		SelectionCriteria string `json:"selectionCriteria"`
		DryRun            bool   `json:"dryRun"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PolicyName == "" || req.PolicyType == "" || req.PolicyDocument == "" {
		AWSError(w, "InvalidParameterException", "policyName, policyType and policyDocument are required", http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(req.PolicyName, "aws/") {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest, "A policy name cannot start with aws/: %s", req.PolicyName)
		return
	}
	if req.DryRun && req.PolicyType != cwPolicyRetention {
		AWSError(w, "InvalidParameterException", "dryRun is valid only for RETENTION_POLICY", http.StatusBadRequest)
		return
	}
	if req.Scope == "" {
		req.Scope = "ALL"
	}
	if _, err := cwParsePolicySelection(req.PolicyType, req.SelectionCriteria); err != nil {
		AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
		return
	}
	key := cwAccountPolicyKey(req.PolicyType, req.PolicyName)
	existing, exists := cwAccountPolicies.Get(key)
	others := []CWAccountPolicy{}
	for _, p := range cwAccountPoliciesOf(req.PolicyType) {
		if p.PolicyName != req.PolicyName {
			others = append(others, p)
		}
	}
	limit := func(format string, args ...any) {
		AWSErrorf(w, "LimitExceededException", http.StatusBadRequest, format, args...)
	}
	switch req.PolicyType {
	case cwPolicyDataProtection:
		if len(others) > 0 {
			limit("An account can have only one account-level data protection policy: %s", others[0].PolicyName)
			return
		}
		compiled, err := cwCompileDataProtection(req.PolicyDocument)
		if err != nil {
			AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
			return
		}
		if !cwPrepareFindingsDestinations(w, r, compiled) {
			return
		}
	case cwPolicySubscription:
		if len(others) > 0 {
			limit("An account can have one account-level subscription filter policy per Region: %s", others[0].PolicyName)
			return
		}
		var doc cwSubscriptionPolicyDocument
		if err := json.Unmarshal([]byte(req.PolicyDocument), &doc); err != nil {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest, "Invalid subscription filter policy document: %v", err)
			return
		}
		if doc.DestinationArn == "" {
			AWSError(w, "InvalidParameterException", "A subscription filter policy document must include DestinationArn", http.StatusBadRequest)
			return
		}
		if doc.Distribution != "" && doc.Distribution != "Random" && doc.Distribution != "ByLogStream" {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest, "Invalid Distribution: %s", doc.Distribution)
			return
		}
		if _, err := cwCompileLogPattern(doc.FilterPattern); err != nil {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest, "Invalid filter pattern: %s", err.Error())
			return
		}
		target, refused := cwResolveSubscriptionTarget(doc.DestinationArn, doc.RoleArn)
		if refused == nil {
			refused = cwCheckSubscriptionTarget(r.Context(), cwLogGroupArn("*"), target)
		}
		if refused != nil {
			cwWriteRefusal(w, refused)
			return
		}
	case cwPolicyTransformer:
		if _, err := cwParseTransformerPolicyDocument(req.PolicyDocument); err != nil {
			cwWriteTransformerConfigError(w, err)
			return
		}
		if len(others) >= cwAccountTransformersMax {
			limit("An account can have at most %d account-level transformer policies", cwAccountTransformersMax)
			return
		}
		mine := cwPolicyPrefixes(req.PolicyType, req.SelectionCriteria)
		for _, p := range others {
			if cwPrefixesOverlap(mine, cwPolicyPrefixes(p.PolicyType, p.SelectionCriteria)) {
				AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
					"The selection criteria overlap with transformer policy %s", p.PolicyName)
				return
			}
		}
	case cwPolicyFieldIndex:
		if _, err := cwParseIndexPolicyDocument(req.PolicyDocument); err != nil {
			AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
			return
		}
		mine := cwPolicyPrefixes(req.PolicyType, req.SelectionCriteria)
		same := 0
		for _, p := range others {
			theirs := cwPolicyPrefixes(p.PolicyType, p.SelectionCriteria)
			if (mine == nil) != (theirs == nil) {
				continue
			}
			same++
			if mine != nil && cwPrefixesOverlap(mine, theirs) {
				AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
					"The selection criteria overlap with field index policy %s", p.PolicyName)
				return
			}
			mineSource, _ := cwPolicyDataSource(req.PolicyType, req.SelectionCriteria)
			theirSource, _ := cwPolicyDataSource(p.PolicyType, p.SelectionCriteria)
			if mine == nil && mineSource.key() == theirSource.key() {
				AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
					"Field index policy %s already uses this data source", p.PolicyName)
				return
			}
		}
		if same >= cwAccountFieldIndexMax {
			limit("An account can have at most %d account-level field index policies of this kind", cwAccountFieldIndexMax)
			return
		}
	case cwPolicyMetricExtraction:
		if _, err := cwParseMetricExtractionDocument(req.PolicyDocument); err != nil {
			AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
			return
		}
		if conflict, overLimit := cwMetricExtractionConflict(req.SelectionCriteria, others); conflict != "" {
			if overLimit {
				limit("%s", conflict)
			} else {
				AWSError(w, "InvalidParameterException", conflict, http.StatusBadRequest)
			}
			return
		}
	case cwPolicyRetention:
		parsed, err := cwParseRetentionPolicyDocument(req.PolicyDocument)
		if err != nil {
			AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
			return
		}
		if len(others) >= cwAccountRetentionMax {
			limit("An account can have at most %d account-level retention policies", cwAccountRetentionMax)
			return
		}
		if req.DryRun {
			created := time.Now().UnixMilli()
			if exists {
				created = existing.CreatedTime
			}
			proposed := cwRetentionVersion{Name: req.PolicyName, Created: created, Document: parsed, Selection: req.SelectionCriteria}
			versions := []cwRetentionVersion{proposed}
			for _, p := range others {
				if v, ok := p.inEffect(time.Now().Add(cwRetentionPolicyDelay)); ok {
					versions = append(versions, v)
				}
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"dryRunResult": cwRetentionDryRun(proposed.applies, versions)})
			return
		}
	default:
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest, "Invalid policyType: %s", req.PolicyType)
		return
	}
	now := time.Now()
	p := CWAccountPolicy{
		PolicyName:        req.PolicyName,
		PolicyDocument:    req.PolicyDocument,
		LastUpdatedTime:   now.UnixMilli(),
		PolicyType:        req.PolicyType,
		Scope:             req.Scope,
		SelectionCriteria: req.SelectionCriteria,
		AccountId:         awsAccountID(),
		CreatedTime:       now.UnixMilli(),
	}
	if exists {
		p.CreatedTime = existing.CreatedTime
	}
	if req.PolicyType == cwPolicyRetention {
		p.EffectiveFrom = now.Add(cwRetentionPolicyDelay).UnixMilli()
		if exists {
			p.PreviousInEffect, p.PreviousDocument, p.PreviousSelection = existing.documentAt(now)
		}
	}
	cwAccountPolicies.Put(key, p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"accountPolicy": p.wire()})
}

func handleCWDescribeAccountPolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyType         string   `json:"policyType"`
		PolicyName         string   `json:"policyName"`
		AccountIdentifiers []string `json:"accountIdentifiers"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PolicyType == "" {
		AWSError(w, "InvalidParameterException", "policyType is required", http.StatusBadRequest)
		return
	}
	policies := []cwAccountPolicyWire{}
	for _, p := range cwAccountPoliciesOf(req.PolicyType) {
		if req.PolicyName == "" || p.PolicyName == req.PolicyName {
			policies = append(policies, p.wire())
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"accountPolicies": policies})
}

func handleCWDeleteAccountPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyName string `json:"policyName"`
		PolicyType string `json:"policyType"`
		DryRun     bool   `json:"dryRun"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.DryRun && req.PolicyType != cwPolicyRetention {
		AWSError(w, "InvalidParameterException", "dryRun is valid only for RETENTION_POLICY", http.StatusBadRequest)
		return
	}
	key := cwAccountPolicyKey(req.PolicyType, req.PolicyName)
	existing, ok := cwAccountPolicies.Get(key)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified account policy does not exist: %s", req.PolicyName)
		return
	}
	if req.DryRun {
		now := time.Now()
		var mine cwRetentionVersion
		var versions []cwRetentionVersion
		for _, p := range cwAccountPoliciesOf(cwPolicyRetention) {
			v, in := p.inEffect(now)
			if !in {
				continue
			}
			if p.PolicyName == existing.PolicyName {
				mine = v
				continue
			}
			versions = append(versions, v)
		}
		scope := func(g CWLogGroup) bool { return mine.Name != "" && mine.applies(g) }
		sim.WriteJSON(w, http.StatusOK, map[string]any{"dryRunResult": cwRetentionDryRun(scope, versions)})
		return
	}
	cwAccountPolicies.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

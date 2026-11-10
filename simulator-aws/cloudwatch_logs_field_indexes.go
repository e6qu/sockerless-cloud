package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// Field indexes as the field indexing guide and the PutIndexPolicy,
// PutAccountPolicy, DescribeIndexPolicies and DescribeFieldIndexes API
// references define them. A log group-level policy overrides the
// account-level policies that select log groups; an event is indexed when it
// is ingested and stays indexed for 30 days.

const (
	cwIndexPolicyMaxFields    = 20
	cwIndexFieldMaxChars      = 100
	cwIndexPolicyMaxBytes     = 51200
	cwIndexedEventLifetime    = 30 * 24 * time.Hour
	cwIndexSourceLogGroup     = "LOG_GROUP"
	cwIndexSourceAccount      = "ACCOUNT"
	cwIndexCategoryDefault    = "DEFAULT"
	cwIndexCategoryCustom     = "CUSTOM"
	cwIndexCategoryInactive   = "INACTIVE"
	cwIndexTypeField          = "FIELD_INDEX"
	cwIndexTypeFacet          = "FACET"
	cwDescribeFieldIndexesMax = 100
)

// The generated fields the field index syntax page lets a policy index by
// their own name; any other field starting with @ is spelled with @@.
var cwIndexableGeneratedFields = map[string]bool{
	"@logStream": true, "@ingestionTime": true, "@requestId": true, "@type": true, "@initDuration": true,
	"@duration": true, "@billedDuration": true, "@memorySize": true, "@maxMemoryUsed": true,
	"@xrayTraceId": true, "@xraySegmentId": true,
}

// The default field indexes of every log group in the Standard log class.
var cwDefaultIndexFields = []string{
	"@logStream", "@aws.region", "@aws.account", "@source.log", "@data_source_name", "@data_source_type",
	"@data_format", "traceId", "severityText", "attributes.session.id",
}

var cwIndexFieldName = regexp.MustCompile(`^@{0,2}[\.\-_/#A-Za-z0-9]+$`)

// Keyed by log group name, then field name.
type CWFieldIndexState struct {
	LogGroupName   string `json:"logGroupName"`
	FieldIndexName string `json:"fieldIndexName"`
	Category       string `json:"category"`
	Type           string `json:"type"`
	FirstEventTime int64  `json:"firstEventTime"`
	LastEventTime  int64  `json:"lastEventTime"`
	LastScanTime   int64  `json:"lastScanTime"`
}

var cwFieldIndexStates sim.Store[CWFieldIndexState]

func registerCloudWatchLogsFieldIndexes(srv *sim.Server) {
	cwFieldIndexStates = sim.MakeStore[CWFieldIndexState](srv.DB(), "cw_field_index_states")
}

func cwFieldIndexKey(group, field string) string { return group + "\x00" + field }

// cwParseIndexPolicyDocument returns each field the document indexes and its
// type.
func cwParseIndexPolicyDocument(doc string) (map[string]string, error) {
	if len(doc) > cwIndexPolicyMaxBytes {
		return nil, fmt.Errorf("the index policy document is %d bytes; the limit is %d", len(doc), cwIndexPolicyMaxBytes)
	}
	var parsed struct {
		Fields   []string `json:"Fields"`
		FieldsV2 map[string]struct {
			Type string `json:"type"`
		} `json:"FieldsV2"`
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		return nil, fmt.Errorf("the index policy document is not valid JSON: %v", err)
	}
	fields := map[string]string{}
	add := func(name, typ string) error {
		if _, dup := fields[name]; dup {
			return fmt.Errorf("field %s appears more than once; the names in Fields and FieldsV2 must be mutually exclusive", name)
		}
		if len([]rune(name)) > cwIndexFieldMaxChars {
			return fmt.Errorf("field name %s is longer than %d characters", name, cwIndexFieldMaxChars)
		}
		if !cwIndexFieldName.MatchString(name) {
			return fmt.Errorf("invalid field name: %s", name)
		}
		if strings.HasPrefix(name, "@") && !strings.HasPrefix(name, "@@") && !cwIndexableGeneratedFields[name] {
			return fmt.Errorf("field %s is not a generated field that can be indexed; index a custom field that starts with @ as @%s", name, name)
		}
		fields[name] = typ
		return nil
	}
	for _, f := range parsed.Fields {
		if err := add(f, cwIndexTypeField); err != nil {
			return nil, err
		}
	}
	for name, spec := range parsed.FieldsV2 {
		if spec.Type != cwIndexTypeField && spec.Type != cwIndexTypeFacet {
			return nil, fmt.Errorf("invalid type %q for field %s: valid types are FIELD_INDEX and FACET", spec.Type, name)
		}
		if err := add(name, spec.Type); err != nil {
			return nil, err
		}
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("the index policy document must include at least one field index")
	}
	if len(fields) > cwIndexPolicyMaxFields {
		return nil, fmt.Errorf("an index policy can include at most %d fields", cwIndexPolicyMaxFields)
	}
	return fields, nil
}

// cwIndexPolicyApplying is the policy that indexes group's events: the
// group's own, else the account-level policy whose selection, no criteria or
// a log group name prefix, takes in the group. Account-level policies that
// select a data source apply on top, to the events of that source.
func cwIndexPolicyApplying(group string) (CWIndexPolicy, bool) {
	if p, ok := cwIndexPolicies.Get(group); ok {
		return p, true
	}
	for _, p := range cwAccountPoliciesOf(cwPolicyFieldIndex) {
		if cwPolicyPrefixes(p.PolicyType, p.SelectionCriteria) == nil {
			continue
		}
		if cwPolicySelects(p.PolicyType, p.SelectionCriteria, group) {
			return CWIndexPolicy{
				LogGroupIdentifier: cwLogGroupArn(group),
				LastUpdateTime:     p.LastUpdatedTime,
				PolicyDocument:     p.PolicyDocument,
				PolicyName:         p.PolicyName,
				Source:             cwIndexSourceAccount,
			}, true
		}
	}
	return CWIndexPolicy{}, false
}

// cwIndexedFieldsOf is the custom fields indexed in group's events of
// source: the log group-level selection's, and those of the account-level
// policy that selects the data source.
func cwIndexedFieldsOf(group string, source cwDataSource) map[string]string {
	fields := map[string]string{}
	if p, ok := cwIndexPolicyApplying(group); ok {
		if parsed, err := cwParseIndexPolicyDocument(p.PolicyDocument); err == nil {
			fields = parsed
		}
	}
	for _, p := range cwAccountPoliciesOf(cwPolicyFieldIndex) {
		selected, ok := cwPolicyDataSource(p.PolicyType, p.SelectionCriteria)
		if !ok || selected.key() != source.key() {
			continue
		}
		parsed, err := cwParseIndexPolicyDocument(p.PolicyDocument)
		if err != nil {
			continue
		}
		for name, typ := range parsed {
			if _, dup := fields[name]; !dup {
				fields[name] = typ
			}
		}
	}
	return fields
}

// cwActiveIndexedFields is the custom fields indexed in any of group's events,
// whichever data source they carry.
func cwActiveIndexedFields(group string) map[string]string {
	fields := cwIndexedFieldsOf(group, cwDataSourceOf(CWLogGroup{}))
	for _, row := range cwDataSourceSchemas.ListPrefix(group + "\x00") {
		for name, typ := range cwIndexedFieldsOf(group, row.Item.source()) {
			fields[name] = typ
		}
	}
	return fields
}

// cwIndexFlatten records the leaf values of a JSON message by dotted path,
// an array element by its position, as the field index syntax page spells
// them.
func cwIndexFlatten(message string) map[string]string {
	var doc any
	if json.Unmarshal([]byte(message), &doc) != nil {
		return nil
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil
	}
	out := map[string]string{}
	const maxDepth = 64
	var walk func(prefix string, v any, depth int)
	walk = func(prefix string, v any, depth int) {
		join := func(k string) string {
			if prefix == "" {
				return k
			}
			return prefix + "." + k
		}
		switch t := v.(type) {
		case map[string]any:
			if depth < maxDepth {
				for k, sub := range t {
					walk(join(k), sub, depth+1)
				}
				return
			}
		case []any:
			if depth < maxDepth {
				for i, sub := range t {
					walk(join(strconv.Itoa(i)), sub, depth+1)
				}
				return
			}
		}
		if prefix != "" {
			out[prefix] = listq.ScalarString(v)
		}
	}
	walk("", doc, 0)
	return out
}

// cwIndexIngested indexes events as a Standard group ingests them and keeps
// each index's first and last event time.
func cwIndexIngested(group, stream string, events []CWLogEvent) {
	if group == "" {
		return
	}
	custom := map[string]string{}
	if len(events) > 0 {
		custom = cwIndexedFieldsOf(group, events[0].dataSource())
	}
	now := time.Now()
	type seen struct {
		category, typ string
		first, last   int64
	}
	touched := map[string]*seen{}
	note := func(field, category, typ string, ts int64) {
		s := touched[field]
		if s == nil {
			s = &seen{category: category, typ: typ, first: ts, last: ts}
			touched[field] = s
		}
		s.first, s.last = min(s.first, ts), max(s.last, ts)
	}
	for i := range events {
		e := &events[i]
		values := cwIndexFlatten(e.processedMessage())
		system := map[string]string{
			"@logStream":     stream,
			"@aws.account":   awsAccountID(),
			"@aws.region":    awsRegion(),
			"@source.log":    group,
			"@ingestionTime": strconv.FormatInt(e.IngestionTime, 10),
		}
		source := e.dataSource()
		system["@data_source_name"], system["@data_source_type"], system["@data_format"] = source.Name, source.Type, source.Format
		lookup := func(field string) (string, bool) {
			if v, ok := system[field]; ok {
				return v, true
			}
			name := field
			if strings.HasPrefix(field, "@@") {
				name = field[1:]
			}
			v, ok := values[name]
			return v, ok
		}
		indexed := map[string]string{}
		for _, field := range cwDefaultIndexFields {
			if v, ok := lookup(field); ok {
				indexed[field] = v
				note(field, cwIndexCategoryDefault, cwIndexTypeField, e.Timestamp)
			}
		}
		for field, typ := range custom {
			if v, ok := lookup(field); ok {
				indexed[field] = v
				note(field, cwIndexCategoryCustom, typ, e.Timestamp)
			}
		}
		if len(indexed) > 0 {
			e.IndexedFields = indexed
		}
	}
	for field, s := range touched {
		key := cwFieldIndexKey(group, field)
		if !cwFieldIndexStates.Update(key, func(st *CWFieldIndexState) {
			st.Category, st.Type = s.category, s.typ
			st.FirstEventTime = min(st.FirstEventTime, s.first)
			st.LastEventTime = max(st.LastEventTime, s.last)
			st.LastScanTime = now.UnixMilli()
		}) {
			cwFieldIndexStates.Put(key, CWFieldIndexState{
				LogGroupName: group, FieldIndexName: field, Category: s.category, Type: s.typ,
				FirstEventTime: s.first, LastEventTime: s.last, LastScanTime: now.UnixMilli(),
			})
		}
	}
}

func cwDeleteFieldIndexes(group string) {
	for _, row := range cwFieldIndexStates.ListPrefix(group + "\x00") {
		cwFieldIndexStates.Delete(row.ID)
	}
}

func cwWriteIndexPolicy(p CWIndexPolicy) map[string]any {
	out := map[string]any{
		"logGroupIdentifier": p.LogGroupIdentifier,
		"lastUpdateTime":     p.LastUpdateTime,
		"policyDocument":     p.PolicyDocument,
		"source":             p.Source,
	}
	if p.PolicyName != "" {
		out["policyName"] = p.PolicyName
	}
	return out
}

func cwRefuseMissingGroup(w http.ResponseWriter, identifier string) (string, bool) {
	name, ok := cwResolveLogGroupIdentifier(identifier)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", identifier)
		return "", false
	}
	return name, true
}

func handleCWPutIndexPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		PolicyDocument     string `json:"policyDocument"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupIdentifier == "" || req.PolicyDocument == "" {
		AWSError(w, "InvalidParameterException", "logGroupIdentifier and policyDocument are required", http.StatusBadRequest)
		return
	}
	if _, err := cwParseIndexPolicyDocument(req.PolicyDocument); err != nil {
		AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
		return
	}
	name, ok := cwRefuseMissingGroup(w, req.LogGroupIdentifier)
	if !ok {
		return
	}
	if group, _ := cwLogGroups.Get(name); !group.standard() {
		AWSError(w, "InvalidParameterException", "Only log groups in the Standard log class support field index policies.", http.StatusBadRequest)
		return
	}
	p := CWIndexPolicy{
		LogGroupIdentifier: cwLogGroupArn(name),
		LastUpdateTime:     time.Now().UnixMilli(),
		PolicyDocument:     req.PolicyDocument,
		Source:             cwIndexSourceLogGroup,
	}
	cwIndexPolicies.Put(name, p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"indexPolicy": cwWriteIndexPolicy(p)})
}

func handleCWDeleteIndexPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := cwRefuseMissingGroup(w, req.LogGroupIdentifier)
	if !ok {
		return
	}
	if !cwIndexPolicies.Delete(name) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified index policy does not exist: %s", req.LogGroupIdentifier)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDescribeIndexPolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifiers []string `json:"logGroupIdentifiers"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.LogGroupIdentifiers) != 1 {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"1 validation error detected: Value at 'logGroupIdentifiers' failed to satisfy constraint: Member must have length equal to 1")
		return
	}
	name, ok := cwRefuseMissingGroup(w, req.LogGroupIdentifiers[0])
	if !ok {
		return
	}
	policies := []map[string]any{}
	if p, ok := cwIndexPolicyApplying(name); ok {
		policies = append(policies, cwWriteIndexPolicy(p))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"indexPolicies": policies})
}

func handleCWDescribeFieldIndexes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifiers []string `json:"logGroupIdentifiers"`
		IndexCategories     []string `json:"indexCategories"`
		NextToken           string   `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.LogGroupIdentifiers) < 1 || len(req.LogGroupIdentifiers) > cwDescribeFieldIndexesMax {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"logGroupIdentifiers must name between 1 and %d log groups", cwDescribeFieldIndexesMax)
		return
	}
	categories := map[string]bool{cwIndexCategoryDefault: true, cwIndexCategoryCustom: true, cwIndexCategoryInactive: true}
	if len(req.IndexCategories) > 0 {
		if len(req.IndexCategories) > 4 {
			AWSError(w, "InvalidParameterException", "indexCategories takes at most 4 values", http.StatusBadRequest)
			return
		}
		categories = map[string]bool{}
		for _, c := range req.IndexCategories {
			switch c {
			case cwIndexCategoryDefault, cwIndexCategoryCustom, "AUTO", cwIndexCategoryInactive:
				categories[c] = true
			default:
				AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
					"Invalid indexCategory %q: valid values are DEFAULT, CUSTOM, AUTO and INACTIVE", c)
				return
			}
		}
	}
	type fieldIndex struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		FieldIndexName     string `json:"fieldIndexName"`
		IndexCategory      string `json:"indexCategory"`
		Type               string `json:"type"`
		FirstEventTime     int64  `json:"firstEventTime"`
		LastEventTime      int64  `json:"lastEventTime"`
		LastScanTime       int64  `json:"lastScanTime"`
	}
	out := []fieldIndex{}
	for _, id := range req.LogGroupIdentifiers {
		name, ok := cwRefuseMissingGroup(w, id)
		if !ok {
			return
		}
		custom := cwActiveIndexedFields(name)
		rows := cwFieldIndexStates.ListPrefix(name + "\x00")
		sort.Slice(rows, func(i, j int) bool { return rows[i].Item.FieldIndexName < rows[j].Item.FieldIndexName })
		for _, row := range rows {
			st := row.Item
			category, typ := st.Category, st.Type
			if category == cwIndexCategoryCustom {
				if t, active := custom[st.FieldIndexName]; active {
					typ = t
				} else {
					category = cwIndexCategoryInactive
				}
			}
			if !categories[category] {
				continue
			}
			out = append(out, fieldIndex{
				LogGroupIdentifier: cwLogGroupArn(name), FieldIndexName: st.FieldIndexName, IndexCategory: category, Type: typ,
				FirstEventTime: st.FirstEventTime, LastEventTime: st.LastEventTime, LastScanTime: st.LastScanTime,
			})
		}
	}
	page, next, ok := awsKeyPage(w, logsBadToken, out, func(f fieldIndex) string {
		return f.LogGroupIdentifier + "\x00" + f.FieldIndexName
	}, req.NextToken, 0, 1000)
	if !ok {
		return
	}
	if page == nil {
		page = []fieldIndex{}
	}
	resp := map[string]any{"fieldIndexes": page}
	if next != "" {
		resp["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// cwFilterIndexField reads the field a filterIndex command or an equality
// filter names: CloudWatch Logs Insights uses an index for `field = value`
// and `field in [...]`.
var cwIndexedComparison = regexp.MustCompile(`^\s*([@\w.\-/#]+)\s*(?:=|(?i:in)\s*\[)`)

// cwIndexRecord is the record an event's indexed values make, which a
// filterIndex command evaluates; an event indexed more than 30 days ago
// counts as unindexed.
func cwIndexRecord(rec cwInsightsRecord) cwInsightsRecord {
	raw := rec["@__indexed"]
	if raw == "" {
		return nil
	}
	var values map[string]string
	if json.Unmarshal([]byte(raw), &values) != nil {
		return nil
	}
	out := cwInsightsRecord{}
	for k, v := range values {
		out[strings.TrimPrefix(k, "@@")] = v
		if strings.HasPrefix(k, "@@") {
			out["@"+k[2:]] = v
		}
	}
	return out
}

// cwRunFilterIndex keeps the records whose indexed field satisfies the
// command; a record not indexed on the field is skipped, which is what sets
// filterIndex apart from filter.
func cwRunFilterIndex(expr string, records []cwInsightsRecord) ([]cwInsightsRecord, error) {
	m := cwIndexedComparison.FindStringSubmatch(expr)
	if m == nil {
		return nil, fmt.Errorf("filterIndex supports only an indexed field compared with = or in")
	}
	field := m[1]
	node, err := cwParseInsightsFilter(expr)
	if err != nil {
		return nil, err
	}
	var out []cwInsightsRecord
	for _, rec := range records {
		indexed := cwIndexRecord(rec)
		if _, ok := indexed[field]; !ok {
			continue
		}
		if node.Eval(indexed.doc()) {
			out = append(out, rec)
		}
	}
	return out, nil
}

// cwIndexSkipped counts the records an indexed comparison in query lets
// CloudWatch Logs Insights skip: those indexed on the field with a value the
// comparison rejects and, under filterIndex, those not indexed on it.
func cwIndexSkipped(query string, records []cwInsightsRecord) int {
	for _, raw := range strings.Split(query, "|") {
		kw, rest := cwSplitFirstWord(strings.TrimSpace(raw))
		kw = strings.ToLower(kw)
		if kw != "filter" && kw != "filterindex" {
			continue
		}
		m := cwIndexedComparison.FindStringSubmatch(rest)
		if m == nil {
			continue
		}
		node, err := cwParseInsightsFilter(rest)
		if err != nil {
			return 0
		}
		skipped := 0
		for _, rec := range records {
			indexed := cwIndexRecord(rec)
			if _, ok := indexed[m[1]]; !ok {
				if kw == "filterindex" {
					skipped++
				}
				continue
			}
			if !node.Eval(indexed.doc()) {
				skipped++
			}
		}
		return skipped
	}
	return 0
}

// cwIndexedFieldsJSON is how a record carries its event's live indexes.
func cwIndexedFieldsJSON(e CWLogEvent, now time.Time) string {
	if len(e.IndexedFields) == 0 || now.Sub(time.UnixMilli(e.IngestionTime)) > cwIndexedEventLifetime {
		return ""
	}
	body, _ := json.Marshal(e.IndexedFields)
	return string(body)
}

// An earlier build keyed a log group-level index policy by the identifier it
// was put with, a name or an ARN; key it by the group's name.
func cwMigrateIndexPolicyKeys(db *sql.DB) error {
	return sim.Migrate(db, "cw_index_policies/by-name", func() error {
		for _, row := range cwIndexPolicies.ListPrefix("") {
			name, ok := cwResolveLogGroupIdentifier(row.ID)
			if !ok {
				cwIndexPolicies.Delete(row.ID)
				continue
			}
			p := row.Item
			p.LogGroupIdentifier = cwLogGroupArn(name)
			if p.Source == "" {
				p.Source = cwIndexSourceLogGroup
			}
			if name != row.ID {
				cwIndexPolicies.Delete(row.ID)
			}
			cwIndexPolicies.Put(name, p)
		}
		return nil
	})
}

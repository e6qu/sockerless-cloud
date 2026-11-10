package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Data sources as the data source discovery and management guide defines
// them: CloudWatch Logs classifies each ingested event by data source name
// and type, from the log group's cw:datasource:name and cw:datasource:type
// tags for application logs, and records it in @data_source_name,
// @data_source_type and @data_format, "Unknown" when undetermined.

const (
	cwDataSourceTagName = "cw:datasource:name"
	cwDataSourceTagType = "cw:datasource:type"
	cwDataSourceUnknown = "Unknown"
	cwDataFormatDefault = "Default"
	cwDataFormatCustom  = "Custom"

	cwAggregateSummariesMax = 50
)

var (
	cwDataSourceTagValue      = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)
	cwAggregateLogGroupNameRe = regexp.MustCompile(`^(\^?[\.\-_\/#A-Za-z0-9]{3,24})(\|\^?[\.\-_\/#A-Za-z0-9]{3,24}){0,4}$`)
)

type cwDataSource struct {
	Name   string
	Type   string
	Format string
}

func (d cwDataSource) key() string { return d.Name + "\x00" + d.Type }

func cwValidDataSourceTag(v string) bool {
	return cwDataSourceTagValue.MatchString(v) && !strings.Contains(v, "__")
}

// cwDataSourceOf classifies the events group ingests. The guide reserves
// names starting with aws or amazon for AWS service logs.
func cwDataSourceOf(group CWLogGroup) cwDataSource {
	name, typ := group.Tags[cwDataSourceTagName], group.Tags[cwDataSourceTagType]
	if cwValidDataSourceTag(name) && cwValidDataSourceTag(typ) &&
		!strings.HasPrefix(name, "aws") && !strings.HasPrefix(name, "amazon") {
		return cwDataSource{Name: name, Type: typ, Format: cwDataFormatCustom}
	}
	return cwDataSource{Name: cwDataSourceUnknown, Type: cwDataSourceUnknown, Format: cwDataFormatDefault}
}

// An event ingested before classification reads as undetermined.
func (e CWLogEvent) dataSource() cwDataSource {
	if e.DataSourceName == "" {
		return cwDataSource{Name: cwDataSourceUnknown, Type: cwDataSourceUnknown, Format: cwDataFormatDefault}
	}
	return cwDataSource{Name: e.DataSourceName, Type: e.DataSourceType, Format: e.DataFormat}
}

// Keyed by log group name, data source name, type and format: the schema
// CloudWatch Logs discovered in the group's events of that data source.
type CWDataSourceSchema struct {
	LogGroupName   string            `json:"logGroupName"`
	DataSourceName string            `json:"dataSourceName"`
	DataSourceType string            `json:"dataSourceType"`
	DataFormat     string            `json:"dataFormat"`
	Fields         map[string]string `json:"fields"`
}

func (s CWDataSourceSchema) source() cwDataSource {
	return cwDataSource{Name: s.DataSourceName, Type: s.DataSourceType, Format: s.DataFormat}
}

var (
	cwDataSourceSchemas      sim.Store[CWDataSourceSchema]
	cwDataSourceSchemasBySrc sim.GenerationIndex[CWDataSourceSchema]
)

func registerCloudWatchLogsDataSources(srv *sim.Server) {
	cwDataSourceSchemas = sim.MakeStore[CWDataSourceSchema](srv.DB(), "cw_data_source_schemas")
}

func cwDataSourceSchemaKey(group string, ds cwDataSource) string {
	return group + "\x00" + ds.Name + "\x00" + ds.Type + "\x00" + ds.Format
}

// cwClassifyIngested stamps each event with group's data source.
func cwClassifyIngested(group CWLogGroup, events []CWLogEvent) cwDataSource {
	ds := cwDataSourceOf(group)
	for i := range events {
		events[i].DataSourceName, events[i].DataSourceType, events[i].DataFormat = ds.Name, ds.Type, ds.Format
	}
	return ds
}

// cwDiscoverSchema records the top-level fields of the ingested events'
// JSON messages under their data source.
func cwDiscoverSchema(group string, ds cwDataSource, events []CWLogEvent) {
	fields := map[string]string{}
	for _, e := range events {
		var obj map[string]any
		if json.Unmarshal([]byte(e.processedMessage()), &obj) == nil {
			for k, v := range obj {
				fields[k] = cwLogFieldDataType(v)
			}
		}
	}
	cwDataSourceSchemas.Upsert(cwDataSourceSchemaKey(group, ds), func(s *CWDataSourceSchema) {
		s.LogGroupName, s.DataSourceName, s.DataSourceType, s.DataFormat = group, ds.Name, ds.Type, ds.Format
		if s.Fields == nil {
			s.Fields = map[string]string{}
		}
		for k, t := range fields {
			s.Fields[k] = t
		}
	})
}

func cwForgetDataSources(group string) {
	for _, row := range cwDataSourceSchemas.ListPrefix(group + "\x00") {
		cwDataSourceSchemas.Delete(row.ID)
	}
}

// The fields every classified event carries.
var cwDataSourceSystemFields = map[string]string{
	"@timestamp": "timestamp", "@ingestionTime": "timestamp", "@message": "string", "@logStream": "string",
	"@data_source_name": "string", "@data_source_type": "string", "@data_format": "string",
}

func handleCWGetLogFields(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DataSourceName string `json:"dataSourceName"`
		DataSourceType string `json:"dataSourceType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.DataSourceName == "" || req.DataSourceType == "" {
		AWSError(w, "InvalidParameterException", "dataSourceName and dataSourceType are required", http.StatusBadRequest)
		return
	}
	ds := cwDataSource{Name: req.DataSourceName, Type: req.DataSourceType}
	schemas := cwDataSourceSchemasBySrc.LookupAll(cwDataSourceSchemas, ds.key(), func(s CWDataSourceSchema) []string { return []string{s.source().key()} })
	if len(schemas) == 0 {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified data source does not exist: %s.%s", req.DataSourceName, req.DataSourceType)
		return
	}
	fieldTypes := map[string]string{}
	for name, t := range cwDataSourceSystemFields {
		fieldTypes[name] = t
	}
	for _, s := range schemas {
		for name, t := range s.Fields {
			fieldTypes[name] = t
		}
	}
	names := make([]string, 0, len(fieldTypes))
	for name := range fieldTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	logFields := make([]map[string]any, 0, len(names))
	for _, name := range names {
		logFields = append(logFields, map[string]any{
			"logFieldName": name,
			"logFieldType": map[string]any{"type": fieldTypes[name]},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"logFields": logFields})
}

// cwLogGroupNamePatternMatches applies ListAggregateLogGroupSummaries'
// logGroupNamePattern: up to five patterns separated by |, each a prefix
// when it starts with ^ and a substring otherwise.
func cwLogGroupNamePatternMatches(pattern, name string) bool {
	for _, p := range strings.Split(pattern, "|") {
		if prefix, ok := strings.CutPrefix(p, "^"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if strings.Contains(name, p) {
			return true
		}
	}
	return false
}

func handleCWListAggregateLogGroupSummaries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupNamePattern string `json:"logGroupNamePattern"`
		LogGroupClass       string `json:"logGroupClass"`
		GroupBy             string `json:"groupBy"`
		DataSources         []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"dataSources"`
		Limit     int    `json:"limit"`
		NextToken string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	withFormat := false
	switch req.GroupBy {
	case "DATA_SOURCE_NAME_AND_TYPE":
	case "DATA_SOURCE_NAME_TYPE_AND_FORMAT":
		withFormat = true
	default:
		AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"1 validation error detected: Value '%s' at 'groupBy' failed to satisfy constraint: Member must satisfy enum value set: [DATA_SOURCE_NAME_TYPE_AND_FORMAT, DATA_SOURCE_NAME_AND_TYPE]", req.GroupBy)
		return
	}
	if req.LogGroupNamePattern != "" && !cwAggregateLogGroupNameRe.MatchString(req.LogGroupNamePattern) {
		AWSError(w, "ValidationException", "1 validation error detected: Value at 'logGroupNamePattern' failed to satisfy constraint: Member must satisfy regular expression pattern: (\\^?[\\.\\-_\\/#A-Za-z0-9]{3,24})(\\|\\^?[\\.\\-_\\/#A-Za-z0-9]{3,24}){0,4}", http.StatusBadRequest)
		return
	}
	if len(req.DataSources) > 5 {
		AWSError(w, "ValidationException", "1 validation error detected: Value at 'dataSources' failed to satisfy constraint: Member must have length less than or equal to 5", http.StatusBadRequest)
		return
	}
	if req.Limit < 0 || req.Limit > cwAggregateSummariesMax {
		AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"1 validation error detected: Value '%d' at 'limit' failed to satisfy constraint: Member must have value between 1 and %d", req.Limit, cwAggregateSummariesMax)
		return
	}
	selected := func(ds cwDataSource) bool {
		if len(req.DataSources) == 0 {
			return true
		}
		for _, f := range req.DataSources {
			if f.Name == ds.Name && (f.Type == "" || f.Type == ds.Type) {
				return true
			}
		}
		return false
	}
	type summary struct {
		ds     cwDataSource
		groups map[string]bool
	}
	summaries := map[string]*summary{}
	for _, s := range cwDataSourceSchemas.List() {
		ds := s.source()
		if !selected(ds) {
			continue
		}
		if req.LogGroupNamePattern != "" && !cwLogGroupNamePatternMatches(req.LogGroupNamePattern, s.LogGroupName) {
			continue
		}
		if req.LogGroupClass != "" {
			group, ok := cwLogGroups.Get(s.LogGroupName)
			if !ok || cwCanonicalClass(group.class()) != cwCanonicalClass(req.LogGroupClass) {
				continue
			}
		}
		key := ds.key()
		if withFormat {
			key += "\x00" + ds.Format
		} else {
			ds.Format = ""
		}
		if summaries[key] == nil {
			summaries[key] = &summary{ds: ds, groups: map[string]bool{}}
		}
		summaries[key].groups[s.LogGroupName] = true
	}
	type identifier struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	type aggregate struct {
		GroupingIdentifiers []identifier `json:"groupingIdentifiers"`
		LogGroupCount       int          `json:"logGroupCount"`
		key                 string
	}
	all := make([]aggregate, 0, len(summaries))
	for key, s := range summaries {
		ids := []identifier{{"dataSource.Name", s.ds.Name}, {"dataSource.Type", s.ds.Type}}
		if withFormat {
			ids = append(ids, identifier{"dataSource.Format", s.ds.Format})
		}
		all = append(all, aggregate{GroupingIdentifiers: ids, LogGroupCount: len(s.groups), key: key})
	}
	page, next, ok := awsKeyPage(w, logsBadToken, all, func(a aggregate) string { return a.key }, req.NextToken, req.Limit, cwAggregateSummariesMax)
	if !ok {
		return
	}
	if page == nil {
		page = []aggregate{}
	}
	out := map[string]any{"aggregateLogGroupSummaries": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

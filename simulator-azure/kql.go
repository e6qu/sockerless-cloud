package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// kqlQuery represents a parsed KQL query with table, filters, limit, and projection.
type kqlQuery struct {
	Table   string
	Filters []kqlFilter
	Limit   int
	Project []string // column names from | project clause
}

// kqlFilter represents a single where-clause filter.
type kqlFilter struct {
	Field    string
	Operator string // "==", ">", ">="
	Value    string
	IsTime   bool // true if value was wrapped in datetime()
}

// parseKQL parses a simplified KQL query into its components.
// Supports: Table | where Field == 'value' | where Field > datetime(ts) | take N
func parseKQL(query string) kqlQuery {
	q := kqlQuery{Limit: -1}
	parts := strings.Split(query, "|")

	for i, part := range parts {
		part = strings.TrimSpace(part)
		if i == 0 {
			// First segment is the table name (may contain trailing spaces)
			q.Table = strings.TrimSpace(part)
			continue
		}

		if strings.HasPrefix(part, "where ") {
			clause := strings.TrimPrefix(part, "where ")
			f := parseKQLWhere(clause)
			if f.Field != "" {
				q.Filters = append(q.Filters, f)
			}
		} else if strings.HasPrefix(part, "take ") {
			if _, err := fmt.Sscanf(strings.TrimPrefix(part, "take "), "%d", &q.Limit); err != nil {
				q.Limit = 0
			}
		} else if strings.HasPrefix(part, "limit ") {
			if _, err := fmt.Sscanf(strings.TrimPrefix(part, "limit "), "%d", &q.Limit); err != nil {
				q.Limit = 0
			}
		} else if strings.HasPrefix(part, "project ") {
			cols := strings.TrimPrefix(part, "project ")
			for _, col := range strings.Split(cols, ",") {
				col = strings.TrimSpace(col)
				if col != "" {
					q.Project = append(q.Project, col)
				}
			}
		}
		// Ignore order by and other clauses — they don't affect filtering
	}

	return q
}

// parseKQLWhere parses a single where clause.
func parseKQLWhere(clause string) kqlFilter {
	// Try operators in order of length (>= before >)
	for _, op := range []string{">=", ">", "=="} {
		idx := strings.Index(clause, op)
		if idx < 0 {
			continue
		}
		field := strings.TrimSpace(clause[:idx])
		rawVal := strings.TrimSpace(clause[idx+len(op):])

		f := kqlFilter{
			Field:    field,
			Operator: op,
		}

		// Check for datetime() wrapper
		if strings.HasPrefix(rawVal, "datetime(") && strings.HasSuffix(rawVal, ")") {
			inner := rawVal[len("datetime(") : len(rawVal)-1]
			f.Value = strings.Trim(inner, "'\"")
			f.IsTime = true
		} else {
			f.Value = strings.Trim(rawVal, "'\"")
		}

		return f
	}
	return kqlFilter{}
}

// runKQLQuery executes a KQL query against the workspace's stored log rows and
// returns the QueryResults tabular shape. Both the POST and GET Log Analytics
// query endpoints and each member of a $batch run through here.
func runKQLQuery(workspaceID, query string) QueryResponse {
	parsed := parseKQL(query)

	columns, ok := kqlTableSchemas[parsed.Table]
	if !ok {
		// Default to ContainerAppConsoleLogs_CL schema for an unknown table.
		columns = kqlTableSchemas["ContainerAppConsoleLogs_CL"]
	}

	storeKey := workspaceID + ":" + parsed.Table
	entries, _ := monitorLogs.Get(storeKey)
	if len(entries) == 0 {
		entries, _ = monitorLogs.Get("default:" + parsed.Table)
	}

	rows := make([][]any, 0)
	for _, row := range entries {
		if !row.matchesFilters(parsed.Filters) {
			continue
		}
		rows = append(rows, row.toRow(columns))
		if parsed.Limit > 0 && len(rows) >= parsed.Limit {
			break
		}
	}

	// Apply project clause — filter columns and rows to the projected subset.
	resultColumns := columns
	resultRows := rows
	if len(parsed.Project) > 0 {
		colIndex := make(map[string]int, len(columns))
		for i, col := range columns {
			colIndex[col.Name] = i
		}
		var projCols []Column
		var projIndices []int
		for _, name := range parsed.Project {
			if idx, ok := colIndex[name]; ok {
				projCols = append(projCols, columns[idx])
				projIndices = append(projIndices, idx)
			}
		}
		projRows := make([][]any, len(rows))
		for i, row := range rows {
			pr := make([]any, len(projIndices))
			for j, idx := range projIndices {
				pr[j] = row[idx]
			}
			projRows[i] = pr
		}
		resultColumns = projCols
		resultRows = projRows
	}

	return QueryResponse{
		Tables: []Table{{
			Name:    "PrimaryResult",
			Columns: resultColumns,
			Rows:    resultRows,
		}},
	}
}

// Table schemas — maps table name to column definitions.
var kqlTableSchemas = map[string][]Column{
	"ContainerAppConsoleLogs_CL": {
		{Name: "TimeGenerated", Type: "datetime"},
		{Name: "ContainerGroupName_s", Type: "string"},
		{Name: "ContainerAppName_s", Type: "string"},
		{Name: "Log_s", Type: "string"},
		{Name: "Stream_s", Type: "string"},
	},
	"AppTraces": {
		{Name: "TimeGenerated", Type: "datetime"},
		{Name: "Message", Type: "string"},
		{Name: "AppRoleName", Type: "string"},
	},
}

// monitorLogRow is a generic log row stored as field→value pairs.
type monitorLogRow map[string]string

// matchesFilters returns true if the row matches all the given KQL filters.
func (row monitorLogRow) matchesFilters(filters []kqlFilter) bool {
	for _, f := range filters {
		val, exists := row[f.Field]
		if !exists {
			return false
		}

		switch f.Operator {
		case "==":
			if val != f.Value {
				return false
			}
		case ">", ">=":
			if f.IsTime {
				rowTime, err1 := parseTimeFlexible(val)
				filterTime, err2 := parseTimeFlexible(f.Value)
				if err1 != nil || err2 != nil {
					return false
				}
				if f.Operator == ">" && !rowTime.After(filterTime) {
					return false
				}
				if f.Operator == ">=" && rowTime.Before(filterTime) {
					return false
				}
			} else {
				// Numeric comparison fallback
				rv, err1 := strconv.ParseFloat(val, 64)
				fv, err2 := strconv.ParseFloat(f.Value, 64)
				if err1 != nil || err2 != nil {
					return false
				}
				if f.Operator == ">" && rv <= fv {
					return false
				}
				if f.Operator == ">=" && rv < fv {
					return false
				}
			}
		}
	}
	return true
}

// toRow converts a monitorLogRow to an ordered slice matching the given columns.
func (row monitorLogRow) toRow(columns []Column) []any {
	result := make([]any, len(columns))
	for i, col := range columns {
		result[i] = row[col.Name]
	}
	return result
}

func parseTimeFlexible(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	return t, err
}

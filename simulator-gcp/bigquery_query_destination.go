package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type bqQueryConfig struct {
	Query             string             `json:"query"`
	DefaultDataset    *BQDatasetRef      `json:"defaultDataset"`
	DestinationTable  *BQTableRef        `json:"destinationTable"`
	CreateDisposition string             `json:"createDisposition"`
	WriteDisposition  string             `json:"writeDisposition"`
	UseLegacySQL      *bool              `json:"useLegacySql"`
	ParameterMode     string             `json:"parameterMode"`
	QueryParameters   []bqQueryParameter `json:"queryParameters"`
	TimePartitioning  json.RawMessage    `json:"timePartitioning"`
	RangePartitioning json.RawMessage    `json:"rangePartitioning"`
	Clustering        json.RawMessage    `json:"clustering"`
}

func (cfg bqQueryConfig) check() error {
	switch cfg.CreateDisposition {
	case "", "CREATE_IF_NEEDED", "CREATE_NEVER":
	default:
		return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for createDisposition: %s", cfg.CreateDisposition)
	}
	switch cfg.WriteDisposition {
	case "", "WRITE_EMPTY", "WRITE_APPEND", "WRITE_TRUNCATE", "WRITE_TRUNCATE_DATA":
	default:
		return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for writeDisposition: %s", cfg.WriteDisposition)
	}
	if d := cfg.DestinationTable; d != nil && (d.DatasetID == "" || d.TableID == "") {
		return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid destination table reference")
	}
	if strings.TrimSpace(cfg.Query) == "" {
		return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Required parameter is missing: query")
	}
	// jobs.insert defaults useLegacySql to true; the simulator's engine reads
	// GoogleSQL only.
	if cfg.UseLegacySQL == nil || *cfg.UseLegacySQL {
		if !strings.HasPrefix(strings.TrimSpace(strings.ToLower(cfg.Query)), "#standardsql") {
			return apiRefuse(http.StatusNotImplemented, "NOT_IMPLEMENTED", "The simulator runs GoogleSQL queries; set useLegacySql to false")
		}
	}
	if (cfg.TimePartitioning != nil || cfg.RangePartitioning != nil || cfg.Clustering != nil) && cfg.DestinationTable == nil {
		return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Partitioning and clustering specifications require a destination table")
	}
	return nil
}

// bqFailureStatuses is the HTTP status getQueryResults answers for a query
// job that failed with an ErrorProto reason.
var bqFailureStatuses = map[string]struct {
	code   int
	status string
}{
	"notFound":     {http.StatusNotFound, "NOT_FOUND"},
	"duplicate":    {http.StatusConflict, "ALREADY_EXISTS"},
	"invalid":      {http.StatusBadRequest, "INVALID_ARGUMENT"},
	"invalidQuery": {http.StatusBadRequest, "INVALID_ARGUMENT"},
	"stopped":      {http.StatusBadRequest, "INVALID_ARGUMENT"},
	"backendError": {http.StatusInternalServerError, "INTERNAL"},
}

// bqWriteQueryDestination stores a query's result in the job's destination
// table under its create and write dispositions, as one atomic update.
// WRITE_EMPTY is the default for a query job, and only WRITE_TRUNCATE takes
// the result's schema over an existing table's.
func bqWriteQueryDestination(ctx context.Context, r *http.Request, project string, cfg bqQueryConfig, schema *BQSchema, rows []map[string]any) error {
	dest := *cfg.DestinationTable
	if dest.ProjectID == "" {
		dest.ProjectID = project
	}
	if _, ok := bqDatasets.Get(bqDatasetKey(dest.ProjectID, dest.DatasetID)); !ok {
		return bqFail("notFound", "Not found: Dataset %s:%s", dest.ProjectID, dest.DatasetID)
	}
	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	return bqWriteTable(ctx, dest, func() error {
		table, exists := bqTables.Get(key)
		if !exists {
			if cfg.CreateDisposition == "CREATE_NEVER" {
				return bqFail("notFound", "Not found: Table %s", bqTableName(dest))
			}
			table = BQTable{TableReference: dest, TimePartitioning: cfg.TimePartitioning, RangePartitioning: cfg.RangePartitioning, Clustering: cfg.Clustering}
		}
		if table.Type != "" && table.Type != "TABLE" {
			return bqFail("invalid", "Cannot write query results to %s %s.", table.Type, bqTableName(dest))
		}
		existing, _ := bqRows.Get(key)
		sameSchema := !exists || table.Schema == nil || len(table.Schema.Fields) == 0 ||
			bqSameSchema(table.Schema.Fields, bqSchemaFields(schema))
		switch cfg.WriteDisposition {
		case "WRITE_TRUNCATE":
			existing.Rows = nil
			table.Schema = schema
		case "WRITE_TRUNCATE_DATA", "WRITE_APPEND":
			if !sameSchema {
				return bqFail("invalid", "Provided Schema does not match Table %s.", bqTableName(dest))
			}
			if cfg.WriteDisposition == "WRITE_TRUNCATE_DATA" {
				existing.Rows = nil
			}
		default:
			if len(existing.Rows) > 0 {
				return bqFail("duplicate", "Already Exists: Table %s", bqTableName(dest))
			}
			if !sameSchema {
				return bqFail("invalid", "Provided Schema does not match Table %s.", bqTableName(dest))
			}
		}
		if table.Schema == nil || len(table.Schema.Fields) == 0 {
			table.Schema = schema
		}
		existing.Rows = append(existing.Rows, rows...)
		if existing.Rows == nil {
			existing.Rows = []map[string]any{}
		}
		bqRows.Put(key, existing)
		bqTables.Put(key, bqApplyTableDefaults(r, table, dest.ProjectID, dest.DatasetID, dest.TableID))
		return nil
	})
}

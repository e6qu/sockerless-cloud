package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// AWS Glue — Catalog (multi-catalog), Table Optimizer, BatchGet*, and zero-ETL
// Integration families. AWS JSON 1.1 (X-Amz-Target: AWSGlue.<Op>). Faithful CRUD
// on real sim.Stores; the BatchGet* ops read the existing Glue stores (crawlers,
// jobs, triggers, workflows, connections) defined in glue.go, read-only.

// GlueCatalog models a Glue Data Catalog (the multi-catalog feature). Keyed by
// the catalog name; the response carries CatalogId == Name and a ResourceArn.
type GlueCatalog struct {
	CatalogId         string            `json:"CatalogId"`
	Name              string            `json:"Name"`
	ResourceArn       string            `json:"ResourceArn"`
	Description       string            `json:"Description,omitempty"`
	Parameters        map[string]string `json:"Parameters,omitempty"`
	CreateTime        float64           `json:"CreateTime"`
	UpdateTime        float64           `json:"UpdateTime"`
	FederatedCatalog  map[string]any    `json:"FederatedCatalog,omitempty"`
	CatalogProperties map[string]any    `json:"CatalogProperties,omitempty"`
	Tags              map[string]string `json:"Tags,omitempty"`
}

// GlueTableOptimizer models a table optimizer attached to a (catalog, database,
// table, type) where type is compaction/retention/orphan_file_deletion. The
// wire shape (TableOptimizer) uses camelCase member names.
type GlueTableOptimizer struct {
	CatalogId     string                   `json:"-"`
	DatabaseName  string                   `json:"-"`
	TableName     string                   `json:"-"`
	Type          string                   `json:"type"`
	Configuration GlueTableOptimizerConfig `json:"configuration"`
	LastRun       *GlueTableOptimizerRun   `json:"lastRun,omitempty"`
	Runs          []GlueTableOptimizerRun  `json:"-"`
}

// GlueTableOptimizerConfig is stored verbatim to preserve the nested wire shape.
type GlueTableOptimizerConfig map[string]any

// GlueTableOptimizerRun is one optimizer run record (TableOptimizerRun shape).
type GlueTableOptimizerRun struct {
	EventType      string  `json:"eventType,omitempty"`
	StartTimestamp float64 `json:"startTimestamp,omitempty"`
	EndTimestamp   float64 `json:"endTimestamp,omitempty"`
	Error          string  `json:"error,omitempty"`
}

// GlueIntegration models a zero-ETL integration keyed by IntegrationName.
type GlueIntegration struct {
	IntegrationName             string            `json:"IntegrationName"`
	IntegrationArn              string            `json:"IntegrationArn"`
	SourceArn                   string            `json:"SourceArn"`
	TargetArn                   string            `json:"TargetArn"`
	Description                 string            `json:"Description,omitempty"`
	Status                      string            `json:"Status"`
	CreateTime                  float64           `json:"CreateTime"`
	KmsKeyId                    string            `json:"KmsKeyId,omitempty"`
	DataFilter                  string            `json:"DataFilter,omitempty"`
	AdditionalEncryptionContext map[string]string `json:"AdditionalEncryptionContext,omitempty"`
	IntegrationConfig           map[string]any    `json:"IntegrationConfig,omitempty"`
	Tags                        []map[string]any  `json:"Tags,omitempty"`
}

// GlueIntegrationResourceProperty holds the source/target processing properties
// for an integration, keyed by ResourceArn.
type GlueIntegrationResourceProperty struct {
	ResourceArn                string         `json:"ResourceArn"`
	ResourcePropertyArn        string         `json:"ResourcePropertyArn"`
	SourceProcessingProperties map[string]any `json:"SourceProcessingProperties,omitempty"`
	TargetProcessingProperties map[string]any `json:"TargetProcessingProperties,omitempty"`
}

// GlueIntegrationTableProperties holds the source/target table configuration for
// an integration's replicated table, keyed by ResourceArn + TableName.
type GlueIntegrationTableProperties struct {
	ResourceArn       string         `json:"ResourceArn"`
	TableName         string         `json:"TableName"`
	SourceTableConfig map[string]any `json:"SourceTableConfig,omitempty"`
	TargetTableConfig map[string]any `json:"TargetTableConfig,omitempty"`
}

var (
	glueCatalogs        sim.Store[GlueCatalog]
	glueTableOptimizers sim.Store[GlueTableOptimizer]
	glueIntegrations    sim.Store[GlueIntegration]
	glueIntegResProps   sim.Store[GlueIntegrationResourceProperty]
	glueIntegTableProps sim.Store[GlueIntegrationTableProperties]
)

// registerGlueCatalogOptimizer registers the Catalog / Table Optimizer / BatchGet /
// Integration awsJson1.1 operations onto the shared Glue router.
func registerGlueCatalogOptimizer(r *sim.AWSRouter, srv *sim.Server) {
	glueCatalogs = sim.MakeStore[GlueCatalog](srv.DB(), "glue_catalogs")
	glueTableOptimizers = sim.MakeStore[GlueTableOptimizer](srv.DB(), "glue_table_optimizers")
	glueIntegrations = sim.MakeStore[GlueIntegration](srv.DB(), "glue_integrations")
	glueIntegResProps = sim.MakeStore[GlueIntegrationResourceProperty](srv.DB(), "glue_integration_resource_properties")
	glueIntegTableProps = sim.MakeStore[GlueIntegrationTableProperties](srv.DB(), "glue_integration_table_properties")

	// Catalog (multi-catalog).
	r.Register("AWSGlue.CreateCatalog", handleGlueCreateCatalog)
	r.Register("AWSGlue.GetCatalog", handleGlueGetCatalog)
	r.Register("AWSGlue.GetCatalogs", handleGlueGetCatalogs)
	r.Register("AWSGlue.UpdateCatalog", handleGlueUpdateCatalog)
	r.Register("AWSGlue.DeleteCatalog", handleGlueDeleteCatalog)

	// Table Optimizer.
	r.Register("AWSGlue.CreateTableOptimizer", handleGlueCreateTableOptimizer)
	r.Register("AWSGlue.GetTableOptimizer", handleGlueGetTableOptimizer)
	r.Register("AWSGlue.BatchGetTableOptimizer", handleGlueBatchGetTableOptimizer)
	r.Register("AWSGlue.UpdateTableOptimizer", handleGlueUpdateTableOptimizer)
	r.Register("AWSGlue.DeleteTableOptimizer", handleGlueDeleteTableOptimizer)
	r.Register("AWSGlue.ListTableOptimizerRuns", handleGlueListTableOptimizerRuns)

	// BatchGet families reading existing stores.
	r.Register("AWSGlue.BatchGetCrawlers", handleGlueBatchGetCrawlers)
	r.Register("AWSGlue.BatchGetJobs", handleGlueBatchGetJobs)
	r.Register("AWSGlue.BatchGetTriggers", handleGlueBatchGetTriggers)
	r.Register("AWSGlue.BatchGetWorkflows", handleGlueBatchGetWorkflows)
	r.Register("AWSGlue.BatchGetCustomEntityTypes", handleGlueBatchGetCustomEntityTypes)
	r.Register("AWSGlue.BatchDeleteConnection", handleGlueBatchDeleteConnection)
	r.Register("AWSGlue.BatchUpdatePartition", handleGlueBatchUpdatePartition)

	// Integration (zero-ETL).
	r.Register("AWSGlue.CreateIntegration", handleGlueCreateIntegration)
	r.Register("AWSGlue.DescribeIntegrations", handleGlueDescribeIntegrations)
	r.Register("AWSGlue.ModifyIntegration", handleGlueModifyIntegration)
	r.Register("AWSGlue.DeleteIntegration", handleGlueDeleteIntegration)
	r.Register("AWSGlue.DescribeInboundIntegrations", handleGlueDescribeInboundIntegrations)

	// Integration resource properties.
	r.Register("AWSGlue.CreateIntegrationResourceProperty", handleGlueCreateIntegrationResourceProperty)
	r.Register("AWSGlue.GetIntegrationResourceProperty", handleGlueGetIntegrationResourceProperty)
	r.Register("AWSGlue.UpdateIntegrationResourceProperty", handleGlueUpdateIntegrationResourceProperty)
	r.Register("AWSGlue.DeleteIntegrationResourceProperty", handleGlueDeleteIntegrationResourceProperty)
	r.Register("AWSGlue.ListIntegrationResourceProperties", handleGlueListIntegrationResourceProperties)

	// Integration table properties.
	r.Register("AWSGlue.CreateIntegrationTableProperties", handleGlueCreateIntegrationTableProperties)
	r.Register("AWSGlue.GetIntegrationTableProperties", handleGlueGetIntegrationTableProperties)
	r.Register("AWSGlue.UpdateIntegrationTableProperties", handleGlueUpdateIntegrationTableProperties)
	r.Register("AWSGlue.DeleteIntegrationTableProperties", handleGlueDeleteIntegrationTableProperties)
}

func glueCatalogArn(name string) string {
	return fmt.Sprintf("arn:aws:glue:%s:%s:catalog/%s", awsRegion(), awsAccountID(), name)
}

func glueIntegrationArn(name string) string {
	return fmt.Sprintf("arn:aws:glue:%s:%s:integration/%s", awsRegion(), awsAccountID(), name)
}

func glueIntegResPropArn(resourceArn string) string {
	return fmt.Sprintf("arn:aws:glue:%s:%s:integrationresourceproperty/%s",
		awsRegion(), awsAccountID(), strings.ReplaceAll(resourceArn, ":", "_"))
}

func handleGlueCreateCatalog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"Name"`
		CatalogInput struct {
			Description       string            `json:"Description"`
			Parameters        map[string]string `json:"Parameters"`
			FederatedCatalog  map[string]any    `json:"FederatedCatalog"`
			CatalogProperties map[string]any    `json:"CatalogProperties"`
		} `json:"CatalogInput"`
		Tags map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueCatalogs.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Catalog already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	cat := GlueCatalog{
		CatalogId:         req.Name,
		Name:              req.Name,
		ResourceArn:       glueCatalogArn(req.Name),
		Description:       req.CatalogInput.Description,
		Parameters:        req.CatalogInput.Parameters,
		FederatedCatalog:  req.CatalogInput.FederatedCatalog,
		CatalogProperties: req.CatalogInput.CatalogProperties,
		CreateTime:        now,
		UpdateTime:        now,
		Tags:              req.Tags,
	}
	glueCatalogs.Put(req.Name, cat)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetCatalog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId string `json:"CatalogId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cat, ok := glueCatalogs.Get(req.CatalogId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Catalog not found: "+req.CatalogId)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Catalog": glueCatalogWire(cat)})
}

func handleGlueGetCatalogs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentCatalogId string `json:"ParentCatalogId"`
		NextToken       string `json:"NextToken"`
		MaxResults      *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueCatalogs.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	wired := make([]map[string]any, 0, len(page))
	for _, c := range page {
		wired = append(wired, glueCatalogWire(c))
	}
	resp := map[string]any{"CatalogList": wired}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateCatalog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		CatalogInput struct {
			Description       string            `json:"Description"`
			Parameters        map[string]string `json:"Parameters"`
			FederatedCatalog  map[string]any    `json:"FederatedCatalog"`
			CatalogProperties map[string]any    `json:"CatalogProperties"`
		} `json:"CatalogInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	cat, ok := glueCatalogs.Get(req.CatalogId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Catalog not found: "+req.CatalogId)
		return
	}
	cat.Description = req.CatalogInput.Description
	cat.Parameters = req.CatalogInput.Parameters
	if req.CatalogInput.FederatedCatalog != nil {
		cat.FederatedCatalog = req.CatalogInput.FederatedCatalog
	}
	if req.CatalogInput.CatalogProperties != nil {
		cat.CatalogProperties = req.CatalogInput.CatalogProperties
	}
	cat.UpdateTime = glueEpochNow()
	glueCatalogs.Put(req.CatalogId, cat)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteCatalog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId string `json:"CatalogId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueCatalogs.Get(req.CatalogId); !ok {
		glueWriteError(w, "EntityNotFoundException", "Catalog not found: "+req.CatalogId)
		return
	}
	glueCatalogs.Delete(req.CatalogId)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// glueCatalogWire emits the Catalog output shape, omitting the Tags member which
// the SDK Catalog shape does not carry (tags ride GetTags).
func glueCatalogWire(c GlueCatalog) map[string]any {
	m := map[string]any{
		"CatalogId":   c.CatalogId,
		"Name":        c.Name,
		"ResourceArn": c.ResourceArn,
		"CreateTime":  c.CreateTime,
		"UpdateTime":  c.UpdateTime,
	}
	if c.Description != "" {
		m["Description"] = c.Description
	}
	if len(c.Parameters) > 0 {
		m["Parameters"] = c.Parameters
	}
	if len(c.FederatedCatalog) > 0 {
		m["FederatedCatalog"] = c.FederatedCatalog
	}
	if len(c.CatalogProperties) > 0 {
		m["CatalogProperties"] = c.CatalogProperties
	}
	return m
}

// glueTableOptimizerKey scopes an optimizer to its table + type.
func glueTableOptimizerKey(database, table, optType string) string {
	return database + "/" + table + "/" + optType
}

func handleGlueCreateTableOptimizer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId                   string                   `json:"CatalogId"`
		DatabaseName                string                   `json:"DatabaseName"`
		TableName                   string                   `json:"TableName"`
		Type                        string                   `json:"Type"`
		TableOptimizerConfiguration GlueTableOptimizerConfig `json:"TableOptimizerConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" || req.Type == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName, TableName and Type are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueTableOptimizerKey(req.DatabaseName, req.TableName, req.Type)
	if _, ok := glueTableOptimizers.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "Table optimizer already exists")
		return
	}
	opt := GlueTableOptimizer{
		CatalogId:     req.CatalogId,
		DatabaseName:  req.DatabaseName,
		TableName:     req.TableName,
		Type:          req.Type,
		Configuration: req.TableOptimizerConfiguration,
	}
	glueTableOptimizers.Put(key, opt)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetTableOptimizer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		Type         string `json:"Type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	opt, ok := glueTableOptimizers.Get(glueTableOptimizerKey(req.DatabaseName, req.TableName, req.Type))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table optimizer not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"CatalogId":      opt.CatalogId,
		"DatabaseName":   opt.DatabaseName,
		"TableName":      opt.TableName,
		"TableOptimizer": opt,
	})
}

func handleGlueBatchGetTableOptimizer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Entries []struct {
			CatalogId    string `json:"catalogId"`
			DatabaseName string `json:"databaseName"`
			TableName    string `json:"tableName"`
			Type         string `json:"type"`
		} `json:"Entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var found []map[string]any
	var failures []map[string]any
	for _, e := range req.Entries {
		opt, ok := glueTableOptimizers.Get(glueTableOptimizerKey(e.DatabaseName, e.TableName, e.Type))
		if ok {
			found = append(found, map[string]any{
				"catalogId":      e.CatalogId,
				"databaseName":   e.DatabaseName,
				"tableName":      e.TableName,
				"tableOptimizer": opt,
			})
		} else {
			failures = append(failures, map[string]any{
				"catalogId":    e.CatalogId,
				"databaseName": e.DatabaseName,
				"tableName":    e.TableName,
				"type":         e.Type,
				"error": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Table optimizer not found",
				},
			})
		}
	}
	resp := map[string]any{}
	if len(found) > 0 {
		resp["TableOptimizers"] = found
	}
	if len(failures) > 0 {
		resp["Failures"] = failures
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateTableOptimizer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId                   string                   `json:"CatalogId"`
		DatabaseName                string                   `json:"DatabaseName"`
		TableName                   string                   `json:"TableName"`
		Type                        string                   `json:"Type"`
		TableOptimizerConfiguration GlueTableOptimizerConfig `json:"TableOptimizerConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueTableOptimizerKey(req.DatabaseName, req.TableName, req.Type)
	opt, ok := glueTableOptimizers.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table optimizer not found")
		return
	}
	opt.Configuration = req.TableOptimizerConfiguration
	glueTableOptimizers.Put(key, opt)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteTableOptimizer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		Type         string `json:"Type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueTableOptimizerKey(req.DatabaseName, req.TableName, req.Type)
	if _, ok := glueTableOptimizers.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table optimizer not found")
		return
	}
	glueTableOptimizers.Delete(key)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListTableOptimizerRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		Type         string `json:"Type"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	opt, ok := glueTableOptimizers.Get(glueTableOptimizerKey(req.DatabaseName, req.TableName, req.Type))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table optimizer not found")
		return
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(opt.Runs, req.NextToken, maxR, 100)
	resp := map[string]any{
		"CatalogId":          opt.CatalogId,
		"DatabaseName":       opt.DatabaseName,
		"TableName":          opt.TableName,
		"TableOptimizerRuns": page,
	}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchGetCrawlers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CrawlerNames []string `json:"CrawlerNames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var found []GlueCrawler
	var notFound []string
	for _, name := range req.CrawlerNames {
		if c, ok := glueCrawlers.Get(name); ok {
			found = append(found, c)
		} else {
			notFound = append(notFound, name)
		}
	}
	resp := map[string]any{}
	if len(found) > 0 {
		resp["Crawlers"] = found
	}
	if len(notFound) > 0 {
		resp["CrawlersNotFound"] = notFound
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchGetJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobNames []string `json:"JobNames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var found []glueJobWire
	var notFound []string
	for _, name := range req.JobNames {
		if j, ok := glueJobs.Get(name); ok {
			found = append(found, glueJobWire{j})
		} else {
			notFound = append(notFound, name)
		}
	}
	resp := map[string]any{}
	if len(found) > 0 {
		resp["Jobs"] = found
	}
	if len(notFound) > 0 {
		resp["JobsNotFound"] = notFound
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchGetTriggers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TriggerNames []string `json:"TriggerNames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var found []GlueTrigger
	var notFound []string
	for _, name := range req.TriggerNames {
		if t, ok := glueTriggers.Get(name); ok {
			found = append(found, t)
		} else {
			notFound = append(notFound, name)
		}
	}
	resp := map[string]any{}
	if len(found) > 0 {
		resp["Triggers"] = found
	}
	if len(notFound) > 0 {
		resp["TriggersNotFound"] = notFound
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchGetWorkflows(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"Names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	var found []GlueWorkflow
	var missing []string
	for _, name := range req.Names {
		if wf, ok := glueWorkflows.Get(name); ok {
			found = append(found, wf)
		} else {
			missing = append(missing, name)
		}
	}
	resp := map[string]any{}
	if len(found) > 0 {
		resp["Workflows"] = found
	}
	if len(missing) > 0 {
		resp["MissingWorkflows"] = missing
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// handleGlueBatchGetCustomEntityTypes reads the (not-yet-populated) custom entity
// type store; with no CreateCustomEntityType slice all requested names are
// reported as not found — the faithful response shape for unknown names.
func handleGlueBatchGetCustomEntityTypes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"Names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	resp := map[string]any{}
	if len(req.Names) > 0 {
		resp["CustomEntityTypesNotFound"] = req.Names
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchDeleteConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId          string   `json:"CatalogId"`
		ConnectionNameList []string `json:"ConnectionNameList"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var succeeded []string
	errs := map[string]any{}
	for _, name := range req.ConnectionNameList {
		if _, ok := glueConnections.Get(name); !ok {
			errs[name] = map[string]any{
				"ErrorCode":    "EntityNotFoundException",
				"ErrorMessage": "Connection not found: " + name,
			}
			continue
		}
		glueConnections.Delete(name)
		succeeded = append(succeeded, name)
	}
	resp := map[string]any{}
	if len(succeeded) > 0 {
		resp["Succeeded"] = succeeded
	}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchUpdatePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		Entries      []struct {
			PartitionValueList []string           `json:"PartitionValueList"`
			PartitionInput     gluePartitionInput `json:"PartitionInput"`
		} `json:"Entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var errs []map[string]any
	for _, e := range req.Entries {
		oldKey := gluePartitionKey(req.DatabaseName, req.TableName, e.PartitionValueList)
		existing, ok := gluePartitions.Get(oldKey)
		if !ok {
			errs = append(errs, map[string]any{
				"PartitionValueList": e.PartitionValueList,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Partition not found",
				},
			})
			continue
		}
		updated := gluePartitionFromInput(req.DatabaseName, req.TableName, e.PartitionInput, existing.CreationTime)
		if !glueValuesEqual(e.PartitionInput.Values, e.PartitionValueList) {
			newKey := gluePartitionKey(req.DatabaseName, req.TableName, e.PartitionInput.Values)
			gluePartitions.Delete(oldKey)
			gluePartitions.Put(newKey, updated)
		} else {
			gluePartitions.Put(oldKey, updated)
		}
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func glueIntegrationWire(i GlueIntegration) map[string]any {
	m := map[string]any{
		"IntegrationName": i.IntegrationName,
		"IntegrationArn":  i.IntegrationArn,
		"SourceArn":       i.SourceArn,
		"TargetArn":       i.TargetArn,
		"Status":          i.Status,
		"CreateTime":      i.CreateTime,
	}
	if i.Description != "" {
		m["Description"] = i.Description
	}
	if i.KmsKeyId != "" {
		m["KmsKeyId"] = i.KmsKeyId
	}
	if i.DataFilter != "" {
		m["DataFilter"] = i.DataFilter
	}
	if len(i.AdditionalEncryptionContext) > 0 {
		m["AdditionalEncryptionContext"] = i.AdditionalEncryptionContext
	}
	if len(i.IntegrationConfig) > 0 {
		m["IntegrationConfig"] = i.IntegrationConfig
	}
	if len(i.Tags) > 0 {
		m["Tags"] = i.Tags
	}
	return m
}

func handleGlueCreateIntegration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationName             string            `json:"IntegrationName"`
		SourceArn                   string            `json:"SourceArn"`
		TargetArn                   string            `json:"TargetArn"`
		Description                 string            `json:"Description"`
		KmsKeyId                    string            `json:"KmsKeyId"`
		DataFilter                  string            `json:"DataFilter"`
		AdditionalEncryptionContext map[string]string `json:"AdditionalEncryptionContext"`
		IntegrationConfig           map[string]any    `json:"IntegrationConfig"`
		Tags                        []map[string]any  `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.IntegrationName == "" || req.SourceArn == "" || req.TargetArn == "" {
		glueWriteError(w, "InvalidInputException", "IntegrationName, SourceArn and TargetArn are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueIntegrations.Get(req.IntegrationName); ok {
		glueWriteError(w, "AlreadyExistsException", "Integration already exists: "+req.IntegrationName)
		return
	}
	integ := GlueIntegration{
		IntegrationName:             req.IntegrationName,
		IntegrationArn:              glueIntegrationArn(req.IntegrationName),
		SourceArn:                   req.SourceArn,
		TargetArn:                   req.TargetArn,
		Description:                 req.Description,
		Status:                      "ACTIVE",
		CreateTime:                  glueEpochNow(),
		KmsKeyId:                    req.KmsKeyId,
		DataFilter:                  req.DataFilter,
		AdditionalEncryptionContext: req.AdditionalEncryptionContext,
		IntegrationConfig:           req.IntegrationConfig,
		Tags:                        req.Tags,
	}
	glueIntegrations.Put(req.IntegrationName, integ)
	resp := glueIntegrationWire(integ)
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDescribeIntegrations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationIdentifier string `json:"IntegrationIdentifier"`
		Marker                string `json:"Marker"`
		MaxRecords            *int   `json:"MaxRecords"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var matched []GlueIntegration
	for _, integ := range glueIntegrations.List() {
		if req.IntegrationIdentifier == "" ||
			integ.IntegrationArn == req.IntegrationIdentifier ||
			integ.IntegrationName == req.IntegrationIdentifier {
			matched = append(matched, integ)
		}
	}
	if req.IntegrationIdentifier != "" && len(matched) == 0 {
		glueWriteError(w, "EntityNotFoundException", "Integration not found: "+req.IntegrationIdentifier)
		return
	}
	maxR := 0
	if req.MaxRecords != nil {
		maxR = *req.MaxRecords
	}
	page, nextTok := awsPage(matched, req.Marker, maxR, 100)
	wired := make([]map[string]any, 0, len(page))
	for _, integ := range page {
		wired = append(wired, glueIntegrationWire(integ))
	}
	resp := map[string]any{"Integrations": wired}
	if nextTok != "" {
		resp["Marker"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueModifyIntegration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationIdentifier string `json:"IntegrationIdentifier"`
		Description           string `json:"Description"`
		DataFilter            string `json:"DataFilter"`
		IntegrationName       string `json:"IntegrationName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	integ, ok := glueResolveIntegration(req.IntegrationIdentifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Integration not found: "+req.IntegrationIdentifier)
		return
	}
	if req.Description != "" {
		integ.Description = req.Description
	}
	if req.DataFilter != "" {
		integ.DataFilter = req.DataFilter
	}
	glueIntegrations.Put(integ.IntegrationName, integ)
	glueWriteJSON(w, http.StatusOK, glueIntegrationWire(integ))
}

func handleGlueDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IntegrationIdentifier string `json:"IntegrationIdentifier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	integ, ok := glueResolveIntegration(req.IntegrationIdentifier)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Integration not found: "+req.IntegrationIdentifier)
		return
	}
	glueIntegrations.Delete(integ.IntegrationName)
	integ.Status = "DELETING"
	resp := glueIntegrationWire(integ)
	delete(resp, "IntegrationConfig") // DeleteIntegrationResponse has no IntegrationConfig member.
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueResolveIntegration finds an integration by name or ARN. Caller holds glueMu.
func glueResolveIntegration(identifier string) (GlueIntegration, bool) {
	if integ, ok := glueIntegrations.Get(identifier); ok {
		return integ, true
	}
	for _, integ := range glueIntegrations.List() {
		if integ.IntegrationArn == identifier {
			return integ, true
		}
	}
	return GlueIntegration{}, false
}

func handleGlueDescribeInboundIntegrations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetArn  string `json:"TargetArn"`
		Marker     string `json:"Marker"`
		MaxRecords *int   `json:"MaxRecords"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var matched []map[string]any
	for _, integ := range glueIntegrations.List() {
		if req.TargetArn != "" && integ.TargetArn != req.TargetArn {
			continue
		}
		inbound := map[string]any{
			"SourceArn":      integ.SourceArn,
			"TargetArn":      integ.TargetArn,
			"IntegrationArn": integ.IntegrationArn,
			"Status":         integ.Status,
			"CreateTime":     integ.CreateTime,
		}
		if len(integ.IntegrationConfig) > 0 {
			inbound["IntegrationConfig"] = integ.IntegrationConfig
		}
		matched = append(matched, inbound)
	}
	maxR := 0
	if req.MaxRecords != nil {
		maxR = *req.MaxRecords
	}
	page, nextTok := awsPage(matched, req.Marker, maxR, 100)
	resp := map[string]any{"InboundIntegrations": page}
	if nextTok != "" {
		resp["Marker"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueCreateIntegrationResourceProperty(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn                string         `json:"ResourceArn"`
		SourceProcessingProperties map[string]any `json:"SourceProcessingProperties"`
		TargetProcessingProperties map[string]any `json:"TargetProcessingProperties"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ResourceArn == "" {
		glueWriteError(w, "InvalidInputException", "ResourceArn is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	prop := GlueIntegrationResourceProperty{
		ResourceArn:                req.ResourceArn,
		ResourcePropertyArn:        glueIntegResPropArn(req.ResourceArn),
		SourceProcessingProperties: req.SourceProcessingProperties,
		TargetProcessingProperties: req.TargetProcessingProperties,
	}
	glueIntegResProps.Put(req.ResourceArn, prop)
	glueWriteJSON(w, http.StatusOK, glueIntegResPropWire(prop))
}

func handleGlueGetIntegrationResourceProperty(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	prop, ok := glueIntegResProps.Get(req.ResourceArn)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Integration resource property not found: "+req.ResourceArn)
		return
	}
	glueWriteJSON(w, http.StatusOK, glueIntegResPropWire(prop))
}

func handleGlueUpdateIntegrationResourceProperty(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn                string         `json:"ResourceArn"`
		SourceProcessingProperties map[string]any `json:"SourceProcessingProperties"`
		TargetProcessingProperties map[string]any `json:"TargetProcessingProperties"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	prop, ok := glueIntegResProps.Get(req.ResourceArn)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Integration resource property not found: "+req.ResourceArn)
		return
	}
	if req.SourceProcessingProperties != nil {
		prop.SourceProcessingProperties = req.SourceProcessingProperties
	}
	if req.TargetProcessingProperties != nil {
		prop.TargetProcessingProperties = req.TargetProcessingProperties
	}
	glueIntegResProps.Put(req.ResourceArn, prop)
	glueWriteJSON(w, http.StatusOK, glueIntegResPropWire(prop))
}

func handleGlueDeleteIntegrationResourceProperty(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueIntegResProps.Get(req.ResourceArn); !ok {
		glueWriteError(w, "EntityNotFoundException", "Integration resource property not found: "+req.ResourceArn)
		return
	}
	glueIntegResProps.Delete(req.ResourceArn)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListIntegrationResourceProperties(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Marker     string `json:"Marker"`
		MaxRecords *int   `json:"MaxRecords"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueIntegResProps.List()
	maxR := 0
	if req.MaxRecords != nil {
		maxR = *req.MaxRecords
	}
	page, nextTok := awsPage(all, req.Marker, maxR, 100)
	wired := make([]map[string]any, 0, len(page))
	for _, p := range page {
		wired = append(wired, glueIntegResPropWire(p))
	}
	resp := map[string]any{"IntegrationResourcePropertyList": wired}
	if nextTok != "" {
		resp["Marker"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func glueIntegResPropWire(p GlueIntegrationResourceProperty) map[string]any {
	m := map[string]any{
		"ResourceArn":         p.ResourceArn,
		"ResourcePropertyArn": p.ResourcePropertyArn,
	}
	if len(p.SourceProcessingProperties) > 0 {
		m["SourceProcessingProperties"] = p.SourceProcessingProperties
	}
	if len(p.TargetProcessingProperties) > 0 {
		m["TargetProcessingProperties"] = p.TargetProcessingProperties
	}
	return m
}

func glueIntegTablePropKey(resourceArn, tableName string) string {
	return resourceArn + "\x1f" + tableName
}

func handleGlueCreateIntegrationTableProperties(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn       string         `json:"ResourceArn"`
		TableName         string         `json:"TableName"`
		SourceTableConfig map[string]any `json:"SourceTableConfig"`
		TargetTableConfig map[string]any `json:"TargetTableConfig"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ResourceArn == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "ResourceArn and TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	glueIntegTableProps.Put(glueIntegTablePropKey(req.ResourceArn, req.TableName), GlueIntegrationTableProperties{
		ResourceArn:       req.ResourceArn,
		TableName:         req.TableName,
		SourceTableConfig: req.SourceTableConfig,
		TargetTableConfig: req.TargetTableConfig,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetIntegrationTableProperties(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
		TableName   string `json:"TableName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	prop, ok := glueIntegTableProps.Get(glueIntegTablePropKey(req.ResourceArn, req.TableName))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Integration table properties not found")
		return
	}
	resp := map[string]any{
		"ResourceArn": prop.ResourceArn,
		"TableName":   prop.TableName,
	}
	if len(prop.SourceTableConfig) > 0 {
		resp["SourceTableConfig"] = prop.SourceTableConfig
	}
	if len(prop.TargetTableConfig) > 0 {
		resp["TargetTableConfig"] = prop.TargetTableConfig
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateIntegrationTableProperties(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn       string         `json:"ResourceArn"`
		TableName         string         `json:"TableName"`
		SourceTableConfig map[string]any `json:"SourceTableConfig"`
		TargetTableConfig map[string]any `json:"TargetTableConfig"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueIntegTablePropKey(req.ResourceArn, req.TableName)
	prop, ok := glueIntegTableProps.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Integration table properties not found")
		return
	}
	if req.SourceTableConfig != nil {
		prop.SourceTableConfig = req.SourceTableConfig
	}
	if req.TargetTableConfig != nil {
		prop.TargetTableConfig = req.TargetTableConfig
	}
	glueIntegTableProps.Put(key, prop)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteIntegrationTableProperties(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
		TableName   string `json:"TableName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueIntegTablePropKey(req.ResourceArn, req.TableName)
	if _, ok := glueIntegTableProps.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Integration table properties not found")
		return
	}
	glueIntegTableProps.Delete(key)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

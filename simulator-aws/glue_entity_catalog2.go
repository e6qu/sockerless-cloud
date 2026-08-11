package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/google/uuid"
)

// This file implements a further slice of the AWS Glue awsJson1.1 surface:
// custom entity types, usage profiles, the Glue Identity Center singleton,
// table search, schema/registry updates, schema-version metadata, schema diffs,
// job source-control sync, user-defined-function updates, resource-policy
// enumeration, and the code-generation helpers (mapping/plan/dataflow/script/
// dashboard). All CRUD operates on real sim.Stores; reads come back from the
// same stores writes go to. The Data Catalog stores defined in glue.go
// (glueTables, glueUDFs, glueSchemas, glueRegistries, glueSchemaVers,
// glueResourcePols) are read/written directly.

// GlueCustomEntityType models a custom sensitive-data pattern keyed by name.
type GlueCustomEntityType struct {
	Name         string            `json:"Name"`
	RegexString  string            `json:"RegexString"`
	ContextWords []string          `json:"ContextWords,omitempty"`
	Tags         map[string]string `json:"Tags,omitempty"`
}

// GlueUsageProfile models a Glue usage profile keyed by name. Configuration
// carries the SessionConfiguration / JobConfiguration maps verbatim.
type GlueUsageProfile struct {
	Name           string            `json:"Name"`
	Description    string            `json:"Description,omitempty"`
	Configuration  map[string]any    `json:"Configuration,omitempty"`
	CreatedOn      float64           `json:"CreatedOn"`
	LastModifiedOn float64           `json:"LastModifiedOn"`
	Tags           map[string]string `json:"Tags,omitempty"`
}

// GlueIdentityCenterConfig models the singleton Glue Identity Center config.
type GlueIdentityCenterConfig struct {
	ApplicationArn                string   `json:"ApplicationArn"`
	InstanceArn                   string   `json:"InstanceArn"`
	Scopes                        []string `json:"Scopes,omitempty"`
	UserBackgroundSessionsEnabled *bool    `json:"UserBackgroundSessionsEnabled,omitempty"`
}

// GlueSchemaVersionMetadataEntry stores one metadata value for a schema-version
// key. The metadata map is per schema-version-id: key -> ordered values.
type GlueSchemaVersionMetadataEntry struct {
	SchemaVersionId string  `json:"SchemaVersionId"`
	MetadataKey     string  `json:"MetadataKey"`
	MetadataValue   string  `json:"MetadataValue"`
	CreatedTime     float64 `json:"CreatedTime"`
}

var (
	glueCustomEntityTypes sim.Store[GlueCustomEntityType]
	glueUsageProfiles     sim.Store[GlueUsageProfile]
	glueIdentityCenter    sim.Store[GlueIdentityCenterConfig]
	glueSchemaVerMetadata sim.Store[GlueSchemaVersionMetadataEntry]
)

// glueIdentityCenterKey is the single store key for the Identity Center config.
const glueIdentityCenterKey = "default"

// glueSchemaVerMetadataKey keys one metadata entry by schema-version-id + key + value.
func glueSchemaVerMetadataKey(versionID, key, value string) string {
	return versionID + "\x00" + key + "\x00" + value
}

// registerGlueEntityCatalog2 registers this Glue sub-service's awsJson1.1 operations.
func registerGlueEntityCatalog2(r *sim.AWSRouter, srv *sim.Server) {
	glueCustomEntityTypes = sim.MakeStore[GlueCustomEntityType](srv.DB(), "glue_custom_entity_types")
	glueUsageProfiles = sim.MakeStore[GlueUsageProfile](srv.DB(), "glue_usage_profiles")
	glueIdentityCenter = sim.MakeStore[GlueIdentityCenterConfig](srv.DB(), "glue_identity_center")
	glueSchemaVerMetadata = sim.MakeStore[GlueSchemaVersionMetadataEntry](srv.DB(), "glue_schema_version_metadata")

	r.Register("AWSGlue.CreateCustomEntityType", handleGlueCreateCustomEntityType)
	r.Register("AWSGlue.GetCustomEntityType", handleGlueGetCustomEntityType)
	r.Register("AWSGlue.ListCustomEntityTypes", handleGlueListCustomEntityTypes)
	r.Register("AWSGlue.DeleteCustomEntityType", handleGlueDeleteCustomEntityType)

	r.Register("AWSGlue.CreateUsageProfile", handleGlueCreateUsageProfile)
	r.Register("AWSGlue.GetUsageProfile", handleGlueGetUsageProfile)
	r.Register("AWSGlue.ListUsageProfiles", handleGlueListUsageProfiles)
	r.Register("AWSGlue.UpdateUsageProfile", handleGlueUpdateUsageProfile)
	r.Register("AWSGlue.DeleteUsageProfile", handleGlueDeleteUsageProfile)

	r.Register("AWSGlue.CreateGlueIdentityCenterConfiguration", handleGlueCreateIdentityCenter)
	r.Register("AWSGlue.GetGlueIdentityCenterConfiguration", handleGlueGetIdentityCenter)
	r.Register("AWSGlue.UpdateGlueIdentityCenterConfiguration", handleGlueUpdateIdentityCenter)
	r.Register("AWSGlue.DeleteGlueIdentityCenterConfiguration", handleGlueDeleteIdentityCenter)

	r.Register("AWSGlue.SearchTables", handleGlueSearchTables)

	r.Register("AWSGlue.ListSchemas", handleGlueListSchemas)
	r.Register("AWSGlue.UpdateSchema", handleGlueUpdateSchema)
	r.Register("AWSGlue.UpdateRegistry", handleGlueUpdateRegistry)
	r.Register("AWSGlue.CheckSchemaVersionValidity", handleGlueCheckSchemaVersionValidity)
	r.Register("AWSGlue.PutSchemaVersionMetadata", handleGluePutSchemaVersionMetadata)
	r.Register("AWSGlue.QuerySchemaVersionMetadata", handleGlueQuerySchemaVersionMetadata)
	r.Register("AWSGlue.RemoveSchemaVersionMetadata", handleGlueRemoveSchemaVersionMetadata)
	r.Register("AWSGlue.GetSchemaVersionsDiff", handleGlueGetSchemaVersionsDiff)

	r.Register("AWSGlue.UpdateJobFromSourceControl", handleGlueUpdateJobFromSourceControl)
	r.Register("AWSGlue.UpdateSourceControlFromJob", handleGlueUpdateSourceControlFromJob)
	r.Register("AWSGlue.UpdateUserDefinedFunction", handleGlueUpdateUserDefinedFunction)
	r.Register("AWSGlue.GetResourcePolicies", handleGlueGetResourcePolicies)

	r.Register("AWSGlue.GetMapping", handleGlueGetMapping)
	r.Register("AWSGlue.GetPlan", handleGlueGetPlan)
	r.Register("AWSGlue.GetDataflowGraph", handleGlueGetDataflowGraph)
	r.Register("AWSGlue.GetDashboardUrl", handleGlueGetDashboardUrl)
	r.Register("AWSGlue.CreateScript", handleGlueCreateScript)
}

// ---------- Custom entity types ----------

func handleGlueCreateCustomEntityType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string            `json:"Name"`
		RegexString  string            `json:"RegexString"`
		ContextWords []string          `json:"ContextWords"`
		Tags         map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" || req.RegexString == "" {
		glueWriteError(w, "InvalidInputException", "Name and RegexString are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueCustomEntityTypes.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Custom entity type already exists: "+req.Name)
		return
	}
	glueCustomEntityTypes.Put(req.Name, GlueCustomEntityType{
		Name:         req.Name,
		RegexString:  req.RegexString,
		ContextWords: req.ContextWords,
		Tags:         req.Tags,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueGetCustomEntityType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cet, ok := glueCustomEntityTypes.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Custom entity type not found: "+req.Name)
		return
	}
	resp := map[string]any{
		"Name":        cet.Name,
		"RegexString": cet.RegexString,
	}
	if len(cet.ContextWords) > 0 {
		resp["ContextWords"] = cet.ContextWords
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListCustomEntityTypes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueCustomEntityTypes.List()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	out := make([]map[string]any, 0, len(page))
	for _, cet := range page {
		item := map[string]any{
			"Name":        cet.Name,
			"RegexString": cet.RegexString,
		}
		if len(cet.ContextWords) > 0 {
			item["ContextWords"] = cet.ContextWords
		}
		out = append(out, item)
	}
	resp := map[string]any{"CustomEntityTypes": out}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteCustomEntityType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueCustomEntityTypes.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Custom entity type not found: "+req.Name)
		return
	}
	glueCustomEntityTypes.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

// ---------- Usage profiles ----------

func handleGlueCreateUsageProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string            `json:"Name"`
		Description   string            `json:"Description"`
		Configuration map[string]any    `json:"Configuration"`
		Tags          map[string]string `json:"Tags"`
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

	if _, ok := glueUsageProfiles.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Usage profile already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	glueUsageProfiles.Put(req.Name, GlueUsageProfile{
		Name:           req.Name,
		Description:    req.Description,
		Configuration:  req.Configuration,
		CreatedOn:      now,
		LastModifiedOn: now,
		Tags:           req.Tags,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueGetUsageProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	up, ok := glueUsageProfiles.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Usage profile not found: "+req.Name)
		return
	}
	resp := map[string]any{
		"Name":           up.Name,
		"CreatedOn":      up.CreatedOn,
		"LastModifiedOn": up.LastModifiedOn,
	}
	if up.Description != "" {
		resp["Description"] = up.Description
	}
	if len(up.Configuration) > 0 {
		resp["Configuration"] = up.Configuration
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListUsageProfiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueUsageProfiles.List()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	out := make([]map[string]any, 0, len(page))
	for _, up := range page {
		item := map[string]any{
			"Name":           up.Name,
			"CreatedOn":      up.CreatedOn,
			"LastModifiedOn": up.LastModifiedOn,
		}
		if up.Description != "" {
			item["Description"] = up.Description
		}
		out = append(out, item)
	}
	resp := map[string]any{"Profiles": out}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateUsageProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string         `json:"Name"`
		Description   string         `json:"Description"`
		Configuration map[string]any `json:"Configuration"`
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

	up, ok := glueUsageProfiles.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Usage profile not found: "+req.Name)
		return
	}
	up.Description = req.Description
	up.Configuration = req.Configuration
	up.LastModifiedOn = glueEpochNow()
	glueUsageProfiles.Put(req.Name, up)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueDeleteUsageProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueUsageProfiles.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Usage profile not found: "+req.Name)
		return
	}
	glueUsageProfiles.Delete(req.Name)
	// DeleteUsageProfileResponse has no members.
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// ---------- Glue Identity Center configuration (singleton) ----------

func handleGlueCreateIdentityCenter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceArn                   string   `json:"InstanceArn"`
		Scopes                        []string `json:"Scopes"`
		UserBackgroundSessionsEnabled *bool    `json:"UserBackgroundSessionsEnabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueIdentityCenter.Get(glueIdentityCenterKey); ok {
		glueWriteError(w, "AlreadyExistsException", "Glue Identity Center configuration already exists")
		return
	}
	appArn := glueGlueArn("application/" + uuid.NewString())
	glueIdentityCenter.Put(glueIdentityCenterKey, GlueIdentityCenterConfig{
		ApplicationArn:                appArn,
		InstanceArn:                   req.InstanceArn,
		Scopes:                        req.Scopes,
		UserBackgroundSessionsEnabled: req.UserBackgroundSessionsEnabled,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"ApplicationArn": appArn})
}

func handleGlueGetIdentityCenter(w http.ResponseWriter, r *http.Request) {
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		glueWriteError(w, "InvalidInputException", "failed to read body")
		return
	}
	cfg, ok := glueIdentityCenter.Get(glueIdentityCenterKey)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glue Identity Center configuration not found")
		return
	}
	resp := map[string]any{
		"ApplicationArn": cfg.ApplicationArn,
		"InstanceArn":    cfg.InstanceArn,
	}
	if len(cfg.Scopes) > 0 {
		resp["Scopes"] = cfg.Scopes
	}
	if cfg.UserBackgroundSessionsEnabled != nil {
		resp["UserBackgroundSessionsEnabled"] = *cfg.UserBackgroundSessionsEnabled
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateIdentityCenter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scopes                        []string `json:"Scopes"`
		ScopesToRemove                []string `json:"ScopesToRemove"`
		UserBackgroundSessionsEnabled *bool    `json:"UserBackgroundSessionsEnabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	cfg, ok := glueIdentityCenter.Get(glueIdentityCenterKey)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Glue Identity Center configuration not found")
		return
	}
	if len(req.Scopes) > 0 {
		cfg.Scopes = mergeScopes(cfg.Scopes, req.Scopes)
	}
	if len(req.ScopesToRemove) > 0 {
		cfg.Scopes = removeScopes(cfg.Scopes, req.ScopesToRemove)
	}
	if req.UserBackgroundSessionsEnabled != nil {
		cfg.UserBackgroundSessionsEnabled = req.UserBackgroundSessionsEnabled
	}
	glueIdentityCenter.Put(glueIdentityCenterKey, cfg)
	// UpdateGlueIdentityCenterConfigurationResponse has no members.
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteIdentityCenter(w http.ResponseWriter, r *http.Request) {
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		glueWriteError(w, "InvalidInputException", "failed to read body")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueIdentityCenter.Get(glueIdentityCenterKey); !ok {
		glueWriteError(w, "EntityNotFoundException", "Glue Identity Center configuration not found")
		return
	}
	glueIdentityCenter.Delete(glueIdentityCenterKey)
	// DeleteGlueIdentityCenterConfigurationResponse has no members.
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func mergeScopes(existing, add []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+len(add))
	for _, s := range existing {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range add {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func removeScopes(existing, remove []string) []string {
	drop := map[string]bool{}
	for _, s := range remove {
		drop[s] = true
	}
	out := make([]string, 0, len(existing))
	for _, s := range existing {
		if !drop[s] {
			out = append(out, s)
		}
	}
	return out
}

// ---------- SearchTables ----------

func handleGlueSearchTables(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SearchText string `json:"SearchText"`
		Filters    []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Filters"`
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	search := strings.ToLower(strings.TrimSpace(req.SearchText))
	var matched []GlueTable
	for _, t := range glueTables.List() {
		if !glueTableMatchesSearch(t, search, req.Filters) {
			continue
		}
		matched = append(matched, t)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].DatabaseName != matched[j].DatabaseName {
			return matched[i].DatabaseName < matched[j].DatabaseName
		}
		return matched[i].Name < matched[j].Name
	})
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(matched, req.NextToken, maxR, 100)
	resp := map[string]any{"TableList": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueTableMatchesSearch applies the free-text search and Key/Value filters to a
// table. SearchText fuzzy-matches the table or database name; filters do an
// exact match on the supported keys (Name, DatabaseName, TableType).
func glueTableMatchesSearch(t GlueTable, search string, filters []struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}) bool {
	if search != "" {
		hay := strings.ToLower(t.Name + " " + t.DatabaseName)
		if !strings.Contains(hay, search) {
			return false
		}
	}
	for _, f := range filters {
		switch f.Key {
		case "name", "Name":
			if t.Name != f.Value {
				return false
			}
		case "databaseName", "DatabaseName":
			if t.DatabaseName != f.Value {
				return false
			}
		case "tableType", "TableType":
			if t.TableType != f.Value {
				return false
			}
		default:
			// Unknown filter key matches nothing, mirroring the real
			// service which only indexes known properties.
			return false
		}
	}
	return true
}

// ---------- Schema / registry updates ----------

func handleGlueListSchemas(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId *struct {
			RegistryName string `json:"RegistryName"`
			RegistryArn  string `json:"RegistryArn"`
		} `json:"RegistryId"`
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	registryFilter := ""
	if req.RegistryId != nil {
		registryFilter = glueRegistryName(req.RegistryId.RegistryName, req.RegistryId.RegistryArn)
		if registryFilter != "" {
			if _, ok := glueRegistries.Get(registryFilter); !ok && registryFilter != "default-registry" {
				glueWriteError(w, "EntityNotFoundException", "Registry not found: "+registryFilter)
				return
			}
		}
	}

	var schemas []GlueSchema
	for _, sc := range glueSchemas.List() {
		if registryFilter != "" && sc.RegistryName != registryFilter {
			continue
		}
		schemas = append(schemas, sc)
	}
	sort.Slice(schemas, func(i, j int) bool {
		if schemas[i].RegistryName != schemas[j].RegistryName {
			return schemas[i].RegistryName < schemas[j].RegistryName
		}
		return schemas[i].SchemaName < schemas[j].SchemaName
	})
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(schemas, req.NextToken, maxR, 100)
	out := make([]map[string]any, 0, len(page))
	for _, sc := range page {
		item := map[string]any{
			"RegistryName": sc.RegistryName,
			"SchemaName":   sc.SchemaName,
			"SchemaArn":    sc.SchemaArn,
			"SchemaStatus": sc.SchemaStatus,
			"CreatedTime":  sc.CreatedTime,
			"UpdatedTime":  sc.UpdatedTime,
		}
		if sc.Description != "" {
			item["Description"] = sc.Description
		}
		out = append(out, item)
	}
	resp := map[string]any{"Schemas": out}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateSchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		Compatibility string `json:"Compatibility"`
		Description   string `json:"Description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema not found")
		return
	}
	if req.Compatibility != "" {
		sc.Compatibility = req.Compatibility
	}
	if req.Description != "" {
		sc.Description = req.Description
	}
	sc.UpdatedTime = glueRFC3339()
	glueSchemas.Put(glueSchemaKey(sc.RegistryName, sc.SchemaName), sc)
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SchemaArn":    sc.SchemaArn,
		"SchemaName":   sc.SchemaName,
		"RegistryName": sc.RegistryName,
	})
}

func handleGlueUpdateRegistry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId struct {
			RegistryName string `json:"RegistryName"`
			RegistryArn  string `json:"RegistryArn"`
		} `json:"RegistryId"`
		Description string `json:"Description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	name := glueRegistryName(req.RegistryId.RegistryName, req.RegistryId.RegistryArn)
	reg, ok := glueRegistries.Get(name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Registry not found: "+name)
		return
	}
	reg.Description = req.Description
	reg.UpdatedTime = glueRFC3339()
	glueRegistries.Put(name, reg)
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"RegistryName": reg.RegistryName,
		"RegistryArn":  reg.RegistryArn,
	})
}

func handleGlueCheckSchemaVersionValidity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DataFormat       string `json:"DataFormat"`
		SchemaDefinition string `json:"SchemaDefinition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DataFormat == "" || req.SchemaDefinition == "" {
		glueWriteError(w, "InvalidInputException", "DataFormat and SchemaDefinition are required")
		return
	}
	valid, errMsg := glueValidateSchemaDefinition(req.DataFormat, req.SchemaDefinition)
	resp := map[string]any{"Valid": valid}
	if !valid {
		resp["Error"] = errMsg
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueValidateSchemaDefinition validates a schema definition for the supplied
// data format. AVRO and JSON definitions must parse as JSON; PROTOBUF
// definitions must be non-empty text.
func glueValidateSchemaDefinition(dataFormat, def string) (bool, string) {
	switch strings.ToUpper(dataFormat) {
	case "AVRO", "JSON":
		var v any
		if err := json.Unmarshal([]byte(def), &v); err != nil {
			return false, fmt.Sprintf("Schema definition is not valid %s: %v", strings.ToUpper(dataFormat), err)
		}
		return true, ""
	case "PROTOBUF":
		if strings.TrimSpace(def) == "" {
			return false, "Schema definition is empty"
		}
		return true, ""
	default:
		return false, "Unsupported data format: " + dataFormat
	}
}

// ---------- Schema version metadata ----------

func handleGluePutSchemaVersionMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId *struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		SchemaVersionId     string `json:"SchemaVersionId"`
		SchemaVersionNumber *struct {
			LatestVersion bool  `json:"LatestVersion"`
			VersionNumber int64 `json:"VersionNumber"`
		} `json:"SchemaVersionNumber"`
		MetadataKeyValue struct {
			MetadataKey   string `json:"MetadataKey"`
			MetadataValue string `json:"MetadataValue"`
		} `json:"MetadataKeyValue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.MetadataKeyValue.MetadataKey == "" {
		glueWriteError(w, "InvalidInputException", "MetadataKeyValue.MetadataKey is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var schemaIDArn, schemaIDName, schemaIDReg string
	if req.SchemaId != nil {
		schemaIDArn, schemaIDName, schemaIDReg = req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName
	}
	var verNum int64
	var verLatest bool
	if req.SchemaVersionNumber != nil {
		verNum, verLatest = req.SchemaVersionNumber.VersionNumber, req.SchemaVersionNumber.LatestVersion
	}
	ver, ok := glueResolveSchemaVersion(req.SchemaVersionId, schemaIDArn, schemaIDName, schemaIDReg, verNum, verLatest)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema version not found")
		return
	}
	key := glueSchemaVerMetadataKey(ver.SchemaVersionId, req.MetadataKeyValue.MetadataKey, req.MetadataKeyValue.MetadataValue)
	if _, exists := glueSchemaVerMetadata.Get(key); exists {
		glueWriteError(w, "AlreadyExistsException", "Metadata key/value already exists")
		return
	}
	glueSchemaVerMetadata.Put(key, GlueSchemaVersionMetadataEntry{
		SchemaVersionId: ver.SchemaVersionId,
		MetadataKey:     req.MetadataKeyValue.MetadataKey,
		MetadataValue:   req.MetadataKeyValue.MetadataValue,
		CreatedTime:     glueEpochNow(),
	})
	sc, _ := glueResolveSchema(ver.SchemaArn, "", "")
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SchemaArn":       ver.SchemaArn,
		"SchemaName":      ver.SchemaName,
		"RegistryName":    sc.RegistryName,
		"LatestVersion":   ver.VersionNumber == sc.LatestSchemaVersion,
		"VersionNumber":   ver.VersionNumber,
		"SchemaVersionId": ver.SchemaVersionId,
		"MetadataKey":     req.MetadataKeyValue.MetadataKey,
		"MetadataValue":   req.MetadataKeyValue.MetadataValue,
	})
}

func handleGlueQuerySchemaVersionMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId *struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		SchemaVersionId     string `json:"SchemaVersionId"`
		SchemaVersionNumber *struct {
			LatestVersion bool  `json:"LatestVersion"`
			VersionNumber int64 `json:"VersionNumber"`
		} `json:"SchemaVersionNumber"`
		MetadataList []struct {
			MetadataKey   string `json:"MetadataKey"`
			MetadataValue string `json:"MetadataValue"`
		} `json:"MetadataList"`
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var schemaIDArn, schemaIDName, schemaIDReg string
	if req.SchemaId != nil {
		schemaIDArn, schemaIDName, schemaIDReg = req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName
	}
	var verNum int64
	var verLatest bool
	if req.SchemaVersionNumber != nil {
		verNum, verLatest = req.SchemaVersionNumber.VersionNumber, req.SchemaVersionNumber.LatestVersion
	}
	ver, ok := glueResolveSchemaVersion(req.SchemaVersionId, schemaIDArn, schemaIDName, schemaIDReg, verNum, verLatest)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema version not found")
		return
	}

	keyFilter := map[string]bool{}
	for _, m := range req.MetadataList {
		keyFilter[m.MetadataKey] = true
	}

	// Group entries by metadata key. The first entry for a key is the primary
	// MetadataValue; subsequent ones land in OtherMetadataValueList.
	type group struct {
		primary GlueSchemaVersionMetadataEntry
		others  []GlueSchemaVersionMetadataEntry
		hasPrim bool
	}
	groups := map[string]*group{}
	var order []string
	for _, e := range glueSchemaVerMetadata.List() {
		if e.SchemaVersionId != ver.SchemaVersionId {
			continue
		}
		if len(keyFilter) > 0 && !keyFilter[e.MetadataKey] {
			continue
		}
		g := groups[e.MetadataKey]
		if g == nil {
			g = &group{}
			groups[e.MetadataKey] = g
			order = append(order, e.MetadataKey)
		}
		if !g.hasPrim {
			g.primary = e
			g.hasPrim = true
		} else {
			g.others = append(g.others, e)
		}
	}
	sort.Strings(order)
	metaMap := map[string]any{}
	for _, k := range order {
		g := groups[k]
		entry := map[string]any{
			"MetadataValue": g.primary.MetadataValue,
			"CreatedTime":   fmt.Sprintf("%d", int64(g.primary.CreatedTime)),
		}
		if len(g.others) > 0 {
			ov := make([]map[string]any, 0, len(g.others))
			for _, o := range g.others {
				ov = append(ov, map[string]any{
					"MetadataValue": o.MetadataValue,
					"CreatedTime":   fmt.Sprintf("%d", int64(o.CreatedTime)),
				})
			}
			entry["OtherMetadataValueList"] = ov
		}
		metaMap[k] = entry
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"MetadataInfoMap": metaMap,
		"SchemaVersionId": ver.SchemaVersionId,
	})
}

func handleGlueRemoveSchemaVersionMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId *struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		SchemaVersionId     string `json:"SchemaVersionId"`
		SchemaVersionNumber *struct {
			LatestVersion bool  `json:"LatestVersion"`
			VersionNumber int64 `json:"VersionNumber"`
		} `json:"SchemaVersionNumber"`
		MetadataKeyValue struct {
			MetadataKey   string `json:"MetadataKey"`
			MetadataValue string `json:"MetadataValue"`
		} `json:"MetadataKeyValue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var schemaIDArn, schemaIDName, schemaIDReg string
	if req.SchemaId != nil {
		schemaIDArn, schemaIDName, schemaIDReg = req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName
	}
	var verNum int64
	var verLatest bool
	if req.SchemaVersionNumber != nil {
		verNum, verLatest = req.SchemaVersionNumber.VersionNumber, req.SchemaVersionNumber.LatestVersion
	}
	ver, ok := glueResolveSchemaVersion(req.SchemaVersionId, schemaIDArn, schemaIDName, schemaIDReg, verNum, verLatest)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema version not found")
		return
	}
	key := glueSchemaVerMetadataKey(ver.SchemaVersionId, req.MetadataKeyValue.MetadataKey, req.MetadataKeyValue.MetadataValue)
	if _, exists := glueSchemaVerMetadata.Get(key); !exists {
		glueWriteError(w, "EntityNotFoundException", "Metadata key/value not found")
		return
	}
	glueSchemaVerMetadata.Delete(key)
	sc, _ := glueResolveSchema(ver.SchemaArn, "", "")
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SchemaArn":       ver.SchemaArn,
		"SchemaName":      ver.SchemaName,
		"RegistryName":    sc.RegistryName,
		"LatestVersion":   ver.VersionNumber == sc.LatestSchemaVersion,
		"VersionNumber":   ver.VersionNumber,
		"SchemaVersionId": ver.SchemaVersionId,
		"MetadataKey":     req.MetadataKeyValue.MetadataKey,
		"MetadataValue":   req.MetadataKeyValue.MetadataValue,
	})
}

// glueResolveSchemaVersion resolves a schema version from an explicit
// SchemaVersionId, or from a SchemaId + version number (or latest).
func glueResolveSchemaVersion(versionID, schemaArn, schemaName, registryName string, versionNumber int64, latest bool) (GlueSchemaVersion, bool) {
	if versionID != "" {
		return glueSchemaVers.Get(versionID)
	}
	sc, ok := glueResolveSchema(schemaArn, schemaName, registryName)
	if !ok {
		return GlueSchemaVersion{}, false
	}
	versions := glueSchemaVersionsFor(sc.SchemaArn)
	if len(versions) == 0 {
		return GlueSchemaVersion{}, false
	}
	if versionNumber > 0 && !latest {
		for _, v := range versions {
			if v.VersionNumber == versionNumber {
				return v, true
			}
		}
		return GlueSchemaVersion{}, false
	}
	return versions[len(versions)-1], true
}

func handleGlueGetSchemaVersionsDiff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		FirstSchemaVersionNumber struct {
			LatestVersion bool  `json:"LatestVersion"`
			VersionNumber int64 `json:"VersionNumber"`
		} `json:"FirstSchemaVersionNumber"`
		SecondSchemaVersionNumber struct {
			LatestVersion bool  `json:"LatestVersion"`
			VersionNumber int64 `json:"VersionNumber"`
		} `json:"SecondSchemaVersionNumber"`
		SchemaDiffType string `json:"SchemaDiffType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	first, ok := glueResolveSchemaVersion("", req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName, req.FirstSchemaVersionNumber.VersionNumber, req.FirstSchemaVersionNumber.LatestVersion)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "First schema version not found")
		return
	}
	second, ok := glueResolveSchemaVersion("", req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName, req.SecondSchemaVersionNumber.VersionNumber, req.SecondSchemaVersionNumber.LatestVersion)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Second schema version not found")
		return
	}
	diff := glueSchemaDiff(first.SchemaDefinition, second.SchemaDefinition)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Diff": diff})
}

// glueSchemaDiff produces a JsonPatch-format string describing the difference
// between two schema definitions. Identical definitions yield "[]".
func glueSchemaDiff(first, second string) string {
	if first == second {
		return "[]"
	}
	ops := []map[string]any{
		{"op": "replace", "path": "", "value": json.RawMessage(glueAsJSONValue(second))},
	}
	b, err := json.Marshal(ops)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// glueAsJSONValue returns s as a JSON value if it parses, otherwise as a JSON
// string literal.
func glueAsJSONValue(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}

// ---------- Source control sync ----------

func handleGlueUpdateJobFromSourceControl(w http.ResponseWriter, r *http.Request) {
	glueSourceControlSync(w, r)
}

func handleGlueUpdateSourceControlFromJob(w http.ResponseWriter, r *http.Request) {
	glueSourceControlSync(w, r)
}

// glueSourceControlSync handles both directions of source-control sync. Both
// operations take the same input shape and return only the JobName; the sync
// records the source-control link onto the job's DefaultArguments so a
// subsequent GetJob reads it back.
func glueSourceControlSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName         string `json:"JobName"`
		Provider        string `json:"Provider"`
		RepositoryName  string `json:"RepositoryName"`
		RepositoryOwner string `json:"RepositoryOwner"`
		BranchName      string `json:"BranchName"`
		Folder          string `json:"Folder"`
		CommitId        string `json:"CommitId"`
		AuthStrategy    string `json:"AuthStrategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.JobName == "" {
		glueWriteError(w, "InvalidInputException", "JobName is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	job, ok := glueJobs.Get(req.JobName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Job not found: "+req.JobName)
		return
	}
	if job.DefaultArguments == nil {
		job.DefaultArguments = map[string]string{}
	}
	if req.Provider != "" {
		job.DefaultArguments["--source-control-provider"] = req.Provider
	}
	if req.RepositoryName != "" {
		job.DefaultArguments["--source-control-repository"] = req.RepositoryName
	}
	if req.RepositoryOwner != "" {
		job.DefaultArguments["--source-control-owner"] = req.RepositoryOwner
	}
	if req.BranchName != "" {
		job.DefaultArguments["--source-control-branch"] = req.BranchName
	}
	if req.Folder != "" {
		job.DefaultArguments["--source-control-folder"] = req.Folder
	}
	if req.CommitId != "" {
		job.DefaultArguments["--source-control-commit"] = req.CommitId
	}
	job.LastModifiedOn = glueEpochNow()
	glueJobs.Put(req.JobName, job)
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobName": req.JobName})
}

// ---------- User-defined function update ----------

func handleGlueUpdateUserDefinedFunction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName  string `json:"DatabaseName"`
		FunctionName  string `json:"FunctionName"`
		FunctionInput struct {
			FunctionName string           `json:"FunctionName"`
			ClassName    string           `json:"ClassName"`
			OwnerName    string           `json:"OwnerName"`
			OwnerType    string           `json:"OwnerType"`
			ResourceUris []map[string]any `json:"ResourceUris"`
		} `json:"FunctionInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.FunctionName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and FunctionName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueUDFKey(req.DatabaseName, req.FunctionName)
	udf, ok := glueUDFs.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "User-defined function not found: "+req.FunctionName)
		return
	}
	if req.FunctionInput.ClassName != "" {
		udf.ClassName = req.FunctionInput.ClassName
	}
	if req.FunctionInput.OwnerName != "" {
		udf.OwnerName = req.FunctionInput.OwnerName
	}
	if req.FunctionInput.OwnerType != "" {
		udf.OwnerType = req.FunctionInput.OwnerType
	}
	if req.FunctionInput.ResourceUris != nil {
		udf.ResourceUris = req.FunctionInput.ResourceUris
	}
	glueUDFs.Put(key, udf)
	// UpdateUserDefinedFunctionResponse has no members.
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// ---------- Resource policies ----------

func handleGlueGetResourcePolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	all := glueResourcePols.List()
	sort.Slice(all, func(i, j int) bool { return all[i].CreateTime < all[j].CreateTime })
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	out := make([]map[string]any, 0, len(page))
	for _, rp := range page {
		out = append(out, map[string]any{
			"PolicyInJson": rp.PolicyInJson,
			"PolicyHash":   rp.PolicyHash,
			"CreateTime":   rp.CreateTime,
			"UpdateTime":   rp.UpdateTime,
		})
	}
	resp := map[string]any{"GetResourcePoliciesResponseList": out}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// ---------- Code generation helpers ----------

func handleGlueGetMapping(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source struct {
			DatabaseName string `json:"DatabaseName"`
			TableName    string `json:"TableName"`
		} `json:"Source"`
		Sinks []struct {
			DatabaseName string `json:"DatabaseName"`
			TableName    string `json:"TableName"`
		} `json:"Sinks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Source.DatabaseName == "" || req.Source.TableName == "" {
		glueWriteError(w, "InvalidInputException", "Source.DatabaseName and Source.TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	src, ok := glueTables.Get(glueTableKey(req.Source.DatabaseName, req.Source.TableName))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Source table not found: "+req.Source.TableName)
		return
	}
	target := req.Source
	if len(req.Sinks) > 0 {
		target = req.Sinks[0]
	}
	mapping := glueColumnMapping(src, req.Source.TableName, target.TableName)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Mapping": mapping})
}

// glueColumnMapping derives a 1:1 MappingEntry list from a source table's
// StorageDescriptor columns to the named target table.
func glueColumnMapping(src GlueTable, sourceTable, targetTable string) []map[string]any {
	mapping := make([]map[string]any, 0)
	for _, col := range glueTableColumns(src) {
		mapping = append(mapping, map[string]any{
			"SourceTable": sourceTable,
			"SourcePath":  col.name,
			"SourceType":  col.typ,
			"TargetTable": targetTable,
			"TargetPath":  col.name,
			"TargetType":  col.typ,
		})
	}
	return mapping
}

type glueColumn struct {
	name string
	typ  string
}

// glueTableColumns extracts the (name, type) columns from a table's
// StorageDescriptor.Columns list.
func glueTableColumns(t GlueTable) []glueColumn {
	var cols []glueColumn
	if t.StorageDescriptor == nil {
		return cols
	}
	raw, ok := t.StorageDescriptor["Columns"].([]any)
	if !ok {
		return cols
	}
	for _, c := range raw {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		name, _ := cm["Name"].(string)
		typ, _ := cm["Type"].(string)
		if name == "" {
			continue
		}
		if typ == "" {
			typ = "string"
		}
		cols = append(cols, glueColumn{name: name, typ: typ})
	}
	return cols
}

func handleGlueGetPlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mapping []struct {
			SourceTable string `json:"SourceTable"`
			SourcePath  string `json:"SourcePath"`
			TargetTable string `json:"TargetTable"`
			TargetPath  string `json:"TargetPath"`
		} `json:"Mapping"`
		Source struct {
			DatabaseName string `json:"DatabaseName"`
			TableName    string `json:"TableName"`
		} `json:"Source"`
		Language string `json:"Language"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var applyArgs []string
	for _, m := range req.Mapping {
		applyArgs = append(applyArgs, fmt.Sprintf("(\"%s\", \"%s\")", m.SourcePath, m.TargetPath))
	}
	python := glueRenderPython(req.Source.DatabaseName, req.Source.TableName, applyArgs)
	resp := map[string]any{"PythonScript": python}
	if strings.EqualFold(req.Language, "SCALA") {
		resp["ScalaCode"] = glueRenderScala(req.Source.DatabaseName, req.Source.TableName)
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func glueRenderPython(database, table string, applyMappings []string) string {
	var b strings.Builder
	b.WriteString("import sys\n")
	b.WriteString("from awsglue.transforms import *\n")
	b.WriteString("from awsglue.context import GlueContext\n")
	b.WriteString("from pyspark.context import SparkContext\n\n")
	b.WriteString("glueContext = GlueContext(SparkContext.getOrCreate())\n")
	fmt.Fprintf(&b, "datasource0 = glueContext.create_dynamic_frame.from_catalog(database = \"%s\", table_name = \"%s\")\n", database, table)
	fmt.Fprintf(&b, "applymapping1 = ApplyMapping.apply(frame = datasource0, mappings = [%s])\n", strings.Join(applyMappings, ", "))
	return b.String()
}

func glueRenderScala(database, table string) string {
	return fmt.Sprintf("import com.amazonaws.services.glue.GlueContext\nval datasource0 = glueContext.getCatalogSource(database = \"%s\", tableName = \"%s\").getDynamicFrame()\n", database, table)
}

func handleGlueGetDataflowGraph(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PythonScript string `json:"PythonScript"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	nodes, edges := glueParseScriptToDag(req.PythonScript)
	resp := map[string]any{}
	if len(nodes) > 0 {
		resp["DagNodes"] = nodes
	}
	if len(edges) > 0 {
		resp["DagEdges"] = edges
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueParseScriptToDag derives a code-generation DAG from a Python script. Each
// assignment of the form `name = Transform.apply(...)` becomes a node; nodes are
// chained by sequential edges.
func glueParseScriptToDag(script string) (nodes []map[string]any, edges []map[string]any) {
	var ids []string
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		eq := strings.Index(line, "=")
		if eq <= 0 || !strings.Contains(line, ".apply(") && !strings.Contains(line, ".from_catalog(") {
			continue
		}
		id := strings.TrimSpace(line[:eq])
		if id == "" || strings.Contains(id, " ") {
			continue
		}
		nodeType := "Transform"
		if strings.Contains(line, "from_catalog") {
			nodeType = "DataSource"
		}
		nodes = append(nodes, map[string]any{
			"Id":       id,
			"NodeType": nodeType,
			"Args":     []map[string]any{},
		})
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		edges = append(edges, map[string]any{
			"Source": ids[i-1],
			"Target": ids[i],
		})
	}
	return nodes, edges
}

func handleGlueGetDashboardUrl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceId   string `json:"ResourceId"`
		ResourceType string `json:"ResourceType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ResourceId == "" || req.ResourceType == "" {
		glueWriteError(w, "InvalidInputException", "ResourceId and ResourceType are required")
		return
	}
	url := fmt.Sprintf("https://us-east-1.console.aws.amazon.com/gluestudio/home?region=us-east-1#/monitoring/%s/%s",
		strings.ToLower(req.ResourceType), req.ResourceId)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Url": url})
}

func handleGlueCreateScript(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DagNodes []struct {
			Id       string `json:"Id"`
			NodeType string `json:"NodeType"`
			Args     []struct {
				Name  string `json:"Name"`
				Value string `json:"Value"`
			} `json:"Args"`
		} `json:"DagNodes"`
		DagEdges []struct {
			Source string `json:"Source"`
			Target string `json:"Target"`
		} `json:"DagEdges"`
		Language string `json:"Language"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var b strings.Builder
	b.WriteString("import sys\n")
	b.WriteString("from awsglue.transforms import *\n")
	b.WriteString("from awsglue.context import GlueContext\n")
	b.WriteString("from pyspark.context import SparkContext\n\n")
	b.WriteString("glueContext = GlueContext(SparkContext.getOrCreate())\n")
	for _, n := range req.DagNodes {
		args := make([]string, 0, len(n.Args))
		for _, a := range n.Args {
			args = append(args, fmt.Sprintf("%s = \"%s\"", a.Name, a.Value))
		}
		fmt.Fprintf(&b, "%s = %s.apply(%s)\n", n.Id, n.NodeType, strings.Join(args, ", "))
	}
	resp := map[string]any{"PythonScript": b.String()}
	if strings.EqualFold(req.Language, "SCALA") {
		var s strings.Builder
		s.WriteString("import com.amazonaws.services.glue.GlueContext\n")
		for _, n := range req.DagNodes {
			fmt.Fprintf(&s, "val %s = %s.apply()\n", n.Id, n.NodeType)
		}
		resp["ScalaCode"] = s.String()
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

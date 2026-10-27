package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
	"github.com/google/uuid"
)

func handleGlueCreateRegistry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryName string            `json:"RegistryName"`
		Description  string            `json:"Description"`
		Tags         map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.RegistryName == "" {
		glueWriteError(w, "InvalidInputException", "RegistryName is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueRegistries.Get(req.RegistryName); ok {
		glueWriteError(w, "AlreadyExistsException", "Registry already exists: "+req.RegistryName)
		return
	}
	arn := glueGlueArn("registry/" + req.RegistryName)
	now := glueRFC3339()
	glueRegistries.Put(req.RegistryName, GlueRegistry{
		RegistryName: req.RegistryName,
		RegistryArn:  arn,
		Description:  req.Description,
		Status:       "AVAILABLE",
		CreatedTime:  now,
		UpdatedTime:  now,
		Tags:         req.Tags,
	})
	resp := map[string]any{
		"RegistryArn":  arn,
		"RegistryName": req.RegistryName,
	}
	if req.Description != "" {
		resp["Description"] = req.Description
	}
	if len(req.Tags) > 0 {
		resp["Tags"] = req.Tags
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func glueRegistryName(name, arn string) string {
	if name != "" {
		return name
	}
	if i := strings.LastIndex(arn, "registry/"); i >= 0 {
		return arn[i+len("registry/"):]
	}
	return ""
}

func handleGlueGetRegistry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId struct {
			RegistryName string `json:"RegistryName"`
			RegistryArn  string `json:"RegistryArn"`
		} `json:"RegistryId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	name := glueRegistryName(req.RegistryId.RegistryName, req.RegistryId.RegistryArn)
	reg, ok := glueRegistries.Get(name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Registry not found: "+name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"RegistryName": reg.RegistryName,
		"RegistryArn":  reg.RegistryArn,
		"Description":  reg.Description,
		"Status":       reg.Status,
		"CreatedTime":  reg.CreatedTime,
		"UpdatedTime":  reg.UpdatedTime,
	})
}

func handleGlueListRegistries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueRegistries.List()
	items := make([]map[string]any, 0, len(all))
	for _, reg := range all {
		items = append(items, map[string]any{
			"RegistryName": reg.RegistryName,
			"RegistryArn":  reg.RegistryArn,
			"Description":  reg.Description,
			"Status":       reg.Status,
			"CreatedTime":  reg.CreatedTime,
			"UpdatedTime":  reg.UpdatedTime,
		})
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, items, awsFieldKey("RegistryName"), req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"Registries": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId struct {
			RegistryName string `json:"RegistryName"`
			RegistryArn  string `json:"RegistryArn"`
		} `json:"RegistryId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	for _, sc := range glueSchemas.List() {
		if sc.RegistryName == name {
			glueSchemas.Delete(glueSchemaKey(name, sc.SchemaName))
		}
	}
	glueRegistries.Delete(name)
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"RegistryName": reg.RegistryName,
		"RegistryArn":  reg.RegistryArn,
		"Status":       "DELETING",
	})
}

func glueSchemaKey(registry, schema string) string { return registry + "\x00" + schema }

func handleGlueCreateSchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId struct {
			RegistryName string `json:"RegistryName"`
			RegistryArn  string `json:"RegistryArn"`
		} `json:"RegistryId"`
		SchemaName       string            `json:"SchemaName"`
		DataFormat       string            `json:"DataFormat"`
		Compatibility    string            `json:"Compatibility"`
		Description      string            `json:"Description"`
		SchemaDefinition string            `json:"SchemaDefinition"`
		Tags             map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.SchemaName == "" || req.DataFormat == "" {
		glueWriteError(w, "InvalidInputException", "SchemaName and DataFormat are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	registryName := glueRegistryName(req.RegistryId.RegistryName, req.RegistryId.RegistryArn)
	if registryName == "" {
		registryName = "default-registry"
	}
	registryArn := glueGlueArn("registry/" + registryName)
	if reg, ok := glueRegistries.Get(registryName); ok {
		registryArn = reg.RegistryArn
	} else if registryName != "default-registry" {
		glueWriteError(w, "EntityNotFoundException", "Registry not found: "+registryName)
		return
	}

	key := glueSchemaKey(registryName, req.SchemaName)
	if _, ok := glueSchemas.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "Schema already exists: "+req.SchemaName)
		return
	}
	compat := req.Compatibility
	if compat == "" {
		compat = "BACKWARD"
	}
	schemaArn := glueGlueArn("schema/" + registryName + "/" + req.SchemaName)
	now := glueRFC3339()
	sc := GlueSchema{
		RegistryName:        registryName,
		RegistryArn:         registryArn,
		SchemaName:          req.SchemaName,
		SchemaArn:           schemaArn,
		Description:         req.Description,
		DataFormat:          req.DataFormat,
		Compatibility:       compat,
		SchemaCheckpoint:    1,
		LatestSchemaVersion: 1,
		NextSchemaVersion:   2,
		SchemaStatus:        "AVAILABLE",
		CreatedTime:         now,
		UpdatedTime:         now,
		SchemaVersionId:     uuid.NewString(),
		Tags:                req.Tags,
	}
	glueSchemas.Put(key, sc)
	glueSchemaVers.Put(sc.SchemaVersionId, GlueSchemaVersion{
		SchemaVersionId:  sc.SchemaVersionId,
		SchemaArn:        schemaArn,
		RegistryName:     registryName,
		SchemaName:       req.SchemaName,
		VersionNumber:    1,
		SchemaDefinition: req.SchemaDefinition,
		DataFormat:       req.DataFormat,
		Status:           "AVAILABLE",
		CreatedTime:      now,
	})
	resp := map[string]any{
		"RegistryName":        registryName,
		"RegistryArn":         registryArn,
		"SchemaName":          req.SchemaName,
		"SchemaArn":           schemaArn,
		"DataFormat":          req.DataFormat,
		"Compatibility":       compat,
		"SchemaCheckpoint":    sc.SchemaCheckpoint,
		"LatestSchemaVersion": sc.LatestSchemaVersion,
		"NextSchemaVersion":   sc.NextSchemaVersion,
		"SchemaStatus":        sc.SchemaStatus,
		"SchemaVersionId":     sc.SchemaVersionId,
		"SchemaVersionStatus": "AVAILABLE",
	}
	if req.Description != "" {
		resp["Description"] = req.Description
	}
	if len(req.Tags) > 0 {
		resp["Tags"] = req.Tags
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func glueResolveSchema(schemaArn, schemaName, registryName string) (GlueSchema, bool) {
	if schemaArn != "" {
		for _, sc := range glueSchemas.List() {
			if sc.SchemaArn == schemaArn {
				return sc, true
			}
		}
		return GlueSchema{}, false
	}
	if registryName == "" {
		registryName = "default-registry"
	}
	return glueSchemas.Get(glueSchemaKey(registryName, schemaName))
}

func handleGlueGetSchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"RegistryName":        sc.RegistryName,
		"RegistryArn":         sc.RegistryArn,
		"SchemaName":          sc.SchemaName,
		"SchemaArn":           sc.SchemaArn,
		"Description":         sc.Description,
		"DataFormat":          sc.DataFormat,
		"Compatibility":       sc.Compatibility,
		"SchemaCheckpoint":    sc.SchemaCheckpoint,
		"LatestSchemaVersion": sc.LatestSchemaVersion,
		"NextSchemaVersion":   sc.NextSchemaVersion,
		"SchemaStatus":        sc.SchemaStatus,
		"CreatedTime":         sc.CreatedTime,
		"UpdatedTime":         sc.UpdatedTime,
	})
}

func handleGlueDeleteSchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	glueSchemas.Delete(glueSchemaKey(sc.RegistryName, sc.SchemaName))
	for _, v := range glueSchemaVers.List() {
		if v.SchemaArn == sc.SchemaArn {
			glueSchemaVers.Delete(v.SchemaVersionId)
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SchemaArn":  sc.SchemaArn,
		"SchemaName": sc.SchemaName,
		"Status":     "DELETING",
	})
}

// glueSchemaVersionsFor returns a schema's versions in ascending VersionNumber order.
func glueSchemaVersionsFor(schemaArn string) []GlueSchemaVersion {
	var out []GlueSchemaVersion
	for _, v := range glueSchemaVers.List() {
		if v.SchemaArn == schemaArn {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VersionNumber < out[j].VersionNumber })
	return out
}

func handleGlueRegisterSchemaVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		SchemaDefinition string `json:"SchemaDefinition"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.SchemaDefinition == "" {
		glueWriteError(w, "InvalidInputException", "SchemaDefinition is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema not found")
		return
	}
	// The registry de-duplicates: a definition identical to an existing version returns that version.
	for _, v := range glueSchemaVersionsFor(sc.SchemaArn) {
		if v.SchemaDefinition == req.SchemaDefinition {
			glueWriteJSON(w, http.StatusOK, map[string]any{
				"SchemaVersionId": v.SchemaVersionId,
				"VersionNumber":   v.VersionNumber,
				"Status":          v.Status,
			})
			return
		}
	}
	newVer := sc.LatestSchemaVersion + 1
	versionID := uuid.NewString()
	glueSchemaVers.Put(versionID, GlueSchemaVersion{
		SchemaVersionId:  versionID,
		SchemaArn:        sc.SchemaArn,
		RegistryName:     sc.RegistryName,
		SchemaName:       sc.SchemaName,
		VersionNumber:    newVer,
		SchemaDefinition: req.SchemaDefinition,
		DataFormat:       sc.DataFormat,
		Status:           "AVAILABLE",
		CreatedTime:      glueRFC3339(),
	})
	sc.LatestSchemaVersion = newVer
	sc.NextSchemaVersion = newVer + 1
	sc.UpdatedTime = glueRFC3339()
	glueSchemas.Put(glueSchemaKey(sc.RegistryName, sc.SchemaName), sc)
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SchemaVersionId": versionID,
		"VersionNumber":   newVer,
		"Status":          "AVAILABLE",
	})
}

func handleGlueGetSchemaVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		SchemaVersionId     string `json:"SchemaVersionId"`
		SchemaVersionNumber struct {
			LatestVersion bool  `json:"LatestVersion"`
			VersionNumber int64 `json:"VersionNumber"`
		} `json:"SchemaVersionNumber"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var ver GlueSchemaVersion
	found := false
	if req.SchemaVersionId != "" {
		ver, found = glueSchemaVers.Get(req.SchemaVersionId)
	} else {
		sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Schema not found")
			return
		}
		versions := glueSchemaVersionsFor(sc.SchemaArn)
		if len(versions) == 0 {
			glueWriteError(w, "EntityNotFoundException", "Schema version not found")
			return
		}
		if req.SchemaVersionNumber.VersionNumber > 0 && !req.SchemaVersionNumber.LatestVersion {
			for _, v := range versions {
				if v.VersionNumber == req.SchemaVersionNumber.VersionNumber {
					ver, found = v, true
					break
				}
			}
		} else {
			ver, found = versions[len(versions)-1], true
		}
	}
	if !found {
		glueWriteError(w, "EntityNotFoundException", "Schema version not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SchemaVersionId":  ver.SchemaVersionId,
		"SchemaDefinition": ver.SchemaDefinition,
		"DataFormat":       ver.DataFormat,
		"SchemaArn":        ver.SchemaArn,
		"VersionNumber":    ver.VersionNumber,
		"Status":           ver.Status,
		"CreatedTime":      ver.CreatedTime,
	})
}

func handleGlueListSchemaVersions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	versions := glueSchemaVersionsFor(sc.SchemaArn)
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, versions, func(it GlueSchemaVersion) string { return listq.IntKey(it.VersionNumber) }, req.NextToken, maxR, 25)
	if !pageOK {
		return
	}
	out := make([]map[string]any, 0, len(page))
	for _, v := range page {
		out = append(out, map[string]any{
			"SchemaArn":       v.SchemaArn,
			"SchemaVersionId": v.SchemaVersionId,
			"VersionNumber":   v.VersionNumber,
			"Status":          v.Status,
			"CreatedTime":     v.CreatedTime,
		})
	}
	resp := map[string]any{"Schemas": out}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueGetSchemaByDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		SchemaDefinition string `json:"SchemaDefinition"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	for _, v := range glueSchemaVersionsFor(sc.SchemaArn) {
		if v.SchemaDefinition == req.SchemaDefinition {
			glueWriteJSON(w, http.StatusOK, map[string]any{
				"SchemaVersionId": v.SchemaVersionId,
				"SchemaArn":       v.SchemaArn,
				"DataFormat":      v.DataFormat,
				"Status":          v.Status,
				"CreatedTime":     v.CreatedTime,
			})
			return
		}
	}
	glueWriteError(w, "EntityNotFoundException", "No schema version found for the given definition")
}

func handleGlueDeleteSchemaVersions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		Versions string `json:"Versions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
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
	lo, hi, ok := glueParseVersionRange(req.Versions)
	if !ok {
		glueWriteError(w, "InvalidInputException", "Invalid Versions range: "+req.Versions)
		return
	}
	var errs []map[string]any
	for _, v := range glueSchemaVersionsFor(sc.SchemaArn) {
		if v.VersionNumber >= lo && v.VersionNumber <= hi {
			glueSchemaVers.Delete(v.SchemaVersionId)
		}
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["SchemaVersionErrors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueParseVersionRange parses a single version ("5") or an inclusive range
// ("5-8") into (lo, hi).
func glueParseVersionRange(s string) (lo, hi int64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	if i := strings.Index(s, "-"); i >= 0 {
		a, err1 := strconv.ParseInt(strings.TrimSpace(s[:i]), 10, 64)
		b, err2 := strconv.ParseInt(strings.TrimSpace(s[i+1:]), 10, 64)
		if err1 != nil || err2 != nil {
			return 0, 0, false
		}
		if a > b {
			a, b = b, a
		}
		return a, b, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return n, n, true
}

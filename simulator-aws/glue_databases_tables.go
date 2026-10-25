package main

import (
	"net/http"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim"
)

func handleGlueCreateDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId     string            `json:"CatalogId"`
		Tags          map[string]string `json:"Tags"`
		DatabaseInput struct {
			Name                          string            `json:"Name"`
			Parameters                    map[string]string `json:"Parameters"`
			LocationUri                   string            `json:"LocationUri"`
			Description                   string            `json:"Description"`
			CreateTableDefaultPermissions []map[string]any  `json:"CreateTableDefaultPermissions"`
		} `json:"DatabaseInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDatabases.Get(req.DatabaseInput.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Database already exists: "+req.DatabaseInput.Name)
		return
	}
	if req.CatalogId == "" {
		req.CatalogId = awsAccountID()
	}
	if req.DatabaseInput.Parameters == nil {
		req.DatabaseInput.Parameters = map[string]string{}
	}
	if req.Tags == nil {
		req.Tags = map[string]string{}
	}
	if req.DatabaseInput.CreateTableDefaultPermissions == nil {
		req.DatabaseInput.CreateTableDefaultPermissions = []map[string]any{}
	}
	db := GlueDatabase{
		Name:                          req.DatabaseInput.Name,
		CatalogId:                     req.CatalogId,
		Parameters:                    req.DatabaseInput.Parameters,
		LocationUri:                   req.DatabaseInput.LocationUri,
		Description:                   req.DatabaseInput.Description,
		CreateTime:                    glueEpochNow(),
		Tags:                          req.Tags,
		CreateTableDefaultPermissions: req.DatabaseInput.CreateTableDefaultPermissions,
	}
	glueDatabases.Put(req.DatabaseInput.Name, db)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	db, ok := glueDatabases.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Database": glueDatabaseResponse(db)})
}

func handleGlueGetDatabases(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	stored := glueDatabases.List()
	all := make([]glueDatabaseWire, 0, len(stored))
	for _, database := range stored {
		all = append(all, glueDatabaseResponse(database))
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, all, func(it glueDatabaseWire) string { return it.Name }, req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"DatabaseList": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDatabases.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.Name)
		return
	}
	glueDatabases.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueUpdateDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId     string `json:"CatalogId"`
		Name          string `json:"Name"`
		DatabaseInput struct {
			Name                          string            `json:"Name"`
			Parameters                    map[string]string `json:"Parameters"`
			LocationUri                   string            `json:"LocationUri"`
			Description                   string            `json:"Description"`
			CreateTableDefaultPermissions []map[string]any  `json:"CreateTableDefaultPermissions"`
		} `json:"DatabaseInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" || req.DatabaseInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name and DatabaseInput.Name are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	existing, ok := glueDatabases.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.Name)
		return
	}
	parameters := req.DatabaseInput.Parameters
	if parameters == nil {
		parameters = map[string]string{}
	}
	defaultPermissions := req.DatabaseInput.CreateTableDefaultPermissions
	if defaultPermissions == nil {
		defaultPermissions = []map[string]any{}
	}
	updated := GlueDatabase{
		Name:                          req.DatabaseInput.Name,
		CatalogId:                     firstNonEmpty(req.CatalogId, existing.CatalogId),
		Parameters:                    parameters,
		LocationUri:                   req.DatabaseInput.LocationUri,
		Description:                   req.DatabaseInput.Description,
		CreateTime:                    existing.CreateTime,
		Tags:                          existing.Tags,
		CreateTableDefaultPermissions: defaultPermissions,
	}
	// A rename moves the row; otherwise overwrite in place.
	if req.DatabaseInput.Name != req.Name {
		if _, clash := glueDatabases.Get(req.DatabaseInput.Name); clash {
			glueWriteError(w, "AlreadyExistsException", "Database already exists: "+req.DatabaseInput.Name)
			return
		}
		glueDatabases.Delete(req.Name)
	}
	glueDatabases.Put(req.DatabaseInput.Name, updated)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func glueTableKey(database, table string) string {
	return database + "/" + table
}

// glueTableVersionKey is the store key for one (database, table, versionId)
// snapshot.
func glueTableVersionKey(database, table, versionID string) string {
	return database + "/" + table + "/" + versionID
}

// glueLatestTableVersion returns the highest VersionId recorded for a table as
// an integer (0 if none). Caller holds glueMu.
func glueLatestTableVersion(database, table string) int {
	latest := 0
	for _, v := range glueTableVersions.List() {
		if v.DatabaseName == database && v.TableName == table {
			if n, err := strconv.Atoi(v.VersionId); err == nil && n > latest {
				latest = n
			}
		}
	}
	return latest
}

// glueRecordTableVersion snapshots a table under the given version id. Caller
// holds glueMu.
func glueRecordTableVersion(database, table, versionID string, t GlueTable) {
	glueTableVersions.Put(glueTableVersionKey(database, table, versionID), GlueTableVersion{
		DatabaseName: database,
		TableName:    table,
		VersionId:    versionID,
		Table:        t,
	})
}

func handleGlueCreateTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableInput   struct {
			Name              string            `json:"Name"`
			StorageDescriptor map[string]any    `json:"StorageDescriptor"`
			PartitionKeys     []map[string]any  `json:"PartitionKeys"`
			Parameters        map[string]string `json:"Parameters"`
			TableType         string            `json:"TableType"`
		} `json:"TableInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and Name are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDatabases.Get(req.DatabaseName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.DatabaseName)
		return
	}
	key := glueTableKey(req.DatabaseName, req.TableInput.Name)
	if _, ok := glueTables.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "Table already exists: "+req.TableInput.Name)
		return
	}
	now := glueEpochNow()
	t := GlueTable{
		Name:              req.TableInput.Name,
		DatabaseName:      req.DatabaseName,
		StorageDescriptor: req.TableInput.StorageDescriptor,
		PartitionKeys:     req.TableInput.PartitionKeys,
		Parameters:        req.TableInput.Parameters,
		TableType:         req.TableInput.TableType,
		CreateTime:        now,
		UpdateTime:        now,
	}
	glueTables.Put(key, t)
	glueRecordTableVersion(req.DatabaseName, req.TableInput.Name, "1", t)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		Name         string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	t, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.Name))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Table": t})
}

func handleGlueGetTables(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueTables.List()
	var filtered []GlueTable
	for _, t := range all {
		if t.DatabaseName == req.DatabaseName {
			filtered = append(filtered, t)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, filtered, func(it GlueTable) string { return it.Name }, req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"TableList": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		Name         string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueTableKey(req.DatabaseName, req.Name)
	if _, ok := glueTables.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.Name)
		return
	}
	glueTables.Delete(key)
	glueDeleteTableVersions(req.DatabaseName, req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// glueDeleteTableVersions removes all recorded versions for a table. Caller
// holds glueMu.
func glueDeleteTableVersions(database, table string) {
	for _, v := range glueTableVersions.List() {
		if v.DatabaseName == database && v.TableName == table {
			glueTableVersions.Delete(glueTableVersionKey(database, table, v.VersionId))
		}
	}
}

func handleGlueUpdateTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableInput   struct {
			Name              string            `json:"Name"`
			StorageDescriptor map[string]any    `json:"StorageDescriptor"`
			PartitionKeys     []map[string]any  `json:"PartitionKeys"`
			Parameters        map[string]string `json:"Parameters"`
			TableType         string            `json:"TableType"`
		} `json:"TableInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableInput.Name are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueTableKey(req.DatabaseName, req.TableInput.Name)
	existing, ok := glueTables.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableInput.Name)
		return
	}
	updated := GlueTable{
		Name:              req.TableInput.Name,
		DatabaseName:      req.DatabaseName,
		StorageDescriptor: req.TableInput.StorageDescriptor,
		PartitionKeys:     req.TableInput.PartitionKeys,
		Parameters:        req.TableInput.Parameters,
		TableType:         req.TableInput.TableType,
		CreateTime:        existing.CreateTime,
		UpdateTime:        glueEpochNow(),
	}
	glueTables.Put(key, updated)
	nextVer := strconv.Itoa(glueLatestTableVersion(req.DatabaseName, req.TableInput.Name) + 1)
	glueRecordTableVersion(req.DatabaseName, req.TableInput.Name, nextVer, updated)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueBatchDeleteTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName   string   `json:"DatabaseName"`
		TablesToDelete []string `json:"TablesToDelete"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var errs []map[string]any
	for _, name := range req.TablesToDelete {
		key := glueTableKey(req.DatabaseName, name)
		if _, ok := glueTables.Get(key); !ok {
			errs = append(errs, map[string]any{
				"TableName": name,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Table not found: " + name,
				},
			})
			continue
		}
		glueTables.Delete(key)
		glueDeleteTableVersions(req.DatabaseName, name)
		// Cascade-delete the table's partitions.
		for _, p := range gluePartitions.List() {
			if p.DatabaseName == req.DatabaseName && p.TableName == name {
				gluePartitions.Delete(gluePartitionKey(req.DatabaseName, name, p.Values))
			}
		}
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

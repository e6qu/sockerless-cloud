package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func gluePartitionKey(database, table string, values []string) string {
	return database + "/" + table + "/" + strings.Join(values, "\x1f")
}

func glueValuesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// glueRequirePartitionTable writes the error and returns false when the parent
// database or table is missing. The caller holds glueMu.
func glueRequirePartitionTable(w http.ResponseWriter, database, table string) bool {
	if _, ok := glueDatabases.Get(database); !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+database)
		return false
	}
	if _, ok := glueTables.Get(glueTableKey(database, table)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+table)
		return false
	}
	return true
}

type gluePartitionInput struct {
	Values            []string          `json:"Values"`
	StorageDescriptor map[string]any    `json:"StorageDescriptor"`
	Parameters        map[string]string `json:"Parameters"`
	LastAccessTime    *float64          `json:"LastAccessTime"`
	LastAnalyzedTime  *float64          `json:"LastAnalyzedTime"`
}

func gluePartitionFromInput(database, table string, in gluePartitionInput, creationTime float64) GluePartition {
	return GluePartition{
		Values:            in.Values,
		DatabaseName:      database,
		TableName:         table,
		StorageDescriptor: in.StorageDescriptor,
		Parameters:        in.Parameters,
		CreationTime:      creationTime,
		LastAccessTime:    in.LastAccessTime,
		LastAnalyzedTime:  in.LastAnalyzedTime,
	}
}

func handleGlueCreatePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName   string             `json:"DatabaseName"`
		TableName      string             `json:"TableName"`
		PartitionInput gluePartitionInput `json:"PartitionInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableName are required")
		return
	}
	if len(req.PartitionInput.Values) == 0 {
		glueWriteError(w, "InvalidInputException", "PartitionInput.Values is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	key := gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionInput.Values)
	if _, ok := gluePartitions.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "Partition already exists")
		return
	}
	gluePartitions.Put(key, gluePartitionFromInput(req.DatabaseName, req.TableName, req.PartitionInput, glueEpochNow()))
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueBatchCreatePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName       string               `json:"DatabaseName"`
		TableName          string               `json:"TableName"`
		PartitionInputList []gluePartitionInput `json:"PartitionInputList"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	now := glueEpochNow()
	var errs []map[string]any
	for _, in := range req.PartitionInputList {
		key := gluePartitionKey(req.DatabaseName, req.TableName, in.Values)
		if _, ok := gluePartitions.Get(key); ok {
			errs = append(errs, map[string]any{
				"PartitionValues": in.Values,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "AlreadyExistsException",
					"ErrorMessage": "Partition already exists",
				},
			})
			continue
		}
		gluePartitions.Put(key, gluePartitionFromInput(req.DatabaseName, req.TableName, in, now))
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueGetPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string   `json:"DatabaseName"`
		TableName       string   `json:"TableName"`
		PartitionValues []string `json:"PartitionValues"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	p, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Partition": p})
}

func handleGlueGetPartitions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var filtered []GluePartition
	for _, p := range gluePartitions.List() {
		if p.DatabaseName == req.DatabaseName && p.TableName == req.TableName {
			filtered = append(filtered, p)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, filtered, func(it GluePartition) string { return gluePartitionKey(it.DatabaseName, it.TableName, it.Values) }, req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"Partitions": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchGetPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string `json:"DatabaseName"`
		TableName       string `json:"TableName"`
		PartitionsToGet []struct {
			Values []string `json:"Values"`
		} `json:"PartitionsToGet"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var found []GluePartition
	var unprocessed []map[string]any
	for _, pk := range req.PartitionsToGet {
		p, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, pk.Values))
		if ok {
			found = append(found, p)
		} else {
			unprocessed = append(unprocessed, map[string]any{"Values": pk.Values})
		}
	}
	resp := map[string]any{}
	if len(found) > 0 {
		resp["Partitions"] = found
	}
	if len(unprocessed) > 0 {
		resp["UnprocessedKeys"] = unprocessed
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdatePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName       string             `json:"DatabaseName"`
		TableName          string             `json:"TableName"`
		PartitionValueList []string           `json:"PartitionValueList"`
		PartitionInput     gluePartitionInput `json:"PartitionInput"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	oldKey := gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValueList)
	existing, ok := gluePartitions.Get(oldKey)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	updated := gluePartitionFromInput(req.DatabaseName, req.TableName, req.PartitionInput, existing.CreationTime)
	// UpdatePartition may move the partition to new values.
	if !glueValuesEqual(req.PartitionInput.Values, req.PartitionValueList) {
		newKey := gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionInput.Values)
		if _, clash := gluePartitions.Get(newKey); clash {
			glueWriteError(w, "AlreadyExistsException", "Partition already exists")
			return
		}
		gluePartitions.Delete(oldKey)
		gluePartitions.Put(newKey, updated)
	} else {
		gluePartitions.Put(oldKey, updated)
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeletePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string   `json:"DatabaseName"`
		TableName       string   `json:"TableName"`
		PartitionValues []string `json:"PartitionValues"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues)
	if _, ok := gluePartitions.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	gluePartitions.Delete(key)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueBatchDeletePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName       string `json:"DatabaseName"`
		TableName          string `json:"TableName"`
		PartitionsToDelete []struct {
			Values []string `json:"Values"`
		} `json:"PartitionsToDelete"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var errs []map[string]any
	for _, pk := range req.PartitionsToDelete {
		key := gluePartitionKey(req.DatabaseName, req.TableName, pk.Values)
		if _, ok := gluePartitions.Get(key); !ok {
			errs = append(errs, map[string]any{
				"PartitionValues": pk.Values,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Partition not found",
				},
			})
			continue
		}
		gluePartitions.Delete(key)
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func gluePartitionIndexKey(database, table, index string) string {
	return database + "/" + table + "/" + index
}

func handleGlueCreatePartitionIndex(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName   string `json:"DatabaseName"`
		TableName      string `json:"TableName"`
		PartitionIndex struct {
			Keys      []string `json:"Keys"`
			IndexName string   `json:"IndexName"`
		} `json:"PartitionIndex"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" || req.PartitionIndex.IndexName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName, TableName and PartitionIndex.IndexName are required")
		return
	}
	if len(req.PartitionIndex.Keys) == 0 {
		glueWriteError(w, "InvalidInputException", "PartitionIndex.Keys is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	tbl, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	idxKey := gluePartitionIndexKey(req.DatabaseName, req.TableName, req.PartitionIndex.IndexName)
	if _, exists := gluePartIndexes.Get(idxKey); exists {
		glueWriteError(w, "AlreadyExistsException", "Partition index already exists: "+req.PartitionIndex.IndexName)
		return
	}
	// Resolve each key name to a KeySchemaElement (Name, Type) from the table's
	// partition keys, the PartitionIndexDescriptor read-back shape.
	keyType := func(name string) string {
		for _, pk := range tbl.PartitionKeys {
			if glueString(pk["Name"]) == name {
				return glueString(pk["Type"])
			}
		}
		return "string"
	}
	keys := make([]map[string]any, 0, len(req.PartitionIndex.Keys))
	for _, name := range req.PartitionIndex.Keys {
		keys = append(keys, map[string]any{"Name": name, "Type": keyType(name)})
	}
	gluePartIndexes.Put(idxKey, GluePartitionIndex{
		DatabaseName: req.DatabaseName,
		TableName:    req.TableName,
		IndexName:    req.PartitionIndex.IndexName,
		Keys:         keys,
		IndexStatus:  "ACTIVE",
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetPartitionIndexes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		NextToken    string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.RLock()
	defer glueMu.RUnlock()

	descriptors := []map[string]any{}
	for _, idx := range gluePartIndexes.List() {
		if idx.DatabaseName == req.DatabaseName && idx.TableName == req.TableName {
			descriptors = append(descriptors, map[string]any{
				"IndexName":   idx.IndexName,
				"Keys":        idx.Keys,
				"IndexStatus": idx.IndexStatus,
			})
		}
	}
	sort.Slice(descriptors, func(i, j int) bool {
		return glueString(descriptors[i]["IndexName"]) < glueString(descriptors[j]["IndexName"])
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"PartitionIndexDescriptorList": descriptors})
}

func handleGlueDeletePartitionIndex(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		IndexName    string `json:"IndexName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	idxKey := gluePartitionIndexKey(req.DatabaseName, req.TableName, req.IndexName)
	if _, ok := gluePartIndexes.Get(idxKey); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition index not found: "+req.IndexName)
		return
	}
	gluePartIndexes.Delete(idxKey)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

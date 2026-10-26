package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// glueResolveTableVersion returns versionID if given, otherwise the latest
// recorded version, and false when the table has none.
func glueResolveTableVersion(database, table, versionID string) (string, bool) {
	if versionID != "" {
		if _, ok := glueTableVersions.Get(glueTableVersionKey(database, table, versionID)); ok {
			return versionID, true
		}
		return "", false
	}
	latest := glueLatestTableVersion(database, table)
	if latest == 0 {
		return "", false
	}
	return strconv.Itoa(latest), true
}

func handleGlueGetTableVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		VersionId    string `json:"VersionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.RLock()
	defer glueMu.RUnlock()

	if _, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	verID, ok := glueResolveTableVersion(req.DatabaseName, req.TableName, req.VersionId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table version not found")
		return
	}
	tv, _ := glueTableVersions.Get(glueTableVersionKey(req.DatabaseName, req.TableName, verID))
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"TableVersion": map[string]any{
			"Table":     tv.Table,
			"VersionId": tv.VersionId,
		},
	})
}

func handleGlueGetTableVersions(w http.ResponseWriter, r *http.Request) {
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

	glueMu.RLock()
	defer glueMu.RUnlock()

	if _, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	var versions []GlueTableVersion
	for _, v := range glueTableVersions.List() {
		if v.DatabaseName == req.DatabaseName && v.TableName == req.TableName {
			versions = append(versions, v)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, versions, func(it GlueTableVersion) string { n, _ := strconv.Atoi(it.VersionId); return listq.IntKey(int64(n)) }, req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
	out := make([]map[string]any, 0, len(page))
	for _, v := range page {
		out = append(out, map[string]any{
			"Table":     v.Table,
			"VersionId": v.VersionId,
		})
	}
	resp := map[string]any{"TableVersions": out}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteTableVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		VersionId    string `json:"VersionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueTableVersionKey(req.DatabaseName, req.TableName, req.VersionId)
	if _, ok := glueTableVersions.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table version not found: "+req.VersionId)
		return
	}
	glueTableVersions.Delete(key)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueBatchDeleteTableVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string   `json:"DatabaseName"`
		TableName    string   `json:"TableName"`
		VersionIds   []string `json:"VersionIds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var errs []map[string]any
	for _, vid := range req.VersionIds {
		key := glueTableVersionKey(req.DatabaseName, req.TableName, vid)
		if _, ok := glueTableVersions.Get(key); !ok {
			errs = append(errs, map[string]any{
				"TableName": req.TableName,
				"VersionId": vid,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Table version not found: " + vid,
				},
			})
			continue
		}
		glueTableVersions.Delete(key)
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// Empty values scope the key to the table rather than a partition.
func glueColumnStatKey(database, table string, values []string, column string) string {
	return database + "\x00" + table + "\x00" + strings.Join(values, "\x1f") + "\x00" + column
}

func handleGlueUpdateColumnStatisticsForTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName         string           `json:"DatabaseName"`
		TableName            string           `json:"TableName"`
		ColumnStatisticsList []map[string]any `json:"ColumnStatisticsList"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	for _, cs := range req.ColumnStatisticsList {
		col := glueString(cs["ColumnName"])
		if col == "" {
			continue
		}
		glueColumnStats.Put(glueColumnStatKey(req.DatabaseName, req.TableName, nil, col), GlueColumnStatisticsRecord{
			DatabaseName: req.DatabaseName,
			TableName:    req.TableName,
			ColumnName:   col,
			Statistics:   cs,
		})
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetColumnStatisticsForTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string   `json:"DatabaseName"`
		TableName    string   `json:"TableName"`
		ColumnNames  []string `json:"ColumnNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.RLock()
	defer glueMu.RUnlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	list := []map[string]any{}
	for _, col := range req.ColumnNames {
		rec, ok := glueColumnStats.Get(glueColumnStatKey(req.DatabaseName, req.TableName, nil, col))
		if ok {
			list = append(list, rec.Statistics)
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"ColumnStatisticsList": list})
}

func handleGlueDeleteColumnStatisticsForTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		ColumnName   string `json:"ColumnName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	glueColumnStats.Delete(glueColumnStatKey(req.DatabaseName, req.TableName, nil, req.ColumnName))
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueUpdateColumnStatisticsForPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName         string           `json:"DatabaseName"`
		TableName            string           `json:"TableName"`
		PartitionValues      []string         `json:"PartitionValues"`
		ColumnStatisticsList []map[string]any `json:"ColumnStatisticsList"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	for _, cs := range req.ColumnStatisticsList {
		col := glueString(cs["ColumnName"])
		if col == "" {
			continue
		}
		glueColumnStats.Put(glueColumnStatKey(req.DatabaseName, req.TableName, req.PartitionValues, col), GlueColumnStatisticsRecord{
			DatabaseName:    req.DatabaseName,
			TableName:       req.TableName,
			PartitionValues: req.PartitionValues,
			ColumnName:      col,
			Statistics:      cs,
		})
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetColumnStatisticsForPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string   `json:"DatabaseName"`
		TableName       string   `json:"TableName"`
		PartitionValues []string `json:"PartitionValues"`
		ColumnNames     []string `json:"ColumnNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.RLock()
	defer glueMu.RUnlock()

	if _, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	list := []map[string]any{}
	for _, col := range req.ColumnNames {
		rec, ok := glueColumnStats.Get(glueColumnStatKey(req.DatabaseName, req.TableName, req.PartitionValues, col))
		if ok {
			list = append(list, rec.Statistics)
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"ColumnStatisticsList": list})
}

func handleGlueDeleteColumnStatisticsForPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string   `json:"DatabaseName"`
		TableName       string   `json:"TableName"`
		PartitionValues []string `json:"PartitionValues"`
		ColumnName      string   `json:"ColumnName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	glueColumnStats.Delete(glueColumnStatKey(req.DatabaseName, req.TableName, req.PartitionValues, req.ColumnName))
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// AWS Glue — Lake Formation credential-vending unfiltered-metadata reads and
// Data Quality statistic/profile annotations (AWS JSON 1.1).
//
// The simulator has no Lake Formation cell-level filtering, so the "unfiltered"
// metadata is the full Data Catalog row exactly as stored — which is the faithful
// result a caller with full table permissions receives. AuthorizedColumns is the
// complete column list of the table; IsRegisteredWithLakeFormation is false
// because the simulator registers no catalog resource with Lake Formation.

// GlueStatisticAnnotation records an inclusion annotation applied to a single
// Data Quality statistic, scoped by ProfileId + StatisticId.
type GlueStatisticAnnotation struct {
	ProfileId           string  `json:"ProfileId"`
	StatisticId         string  `json:"StatisticId"`
	InclusionAnnotation string  `json:"InclusionAnnotation"`
	LastModifiedOn      float64 `json:"LastModifiedOn"`
}

// GlueProfileAnnotation records the inclusion annotation applied to a whole Data
// Quality monitoring profile, keyed by ProfileId.
type GlueProfileAnnotation struct {
	ProfileId           string  `json:"ProfileId"`
	InclusionAnnotation string  `json:"InclusionAnnotation"`
	LastModifiedOn      float64 `json:"LastModifiedOn"`
}

var (
	glueStatAnnotations    sim.Store[GlueStatisticAnnotation]
	glueProfileAnnotations sim.Store[GlueProfileAnnotation]
	glueAnnoMu             sync.Mutex
)

func glueStatAnnotationKey(profileID, statisticID string) string {
	return profileID + "/" + statisticID
}

func registerGlueUnfilteredDQ(r *sim.AWSRouter, srv *sim.Server) {
	glueStatAnnotations = sim.MakeStore[GlueStatisticAnnotation](srv.DB(), "glue_statistic_annotations")
	glueProfileAnnotations = sim.MakeStore[GlueProfileAnnotation](srv.DB(), "glue_profile_annotations")

	r.Register("AWSGlue.GetUnfilteredTableMetadata", handleGlueGetUnfilteredTableMetadata)
	r.Register("AWSGlue.GetUnfilteredPartitionMetadata", handleGlueGetUnfilteredPartitionMetadata)
	r.Register("AWSGlue.GetUnfilteredPartitionsMetadata", handleGlueGetUnfilteredPartitionsMetadata)
	r.Register("AWSGlue.BatchPutDataQualityStatisticAnnotation", handleGlueBatchPutDataQualityStatisticAnnotation)
	r.Register("AWSGlue.PutDataQualityProfileAnnotation", handleGluePutDataQualityProfileAnnotation)
	r.Register("AWSGlue.ListDataQualityStatisticAnnotations", handleGlueListDataQualityStatisticAnnotations)
}

// glueTableColumnNames returns every column name declared in the table's
// StorageDescriptor, in declaration order — the set of columns a fully-authorized
// caller may read.
func glueTableColumnNames(t GlueTable) []string {
	names := make([]string, 0)
	if t.StorageDescriptor == nil {
		return names
	}
	cols, ok := t.StorageDescriptor["Columns"].([]any)
	if !ok {
		return names
	}
	for _, c := range cols {
		col, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := col["Name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func handleGlueGetUnfilteredTableMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		DatabaseName string `json:"DatabaseName"`
		Name         string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	t, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.Name))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"Table":                         t,
		"AuthorizedColumns":             glueTableColumnNames(t),
		"IsRegisteredWithLakeFormation": false,
	})
}

func handleGlueGetUnfilteredPartitionMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId       string   `json:"CatalogId"`
		DatabaseName    string   `json:"DatabaseName"`
		TableName       string   `json:"TableName"`
		PartitionValues []string `json:"PartitionValues"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	t, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	p, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"Partition":                     p,
		"AuthorizedColumns":             glueTableColumnNames(t),
		"IsRegisteredWithLakeFormation": false,
	})
}

func handleGlueGetUnfilteredPartitionsMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId    string `json:"CatalogId"`
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	t, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	authorized := glueTableColumnNames(t)

	var unfiltered []map[string]any
	for _, p := range gluePartitions.List() {
		if p.DatabaseName == req.DatabaseName && p.TableName == req.TableName {
			unfiltered = append(unfiltered, map[string]any{
				"Partition":                     p,
				"AuthorizedColumns":             authorized,
				"IsRegisteredWithLakeFormation": false,
			})
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(unfiltered, req.NextToken, maxR, 100)
	resp := map[string]any{"UnfilteredPartitions": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchPutDataQualityStatisticAnnotation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InclusionAnnotations []struct {
			ProfileId           string `json:"ProfileId"`
			StatisticId         string `json:"StatisticId"`
			InclusionAnnotation string `json:"InclusionAnnotation"`
		} `json:"InclusionAnnotations"`
		ClientToken string `json:"ClientToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueAnnoMu.Lock()
	defer glueAnnoMu.Unlock()

	var failed []map[string]any
	for _, a := range req.InclusionAnnotations {
		if a.ProfileId == "" || a.StatisticId == "" {
			failed = append(failed, map[string]any{
				"ProfileId":     a.ProfileId,
				"StatisticId":   a.StatisticId,
				"FailureReason": "ProfileId and StatisticId are required",
			})
			continue
		}
		// An entry naming a profile no evaluation wrote is reported as the
		// entry that failed rather than refused for the whole batch, which is
		// what the per-entry channel is for.
		if _, known := glueResultForProfile(a.ProfileId); !known {
			failed = append(failed, map[string]any{
				"ProfileId":     a.ProfileId,
				"StatisticId":   a.StatisticId,
				"FailureReason": "No data quality profile " + a.ProfileId + " exists.",
			})
			continue
		}
		glueStatAnnotations.Put(glueStatAnnotationKey(a.ProfileId, a.StatisticId), GlueStatisticAnnotation{
			ProfileId:           a.ProfileId,
			StatisticId:         a.StatisticId,
			InclusionAnnotation: a.InclusionAnnotation,
			LastModifiedOn:      glueEpochNow(),
		})
	}
	resp := map[string]any{}
	if len(failed) > 0 {
		resp["FailedInclusionAnnotations"] = failed
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGluePutDataQualityProfileAnnotation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProfileId           string `json:"ProfileId"`
		InclusionAnnotation string `json:"InclusionAnnotation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	// An annotation is written against a profile an evaluation wrote. Recording
	// one for a profile that was never issued would keep a judgement about
	// something that does not exist, and the read of it would report on
	// nothing.
	if glueProfileMissing(w, req.ProfileId) {
		return
	}

	glueAnnoMu.Lock()
	defer glueAnnoMu.Unlock()

	glueProfileAnnotations.Put(req.ProfileId, GlueProfileAnnotation{
		ProfileId:           req.ProfileId,
		InclusionAnnotation: req.InclusionAnnotation,
		LastModifiedOn:      glueEpochNow(),
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListDataQualityStatisticAnnotations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StatisticId string `json:"StatisticId"`
		ProfileId   string `json:"ProfileId"`
		NextToken   string `json:"NextToken"`
		MaxResults  *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var annotations []map[string]any
	for _, a := range glueStatAnnotations.List() {
		if req.StatisticId != "" && a.StatisticId != req.StatisticId {
			continue
		}
		if req.ProfileId != "" && a.ProfileId != req.ProfileId {
			continue
		}
		annotations = append(annotations, map[string]any{
			"ProfileId":           a.ProfileId,
			"StatisticId":         a.StatisticId,
			"StatisticRecordedOn": a.LastModifiedOn,
			"InclusionAnnotation": map[string]any{
				"Value":          a.InclusionAnnotation,
				"LastModifiedOn": a.LastModifiedOn,
			},
		})
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(annotations, req.NextToken, maxR, 100)
	resp := map[string]any{"Annotations": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

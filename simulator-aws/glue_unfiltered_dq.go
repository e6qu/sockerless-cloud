package main

import (
	"net/http"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The simulator has no AWS Lake Formation cell-level filtering, so unfiltered
// metadata is the stored Data Catalog row, which is what a caller with full
// table permissions receives. AuthorizedColumns lists every column, and
// IsRegisteredWithLakeFormation is false because the simulator registers nothing
// with Lake Formation.

// Keyed by ProfileId and StatisticId.
type GlueStatisticAnnotation struct {
	ProfileId           string  `json:"ProfileId"`
	StatisticId         string  `json:"StatisticId"`
	InclusionAnnotation string  `json:"InclusionAnnotation"`
	LastModifiedOn      float64 `json:"LastModifiedOn"`
}

// Keyed by ProfileId.
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

func registerGlueUnfilteredDQ(r *AWSRouter, srv *sim.Server) {
	glueStatAnnotations = sim.MakeStore[GlueStatisticAnnotation](srv.DB(), "glue_statistic_annotations")
	glueProfileAnnotations = sim.MakeStore[GlueProfileAnnotation](srv.DB(), "glue_profile_annotations")

	r.Register("AWSGlue.GetUnfilteredTableMetadata", handleGlueGetUnfilteredTableMetadata)
	r.Register("AWSGlue.GetUnfilteredPartitionMetadata", handleGlueGetUnfilteredPartitionMetadata)
	r.Register("AWSGlue.GetUnfilteredPartitionsMetadata", handleGlueGetUnfilteredPartitionsMetadata)
	r.Register("AWSGlue.BatchPutDataQualityStatisticAnnotation", handleGlueBatchPutDataQualityStatisticAnnotation)
	r.Register("AWSGlue.PutDataQualityProfileAnnotation", handleGluePutDataQualityProfileAnnotation)
	r.Register("AWSGlue.ListDataQualityStatisticAnnotations", handleGlueListDataQualityStatisticAnnotations)
}

// glueTableColumnNames returns the table's StorageDescriptor columns in
// declaration order, the set a fully authorized caller may read.
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, unfiltered, func(it map[string]any) string {
		p, _ := it["Partition"].(GluePartition)
		return gluePartitionKey(p.DatabaseName, p.TableName, p.Values)
	}, req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
		// Report an entry naming a profile no evaluation wrote as a failed entry, not a
		// refusal of the whole batch.
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
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	// Annotate only a profile an evaluation wrote; an annotation on one never issued would judge nothing.
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
	if err := sim.ReadJSON(r, &req); err != nil {
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
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, annotations, func(it map[string]any) string {
		profile, _ := it["ProfileId"].(string)
		statistic, _ := it["StatisticId"].(string)
		return profile + "\x00" + statistic
	}, req.NextToken, maxR, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"Annotations": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

var glueResultsByClientToken sim.GenerationIndex[GlueDataQualityResult]

func glueResultClientTokenKeys(result GlueDataQualityResult) []string {
	if result.ClientToken == "" {
		return nil
	}
	return []string{result.ClientToken}
}

type gluePublishDataQualityResultV2Request struct {
	Dimensions                       map[string]string `json:"Dimensions"`
	Tags                             map[string]string `json:"Tags,omitempty"`
	ExecutionStartedOn               *float64          `json:"ExecutionStartedOn,omitempty"`
	ExecutionCompletedOn             *float64          `json:"ExecutionCompletedOn,omitempty"`
	Score                            *float64          `json:"Score"`
	RuleResults                      []map[string]any  `json:"RuleResults"`
	AnalyzerResults                  []map[string]any  `json:"AnalyzerResults,omitempty"`
	Observations                     []map[string]any  `json:"Observations,omitempty"`
	AggregatedMetrics                map[string]any    `json:"AggregatedMetrics,omitempty"`
	Statistics                       []map[string]any  `json:"Statistics,omitempty"`
	RunConfiguration                 map[string]any    `json:"RunConfiguration,omitempty"`
	ClientToken                      string            `json:"ClientToken,omitempty"`
	EvaluationContext                string            `json:"EvaluationContext"`
	Descriptors                      map[string]string `json:"Descriptors,omitempty"`
	RulesetName                      string            `json:"RulesetName,omitempty"`
	DataQualitySecurityConfiguration string            `json:"DataQualitySecurityConfiguration,omitempty"`
	Telemetry                        map[string]any    `json:"Telemetry,omitempty"`
}

func handleGluePublishDataQualityResultV2(w http.ResponseWriter, r *http.Request) {
	var req gluePublishDataQualityResultV2Request
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	switch {
	case len(req.Dimensions) < 1 || len(req.Dimensions) > 10:
		glueWriteError(w, "InvalidInputException", "Dimensions must name between 1 and 10 entries")
		return
	case req.Score == nil:
		glueWriteError(w, "InvalidInputException", "Score is required")
		return
	case *req.Score < 0 || *req.Score > 1:
		glueWriteError(w, "InvalidInputException", "Score must be between 0 and 1")
		return
	case req.RuleResults == nil:
		glueWriteError(w, "InvalidInputException", "RuleResults is required")
		return
	case req.EvaluationContext == "":
		glueWriteError(w, "InvalidInputException", "EvaluationContext is required")
		return
	}
	// A retried publish carries the same ClientToken and the same body; the
	// token names the result the first attempt created.
	canonical, err := json.Marshal(req)
	if err != nil {
		glueWriteError(w, "InternalServiceException", "encode request: "+err.Error())
		return
	}
	digest := sha256.Sum256(canonical)
	fingerprint := hex.EncodeToString(digest[:])

	glueMu.Lock()
	defer glueMu.Unlock()
	if req.ClientToken != "" {
		if prior, ok := glueResultsByClientToken.Lookup(glueDQResults, req.ClientToken, glueResultClientTokenKeys); ok {
			if prior.PublishRequest != fingerprint {
				glueWriteError(w, "IdempotentParameterMismatchException",
					"ClientToken "+req.ClientToken+" was already used with different parameters")
				return
			}
			glueWriteJSON(w, http.StatusOK, map[string]any{"ResultId": prior.ResultId})
			return
		}
	}

	now := glueEpochNow()
	started, completed := now, now
	if req.ExecutionStartedOn != nil {
		started = *req.ExecutionStartedOn
	}
	if req.ExecutionCompletedOn != nil {
		completed = *req.ExecutionCompletedOn
	}
	resID := "dqresult-" + glueHashID()[:16]
	glueDQResults.Put(resID, GlueDataQualityResult{
		ResultId:          resID,
		RulesetName:       req.RulesetName,
		StartedOn:         started,
		CompletedOn:       completed,
		RuleResults:       req.RuleResults,
		PublishedOn:       now,
		Score:             req.Score,
		Dimensions:        req.Dimensions,
		Tags:              req.Tags,
		Descriptors:       req.Descriptors,
		EvaluationContext: req.EvaluationContext,
		AnalyzerResults:   req.AnalyzerResults,
		Observations:      req.Observations,
		AggregatedMetrics: req.AggregatedMetrics,
		ClientToken:       req.ClientToken,
		PublishRequest:    fingerprint,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"ResultId": resID})
}

// glueDQResultPublishedOn is when a result reached the service: the publish
// time PublishDataQualityResultV2 recorded, or the completion of the
// evaluation run that produced it.
func glueDQResultPublishedOn(res GlueDataQualityResult) float64 {
	if res.PublishedOn != 0 {
		return res.PublishedOn
	}
	return res.CompletedOn
}

// glueDQResultScore is the ratio of passed rules to all rules, which is how
// the service defines Score; a result with no rule results has none.
func glueDQResultScore(res GlueDataQualityResult) *float64 {
	if res.Score != nil {
		return res.Score
	}
	if len(res.RuleResults) == 0 {
		return nil
	}
	passed := 0
	for _, rule := range res.RuleResults {
		if rule["Result"] == "PASS" {
			passed++
		}
	}
	score := float64(passed) / float64(len(res.RuleResults))
	return &score
}

func glueDQResultV2Payload(res GlueDataQualityResult) map[string]any {
	m := map[string]any{
		"ResultId":             res.ResultId,
		"ExecutionStartedOn":   res.StartedOn,
		"ExecutionCompletedOn": res.CompletedOn,
		"ResultPublishedOn":    glueDQResultPublishedOn(res),
		"RuleResults":          res.RuleResults,
	}
	if res.RuleResults == nil {
		m["RuleResults"] = []map[string]any{}
	}
	if score := glueDQResultScore(res); score != nil {
		m["Score"] = *score
	}
	if res.DataSource != nil {
		m["DataSource"] = res.DataSource
	}
	if res.ProfileId != "" {
		m["ProfileId"] = res.ProfileId
	}
	if res.RulesetName != "" {
		m["RulesetName"] = res.RulesetName
	}
	if res.EvaluationContext != "" {
		m["EvaluationContext"] = res.EvaluationContext
	}
	for key, value := range map[string]map[string]string{
		"Dimensions": res.Dimensions, "Tags": res.Tags, "Descriptors": res.Descriptors,
	} {
		if len(value) > 0 {
			m[key] = value
		}
	}
	if len(res.AnalyzerResults) > 0 {
		m["AnalyzerResults"] = res.AnalyzerResults
	}
	if len(res.Observations) > 0 {
		m["Observations"] = res.Observations
	}
	if len(res.AggregatedMetrics) > 0 {
		m["AggregatedMetrics"] = res.AggregatedMetrics
	}
	return m
}

func handleGlueGetDataQualityResultV2(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResultId string `json:"ResultId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ResultId == "" {
		glueWriteError(w, "InvalidInputException", "ResultId is required")
		return
	}
	res, ok := glueDQResults.Get(req.ResultId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "DataQualityResult not found: "+req.ResultId)
		return
	}
	glueWriteJSON(w, http.StatusOK, glueDQResultV2Payload(res))
}

func handleGlueBatchGetDataQualityResultV2(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResultIds []string `json:"ResultIds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if len(req.ResultIds) < 1 || len(req.ResultIds) > 100 {
		glueWriteError(w, "InvalidInputException", "ResultIds must name between 1 and 100 results")
		return
	}
	results := []map[string]any{}
	var notFound []string
	for _, id := range req.ResultIds {
		if res, ok := glueDQResults.Get(id); ok {
			results = append(results, glueDQResultV2Payload(res))
		} else {
			notFound = append(notFound, id)
		}
	}
	resp := map[string]any{"Results": results}
	if len(notFound) > 0 {
		resp["ResultsNotFound"] = notFound
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueListDataQualityResultsV2(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filter *struct {
			DataSource *struct {
				GlueTable *struct {
					DatabaseName string `json:"DatabaseName"`
					TableName    string `json:"TableName"`
					CatalogId    string `json:"CatalogId"`
				} `json:"GlueTable"`
			} `json:"DataSource"`
			PublishedAfter    *float64          `json:"PublishedAfter"`
			PublishedBefore   *float64          `json:"PublishedBefore"`
			Dimensions        map[string]string `json:"Dimensions"`
			EvaluationContext string            `json:"EvaluationContext"`
		} `json:"Filter"`
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.MaxResults != nil && (*req.MaxResults < 1 || *req.MaxResults > 1000) {
		glueWriteError(w, "InvalidInputException", "MaxResults must be between 1 and 1000")
		return
	}
	descs := []map[string]any{}
	for _, res := range glueDQResults.List() {
		publishedOn := glueDQResultPublishedOn(res)
		if f := req.Filter; f != nil {
			if f.DataSource != nil && f.DataSource.GlueTable != nil && !glueDQResultOnTable(res,
				f.DataSource.GlueTable.DatabaseName, f.DataSource.GlueTable.TableName, f.DataSource.GlueTable.CatalogId) {
				continue
			}
			if f.PublishedAfter != nil && publishedOn <= *f.PublishedAfter {
				continue
			}
			if f.PublishedBefore != nil && publishedOn >= *f.PublishedBefore {
				continue
			}
			if f.EvaluationContext != "" && res.EvaluationContext != f.EvaluationContext {
				continue
			}
			if !glueDimensionsMatch(res.Dimensions, f.Dimensions) {
				continue
			}
		}
		d := map[string]any{
			"ResultId":          res.ResultId,
			"ResultPublishedOn": publishedOn,
		}
		if res.DataSource != nil {
			d["DataSource"] = res.DataSource
		}
		if len(res.Dimensions) > 0 {
			d["Dimensions"] = res.Dimensions
		}
		if res.EvaluationContext != "" {
			d["EvaluationContext"] = res.EvaluationContext
		}
		if len(res.Descriptors) > 0 {
			d["Descriptors"] = res.Descriptors
		}
		descs = append(descs, d)
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, descs, awsFieldKey("ResultId"), req.NextToken, derefIntDefault(req.MaxResults, 0), 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"Results": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueDQResultOnTable reports whether a result evaluated the named Glue
// table. A filter that omits CatalogId means the caller's own catalog.
func glueDQResultOnTable(res GlueDataQualityResult, database, table, catalog string) bool {
	glueTable, _ := res.DataSource["GlueTable"].(map[string]any)
	if glueTable == nil {
		glueTable, _ = res.DataSource["DataQualityGlueTable"].(map[string]any)
	}
	if glueTable == nil || glueTable["DatabaseName"] != database || glueTable["TableName"] != table {
		return false
	}
	resultCatalog, _ := glueTable["CatalogId"].(string)
	if resultCatalog == "" {
		resultCatalog = awsAccountID()
	}
	if catalog == "" {
		catalog = awsAccountID()
	}
	return resultCatalog == catalog
}

func glueDimensionsMatch(have, want map[string]string) bool {
	for key, value := range want {
		if got, ok := have[key]; !ok || got != value {
			return false
		}
	}
	return true
}

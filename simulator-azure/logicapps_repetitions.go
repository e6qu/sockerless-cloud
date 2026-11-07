package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A run keeps, for each action inside a For each loop, one repetition per
// iteration; for each loop, one scope repetition per iteration; and, for an
// HTTP action, the request it sent and the response it got, on the action or
// on its repetition.

var (
	logicRunRepetitions      sim.Store[LogicResource]
	logicRunScopeRepetitions sim.Store[LogicResource]
	logicRunRequestHistories sim.Store[LogicResource]
)

func registerLogicRunDetails(srv *sim.Server) {
	logicRunRepetitions = sim.MakeStore[LogicResource](srv.DB(), "logic_run_repetitions")
	logicRunScopeRepetitions = sim.MakeStore[LogicResource](srv.DB(), "logic_run_scope_repetitions")
	logicRunRequestHistories = sim.MakeStore[LogicResource](srv.DB(), "logic_run_request_histories")
}

// The repetition's name: each loop's zero-padded index, outermost first.
func logicRepetitionName(indexes []logicRepetitionIndex) string {
	parts := make([]string, len(indexes))
	for i, idx := range indexes {
		parts[i] = fmt.Sprintf("%06d", idx.ItemIndex)
	}
	return strings.Join(parts, "-")
}

func logicResultProperties(result *logicActionResult) map[string]any {
	props := map[string]any{
		"status":    result.Status,
		"code":      result.Code,
		"startTime": result.StartTime.Format(time.RFC3339Nano),
		"endTime":   result.EndTime.Format(time.RFC3339Nano),
	}
	if result.Error != nil {
		props["error"] = result.Error
	}
	return props
}

func logicPutRequestHistories(parentID, workflowType string, requests []logicRequestRecord) {
	for i, req := range requests {
		id := fmt.Sprintf("%s/requestHistories/%08d", parentID, i)
		logicRunRequestHistories.Put(id, LogicResource{
			ID: id, Name: id[strings.LastIndex(id, "/")+1:], Type: workflowType + "/runs/actions/requestHistories",
			Properties: map[string]any{
				"startTime": req.StartTime.Format(time.RFC3339Nano),
				"endTime":   req.EndTime.Format(time.RFC3339Nano),
				"request":   map[string]any{"method": req.Method, "uri": req.URI, "headers": req.RequestHeaders},
				"response":  map[string]any{"statusCode": req.StatusCode, "headers": req.ResponseHeaders},
			},
		})
	}
}

// logicPersistRunDetails stores the repetitions and request histories of a
// settled run's actions.
func logicPersistRunDetails(workflowType, runID string, outcome logicRunOutcome) {
	for _, actionName := range outcome.Actions {
		actID := runID + "/actions/" + actionName
		reps := outcome.Repetitions[actionName]
		if len(reps) == 0 {
			if result := outcome.Results[actionName]; result != nil {
				logicPutRequestHistories(actID, workflowType, result.Requests)
			}
		}
		for _, rep := range reps {
			name := logicRepetitionName(rep.Indexes)
			id := actID + "/repetitions/" + name
			props := logicResultProperties(rep.Result)
			props["repetitionIndexes"] = rep.Indexes
			logicRunRepetitions.Put(id, LogicResource{ID: id, Name: name, Type: workflowType + "/runs/actions/repetitions", Properties: props})
			logicPutRequestHistories(id, workflowType, rep.Result.Requests)
		}
		for _, rep := range outcome.ScopeRepetitions[actionName] {
			name := logicRepetitionName(rep.Indexes)
			id := actID + "/scopeRepetitions/" + name
			props := logicResultProperties(rep.Result)
			props["repetitionIndexes"] = rep.Indexes
			logicRunScopeRepetitions.Put(id, LogicResource{ID: id, Name: name, Type: workflowType + "/runs/actions/scopeRepetitions", Properties: props})
		}
	}
}

// logicDropRunDetails deletes everything stored under a run.
func logicDropRunDetails(runID string) {
	prefix := runID + "/"
	for _, store := range []sim.Store[LogicResource]{logicRunRepetitions, logicRunScopeRepetitions, logicRunRequestHistories} {
		for _, res := range store.Filter(func(res LogicResource) bool { return strings.HasPrefix(res.ID, prefix) }) {
			store.Delete(res.ID)
		}
	}
}

// logicRunIDOf is the addressed run's ID under the standalone workflow or the
// App Service hostruntime bridge.
func logicRunIDOf(r *http.Request) string {
	if sim.PathParam(r, "siteName") != "" {
		return webWorkflowRunID(r)
	}
	return logicWorkflowRunID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "workflowName"), sim.PathParam(r, "runName"))
}

// logicDetailParent is the action, or the action's repetition, the route
// addresses.
func logicDetailParent(r *http.Request) string {
	id := logicRunIDOf(r) + "/actions/" + sim.PathParam(r, "actionName")
	if rep := sim.PathParam(r, "repetitionName"); rep != "" {
		id += "/repetitions/" + rep
	}
	return id
}

func handleLogicRunDetailList(store func() sim.Store[LogicResource], collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if logicRunMissing(w, r) {
			return
		}
		prefix := logicDetailParent(r) + "/" + collection + "/"
		writeLogicResourceList(w, store(), func(res LogicResource) bool {
			return strings.HasPrefix(res.ID, prefix) && !strings.Contains(strings.TrimPrefix(res.ID, prefix), "/")
		})
	}
}

func handleLogicRunDetailGet(store func() sim.Store[LogicResource], collection, nameParam, label string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if logicRunMissing(w, r) {
			return
		}
		parent := logicRunIDOf(r) + "/actions/" + sim.PathParam(r, "actionName")
		if collection == "requestHistories" && sim.PathParam(r, "repetitionName") != "" {
			parent += "/repetitions/" + sim.PathParam(r, "repetitionName")
		}
		res, ok := store().Get(parent + "/" + collection + "/" + sim.PathParam(r, nameParam))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The %s %q was not found.", label, sim.PathParam(r, nameParam))
			return
		}
		sim.WriteJSON(w, http.StatusOK, res)
	}
}

var (
	logicRepetitionsStore      = func() sim.Store[LogicResource] { return logicRunRepetitions }
	logicScopeRepetitionsStore = func() sim.Store[LogicResource] { return logicRunScopeRepetitions }
	logicRequestHistoriesStore = func() sim.Store[LogicResource] { return logicRunRequestHistories }
)

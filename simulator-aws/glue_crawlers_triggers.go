package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

func handleGlueCreateCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string            `json:"Name"`
		Role         string            `json:"Role"`
		Targets      map[string]any    `json:"Targets"`
		DatabaseName string            `json:"DatabaseName"`
		Description  string            `json:"Description"`
		Classifiers  []string          `json:"Classifiers"`
		TablePrefix  string            `json:"TablePrefix"`
		Schedule     string            `json:"Schedule"`
		Tags         map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueCrawlers.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Crawler already exists: "+req.Name)
		return
	}
	if glueRefusesRole(w, req.Role, "crawler/"+req.Name, glueCrawlerRoleRefusal) {
		return
	}
	now := glueEpochNow()
	crawler := GlueCrawler{
		Name:         req.Name,
		Role:         req.Role,
		Targets:      req.Targets,
		DatabaseName: req.DatabaseName,
		Description:  req.Description,
		Classifiers:  req.Classifiers,
		TablePrefix:  req.TablePrefix,
		State:        "READY",
		CreationTime: now,
		LastUpdated:  now,
		Version:      1,
		Tags:         req.Tags,
	}
	if req.Schedule != "" {
		crawler.Schedule = &GlueSchedule{ScheduleExpression: req.Schedule, State: "SCHEDULED"}
	}
	glueCrawlers.Put(req.Name, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// glueCrawlerWire strips the persistence-only Tags member from the wire shape;
// the real Crawler structure has no Tags field (tags ride GetTags).
type glueCrawlerWire struct {
	GlueCrawler
}

func (c glueCrawlerWire) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(c.GlueCrawler)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "Tags")
	return json.Marshal(m)
}

func handleGlueGetCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Crawler": glueCrawlerWire{crawler}})
}

func handleGlueGetCrawlers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueCrawlers.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, all, func(it GlueCrawler) string { return it.Name }, req.NextToken, maxR, 25)
	if !pageOK {
		return
	}
	crawlers := make([]glueCrawlerWire, 0, len(page))
	for _, c := range page {
		crawlers = append(crawlers, glueCrawlerWire{c})
	}
	resp := map[string]any{"Crawlers": crawlers}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string         `json:"Name"`
		Role         string         `json:"Role"`
		Targets      map[string]any `json:"Targets"`
		DatabaseName string         `json:"DatabaseName"`
		Description  string         `json:"Description"`
		Classifiers  []string       `json:"Classifiers"`
		TablePrefix  string         `json:"TablePrefix"`
		Schedule     string         `json:"Schedule"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	if req.Role != "" {
		if glueRefusesRole(w, req.Role, "crawler/"+req.Name, glueCrawlerRoleRefusal) {
			return
		}
		crawler.Role = req.Role
	}
	if req.Targets != nil {
		crawler.Targets = req.Targets
	}
	if req.DatabaseName != "" {
		crawler.DatabaseName = req.DatabaseName
	}
	crawler.Description = req.Description
	if req.Classifiers != nil {
		crawler.Classifiers = req.Classifiers
	}
	if req.TablePrefix != "" {
		crawler.TablePrefix = req.TablePrefix
	}
	if req.Schedule != "" {
		crawler.Schedule = &GlueSchedule{ScheduleExpression: req.Schedule, State: "SCHEDULED"}
	}
	crawler.LastUpdated = glueEpochNow()
	crawler.Version++
	glueCrawlers.Put(req.Name, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	if crawler.State == "RUNNING" {
		glueWriteError(w, "CrawlerRunningException", "Cannot delete crawler while running: "+req.Name)
		return
	}
	glueCrawlers.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStartCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	if crawler.State == "RUNNING" {
		glueWriteError(w, "CrawlerRunningException", "Crawler is already running: "+req.Name)
		return
	}
	crawler.State = "RUNNING"
	crawler.LastUpdated = glueEpochNow()
	glueCrawlers.Put(req.Name, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStopCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	if crawler.State != "RUNNING" {
		glueWriteError(w, "CrawlerNotRunningException", "Crawler is not running: "+req.Name)
		return
	}
	crawler.State = "STOPPING"
	crawler.LastUpdated = glueEpochNow()
	glueCrawlers.Put(req.Name, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListCrawlers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueCrawlers.List()
	names := make([]string, 0, len(all))
	for _, c := range all {
		names = append(names, c.Name)
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, names, func(it string) string { return it }, req.NextToken, maxR, 25)
	if !pageOK {
		return
	}
	resp := map[string]any{"CrawlerNames": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueCreateTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string            `json:"Name"`
		WorkflowName    string            `json:"WorkflowName"`
		Type            string            `json:"Type"`
		Schedule        string            `json:"Schedule"`
		Predicate       map[string]any    `json:"Predicate"`
		Actions         []map[string]any  `json:"Actions"`
		Description     string            `json:"Description"`
		StartOnCreation bool              `json:"StartOnCreation"`
		Tags            map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}
	if req.Type == "" {
		glueWriteError(w, "InvalidInputException", "Type is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueTriggers.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Trigger already exists: "+req.Name)
		return
	}
	state := "CREATED"
	if req.Type == "ON_DEMAND" {
		state = "CREATED"
	} else if req.StartOnCreation {
		state = "ACTIVATED"
	}
	trigger := GlueTrigger{
		ID:           strings.ReplaceAll(uuid.New().String(), "-", ""),
		Name:         req.Name,
		WorkflowName: req.WorkflowName,
		Type:         req.Type,
		State:        state,
		Description:  req.Description,
		Schedule:     req.Schedule,
		Actions:      req.Actions,
		Predicate:    req.Predicate,
		Tags:         req.Tags,
	}
	glueTriggers.Put(req.Name, trigger)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

// glueTriggerWire strips the persistence-only Tags member from the wire shape;
// the real Trigger structure has no Tags field (tags ride GetTags).
type glueTriggerWire struct {
	GlueTrigger
}

func (t glueTriggerWire) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(t.GlueTrigger)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "Tags")
	return json.Marshal(m)
}

func handleGlueGetTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	trigger, ok := glueTriggers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Trigger not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Trigger": glueTriggerWire{trigger}})
}

func handleGlueGetTriggers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken        string `json:"NextToken"`
		DependentJobName string `json:"DependentJobName"`
		MaxResults       *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueTriggers.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, all, func(it GlueTrigger) string { return it.Name }, req.NextToken, maxR, 25)
	if !pageOK {
		return
	}
	triggers := make([]glueTriggerWire, 0, len(page))
	for _, t := range page {
		triggers = append(triggers, glueTriggerWire{t})
	}
	resp := map[string]any{"Triggers": triggers}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	glueTriggers.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueStartTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	trigger, ok := glueTriggers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Trigger not found: "+req.Name)
		return
	}
	trigger.State = "ACTIVATED"
	glueTriggers.Put(req.Name, trigger)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueStopTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	trigger, ok := glueTriggers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Trigger not found: "+req.Name)
		return
	}
	trigger.State = "DEACTIVATED"
	glueTriggers.Put(req.Name, trigger)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

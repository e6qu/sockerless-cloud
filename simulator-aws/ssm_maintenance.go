package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

// SSM Maintenance Windows — terraform's `aws_ssm_maintenance_window`,
// `aws_ssm_maintenance_window_target`, and `aws_ssm_maintenance_window_task`
// resources hit this slice. A window owns a recurring schedule plus a set
// of registered targets and tasks; the sim models all three as real
// control-plane state.

// SSMTarget is the Key/Values filter shared by targets and tasks.
type SSMTarget struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values"`
}

// SSMMaintenanceWindow is a recurring window definition.
type SSMMaintenanceWindow struct {
	WindowId                 string  `json:"WindowId"`
	Name                     string  `json:"Name"`
	Description              string  `json:"Description,omitempty"`
	Schedule                 string  `json:"Schedule"`
	ScheduleTimezone         string  `json:"ScheduleTimezone,omitempty"`
	ScheduleOffset           int     `json:"ScheduleOffset,omitempty"`
	StartDate                string  `json:"StartDate,omitempty"`
	EndDate                  string  `json:"EndDate,omitempty"`
	Duration                 int     `json:"Duration"`
	Cutoff                   int     `json:"Cutoff"`
	AllowUnassociatedTargets bool    `json:"AllowUnassociatedTargets"`
	Enabled                  bool    `json:"Enabled"`
	CreatedDate              float64 `json:"CreatedDate"`
	ModifiedDate             float64 `json:"ModifiedDate"`
}

// SSMMaintenanceWindowTarget is a registered target within a window.
type SSMMaintenanceWindowTarget struct {
	WindowId         string      `json:"WindowId"`
	WindowTargetId   string      `json:"WindowTargetId"`
	ResourceType     string      `json:"ResourceType"`
	Targets          []SSMTarget `json:"Targets"`
	OwnerInformation string      `json:"OwnerInformation,omitempty"`
	Name             string      `json:"Name,omitempty"`
	Description      string      `json:"Description,omitempty"`
}

// SSMMaintenanceWindowTask is a registered task within a window.
type SSMMaintenanceWindowTask struct {
	WindowId       string            `json:"WindowId"`
	WindowTaskId   string            `json:"WindowTaskId"`
	TaskArn        string            `json:"TaskArn"`
	Type           string            `json:"Type"`
	Targets        []SSMTarget       `json:"Targets"`
	TaskParameters map[string]any    `json:"TaskParameters,omitempty"`
	Priority       int               `json:"Priority"`
	ServiceRoleArn string            `json:"ServiceRoleArn,omitempty"`
	MaxConcurrency string            `json:"MaxConcurrency,omitempty"`
	MaxErrors      string            `json:"MaxErrors,omitempty"`
	Name           string            `json:"Name,omitempty"`
	Description    string            `json:"Description,omitempty"`
	CutoffBehavior string            `json:"CutoffBehavior,omitempty"`
	LoggingInfo    map[string]string `json:"LoggingInfo,omitempty"`
}

var (
	ssmWindows       sim.Store[SSMMaintenanceWindow]
	ssmWindowTargets sim.Store[SSMMaintenanceWindowTarget]
	ssmWindowTasks   sim.Store[SSMMaintenanceWindowTask]
)

func registerSSMMaintenanceWindows(r *AWSRouter, srv *sim.Server) {
	ssmWindows = sim.MakeStore[SSMMaintenanceWindow](srv.DB(), "ssm_maintenance_windows")
	ssmWindowTargets = sim.MakeStore[SSMMaintenanceWindowTarget](srv.DB(), "ssm_maintenance_window_targets")
	ssmWindowTasks = sim.MakeStore[SSMMaintenanceWindowTask](srv.DB(), "ssm_maintenance_window_tasks")

	r.Register("AmazonSSM.CreateMaintenanceWindow", handleSSMCreateMaintenanceWindow)
	r.Register("AmazonSSM.DeleteMaintenanceWindow", handleSSMDeleteMaintenanceWindow)
	r.Register("AmazonSSM.GetMaintenanceWindow", handleSSMGetMaintenanceWindow)
	r.Register("AmazonSSM.UpdateMaintenanceWindow", handleSSMUpdateMaintenanceWindow)
	r.Register("AmazonSSM.DescribeMaintenanceWindows", handleSSMDescribeMaintenanceWindows)
	r.Register("AmazonSSM.RegisterTargetWithMaintenanceWindow", handleSSMRegisterTarget)
	r.Register("AmazonSSM.RegisterTaskWithMaintenanceWindow", handleSSMRegisterTask)
	r.Register("AmazonSSM.DeregisterTargetFromMaintenanceWindow", handleSSMDeregisterTarget)
	r.Register("AmazonSSM.DeregisterTaskFromMaintenanceWindow", handleSSMDeregisterTask)
	r.Register("AmazonSSM.DescribeMaintenanceWindowTargets", handleSSMDescribeTargets)
	r.Register("AmazonSSM.DescribeMaintenanceWindowTasks", handleSSMDescribeTasks)
}

// newSSMWindowID returns an "mw-" + 17 lowercase hex chars ID per the
// MaintenanceWindowId pattern ^mw-[0-9a-f]{17}$.
func newSSMWindowID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return "mw-" + hex.EncodeToString(b)[:17]
}

func ssmWindowKey(windowID, id string) string {
	return windowID + "/" + id
}

// ssmWindowIdentityWire projects a window onto MaintenanceWindowIdentity
// (DescribeMaintenanceWindows). This shape lacks AllowUnassociatedTargets,
// CreatedDate, and ModifiedDate — emit only its defined members.
func ssmWindowIdentityWire(m SSMMaintenanceWindow) map[string]any {
	out := map[string]any{
		"WindowId": m.WindowId,
		"Name":     m.Name,
		"Schedule": m.Schedule,
		"Duration": m.Duration,
		"Cutoff":   m.Cutoff,
		"Enabled":  m.Enabled,
	}
	if m.Description != "" {
		out["Description"] = m.Description
	}
	if m.ScheduleTimezone != "" {
		out["ScheduleTimezone"] = m.ScheduleTimezone
	}
	if m.ScheduleOffset != 0 {
		out["ScheduleOffset"] = m.ScheduleOffset
	}
	if m.StartDate != "" {
		out["StartDate"] = m.StartDate
	}
	if m.EndDate != "" {
		out["EndDate"] = m.EndDate
	}
	return out
}

// ssmWindowWire projects a window onto the GetMaintenanceWindow /
// UpdateMaintenanceWindow shape, which adds AllowUnassociatedTargets.
func ssmWindowWire(m SSMMaintenanceWindow) map[string]any {
	out := ssmWindowIdentityWire(m)
	out["AllowUnassociatedTargets"] = m.AllowUnassociatedTargets
	return out
}

func handleSSMCreateMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                     string `json:"Name"`
		Description              string `json:"Description"`
		Schedule                 string `json:"Schedule"`
		ScheduleTimezone         string `json:"ScheduleTimezone"`
		ScheduleOffset           int    `json:"ScheduleOffset"`
		StartDate                string `json:"StartDate"`
		EndDate                  string `json:"EndDate"`
		Duration                 int    `json:"Duration"`
		Cutoff                   int    `json:"Cutoff"`
		AllowUnassociatedTargets bool   `json:"AllowUnassociatedTargets"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Schedule == "" || req.Duration == 0 {
		AWSError(w, "ValidationException", "Name, Schedule, and Duration are required", http.StatusBadRequest)
		return
	}
	now := float64(time.Now().Unix())
	m := SSMMaintenanceWindow{
		WindowId:                 newSSMWindowID(),
		Name:                     req.Name,
		Description:              req.Description,
		Schedule:                 req.Schedule,
		ScheduleTimezone:         req.ScheduleTimezone,
		ScheduleOffset:           req.ScheduleOffset,
		StartDate:                req.StartDate,
		EndDate:                  req.EndDate,
		Duration:                 req.Duration,
		Cutoff:                   req.Cutoff,
		AllowUnassociatedTargets: req.AllowUnassociatedTargets,
		Enabled:                  true,
		CreatedDate:              now,
		ModifiedDate:             now,
	}
	ssmWindows.Put(m.WindowId, m)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"WindowId": m.WindowId})
}

func handleSSMDeleteMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId string `json:"WindowId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmWindows.Get(req.WindowId); !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window %s doesn't exist.", req.WindowId)
		return
	}
	ssmWindows.Delete(req.WindowId)
	// Cascade-delete registered targets/tasks.
	for _, t := range ssmWindowTargets.List() {
		if t.WindowId == req.WindowId {
			ssmWindowTargets.Delete(ssmWindowKey(t.WindowId, t.WindowTargetId))
		}
	}
	for _, t := range ssmWindowTasks.List() {
		if t.WindowId == req.WindowId {
			ssmWindowTasks.Delete(ssmWindowKey(t.WindowId, t.WindowTaskId))
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"WindowId": req.WindowId})
}

func handleSSMGetMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId string `json:"WindowId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	m, ok := ssmWindows.Get(req.WindowId)
	if !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window %s doesn't exist.", req.WindowId)
		return
	}
	out := ssmWindowWire(m)
	out["CreatedDate"] = m.CreatedDate
	out["ModifiedDate"] = m.ModifiedDate
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSSMUpdateMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId                 string `json:"WindowId"`
		Name                     string `json:"Name"`
		Description              string `json:"Description"`
		Schedule                 string `json:"Schedule"`
		ScheduleTimezone         string `json:"ScheduleTimezone"`
		ScheduleOffset           *int   `json:"ScheduleOffset"`
		StartDate                string `json:"StartDate"`
		EndDate                  string `json:"EndDate"`
		Duration                 *int   `json:"Duration"`
		Cutoff                   *int   `json:"Cutoff"`
		AllowUnassociatedTargets *bool  `json:"AllowUnassociatedTargets"`
		Enabled                  *bool  `json:"Enabled"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	m, ok := ssmWindows.Get(req.WindowId)
	if !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window %s doesn't exist.", req.WindowId)
		return
	}
	if req.Name != "" {
		m.Name = req.Name
	}
	if req.Description != "" {
		m.Description = req.Description
	}
	if req.Schedule != "" {
		m.Schedule = req.Schedule
	}
	if req.ScheduleTimezone != "" {
		m.ScheduleTimezone = req.ScheduleTimezone
	}
	if req.ScheduleOffset != nil {
		m.ScheduleOffset = *req.ScheduleOffset
	}
	if req.StartDate != "" {
		m.StartDate = req.StartDate
	}
	if req.EndDate != "" {
		m.EndDate = req.EndDate
	}
	if req.Duration != nil {
		m.Duration = *req.Duration
	}
	if req.Cutoff != nil {
		m.Cutoff = *req.Cutoff
	}
	if req.AllowUnassociatedTargets != nil {
		m.AllowUnassociatedTargets = *req.AllowUnassociatedTargets
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	}
	m.ModifiedDate = float64(time.Now().Unix())
	ssmWindows.Put(m.WindowId, m)
	sim.WriteJSON(w, http.StatusOK, ssmWindowWire(m))
}

func handleSSMDescribeMaintenanceWindows(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmWindows.List()
	if all == nil {
		all = []SSMMaintenanceWindow{}
	}
	sortBy(all, func(m SSMMaintenanceWindow) string { return m.WindowId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, m := range page {
		out = append(out, ssmWindowIdentityWire(m))
	}
	resp := map[string]any{"WindowIdentities": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMRegisterTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId         string      `json:"WindowId"`
		ResourceType     string      `json:"ResourceType"`
		Targets          []SSMTarget `json:"Targets"`
		OwnerInformation string      `json:"OwnerInformation"`
		Name             string      `json:"Name"`
		Description      string      `json:"Description"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmWindows.Get(req.WindowId); !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window %s doesn't exist.", req.WindowId)
		return
	}
	if req.ResourceType == "" {
		req.ResourceType = "INSTANCE"
	}
	t := SSMMaintenanceWindowTarget{
		WindowId:         req.WindowId,
		WindowTargetId:   uuid.New().String(),
		ResourceType:     req.ResourceType,
		Targets:          req.Targets,
		OwnerInformation: req.OwnerInformation,
		Name:             req.Name,
		Description:      req.Description,
	}
	ssmWindowTargets.Put(ssmWindowKey(t.WindowId, t.WindowTargetId), t)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"WindowTargetId": t.WindowTargetId})
}

func handleSSMRegisterTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId       string         `json:"WindowId"`
		TaskArn        string         `json:"TaskArn"`
		TaskType       string         `json:"TaskType"`
		Targets        []SSMTarget    `json:"Targets"`
		TaskParameters map[string]any `json:"TaskParameters"`
		Priority       int            `json:"Priority"`
		ServiceRoleArn string         `json:"ServiceRoleArn"`
		MaxConcurrency string         `json:"MaxConcurrency"`
		MaxErrors      string         `json:"MaxErrors"`
		Name           string         `json:"Name"`
		Description    string         `json:"Description"`
		CutoffBehavior string         `json:"CutoffBehavior"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmWindows.Get(req.WindowId); !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window %s doesn't exist.", req.WindowId)
		return
	}
	if req.TaskArn == "" || req.TaskType == "" {
		AWSError(w, "ValidationException", "TaskArn and TaskType are required", http.StatusBadRequest)
		return
	}
	t := SSMMaintenanceWindowTask{
		WindowId:       req.WindowId,
		WindowTaskId:   uuid.New().String(),
		TaskArn:        req.TaskArn,
		Type:           req.TaskType,
		Targets:        req.Targets,
		TaskParameters: req.TaskParameters,
		Priority:       req.Priority,
		ServiceRoleArn: req.ServiceRoleArn,
		MaxConcurrency: req.MaxConcurrency,
		MaxErrors:      req.MaxErrors,
		Name:           req.Name,
		Description:    req.Description,
		CutoffBehavior: req.CutoffBehavior,
	}
	ssmWindowTasks.Put(ssmWindowKey(t.WindowId, t.WindowTaskId), t)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"WindowTaskId": t.WindowTaskId})
}

func handleSSMDeregisterTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId       string `json:"WindowId"`
		WindowTargetId string `json:"WindowTargetId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ssmWindowKey(req.WindowId, req.WindowTargetId)
	if _, ok := ssmWindowTargets.Get(key); !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Target %s doesn't exist in maintenance window %s.", req.WindowTargetId, req.WindowId)
		return
	}
	ssmWindowTargets.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"WindowId":       req.WindowId,
		"WindowTargetId": req.WindowTargetId,
	})
}

func handleSSMDeregisterTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId     string `json:"WindowId"`
		WindowTaskId string `json:"WindowTaskId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ssmWindowKey(req.WindowId, req.WindowTaskId)
	if _, ok := ssmWindowTasks.Get(key); !ok {
		AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Task %s doesn't exist in maintenance window %s.", req.WindowTaskId, req.WindowId)
		return
	}
	ssmWindowTasks.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"WindowId":     req.WindowId,
		"WindowTaskId": req.WindowTaskId,
	})
}

func handleSSMDescribeTargets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId   string `json:"WindowId"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var all []SSMMaintenanceWindowTarget
	for _, t := range ssmWindowTargets.List() {
		if t.WindowId == req.WindowId {
			all = append(all, t)
		}
	}
	sortBy(all, func(t SSMMaintenanceWindowTarget) string { return t.WindowTargetId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, t := range page {
		row := map[string]any{
			"WindowId":       t.WindowId,
			"WindowTargetId": t.WindowTargetId,
			"ResourceType":   t.ResourceType,
			"Targets":        ssmTargetsWire(t.Targets),
		}
		if t.OwnerInformation != "" {
			row["OwnerInformation"] = t.OwnerInformation
		}
		if t.Name != "" {
			row["Name"] = t.Name
		}
		if t.Description != "" {
			row["Description"] = t.Description
		}
		out = append(out, row)
	}
	resp := map[string]any{"Targets": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId   string `json:"WindowId"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var all []SSMMaintenanceWindowTask
	for _, t := range ssmWindowTasks.List() {
		if t.WindowId == req.WindowId {
			all = append(all, t)
		}
	}
	sortBy(all, func(t SSMMaintenanceWindowTask) string { return t.WindowTaskId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, t := range page {
		row := map[string]any{
			"WindowId":     t.WindowId,
			"WindowTaskId": t.WindowTaskId,
			"TaskArn":      t.TaskArn,
			"Type":         t.Type,
			"Targets":      ssmTargetsWire(t.Targets),
			"Priority":     t.Priority,
		}
		if t.TaskParameters != nil {
			row["TaskParameters"] = t.TaskParameters
		}
		if t.ServiceRoleArn != "" {
			row["ServiceRoleArn"] = t.ServiceRoleArn
		}
		if t.MaxConcurrency != "" {
			row["MaxConcurrency"] = t.MaxConcurrency
		}
		if t.MaxErrors != "" {
			row["MaxErrors"] = t.MaxErrors
		}
		if t.Name != "" {
			row["Name"] = t.Name
		}
		if t.Description != "" {
			row["Description"] = t.Description
		}
		if t.CutoffBehavior != "" {
			row["CutoffBehavior"] = t.CutoffBehavior
		}
		out = append(out, row)
	}
	resp := map[string]any{"Tasks": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func ssmTargetsWire(targets []SSMTarget) []map[string]any {
	out := make([]map[string]any, 0, len(targets))
	for _, t := range targets {
		out = append(out, map[string]any{
			"Key":    t.Key,
			"Values": t.Values,
		})
	}
	return out
}

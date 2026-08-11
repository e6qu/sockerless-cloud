package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/google/uuid"
)

// registerSSMOpsItems wires the OpsCenter (OpsItems), Maintenance Window
// execution-side reads, Session Manager, and hybrid-activation slices of
// Amazon Systems Manager (SSM). All are awsJson1.1 actions on the shared
// AmazonSSM router. The maintenance-window execution handlers read off the
// window/task control-plane stores owned by ssm_maintenance.go and settle a
// deterministic execution record per window on read.

// SSMOpsItem is an OpsCenter operational item.
type SSMOpsItem struct {
	OpsItemId        string                       `json:"OpsItemId"`
	OpsItemArn       string                       `json:"OpsItemArn"`
	OpsItemType      string                       `json:"OpsItemType,omitempty"`
	Title            string                       `json:"Title"`
	Description      string                       `json:"Description"`
	Source           string                       `json:"Source"`
	Status           string                       `json:"Status"`
	Priority         int                          `json:"Priority,omitempty"`
	Category         string                       `json:"Category,omitempty"`
	Severity         string                       `json:"Severity,omitempty"`
	Version          string                       `json:"Version"`
	CreatedBy        string                       `json:"CreatedBy"`
	CreatedTime      float64                      `json:"CreatedTime"`
	LastModifiedBy   string                       `json:"LastModifiedBy"`
	LastModifiedTime float64                      `json:"LastModifiedTime"`
	Notifications    []string                     `json:"Notifications,omitempty"`
	RelatedOpsItems  []string                     `json:"RelatedOpsItems,omitempty"`
	OperationalData  map[string]SSMOpsItemDataVal `json:"OperationalData,omitempty"`
}

// SSMOpsItemDataVal is one OperationalData map value.
type SSMOpsItemDataVal struct {
	Value string `json:"Value"`
	Type  string `json:"Type,omitempty"`
}

// SSMOpsItemRelatedItem is an OpsItem-to-resource association.
type SSMOpsItemRelatedItem struct {
	AssociationId    string  `json:"AssociationId"`
	OpsItemId        string  `json:"OpsItemId"`
	AssociationType  string  `json:"AssociationType"`
	ResourceType     string  `json:"ResourceType"`
	ResourceUri      string  `json:"ResourceUri"`
	CreatedBy        string  `json:"CreatedBy"`
	CreatedTime      float64 `json:"CreatedTime"`
	LastModifiedBy   string  `json:"LastModifiedBy"`
	LastModifiedTime float64 `json:"LastModifiedTime"`
}

// SSMOpsItemEvent is one entry in an OpsItem's activity timeline.
type SSMOpsItemEvent struct {
	OpsItemId   string  `json:"OpsItemId"`
	EventId     string  `json:"EventId"`
	Source      string  `json:"Source"`
	DetailType  string  `json:"DetailType"`
	Detail      string  `json:"Detail"`
	CreatedBy   string  `json:"CreatedBy"`
	CreatedTime float64 `json:"CreatedTime"`
}

// SSMSession is a Session Manager session. The WebSocket data plane is out of
// scope; StartSession returns the session metadata (id/token/streamUrl) only.
type SSMSession struct {
	SessionId    string  `json:"SessionId"`
	Target       string  `json:"Target"`
	Status       string  `json:"Status"`
	StartDate    float64 `json:"StartDate"`
	EndDate      float64 `json:"EndDate"`
	DocumentName string  `json:"DocumentName,omitempty"`
	Owner        string  `json:"Owner"`
	Reason       string  `json:"Reason,omitempty"`
	TokenValue   string  `json:"TokenValue"`
	StreamUrl    string  `json:"StreamUrl"`
}

// SSMActivation is a hybrid managed-instance registration activation.
type SSMActivation struct {
	ActivationId        string   `json:"ActivationId"`
	ActivationCode      string   `json:"ActivationCode"`
	Description         string   `json:"Description,omitempty"`
	DefaultInstanceName string   `json:"DefaultInstanceName,omitempty"`
	IamRole             string   `json:"IamRole"`
	RegistrationLimit   int      `json:"RegistrationLimit"`
	RegistrationsCount  int      `json:"RegistrationsCount"`
	ExpirationDate      float64  `json:"ExpirationDate"`
	Expired             bool     `json:"Expired"`
	CreatedDate         float64  `json:"CreatedDate"`
	Tags                []SSMTag `json:"Tags,omitempty"`
}

var (
	ssmOpsItems       sim.Store[SSMOpsItem]
	ssmOpsItemRelated sim.Store[SSMOpsItemRelatedItem]
	ssmOpsItemEvents  sim.Store[SSMOpsItemEvent]
	ssmSessions       sim.Store[SSMSession]
	ssmActivations    sim.Store[SSMActivation]
)

func registerSSMOpsItems(r *sim.AWSRouter, srv *sim.Server) {
	ssmOpsItems = sim.MakeStore[SSMOpsItem](srv.DB(), "ssm_opsitems")
	ssmOpsItemRelated = sim.MakeStore[SSMOpsItemRelatedItem](srv.DB(), "ssm_opsitem_related")
	ssmOpsItemEvents = sim.MakeStore[SSMOpsItemEvent](srv.DB(), "ssm_opsitem_events")
	ssmSessions = sim.MakeStore[SSMSession](srv.DB(), "ssm_sessions")
	ssmActivations = sim.MakeStore[SSMActivation](srv.DB(), "ssm_activations")

	for target, h := range map[string]http.HandlerFunc{
		// OpsItems (OpsCenter).
		"AmazonSSM.CreateOpsItem":                  handleSSMCreateOpsItem,
		"AmazonSSM.DeleteOpsItem":                  handleSSMDeleteOpsItem,
		"AmazonSSM.GetOpsItem":                     handleSSMGetOpsItem,
		"AmazonSSM.UpdateOpsItem":                  handleSSMUpdateOpsItem,
		"AmazonSSM.DescribeOpsItems":               handleSSMDescribeOpsItems,
		"AmazonSSM.AssociateOpsItemRelatedItem":    handleSSMAssociateOpsItemRelatedItem,
		"AmazonSSM.DisassociateOpsItemRelatedItem": handleSSMDisassociateOpsItemRelatedItem,
		"AmazonSSM.ListOpsItemEvents":              handleSSMListOpsItemEvents,
		"AmazonSSM.ListOpsItemRelatedItems":        handleSSMListOpsItemRelatedItems,

		// Maintenance Window execution-side reads/updates.
		"AmazonSSM.CancelMaintenanceWindowExecution":                  handleSSMCancelMaintenanceWindowExecution,
		"AmazonSSM.DescribeMaintenanceWindowExecutions":               handleSSMDescribeMaintenanceWindowExecutions,
		"AmazonSSM.DescribeMaintenanceWindowExecutionTasks":           handleSSMDescribeMaintenanceWindowExecutionTasks,
		"AmazonSSM.DescribeMaintenanceWindowExecutionTaskInvocations": handleSSMDescribeMaintenanceWindowExecutionTaskInvocations,
		"AmazonSSM.DescribeMaintenanceWindowSchedule":                 handleSSMDescribeMaintenanceWindowSchedule,
		"AmazonSSM.DescribeMaintenanceWindowsForTarget":               handleSSMDescribeMaintenanceWindowsForTarget,
		"AmazonSSM.GetMaintenanceWindowExecution":                     handleSSMGetMaintenanceWindowExecution,
		"AmazonSSM.GetMaintenanceWindowExecutionTask":                 handleSSMGetMaintenanceWindowExecutionTask,
		"AmazonSSM.GetMaintenanceWindowExecutionTaskInvocation":       handleSSMGetMaintenanceWindowExecutionTaskInvocation,
		"AmazonSSM.GetMaintenanceWindowTask":                          handleSSMGetMaintenanceWindowTask,
		"AmazonSSM.UpdateMaintenanceWindowTarget":                     handleSSMUpdateMaintenanceWindowTarget,
		"AmazonSSM.UpdateMaintenanceWindowTask":                       handleSSMUpdateMaintenanceWindowTask,

		// Session Manager.
		"AmazonSSM.StartSession":        handleSSMStartSession,
		"AmazonSSM.ResumeSession":       handleSSMResumeSession,
		"AmazonSSM.TerminateSession":    handleSSMTerminateSession,
		"AmazonSSM.DescribeSessions":    handleSSMDescribeSessions,
		"AmazonSSM.GetConnectionStatus": handleSSMGetConnectionStatus,

		// Hybrid activations.
		"AmazonSSM.CreateActivation":    handleSSMCreateActivation,
		"AmazonSSM.DeleteActivation":    handleSSMDeleteActivation,
		"AmazonSSM.DescribeActivations": handleSSMDescribeActivations,
	} {
		r.Register(target, h)
	}
}

// ---------------------------------------------------------------------------
// OpsItems
// ---------------------------------------------------------------------------

func newSSMOpsItemID() string {
	b := make([]byte, 14)
	_, _ = rand.Read(b)
	return "oi-" + hex.EncodeToString(b)[:12]
}

func ssmOpsItemArn(id string) string {
	return fmt.Sprintf("arn:aws:ssm:us-east-1:123456789012:opsitem/%s", id)
}

func ssmOpsDataWire(d map[string]SSMOpsItemDataVal) map[string]any {
	if len(d) == 0 {
		return nil
	}
	out := make(map[string]any, len(d))
	for k, v := range d {
		entry := map[string]any{"Value": v.Value}
		if v.Type != "" {
			entry["Type"] = v.Type
		}
		out[k] = entry
	}
	return out
}

func ssmNotificationsWire(arns []string) []map[string]any {
	if len(arns) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(arns))
	for _, a := range arns {
		out = append(out, map[string]any{"Arn": a})
	}
	return out
}

func ssmRelatedOpsItemsWire(ids []string) []map[string]any {
	if len(ids) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"OpsItemId": id})
	}
	return out
}

func ssmOpsItemWire(o SSMOpsItem) map[string]any {
	out := map[string]any{
		"OpsItemId":        o.OpsItemId,
		"OpsItemArn":       o.OpsItemArn,
		"Title":            o.Title,
		"Description":      o.Description,
		"Source":           o.Source,
		"Status":           o.Status,
		"Version":          o.Version,
		"CreatedBy":        o.CreatedBy,
		"CreatedTime":      o.CreatedTime,
		"LastModifiedBy":   o.LastModifiedBy,
		"LastModifiedTime": o.LastModifiedTime,
	}
	if o.OpsItemType != "" {
		out["OpsItemType"] = o.OpsItemType
	}
	if o.Priority != 0 {
		out["Priority"] = o.Priority
	}
	if o.Category != "" {
		out["Category"] = o.Category
	}
	if o.Severity != "" {
		out["Severity"] = o.Severity
	}
	if d := ssmOpsDataWire(o.OperationalData); d != nil {
		out["OperationalData"] = d
	}
	if n := ssmNotificationsWire(o.Notifications); n != nil {
		out["Notifications"] = n
	}
	if rel := ssmRelatedOpsItemsWire(o.RelatedOpsItems); rel != nil {
		out["RelatedOpsItems"] = rel
	}
	return out
}

// ssmOpsItemSummaryWire projects an OpsItem onto OpsItemSummary, the
// DescribeOpsItems list element (no Description/Notifications/Version).
func ssmOpsItemSummaryWire(o SSMOpsItem) map[string]any {
	out := map[string]any{
		"OpsItemId":        o.OpsItemId,
		"Title":            o.Title,
		"Source":           o.Source,
		"Status":           o.Status,
		"CreatedBy":        o.CreatedBy,
		"CreatedTime":      o.CreatedTime,
		"LastModifiedBy":   o.LastModifiedBy,
		"LastModifiedTime": o.LastModifiedTime,
	}
	if o.OpsItemType != "" {
		out["OpsItemType"] = o.OpsItemType
	}
	if o.Priority != 0 {
		out["Priority"] = o.Priority
	}
	if o.Category != "" {
		out["Category"] = o.Category
	}
	if o.Severity != "" {
		out["Severity"] = o.Severity
	}
	if d := ssmOpsDataWire(o.OperationalData); d != nil {
		out["OperationalData"] = d
	}
	return out
}

type ssmOpsDataReq map[string]struct {
	Value string `json:"Value"`
	Type  string `json:"Type"`
}

func (m ssmOpsDataReq) toModel() map[string]SSMOpsItemDataVal {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]SSMOpsItemDataVal, len(m))
	for k, v := range m {
		out[k] = SSMOpsItemDataVal{Value: v.Value, Type: v.Type}
	}
	return out
}

func handleSSMCreateOpsItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title           string        `json:"Title"`
		Description     string        `json:"Description"`
		Source          string        `json:"Source"`
		OpsItemType     string        `json:"OpsItemType"`
		Priority        int           `json:"Priority"`
		Category        string        `json:"Category"`
		Severity        string        `json:"Severity"`
		OperationalData ssmOpsDataReq `json:"OperationalData"`
		Notifications   []struct {
			Arn string `json:"Arn"`
		} `json:"Notifications"`
		RelatedOpsItems []struct {
			OpsItemId string `json:"OpsItemId"`
		} `json:"RelatedOpsItems"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title == "" || req.Description == "" || req.Source == "" {
		sim.AWSError(w, "ValidationException", "Title, Description, and Source are required", http.StatusBadRequest)
		return
	}
	now := float64(time.Now().Unix())
	id := newSSMOpsItemID()
	o := SSMOpsItem{
		OpsItemId:        id,
		OpsItemArn:       ssmOpsItemArn(id),
		OpsItemType:      req.OpsItemType,
		Title:            req.Title,
		Description:      req.Description,
		Source:           req.Source,
		Status:           "Open",
		Priority:         req.Priority,
		Category:         req.Category,
		Severity:         req.Severity,
		Version:          "1",
		CreatedBy:        "arn:aws:iam::123456789012:user/sockerless",
		CreatedTime:      now,
		LastModifiedBy:   "arn:aws:iam::123456789012:user/sockerless",
		LastModifiedTime: now,
		OperationalData:  req.OperationalData.toModel(),
	}
	for _, n := range req.Notifications {
		o.Notifications = append(o.Notifications, n.Arn)
	}
	for _, rel := range req.RelatedOpsItems {
		o.RelatedOpsItems = append(o.RelatedOpsItems, rel.OpsItemId)
	}
	ssmOpsItems.Put(o.OpsItemId, o)

	ssmRecordOpsItemEvent(o.OpsItemId, "OpsCenter", "OpsItem Create", "Created OpsItem")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"OpsItemId":  o.OpsItemId,
		"OpsItemArn": o.OpsItemArn,
	})
}

func handleSSMGetOpsItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpsItemId string `json:"OpsItemId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	o, ok := ssmOpsItems.Get(req.OpsItemId)
	if !ok {
		sim.AWSErrorf(w, "OpsItemNotFoundException", http.StatusBadRequest,
			"OpsItem %s does not exist.", req.OpsItemId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"OpsItem": ssmOpsItemWire(o)})
}

func handleSSMDeleteOpsItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpsItemId string `json:"OpsItemId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmOpsItems.Get(req.OpsItemId); !ok {
		sim.AWSErrorf(w, "OpsItemNotFoundException", http.StatusBadRequest,
			"OpsItem %s does not exist.", req.OpsItemId)
		return
	}
	ssmOpsItems.Delete(req.OpsItemId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMUpdateOpsItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpsItemId               string        `json:"OpsItemId"`
		Title                   string        `json:"Title"`
		Description             string        `json:"Description"`
		Status                  string        `json:"Status"`
		Priority                *int          `json:"Priority"`
		Category                string        `json:"Category"`
		Severity                string        `json:"Severity"`
		OperationalData         ssmOpsDataReq `json:"OperationalData"`
		OperationalDataToDelete []string      `json:"OperationalDataToDelete"`
		Notifications           []struct {
			Arn string `json:"Arn"`
		} `json:"Notifications"`
		RelatedOpsItems []struct {
			OpsItemId string `json:"OpsItemId"`
		} `json:"RelatedOpsItems"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	o, ok := ssmOpsItems.Get(req.OpsItemId)
	if !ok {
		sim.AWSErrorf(w, "OpsItemNotFoundException", http.StatusBadRequest,
			"OpsItem %s does not exist.", req.OpsItemId)
		return
	}
	if req.Title != "" {
		o.Title = req.Title
	}
	if req.Description != "" {
		o.Description = req.Description
	}
	if req.Status != "" {
		o.Status = req.Status
	}
	if req.Priority != nil {
		o.Priority = *req.Priority
	}
	if req.Category != "" {
		o.Category = req.Category
	}
	if req.Severity != "" {
		o.Severity = req.Severity
	}
	if len(req.OperationalData) > 0 {
		if o.OperationalData == nil {
			o.OperationalData = map[string]SSMOpsItemDataVal{}
		}
		for k, v := range req.OperationalData {
			o.OperationalData[k] = SSMOpsItemDataVal{Value: v.Value, Type: v.Type}
		}
	}
	for _, k := range req.OperationalDataToDelete {
		delete(o.OperationalData, k)
	}
	if req.Notifications != nil {
		o.Notifications = o.Notifications[:0]
		for _, n := range req.Notifications {
			o.Notifications = append(o.Notifications, n.Arn)
		}
	}
	if req.RelatedOpsItems != nil {
		o.RelatedOpsItems = o.RelatedOpsItems[:0]
		for _, rel := range req.RelatedOpsItems {
			o.RelatedOpsItems = append(o.RelatedOpsItems, rel.OpsItemId)
		}
	}
	o.Version = ssmBumpVersion(o.Version)
	o.LastModifiedTime = float64(time.Now().Unix())
	ssmOpsItems.Put(o.OpsItemId, o)

	ssmRecordOpsItemEvent(o.OpsItemId, "OpsCenter", "OpsItem Update", "Updated OpsItem")
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func ssmBumpVersion(v string) string {
	n := 1
	_, _ = fmt.Sscanf(v, "%d", &n)
	return fmt.Sprintf("%d", n+1)
}

func handleSSMDescribeOpsItems(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmOpsItems.List()
	if all == nil {
		all = []SSMOpsItem{}
	}
	sortBy(all, func(o SSMOpsItem) string { return o.OpsItemId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, o := range page {
		out = append(out, ssmOpsItemSummaryWire(o))
	}
	resp := map[string]any{"OpsItemSummaries": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMAssociateOpsItemRelatedItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpsItemId       string `json:"OpsItemId"`
		AssociationType string `json:"AssociationType"`
		ResourceType    string `json:"ResourceType"`
		ResourceUri     string `json:"ResourceUri"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmOpsItems.Get(req.OpsItemId); !ok {
		sim.AWSErrorf(w, "OpsItemNotFoundException", http.StatusBadRequest,
			"OpsItem %s does not exist.", req.OpsItemId)
		return
	}
	now := float64(time.Now().Unix())
	rel := SSMOpsItemRelatedItem{
		AssociationId:    uuid.New().String(),
		OpsItemId:        req.OpsItemId,
		AssociationType:  req.AssociationType,
		ResourceType:     req.ResourceType,
		ResourceUri:      req.ResourceUri,
		CreatedBy:        "arn:aws:iam::123456789012:user/sockerless",
		CreatedTime:      now,
		LastModifiedBy:   "arn:aws:iam::123456789012:user/sockerless",
		LastModifiedTime: now,
	}
	ssmOpsItemRelated.Put(rel.AssociationId, rel)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AssociationId": rel.AssociationId})
}

func handleSSMDisassociateOpsItemRelatedItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpsItemId     string `json:"OpsItemId"`
		AssociationId string `json:"AssociationId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	rel, ok := ssmOpsItemRelated.Get(req.AssociationId)
	if !ok || rel.OpsItemId != req.OpsItemId {
		sim.AWSErrorf(w, "OpsItemRelatedItemAssociationNotFoundException", http.StatusBadRequest,
			"Related item association %s does not exist.", req.AssociationId)
		return
	}
	ssmOpsItemRelated.Delete(req.AssociationId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func ssmRecordOpsItemEvent(opsItemID, source, detailType, detail string) {
	ev := SSMOpsItemEvent{
		OpsItemId:   opsItemID,
		EventId:     uuid.New().String(),
		Source:      source,
		DetailType:  detailType,
		Detail:      detail,
		CreatedBy:   "arn:aws:iam::123456789012:user/sockerless",
		CreatedTime: float64(time.Now().Unix()),
	}
	ssmOpsItemEvents.Put(ev.EventId, ev)
}

func handleSSMListOpsItemEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters []struct {
			Key      string   `json:"Key"`
			Values   []string `json:"Values"`
			Operator string   `json:"Operator"`
		} `json:"Filters"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// The OpsItemId filter scopes events to a single item.
	var wantOpsItem string
	for _, f := range req.Filters {
		if f.Key == "OpsItemId" && len(f.Values) > 0 {
			wantOpsItem = f.Values[0]
		}
	}
	var all []SSMOpsItemEvent
	for _, ev := range ssmOpsItemEvents.List() {
		if wantOpsItem != "" && ev.OpsItemId != wantOpsItem {
			continue
		}
		all = append(all, ev)
	}
	sortBy(all, func(e SSMOpsItemEvent) string { return e.EventId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, ev := range page {
		out = append(out, map[string]any{
			"OpsItemId":   ev.OpsItemId,
			"EventId":     ev.EventId,
			"Source":      ev.Source,
			"DetailType":  ev.DetailType,
			"Detail":      ev.Detail,
			"CreatedBy":   map[string]any{"Arn": ev.CreatedBy},
			"CreatedTime": ev.CreatedTime,
		})
	}
	resp := map[string]any{"Summaries": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMListOpsItemRelatedItems(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpsItemId  string `json:"OpsItemId"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var all []SSMOpsItemRelatedItem
	for _, rel := range ssmOpsItemRelated.List() {
		if req.OpsItemId != "" && rel.OpsItemId != req.OpsItemId {
			continue
		}
		all = append(all, rel)
	}
	sortBy(all, func(rel SSMOpsItemRelatedItem) string { return rel.AssociationId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, rel := range page {
		out = append(out, map[string]any{
			"OpsItemId":        rel.OpsItemId,
			"AssociationId":    rel.AssociationId,
			"ResourceType":     rel.ResourceType,
			"AssociationType":  rel.AssociationType,
			"ResourceUri":      rel.ResourceUri,
			"CreatedBy":        map[string]any{"Arn": rel.CreatedBy},
			"CreatedTime":      rel.CreatedTime,
			"LastModifiedBy":   map[string]any{"Arn": rel.LastModifiedBy},
			"LastModifiedTime": rel.LastModifiedTime,
		})
	}
	resp := map[string]any{"Summaries": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Maintenance Window executions
//
// The sim has no native maintenance-window trigger, so a window's execution is
// settled deterministically on read: a single SUCCESS execution per window,
// one task-execution per registered task, one invocation per task. The IDs are
// derived from the window/task IDs so successive reads are stable.
// ---------------------------------------------------------------------------

const ssmMWExecStatus = "SUCCESS"

// ssmWindowExecID returns a deterministic UUID-shaped execution id for a window.
func ssmWindowExecID(windowID string) string {
	return uuid.NewSHA1(uuid.Nil, []byte("mwexec/"+windowID)).String()
}

// ssmWindowExecTaskID returns a deterministic task-execution id for a task.
func ssmWindowExecTaskID(windowTaskID string) string {
	return uuid.NewSHA1(uuid.Nil, []byte("mwexectask/"+windowTaskID)).String()
}

// ssmWindowExecInvocationID returns a deterministic invocation id for a task.
func ssmWindowExecInvocationID(windowTaskID string) string {
	return uuid.NewSHA1(uuid.Nil, []byte("mwexecinv/"+windowTaskID)).String()
}

// ssmWindowForExec finds the window whose deterministic execution id matches.
func ssmWindowForExec(execID string) (SSMMaintenanceWindow, bool) {
	for _, m := range ssmWindows.List() {
		if ssmWindowExecID(m.WindowId) == execID {
			return m, true
		}
	}
	return SSMMaintenanceWindow{}, false
}

// ssmTasksForWindow returns the registered tasks of a window, ordered.
func ssmTasksForWindow(windowID string) []SSMMaintenanceWindowTask {
	var tasks []SSMMaintenanceWindowTask
	for _, t := range ssmWindowTasks.List() {
		if t.WindowId == windowID {
			tasks = append(tasks, t)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].WindowTaskId < tasks[j].WindowTaskId })
	return tasks
}

func handleSSMCancelMaintenanceWindowExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmWindowForExec(req.WindowExecutionId); !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution %s doesn't exist.", req.WindowExecutionId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"WindowExecutionId": req.WindowExecutionId})
}

func handleSSMDescribeMaintenanceWindowExecutions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId   string `json:"WindowId"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	m, ok := ssmWindows.Get(req.WindowId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window %s doesn't exist.", req.WindowId)
		return
	}
	execID := ssmWindowExecID(m.WindowId)
	out := []map[string]any{{
		"WindowId":          m.WindowId,
		"WindowExecutionId": execID,
		"Status":            ssmMWExecStatus,
		"StartTime":         m.CreatedDate,
		"EndTime":           m.CreatedDate + 60,
	}}
	page, next := awsPage(out, req.NextToken, req.MaxResults, 50)
	resp := map[string]any{"WindowExecutions": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeMaintenanceWindowExecutionTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
		NextToken         string `json:"NextToken"`
		MaxResults        int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	m, ok := ssmWindowForExec(req.WindowExecutionId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution %s doesn't exist.", req.WindowExecutionId)
		return
	}
	var rows []map[string]any
	for _, t := range ssmTasksForWindow(m.WindowId) {
		rows = append(rows, map[string]any{
			"WindowExecutionId": req.WindowExecutionId,
			"TaskExecutionId":   ssmWindowExecTaskID(t.WindowTaskId),
			"Status":            ssmMWExecStatus,
			"StartTime":         m.CreatedDate,
			"EndTime":           m.CreatedDate + 60,
			"TaskArn":           t.TaskArn,
			"TaskType":          t.Type,
		})
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	page, next := awsPage(rows, req.NextToken, req.MaxResults, 50)
	resp := map[string]any{"WindowExecutionTaskIdentities": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeMaintenanceWindowExecutionTaskInvocations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
		TaskId            string `json:"TaskId"`
		NextToken         string `json:"NextToken"`
		MaxResults        int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	m, ok := ssmWindowForExec(req.WindowExecutionId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution %s doesn't exist.", req.WindowExecutionId)
		return
	}
	var rows []map[string]any
	for _, t := range ssmTasksForWindow(m.WindowId) {
		taskExecID := ssmWindowExecTaskID(t.WindowTaskId)
		if req.TaskId != "" && req.TaskId != taskExecID {
			continue
		}
		rows = append(rows, map[string]any{
			"WindowExecutionId": req.WindowExecutionId,
			"TaskExecutionId":   taskExecID,
			"InvocationId":      ssmWindowExecInvocationID(t.WindowTaskId),
			"ExecutionId":       uuid.NewSHA1(uuid.Nil, []byte("mwexecid/"+t.WindowTaskId)).String(),
			"TaskType":          t.Type,
			"Parameters":        "{}",
			"Status":            ssmMWExecStatus,
			"StartTime":         m.CreatedDate,
			"EndTime":           m.CreatedDate + 60,
		})
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	page, next := awsPage(rows, req.NextToken, req.MaxResults, 50)
	resp := map[string]any{"WindowExecutionTaskInvocationIdentities": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMGetMaintenanceWindowExecution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	m, ok := ssmWindowForExec(req.WindowExecutionId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution %s doesn't exist.", req.WindowExecutionId)
		return
	}
	taskIDs := []string{}
	for _, t := range ssmTasksForWindow(m.WindowId) {
		taskIDs = append(taskIDs, ssmWindowExecTaskID(t.WindowTaskId))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"WindowExecutionId": req.WindowExecutionId,
		"TaskIds":           taskIDs,
		"Status":            ssmMWExecStatus,
		"StartTime":         m.CreatedDate,
		"EndTime":           m.CreatedDate + 60,
	})
}

// ssmTaskForExecTask finds the registered task whose deterministic
// task-execution id matches, scoped to the window owning the execution.
func ssmTaskForExecTask(execID, taskExecID string) (SSMMaintenanceWindow, SSMMaintenanceWindowTask, bool) {
	m, ok := ssmWindowForExec(execID)
	if !ok {
		return SSMMaintenanceWindow{}, SSMMaintenanceWindowTask{}, false
	}
	for _, t := range ssmTasksForWindow(m.WindowId) {
		if ssmWindowExecTaskID(t.WindowTaskId) == taskExecID {
			return m, t, true
		}
	}
	return SSMMaintenanceWindow{}, SSMMaintenanceWindowTask{}, false
}

func handleSSMGetMaintenanceWindowExecutionTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
		TaskId            string `json:"TaskId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	m, t, ok := ssmTaskForExecTask(req.WindowExecutionId, req.TaskId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution task %s doesn't exist.", req.TaskId)
		return
	}
	out := map[string]any{
		"WindowExecutionId": req.WindowExecutionId,
		"TaskExecutionId":   req.TaskId,
		"TaskArn":           t.TaskArn,
		"Type":              t.Type,
		"Priority":          t.Priority,
		"Status":            ssmMWExecStatus,
		"StartTime":         m.CreatedDate,
		"EndTime":           m.CreatedDate + 60,
	}
	if t.ServiceRoleArn != "" {
		out["ServiceRole"] = t.ServiceRoleArn
	}
	if t.MaxConcurrency != "" {
		out["MaxConcurrency"] = t.MaxConcurrency
	}
	if t.MaxErrors != "" {
		out["MaxErrors"] = t.MaxErrors
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSSMGetMaintenanceWindowExecutionTaskInvocation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowExecutionId string `json:"WindowExecutionId"`
		TaskId            string `json:"TaskId"`
		InvocationId      string `json:"InvocationId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	m, t, ok := ssmTaskForExecTask(req.WindowExecutionId, req.TaskId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Maintenance window execution task invocation %s doesn't exist.", req.InvocationId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"WindowExecutionId": req.WindowExecutionId,
		"TaskExecutionId":   req.TaskId,
		"InvocationId":      ssmWindowExecInvocationID(t.WindowTaskId),
		"ExecutionId":       uuid.NewSHA1(uuid.Nil, []byte("mwexecid/"+t.WindowTaskId)).String(),
		"TaskType":          t.Type,
		"Parameters":        "{}",
		"Status":            ssmMWExecStatus,
		"StartTime":         m.CreatedDate,
		"EndTime":           m.CreatedDate + 60,
	})
}

func handleSSMGetMaintenanceWindowTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId     string `json:"WindowId"`
		WindowTaskId string `json:"WindowTaskId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ssmWindowTasks.Get(ssmWindowKey(req.WindowId, req.WindowTaskId))
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Task %s doesn't exist in maintenance window %s.", req.WindowTaskId, req.WindowId)
		return
	}
	out := map[string]any{
		"WindowId":     t.WindowId,
		"WindowTaskId": t.WindowTaskId,
		"TaskArn":      t.TaskArn,
		"TaskType":     t.Type,
		"Targets":      ssmTargetsWire(t.Targets),
		"Priority":     t.Priority,
	}
	if t.TaskParameters != nil {
		out["TaskParameters"] = t.TaskParameters
	}
	if t.ServiceRoleArn != "" {
		out["ServiceRoleArn"] = t.ServiceRoleArn
	}
	if t.MaxConcurrency != "" {
		out["MaxConcurrency"] = t.MaxConcurrency
	}
	if t.MaxErrors != "" {
		out["MaxErrors"] = t.MaxErrors
	}
	if t.Name != "" {
		out["Name"] = t.Name
	}
	if t.Description != "" {
		out["Description"] = t.Description
	}
	if t.CutoffBehavior != "" {
		out["CutoffBehavior"] = t.CutoffBehavior
	}
	if t.LoggingInfo != nil {
		out["LoggingInfo"] = t.LoggingInfo
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSSMDescribeMaintenanceWindowSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId   string `json:"WindowId"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var rows []map[string]any
	for _, m := range ssmWindows.List() {
		if req.WindowId != "" && m.WindowId != req.WindowId {
			continue
		}
		if !m.Enabled {
			continue
		}
		rows = append(rows, map[string]any{
			"WindowId":      m.WindowId,
			"Name":          m.Name,
			"ExecutionTime": time.Unix(int64(m.CreatedDate)+86400, 0).UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["WindowId"]) < fmt.Sprint(rows[j]["WindowId"]) })
	if rows == nil {
		rows = []map[string]any{}
	}
	page, next := awsPage(rows, req.NextToken, req.MaxResults, 50)
	resp := map[string]any{"ScheduledWindowExecutions": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeMaintenanceWindowsForTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Targets []struct {
			Key    string   `json:"Key"`
			Values []string `json:"Values"`
		} `json:"Targets"`
		ResourceType string `json:"ResourceType"`
		NextToken    string `json:"NextToken"`
		MaxResults   int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// A window matches a target if it has a registered target. Project the
	// windows that own at least one registered target.
	owners := map[string]bool{}
	for _, t := range ssmWindowTargets.List() {
		owners[t.WindowId] = true
	}
	var rows []map[string]any
	for _, m := range ssmWindows.List() {
		if !owners[m.WindowId] {
			continue
		}
		rows = append(rows, map[string]any{
			"WindowId": m.WindowId,
			"Name":     m.Name,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["WindowId"]) < fmt.Sprint(rows[j]["WindowId"]) })
	if rows == nil {
		rows = []map[string]any{}
	}
	page, next := awsPage(rows, req.NextToken, req.MaxResults, 50)
	resp := map[string]any{"WindowIdentities": page}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMUpdateMaintenanceWindowTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId         string      `json:"WindowId"`
		WindowTargetId   string      `json:"WindowTargetId"`
		Targets          []SSMTarget `json:"Targets"`
		OwnerInformation *string     `json:"OwnerInformation"`
		Name             *string     `json:"Name"`
		Description      *string     `json:"Description"`
		Replace          *bool       `json:"Replace"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ssmWindowKey(req.WindowId, req.WindowTargetId)
	t, ok := ssmWindowTargets.Get(key)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Target %s doesn't exist in maintenance window %s.", req.WindowTargetId, req.WindowId)
		return
	}
	replace := req.Replace != nil && *req.Replace
	if req.Targets != nil {
		t.Targets = req.Targets
	}
	if req.OwnerInformation != nil {
		t.OwnerInformation = *req.OwnerInformation
	} else if replace {
		t.OwnerInformation = ""
	}
	if req.Name != nil {
		t.Name = *req.Name
	} else if replace {
		t.Name = ""
	}
	if req.Description != nil {
		t.Description = *req.Description
	} else if replace {
		t.Description = ""
	}
	ssmWindowTargets.Put(key, t)
	out := map[string]any{
		"WindowId":       t.WindowId,
		"WindowTargetId": t.WindowTargetId,
		"Targets":        ssmTargetsWire(t.Targets),
	}
	if t.OwnerInformation != "" {
		out["OwnerInformation"] = t.OwnerInformation
	}
	if t.Name != "" {
		out["Name"] = t.Name
	}
	if t.Description != "" {
		out["Description"] = t.Description
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSSMUpdateMaintenanceWindowTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WindowId       string         `json:"WindowId"`
		WindowTaskId   string         `json:"WindowTaskId"`
		Targets        []SSMTarget    `json:"Targets"`
		TaskArn        string         `json:"TaskArn"`
		ServiceRoleArn *string        `json:"ServiceRoleArn"`
		TaskParameters map[string]any `json:"TaskParameters"`
		Priority       *int           `json:"Priority"`
		MaxConcurrency *string        `json:"MaxConcurrency"`
		MaxErrors      *string        `json:"MaxErrors"`
		Name           *string        `json:"Name"`
		Description    *string        `json:"Description"`
		CutoffBehavior *string        `json:"CutoffBehavior"`
		Replace        *bool          `json:"Replace"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ssmWindowKey(req.WindowId, req.WindowTaskId)
	t, ok := ssmWindowTasks.Get(key)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Task %s doesn't exist in maintenance window %s.", req.WindowTaskId, req.WindowId)
		return
	}
	replace := req.Replace != nil && *req.Replace
	if req.Targets != nil {
		t.Targets = req.Targets
	}
	if req.TaskArn != "" {
		t.TaskArn = req.TaskArn
	}
	if req.ServiceRoleArn != nil {
		t.ServiceRoleArn = *req.ServiceRoleArn
	} else if replace {
		t.ServiceRoleArn = ""
	}
	if req.TaskParameters != nil {
		t.TaskParameters = req.TaskParameters
	} else if replace {
		t.TaskParameters = nil
	}
	if req.Priority != nil {
		t.Priority = *req.Priority
	}
	if req.MaxConcurrency != nil {
		t.MaxConcurrency = *req.MaxConcurrency
	}
	if req.MaxErrors != nil {
		t.MaxErrors = *req.MaxErrors
	}
	if req.Name != nil {
		t.Name = *req.Name
	} else if replace {
		t.Name = ""
	}
	if req.Description != nil {
		t.Description = *req.Description
	} else if replace {
		t.Description = ""
	}
	if req.CutoffBehavior != nil {
		t.CutoffBehavior = *req.CutoffBehavior
	}
	ssmWindowTasks.Put(key, t)
	out := map[string]any{
		"WindowId":     t.WindowId,
		"WindowTaskId": t.WindowTaskId,
		"TaskArn":      t.TaskArn,
		"Targets":      ssmTargetsWire(t.Targets),
		"Priority":     t.Priority,
	}
	if t.TaskParameters != nil {
		out["TaskParameters"] = t.TaskParameters
	}
	if t.ServiceRoleArn != "" {
		out["ServiceRoleArn"] = t.ServiceRoleArn
	}
	if t.MaxConcurrency != "" {
		out["MaxConcurrency"] = t.MaxConcurrency
	}
	if t.MaxErrors != "" {
		out["MaxErrors"] = t.MaxErrors
	}
	if t.Name != "" {
		out["Name"] = t.Name
	}
	if t.Description != "" {
		out["Description"] = t.Description
	}
	if t.CutoffBehavior != "" {
		out["CutoffBehavior"] = t.CutoffBehavior
	}
	if t.LoggingInfo != nil {
		out["LoggingInfo"] = t.LoggingInfo
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Session Manager
// ---------------------------------------------------------------------------

func newSSMSessionID() string {
	return "sockerless-" + uuid.New().String()[:24]
}

func ssmTokenValue() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func ssmStreamURL(sessionID string) string {
	return "wss://ssmmessages.us-east-1.amazonaws.com/v1/data-channel/" + sessionID + "?role=publish_subscribe"
}

func handleSSMStartSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target       string `json:"Target"`
		DocumentName string `json:"DocumentName"`
		Reason       string `json:"Reason"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Target == "" {
		sim.AWSError(w, "ValidationException", "Target is required", http.StatusBadRequest)
		return
	}
	sess := SSMSession{
		SessionId:    newSSMSessionID(),
		Target:       req.Target,
		Status:       "Connected",
		StartDate:    float64(time.Now().Unix()),
		DocumentName: req.DocumentName,
		Owner:        "arn:aws:iam::123456789012:user/sockerless",
		Reason:       req.Reason,
		TokenValue:   ssmTokenValue(),
	}
	sess.StreamUrl = ssmStreamURL(sess.SessionId)
	ssmSessions.Put(sess.SessionId, sess)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"SessionId":  sess.SessionId,
		"TokenValue": sess.TokenValue,
		"StreamUrl":  sess.StreamUrl,
	})
}

func handleSSMResumeSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionId string `json:"SessionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sess, ok := ssmSessions.Get(req.SessionId)
	if !ok {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"Session %s does not exist.", req.SessionId)
		return
	}
	sess.Status = "Connected"
	sess.EndDate = 0
	sess.TokenValue = ssmTokenValue()
	sess.StreamUrl = ssmStreamURL(sess.SessionId)
	ssmSessions.Put(sess.SessionId, sess)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"SessionId":  sess.SessionId,
		"TokenValue": sess.TokenValue,
		"StreamUrl":  sess.StreamUrl,
	})
}

func handleSSMTerminateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionId string `json:"SessionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sess, ok := ssmSessions.Get(req.SessionId)
	if !ok {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"Session %s does not exist.", req.SessionId)
		return
	}
	sess.Status = "Terminated"
	sess.EndDate = float64(time.Now().Unix())
	ssmSessions.Put(sess.SessionId, sess)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"SessionId": sess.SessionId})
}

func handleSSMDescribeSessions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State      string `json:"State"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// State "Active" => Connected/Connecting; "History" => terminated etc.
	active := req.State == "" || req.State == "Active"
	var all []SSMSession
	for _, s := range ssmSessions.List() {
		isActive := s.Status == "Connected" || s.Status == "Connecting"
		if active && !isActive {
			continue
		}
		if !active && isActive {
			continue
		}
		all = append(all, s)
	}
	sortBy(all, func(s SSMSession) string { return s.SessionId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, s := range page {
		row := map[string]any{
			"SessionId": s.SessionId,
			"Target":    s.Target,
			"Status":    s.Status,
			"StartDate": s.StartDate,
			"Owner":     s.Owner,
		}
		if s.EndDate != 0 {
			row["EndDate"] = s.EndDate
		}
		if s.DocumentName != "" {
			row["DocumentName"] = s.DocumentName
		}
		if s.Reason != "" {
			row["Reason"] = s.Reason
		}
		out = append(out, row)
	}
	resp := map[string]any{"Sessions": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMGetConnectionStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"Target"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Target == "" {
		sim.AWSError(w, "ValidationException", "Target is required", http.StatusBadRequest)
		return
	}
	// An instance is "connected" if it has an active session, else "notconnected".
	status := "notconnected"
	for _, s := range ssmSessions.List() {
		if s.Target == req.Target && (s.Status == "Connected" || s.Status == "Connecting") {
			status = "connected"
			break
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Target": req.Target,
		"Status": status,
	})
}

// ---------------------------------------------------------------------------
// Hybrid activations
// ---------------------------------------------------------------------------

func newSSMActivationID() string {
	return uuid.New().String()
}

func ssmActivationCode() string {
	b := make([]byte, 15)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:20]
}

func handleSSMCreateActivation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description         string   `json:"Description"`
		DefaultInstanceName string   `json:"DefaultInstanceName"`
		IamRole             string   `json:"IamRole"`
		RegistrationLimit   int      `json:"RegistrationLimit"`
		ExpirationDate      *float64 `json:"ExpirationDate"`
		Tags                []SSMTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.IamRole == "" {
		sim.AWSError(w, "ValidationException", "IamRole is required", http.StatusBadRequest)
		return
	}
	now := float64(time.Now().Unix())
	limit := req.RegistrationLimit
	if limit == 0 {
		limit = 1
	}
	exp := now + 86400 // default 24h
	if req.ExpirationDate != nil {
		exp = *req.ExpirationDate
	}
	a := SSMActivation{
		ActivationId:        newSSMActivationID(),
		ActivationCode:      ssmActivationCode(),
		Description:         req.Description,
		DefaultInstanceName: req.DefaultInstanceName,
		IamRole:             req.IamRole,
		RegistrationLimit:   limit,
		RegistrationsCount:  0,
		ExpirationDate:      exp,
		Expired:             false,
		CreatedDate:         now,
		Tags:                req.Tags,
	}
	ssmActivations.Put(a.ActivationId, a)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ActivationId":   a.ActivationId,
		"ActivationCode": a.ActivationCode,
	})
}

func handleSSMDeleteActivation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ActivationId string `json:"ActivationId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmActivations.Get(req.ActivationId); !ok {
		sim.AWSErrorf(w, "InvalidActivationId", http.StatusBadRequest,
			"Activation %s does not exist.", req.ActivationId)
		return
	}
	ssmActivations.Delete(req.ActivationId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMDescribeActivations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	now := float64(time.Now().Unix())
	all := ssmActivations.List()
	if all == nil {
		all = []SSMActivation{}
	}
	sortBy(all, func(a SSMActivation) string { return a.ActivationId })
	page, next := awsPageExplicit(all, req.NextToken, req.MaxResults)
	out := make([]map[string]any, 0, len(page))
	for _, a := range page {
		expired := a.ExpirationDate != 0 && now > a.ExpirationDate
		row := map[string]any{
			"ActivationId":       a.ActivationId,
			"IamRole":            a.IamRole,
			"RegistrationLimit":  a.RegistrationLimit,
			"RegistrationsCount": a.RegistrationsCount,
			"ExpirationDate":     a.ExpirationDate,
			"Expired":            expired,
			"CreatedDate":        a.CreatedDate,
		}
		if a.Description != "" {
			row["Description"] = a.Description
		}
		if a.DefaultInstanceName != "" {
			row["DefaultInstanceName"] = a.DefaultInstanceName
		}
		if len(a.Tags) > 0 {
			tags := make([]map[string]any, 0, len(a.Tags))
			for _, t := range a.Tags {
				tags = append(tags, map[string]any{"Key": t.Key, "Value": t.Value})
			}
			row["Tags"] = tags
		}
		out = append(out, row)
	}
	resp := map[string]any{"ActivationList": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

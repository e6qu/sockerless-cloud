package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// AWS Budgets actions: the operations a budget takes when a threshold is
// crossed, and the operator-driven execution of them.
//
// An action's definition is applied through this simulator's own services —
// an IAM definition attaches the policy through the same stores
// AttachRolePolicy/AttachUserPolicy/AttachGroupPolicy write, an SCP definition
// attaches the organizations policy the way AttachPolicy does, and an SSM
// definition stops the named EC2 instances through the same halt the
// StopInstances handler uses. Nothing is marked EXECUTION_SUCCESS without the
// simulator having performed the operation the definition describes.

type budgetsAction struct {
	ActionId         string               `json:"actionId"`
	BudgetName       string               `json:"budgetName"`
	NotificationType string               `json:"notificationType"`
	ActionType       string               `json:"actionType"`
	ActionThreshold  map[string]any       `json:"actionThreshold"`
	Definition       budgetsActionDef     `json:"definition"`
	ExecutionRoleArn string               `json:"executionRoleArn"`
	ApprovalModel    string               `json:"approvalModel"`
	Status           string               `json:"status"`
	Subscribers      []budgetsSubscriber  `json:"subscribers"`
	History          []budgetsActionEvent `json:"history"`
}

type budgetsActionDef struct {
	IamActionDefinition *struct {
		PolicyArn string   `json:"PolicyArn"`
		Roles     []string `json:"Roles"`
		Groups    []string `json:"Groups"`
		Users     []string `json:"Users"`
	} `json:"IamActionDefinition,omitempty"`
	ScpActionDefinition *struct {
		PolicyId  string   `json:"PolicyId"`
		TargetIds []string `json:"TargetIds"`
	} `json:"ScpActionDefinition,omitempty"`
	SsmActionDefinition *struct {
		ActionSubType string   `json:"ActionSubType"`
		Region        string   `json:"Region"`
		InstanceIds   []string `json:"InstanceIds"`
	} `json:"SsmActionDefinition,omitempty"`
}

// budgetsActionEvent is one row of an action's history.
type budgetsActionEvent struct {
	Timestamp   float64 `json:"timestamp"`
	Status      string  `json:"status"`
	EventType   string  `json:"eventType"`
	Description string  `json:"description"`
}

var budgetsActions sim.Store[budgetsAction]

func budgetsActionKey(budgetName, actionID string) string { return budgetName + "/" + actionID }

func registerBudgetsActions(r *AWSRouter, srv *sim.Server) {
	budgetsActions = sim.MakeStore[budgetsAction](srv.DB(), "budget_actions")
	r.Register("AWSBudgetServiceGateway.CreateBudgetAction", handleBudgetsCreateBudgetAction)
	r.Register("AWSBudgetServiceGateway.DescribeBudgetAction", handleBudgetsDescribeBudgetAction)
	r.Register("AWSBudgetServiceGateway.DescribeBudgetActionsForBudget", handleBudgetsDescribeBudgetActionsForBudget)
	r.Register("AWSBudgetServiceGateway.DescribeBudgetActionsForAccount", handleBudgetsDescribeBudgetActionsForAccount)
	r.Register("AWSBudgetServiceGateway.DescribeBudgetActionHistories", handleBudgetsDescribeBudgetActionHistories)
	r.Register("AWSBudgetServiceGateway.UpdateBudgetAction", handleBudgetsUpdateBudgetAction)
	r.Register("AWSBudgetServiceGateway.DeleteBudgetAction", handleBudgetsDeleteBudgetAction)
	r.Register("AWSBudgetServiceGateway.ExecuteBudgetAction", handleBudgetsExecuteBudgetAction)
}

func budgetsActionJSON(action budgetsAction) map[string]any {
	return map[string]any{
		"ActionId":         action.ActionId,
		"BudgetName":       action.BudgetName,
		"NotificationType": action.NotificationType,
		"ActionType":       action.ActionType,
		"ActionThreshold":  action.ActionThreshold,
		"Definition":       action.Definition,
		"ExecutionRoleArn": action.ExecutionRoleArn,
		"ApprovalModel":    action.ApprovalModel,
		"Status":           action.Status,
		"Subscribers":      action.Subscribers,
	}
}

func budgetsRecordActionEvent(action *budgetsAction, eventType, description string) {
	action.History = append(action.History, budgetsActionEvent{
		Timestamp:   float64(time.Now().Unix()),
		Status:      action.Status,
		EventType:   eventType,
		Description: description,
	})
}

func handleBudgetsCreateBudgetAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName       string
		NotificationType string
		ActionType       string
		ActionThreshold  map[string]any
		Definition       budgetsActionDef
		ExecutionRoleArn string
		ApprovalModel    string
		Subscribers      []budgetsSubscriber
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if _, ok := budgetsMustGetEntry(w, req.BudgetName); !ok {
		return
	}
	if req.Definition.IamActionDefinition == nil &&
		req.Definition.ScpActionDefinition == nil &&
		req.Definition.SsmActionDefinition == nil {
		budgetsError(w, "InvalidParameterException",
			"Definition must carry an IAM, SCP or SSM action definition", http.StatusBadRequest)
		return
	}
	action := budgetsAction{
		ActionId:         generateUUID(),
		BudgetName:       req.BudgetName,
		NotificationType: req.NotificationType,
		ActionType:       req.ActionType,
		ActionThreshold:  req.ActionThreshold,
		Definition:       req.Definition,
		ExecutionRoleArn: req.ExecutionRoleArn,
		ApprovalModel:    req.ApprovalModel,
		Status:           "STANDBY",
		Subscribers:      req.Subscribers,
	}
	budgetsRecordActionEvent(&action, "CREATE_ACTION", "the action was created")
	budgetsActions.Put(budgetsActionKey(req.BudgetName, action.ActionId), action)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AccountId":  awsAccountID(),
		"BudgetName": req.BudgetName,
		"ActionId":   action.ActionId,
	})
}

func budgetsMustGetAction(w http.ResponseWriter, budgetName, actionID string) (budgetsAction, bool) {
	action, ok := budgetsActions.Get(budgetsActionKey(budgetName, actionID))
	if !ok {
		budgetsError(w, "NotFoundException", "Unable to find action: "+actionID, http.StatusNotFound)
		return budgetsAction{}, false
	}
	return action, true
}

func handleBudgetsDescribeBudgetAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName string
		ActionId   string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	action, ok := budgetsMustGetAction(w, req.BudgetName, req.ActionId)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AccountId":  awsAccountID(),
		"BudgetName": req.BudgetName,
		"Action":     budgetsActionJSON(action),
	})
}

func budgetsActionsSorted(filter func(budgetsAction) bool) []budgetsAction {
	var actions []budgetsAction
	for _, action := range budgetsActions.List() {
		if filter(action) {
			actions = append(actions, action)
		}
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].ActionId < actions[j].ActionId })
	return actions
}

func handleBudgetsDescribeBudgetActionsForBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if _, ok := budgetsMustGetEntry(w, req.BudgetName); !ok {
		return
	}
	actions := budgetsActionsSorted(func(a budgetsAction) bool { return a.BudgetName == req.BudgetName })
	out := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		out = append(out, budgetsActionJSON(action))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Actions": out})
}

func handleBudgetsDescribeBudgetActionsForAccount(w http.ResponseWriter, r *http.Request) {
	var req struct{ budgetsRequestEnvelope }
	if !budgetsRead(w, r, &req) {
		return
	}
	actions := budgetsActionsSorted(func(budgetsAction) bool { return true })
	out := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		out = append(out, budgetsActionJSON(action))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Actions": out})
}

func handleBudgetsDescribeBudgetActionHistories(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName string
		ActionId   string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	action, ok := budgetsMustGetAction(w, req.BudgetName, req.ActionId)
	if !ok {
		return
	}
	histories := make([]map[string]any, 0, len(action.History))
	for _, event := range action.History {
		histories = append(histories, map[string]any{
			"Timestamp": event.Timestamp,
			"Status":    event.Status,
			"EventType": event.EventType,
			"ActionHistoryDetails": map[string]any{
				"Message": event.Description,
				"Action":  budgetsActionJSON(action),
			},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ActionHistories": histories})
}

func handleBudgetsUpdateBudgetAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName       string
		ActionId         string
		NotificationType string
		ActionThreshold  map[string]any
		Definition       *budgetsActionDef
		ExecutionRoleArn string
		ApprovalModel    string
		Subscribers      []budgetsSubscriber
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	action, ok := budgetsMustGetAction(w, req.BudgetName, req.ActionId)
	if !ok {
		return
	}
	old := budgetsActionJSON(action)
	if req.NotificationType != "" {
		action.NotificationType = req.NotificationType
	}
	if req.ActionThreshold != nil {
		action.ActionThreshold = req.ActionThreshold
	}
	if req.Definition != nil {
		action.Definition = *req.Definition
	}
	if req.ExecutionRoleArn != "" {
		action.ExecutionRoleArn = req.ExecutionRoleArn
	}
	if req.ApprovalModel != "" {
		action.ApprovalModel = req.ApprovalModel
	}
	if req.Subscribers != nil {
		action.Subscribers = req.Subscribers
	}
	budgetsRecordActionEvent(&action, "UPDATE_ACTION", "the action was updated")
	budgetsActions.Put(budgetsActionKey(req.BudgetName, req.ActionId), action)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AccountId":  awsAccountID(),
		"BudgetName": req.BudgetName,
		"OldAction":  old,
		"NewAction":  budgetsActionJSON(action),
	})
}

func handleBudgetsDeleteBudgetAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName string
		ActionId   string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	action, ok := budgetsMustGetAction(w, req.BudgetName, req.ActionId)
	if !ok {
		return
	}
	budgetsActions.Delete(budgetsActionKey(req.BudgetName, req.ActionId))
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AccountId":  awsAccountID(),
		"BudgetName": req.BudgetName,
		"Action":     budgetsActionJSON(action),
	})
}

func handleBudgetsExecuteBudgetAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName    string
		ActionId      string
		ExecutionType string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	action, ok := budgetsMustGetAction(w, req.BudgetName, req.ActionId)
	if !ok {
		return
	}
	switch req.ExecutionType {
	case "APPROVE_BUDGET_ACTION", "RETRY_BUDGET_ACTION":
		if failure := budgetsApplyActionDefinition(r, action.Definition); failure != "" {
			action.Status = "EXECUTION_FAILURE"
			budgetsRecordActionEvent(&action, "EXECUTE_ACTION", failure)
		} else {
			action.Status = "EXECUTION_SUCCESS"
			budgetsRecordActionEvent(&action, "EXECUTE_ACTION", "the definition was applied")
		}
	case "REVERSE_BUDGET_ACTION":
		if failure := budgetsReverseActionDefinition(action.Definition); failure != "" {
			action.Status = "REVERSE_FAILURE"
			budgetsRecordActionEvent(&action, "EXECUTE_ACTION", failure)
		} else {
			action.Status = "REVERSE_SUCCESS"
			budgetsRecordActionEvent(&action, "EXECUTE_ACTION", "the definition was reversed")
		}
	case "RESET_BUDGET_ACTION":
		action.Status = "STANDBY"
		budgetsRecordActionEvent(&action, "EXECUTE_ACTION", "the action was reset to standby")
	default:
		budgetsError(w, "InvalidParameterException",
			"ExecutionType must be APPROVE_BUDGET_ACTION, RETRY_BUDGET_ACTION, REVERSE_BUDGET_ACTION or RESET_BUDGET_ACTION",
			http.StatusBadRequest)
		return
	}
	budgetsActions.Put(budgetsActionKey(req.BudgetName, req.ActionId), action)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AccountId":     awsAccountID(),
		"BudgetName":    req.BudgetName,
		"ActionId":      req.ActionId,
		"ExecutionType": req.ExecutionType,
	})
}

// budgetsApplyActionDefinition performs the operation the definition
// describes, through the simulator's own services, and returns a failure
// description when it cannot. An empty return means the operation happened.
func budgetsApplyActionDefinition(r *http.Request, definition budgetsActionDef) string {
	switch {
	case definition.IamActionDefinition != nil:
		def := definition.IamActionDefinition
		for _, roleName := range def.Roles {
			if _, ok := iamRoles.Get(roleName); !ok {
				return "IAM role not found: " + roleName
			}
			iamAttachedPolicies.Put(roleName+"/"+def.PolicyArn, IAMAttachedPolicy{
				RoleName:   roleName,
				PolicyArn:  def.PolicyArn,
				PolicyName: budgetsPolicyNameFromARN(def.PolicyArn),
			})
		}
		for _, userName := range def.Users {
			if _, ok := iamUsers.Get(userName); !ok {
				return "IAM user not found: " + userName
			}
			iamUserAttached.Put(userName+"/"+def.PolicyArn, IAMUserAttachedPolicy{
				UserName:   userName,
				PolicyArn:  def.PolicyArn,
				PolicyName: budgetsPolicyNameFromARN(def.PolicyArn),
			})
		}
		for _, groupName := range def.Groups {
			if _, ok := iamGroups.Get(groupName); !ok {
				return "IAM group not found: " + groupName
			}
			iamGroupAttached.Put(groupName+"/"+def.PolicyArn, IAMGroupAttachedPolicy{
				GroupName:  groupName,
				PolicyArn:  def.PolicyArn,
				PolicyName: budgetsPolicyNameFromARN(def.PolicyArn),
			})
		}
		return ""
	case definition.ScpActionDefinition != nil:
		def := definition.ScpActionDefinition
		if _, ok := orgPolicies.Get(def.PolicyId); !ok {
			return "organizations policy not found: " + def.PolicyId
		}
		for _, targetID := range def.TargetIds {
			orgPolicyAttachments.Put(def.PolicyId+"/"+targetID, OrgPolicyAttachment{
				PolicyId: def.PolicyId,
				TargetId: targetID,
			})
		}
		return ""
	case definition.SsmActionDefinition != nil:
		def := definition.SsmActionDefinition
		if !strings.EqualFold(def.ActionSubType, "STOP_EC2_INSTANCES") {
			// STOP_RDS_INSTANCES names the one Amazon RDS control this
			// simulator's budgets cannot reach: its RDS slice runs data
			// planes, not instance lifecycles this action could halt.
			return "this simulator applies STOP_EC2_INSTANCES; " + def.ActionSubType + " is not implemented"
		}
		for _, instanceID := range def.InstanceIds {
			if err := ec2HaltInstance(r.Context(), instanceID, "stopped"); err != nil {
				return "stopping EC2 instance " + instanceID + ": " + err.Error()
			}
		}
		return ""
	}
	return "the action carries no definition"
}

// budgetsReverseActionDefinition undoes what an apply did: attachments are
// removed, and stopped instances are left stopped, which is what real AWS
// Budgets does — reversing an SSM action does not restart instances.
func budgetsReverseActionDefinition(definition budgetsActionDef) string {
	switch {
	case definition.IamActionDefinition != nil:
		def := definition.IamActionDefinition
		for _, roleName := range def.Roles {
			iamAttachedPolicies.Delete(roleName + "/" + def.PolicyArn)
		}
		for _, userName := range def.Users {
			iamUserAttached.Delete(userName + "/" + def.PolicyArn)
		}
		for _, groupName := range def.Groups {
			iamGroupAttached.Delete(groupName + "/" + def.PolicyArn)
		}
		return ""
	case definition.ScpActionDefinition != nil:
		def := definition.ScpActionDefinition
		for _, targetID := range def.TargetIds {
			orgPolicyAttachments.Delete(def.PolicyId + "/" + targetID)
		}
		return ""
	case definition.SsmActionDefinition != nil:
		return ""
	}
	return "the action carries no definition"
}

func budgetsPolicyNameFromARN(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

func registerBudgetsNotificationExtras(r *AWSRouter) {
	r.Register("AWSBudgetServiceGateway.UpdateNotification", handleBudgetsUpdateNotification)
	r.Register("AWSBudgetServiceGateway.UpdateSubscriber", handleBudgetsUpdateSubscriber)
	r.Register("AWSBudgetServiceGateway.DescribeBudgetNotificationsForAccount", handleBudgetsDescribeBudgetNotificationsForAccount)
	r.Register("AWSBudgetServiceGateway.DescribeBudgetPerformanceHistory", handleBudgetsDescribeBudgetPerformanceHistory)
}

// handleBudgetsUpdateNotification replaces one notification with another. The
// notification's identity is its own field tuple, so the subscribers recorded
// under the old tuple move to the new one — dropping them would orphan every
// subscriber each time a threshold is tuned.
func handleBudgetsUpdateNotification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName      string
		OldNotification *budgetsNotification
		NewNotification *budgetsNotification
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.OldNotification == nil || req.NewNotification == nil {
		budgetsError(w, "InvalidParameterException",
			"OldNotification and NewNotification are required", http.StatusBadRequest)
		return
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	oldKey := budgetsNotificationKey(*req.OldNotification)
	replaced := false
	for i, notification := range entry.Notifications {
		if budgetsNotificationKey(notification) != oldKey {
			continue
		}
		entry.Notifications[i] = *req.NewNotification
		replaced = true
	}
	if !replaced {
		budgetsError(w, "NotFoundException", "Unable to find notification", http.StatusNotFound)
		return
	}
	if entry.Subscribers != nil {
		newKey := budgetsNotificationKey(*req.NewNotification)
		if subscribers, held := entry.Subscribers[oldKey]; held && newKey != oldKey {
			entry.Subscribers[newKey] = subscribers
			delete(entry.Subscribers, oldKey)
		}
	}
	budgetsStore.Put(req.BudgetName, entry)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleBudgetsUpdateSubscriber replaces one subscriber of a notification.
func handleBudgetsUpdateSubscriber(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName    string
		Notification  *budgetsNotification
		OldSubscriber *budgetsSubscriber
		NewSubscriber *budgetsSubscriber
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.Notification == nil || req.OldSubscriber == nil || req.NewSubscriber == nil {
		budgetsError(w, "InvalidParameterException",
			"Notification, OldSubscriber and NewSubscriber are required", http.StatusBadRequest)
		return
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	key := budgetsNotificationKey(*req.Notification)
	subscribers := entry.Subscribers[key]
	replaced := false
	for i, subscriber := range subscribers {
		if subscriber == *req.OldSubscriber {
			subscribers[i] = *req.NewSubscriber
			replaced = true
		}
	}
	if !replaced {
		budgetsError(w, "NotFoundException", "Unable to find subscriber", http.StatusNotFound)
		return
	}
	entry.Subscribers[key] = subscribers
	budgetsStore.Put(req.BudgetName, entry)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleBudgetsDescribeBudgetNotificationsForAccount lists every budget's
// notifications, keyed by the budget that carries them.
func handleBudgetsDescribeBudgetNotificationsForAccount(w http.ResponseWriter, r *http.Request) {
	var req struct{ budgetsRequestEnvelope }
	if !budgetsRead(w, r, &req) {
		return
	}
	names := make([]string, 0)
	entries := map[string]budgetsStoreEntry{}
	for _, entry := range budgetsStore.List() {
		names = append(names, entry.Budget.BudgetName)
		entries[entry.Budget.BudgetName] = entry
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		notifications := make([]map[string]any, 0)
		for _, notification := range entries[name].Notifications {
			// ThresholdType and NotificationState are optional and have no
			// default, so a notification that carries neither is answered
			// without them. Sending "" put a value in a field whose enum does
			// not declare one — the absence is the honest answer.
			entry := map[string]any{
				"NotificationType":   notification.NotificationType,
				"ComparisonOperator": notification.ComparisonOperator,
				"Threshold":          notification.Threshold,
			}
			if notification.ThresholdType != "" {
				entry["ThresholdType"] = notification.ThresholdType
			}
			if notification.NotificationState != "" {
				entry["NotificationState"] = notification.NotificationState
			}
			notifications = append(notifications, entry)
		}
		out = append(out, map[string]any{
			"BudgetName":    name,
			"Notifications": notifications,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"BudgetNotificationsForAccount": out})
}

// handleBudgetsDescribeBudgetPerformanceHistory reports the budget's limit
// against its actual spend. This simulator accrues no cost for the resources
// it runs, so the actual amount genuinely is zero — the simulator's own truth,
// not a placeholder — and the history carries the budgeted amount beside it
// for the budget's own time unit.
func handleBudgetsDescribeBudgetPerformanceHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	budget := entry.Budget
	history := map[string]any{
		"BudgetName": budget.BudgetName,
		"BudgetType": budget.BudgetType,
		"TimeUnit":   budget.TimeUnit,
	}
	if budget.BudgetLimit != nil {
		history["BudgetedAndActualAmountsList"] = []map[string]any{{
			"BudgetedAmount": budget.BudgetLimit,
			"ActualAmount": map[string]any{
				"Amount": "0",
				"Unit":   budget.BudgetLimit.Unit,
			},
		}}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"BudgetPerformanceHistory": history})
}

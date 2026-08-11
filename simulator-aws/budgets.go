package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Budgets shapes. AWS Budgets speaks awsJson1.1 over POST / with the
// X-Amz-Target header prefix AWSBudgetServiceGateway. Field names mirror
// the Budgets Smithy model verbatim.
type budgetsSpend struct {
	Amount string
	Unit   string
}

type budgetsCostTypes struct {
	IncludeCredit            *bool
	IncludeDiscount          *bool
	IncludeOtherSubscription *bool
	IncludeRecurring         *bool
	IncludeRefund            *bool
	IncludeSubscription      *bool
	IncludeSupport           *bool
	IncludeTax               *bool
	IncludeUpfront           *bool
	UseAmortized             *bool
	UseBlended               *bool
}

type budgetsTimePeriod struct {
	Start *float64
	End   *float64
}

type budgetsBudget struct {
	BudgetName          string
	BudgetLimit         *budgetsSpend
	PlannedBudgetLimits map[string]budgetsSpend
	TimeUnit            string
	TimePeriod          *budgetsTimePeriod
	BudgetType          string
	CostTypes           *budgetsCostTypes
	CostFilters         map[string][]string
	LastUpdatedTime     *float64
}

type budgetsNotification struct {
	ComparisonOperator string
	NotificationType   string
	Threshold          float64
	ThresholdType      string
	NotificationState  string
}

type budgetsSubscriber struct {
	SubscriptionType string
	Address          string
}

type budgetsStoreEntry struct {
	Budget        budgetsBudget
	Notifications []budgetsNotification
	Subscribers   map[string][]budgetsSubscriber
}

var budgetsStore sim.Store[budgetsStoreEntry]

func registerBudgets(r *sim.AWSRouter, srv *sim.Server) {
	budgetsStore = sim.MakeStore[budgetsStoreEntry](srv.DB(), "budgets")
	budgetsTags = sim.MakeStore[map[string]string](srv.DB(), "budget_tags")

	r.Register("AWSBudgetServiceGateway.CreateBudget", handleBudgetsCreateBudget)
	r.Register("AWSBudgetServiceGateway.DescribeBudget", handleBudgetsDescribeBudget)
	r.Register("AWSBudgetServiceGateway.DeleteBudget", handleBudgetsDeleteBudget)
	r.Register("AWSBudgetServiceGateway.UpdateBudget", handleBudgetsUpdateBudget)
	r.Register("AWSBudgetServiceGateway.DescribeBudgets", handleBudgetsDescribeBudgets)
	r.Register("AWSBudgetServiceGateway.CreateNotification", handleBudgetsCreateNotification)
	r.Register("AWSBudgetServiceGateway.DescribeNotificationsForBudget", handleBudgetsDescribeNotificationsForBudget)
	r.Register("AWSBudgetServiceGateway.DeleteNotification", handleBudgetsDeleteNotification)
	r.Register("AWSBudgetServiceGateway.CreateSubscriber", handleBudgetsCreateSubscriber)
	r.Register("AWSBudgetServiceGateway.DescribeSubscribersForNotification", handleBudgetsDescribeSubscribersForNotification)
	r.Register("AWSBudgetServiceGateway.DeleteSubscriber", handleBudgetsDeleteSubscriber)
	r.Register("AWSBudgetServiceGateway.ListTagsForResource", handleBudgetsListTagsForResource)
	r.Register("AWSBudgetServiceGateway.TagResource", handleBudgetsTagResource)
	r.Register("AWSBudgetServiceGateway.UntagResource", handleBudgetsUntagResource)
}

func budgetsNowEpoch() float64 {
	return float64(time.Now().UTC().Unix())
}

func budgetsError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("X-Amzn-Errortype", code)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": code, "message": message})
}

func budgetsRead(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		budgetsError(w, "InvalidParameterException", "invalid JSON request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

type budgetsRequestEnvelope struct {
	AccountId string
}

func budgetsMustGetEntry(w http.ResponseWriter, name string) (budgetsStoreEntry, bool) {
	entry, ok := budgetsStore.Get(name)
	if !ok {
		budgetsError(w, "NotFoundException", "Unable to find budget: "+name, http.StatusNotFound)
		return budgetsStoreEntry{}, false
	}
	return entry, true
}

func budgetsBudgetToWire(b budgetsBudget) map[string]any {
	out := map[string]any{
		"BudgetName": b.BudgetName,
		"BudgetType": b.BudgetType,
		"TimeUnit":   b.TimeUnit,
	}
	if b.BudgetLimit != nil {
		out["BudgetLimit"] = map[string]any{
			"Amount": b.BudgetLimit.Amount,
			"Unit":   b.BudgetLimit.Unit,
		}
	}
	if b.TimePeriod != nil {
		tp := map[string]any{}
		if b.TimePeriod.Start != nil {
			tp["Start"] = *b.TimePeriod.Start
		}
		if b.TimePeriod.End != nil {
			tp["End"] = *b.TimePeriod.End
		}
		out["TimePeriod"] = tp
	}
	if b.CostTypes != nil {
		out["CostTypes"] = budgetsCostTypesToWire(b.CostTypes)
	}
	if len(b.CostFilters) > 0 {
		out["CostFilters"] = b.CostFilters
	}
	if b.LastUpdatedTime != nil {
		out["LastUpdatedTime"] = *b.LastUpdatedTime
	}
	if len(b.PlannedBudgetLimits) > 0 {
		wire := map[string]any{}
		for k, v := range b.PlannedBudgetLimits {
			wire[k] = map[string]any{"Amount": v.Amount, "Unit": v.Unit}
		}
		out["PlannedBudgetLimits"] = wire
	}
	return out
}

func budgetsCostTypesToWire(c *budgetsCostTypes) map[string]any {
	if c == nil {
		return nil
	}
	out := map[string]any{}
	if c.IncludeCredit != nil {
		out["IncludeCredit"] = *c.IncludeCredit
	}
	if c.IncludeDiscount != nil {
		out["IncludeDiscount"] = *c.IncludeDiscount
	}
	if c.IncludeOtherSubscription != nil {
		out["IncludeOtherSubscription"] = *c.IncludeOtherSubscription
	}
	if c.IncludeRecurring != nil {
		out["IncludeRecurring"] = *c.IncludeRecurring
	}
	if c.IncludeRefund != nil {
		out["IncludeRefund"] = *c.IncludeRefund
	}
	if c.IncludeSubscription != nil {
		out["IncludeSubscription"] = *c.IncludeSubscription
	}
	if c.IncludeSupport != nil {
		out["IncludeSupport"] = *c.IncludeSupport
	}
	if c.IncludeTax != nil {
		out["IncludeTax"] = *c.IncludeTax
	}
	if c.IncludeUpfront != nil {
		out["IncludeUpfront"] = *c.IncludeUpfront
	}
	if c.UseAmortized != nil {
		out["UseAmortized"] = *c.UseAmortized
	}
	if c.UseBlended != nil {
		out["UseBlended"] = *c.UseBlended
	}
	return out
}

func budgetsNotificationToWire(n budgetsNotification) map[string]any {
	out := map[string]any{
		"ComparisonOperator": n.ComparisonOperator,
		"NotificationType":   n.NotificationType,
		"Threshold":          n.Threshold,
	}
	if n.ThresholdType != "" {
		out["ThresholdType"] = n.ThresholdType
	}
	if n.NotificationState != "" {
		out["NotificationState"] = n.NotificationState
	}
	return out
}

func budgetsSubscriberToWire(s budgetsSubscriber) map[string]any {
	return map[string]any{
		"SubscriptionType": s.SubscriptionType,
		"Address":          s.Address,
	}
}

// budgetsNotificationKey produces the stable string key under which a
// notification's subscribers are stored. The Budgets data model treats a
// notification as identified by its full shape (type, operator, threshold,
// threshold type), not by any server-assigned id.
func budgetsNotificationKey(n budgetsNotification) string {
	return strings.Join([]string{
		n.NotificationType,
		n.ComparisonOperator,
		strconv.FormatFloat(n.Threshold, 'f', -1, 64),
		n.ThresholdType,
	}, "|")
}

func handleBudgetsCreateBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		Budget       *budgetsBudget
		ResourceTags []struct {
			Key   string
			Value string
		}
		NotificationsWithSubscribers []struct {
			Notification *budgetsNotification
			Subscribers  []budgetsSubscriber
		}
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		// Real AWS derives the AccountId from the caller's signing credentials
		// when the request omits it. terraform-provider-aws with
		// skip_requesting_account_id = true relies on this behavior.
		req.AccountId = awsAccountID()
	}
	if req.Budget == nil || req.Budget.BudgetName == "" {
		budgetsError(w, "InvalidParameterException", "Budget.BudgetName is required", http.StatusBadRequest)
		return
	}
	if req.Budget.BudgetType == "" || req.Budget.TimeUnit == "" {
		budgetsError(w, "InvalidParameterException", "Budget.BudgetType and Budget.TimeUnit are required", http.StatusBadRequest)
		return
	}
	if _, exists := budgetsStore.Get(req.Budget.BudgetName); exists {
		budgetsError(w, "DuplicateRecordException", "Budget already exists: "+req.Budget.BudgetName, http.StatusBadRequest)
		return
	}
	now := budgetsNowEpoch()
	req.Budget.LastUpdatedTime = &now
	entry := budgetsStoreEntry{
		Budget:      *req.Budget,
		Subscribers: map[string][]budgetsSubscriber{},
	}
	for _, nws := range req.NotificationsWithSubscribers {
		if nws.Notification == nil {
			continue
		}
		entry.Notifications = append(entry.Notifications, *nws.Notification)
		entry.Subscribers[budgetsNotificationKey(*nws.Notification)] = append(
			entry.Subscribers[budgetsNotificationKey(*nws.Notification)], nws.Subscribers...)
	}
	budgetsStore.Put(req.Budget.BudgetName, entry)
	budgetTagsSet(req.Budget.BudgetName, req.ResourceTags)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleBudgetsDescribeBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"Budget": budgetsBudgetToWire(entry.Budget)})
}

func handleBudgetsDeleteBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	if _, ok := budgetsMustGetEntry(w, req.BudgetName); !ok {
		return
	}
	budgetsStore.Delete(req.BudgetName)
	budgetTagsDelete(req.BudgetName)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleBudgetsUpdateBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		NewBudget *budgetsBudget
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	if req.NewBudget == nil || req.NewBudget.BudgetName == "" {
		budgetsError(w, "InvalidParameterException", "NewBudget.BudgetName is required", http.StatusBadRequest)
		return
	}
	entry, ok := budgetsMustGetEntry(w, req.NewBudget.BudgetName)
	if !ok {
		return
	}
	updated := *req.NewBudget
	if updated.BudgetType == "" {
		updated.BudgetType = entry.Budget.BudgetType
	}
	if updated.TimeUnit == "" {
		updated.TimeUnit = entry.Budget.TimeUnit
	}
	if updated.BudgetLimit == nil {
		updated.BudgetLimit = entry.Budget.BudgetLimit
	}
	now := budgetsNowEpoch()
	updated.LastUpdatedTime = &now
	entry.Budget = updated
	budgetsStore.Put(req.NewBudget.BudgetName, entry)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleBudgetsDescribeBudgets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		MaxResults int32
		NextToken  string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	all := budgetsStore.List()
	total := len(all)
	pageSize := int(req.MaxResults)
	if pageSize <= 0 {
		pageSize = 100
	}
	offset := 0
	if req.NextToken != "" {
		n, err := strconv.Atoi(req.NextToken)
		if err != nil || n < 0 || n > total {
			budgetsError(w, "InvalidNextTokenException", "Invalid NextToken", http.StatusBadRequest)
			return
		}
		offset = n
	}
	end := offset + pageSize
	if end > total {
		end = total
	}
	page := all[offset:end]
	budgets := make([]map[string]any, 0, len(page))
	for _, e := range page {
		budgets = append(budgets, budgetsBudgetToWire(e.Budget))
	}
	resp := map[string]any{"Budgets": budgets}
	if end < total {
		resp["NextToken"] = strconv.Itoa(end)
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleBudgetsCreateNotification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName   string
		Notification *budgetsNotification
		Subscribers  []budgetsSubscriber
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	if req.Notification == nil {
		budgetsError(w, "InvalidParameterException", "Notification is required", http.StatusBadRequest)
		return
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	key := budgetsNotificationKey(*req.Notification)
	for _, existing := range entry.Notifications {
		if budgetsNotificationKey(existing) == key {
			budgetsError(w, "DuplicateRecordException", "Notification already exists for budget", http.StatusBadRequest)
			return
		}
	}
	entry.Notifications = append(entry.Notifications, *req.Notification)
	entry.Subscribers[key] = append(entry.Subscribers[key], req.Subscribers...)
	budgetsStore.Put(req.BudgetName, entry)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleBudgetsDescribeNotificationsForBudget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName string
		MaxResults int32
		NextToken  string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	all := entry.Notifications
	total := len(all)
	pageSize := int(req.MaxResults)
	if pageSize <= 0 {
		pageSize = 100
	}
	offset := 0
	if req.NextToken != "" {
		n, err := strconv.Atoi(req.NextToken)
		if err != nil || n < 0 || n > total {
			budgetsError(w, "InvalidNextTokenException", "Invalid NextToken", http.StatusBadRequest)
			return
		}
		offset = n
	}
	end := offset + pageSize
	if end > total {
		end = total
	}
	page := all[offset:end]
	notifs := make([]map[string]any, 0, len(page))
	for _, n := range page {
		notifs = append(notifs, budgetsNotificationToWire(n))
	}
	resp := map[string]any{"Notifications": notifs}
	if end < total {
		resp["NextToken"] = strconv.Itoa(end)
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleBudgetsDeleteNotification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName   string
		Notification *budgetsNotification
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	if req.Notification == nil {
		budgetsError(w, "InvalidParameterException", "Notification is required", http.StatusBadRequest)
		return
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	key := budgetsNotificationKey(*req.Notification)
	found := false
	kept := entry.Notifications[:0]
	for _, n := range entry.Notifications {
		if budgetsNotificationKey(n) == key {
			found = true
			continue
		}
		kept = append(kept, n)
	}
	if !found {
		budgetsError(w, "NotFoundException", "Notification not found for budget", http.StatusNotFound)
		return
	}
	entry.Notifications = kept
	delete(entry.Subscribers, key)
	budgetsStore.Put(req.BudgetName, entry)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleBudgetsCreateSubscriber(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName   string
		Notification *budgetsNotification
		Subscriber   *budgetsSubscriber
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	if req.Subscriber == nil || req.Notification == nil {
		budgetsError(w, "InvalidParameterException", "Notification and Subscriber are required", http.StatusBadRequest)
		return
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	key := budgetsNotificationKey(*req.Notification)
	hasNotif := false
	for _, n := range entry.Notifications {
		if budgetsNotificationKey(n) == key {
			hasNotif = true
			break
		}
	}
	if !hasNotif {
		budgetsError(w, "NotFoundException", "Notification not found for budget", http.StatusNotFound)
		return
	}
	for _, existing := range entry.Subscribers[key] {
		if existing.SubscriptionType == req.Subscriber.SubscriptionType && existing.Address == req.Subscriber.Address {
			budgetsError(w, "DuplicateRecordException", "Subscriber already exists", http.StatusBadRequest)
			return
		}
	}
	entry.Subscribers[key] = append(entry.Subscribers[key], *req.Subscriber)
	budgetsStore.Put(req.BudgetName, entry)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleBudgetsDescribeSubscribersForNotification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName   string
		Notification *budgetsNotification
		MaxResults   int32
		NextToken    string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	if req.Notification == nil {
		budgetsError(w, "InvalidParameterException", "Notification is required", http.StatusBadRequest)
		return
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	key := budgetsNotificationKey(*req.Notification)
	all := entry.Subscribers[key]
	total := len(all)
	pageSize := int(req.MaxResults)
	if pageSize <= 0 {
		pageSize = 100
	}
	offset := 0
	if req.NextToken != "" {
		n, err := strconv.Atoi(req.NextToken)
		if err != nil || n < 0 || n > total {
			budgetsError(w, "InvalidNextTokenException", "Invalid NextToken", http.StatusBadRequest)
			return
		}
		offset = n
	}
	end := offset + pageSize
	if end > total {
		end = total
	}
	page := all[offset:end]
	subs := make([]map[string]any, 0, len(page))
	for _, s := range page {
		subs = append(subs, budgetsSubscriberToWire(s))
	}
	resp := map[string]any{"Subscribers": subs}
	if end < total {
		resp["NextToken"] = strconv.Itoa(end)
	}
	writeAWSJSON(w, http.StatusOK, resp)
}

func handleBudgetsDeleteSubscriber(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		BudgetName   string
		Notification *budgetsNotification
		Subscriber   *budgetsSubscriber
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	if req.AccountId == "" {
		req.AccountId = awsAccountID()
	}
	if req.Notification == nil || req.Subscriber == nil {
		budgetsError(w, "InvalidParameterException", "Notification and Subscriber are required", http.StatusBadRequest)
		return
	}
	entry, ok := budgetsMustGetEntry(w, req.BudgetName)
	if !ok {
		return
	}
	key := budgetsNotificationKey(*req.Notification)
	subs := entry.Subscribers[key]
	found := false
	kept := subs[:0]
	for _, s := range subs {
		if s.SubscriptionType == req.Subscriber.SubscriptionType && s.Address == req.Subscriber.Address {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		budgetsError(w, "NotFoundException", "Subscriber not found", http.StatusNotFound)
		return
	}
	entry.Subscribers[key] = kept
	budgetsStore.Put(req.BudgetName, entry)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleBudgetsListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		ResourceARN string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	name := budgetsNameFromARN(req.ResourceARN)
	entry, ok := budgetsMustGetEntry(w, name)
	if !ok {
		return
	}
	writeAWSJSON(w, http.StatusOK, map[string]any{"ResourceTags": budgetsTagsToWire(entry.Budget.BudgetName)})
}

func handleBudgetsTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		ResourceARN  string
		ResourceTags []struct {
			Key   string
			Value string
		}
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	name := budgetsNameFromARN(req.ResourceARN)
	entry, ok := budgetsMustGetEntry(w, name)
	if !ok {
		return
	}
	budgetTagsSet(entry.Budget.BudgetName, req.ResourceTags)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

func handleBudgetsUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		budgetsRequestEnvelope
		ResourceARN     string
		ResourceTagKeys []string
	}
	if !budgetsRead(w, r, &req) {
		return
	}
	name := budgetsNameFromARN(req.ResourceARN)
	entry, ok := budgetsMustGetEntry(w, name)
	if !ok {
		return
	}
	budgetTagsUnset(entry.Budget.BudgetName, req.ResourceTagKeys)
	writeAWSJSON(w, http.StatusOK, map[string]any{})
}

// budgetsNameFromARN extracts the budget name from the ARN the Budgets API
// uses for tagging. The provider builds "arn:aws:budgets::<account>:budget/<name>".
func budgetsNameFromARN(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// budgetsTags is the tag store keyed by budget name.
var budgetsTags sim.Store[map[string]string]

func budgetTagsSet(name string, tags []struct {
	Key   string
	Value string
}) {
	current, _ := budgetsTags.Get(name)
	if current == nil {
		current = map[string]string{}
	}
	for _, t := range tags {
		current[t.Key] = t.Value
	}
	budgetsTags.Put(name, current)
}

func budgetTagsUnset(name string, keys []string) {
	current, ok := budgetsTags.Get(name)
	if !ok {
		return
	}
	for _, k := range keys {
		delete(current, k)
	}
	budgetsTags.Put(name, current)
}

func budgetTagsDelete(name string) {
	budgetsTags.Delete(name)
}

func budgetsTagsToWire(name string) []map[string]any {
	tags, _ := budgetsTags.Get(name)
	out := make([]map[string]any, 0, len(tags))
	for k, v := range tags {
		out = append(out, map[string]any{"Key": k, "Value": v})
	}
	return out
}

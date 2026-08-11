package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	sbAtomSchema       = "http://www.w3.org/2005/Atom"
	sbDataSchema       = "http://schemas.microsoft.com/netservices/2010/10/servicebus/connect"
	sbXMLSchema        = "http://www.w3.org/2001/XMLSchema-instance"
	sbAdminContentType = "application/atom+xml;type=entry;charset=utf-8"
)

type sbAdminProbe struct {
	QueueDescription        *sbAdminQueueDescription        `xml:"content>QueueDescription"`
	TopicDescription        *sbAdminTopicDescription        `xml:"content>TopicDescription"`
	SubscriptionDescription *sbAdminSubscriptionDescription `xml:"content>SubscriptionDescription"`
	RuleDescription         *sbAdminRuleDescription         `xml:"content>RuleDescription"`
}

type sbAdminEntryBase struct {
	XMLName    xml.Name       `xml:"entry"`
	AtomSchema string         `xml:"xmlns,attr,omitempty"`
	ID         string         `xml:"id,omitempty"`
	Title      string         `xml:"title,omitempty"`
	Updated    string         `xml:"updated,omitempty"`
	Author     *sbAdminAuthor `xml:"author,omitempty"`
	Link       *sbAdminLink   `xml:"link,omitempty"`
}

type sbAdminAuthor struct {
	Name string `xml:"name,omitempty"`
}

type sbAdminLink struct {
	Rel  string `xml:"rel,attr"`
	HREF string `xml:"href,attr"`
}

type sbAdminCountDetails struct {
	ActiveMessageCount             *int32 `xml:"ActiveMessageCount,omitempty"`
	DeadLetterMessageCount         *int32 `xml:"DeadLetterMessageCount,omitempty"`
	ScheduledMessageCount          *int32 `xml:"ScheduledMessageCount,omitempty"`
	TransferDeadLetterMessageCount *int32 `xml:"TransferDeadLetterMessageCount,omitempty"`
	TransferMessageCount           *int32 `xml:"TransferMessageCount,omitempty"`
}

type sbAdminQueueEntry struct {
	sbAdminEntryBase
	Content sbAdminQueueContent `xml:"content"`
}

type sbAdminQueueContent struct {
	Type             string                  `xml:"type,attr"`
	QueueDescription sbAdminQueueDescription `xml:"QueueDescription"`
}

type sbAdminQueueDescription struct {
	XMLName                             xml.Name             `xml:"QueueDescription"`
	ServiceBusSchema                    string               `xml:"xmlns,attr,omitempty"`
	InstanceMetadataSchema              string               `xml:"xmlns:i,attr,omitempty"`
	LockDuration                        *string              `xml:"LockDuration,omitempty"`
	MaxSizeInMegabytes                  *int32               `xml:"MaxSizeInMegabytes,omitempty"`
	RequiresDuplicateDetection          *bool                `xml:"RequiresDuplicateDetection,omitempty"`
	RequiresSession                     *bool                `xml:"RequiresSession,omitempty"`
	DefaultMessageTimeToLive            *string              `xml:"DefaultMessageTimeToLive,omitempty"`
	DeadLetteringOnMessageExpiration    *bool                `xml:"DeadLetteringOnMessageExpiration,omitempty"`
	DuplicateDetectionHistoryTimeWindow *string              `xml:"DuplicateDetectionHistoryTimeWindow,omitempty"`
	MaxDeliveryCount                    *int32               `xml:"MaxDeliveryCount,omitempty"`
	EnableBatchedOperations             *bool                `xml:"EnableBatchedOperations,omitempty"`
	SizeInBytes                         *int64               `xml:"SizeInBytes,omitempty"`
	MessageCount                        *int64               `xml:"MessageCount,omitempty"`
	Status                              *string              `xml:"Status,omitempty"`
	AccessedAt                          string               `xml:"AccessedAt,omitempty"`
	CreatedAt                           string               `xml:"CreatedAt,omitempty"`
	UpdatedAt                           string               `xml:"UpdatedAt,omitempty"`
	AutoDeleteOnIdle                    *string              `xml:"AutoDeleteOnIdle,omitempty"`
	EnablePartitioning                  *bool                `xml:"EnablePartitioning,omitempty"`
	EnableExpress                       *bool                `xml:"EnableExpress,omitempty"`
	CountDetails                        *sbAdminCountDetails `xml:"CountDetails,omitempty"`
	ForwardTo                           *string              `xml:"ForwardTo,omitempty"`
	ForwardDeadLetteredMessagesTo       *string              `xml:"ForwardDeadLetteredMessagesTo,omitempty"`
	UserMetadata                        *string              `xml:"UserMetadata,omitempty"`
	MaxMessageSizeInKilobytes           *int64               `xml:"MaxMessageSizeInKilobytes,omitempty"`
}

type sbAdminTopicEntry struct {
	sbAdminEntryBase
	Content sbAdminTopicContent `xml:"content"`
}

type sbAdminTopicContent struct {
	Type             string                  `xml:"type,attr"`
	TopicDescription sbAdminTopicDescription `xml:"TopicDescription"`
}

type sbAdminTopicDescription struct {
	XMLName                             xml.Name             `xml:"TopicDescription"`
	ServiceBusSchema                    string               `xml:"xmlns,attr,omitempty"`
	InstanceMetadataSchema              string               `xml:"xmlns:i,attr,omitempty"`
	DefaultMessageTimeToLive            *string              `xml:"DefaultMessageTimeToLive,omitempty"`
	MaxSizeInMegabytes                  *int32               `xml:"MaxSizeInMegabytes,omitempty"`
	RequiresDuplicateDetection          *bool                `xml:"RequiresDuplicateDetection,omitempty"`
	DuplicateDetectionHistoryTimeWindow *string              `xml:"DuplicateDetectionHistoryTimeWindow,omitempty"`
	EnableBatchedOperations             *bool                `xml:"EnableBatchedOperations,omitempty"`
	SizeInBytes                         *int64               `xml:"SizeInBytes,omitempty"`
	Status                              *string              `xml:"Status,omitempty"`
	UserMetadata                        *string              `xml:"UserMetadata,omitempty"`
	AccessedAt                          string               `xml:"AccessedAt,omitempty"`
	CreatedAt                           string               `xml:"CreatedAt,omitempty"`
	UpdatedAt                           string               `xml:"UpdatedAt,omitempty"`
	SupportOrdering                     *bool                `xml:"SupportOrdering,omitempty"`
	AutoDeleteOnIdle                    *string              `xml:"AutoDeleteOnIdle,omitempty"`
	EnablePartitioning                  *bool                `xml:"EnablePartitioning,omitempty"`
	EnableSubscriptionPartitioning      *bool                `xml:"EnableSubscriptionPartitioning,omitempty"`
	EnableExpress                       *bool                `xml:"EnableExpress,omitempty"`
	CountDetails                        *sbAdminCountDetails `xml:"CountDetails,omitempty"`
	SubscriptionCount                   *int32               `xml:"SubscriptionCount,omitempty"`
	MaxMessageSizeInKilobytes           *int64               `xml:"MaxMessageSizeInKilobytes,omitempty"`
}

type sbAdminSubscriptionEntry struct {
	sbAdminEntryBase
	Content sbAdminSubscriptionContent `xml:"content"`
}

type sbAdminSubscriptionContent struct {
	Type                    string                         `xml:"type,attr"`
	SubscriptionDescription sbAdminSubscriptionDescription `xml:"SubscriptionDescription"`
}

type sbAdminSubscriptionDescription struct {
	XMLName                                   xml.Name             `xml:"SubscriptionDescription"`
	ServiceBusSchema                          string               `xml:"xmlns,attr,omitempty"`
	InstanceMetadataSchema                    string               `xml:"xmlns:i,attr,omitempty"`
	LockDuration                              *string              `xml:"LockDuration,omitempty"`
	RequiresSession                           *bool                `xml:"RequiresSession,omitempty"`
	DefaultMessageTimeToLive                  *string              `xml:"DefaultMessageTimeToLive,omitempty"`
	DeadLetteringOnMessageExpiration          *bool                `xml:"DeadLetteringOnMessageExpiration,omitempty"`
	DeadLetteringOnFilterEvaluationExceptions *bool                `xml:"DeadLetteringOnFilterEvaluationExceptions,omitempty"`
	MaxDeliveryCount                          *int32               `xml:"MaxDeliveryCount,omitempty"`
	MessageCount                              *int64               `xml:"MessageCount,omitempty"`
	EnableBatchedOperations                   *bool                `xml:"EnableBatchedOperations,omitempty"`
	Status                                    *string              `xml:"Status,omitempty"`
	ForwardTo                                 *string              `xml:"ForwardTo,omitempty"`
	UserMetadata                              *string              `xml:"UserMetadata,omitempty"`
	ForwardDeadLetteredMessagesTo             *string              `xml:"ForwardDeadLetteredMessagesTo,omitempty"`
	AutoDeleteOnIdle                          *string              `xml:"AutoDeleteOnIdle,omitempty"`
	CreatedAt                                 string               `xml:"CreatedAt,omitempty"`
	UpdatedAt                                 string               `xml:"UpdatedAt,omitempty"`
	AccessedAt                                string               `xml:"AccessedAt,omitempty"`
	CountDetails                              *sbAdminCountDetails `xml:"CountDetails,omitempty"`
}

type sbAdminRuleEntry struct {
	sbAdminEntryBase
	Content sbAdminRuleContent `xml:"content"`
}

type sbAdminRuleContent struct {
	Type            string                 `xml:"type,attr"`
	RuleDescription sbAdminRuleDescription `xml:"RuleDescription"`
}

type sbAdminRuleDescription struct {
	XMLName          xml.Name           `xml:"RuleDescription"`
	ServiceBusSchema string             `xml:"xmlns,attr,omitempty"`
	XMLSchema        string             `xml:"xmlns:i,attr,omitempty"`
	CreatedAt        string             `xml:"CreatedAt,omitempty"`
	Filter           *sbAdminFilter     `xml:"Filter,omitempty"`
	Action           *sbAdminRuleAction `xml:"Action,omitempty"`
	Name             string             `xml:"Name"`
}

type sbAdminFilter struct {
	Type          string  `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
	SQLExpression *string `xml:"SqlExpression,omitempty"`
}

type sbAdminRuleAction struct {
	Type          string `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
	SQLExpression string `xml:"SqlExpression,omitempty"`
}

type sbAdminQueueFeed struct {
	XMLName    xml.Name            `xml:"feed"`
	AtomSchema string              `xml:"xmlns,attr,omitempty"`
	ID         string              `xml:"id,omitempty"`
	Title      string              `xml:"title,omitempty"`
	Entries    []sbAdminQueueEntry `xml:"entry"`
}

type sbAdminTopicFeed struct {
	XMLName    xml.Name            `xml:"feed"`
	AtomSchema string              `xml:"xmlns,attr,omitempty"`
	ID         string              `xml:"id,omitempty"`
	Title      string              `xml:"title,omitempty"`
	Entries    []sbAdminTopicEntry `xml:"entry"`
}

type sbAdminSubscriptionFeed struct {
	XMLName    xml.Name                   `xml:"feed"`
	AtomSchema string                     `xml:"xmlns,attr,omitempty"`
	ID         string                     `xml:"id,omitempty"`
	Title      string                     `xml:"title,omitempty"`
	Entries    []sbAdminSubscriptionEntry `xml:"entry"`
}

type sbAdminRuleFeed struct {
	XMLName    xml.Name           `xml:"feed"`
	AtomSchema string             `xml:"xmlns,attr,omitempty"`
	ID         string             `xml:"id,omitempty"`
	Title      string             `xml:"title,omitempty"`
	Entries    []sbAdminRuleEntry `xml:"entry"`
}

func handleSBAdminDataPlane(w http.ResponseWriter, r *http.Request, namespace string) bool {
	path := strings.Trim(r.URL.Path, "/")
	segs := splitSBAdminPath(path)
	if len(segs) == 0 {
		return false
	}
	if sbAdminIsMessagePath(segs) {
		return false
	}

	if len(segs) == 2 && segs[0] == "$Resources" {
		switch {
		case r.Method == http.MethodGet && strings.EqualFold(segs[1], "Queues"):
			handleSBAdminListQueues(w, r, namespace)
			return true
		case r.Method == http.MethodGet && strings.EqualFold(segs[1], "Topics"):
			handleSBAdminListTopics(w, r, namespace)
			return true
		}
	}

	if len(segs) == 1 {
		switch r.Method {
		case http.MethodPut:
			handleSBAdminPutEntity(w, r, namespace, segs[0])
			return true
		case http.MethodGet:
			handleSBAdminGetEntity(w, r, namespace, segs[0])
			return true
		case http.MethodDelete:
			handleSBAdminDeleteEntity(w, r, namespace, segs[0])
			return true
		}
	}

	if len(segs) >= 2 && strings.EqualFold(segs[1], "Subscriptions") {
		topic := segs[0]
		if len(segs) == 2 && r.Method == http.MethodGet {
			handleSBAdminListSubscriptions(w, r, namespace, topic)
			return true
		}
		if len(segs) == 3 {
			switch r.Method {
			case http.MethodPut:
				handleSBAdminPutSubscription(w, r, namespace, topic, segs[2])
				return true
			case http.MethodGet:
				handleSBAdminGetSubscription(w, r, namespace, topic, segs[2])
				return true
			case http.MethodDelete:
				handleSBAdminDeleteSubscription(w, r, namespace, topic, segs[2])
				return true
			}
		}
		if len(segs) >= 4 && strings.EqualFold(segs[3], "Rules") {
			sub := segs[2]
			if len(segs) == 4 && r.Method == http.MethodGet {
				handleSBAdminListRules(w, r, namespace, topic, sub)
				return true
			}
			if len(segs) == 5 {
				switch r.Method {
				case http.MethodPut:
					handleSBAdminPutRule(w, r, namespace, topic, sub, segs[4])
					return true
				case http.MethodGet:
					handleSBAdminGetRule(w, r, namespace, topic, sub, segs[4])
					return true
				case http.MethodDelete:
					handleSBAdminDeleteRule(w, r, namespace, topic, sub, segs[4])
					return true
				}
			}
		}
	}

	return false
}

func splitSBAdminPath(path string) []string {
	if path == "" {
		return nil
	}
	raw := strings.Split(path, "/")
	segs := raw[:0]
	for _, seg := range raw {
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	return segs
}

func sbAdminIsMessagePath(segs []string) bool {
	for _, seg := range segs {
		if strings.EqualFold(seg, "messages") {
			return true
		}
	}
	return false
}

func sbAdminNamespaceID(namespace string) string {
	for _, ns := range sbNamespaces.List() {
		if ns.Name == namespace {
			return ns.ID
		}
	}
	return "servicebus://" + namespace
}

func sbAdminQueueID(namespace, queue string) string {
	return sbAdminNamespaceID(namespace) + "/queues/" + queue
}

func sbAdminTopicID(namespace, topic string) string {
	return sbAdminNamespaceID(namespace) + "/topics/" + topic
}

func sbAdminSubscriptionID(namespace, topic, sub string) string {
	return sbAdminTopicID(namespace, topic) + "/subscriptions/" + sub
}

func sbAdminRuleID(namespace, topic, sub, rule string) string {
	return sbAdminSubscriptionID(namespace, topic, sub) + "/rules/" + rule
}

func handleSBAdminPutEntity(w http.ResponseWriter, r *http.Request, namespace, name string) {
	var req sbAdminProbe
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		sbAdminError(w, http.StatusBadRequest, "BadRequest", "invalid ATOM XML body: "+err.Error())
		return
	}
	if req.QueueDescription != nil {
		id := sbAdminQueueID(namespace, name)
		_, existed := sbQueues.Get(id)
		sbQueues.Put(id, sbQueueFromAdminDescription(id, name, req.QueueDescription))
		q, _ := sbQueues.Get(id)
		status := http.StatusCreated
		if existed {
			status = http.StatusOK
		}
		writeSBAdminXML(w, status, sbAdminQueueEntryFor(r, namespace, name, q))
		return
	}
	if req.TopicDescription != nil {
		id := sbAdminTopicID(namespace, name)
		_, existed := sbTopics.Get(id)
		sbTopics.Put(id, sbTopicFromAdminDescription(id, name, req.TopicDescription))
		topic, _ := sbTopics.Get(id)
		status := http.StatusCreated
		if existed {
			status = http.StatusOK
		}
		writeSBAdminXML(w, status, sbAdminTopicEntryFor(r, namespace, name, topic))
		return
	}
	sbAdminError(w, http.StatusBadRequest, "BadRequest", "expected QueueDescription or TopicDescription")
}

func handleSBAdminGetEntity(w http.ResponseWriter, r *http.Request, namespace, name string) {
	if q, ok := sbQueues.Get(sbAdminQueueID(namespace, name)); ok {
		writeSBAdminXML(w, http.StatusOK, sbAdminQueueEntryFor(r, namespace, name, q))
		return
	}
	if t, ok := sbTopics.Get(sbAdminTopicID(namespace, name)); ok {
		writeSBAdminXML(w, http.StatusOK, sbAdminTopicEntryFor(r, namespace, name, t))
		return
	}
	sbAdminError(w, http.StatusNotFound, "404", "The messaging entity could not be found.")
}

func handleSBAdminDeleteEntity(w http.ResponseWriter, r *http.Request, namespace, name string) {
	queueID := sbAdminQueueID(namespace, name)
	if sbQueues.Delete(queueID) {
		sbDropAuthRulesUnder(queueID)
		w.WriteHeader(http.StatusOK)
		return
	}
	topicID := sbAdminTopicID(namespace, name)
	if sbTopics.Delete(topicID) {
		for _, sub := range sbSubscriptions.List() {
			if strings.HasPrefix(sub.ID, topicID+"/subscriptions/") {
				sbSubscriptions.Delete(sub.ID)
			}
		}
		for _, rule := range sbRules.List() {
			if strings.HasPrefix(rule.ID, topicID+"/subscriptions/") {
				sbRules.Delete(rule.ID)
			}
		}
		sbDropAuthRulesUnder(topicID)
		w.WriteHeader(http.StatusOK)
		return
	}
	sbAdminError(w, http.StatusNotFound, "404", "The messaging entity could not be found.")
}

func handleSBAdminListQueues(w http.ResponseWriter, r *http.Request, namespace string) {
	prefix := sbAdminNamespaceID(namespace) + "/queues/"
	queues := sbQueues.Filter(func(q SBQueue) bool {
		return strings.HasPrefix(q.ID, prefix)
	})
	entries := []sbAdminQueueEntry{}
	for _, q := range sbAdminPaged(queues, r) {
		entries = append(entries, sbAdminQueueEntryFor(r, namespace, q.Name, q))
	}
	writeSBAdminXML(w, http.StatusOK, sbAdminQueueFeed{
		AtomSchema: sbAtomSchema,
		ID:         sbAdminURL(r, namespace, "$Resources/Queues"),
		Title:      "Publicly Listed Services",
		Entries:    entries,
	})
}

func handleSBAdminListTopics(w http.ResponseWriter, r *http.Request, namespace string) {
	prefix := sbAdminNamespaceID(namespace) + "/topics/"
	topics := sbTopics.Filter(func(topic SBTopic) bool {
		return strings.HasPrefix(topic.ID, prefix) && !strings.Contains(strings.TrimPrefix(topic.ID, prefix), "/")
	})
	entries := []sbAdminTopicEntry{}
	for _, topic := range sbAdminPaged(topics, r) {
		entries = append(entries, sbAdminTopicEntryFor(r, namespace, topic.Name, topic))
	}
	writeSBAdminXML(w, http.StatusOK, sbAdminTopicFeed{
		AtomSchema: sbAtomSchema,
		ID:         sbAdminURL(r, namespace, "$Resources/Topics"),
		Title:      "Publicly Listed Services",
		Entries:    entries,
	})
}

func handleSBAdminPutSubscription(w http.ResponseWriter, r *http.Request, namespace, topic, sub string) {
	if _, ok := sbTopics.Get(sbAdminTopicID(namespace, topic)); !ok {
		sbAdminError(w, http.StatusNotFound, "404", "Topic not found.")
		return
	}
	var req sbAdminProbe
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		sbAdminError(w, http.StatusBadRequest, "BadRequest", "invalid ATOM XML body: "+err.Error())
		return
	}
	if req.SubscriptionDescription == nil {
		sbAdminError(w, http.StatusBadRequest, "BadRequest", "expected SubscriptionDescription")
		return
	}
	id := sbAdminSubscriptionID(namespace, topic, sub)
	_, existed := sbSubscriptions.Get(id)
	sbSubscriptions.Put(id, sbSubscriptionFromAdminDescription(id, sub, req.SubscriptionDescription))
	stored, _ := sbSubscriptions.Get(id)
	defaultRuleID := sbAdminRuleID(namespace, topic, sub, "$Default")
	if _, ok := sbRules.Get(defaultRuleID); !ok {
		sbRules.Put(defaultRuleID, sbDefaultRule(defaultRuleID))
	}
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeSBAdminXML(w, status, sbAdminSubscriptionEntryFor(r, namespace, topic, sub, stored))
}

func handleSBAdminGetSubscription(w http.ResponseWriter, r *http.Request, namespace, topic, sub string) {
	if s, ok := sbSubscriptions.Get(sbAdminSubscriptionID(namespace, topic, sub)); ok {
		writeSBAdminXML(w, http.StatusOK, sbAdminSubscriptionEntryFor(r, namespace, topic, sub, s))
		return
	}
	sbAdminError(w, http.StatusNotFound, "404", "The messaging entity could not be found.")
}

func handleSBAdminDeleteSubscription(w http.ResponseWriter, r *http.Request, namespace, topic, sub string) {
	id := sbAdminSubscriptionID(namespace, topic, sub)
	if !sbSubscriptions.Delete(id) {
		sbAdminError(w, http.StatusNotFound, "404", "Subscription not found.")
		return
	}
	for _, rule := range sbRules.List() {
		if strings.HasPrefix(rule.ID, id+"/rules/") {
			sbRules.Delete(rule.ID)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func handleSBAdminListSubscriptions(w http.ResponseWriter, r *http.Request, namespace, topic string) {
	prefix := sbAdminTopicID(namespace, topic) + "/subscriptions/"
	subscriptions := sbSubscriptions.Filter(func(sub SBSubscription) bool {
		return strings.HasPrefix(sub.ID, prefix)
	})
	entries := []sbAdminSubscriptionEntry{}
	for _, sub := range sbAdminPaged(subscriptions, r) {
		entries = append(entries, sbAdminSubscriptionEntryFor(r, namespace, topic, sub.Name, sub))
	}
	writeSBAdminXML(w, http.StatusOK, sbAdminSubscriptionFeed{
		AtomSchema: sbAtomSchema,
		ID:         sbAdminURL(r, namespace, topic+"/Subscriptions"),
		Title:      "Publicly Listed Services",
		Entries:    entries,
	})
}

func handleSBAdminPutRule(w http.ResponseWriter, r *http.Request, namespace, topic, sub, rule string) {
	if _, ok := sbSubscriptions.Get(sbAdminSubscriptionID(namespace, topic, sub)); !ok {
		sbAdminError(w, http.StatusNotFound, "404", "Subscription not found.")
		return
	}
	var req sbAdminProbe
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		sbAdminError(w, http.StatusBadRequest, "BadRequest", "invalid ATOM XML body: "+err.Error())
		return
	}
	if req.RuleDescription == nil {
		sbAdminError(w, http.StatusBadRequest, "BadRequest", "expected RuleDescription")
		return
	}
	id := sbAdminRuleID(namespace, topic, sub, rule)
	_, existed := sbRules.Get(id)
	sbRules.Put(id, sbRuleFromAdminDescription(id, rule, req.RuleDescription))
	stored, _ := sbRules.Get(id)
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeSBAdminXML(w, status, sbAdminRuleEntryFor(r, namespace, topic, sub, rule, stored))
}

func handleSBAdminGetRule(w http.ResponseWriter, r *http.Request, namespace, topic, sub, rule string) {
	if stored, ok := sbRules.Get(sbAdminRuleID(namespace, topic, sub, rule)); ok {
		writeSBAdminXML(w, http.StatusOK, sbAdminRuleEntryFor(r, namespace, topic, sub, rule, stored))
		return
	}
	sbAdminError(w, http.StatusNotFound, "404", "The messaging entity could not be found.")
}

func handleSBAdminDeleteRule(w http.ResponseWriter, r *http.Request, namespace, topic, sub, rule string) {
	if !sbRules.Delete(sbAdminRuleID(namespace, topic, sub, rule)) {
		sbAdminError(w, http.StatusNotFound, "404", "Rule not found.")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleSBAdminListRules(w http.ResponseWriter, r *http.Request, namespace, topic, sub string) {
	prefix := sbAdminSubscriptionID(namespace, topic, sub) + "/rules/"
	rules := sbRules.Filter(func(rule SBRule) bool {
		return strings.HasPrefix(rule.ID, prefix)
	})
	entries := []sbAdminRuleEntry{}
	for _, rule := range sbAdminPaged(rules, r) {
		entries = append(entries, sbAdminRuleEntryFor(r, namespace, topic, sub, rule.Name, rule))
	}
	writeSBAdminXML(w, http.StatusOK, sbAdminRuleFeed{
		AtomSchema: sbAtomSchema,
		ID:         sbAdminURL(r, namespace, topic+"/Subscriptions/"+sub+"/Rules"),
		Title:      "Publicly Listed Services",
		Entries:    entries,
	})
}

// sbDefaultQueueProperties returns the server-assigned property defaults
// real Azure stamps on a queue when the client omits them. The ARM create
// handler applies these first, then overlays the client's explicit values.
func sbDefaultQueueProperties() map[string]any {
	return map[string]any{
		"status":                              "Active",
		"maxSizeInMegabytes":                  1024,
		"lockDuration":                        "PT1M",
		"defaultMessageTimeToLive":            "P10675199DT2H48M5.4775807S",
		"maxDeliveryCount":                    10,
		"requiresDuplicateDetection":          false,
		"requiresSession":                     false,
		"deadLetteringOnMessageExpiration":    false,
		"enableBatchedOperations":             true,
		"enablePartitioning":                  false,
		"autoDeleteOnIdle":                    "P10675199DT2H48M5.4775807S",
		"duplicateDetectionHistoryTimeWindow": "PT10M",
	}
}

// sbDefaultTopicProperties returns the server-assigned property defaults
// real Azure stamps on a topic when the client omits them.
func sbDefaultTopicProperties() map[string]any {
	return map[string]any{
		"status":                              "Active",
		"maxSizeInMegabytes":                  1024,
		"defaultMessageTimeToLive":            "P10675199DT2H48M5.4775807S",
		"requiresDuplicateDetection":          false,
		"enableBatchedOperations":             true,
		"enablePartitioning":                  false,
		"supportOrdering":                     true,
		"autoDeleteOnIdle":                    "P10675199DT2H48M5.4775807S",
		"duplicateDetectionHistoryTimeWindow": "PT10M",
	}
}

// sbDefaultSubscriptionProperties returns the server-assigned property
// defaults real Azure stamps on a subscription when the client omits them.
func sbDefaultSubscriptionProperties() map[string]any {
	return map[string]any{
		"status":                                    "Active",
		"lockDuration":                              "PT1M",
		"defaultMessageTimeToLive":                  "P10675199DT2H48M5.4775807S",
		"maxDeliveryCount":                          10,
		"requiresSession":                           false,
		"deadLetteringOnMessageExpiration":          false,
		"deadLetteringOnFilterEvaluationExceptions": true,
		"enableBatchedOperations":                   true,
		"autoDeleteOnIdle":                          "P10675199DT2H48M5.4775807S",
	}
}

func sbQueueFromAdminDescription(id, name string, desc *sbAdminQueueDescription) SBQueue {
	props := map[string]any{
		"status":             ptrString(desc.Status, "Active"),
		"maxSizeInMegabytes": ptrInt32(desc.MaxSizeInMegabytes, 1024),
	}
	putStringPtr(props, "lockDuration", desc.LockDuration)
	putStringPtr(props, "defaultMessageTimeToLive", desc.DefaultMessageTimeToLive)
	putStringPtr(props, "duplicateDetectionHistoryTimeWindow", desc.DuplicateDetectionHistoryTimeWindow)
	putStringPtr(props, "autoDeleteOnIdle", desc.AutoDeleteOnIdle)
	putStringPtr(props, "forwardTo", desc.ForwardTo)
	putStringPtr(props, "forwardDeadLetteredMessagesTo", desc.ForwardDeadLetteredMessagesTo)
	putStringPtr(props, "userMetadata", desc.UserMetadata)
	putBoolPtr(props, "requiresDuplicateDetection", desc.RequiresDuplicateDetection)
	putBoolPtr(props, "requiresSession", desc.RequiresSession)
	putBoolPtr(props, "deadLetteringOnMessageExpiration", desc.DeadLetteringOnMessageExpiration)
	putBoolPtr(props, "enableBatchedOperations", desc.EnableBatchedOperations)
	putBoolPtr(props, "enablePartitioning", desc.EnablePartitioning)
	putInt32Ptr(props, "maxDeliveryCount", desc.MaxDeliveryCount)
	putInt64Ptr(props, "maxMessageSizeInKilobytes", desc.MaxMessageSizeInKilobytes)
	return SBQueue{ID: id, Name: name, Type: "Microsoft.ServiceBus/namespaces/queues", Properties: props}
}

func sbTopicFromAdminDescription(id, name string, desc *sbAdminTopicDescription) SBTopic {
	props := map[string]any{
		"status":             ptrString(desc.Status, "Active"),
		"maxSizeInMegabytes": ptrInt32(desc.MaxSizeInMegabytes, 1024),
	}
	putStringPtr(props, "defaultMessageTimeToLive", desc.DefaultMessageTimeToLive)
	putStringPtr(props, "duplicateDetectionHistoryTimeWindow", desc.DuplicateDetectionHistoryTimeWindow)
	putStringPtr(props, "autoDeleteOnIdle", desc.AutoDeleteOnIdle)
	putStringPtr(props, "userMetadata", desc.UserMetadata)
	putBoolPtr(props, "requiresDuplicateDetection", desc.RequiresDuplicateDetection)
	putBoolPtr(props, "enableBatchedOperations", desc.EnableBatchedOperations)
	putBoolPtr(props, "enablePartitioning", desc.EnablePartitioning)
	putBoolPtr(props, "supportOrdering", desc.SupportOrdering)
	putInt64Ptr(props, "maxMessageSizeInKilobytes", desc.MaxMessageSizeInKilobytes)
	return SBTopic{ID: id, Name: name, Type: "Microsoft.ServiceBus/namespaces/topics", Properties: props}
}

func sbSubscriptionFromAdminDescription(id, name string, desc *sbAdminSubscriptionDescription) SBSubscription {
	props := map[string]any{"status": ptrString(desc.Status, "Active")}
	putStringPtr(props, "lockDuration", desc.LockDuration)
	putStringPtr(props, "defaultMessageTimeToLive", desc.DefaultMessageTimeToLive)
	putStringPtr(props, "autoDeleteOnIdle", desc.AutoDeleteOnIdle)
	putStringPtr(props, "forwardTo", desc.ForwardTo)
	putStringPtr(props, "forwardDeadLetteredMessagesTo", desc.ForwardDeadLetteredMessagesTo)
	putStringPtr(props, "userMetadata", desc.UserMetadata)
	putBoolPtr(props, "requiresSession", desc.RequiresSession)
	putBoolPtr(props, "deadLetteringOnMessageExpiration", desc.DeadLetteringOnMessageExpiration)
	putBoolPtr(props, "deadLetteringOnFilterEvaluationExceptions", desc.DeadLetteringOnFilterEvaluationExceptions)
	putBoolPtr(props, "enableBatchedOperations", desc.EnableBatchedOperations)
	putInt32Ptr(props, "maxDeliveryCount", desc.MaxDeliveryCount)
	return SBSubscription{ID: id, Name: name, Type: "Microsoft.ServiceBus/namespaces/topics/subscriptions", Properties: props}
}

func sbRuleFromAdminDescription(id, name string, desc *sbAdminRuleDescription) SBRule {
	props := map[string]any{
		"filterType": "TrueFilter",
		"actionType": "EmptyRuleAction",
	}
	if desc.Filter != nil {
		props["filterType"] = desc.Filter.Type
		if desc.Filter.SQLExpression != nil {
			props["sqlExpression"] = *desc.Filter.SQLExpression
		}
	}
	if desc.Action != nil {
		props["actionType"] = desc.Action.Type
		props["actionSQLExpression"] = desc.Action.SQLExpression
	}
	return SBRule{ID: id, Name: name, Type: "Microsoft.ServiceBus/namespaces/topics/subscriptions/rules", Properties: props}
}

func sbDefaultRule(id string) SBRule {
	return SBRule{
		ID:   id,
		Name: "$Default",
		Type: "Microsoft.ServiceBus/namespaces/topics/subscriptions/rules",
		Properties: map[string]any{
			"filterType":    "TrueFilter",
			"sqlExpression": "1=1",
			"actionType":    "EmptyRuleAction",
			"createdAt":     time.Now().UTC().Format(time.RFC3339),
		},
	}
}

func sbAdminQueueEntryFor(r *http.Request, namespace, name string, q SBQueue) sbAdminQueueEntry {
	return sbAdminQueueEntry{
		sbAdminEntryBase: sbAdminBaseEntry(r, namespace, name),
		Content: sbAdminQueueContent{
			Type:             "application/xml",
			QueueDescription: sbAdminQueueDescriptionFor(namespace, name, q),
		},
	}
}

func sbAdminTopicEntryFor(r *http.Request, namespace, name string, topic SBTopic) sbAdminTopicEntry {
	return sbAdminTopicEntry{
		sbAdminEntryBase: sbAdminBaseEntry(r, namespace, name),
		Content: sbAdminTopicContent{
			Type:             "application/xml",
			TopicDescription: sbAdminTopicDescriptionFor(topic),
		},
	}
}

func sbAdminSubscriptionEntryFor(r *http.Request, namespace, topic, sub string, stored SBSubscription) sbAdminSubscriptionEntry {
	return sbAdminSubscriptionEntry{
		sbAdminEntryBase: sbAdminBaseEntry(r, namespace, topic+"/Subscriptions/"+sub),
		Content: sbAdminSubscriptionContent{
			Type:                    "application/xml",
			SubscriptionDescription: sbAdminSubscriptionDescriptionFor(namespace, topic, sub, stored),
		},
	}
}

func sbAdminRuleEntryFor(r *http.Request, namespace, topic, sub, rule string, stored SBRule) sbAdminRuleEntry {
	return sbAdminRuleEntry{
		sbAdminEntryBase: sbAdminBaseEntry(r, namespace, topic+"/Subscriptions/"+sub+"/Rules/"+rule),
		Content: sbAdminRuleContent{
			Type:            "application/xml",
			RuleDescription: sbAdminRuleDescriptionFor(stored),
		},
	}
}

func sbAdminBaseEntry(r *http.Request, namespace, path string) sbAdminEntryBase {
	now := time.Now().UTC().Format(time.RFC3339)
	u := sbAdminURL(r, namespace, path)
	title := path[strings.LastIndex(path, "/")+1:]
	return sbAdminEntryBase{
		AtomSchema: sbAtomSchema,
		ID:         u,
		Title:      title,
		Updated:    now,
		Author:     &sbAdminAuthor{Name: namespace},
		Link:       &sbAdminLink{Rel: "self", HREF: u},
	}
}

func sbAdminURL(r *http.Request, namespace, path string) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s/%s", scheme, r.Host, strings.TrimPrefix(path, "/"))
}

// sbAdminQueueDescriptionFor renders a queue's admin-plane description.
// MessageCount / ActiveMessageCount reflect the queue's real data-plane state;
// dead-letter, scheduled, and transfer counts are zero because the sim models
// none of those sub-queues.
func sbAdminQueueDescriptionFor(namespace, name string, q SBQueue) sbAdminQueueDescription {
	dead, scheduled, transferDead, transfer := int32(0), int32(0), int32(0), int32(0)
	size := int64(0)
	count, active := sbQueueCounts(namespace, name)
	return sbAdminQueueDescription{
		ServiceBusSchema:                    sbDataSchema,
		InstanceMetadataSchema:              sbXMLSchema,
		LockDuration:                        stringProp(q.Properties, "lockDuration"),
		MaxSizeInMegabytes:                  int32Prop(q.Properties, "maxSizeInMegabytes"),
		RequiresDuplicateDetection:          boolProp(q.Properties, "requiresDuplicateDetection"),
		RequiresSession:                     boolProp(q.Properties, "requiresSession"),
		DefaultMessageTimeToLive:            stringProp(q.Properties, "defaultMessageTimeToLive"),
		DeadLetteringOnMessageExpiration:    boolProp(q.Properties, "deadLetteringOnMessageExpiration"),
		DuplicateDetectionHistoryTimeWindow: stringProp(q.Properties, "duplicateDetectionHistoryTimeWindow"),
		MaxDeliveryCount:                    int32Prop(q.Properties, "maxDeliveryCount"),
		EnableBatchedOperations:             boolProp(q.Properties, "enableBatchedOperations"),
		SizeInBytes:                         &size,
		MessageCount:                        &count,
		Status:                              stringProp(q.Properties, "status"),
		AccessedAt:                          "0001-01-01T00:00:00",
		CreatedAt:                           time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:                           time.Now().UTC().Format(time.RFC3339),
		AutoDeleteOnIdle:                    stringProp(q.Properties, "autoDeleteOnIdle"),
		EnablePartitioning:                  boolProp(q.Properties, "enablePartitioning"),
		CountDetails: &sbAdminCountDetails{
			ActiveMessageCount:             &active,
			DeadLetterMessageCount:         &dead,
			ScheduledMessageCount:          &scheduled,
			TransferDeadLetterMessageCount: &transferDead,
			TransferMessageCount:           &transfer,
		},
		ForwardTo:                     stringProp(q.Properties, "forwardTo"),
		ForwardDeadLetteredMessagesTo: stringProp(q.Properties, "forwardDeadLetteredMessagesTo"),
		UserMetadata:                  stringProp(q.Properties, "userMetadata"),
		MaxMessageSizeInKilobytes:     int64Prop(q.Properties, "maxMessageSizeInKilobytes"),
	}
}

func sbAdminTopicDescriptionFor(topic SBTopic) sbAdminTopicDescription {
	scheduled := int32(0)
	subCount := int32(0)
	for _, sub := range sbSubscriptions.List() {
		if strings.HasPrefix(sub.ID, topic.ID+"/subscriptions/") {
			subCount++
		}
	}
	size := int64(0)
	return sbAdminTopicDescription{
		ServiceBusSchema:                    sbDataSchema,
		InstanceMetadataSchema:              sbXMLSchema,
		DefaultMessageTimeToLive:            stringProp(topic.Properties, "defaultMessageTimeToLive"),
		MaxSizeInMegabytes:                  int32Prop(topic.Properties, "maxSizeInMegabytes"),
		RequiresDuplicateDetection:          boolProp(topic.Properties, "requiresDuplicateDetection"),
		DuplicateDetectionHistoryTimeWindow: stringProp(topic.Properties, "duplicateDetectionHistoryTimeWindow"),
		EnableBatchedOperations:             boolProp(topic.Properties, "enableBatchedOperations"),
		SizeInBytes:                         &size,
		Status:                              stringProp(topic.Properties, "status"),
		UserMetadata:                        stringProp(topic.Properties, "userMetadata"),
		AccessedAt:                          "0001-01-01T00:00:00",
		CreatedAt:                           time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:                           time.Now().UTC().Format(time.RFC3339),
		SupportOrdering:                     boolProp(topic.Properties, "supportOrdering"),
		AutoDeleteOnIdle:                    stringProp(topic.Properties, "autoDeleteOnIdle"),
		EnablePartitioning:                  boolProp(topic.Properties, "enablePartitioning"),
		CountDetails:                        &sbAdminCountDetails{ScheduledMessageCount: &scheduled},
		SubscriptionCount:                   &subCount,
		MaxMessageSizeInKilobytes:           int64Prop(topic.Properties, "maxMessageSizeInKilobytes"),
	}
}

// sbAdminSubscriptionDescriptionFor renders a topic subscription's admin-plane
// description with real data-plane message counts, like the queue equivalent.
func sbAdminSubscriptionDescriptionFor(namespace, topic, subName string, sub SBSubscription) sbAdminSubscriptionDescription {
	dead, transferDead, transfer := int32(0), int32(0), int32(0)
	count, active := sbQueueCounts(namespace, topic+"/"+subName)
	return sbAdminSubscriptionDescription{
		ServiceBusSchema:                          sbDataSchema,
		InstanceMetadataSchema:                    sbXMLSchema,
		LockDuration:                              stringProp(sub.Properties, "lockDuration"),
		RequiresSession:                           boolProp(sub.Properties, "requiresSession"),
		DefaultMessageTimeToLive:                  stringProp(sub.Properties, "defaultMessageTimeToLive"),
		DeadLetteringOnMessageExpiration:          boolProp(sub.Properties, "deadLetteringOnMessageExpiration"),
		DeadLetteringOnFilterEvaluationExceptions: boolProp(sub.Properties, "deadLetteringOnFilterEvaluationExceptions"),
		MaxDeliveryCount:                          int32Prop(sub.Properties, "maxDeliveryCount"),
		MessageCount:                              &count,
		EnableBatchedOperations:                   boolProp(sub.Properties, "enableBatchedOperations"),
		Status:                                    stringProp(sub.Properties, "status"),
		ForwardTo:                                 stringProp(sub.Properties, "forwardTo"),
		UserMetadata:                              stringProp(sub.Properties, "userMetadata"),
		ForwardDeadLetteredMessagesTo:             stringProp(sub.Properties, "forwardDeadLetteredMessagesTo"),
		AutoDeleteOnIdle:                          stringProp(sub.Properties, "autoDeleteOnIdle"),
		CreatedAt:                                 time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:                                 time.Now().UTC().Format(time.RFC3339),
		AccessedAt:                                "0001-01-01T00:00:00",
		CountDetails: &sbAdminCountDetails{
			ActiveMessageCount:             &active,
			DeadLetterMessageCount:         &dead,
			TransferDeadLetterMessageCount: &transferDead,
			TransferMessageCount:           &transfer,
		},
	}
}

func sbAdminRuleDescriptionFor(rule SBRule) sbAdminRuleDescription {
	filterType := stringValue(rule.Properties, "filterType", "TrueFilter")
	actionType := stringValue(rule.Properties, "actionType", "EmptyRuleAction")
	return sbAdminRuleDescription{
		ServiceBusSchema: sbDataSchema,
		XMLSchema:        sbXMLSchema,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		Name:             rule.Name,
		Filter: &sbAdminFilter{
			Type:          filterType,
			SQLExpression: stringProp(rule.Properties, "sqlExpression"),
		},
		Action: &sbAdminRuleAction{
			Type:          actionType,
			SQLExpression: stringValue(rule.Properties, "actionSQLExpression", ""),
		},
	}
}

func writeSBAdminXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", sbAdminContentType)
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(v)
}

func sbAdminError(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Detail  string   `xml:"Detail"`
	}{Code: code, Detail: detail})
}

func sbAdminPaged[T any](items []T, r *http.Request) []T {
	skip := parsePositiveInt(r.URL.Query().Get("$skip"))
	top := parsePositiveInt(r.URL.Query().Get("$top"))
	if skip > len(items) {
		return nil
	}
	items = items[skip:]
	if top > 0 && top < len(items) {
		items = items[:top]
	}
	return items
}

func parsePositiveInt(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func putStringPtr(m map[string]any, key string, v *string) {
	if v != nil {
		m[key] = *v
	}
}

func putBoolPtr(m map[string]any, key string, v *bool) {
	if v != nil {
		m[key] = *v
	}
}

func putInt32Ptr(m map[string]any, key string, v *int32) {
	if v != nil {
		m[key] = *v
	}
}

func putInt64Ptr(m map[string]any, key string, v *int64) {
	if v != nil {
		m[key] = *v
	}
}

func stringProp(m map[string]any, key string) *string {
	if v, ok := m[key].(string); ok {
		return &v
	}
	return nil
}

func stringValue(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func boolProp(m map[string]any, key string) *bool {
	if v, ok := m[key].(bool); ok {
		return &v
	}
	return nil
}

func int32Prop(m map[string]any, key string) *int32 {
	switch v := m[key].(type) {
	case int32:
		return &v
	case int:
		n := int32(v)
		return &n
	case float64:
		n := int32(v)
		return &n
	}
	return nil
}

func int64Prop(m map[string]any, key string) *int64 {
	switch v := m[key].(type) {
	case int64:
		return &v
	case int:
		n := int64(v)
		return &n
	case float64:
		n := int64(v)
		return &n
	}
	return nil
}

func ptrString(v *string, def string) string {
	if v == nil {
		return def
	}
	return *v
}

func ptrInt32(v *int32, def int32) int32 {
	if v == nil {
		return def
	}
	return *v
}

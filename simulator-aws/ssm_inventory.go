package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/google/uuid"
)

// SSM Inventory + Compliance + Nodes + managed-instance information +
// document-permissions slice. These are the read/report-back surfaces
// that fleet-management tooling (and the `aws ssm` CLI / terraform's
// inventory-adjacent data sources) hit on top of the document control
// plane in ssm_documents.go.
//
//   - Inventory: a managed node reports typed item lists (PutInventory)
//     keyed by (instance, type-name); GetInventory / ListInventoryEntries
//     read them back, GetInventorySchema describes the item types, and
//     DeleteInventory/DescribeInventoryDeletions model the async schema
//     deletion job.
//   - Compliance: PutComplianceItems records compliance items per
//     (resource, compliance-type); the List* ops read items and roll up
//     compliant/non-compliant counts.
//   - Nodes / managed-instance information: a node becomes "managed" the
//     moment it reports inventory or compliance, so DescribeInstance-
//     Information / DescribeInstanceProperties / ListNodes enumerate the
//     same set. DeregisterManagedInstance / UpdateManagedInstanceRole
//     mutate a managed-instance row.
//   - Document permissions: an account-share list layered over the
//     existing ssmDocuments store.
//   - Change Calendar state, plus the just-in-time node-access token /
//     request records.

// ---- stores -------------------------------------------------------------

// SSMInventoryEntry is one captured inventory item type for a node:
// the typed content (a list of attribute maps) plus capture metadata.
type SSMInventoryEntry struct {
	InstanceId    string              `json:"InstanceId"`
	TypeName      string              `json:"TypeName"`
	SchemaVersion string              `json:"SchemaVersion"`
	CaptureTime   string              `json:"CaptureTime"`
	ContentHash   string              `json:"ContentHash"`
	Content       []map[string]string `json:"Content"`
}

// SSMComplianceItem is one recorded compliance item for a resource,
// scoped by compliance type.
type SSMComplianceItem struct {
	ResourceId     string            `json:"ResourceId"`
	ResourceType   string            `json:"ResourceType"`
	ComplianceType string            `json:"ComplianceType"`
	Id             string            `json:"Id"`
	Title          string            `json:"Title"`
	Status         string            `json:"Status"`
	Severity       string            `json:"Severity"`
	Details        map[string]string `json:"Details"`
	ExecutionId    string            `json:"ExecutionId"`
	ExecutionType  string            `json:"ExecutionType"`
	ExecutionTime  float64           `json:"ExecutionTime"`
}

// SSMManagedInstance is a node Systems Manager knows about. A node is
// registered the moment it reports inventory or compliance (real SSM
// tracks a node as soon as its agent checks in).
type SSMManagedInstance struct {
	InstanceId       string  `json:"InstanceId"`
	PingStatus       string  `json:"PingStatus"`
	PlatformType     string  `json:"PlatformType"`
	PlatformName     string  `json:"PlatformName"`
	PlatformVersion  string  `json:"PlatformVersion"`
	AgentVersion     string  `json:"AgentVersion"`
	IamRole          string  `json:"IamRole"`
	ActivationId     string  `json:"ActivationId"`
	ComputerName     string  `json:"ComputerName"`
	IPAddress        string  `json:"IPAddress"`
	Name             string  `json:"Name"`
	ResourceType     string  `json:"ResourceType"`
	RegistrationDate float64 `json:"RegistrationDate"`
	LastPingDateTime float64 `json:"LastPingDateTime"`
}

// SSMInventoryDeletion is the async deletion job created by DeleteInventory.
type SSMInventoryDeletion struct {
	DeletionId        string  `json:"DeletionId"`
	TypeName          string  `json:"TypeName"`
	DeletionStartTime float64 `json:"DeletionStartTime"`
	LastStatus        string  `json:"LastStatus"`
	LastStatusMessage string  `json:"LastStatusMessage"`
	TotalCount        int     `json:"TotalCount"`
	RemainingCount    int     `json:"RemainingCount"`
}

// SSMDocShare is the per-document account-share list (PermissionType SHARE).
type SSMDocShare struct {
	Name       string   `json:"Name"`
	AccountIds []string `json:"AccountIds"`
	Version    string   `json:"Version"`
}

// SSMAccessRequest is a just-in-time node-access request and its issued
// short-lived credentials.
type SSMAccessRequest struct {
	AccessRequestId string  `json:"AccessRequestId"`
	Status          string  `json:"Status"`
	Reason          string  `json:"Reason"`
	AccessKeyId     string  `json:"AccessKeyId"`
	SecretAccessKey string  `json:"SecretAccessKey"`
	SessionToken    string  `json:"SessionToken"`
	ExpirationTime  float64 `json:"ExpirationTime"`
	CreatedTime     float64 `json:"CreatedTime"`
}

var (
	ssmInventory          sim.Store[SSMInventoryEntry]
	ssmComplianceItems    sim.Store[SSMComplianceItem]
	ssmManagedInstances   sim.Store[SSMManagedInstance]
	ssmInventoryDeletions sim.Store[SSMInventoryDeletion]
	ssmDocShares          sim.Store[SSMDocShare]
	ssmAccessRequests     sim.Store[SSMAccessRequest]
)

func registerSSMInventory(r *sim.AWSRouter, srv *sim.Server) {
	ssmInventory = sim.MakeStore[SSMInventoryEntry](srv.DB(), "ssm_inventory")
	ssmComplianceItems = sim.MakeStore[SSMComplianceItem](srv.DB(), "ssm_compliance_items")
	ssmManagedInstances = sim.MakeStore[SSMManagedInstance](srv.DB(), "ssm_managed_instances")
	ssmInventoryDeletions = sim.MakeStore[SSMInventoryDeletion](srv.DB(), "ssm_inventory_deletions")
	ssmDocShares = sim.MakeStore[SSMDocShare](srv.DB(), "ssm_doc_shares")
	ssmAccessRequests = sim.MakeStore[SSMAccessRequest](srv.DB(), "ssm_access_requests")

	for target, h := range map[string]http.HandlerFunc{
		"AmazonSSM.PutInventory":                    handleSSMPutInventory,
		"AmazonSSM.GetInventory":                    handleSSMGetInventory,
		"AmazonSSM.GetInventorySchema":              handleSSMGetInventorySchema,
		"AmazonSSM.DeleteInventory":                 handleSSMDeleteInventory,
		"AmazonSSM.DescribeInventoryDeletions":      handleSSMDescribeInventoryDeletions,
		"AmazonSSM.ListInventoryEntries":            handleSSMListInventoryEntries,
		"AmazonSSM.PutComplianceItems":              handleSSMPutComplianceItems,
		"AmazonSSM.ListComplianceItems":             handleSSMListComplianceItems,
		"AmazonSSM.ListComplianceSummaries":         handleSSMListComplianceSummaries,
		"AmazonSSM.ListResourceComplianceSummaries": handleSSMListResourceComplianceSummaries,
		"AmazonSSM.ListNodes":                       handleSSMListNodes,
		"AmazonSSM.ListNodesSummary":                handleSSMListNodesSummary,
		"AmazonSSM.DescribeInstanceInformation":     handleSSMDescribeInstanceInformation,
		"AmazonSSM.DescribeInstanceProperties":      handleSSMDescribeInstanceProperties,
		"AmazonSSM.DeregisterManagedInstance":       handleSSMDeregisterManagedInstance,
		"AmazonSSM.UpdateManagedInstanceRole":       handleSSMUpdateManagedInstanceRole,
		"AmazonSSM.DescribeDocumentPermission":      handleSSMDescribeDocumentPermission,
		"AmazonSSM.ModifyDocumentPermission":        handleSSMModifyDocumentPermission,
		"AmazonSSM.UpdateDocumentMetadata":          handleSSMUpdateDocumentMetadata,
		"AmazonSSM.ListDocumentMetadataHistory":     handleSSMListDocumentMetadataHistory,
		"AmazonSSM.GetCalendarState":                handleSSMGetCalendarState,
		"AmazonSSM.GetAccessToken":                  handleSSMGetAccessToken,
		"AmazonSSM.StartAccessRequest":              handleSSMStartAccessRequest,
	} {
		r.Register(target, h)
	}
}

// ssmInvKey keys an inventory entry by (instance, type-name).
func ssmInvKey(instanceID, typeName string) string {
	return instanceID + "\x00" + typeName
}

// ssmComplianceKey keys a compliance item by (resource, type, item-id).
func ssmComplianceKey(resourceID, complianceType, itemID string) string {
	return resourceID + "\x00" + complianceType + "\x00" + itemID
}

// ssmTrackManagedInstance upserts a managed-instance row, registering the
// node the first time it reports inventory/compliance and refreshing the
// last-ping time on every subsequent report.
func ssmTrackManagedInstance(instanceID string) {
	now := float64(time.Now().Unix())
	if mi, ok := ssmManagedInstances.Get(instanceID); ok {
		mi.LastPingDateTime = now
		mi.PingStatus = "Online"
		ssmManagedInstances.Put(instanceID, mi)
		return
	}
	resourceType := "ManagedInstance"
	if strings.HasPrefix(instanceID, "i-") {
		resourceType = "EC2Instance"
	}
	ssmManagedInstances.Put(instanceID, SSMManagedInstance{
		InstanceId:       instanceID,
		PingStatus:       "Online",
		PlatformType:     "Linux",
		PlatformName:     "Ubuntu",
		PlatformVersion:  "22.04",
		AgentVersion:     "3.3.0.0",
		ResourceType:     resourceType,
		ComputerName:     instanceID + ".sockerless.internal",
		RegistrationDate: now,
		LastPingDateTime: now,
	})
}

// ---- Inventory ----------------------------------------------------------

func handleSSMPutInventory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceId string `json:"InstanceId"`
		Items      []struct {
			TypeName      string              `json:"TypeName"`
			SchemaVersion string              `json:"SchemaVersion"`
			CaptureTime   string              `json:"CaptureTime"`
			ContentHash   string              `json:"ContentHash"`
			Content       []map[string]string `json:"Content"`
		} `json:"Items"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.InstanceId == "" {
		sim.AWSError(w, "ValidationException", "InstanceId is required", http.StatusBadRequest)
		return
	}
	if len(req.Items) == 0 {
		sim.AWSErrorf(w, "ItemContentMismatchException", http.StatusBadRequest,
			"At least one inventory item is required.")
		return
	}
	for _, it := range req.Items {
		if it.TypeName == "" {
			sim.AWSErrorf(w, "InvalidTypeNameException", http.StatusBadRequest,
				"The parameter type name isn't valid.")
			return
		}
		content := it.Content
		if content == nil {
			content = []map[string]string{}
		}
		ssmInventory.Put(ssmInvKey(req.InstanceId, it.TypeName), SSMInventoryEntry{
			InstanceId:    req.InstanceId,
			TypeName:      it.TypeName,
			SchemaVersion: it.SchemaVersion,
			CaptureTime:   it.CaptureTime,
			ContentHash:   it.ContentHash,
			Content:       content,
		})
	}
	ssmTrackManagedInstance(req.InstanceId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMGetInventory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// Group the captured entries by node into result entities.
	byNode := map[string][]SSMInventoryEntry{}
	for _, e := range ssmInventory.List() {
		byNode[e.InstanceId] = append(byNode[e.InstanceId], e)
	}
	ids := make([]string, 0, len(byNode))
	for id := range byNode {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	page, next := awsPage(ids, req.NextToken, req.MaxResults, 50)
	entities := make([]map[string]any, 0, len(page))
	for _, id := range page {
		entries := byNode[id]
		sort.Slice(entries, func(i, j int) bool { return entries[i].TypeName < entries[j].TypeName })
		data := map[string]any{}
		for _, e := range entries {
			data[e.TypeName] = map[string]any{
				"TypeName":      e.TypeName,
				"SchemaVersion": e.SchemaVersion,
				"CaptureTime":   e.CaptureTime,
				"ContentHash":   e.ContentHash,
				"Content":       ssmInvContentWire(e.Content),
			}
		}
		entities = append(entities, map[string]any{
			"Id":   id,
			"Data": data,
		})
	}
	resp := map[string]any{"Entities": entities}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func ssmInvContentWire(content []map[string]string) []map[string]string {
	if content == nil {
		return []map[string]string{}
	}
	return content
}

// ssmKnownInventoryTypes returns the inventory type names known to the
// account: the AWS-managed core types plus any custom type a node has
// reported.
func ssmKnownInventoryTypes() []string {
	seen := map[string]bool{
		"AWS:InstanceInformation": true,
		"AWS:Application":         true,
		"AWS:Network":             true,
		"AWS:AWSComponent":        true,
		"AWS:File":                true,
		"AWS:Service":             true,
	}
	for _, e := range ssmInventory.List() {
		seen[e.TypeName] = true
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func handleSSMGetInventorySchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TypeName   string `json:"TypeName"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	types := ssmKnownInventoryTypes()
	if req.TypeName != "" {
		filtered := types[:0:0]
		for _, t := range types {
			if t == req.TypeName || strings.HasPrefix(t, req.TypeName) {
				filtered = append(filtered, t)
			}
		}
		types = filtered
	}
	page, next := awsPage(types, req.NextToken, req.MaxResults, 50)
	schemas := make([]map[string]any, 0, len(page))
	for _, t := range page {
		schemas = append(schemas, map[string]any{
			"TypeName":    t,
			"Version":     "1.0",
			"DisplayName": ssmInventoryDisplayName(t),
			"Attributes":  ssmInventoryAttributes(t),
		})
	}
	resp := map[string]any{"Schemas": schemas}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func ssmInventoryDisplayName(typeName string) string {
	return strings.TrimPrefix(typeName, "AWS:")
}

// ssmInventoryAttributes returns the attribute schema for a type. For
// reported custom types we derive the attributes from the captured
// content keys so the schema reflects real data; AWS-managed types get a
// representative set.
func ssmInventoryAttributes(typeName string) []map[string]any {
	keys := map[string]bool{}
	for _, e := range ssmInventory.List() {
		if e.TypeName != typeName {
			continue
		}
		for _, row := range e.Content {
			for k := range row {
				keys[k] = true
			}
		}
	}
	if len(keys) == 0 {
		// AWS-managed type with no reported content yet: a minimal,
		// stable attribute set.
		keys["Name"] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{
			"Name":     n,
			"DataType": "STRING",
		})
	}
	return out
}

func handleSSMDeleteInventory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TypeName           string `json:"TypeName"`
		SchemaDeleteOption string `json:"SchemaDeleteOption"`
		DryRun             bool   `json:"DryRun"`
		ClientToken        string `json:"ClientToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TypeName == "" {
		sim.AWSErrorf(w, "InvalidTypeNameException", http.StatusBadRequest,
			"The parameter type name isn't valid.")
		return
	}
	// Count and (unless dry-run) drop matching entries.
	total := 0
	var matched []string
	for _, e := range ssmInventory.List() {
		if e.TypeName == req.TypeName {
			total++
			matched = append(matched, ssmInvKey(e.InstanceId, e.TypeName))
		}
	}
	deletionID := uuid.NewString()
	if !req.DryRun {
		for _, k := range matched {
			ssmInventory.Delete(k)
		}
		ssmInventoryDeletions.Put(deletionID, SSMInventoryDeletion{
			DeletionId:        deletionID,
			TypeName:          req.TypeName,
			DeletionStartTime: float64(time.Now().Unix()),
			LastStatus:        "Complete",
			LastStatusMessage: "Deletion complete.",
			TotalCount:        total,
			RemainingCount:    0,
		})
	}
	resp := map[string]any{
		"DeletionId":      deletionID,
		"TypeName":        req.TypeName,
		"DeletionSummary": ssmInvDeletionSummaryWire(total, 0),
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func ssmInvDeletionSummaryWire(total, remaining int) map[string]any {
	return map[string]any{
		"TotalCount":     total,
		"RemainingCount": remaining,
		"SummaryItems": []map[string]any{
			{
				"Version":        "1.0",
				"Count":          total,
				"RemainingCount": remaining,
			},
		},
	}
}

func handleSSMDescribeInventoryDeletions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeletionId string `json:"DeletionId"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmInventoryDeletions.List()
	if req.DeletionId != "" {
		filtered := all[:0:0]
		for _, d := range all {
			if d.DeletionId == req.DeletionId {
				filtered = append(filtered, d)
			}
		}
		all = filtered
	}
	sortBy(all, func(d SSMInventoryDeletion) string { return d.DeletionId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, d := range page {
		out = append(out, map[string]any{
			"DeletionId":           d.DeletionId,
			"TypeName":             d.TypeName,
			"DeletionStartTime":    d.DeletionStartTime,
			"LastStatus":           d.LastStatus,
			"LastStatusMessage":    d.LastStatusMessage,
			"DeletionSummary":      ssmInvDeletionSummaryWire(d.TotalCount, d.RemainingCount),
			"LastStatusUpdateTime": d.DeletionStartTime,
		})
	}
	resp := map[string]any{"InventoryDeletions": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMListInventoryEntries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceId string `json:"InstanceId"`
		TypeName   string `json:"TypeName"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.InstanceId == "" || req.TypeName == "" {
		sim.AWSError(w, "ValidationException", "InstanceId and TypeName are required", http.StatusBadRequest)
		return
	}
	entry, ok := ssmInventory.Get(ssmInvKey(req.InstanceId, req.TypeName))
	rows := []map[string]string{}
	schemaVersion := "1.0"
	captureTime := ""
	if ok {
		rows = entry.Content
		if entry.SchemaVersion != "" {
			schemaVersion = entry.SchemaVersion
		}
		captureTime = entry.CaptureTime
	}
	page, next := awsPage(rows, req.NextToken, req.MaxResults, 50)
	resp := map[string]any{
		"TypeName":      req.TypeName,
		"InstanceId":    req.InstanceId,
		"SchemaVersion": schemaVersion,
		"Entries":       ssmInvContentWire(page),
	}
	// An instance with nothing recorded for the type has no capture time, and
	// the model constrains the member to a real timestamp — so it is absent
	// rather than empty.
	if captureTime != "" {
		resp["CaptureTime"] = captureTime
	}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ---- Compliance ---------------------------------------------------------

func handleSSMPutComplianceItems(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceId       string `json:"ResourceId"`
		ResourceType     string `json:"ResourceType"`
		ComplianceType   string `json:"ComplianceType"`
		ExecutionSummary struct {
			ExecutionTime float64 `json:"ExecutionTime"`
			ExecutionId   string  `json:"ExecutionId"`
			ExecutionType string  `json:"ExecutionType"`
		} `json:"ExecutionSummary"`
		Items []struct {
			Id       string            `json:"Id"`
			Title    string            `json:"Title"`
			Severity string            `json:"Severity"`
			Status   string            `json:"Status"`
			Details  map[string]string `json:"Details"`
		} `json:"Items"`
		UploadType string `json:"UploadType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceId == "" || req.ComplianceType == "" {
		sim.AWSError(w, "ValidationException", "ResourceId and ComplianceType are required", http.StatusBadRequest)
		return
	}
	resourceType := req.ResourceType
	if resourceType == "" {
		resourceType = "ManagedInstance"
	}
	// COMPLETE upload (the default) replaces the items for this
	// (resource, type); PARTIAL appends. Either way the existing items
	// for this resource+type that we don't re-list go away on COMPLETE.
	if !strings.EqualFold(req.UploadType, "PARTIAL") {
		for _, c := range ssmComplianceItems.List() {
			if c.ResourceId == req.ResourceId && c.ComplianceType == req.ComplianceType {
				ssmComplianceItems.Delete(ssmComplianceKey(c.ResourceId, c.ComplianceType, c.Id))
			}
		}
	}
	for _, it := range req.Items {
		details := it.Details
		if details == nil {
			details = map[string]string{}
		}
		ssmComplianceItems.Put(ssmComplianceKey(req.ResourceId, req.ComplianceType, it.Id), SSMComplianceItem{
			ResourceId:     req.ResourceId,
			ResourceType:   resourceType,
			ComplianceType: req.ComplianceType,
			Id:             it.Id,
			Title:          it.Title,
			Status:         it.Status,
			Severity:       it.Severity,
			Details:        details,
			ExecutionId:    req.ExecutionSummary.ExecutionId,
			ExecutionType:  req.ExecutionSummary.ExecutionType,
			ExecutionTime:  req.ExecutionSummary.ExecutionTime,
		})
	}
	if strings.HasPrefix(req.ResourceId, "i-") || strings.HasPrefix(req.ResourceId, "mi-") {
		ssmTrackManagedInstance(req.ResourceId)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func ssmComplianceExecutionWire(c SSMComplianceItem) map[string]any {
	return map[string]any{
		"ExecutionTime": c.ExecutionTime,
		"ExecutionId":   c.ExecutionId,
		"ExecutionType": c.ExecutionType,
	}
}

func handleSSMListComplianceItems(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceIds   []string `json:"ResourceIds"`
		ResourceTypes []string `json:"ResourceTypes"`
		NextToken     string   `json:"NextToken"`
		MaxResults    int      `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmComplianceItems.List()
	filtered := all[:0:0]
	for _, c := range all {
		if len(req.ResourceIds) > 0 && !containsStr(req.ResourceIds, c.ResourceId) {
			continue
		}
		if len(req.ResourceTypes) > 0 && !containsStr(req.ResourceTypes, c.ResourceType) {
			continue
		}
		filtered = append(filtered, c)
	}
	sortBy(filtered, func(c SSMComplianceItem) string {
		return c.ResourceId + "\x00" + c.ComplianceType + "\x00" + c.Id
	})
	page, next := awsPage(filtered, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, c := range page {
		out = append(out, map[string]any{
			"ComplianceType":   c.ComplianceType,
			"ResourceType":     c.ResourceType,
			"ResourceId":       c.ResourceId,
			"Id":               c.Id,
			"Title":            c.Title,
			"Status":           c.Status,
			"Severity":         c.Severity,
			"ExecutionSummary": ssmComplianceExecutionWire(c),
			"Details":          c.Details,
		})
	}
	resp := map[string]any{"ComplianceItems": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ssmSeveritySummary tallies compliance items by severity for the
// SeveritySummary shape.
func ssmSeveritySummary(items []SSMComplianceItem) map[string]any {
	var critical, high, medium, low, informational, unspecified int
	for _, c := range items {
		switch strings.ToUpper(c.Severity) {
		case "CRITICAL":
			critical++
		case "HIGH":
			high++
		case "MEDIUM":
			medium++
		case "LOW":
			low++
		case "INFORMATIONAL":
			informational++
		default:
			unspecified++
		}
	}
	return map[string]any{
		"CriticalCount":      critical,
		"HighCount":          high,
		"MediumCount":        medium,
		"LowCount":           low,
		"InformationalCount": informational,
		"UnspecifiedCount":   unspecified,
	}
}

func ssmCompliantSplit(items []SSMComplianceItem) (compliant, nonCompliant []SSMComplianceItem) {
	for _, c := range items {
		if strings.EqualFold(c.Status, "NON_COMPLIANT") {
			nonCompliant = append(nonCompliant, c)
		} else {
			compliant = append(compliant, c)
		}
	}
	return compliant, nonCompliant
}

func handleSSMListComplianceSummaries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// One summary item per compliance type.
	byType := map[string][]SSMComplianceItem{}
	for _, c := range ssmComplianceItems.List() {
		byType[c.ComplianceType] = append(byType[c.ComplianceType], c)
	}
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	page, next := awsPage(types, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, t := range page {
		compliant, nonCompliant := ssmCompliantSplit(byType[t])
		out = append(out, map[string]any{
			"ComplianceType": t,
			"CompliantSummary": map[string]any{
				"CompliantCount":  len(compliant),
				"SeveritySummary": ssmSeveritySummary(compliant),
			},
			"NonCompliantSummary": map[string]any{
				"NonCompliantCount": len(nonCompliant),
				"SeveritySummary":   ssmSeveritySummary(nonCompliant),
			},
		})
	}
	resp := map[string]any{"ComplianceSummaryItems": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMListResourceComplianceSummaries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// One summary per (resource, compliance type).
	type rcKey struct{ resource, ctype string }
	byKey := map[rcKey][]SSMComplianceItem{}
	for _, c := range ssmComplianceItems.List() {
		k := rcKey{c.ResourceId, c.ComplianceType}
		byKey[k] = append(byKey[k], c)
	}
	keys := make([]rcKey, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].resource != keys[j].resource {
			return keys[i].resource < keys[j].resource
		}
		return keys[i].ctype < keys[j].ctype
	})
	page, next := awsPage(keys, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, k := range page {
		items := byKey[k]
		compliant, nonCompliant := ssmCompliantSplit(items)
		status := "COMPLIANT"
		overallSeverity := "UNSPECIFIED"
		if len(nonCompliant) > 0 {
			status = "NON_COMPLIANT"
			overallSeverity = ssmHighestSeverity(nonCompliant)
		}
		resourceType := "ManagedInstance"
		if len(items) > 0 {
			resourceType = items[0].ResourceType
		}
		out = append(out, map[string]any{
			"ComplianceType":   k.ctype,
			"ResourceType":     resourceType,
			"ResourceId":       k.resource,
			"Status":           status,
			"OverallSeverity":  overallSeverity,
			"ExecutionSummary": ssmComplianceExecutionWire(items[0]),
			"CompliantSummary": map[string]any{
				"CompliantCount":  len(compliant),
				"SeveritySummary": ssmSeveritySummary(compliant),
			},
			"NonCompliantSummary": map[string]any{
				"NonCompliantCount": len(nonCompliant),
				"SeveritySummary":   ssmSeveritySummary(nonCompliant),
			},
		})
	}
	resp := map[string]any{"ResourceComplianceSummaryItems": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func ssmHighestSeverity(items []SSMComplianceItem) string {
	order := []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "INFORMATIONAL", "UNSPECIFIED"}
	best := len(order)
	for _, c := range items {
		for i, s := range order {
			if strings.EqualFold(c.Severity, s) && i < best {
				best = i
			}
		}
	}
	if best == len(order) {
		return "UNSPECIFIED"
	}
	return order[best]
}

// ---- Nodes + managed-instance information -------------------------------

func ssmManagedInstanceList() []SSMManagedInstance {
	all := ssmManagedInstances.List()
	sortBy(all, func(m SSMManagedInstance) string { return m.InstanceId })
	return all
}

func handleSSMListNodes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SyncName   string `json:"SyncName"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmManagedInstanceList()
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, m := range page {
		out = append(out, map[string]any{
			"Id":          m.InstanceId,
			"CaptureTime": m.LastPingDateTime,
			"Region":      awsRegion(),
			"Owner": map[string]any{
				"AccountId": awsAccountID(),
			},
			"NodeType": map[string]any{
				"Instance": map[string]any{
					"AgentType":       "amazon-ssm-agent",
					"AgentVersion":    m.AgentVersion,
					"ComputerName":    m.ComputerName,
					"InstanceStatus":  m.PingStatus,
					"IpAddress":       m.IPAddress,
					"ManagedStatus":   "Managed",
					"PlatformType":    m.PlatformType,
					"PlatformName":    m.PlatformName,
					"PlatformVersion": m.PlatformVersion,
					"ResourceType":    m.ResourceType,
				},
			},
		})
	}
	resp := map[string]any{"Nodes": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMListNodesSummary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SyncName    string `json:"SyncName"`
		Aggregators []struct {
			AggregatorType string `json:"AggregatorType"`
			TypeName       string `json:"TypeName"`
			AttributeName  string `json:"AttributeName"`
		} `json:"Aggregators"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// The first aggregator's AttributeName selects the grouping key.
	attr := "ResourceType"
	if len(req.Aggregators) > 0 && req.Aggregators[0].AttributeName != "" {
		attr = req.Aggregators[0].AttributeName
	}
	// Each summary entry is a string map of attribute-name -> value
	// (NodeSummary is a string map): the grouped attribute value plus
	// the Count.
	byVal := map[string]int{}
	for _, m := range ssmManagedInstanceList() {
		byVal[ssmNodeAttributeValue(m, attr)]++
	}
	vals := make([]string, 0, len(byVal))
	for v := range byVal {
		vals = append(vals, v)
	}
	sort.Strings(vals)
	out := make([]map[string]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, map[string]string{
			attr:    v,
			"Count": itoaSSM(byVal[v]),
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Summary": out})
}

func ssmNodeAttributeValue(m SSMManagedInstance, attr string) string {
	switch attr {
	case "AgentVersion":
		return m.AgentVersion
	case "PlatformName":
		return m.PlatformName
	case "PlatformType":
		return m.PlatformType
	case "PlatformVersion":
		return m.PlatformVersion
	case "Region":
		return awsRegion()
	default:
		return m.ResourceType
	}
}

func handleSSMDescribeInstanceInformation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmManagedInstanceList()
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, m := range page {
		info := map[string]any{
			"InstanceId":       m.InstanceId,
			"PingStatus":       m.PingStatus,
			"LastPingDateTime": m.LastPingDateTime,
			"AgentVersion":     m.AgentVersion,
			"IsLatestVersion":  true,
			"PlatformType":     m.PlatformType,
			"PlatformName":     m.PlatformName,
			"PlatformVersion":  m.PlatformVersion,
			"ResourceType":     m.ResourceType,
			"ComputerName":     m.ComputerName,
			"RegistrationDate": m.RegistrationDate,
		}
		if m.IPAddress != "" {
			info["IPAddress"] = m.IPAddress
		}
		if m.Name != "" {
			info["Name"] = m.Name
		}
		if m.IamRole != "" {
			info["IamRole"] = m.IamRole
		}
		if m.ActivationId != "" {
			info["ActivationId"] = m.ActivationId
		}
		out = append(out, info)
	}
	resp := map[string]any{"InstanceInformationList": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribeInstanceProperties(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmManagedInstanceList()
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, m := range page {
		prop := map[string]any{
			"InstanceId":       m.InstanceId,
			"Name":             m.Name,
			"PingStatus":       m.PingStatus,
			"LastPingDateTime": m.LastPingDateTime,
			"AgentVersion":     m.AgentVersion,
			"PlatformType":     m.PlatformType,
			"PlatformName":     m.PlatformName,
			"PlatformVersion":  m.PlatformVersion,
			"ResourceType":     m.ResourceType,
			"ComputerName":     m.ComputerName,
			"RegistrationDate": m.RegistrationDate,
		}
		if m.IPAddress != "" {
			prop["IPAddress"] = m.IPAddress
		}
		if m.IamRole != "" {
			prop["IamRole"] = m.IamRole
		}
		if m.ActivationId != "" {
			prop["ActivationId"] = m.ActivationId
		}
		out = append(out, prop)
	}
	resp := map[string]any{"InstanceProperties": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDeregisterManagedInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceId string `json:"InstanceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmManagedInstances.Get(req.InstanceId); !ok {
		sim.AWSErrorf(w, "InvalidInstanceId", http.StatusBadRequest,
			"The following instance IDs aren't valid or don't exist: %s", req.InstanceId)
		return
	}
	ssmManagedInstances.Delete(req.InstanceId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMUpdateManagedInstanceRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceId string `json:"InstanceId"`
		IamRole    string `json:"IamRole"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	mi, ok := ssmManagedInstances.Get(req.InstanceId)
	if !ok {
		sim.AWSErrorf(w, "InvalidInstanceId", http.StatusBadRequest,
			"The following instance IDs aren't valid or don't exist: %s", req.InstanceId)
		return
	}
	mi.IamRole = req.IamRole
	ssmManagedInstances.Put(req.InstanceId, mi)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---- Document permissions + metadata ------------------------------------

func handleSSMDescribeDocumentPermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           string `json:"Name"`
		PermissionType string `json:"PermissionType"`
		MaxResults     int    `json:"MaxResults"`
		NextToken      string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmDocuments.Get(req.Name); !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	share, _ := ssmDocShares.Get(req.Name)
	ids := share.AccountIds
	if ids == nil {
		ids = []string{}
	}
	sharingInfo := make([]map[string]any, 0, len(ids))
	version := share.Version
	if version == "" {
		version = "$DEFAULT"
	}
	for _, id := range ids {
		sharingInfo = append(sharingInfo, map[string]any{
			"AccountId":             id,
			"SharedDocumentVersion": version,
		})
	}
	resp := map[string]any{
		"AccountIds":             ids,
		"AccountSharingInfoList": sharingInfo,
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMModifyDocumentPermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                  string   `json:"Name"`
		PermissionType        string   `json:"PermissionType"`
		AccountIdsToAdd       []string `json:"AccountIdsToAdd"`
		AccountIdsToRemove    []string `json:"AccountIdsToRemove"`
		SharedDocumentVersion string   `json:"SharedDocumentVersion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmDocuments.Get(req.Name); !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	if !strings.EqualFold(req.PermissionType, "Share") {
		sim.AWSError(w, "ValidationException", "PermissionType must be Share", http.StatusBadRequest)
		return
	}
	share, _ := ssmDocShares.Get(req.Name)
	share.Name = req.Name
	if req.SharedDocumentVersion != "" {
		share.Version = req.SharedDocumentVersion
	}
	set := map[string]bool{}
	for _, id := range share.AccountIds {
		set[id] = true
	}
	for _, id := range req.AccountIdsToAdd {
		set[id] = true
	}
	for _, id := range req.AccountIdsToRemove {
		delete(set, id)
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	share.AccountIds = ids
	ssmDocShares.Put(req.Name, share)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMUpdateDocumentMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"Name"`
		DocumentVersion string `json:"DocumentVersion"`
		DocumentReviews struct {
			Action  string `json:"Action"`
			Comment []struct {
				Type    string `json:"Type"`
				Content string `json:"Content"`
			} `json:"Comment"`
		} `json:"DocumentReviews"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	doc, ok := ssmDocuments.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	// Apply the review action to the targeted version's review status.
	reviewStatus := ""
	switch strings.ToUpper(req.DocumentReviews.Action) {
	case "SENDFORREVIEW":
		reviewStatus = "PENDING"
	case "APPROVE":
		reviewStatus = "APPROVED"
	case "REJECT":
		reviewStatus = "REJECTED"
	case "UPDATEREVIEW":
		reviewStatus = "PENDING"
	}
	if reviewStatus != "" {
		for i := range doc.Versions {
			if doc.Versions[i].DocumentVersion == req.DocumentVersion ||
				(req.DocumentVersion == "" && doc.Versions[i].DocumentVersion == doc.DefaultVersion) {
				doc.Versions[i].ReviewStatus = reviewStatus
			}
		}
		ssmDocuments.Put(doc.Name, doc)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMListDocumentMetadataHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"Name"`
		DocumentVersion string `json:"DocumentVersion"`
		Metadata        string `json:"Metadata"`
		NextToken       string `json:"NextToken"`
		MaxResults      int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	doc, ok := ssmDocuments.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
			"The specified SSM document doesn't exist.")
		return
	}
	ver := req.DocumentVersion
	if ver == "" {
		ver = doc.DefaultVersion
	}
	var reviewStatus string
	for _, v := range doc.Versions {
		if v.DocumentVersion == ver {
			reviewStatus = v.ReviewStatus
		}
	}
	if reviewStatus == "" {
		reviewStatus = "NOT_REVIEWED"
	}
	reviewerResponses := []map[string]any{
		{
			"CreateTime":   doc.CreatedDate,
			"UpdatedTime":  doc.CreatedDate,
			"ReviewStatus": reviewStatus,
			// Reviewer is a principal's name, not its ARN: the model admits no
			// colon in it, so an ARN here is a value the service never returns.
			"Reviewer": "root",
			"Comment":  []map[string]any{},
		},
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Name":            doc.Name,
		"DocumentVersion": ver,
		"Author":          doc.Owner,
		"Metadata": map[string]any{
			"ReviewerResponse": reviewerResponses,
		},
	})
}

// ---- Change Calendar ----------------------------------------------------

func handleSSMGetCalendarState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CalendarNames []string `json:"CalendarNames"`
		AtTime        string   `json:"AtTime"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	atTime := req.AtTime
	if atTime == "" {
		atTime = time.Now().UTC().Format(time.RFC3339)
	}
	// The calendar state is the intersection of every referenced
	// Change-Calendar document's default state. A Change Calendar
	// document's first mainStep action selects the default type:
	// "aws:openHours" => default OPEN, "aws:closedHours" => default
	// CLOSED. With no entries, the document's default state governs.
	state := "OPEN"
	for _, name := range req.CalendarNames {
		doc, ok := ssmDocuments.Get(name)
		if !ok {
			sim.AWSErrorf(w, "InvalidDocument", http.StatusBadRequest,
				"The specified SSM document doesn't exist.")
			return
		}
		ver, _ := ssmDocVersion(doc, "$DEFAULT")
		if ssmCalendarDefaultClosed(ver.Content) {
			state = "CLOSED"
		}
	}
	next := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"State":              state,
		"AtTime":             atTime,
		"NextTransitionTime": next,
	})
}

// ssmCalendarDefaultClosed reports whether a Change-Calendar document's
// default state is CLOSED. The iCalendar content carries an
// X-WR-CALDESC / X-CALENDAR-TYPE property; "DEFAULT-CLOSED" means the
// default state is CLOSED.
func ssmCalendarDefaultClosed(content string) bool {
	up := strings.ToUpper(content)
	return strings.Contains(up, "DEFAULT-CLOSED") || strings.Contains(up, "CLOSED")
}

// ---- Just-in-time node access -------------------------------------------

func handleSSMStartAccessRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason  string `json:"Reason"`
		Targets []struct {
			Key    string   `json:"Key"`
			Values []string `json:"Values"`
		} `json:"Targets"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Reason == "" {
		sim.AWSError(w, "ValidationException", "Reason is required", http.StatusBadRequest)
		return
	}
	if len(req.Targets) == 0 {
		sim.AWSError(w, "ValidationException", "Targets is required", http.StatusBadRequest)
		return
	}
	// An access-request id is "oi-" followed by exactly twelve hex digits.
	id := "oi-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	now := time.Now()
	ar := SSMAccessRequest{
		AccessRequestId: id,
		Status:          "Approved",
		Reason:          req.Reason,
		AccessKeyId:     "ASIA" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16],
		SecretAccessKey: strings.ReplaceAll(uuid.NewString(), "-", "") + strings.ReplaceAll(uuid.NewString(), "-", "")[:8],
		SessionToken:    "FwoG" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		ExpirationTime:  float64(now.Add(time.Hour).Unix()),
		CreatedTime:     float64(now.Unix()),
	}
	ssmAccessRequests.Put(id, ar)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AccessRequestId": id,
	})
}

func handleSSMGetAccessToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccessRequestId string `json:"AccessRequestId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	ar, ok := ssmAccessRequests.Get(req.AccessRequestId)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified access request doesn't exist.")
		return
	}
	// Settle the status deterministically on read: an approved request
	// whose credentials have expired reports Expired.
	status := ar.Status
	if strings.EqualFold(status, "Approved") && float64(time.Now().Unix()) > ar.ExpirationTime {
		status = "Expired"
	}
	resp := map[string]any{
		"AccessRequestStatus": status,
	}
	if strings.EqualFold(status, "Approved") {
		resp["Credentials"] = map[string]any{
			"AccessKeyId":     ar.AccessKeyId,
			"SecretAccessKey": ar.SecretAccessKey,
			"SessionToken":    ar.SessionToken,
			"ExpirationTime":  ar.ExpirationTime,
		}
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// AWS Organizations — the remaining operation surface beyond organizations.go:
// LeaveOrganization, CreateGovCloudAccount, the effective-policy validation
// reads (ListAccountsWithInvalidEffectivePolicy /
// ListEffectivePolicyValidationErrors), and the responsibility-transfer family
// (Invite/Update/Terminate/Describe + inbound/outbound lists). awsJson1.1,
// dispatched by X-Amz-Target AWSOrganizationsV20161128.<Op>. These reuse the
// account/org/policy stores from organizations.go; responsibility transfers are
// their own real store.

// OrgResponsibilityTransfer is a real responsibility-transfer resource. A
// transfer moves a responsibility (today only BILLING) from a source
// organization to a target organization; the sim, running as a single
// management account, records the transfer with its status and the source/target
// management-account participants. Direction marks whether the sim's management
// account is the originator (OUTBOUND) or the recipient (INBOUND).
type OrgResponsibilityTransfer struct {
	Id                 string `json:"Id"`
	Arn                string `json:"Arn"`
	Name               string `json:"Name"`
	Type               string `json:"Type"`
	Status             string `json:"Status"`
	SourceAccountId    string `json:"SourceAccountId"`
	SourceAccountEmail string `json:"SourceAccountEmail"`
	TargetAccountId    string `json:"TargetAccountId"`
	TargetAccountEmail string `json:"TargetAccountEmail"`
	StartTimestamp     int64  `json:"StartTimestamp"`
	EndTimestamp       int64  `json:"EndTimestamp"`
	ActiveHandshakeId  string `json:"ActiveHandshakeId"`
	Direction          string `json:"Direction"`
}

var orgResponsibilityTransfers sim.Store[OrgResponsibilityTransfer]

func registerOrganizationsExtra(r *AWSRouter, srv *sim.Server) {
	orgResponsibilityTransfers = sim.MakeStore[OrgResponsibilityTransfer](srv.DB(), "org_responsibility_transfers")

	for target, h := range map[string]http.HandlerFunc{
		"AWSOrganizationsV20161128.LeaveOrganization":                          handleOrgLeaveOrganization,
		"AWSOrganizationsV20161128.CreateGovCloudAccount":                      handleOrgCreateGovCloudAccount,
		"AWSOrganizationsV20161128.ListAccountsWithInvalidEffectivePolicy":     handleOrgListAccountsWithInvalidEffectivePolicy,
		"AWSOrganizationsV20161128.ListEffectivePolicyValidationErrors":        handleOrgListEffectivePolicyValidationErrors,
		"AWSOrganizationsV20161128.InviteOrganizationToTransferResponsibility": handleOrgInviteResponsibilityTransfer,
		"AWSOrganizationsV20161128.DescribeResponsibilityTransfer":             handleOrgDescribeResponsibilityTransfer,
		"AWSOrganizationsV20161128.UpdateResponsibilityTransfer":               handleOrgUpdateResponsibilityTransfer,
		"AWSOrganizationsV20161128.TerminateResponsibilityTransfer":            handleOrgTerminateResponsibilityTransfer,
		"AWSOrganizationsV20161128.ListInboundResponsibilityTransfers":         handleOrgListInboundResponsibilityTransfers,
		"AWSOrganizationsV20161128.ListOutboundResponsibilityTransfers":        handleOrgListOutboundResponsibilityTransfers,
	} {
		r.Register(target, h)
	}
}

// LeaveOrganization ----------------------------------------------------------

// handleOrgLeaveOrganization removes the *calling* member account from its
// organization. The real operation takes no input and acts on the caller's
// account; the sim's caller is always the organization's management account,
// which can never leave — so it faithfully raises
// MasterCannotLeaveOrganizationException, matching the real service.
func handleOrgLeaveOrganization(w http.ResponseWriter, _ *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	AWSError(w, "MasterCannotLeaveOrganizationException", "You can't remove the management account from the organization.", http.StatusBadRequest)
}

// CreateGovCloudAccount ------------------------------------------------------

// handleOrgCreateGovCloudAccount mirrors CreateAccount but also provisions the
// linked Amazon Web Services GovCloud (US) member account, so the resulting
// CreateAccountStatus carries both AccountId (commercial) and GovCloudAccountId.
// Both accounts are created immediately (the request settles to SUCCEEDED) and
// the status is readable via DescribeCreateAccountStatus.
func handleOrgCreateGovCloudAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		Email       string `json:"Email"`
		AccountName string `json:"AccountName"`
		RoleName    string `json:"RoleName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AccountName == "" || req.Email == "" {
		AWSError(w, "InvalidInputException", "AccountName and Email are required", http.StatusBadRequest)
		return
	}
	now := orgEpoch()

	commercialID := orgNewAccountID()
	orgAccounts.Put(commercialID, OrgAccount{
		Id:              commercialID,
		Arn:             orgAccountArn(commercialID),
		Email:           req.Email,
		Name:            req.AccountName,
		Status:          "ACTIVE",
		JoinedMethod:    "CREATED",
		JoinedTimestamp: now,
		ParentId:        orgRootID(),
	})

	// The GovCloud account is a separate member account in the GovCloud partition;
	// it is not part of the commercial organization tree (no ParentId placement).
	govCloudID := orgNewAccountID()

	status := OrgCreateAccountStatus{
		Id:                 "car-" + orgRandHex(16),
		AccountName:        req.AccountName,
		State:              "SUCCEEDED",
		RequestedTimestamp: now,
		CompletedTimestamp: now,
		AccountId:          commercialID,
	}
	orgCreateStatuses.Put(status.Id, status)

	m := orgCreateStatusToMap(status)
	m["GovCloudAccountId"] = govCloudID
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CreateAccountStatus": m})
}

// Effective-policy validation reads ------------------------------------------

// handleOrgListAccountsWithInvalidEffectivePolicy lists accounts whose effective
// policy of the given type fails validation. The sim's policies are stored
// verbatim and never produce an invalid merge, so this is an honest empty list
// over the real account store — the same shape the real service returns when no
// account has an invalid effective policy.
func handleOrgListAccountsWithInvalidEffectivePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		PolicyType string `json:"PolicyType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PolicyType == "" {
		AWSError(w, "InvalidInputException", "PolicyType is required", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Accounts":   []map[string]any{},
		"PolicyType": req.PolicyType,
	})
}

// handleOrgListEffectivePolicyValidationErrors lists the validation errors in an
// account's effective policy of the given type. The sim's policies always merge
// cleanly, so it returns an honest empty error list (with the account id, policy
// type and evaluation path echoed) for a real account, matching the real
// service's shape when the effective policy is valid.
func handleOrgListEffectivePolicyValidationErrors(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		AccountId  string `json:"AccountId"`
		PolicyType string `json:"PolicyType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AccountId == "" || req.PolicyType == "" {
		AWSError(w, "InvalidInputException", "AccountId and PolicyType are required", http.StatusBadRequest)
		return
	}
	if _, ok := orgAccounts.Get(req.AccountId); !ok {
		AWSError(w, "AccountNotFoundException", "You specified an account that doesn't exist.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AccountId":                       req.AccountId,
		"PolicyType":                      req.PolicyType,
		"Path":                            orgHierarchyPath(req.AccountId),
		"EvaluationTimestamp":             orgEpoch(),
		"EffectivePolicyValidationErrors": []map[string]any{},
	})
}

// Responsibility transfers ---------------------------------------------------

// orgResponsibilityTransferArn builds a transfer's ARN. The resource type the
// Service Reference names is "responsibilitytransfer", but the ARN segment the
// model's ResponsibilityTransferArn pattern requires is "transfer", followed by
// what is being transferred and which way it moves relative to this
// organization — a transfer is only identifiable with both.
func orgResponsibilityTransferArn(id, transferType, direction string) string {
	return "arn:aws:organizations::" + awsAccountID() + ":transfer/" + awsOrgID() +
		"/" + strings.ToLower(transferType) + "/" + strings.ToLower(direction) + "/" + id
}

// orgHierarchyPath renders a node's place in the organization the way the
// model's Path pattern requires: the organization, then the chain from the root
// down to the node, then a trailing slash. orgPathToRoot walks the other way —
// from the node up — because that is the order policy inheritance needs, so the
// chain is reversed here rather than in the walk.
func orgHierarchyPath(nodeID string) string {
	up := orgPathToRoot(nodeID)
	segments := make([]string, 0, len(up)+1)
	segments = append(segments, awsOrgID())
	for i := len(up) - 1; i >= 0; i-- {
		segments = append(segments, up[i])
	}
	return strings.Join(segments, "/") + "/"
}

func orgTransferParticipant(accountID, email string) map[string]any {
	m := map[string]any{}
	if accountID != "" {
		m["ManagementAccountId"] = accountID
	}
	if email != "" {
		m["ManagementAccountEmail"] = email
	}
	return m
}

func orgResponsibilityTransferToMap(t OrgResponsibilityTransfer) map[string]any {
	m := map[string]any{
		"Arn":            t.Arn,
		"Id":             t.Id,
		"Type":           t.Type,
		"Status":         t.Status,
		"Source":         orgTransferParticipant(t.SourceAccountId, t.SourceAccountEmail),
		"Target":         orgTransferParticipant(t.TargetAccountId, t.TargetAccountEmail),
		"StartTimestamp": t.StartTimestamp,
	}
	if t.Name != "" {
		m["Name"] = t.Name
	}
	if t.EndTimestamp != 0 {
		m["EndTimestamp"] = t.EndTimestamp
	}
	if t.ActiveHandshakeId != "" {
		m["ActiveHandshakeId"] = t.ActiveHandshakeId
	}
	return m
}

// handleOrgInviteResponsibilityTransfer creates a new responsibility transfer
// (status REQUESTED) initiated by the sim's management account toward the target
// organization, with an associated handshake. It returns the Handshake (matching
// the real response shape); the transfer is readable via
// DescribeResponsibilityTransfer and the outbound list.
func handleOrgInviteResponsibilityTransfer(w http.ResponseWriter, r *http.Request) {
	o, ok := orgRequireOrg(w)
	if !ok {
		return
	}
	var req struct {
		Type   string `json:"Type"`
		Target struct {
			Id   string `json:"Id"`
			Type string `json:"Type"`
		} `json:"Target"`
		Notes      string `json:"Notes"`
		SourceName string `json:"SourceName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Type == "" || req.Target.Id == "" || req.Target.Type == "" {
		AWSError(w, "InvalidInputException", "Type and Target (Id, Type) are required", http.StatusBadRequest)
		return
	}
	now := orgEpoch()

	h := OrgHandshake{
		Id:                  "h-" + orgRandHex(16),
		State:               "OPEN",
		RequestedTimestamp:  now,
		ExpirationTimestamp: now + 15*24*3600,
		Action:              "TRANSFER_RESPONSIBILITY",
		Parties: []OrgHandshakeParty{
			{Id: awsOrgID(), Type: "ORGANIZATION"},
			{Id: req.Target.Id, Type: req.Target.Type},
		},
	}
	h.Arn = orgHandshakeArn(h.Id, h.Action)
	orgHandshakes.Put(h.Id, h)

	// A handshake party names an account, an email address, or a whole
	// organization, and a transfer participant reports a management account and
	// its email. Only the first spelling names an account: inviting an
	// organization says nothing about which of its accounts manages it until
	// the handshake is accepted, and an id of any other kind put in the account
	// member would be an account id that is not one.
	targetAccount, targetEmail := "", ""
	switch req.Target.Type {
	case "ACCOUNT":
		targetAccount = req.Target.Id
	case "EMAIL":
		targetEmail = req.Target.Id
	}

	t := OrgResponsibilityTransfer{
		Id:                 "rt-" + orgRandHex(8),
		Name:               req.SourceName,
		Type:               req.Type,
		Status:             "REQUESTED",
		SourceAccountId:    o.MasterAccountId,
		SourceAccountEmail: o.MasterAccountEmail,
		TargetAccountId:    targetAccount,
		TargetAccountEmail: targetEmail,
		StartTimestamp:     now,
		ActiveHandshakeId:  h.Id,
		Direction:          "OUTBOUND",
	}
	t.Arn = orgResponsibilityTransferArn(t.Id, t.Type, t.Direction)
	orgResponsibilityTransfers.Put(t.Id, t)

	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(h)})
}

func handleOrgDescribeResponsibilityTransfer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"Id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := orgResponsibilityTransfers.Get(req.Id)
	if !ok {
		AWSError(w, "ResponsibilityTransferNotFoundException", "We can't find a responsibility transfer with the Id that you specified.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ResponsibilityTransfer": orgResponsibilityTransferToMap(t)})
}

func handleOrgUpdateResponsibilityTransfer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := orgResponsibilityTransfers.Get(req.Id)
	if !ok {
		AWSError(w, "ResponsibilityTransferNotFoundException", "We can't find a responsibility transfer with the Id that you specified.", http.StatusBadRequest)
		return
	}
	if req.Name != "" {
		t.Name = req.Name
	}
	orgResponsibilityTransfers.Put(t.Id, t)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ResponsibilityTransfer": orgResponsibilityTransferToMap(t)})
}

// handleOrgTerminateResponsibilityTransfer ends an in-flight transfer. The
// initiator withdrawing a still-REQUESTED transfer settles it to WITHDRAWN; an
// already-terminal transfer raises ResponsibilityTransferAlreadyInStatusException.
func handleOrgTerminateResponsibilityTransfer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id           string `json:"Id"`
		EndTimestamp int64  `json:"EndTimestamp"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := orgResponsibilityTransfers.Get(req.Id)
	if !ok {
		AWSError(w, "ResponsibilityTransferNotFoundException", "We can't find a responsibility transfer with the Id that you specified.", http.StatusBadRequest)
		return
	}
	switch t.Status {
	case "WITHDRAWN", "CANCELED", "DECLINED", "EXPIRED":
		AWSError(w, "ResponsibilityTransferAlreadyInStatusException", "The responsibility transfer is already in a terminal status.", http.StatusBadRequest)
		return
	}
	t.Status = "WITHDRAWN"
	end := req.EndTimestamp
	if end == 0 {
		end = orgEpoch()
	}
	t.EndTimestamp = end
	if h, hok := orgHandshakes.Get(t.ActiveHandshakeId); hok {
		h.State = "CANCELED"
		orgHandshakes.Put(h.Id, h)
	}
	t.ActiveHandshakeId = ""
	orgResponsibilityTransfers.Put(t.Id, t)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ResponsibilityTransfer": orgResponsibilityTransferToMap(t)})
}

func orgListResponsibilityTransfers(w http.ResponseWriter, r *http.Request, direction string) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		Type string `json:"Type"`
		Id   string `json:"Id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	transfers := orgResponsibilityTransfers.Filter(func(t OrgResponsibilityTransfer) bool {
		if t.Direction != direction {
			return false
		}
		if req.Type != "" && t.Type != req.Type {
			return false
		}
		if req.Id != "" && t.Id != req.Id {
			return false
		}
		return true
	})
	sort.Slice(transfers, func(i, j int) bool { return transfers[i].Id < transfers[j].Id })
	out := []map[string]any{}
	for _, t := range transfers {
		out = append(out, orgResponsibilityTransferToMap(t))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ResponsibilityTransfers": out})
}

func handleOrgListInboundResponsibilityTransfers(w http.ResponseWriter, r *http.Request) {
	orgListResponsibilityTransfers(w, r, "INBOUND")
}

func handleOrgListOutboundResponsibilityTransfers(w http.ResponseWriter, r *http.Request) {
	orgListResponsibilityTransfers(w, r, "OUTBOUND")
}

package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A handshake is an exchange between the organization that sends it and the
// account or organization that receives it. The model's documentation of
// each operation names which side may act: the recipient accepts or
// declines, the sender cancels, and both sides read it.

type OrgHandshake struct {
	Id                  string                 `json:"Id"`
	Arn                 string                 `json:"Arn"`
	Parties             []OrgHandshakeParty    `json:"Parties"`
	State               string                 `json:"State"`
	RequestedTimestamp  int64                  `json:"RequestedTimestamp"`
	ExpirationTimestamp int64                  `json:"ExpirationTimestamp"`
	Action              string                 `json:"Action"`
	Resources           []OrgHandshakeResource `json:"Resources,omitempty"`
	// StateTimestamp is when the handshake entered its state, from which the
	// 30 days a closed handshake stays listed run.
	StateTimestamp int64 `json:"StateTimestamp,omitempty"`
}

type OrgHandshakeParty struct {
	Id   string `json:"Id"`
	Type string `json:"Type"`
}

type OrgHandshakeResource struct {
	Type      string                 `json:"Type"`
	Value     string                 `json:"Value"`
	Resources []OrgHandshakeResource `json:"Resources,omitempty"`
}

// The User Guide gives an invitation 15 days for an answer and the invited
// accounts 90 days to approve the standard migration to all features.
const (
	orgHandshakeLifetime   = 15 * 24 * time.Hour
	orgAllFeaturesLifetime = 90 * 24 * time.Hour
	orgHandshakeRetention  = 30 * 24 * time.Hour
)

func orgHandshakeResourcesToMaps(rs []OrgHandshakeResource) []map[string]any {
	out := []map[string]any{}
	for _, res := range rs {
		m := map[string]any{"Type": res.Type, "Value": res.Value}
		if len(res.Resources) > 0 {
			m["Resources"] = orgHandshakeResourcesToMaps(res.Resources)
		}
		out = append(out, m)
	}
	return out
}

func orgHandshakeToMap(h OrgHandshake) map[string]any {
	parties := []map[string]any{}
	for _, p := range h.Parties {
		parties = append(parties, map[string]any{"Id": p.Id, "Type": p.Type})
	}
	return map[string]any{
		"Id":                  h.Id,
		"Arn":                 h.Arn,
		"Parties":             parties,
		"State":               h.State,
		"RequestedTimestamp":  h.RequestedTimestamp,
		"ExpirationTimestamp": h.ExpirationTimestamp,
		"Action":              h.Action,
		"Resources":           orgHandshakeResourcesToMaps(h.Resources),
	}
}

func orgHandshakeClosed(state string) bool {
	switch state {
	case "ACCEPTED", "DECLINED", "CANCELED", "EXPIRED":
		return true
	}
	return false
}

// orgLiveHandshake reads a handshake as it stands now: one left unanswered
// past its expiration has expired, and one closed more than 30 days ago is
// gone.
func orgLiveHandshake(id string) (OrgHandshake, bool) {
	h, ok := orgHandshakes.Get(id)
	if !ok {
		return OrgHandshake{}, false
	}
	return orgHandshakeNow(h)
}

func orgHandshakeNow(h OrgHandshake) (OrgHandshake, bool) {
	now := orgEpoch()
	if !orgHandshakeClosed(h.State) && now >= h.ExpirationTimestamp {
		// The User Guide: when the migration expires, AWS Organizations
		// cancels every request of that attempt.
		state := "EXPIRED"
		if h.Action == "APPROVE_ALL_FEATURES" {
			state = "CANCELED"
		}
		h.State, h.StateTimestamp = state, h.ExpirationTimestamp
		orgHandshakes.Put(h.Id, h)
		orgHandshakeSettled(h)
	}
	if orgHandshakeClosed(h.State) && now-h.StateTimestamp > int64(orgHandshakeRetention/time.Second) {
		orgHandshakes.Delete(h.Id)
		return OrgHandshake{}, false
	}
	return h, true
}

// orgHandshakeSentBy reports whether account sent h: every handshake the
// simulator holds comes from its organization, whose management account
// acts for it.
func orgHandshakeSentBy(h OrgHandshake, account string) bool {
	if !iamIsManagementAccount(account) || len(h.Parties) == 0 {
		return false
	}
	sender := h.Parties[0]
	return sender.Type == "ORGANIZATION" && sender.Id == awsOrgID()
}

func orgHandshakePartyTo(h OrgHandshake, account string) bool {
	return orgHandshakeSentBy(h, account) || orgHandshakeReceivedBy(h, account)
}

// orgHandshakeReceivedBy reports whether account receives h: the
// organization finalizes its own ENABLE_ALL_FEATURES request, and any other
// handshake goes to the account, the email address of its owner, or the
// organization it manages that its other party names.
func orgHandshakeReceivedBy(h OrgHandshake, account string) bool {
	if h.Action == "ENABLE_ALL_FEATURES" {
		return orgHandshakeSentBy(h, account)
	}
	for _, p := range h.Parties[1:] {
		if orgPartyNamesAccount(p, account) {
			return true
		}
	}
	return false
}

func orgPartyNamesAccount(p OrgHandshakeParty, account string) bool {
	switch p.Type {
	case "ACCOUNT":
		return p.Id == account
	case "EMAIL":
		email, ok := orgHostedAccountEmail(account)
		return ok && strings.EqualFold(email, p.Id)
	case "ORGANIZATION":
		return p.Id == awsOrgID() && iamIsManagementAccount(account)
	}
	return false
}

func orgHostedAccountEmail(account string) (string, bool) {
	if a, ok := orgAccounts.Get(account); ok {
		return a.Email, true
	}
	if a, ok := orgStandaloneAccounts.Get(account); ok {
		return a.Email, true
	}
	return "", false
}

// orgHandshakeTargetAccount is the hosted account an invitation's target
// names, if any.
func orgHandshakeTargetAccount(target OrgHandshakeParty) (string, bool) {
	if target.Type == "ACCOUNT" {
		return target.Id, iamHostedAccount(target.Id)
	}
	for _, store := range []sim.Store[OrgAccount]{orgAccounts, orgStandaloneAccounts} {
		for _, a := range store.List() {
			if strings.EqualFold(a.Email, target.Id) {
				return a.Id, true
			}
		}
	}
	return "", false
}

func writeOrgHandshakeConstraintViolation(w http.ResponseWriter, reason, message string) {
	awsWriteJSONErrorBody(w, awsJSON11, http.StatusBadRequest, "HandshakeConstraintViolationException", map[string]string{
		"__type":  "HandshakeConstraintViolationException",
		"Message": message,
		"Reason":  reason,
	})
}

func writeOrgHandshakeNotFound(w http.ResponseWriter) {
	AWSError(w, "HandshakeNotFoundException", "We can't find a handshake with the HandshakeId that you specified.", http.StatusBadRequest)
}

func writeOrgAlreadyInOrganization(w http.ResponseWriter) {
	writeOrgHandshakeConstraintViolation(w, "ALREADY_IN_AN_ORGANIZATION",
		"The handshake request is invalid because the invited account is already a member of an organization.")
}

func handleOrgInviteAccount(w http.ResponseWriter, r *http.Request) {
	o, ok := orgRequireOrg(w)
	if !ok {
		return
	}
	var req struct {
		Target OrgHandshakeParty `json:"Target"`
		Notes  string            `json:"Notes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Target.Id == "" || req.Target.Type == "" {
		AWSError(w, "InvalidInputException", "Target Id and Type are required", http.StatusBadRequest)
		return
	}
	// The model's HandshakeParty documentation: ORGANIZATION names only the
	// inviting organization, in responses.
	if req.Target.Type != "ACCOUNT" && req.Target.Type != "EMAIL" {
		AWSErrorf(w, "InvalidInputException", http.StatusBadRequest, "Target Type must be ACCOUNT or EMAIL, not %s.", req.Target.Type)
		return
	}
	if account, hosted := orgHandshakeTargetAccount(req.Target); hosted && iamOrganizationMember(account) {
		writeOrgAlreadyInOrganization(w)
		return
	}
	for _, h := range orgHandshakes.List() {
		h, live := orgHandshakeNow(h)
		if !live || h.Action != "INVITE" || orgHandshakeClosed(h.State) || len(h.Parties) < 2 {
			continue
		}
		if p := h.Parties[1]; p.Type == req.Target.Type && strings.EqualFold(p.Id, req.Target.Id) {
			AWSError(w, "DuplicateHandshakeException", "A handshake with the same action and target already exists.", http.StatusBadRequest)
			return
		}
	}
	managementName := ""
	if m, ok := orgAccounts.Get(o.MasterAccountId); ok {
		managementName = m.Name
	}
	now := orgEpoch()
	h := OrgHandshake{
		Id:                  "h-" + orgRandHex(16),
		State:               "OPEN",
		RequestedTimestamp:  now,
		StateTimestamp:      now,
		ExpirationTimestamp: now + int64(orgHandshakeLifetime/time.Second),
		Action:              "INVITE",
		Parties:             []OrgHandshakeParty{{Id: o.Id, Type: "ORGANIZATION"}, req.Target},
		Resources: []OrgHandshakeResource{
			{Type: "ORGANIZATION", Value: o.Id, Resources: []OrgHandshakeResource{
				{Type: "MASTER_EMAIL", Value: o.MasterAccountEmail},
				{Type: "MASTER_NAME", Value: managementName},
			}},
			{Type: req.Target.Type, Value: req.Target.Id},
		},
	}
	if req.Notes != "" {
		h.Resources = append(h.Resources, OrgHandshakeResource{Type: "NOTES", Value: req.Notes})
	}
	h.Arn = orgHandshakeArn(h.Id, h.Action)
	orgHandshakes.Put(h.Id, h)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(h)})
}

// orgHandshakeRespond moves the handshake the request names to state on
// behalf of a caller that may, running effect first when one is given.
func orgHandshakeRespond(w http.ResponseWriter, r *http.Request, state string, may func(OrgHandshake, string) bool, effect func(http.ResponseWriter, OrgHandshake, string) bool) {
	var req struct {
		HandshakeId string `json:"HandshakeId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	account := iamRequestAccount(r)
	h, ok := orgLiveHandshake(req.HandshakeId)
	if !ok || !orgHandshakePartyTo(h, account) {
		writeOrgHandshakeNotFound(w)
		return
	}
	if !may(h, account) {
		AWSError(w, "AccessDeniedException", "You don't have permissions to perform the requested operation.", http.StatusForbidden)
		return
	}
	switch {
	case h.State == state:
		AWSError(w, "HandshakeAlreadyInStateException", "The specified handshake is already in the requested state.", http.StatusBadRequest)
		return
	case orgHandshakeClosed(h.State):
		AWSError(w, "InvalidHandshakeTransitionException", "You can't perform the operation on the handshake in its current state.", http.StatusBadRequest)
		return
	}
	if effect != nil && !effect(w, h, account) {
		return
	}
	h.State, h.StateTimestamp = state, orgEpoch()
	orgHandshakes.Put(h.Id, h)
	orgHandshakeSettled(h)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(h)})
}

// orgAccepting checks that h can be accepted now: the organization finalizes
// the migration to all features only once every invited account approved,
// and an invitation brings its account into the organization.
func orgAccepting(w http.ResponseWriter, h OrgHandshake, account string) bool {
	switch h.Action {
	case "ENABLE_ALL_FEATURES":
		if h.State != "OPEN" {
			AWSError(w, "InvalidHandshakeTransitionException", "You can't perform the operation on the handshake in its current state.", http.StatusBadRequest)
			return false
		}
		return true
	case "INVITE":
		return orgJoinInvitedAccount(w, account)
	}
	return true
}

// orgHandshakeSettled carries a handshake's new state through the migration
// to all features: the last approval opens the request to finalize it, its
// acceptance enables all features, and closing it any other way cancels the
// approvals still open.
func orgHandshakeSettled(h OrgHandshake) {
	switch {
	case h.Action == "APPROVE_ALL_FEATURES" && h.State == "ACCEPTED":
		parent, ok := orgHandshakes.Get(orgParentHandshakeID(h))
		if !ok || parent.State != "REQUESTED" {
			return
		}
		for _, child := range orgMigrationApprovals(parent.Id) {
			if child.State != "ACCEPTED" {
				return
			}
		}
		parent.State, parent.StateTimestamp = "OPEN", orgEpoch()
		orgHandshakes.Put(parent.Id, parent)
	case h.Action == "ENABLE_ALL_FEATURES" && h.State == "ACCEPTED":
		if o, ok := orgOrg.Get(orgSingletonKey); ok {
			o.FeatureSet = "ALL"
			orgOrg.Put(orgSingletonKey, o)
		}
	case h.Action == "ENABLE_ALL_FEATURES" && orgHandshakeClosed(h.State):
		for _, child := range orgMigrationApprovals(h.Id) {
			if !orgHandshakeClosed(child.State) {
				child.State, child.StateTimestamp = "CANCELED", h.StateTimestamp
				orgHandshakes.Put(child.Id, child)
			}
		}
	}
}

func orgParentHandshakeID(h OrgHandshake) string {
	for _, res := range h.Resources {
		if res.Type == "PARENT_HANDSHAKE" {
			return res.Value
		}
	}
	return ""
}

// orgMigrationApprovals reads the APPROVE_ALL_FEATURES handshakes of one
// migration as stored, without expiring them.
func orgMigrationApprovals(parentID string) []OrgHandshake {
	return orgHandshakes.Filter(func(h OrgHandshake) bool {
		return h.Action == "APPROVE_ALL_FEATURES" && orgParentHandshakeID(h) == parentID
	})
}

// orgJoinInvitedAccount brings the account accepting an invitation into the
// organization at its root, where the FullAWSAccess policy covers it.
func orgJoinInvitedAccount(w http.ResponseWriter, account string) bool {
	if _, ok := orgRequireOrg(w); !ok {
		return false
	}
	if iamOrganizationMember(account) {
		writeOrgAlreadyInOrganization(w)
		return false
	}
	a, ok := orgStandaloneAccounts.Get(account)
	if !ok {
		panic("account " + account + " received an invitation but is neither a member nor a standalone account")
	}
	a.JoinedMethod, a.JoinedTimestamp, a.ParentId = "INVITED", orgEpoch(), orgRootID()
	orgAccounts.Put(account, a)
	orgStandaloneAccounts.Delete(account)
	orgAttachFullAWSAccess(account)
	return true
}

func handleOrgAcceptHandshake(w http.ResponseWriter, r *http.Request) {
	orgHandshakeRespond(w, r, "ACCEPTED", orgHandshakeReceivedBy, orgAccepting)
}

func handleOrgDeclineHandshake(w http.ResponseWriter, r *http.Request) {
	orgHandshakeRespond(w, r, "DECLINED", orgHandshakeReceivedBy, nil)
}

func handleOrgCancelHandshake(w http.ResponseWriter, r *http.Request) {
	orgHandshakeRespond(w, r, "CANCELED", orgHandshakeSentBy, nil)
}

func handleOrgDescribeHandshake(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HandshakeId string `json:"HandshakeId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	account := iamRequestAccount(r)
	h, ok := orgLiveHandshake(req.HandshakeId)
	if !ok || !orgHandshakePartyTo(h, account) {
		writeOrgHandshakeNotFound(w)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(h)})
}

// orgListHandshakes answers the live handshakes include admits, narrowed by
// the request's HandshakeFilter.
func orgListHandshakes(w http.ResponseWriter, r *http.Request, include func(OrgHandshake) bool) {
	var req struct {
		orgPage
		Filter struct {
			ActionType        string `json:"ActionType"`
			ParentHandshakeId string `json:"ParentHandshakeId"`
		} `json:"Filter"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// The model's HandshakeFilter documentation.
	if req.Filter.ActionType != "" && req.Filter.ParentHandshakeId != "" {
		AWSError(w, "InvalidInputException", "If you specify ActionType, you cannot also specify ParentHandshakeId.", http.StatusBadRequest)
		return
	}
	action, parent := req.Filter.ActionType, req.Filter.ParentHandshakeId
	out := []map[string]any{}
	for _, h := range orgHandshakes.List() {
		h, live := orgHandshakeNow(h)
		if !live || !include(h) || (action != "" && h.Action != action) || (parent != "" && orgParentHandshakeID(h) != parent) {
			continue
		}
		out = append(out, orgHandshakeToMap(h))
	}
	orgWriteList(w, req.orgPage, "Handshakes", "Id", out, nil)
}

func handleOrgListHandshakesForAccount(w http.ResponseWriter, r *http.Request) {
	account := iamRequestAccount(r)
	orgListHandshakes(w, r, func(h OrgHandshake) bool { return orgHandshakeReceivedBy(h, account) })
}

func handleOrgListHandshakesForOrganization(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	orgListHandshakes(w, r, func(h OrgHandshake) bool {
		return len(h.Parties) > 0 && h.Parties[0].Type == "ORGANIZATION" && h.Parties[0].Id == awsOrgID()
	})
}

// handleOrgEnableAllFeatures starts the User Guide's standard migration: it
// cancels the invitations still open, asks every invited account to approve
// in an APPROVE_ALL_FEATURES handshake, and answers the ENABLE_ALL_FEATURES
// request the management account accepts to finalize it, open at once when
// no account was invited.
func handleOrgEnableAllFeatures(w http.ResponseWriter, _ *http.Request) {
	o, ok := orgRequireOrg(w)
	if !ok {
		return
	}
	if o.FeatureSet == "ALL" {
		writeOrgHandshakeConstraintViolation(w, "ORGANIZATION_ALREADY_HAS_ALL_FEATURES",
			"The handshake request is invalid because the organization has already enabled all features.")
		return
	}
	live := []OrgHandshake{}
	for _, h := range orgHandshakes.List() {
		if h, ok := orgHandshakeNow(h); ok && !orgHandshakeClosed(h.State) {
			live = append(live, h)
		}
	}
	for _, h := range live {
		if h.Action == "ENABLE_ALL_FEATURES" {
			writeOrgHandshakeConstraintViolation(w, "ORGANIZATION_IS_ALREADY_PENDING_ALL_FEATURES_MIGRATION",
				"The handshake request is invalid because the organization has already started the process to enable all features.")
			return
		}
	}
	now := orgEpoch()
	for _, h := range live {
		if h.Action == "INVITE" {
			h.State, h.StateTimestamp = "CANCELED", now
			orgHandshakes.Put(h.Id, h)
		}
	}
	organization := OrgHandshakeParty{Id: o.Id, Type: "ORGANIZATION"}
	parent := OrgHandshake{
		Id:                  "h-" + orgRandHex(16),
		State:               "OPEN",
		RequestedTimestamp:  now,
		StateTimestamp:      now,
		ExpirationTimestamp: now + int64(orgAllFeaturesLifetime/time.Second),
		Action:              "ENABLE_ALL_FEATURES",
		Parties:             []OrgHandshakeParty{organization},
		Resources:           []OrgHandshakeResource{{Type: "ORGANIZATION", Value: o.Id}},
	}
	parent.Arn = orgHandshakeArn(parent.Id, parent.Action)
	invited := orgAccounts.Filter(func(a OrgAccount) bool { return a.JoinedMethod == "INVITED" && a.Id != o.MasterAccountId })
	sort.Slice(invited, func(i, j int) bool { return invited[i].Id < invited[j].Id })
	if len(invited) > 0 {
		parent.State = "REQUESTED"
	}
	orgHandshakes.Put(parent.Id, parent)
	for _, a := range invited {
		approval := OrgHandshake{
			Id:                  "h-" + orgRandHex(16),
			State:               "OPEN",
			RequestedTimestamp:  now,
			StateTimestamp:      now,
			ExpirationTimestamp: parent.ExpirationTimestamp,
			Action:              "APPROVE_ALL_FEATURES",
			Parties:             []OrgHandshakeParty{organization, {Id: a.Id, Type: "ACCOUNT"}},
			Resources: []OrgHandshakeResource{
				{Type: "PARENT_HANDSHAKE", Value: parent.Id},
				{Type: "ORGANIZATION", Value: o.Id},
				{Type: "ACCOUNT", Value: a.Id},
			},
		}
		approval.Arn = orgHandshakeArn(approval.Id, approval.Action)
		orgHandshakes.Put(approval.Id, approval)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(parent)})
}

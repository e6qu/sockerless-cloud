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

const (
	orgHandshakeLifetime  = 15 * 24 * time.Hour
	orgHandshakeRetention = 30 * 24 * time.Hour
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
		h.State, h.StateTimestamp = "EXPIRED", h.ExpirationTimestamp
		orgHandshakes.Put(h.Id, h)
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

// orgHandshakeReceivedBy reports whether account receives h. An
// ENABLE_ALL_FEATURES request goes to every member account; any other
// handshake to the account, the email address of its owner, or the
// organization it manages that its other party names.
func orgHandshakeReceivedBy(h OrgHandshake, account string) bool {
	if h.Action == "ENABLE_ALL_FEATURES" {
		_, member := orgAccounts.Get(account)
		return member && !iamIsManagementAccount(account)
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
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(h)})
}

// orgJoinInvitedAccount brings the account accepting an invitation into the
// organization at its root, where the FullAWSAccess policy covers it.
func orgJoinInvitedAccount(w http.ResponseWriter, h OrgHandshake, account string) bool {
	if h.Action != "INVITE" {
		return true
	}
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
	orgHandshakeRespond(w, r, "ACCEPTED", orgHandshakeReceivedBy, orgJoinInvitedAccount)
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
	action := req.Filter.ActionType
	out := []map[string]any{}
	hs := orgHandshakes.List()
	sort.Slice(hs, func(i, j int) bool { return hs[i].Id < hs[j].Id })
	for _, h := range hs {
		h, live := orgHandshakeNow(h)
		if !live || !include(h) || (action != "" && h.Action != action) {
			continue
		}
		// No handshake the simulator sends is the child of another.
		if req.Filter.ParentHandshakeId != "" {
			continue
		}
		out = append(out, orgHandshakeToMap(h))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshakes": out})
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

package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// AWS Organizations — a faithful slice of the real service's resource tree:
// an Organization (management account + a Root), Organizational Units in a tree
// under the Root, member Accounts placed in the Root or an OU, Policies
// (SERVICE_CONTROL_POLICY / TAG_POLICY / …) attachable to roots/OUs/accounts,
// Handshakes (invitations), delegated administrators, AWS service access, a
// resource policy, and tags. awsJson1.1, dispatched by X-Amz-Target
// AWSOrganizationsV20161128.<Op>.
//
// The org id is stable per process (override with SOCKERLESS_AWS_ORG_ID); the
// account the sim runs as is the organization's management account. A default
// organization (with a Root and the management account) is materialized on
// startup so the aws:PrincipalOrgID IAM condition key always resolves.

func awsOrgID() string {
	if id := os.Getenv("SOCKERLESS_AWS_ORG_ID"); id != "" {
		return id
	}
	// The model's OrganizationId pattern requires at least ten characters after
	// the "o-", and the id appears inside every ARN Organizations emits — so an
	// id one character short makes every one of those ARNs malformed rather
	// than just this one value.
	return "o-sim0000000"
}

func awsOrgArn() string {
	return "arn:aws:organizations::" + awsAccountID() + ":organization/" + awsOrgID()
}

// AWS-managed FullAWSAccess SCP — present on every real org and attached to the
// root by default. Its fixed id mirrors the real service.
const orgFullAWSAccessPolicyID = "p-FullAWSAccess"

// Stored shapes -------------------------------------------------------------

// OrgOrganization is the singleton organization row (one per sim process).
type OrgOrganization struct {
	Id                 string `json:"Id"`
	Arn                string `json:"Arn"`
	FeatureSet         string `json:"FeatureSet"`
	MasterAccountId    string `json:"MasterAccountId"`
	MasterAccountArn   string `json:"MasterAccountArn"`
	MasterAccountEmail string `json:"MasterAccountEmail"`
}

// OrgAccount is a member account.
type OrgAccount struct {
	Id              string `json:"Id"`
	Arn             string `json:"Arn"`
	Email           string `json:"Email"`
	Name            string `json:"Name"`
	Status          string `json:"Status"`
	JoinedMethod    string `json:"JoinedMethod"`
	JoinedTimestamp int64  `json:"JoinedTimestamp"`
	ParentId        string `json:"-"`
}

// OrgRoot is the organization root.
type OrgRoot struct {
	Id   string `json:"Id"`
	Arn  string `json:"Arn"`
	Name string `json:"Name"`
	// EnabledPolicyTypes are the policy types ENABLED on the root (beyond the
	// always-available SERVICE_CONTROL_POLICY).
	EnabledPolicyTypes []string `json:"EnabledPolicyTypes"`
}

// OrgOU is an organizational unit.
type OrgOU struct {
	Id       string `json:"Id"`
	Arn      string `json:"Arn"`
	Name     string `json:"Name"`
	ParentId string `json:"ParentId"`
}

// OrgPolicy is a policy document.
type OrgPolicy struct {
	Id          string `json:"Id"`
	Arn         string `json:"Arn"`
	Name        string `json:"Name"`
	Description string `json:"Description"`
	Type        string `json:"Type"`
	AwsManaged  bool   `json:"AwsManaged"`
	Content     string `json:"Content"`
}

// OrgHandshake is an invitation handshake.
type OrgHandshake struct {
	Id                  string              `json:"Id"`
	Arn                 string              `json:"Arn"`
	Parties             []OrgHandshakeParty `json:"Parties"`
	State               string              `json:"State"`
	RequestedTimestamp  int64               `json:"RequestedTimestamp"`
	ExpirationTimestamp int64               `json:"ExpirationTimestamp"`
	Action              string              `json:"Action"`
}

type OrgHandshakeParty struct {
	Id   string `json:"Id"`
	Type string `json:"Type"`
}

// OrgCreateAccountStatus tracks an async CreateAccount request.
type OrgCreateAccountStatus struct {
	Id                 string `json:"Id"`
	AccountName        string `json:"AccountName"`
	State              string `json:"State"`
	RequestedTimestamp int64  `json:"RequestedTimestamp"`
	CompletedTimestamp int64  `json:"CompletedTimestamp"`
	AccountId          string `json:"AccountId"`
}

// OrgDelegatedAdmin is a delegated administrator account for a service.
type OrgDelegatedAdmin struct {
	AccountId             string `json:"AccountId"`
	ServicePrincipal      string `json:"ServicePrincipal"`
	DelegationEnabledDate int64  `json:"DelegationEnabledDate"`
}

// OrgServiceAccess is a service principal granted trusted access.
type OrgServiceAccess struct {
	ServicePrincipal string `json:"ServicePrincipal"`
	DateEnabled      int64  `json:"DateEnabled"`
}

// OrgPolicyAttachment links a policy to a target (root/OU/account).
type OrgPolicyAttachment struct {
	PolicyId string `json:"PolicyId"`
	TargetId string `json:"TargetId"`
}

// OrgResourcePolicy is the singleton organization resource policy.
type OrgResourcePolicy struct {
	Id      string `json:"Id"`
	Arn     string `json:"Arn"`
	Content string `json:"Content"`
}

// OrgTags is a tag set keyed by ResourceId.
type OrgTags struct {
	ResourceId string            `json:"ResourceId"`
	Tags       map[string]string `json:"Tags"`
}

// State stores ---------------------------------------------------------------

var (
	orgOrg               sim.Store[OrgOrganization]
	orgAccounts          sim.Store[OrgAccount]
	orgRoots             sim.Store[OrgRoot]
	orgOUs               sim.Store[OrgOU]
	orgPolicies          sim.Store[OrgPolicy]
	orgPolicyAttachments sim.Store[OrgPolicyAttachment]
	orgHandshakes        sim.Store[OrgHandshake]
	orgCreateStatuses    sim.Store[OrgCreateAccountStatus]
	orgDelegatedAdmins   sim.Store[OrgDelegatedAdmin]
	orgServiceAccess     sim.Store[OrgServiceAccess]
	orgResourcePolicies  sim.Store[OrgResourcePolicy]
	orgTags              sim.Store[OrgTags]
)

const orgSingletonKey = "default"

func registerOrganizations(r *sim.AWSRouter, srv *sim.Server) {
	orgOrg = sim.MakeStore[OrgOrganization](srv.DB(), "org_organization")
	orgAccounts = sim.MakeStore[OrgAccount](srv.DB(), "org_accounts")
	orgRoots = sim.MakeStore[OrgRoot](srv.DB(), "org_roots")
	orgOUs = sim.MakeStore[OrgOU](srv.DB(), "org_ous")
	orgPolicies = sim.MakeStore[OrgPolicy](srv.DB(), "org_policies")
	orgPolicyAttachments = sim.MakeStore[OrgPolicyAttachment](srv.DB(), "org_policy_attachments")
	orgHandshakes = sim.MakeStore[OrgHandshake](srv.DB(), "org_handshakes")
	orgCreateStatuses = sim.MakeStore[OrgCreateAccountStatus](srv.DB(), "org_create_statuses")
	orgDelegatedAdmins = sim.MakeStore[OrgDelegatedAdmin](srv.DB(), "org_delegated_admins")
	orgServiceAccess = sim.MakeStore[OrgServiceAccess](srv.DB(), "org_service_access")
	orgResourcePolicies = sim.MakeStore[OrgResourcePolicy](srv.DB(), "org_resource_policies")
	orgTags = sim.MakeStore[OrgTags](srv.DB(), "org_tags")

	orgEnsureDefault()

	// Organization.
	r.Register("AWSOrganizationsV20161128.CreateOrganization", handleOrgCreateOrganization)
	r.Register("AWSOrganizationsV20161128.DeleteOrganization", handleOrgDeleteOrganization)
	r.Register("AWSOrganizationsV20161128.DescribeOrganization", handleOrgDescribeOrganization)
	r.Register("AWSOrganizationsV20161128.EnableAllFeatures", handleOrgEnableAllFeatures)

	// Accounts.
	r.Register("AWSOrganizationsV20161128.CreateAccount", handleOrgCreateAccount)
	r.Register("AWSOrganizationsV20161128.DescribeAccount", handleOrgDescribeAccount)
	r.Register("AWSOrganizationsV20161128.DescribeCreateAccountStatus", handleOrgDescribeCreateAccountStatus)
	r.Register("AWSOrganizationsV20161128.ListCreateAccountStatus", handleOrgListCreateAccountStatus)
	r.Register("AWSOrganizationsV20161128.ListAccounts", handleOrgListAccounts)
	r.Register("AWSOrganizationsV20161128.ListAccountsForParent", handleOrgListAccountsForParent)
	r.Register("AWSOrganizationsV20161128.MoveAccount", handleOrgMoveAccount)
	r.Register("AWSOrganizationsV20161128.RemoveAccountFromOrganization", handleOrgRemoveAccountFromOrganization)
	r.Register("AWSOrganizationsV20161128.CloseAccount", handleOrgCloseAccount)

	// Organizational Units.
	r.Register("AWSOrganizationsV20161128.CreateOrganizationalUnit", handleOrgCreateOU)
	r.Register("AWSOrganizationsV20161128.DeleteOrganizationalUnit", handleOrgDeleteOU)
	r.Register("AWSOrganizationsV20161128.DescribeOrganizationalUnit", handleOrgDescribeOU)
	r.Register("AWSOrganizationsV20161128.UpdateOrganizationalUnit", handleOrgUpdateOU)
	r.Register("AWSOrganizationsV20161128.ListOrganizationalUnitsForParent", handleOrgListOUsForParent)

	// Tree navigation.
	r.Register("AWSOrganizationsV20161128.ListRoots", handleOrgListRoots)
	r.Register("AWSOrganizationsV20161128.ListChildren", handleOrgListChildren)
	r.Register("AWSOrganizationsV20161128.ListParents", handleOrgListParents)

	// Policies.
	r.Register("AWSOrganizationsV20161128.CreatePolicy", handleOrgCreatePolicy)
	r.Register("AWSOrganizationsV20161128.DeletePolicy", handleOrgDeletePolicy)
	r.Register("AWSOrganizationsV20161128.DescribePolicy", handleOrgDescribePolicy)
	r.Register("AWSOrganizationsV20161128.UpdatePolicy", handleOrgUpdatePolicy)
	r.Register("AWSOrganizationsV20161128.AttachPolicy", handleOrgAttachPolicy)
	r.Register("AWSOrganizationsV20161128.DetachPolicy", handleOrgDetachPolicy)
	r.Register("AWSOrganizationsV20161128.ListPolicies", handleOrgListPolicies)
	r.Register("AWSOrganizationsV20161128.ListPoliciesForTarget", handleOrgListPoliciesForTarget)
	r.Register("AWSOrganizationsV20161128.ListTargetsForPolicy", handleOrgListTargetsForPolicy)
	r.Register("AWSOrganizationsV20161128.EnablePolicyType", handleOrgEnablePolicyType)
	r.Register("AWSOrganizationsV20161128.DisablePolicyType", handleOrgDisablePolicyType)
	r.Register("AWSOrganizationsV20161128.DescribeEffectivePolicy", handleOrgDescribeEffectivePolicy)

	// Handshakes.
	r.Register("AWSOrganizationsV20161128.InviteAccountToOrganization", handleOrgInviteAccount)
	r.Register("AWSOrganizationsV20161128.AcceptHandshake", handleOrgAcceptHandshake)
	r.Register("AWSOrganizationsV20161128.DeclineHandshake", handleOrgDeclineHandshake)
	r.Register("AWSOrganizationsV20161128.CancelHandshake", handleOrgCancelHandshake)
	r.Register("AWSOrganizationsV20161128.DescribeHandshake", handleOrgDescribeHandshake)
	r.Register("AWSOrganizationsV20161128.ListHandshakesForAccount", handleOrgListHandshakesForAccount)
	r.Register("AWSOrganizationsV20161128.ListHandshakesForOrganization", handleOrgListHandshakesForOrganization)

	// Delegated admin & service access.
	r.Register("AWSOrganizationsV20161128.RegisterDelegatedAdministrator", handleOrgRegisterDelegatedAdmin)
	r.Register("AWSOrganizationsV20161128.DeregisterDelegatedAdministrator", handleOrgDeregisterDelegatedAdmin)
	r.Register("AWSOrganizationsV20161128.ListDelegatedAdministrators", handleOrgListDelegatedAdmins)
	r.Register("AWSOrganizationsV20161128.ListDelegatedServicesForAccount", handleOrgListDelegatedServices)
	r.Register("AWSOrganizationsV20161128.EnableAWSServiceAccess", handleOrgEnableServiceAccess)
	r.Register("AWSOrganizationsV20161128.DisableAWSServiceAccess", handleOrgDisableServiceAccess)
	r.Register("AWSOrganizationsV20161128.ListAWSServiceAccessForOrganization", handleOrgListServiceAccess)

	// Resource policy.
	r.Register("AWSOrganizationsV20161128.PutResourcePolicy", handleOrgPutResourcePolicy)
	r.Register("AWSOrganizationsV20161128.DeleteResourcePolicy", handleOrgDeleteResourcePolicy)
	r.Register("AWSOrganizationsV20161128.DescribeResourcePolicy", handleOrgDescribeResourcePolicy)

	// Tags.
	r.Register("AWSOrganizationsV20161128.TagResource", handleOrgTagResource)
	r.Register("AWSOrganizationsV20161128.UntagResource", handleOrgUntagResource)
	r.Register("AWSOrganizationsV20161128.ListTagsForResource", handleOrgListTagsForResource)
}

// Helpers --------------------------------------------------------------------

func orgRootID() string { return "r-sim0" }
func orgEpoch() int64   { return time.Now().Unix() }
func orgAccountArn(id string) string {
	return "arn:aws:organizations::" + awsAccountID() + ":account/" + awsOrgID() + "/" + id
}
func orgOUArn(id string) string {
	return "arn:aws:organizations::" + awsAccountID() + ":ou/" + awsOrgID() + "/" + id
}
func orgRootArn(id string) string {
	return "arn:aws:organizations::" + awsAccountID() + ":root/" + awsOrgID() + "/" + id
}

// orgPolicyArn builds a policy's ARN. The model's PolicyArn pattern is an
// alternation of two shapes, and which one applies is whether AWS manages the
// policy: a customer policy is scoped to the account and the organization,
// while an AWS-managed policy belongs to no organization and carries the
// literal "aws" where an account would be. Only the AWS-managed arm admits an
// uppercase letter in the identifier, which is what "p-FullAWSAccess" needs.
func orgPolicyArn(id, typ string, awsManaged bool) string {
	if awsManaged {
		return "arn:aws:organizations::aws:policy/" + orgPolicyTypeSlug(typ) + "/" + id
	}
	return "arn:aws:organizations::" + awsAccountID() + ":policy/" + awsOrgID() + "/" + orgPolicyTypeSlug(typ) + "/" + id
}
func orgPolicyTypeSlug(typ string) string {
	return strings.ToLower(typ)
}

// orgHandshakeArn builds a handshake's ARN. The segment before the identifier
// names what the handshake is for, which the model spells as a variable
// ([a-z_]{1,32}) and never as a constant: an invitation and an
// enable-all-features handshake are the same resource type reached by
// different actions, and the ARN is where they differ.
func orgHandshakeArn(id, action string) string {
	return "arn:aws:organizations::" + awsAccountID() + ":handshake/" + awsOrgID() +
		"/" + strings.ToLower(action) + "/" + id
}
func orgResourcePolicyArn(id string) string {
	return "arn:aws:organizations::" + awsAccountID() + ":resourcepolicy/" + awsOrgID() + "/" + id
}

func orgRandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func orgNewAccountID() string {
	// 12-digit account ids; keep it numeric like the real service.
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	digits := make([]byte, 12)
	for i := 0; i < 12; i++ {
		digits[i] = '0' + (b[i%6] % 10)
	}
	if digits[0] == '0' {
		digits[0] = '1'
	}
	return string(digits)
}

func orgParentTypeFor(id string) string {
	if strings.HasPrefix(id, "r-") {
		return "ROOT"
	}
	return "ORGANIZATIONAL_UNIT"
}

// orgEnsureDefault materializes the default organization, its root, the
// AWS-managed FullAWSAccess SCP attached to the root, and the management
// account if none yet exists.
func orgEnsureDefault() {
	if _, ok := orgOrg.Get(orgSingletonKey); ok {
		return
	}
	acct := awsAccountID()
	orgOrg.Put(orgSingletonKey, OrgOrganization{
		Id:                 awsOrgID(),
		Arn:                awsOrgArn(),
		FeatureSet:         "ALL",
		MasterAccountId:    acct,
		MasterAccountArn:   orgAccountArn(acct),
		MasterAccountEmail: "management@sim.invalid",
	})
	orgRoots.Put(orgRootID(), OrgRoot{
		Id:                 orgRootID(),
		Arn:                orgRootArn(orgRootID()),
		Name:               "Root",
		EnabledPolicyTypes: nil,
	})
	orgAccounts.Put(acct, OrgAccount{
		Id:              acct,
		Arn:             orgAccountArn(acct),
		Email:           "management@sim.invalid",
		Name:            "Management",
		Status:          "ACTIVE",
		JoinedMethod:    "INVITED",
		JoinedTimestamp: 0,
		ParentId:        orgRootID(),
	})
	orgPolicies.Put(orgFullAWSAccessPolicyID, OrgPolicy{
		Id:          orgFullAWSAccessPolicyID,
		Arn:         orgPolicyArn(orgFullAWSAccessPolicyID, "SERVICE_CONTROL_POLICY", true),
		Name:        "FullAWSAccess",
		Description: "Allows access to every operation",
		Type:        "SERVICE_CONTROL_POLICY",
		AwsManaged:  true,
		Content:     `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`,
	})
	orgPolicyAttachments.Put(orgFullAWSAccessPolicyID+"|"+orgRootID(), OrgPolicyAttachment{
		PolicyId: orgFullAWSAccessPolicyID,
		TargetId: orgRootID(),
	})
}

// Response builders ----------------------------------------------------------

func orgOrgToMap(o OrgOrganization) map[string]any {
	return map[string]any{
		"Id":                 o.Id,
		"Arn":                o.Arn,
		"FeatureSet":         o.FeatureSet,
		"MasterAccountId":    o.MasterAccountId,
		"MasterAccountArn":   o.MasterAccountArn,
		"MasterAccountEmail": o.MasterAccountEmail,
		"AvailablePolicyTypes": []map[string]any{
			{"Type": "SERVICE_CONTROL_POLICY", "Status": "ENABLED"},
		},
	}
}

func orgAccountToMap(a OrgAccount) map[string]any {
	return map[string]any{
		"Id":              a.Id,
		"Arn":             a.Arn,
		"Email":           a.Email,
		"Name":            a.Name,
		"Status":          a.Status,
		"JoinedMethod":    a.JoinedMethod,
		"JoinedTimestamp": a.JoinedTimestamp,
	}
}

func orgRootToMap(r OrgRoot) map[string]any {
	policyTypes := []map[string]any{}
	for _, t := range r.EnabledPolicyTypes {
		policyTypes = append(policyTypes, map[string]any{"Type": t, "Status": "ENABLED"})
	}
	return map[string]any{
		"Id":          r.Id,
		"Arn":         r.Arn,
		"Name":        r.Name,
		"PolicyTypes": policyTypes,
	}
}

func orgOUToMap(o OrgOU) map[string]any {
	return map[string]any{
		"Id":   o.Id,
		"Arn":  o.Arn,
		"Name": o.Name,
	}
}

func orgPolicySummaryMap(p OrgPolicy) map[string]any {
	return map[string]any{
		"Id":          p.Id,
		"Arn":         p.Arn,
		"Name":        p.Name,
		"Description": p.Description,
		"Type":        p.Type,
		"AwsManaged":  p.AwsManaged,
	}
}

func orgPolicyToMap(p OrgPolicy) map[string]any {
	return map[string]any{
		"PolicySummary": orgPolicySummaryMap(p),
		"Content":       p.Content,
	}
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
		"Resources":           []map[string]any{},
	}
}

func orgCreateStatusToMap(s OrgCreateAccountStatus) map[string]any {
	m := map[string]any{
		"Id":                 s.Id,
		"AccountName":        s.AccountName,
		"State":              s.State,
		"RequestedTimestamp": s.RequestedTimestamp,
	}
	if s.CompletedTimestamp != 0 {
		m["CompletedTimestamp"] = s.CompletedTimestamp
	}
	if s.AccountId != "" {
		m["AccountId"] = s.AccountId
	}
	return m
}

// orgRequireOrg returns the organization, writing AWSOrganizationsNotInUseException
// if none exists. The default org always exists unless DeleteOrganization ran.
func orgRequireOrg(w http.ResponseWriter) (OrgOrganization, bool) {
	o, ok := orgOrg.Get(orgSingletonKey)
	if !ok {
		sim.AWSError(w, "AWSOrganizationsNotInUseException", "Your account is not a member of an organization.", http.StatusBadRequest)
		return OrgOrganization{}, false
	}
	return o, true
}

// Organization ops -----------------------------------------------------------

func handleOrgCreateOrganization(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FeatureSet string `json:"FeatureSet"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "The request body is not valid JSON.", http.StatusBadRequest)
		return
	}
	if _, ok := orgOrg.Get(orgSingletonKey); ok {
		// Real service raises this when the account already runs an org.
		sim.AWSError(w, "AlreadyInOrganizationException", "The provided account is already a member of an organization.", http.StatusBadRequest)
		return
	}
	feature := req.FeatureSet
	if feature == "" {
		feature = "ALL"
	}
	orgEnsureDefault()
	o, _ := orgOrg.Get(orgSingletonKey)
	o.FeatureSet = feature
	orgOrg.Put(orgSingletonKey, o)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Organization": orgOrgToMap(o)})
}

func handleOrgDeleteOrganization(w http.ResponseWriter, _ *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	orgOrg.Delete(orgSingletonKey)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgDescribeOrganization(w http.ResponseWriter, _ *http.Request) {
	o, ok := orgRequireOrg(w)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Organization": orgOrgToMap(o)})
}

func handleOrgEnableAllFeatures(w http.ResponseWriter, _ *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	// Real service starts a handshake the other accounts approve. The sim has a
	// single (management) account, so the handshake is created and returned.
	now := orgEpoch()
	h := OrgHandshake{
		Id:                  "h-" + orgRandHex(16),
		State:               "REQUESTED",
		RequestedTimestamp:  now,
		ExpirationTimestamp: now + 15*24*3600,
		Action:              "ENABLE_ALL_FEATURES",
		Parties: []OrgHandshakeParty{
			{Id: awsOrgID(), Type: "ORGANIZATION"},
		},
	}
	h.Arn = orgHandshakeArn(h.Id, h.Action)
	orgHandshakes.Put(h.Id, h)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(h)})
}

// Account ops ----------------------------------------------------------------

func handleOrgCreateAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		Email       string `json:"Email"`
		AccountName string `json:"AccountName"`
		RoleName    string `json:"RoleName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AccountName == "" || req.Email == "" {
		sim.AWSError(w, "InvalidInputException", "AccountName and Email are required", http.StatusBadRequest)
		return
	}
	now := orgEpoch()
	acctID := orgNewAccountID()
	// The account is created immediately (the request transitions straight to
	// SUCCEEDED); the real service is async but the sim has no provisioning lag.
	orgAccounts.Put(acctID, OrgAccount{
		Id:              acctID,
		Arn:             orgAccountArn(acctID),
		Email:           req.Email,
		Name:            req.AccountName,
		Status:          "ACTIVE",
		JoinedMethod:    "CREATED",
		JoinedTimestamp: now,
		ParentId:        orgRootID(),
	})
	status := OrgCreateAccountStatus{
		Id:                 "car-" + orgRandHex(16),
		AccountName:        req.AccountName,
		State:              "SUCCEEDED",
		RequestedTimestamp: now,
		CompletedTimestamp: now,
		AccountId:          acctID,
	}
	orgCreateStatuses.Put(status.Id, status)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CreateAccountStatus": orgCreateStatusToMap(status)})
}

func handleOrgDescribeAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountId string `json:"AccountId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := orgAccounts.Get(req.AccountId)
	if !ok {
		sim.AWSError(w, "AccountNotFoundException", "You specified an account that doesn't exist.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Account": orgAccountToMap(a)})
}

func handleOrgDescribeCreateAccountStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CreateAccountRequestId string `json:"CreateAccountRequestId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	s, ok := orgCreateStatuses.Get(req.CreateAccountRequestId)
	if !ok {
		sim.AWSError(w, "CreateAccountStatusNotFoundException", "We can't find a create account request with the specified id.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CreateAccountStatus": orgCreateStatusToMap(s)})
}

func handleOrgListCreateAccountStatus(w http.ResponseWriter, _ *http.Request) {
	statuses := orgCreateStatuses.List()
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Id < statuses[j].Id })
	out := []map[string]any{}
	for _, s := range statuses {
		out = append(out, orgCreateStatusToMap(s))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CreateAccountStatuses": out})
}

func handleOrgListAccounts(w http.ResponseWriter, _ *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	accts := orgAccounts.List()
	sort.Slice(accts, func(i, j int) bool { return accts[i].Id < accts[j].Id })
	out := []map[string]any{}
	for _, a := range accts {
		out = append(out, orgAccountToMap(a))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Accounts": out})
}

func handleOrgListAccountsForParent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentId string `json:"ParentId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ParentId == "" {
		sim.AWSError(w, "InvalidInputException", "ParentId is required", http.StatusBadRequest)
		return
	}
	accts := orgAccounts.Filter(func(a OrgAccount) bool { return a.ParentId == req.ParentId })
	sort.Slice(accts, func(i, j int) bool { return accts[i].Id < accts[j].Id })
	out := []map[string]any{}
	for _, a := range accts {
		out = append(out, orgAccountToMap(a))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Accounts": out})
}

func handleOrgMoveAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountId           string `json:"AccountId"`
		SourceParentId      string `json:"SourceParentId"`
		DestinationParentId string `json:"DestinationParentId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := orgAccounts.Get(req.AccountId)
	if !ok {
		sim.AWSError(w, "AccountNotFoundException", "You specified an account that doesn't exist.", http.StatusBadRequest)
		return
	}
	if !orgParentExists(req.DestinationParentId) {
		sim.AWSError(w, "DestinationParentNotFoundException", "We can't find the destination container (a root or OU) with the specified parent ID.", http.StatusBadRequest)
		return
	}
	if a.ParentId != req.SourceParentId {
		sim.AWSError(w, "SourceParentNotFoundException", "We can't find a source root or OU with the specified parent ID.", http.StatusBadRequest)
		return
	}
	a.ParentId = req.DestinationParentId
	orgAccounts.Put(a.Id, a)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgRemoveAccountFromOrganization(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountId string `json:"AccountId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AccountId == awsAccountID() {
		sim.AWSError(w, "MasterCannotLeaveOrganizationException", "You can't remove the management account from the organization.", http.StatusBadRequest)
		return
	}
	if _, ok := orgAccounts.Get(req.AccountId); !ok {
		sim.AWSError(w, "AccountNotFoundException", "You specified an account that doesn't exist.", http.StatusBadRequest)
		return
	}
	orgAccounts.Delete(req.AccountId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgCloseAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountId string `json:"AccountId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	a, ok := orgAccounts.Get(req.AccountId)
	if !ok {
		sim.AWSError(w, "AccountNotFoundException", "You specified an account that doesn't exist.", http.StatusBadRequest)
		return
	}
	if req.AccountId == awsAccountID() {
		sim.AWSError(w, "ConstraintViolationException", "You can't close the management account.", http.StatusBadRequest)
		return
	}
	a.Status = "SUSPENDED"
	orgAccounts.Put(a.Id, a)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// OU ops ---------------------------------------------------------------------

func orgParentExists(id string) bool {
	if id == orgRootID() {
		return true
	}
	if _, ok := orgRoots.Get(id); ok {
		return true
	}
	_, ok := orgOUs.Get(id)
	return ok
}

func handleOrgCreateOU(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		ParentId string `json:"ParentId"`
		Name     string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.ParentId == "" {
		sim.AWSError(w, "InvalidInputException", "Name and ParentId are required", http.StatusBadRequest)
		return
	}
	if !orgParentExists(req.ParentId) {
		sim.AWSError(w, "ParentNotFoundException", "We can't find a root or OU with the ParentId that you specified.", http.StatusBadRequest)
		return
	}
	ou := OrgOU{
		Id:       "ou-" + strings.TrimPrefix(orgRootID(), "r-") + "-" + orgRandHex(4),
		Name:     req.Name,
		ParentId: req.ParentId,
	}
	ou.Arn = orgOUArn(ou.Id)
	orgOUs.Put(ou.Id, ou)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"OrganizationalUnit": orgOUToMap(ou)})
}

func handleOrgDeleteOU(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrganizationalUnitId string `json:"OrganizationalUnitId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := orgOUs.Get(req.OrganizationalUnitId); !ok {
		sim.AWSError(w, "OrganizationalUnitNotFoundException", "We can't find an OU with the OrganizationalUnitId that you specified.", http.StatusBadRequest)
		return
	}
	// Reject deletion of a non-empty OU like the real service.
	if orgOUHasChildren(req.OrganizationalUnitId) {
		sim.AWSError(w, "OrganizationalUnitNotEmptyException", "The specified OU is not empty.", http.StatusBadRequest)
		return
	}
	orgOUs.Delete(req.OrganizationalUnitId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func orgOUHasChildren(id string) bool {
	for _, a := range orgAccounts.List() {
		if a.ParentId == id {
			return true
		}
	}
	for _, o := range orgOUs.List() {
		if o.ParentId == id {
			return true
		}
	}
	return false
}

func handleOrgDescribeOU(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrganizationalUnitId string `json:"OrganizationalUnitId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	ou, ok := orgOUs.Get(req.OrganizationalUnitId)
	if !ok {
		sim.AWSError(w, "OrganizationalUnitNotFoundException", "We can't find an OU with the OrganizationalUnitId that you specified.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"OrganizationalUnit": orgOUToMap(ou)})
}

func handleOrgUpdateOU(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrganizationalUnitId string `json:"OrganizationalUnitId"`
		Name                 string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	ou, ok := orgOUs.Get(req.OrganizationalUnitId)
	if !ok {
		sim.AWSError(w, "OrganizationalUnitNotFoundException", "We can't find an OU with the OrganizationalUnitId that you specified.", http.StatusBadRequest)
		return
	}
	if req.Name != "" {
		ou.Name = req.Name
	}
	orgOUs.Put(ou.Id, ou)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"OrganizationalUnit": orgOUToMap(ou)})
}

func handleOrgListOUsForParent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentId string `json:"ParentId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ParentId == "" {
		sim.AWSError(w, "InvalidInputException", "ParentId is required", http.StatusBadRequest)
		return
	}
	ous := orgOUs.Filter(func(o OrgOU) bool { return o.ParentId == req.ParentId })
	sort.Slice(ous, func(i, j int) bool { return ous[i].Id < ous[j].Id })
	out := []map[string]any{}
	for _, o := range ous {
		out = append(out, orgOUToMap(o))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"OrganizationalUnits": out})
}

// Tree navigation ------------------------------------------------------------

func handleOrgListRoots(w http.ResponseWriter, _ *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	roots := orgRoots.List()
	sort.Slice(roots, func(i, j int) bool { return roots[i].Id < roots[j].Id })
	out := []map[string]any{}
	for _, r := range roots {
		out = append(out, orgRootToMap(r))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Roots": out})
}

func handleOrgListChildren(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentId  string `json:"ParentId"`
		ChildType string `json:"ChildType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ParentId == "" || req.ChildType == "" {
		sim.AWSError(w, "InvalidInputException", "ParentId and ChildType are required", http.StatusBadRequest)
		return
	}
	out := []map[string]any{}
	switch req.ChildType {
	case "ACCOUNT":
		accts := orgAccounts.Filter(func(a OrgAccount) bool { return a.ParentId == req.ParentId })
		sort.Slice(accts, func(i, j int) bool { return accts[i].Id < accts[j].Id })
		for _, a := range accts {
			out = append(out, map[string]any{"Id": a.Id, "Type": "ACCOUNT"})
		}
	case "ORGANIZATIONAL_UNIT":
		ous := orgOUs.Filter(func(o OrgOU) bool { return o.ParentId == req.ParentId })
		sort.Slice(ous, func(i, j int) bool { return ous[i].Id < ous[j].Id })
		for _, o := range ous {
			out = append(out, map[string]any{"Id": o.Id, "Type": "ORGANIZATIONAL_UNIT"})
		}
	default:
		sim.AWSError(w, "InvalidInputException", "ChildType must be ACCOUNT or ORGANIZATIONAL_UNIT", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Children": out})
}

func handleOrgListParents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChildId string `json:"ChildId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var parentID string
	if a, ok := orgAccounts.Get(req.ChildId); ok {
		parentID = a.ParentId
	} else if o, ok := orgOUs.Get(req.ChildId); ok {
		parentID = o.ParentId
	} else {
		sim.AWSError(w, "ChildNotFoundException", "We can't find an organizational unit (OU) or account with the ChildId that you specified.", http.StatusBadRequest)
		return
	}
	out := []map[string]any{
		{"Id": parentID, "Type": orgParentTypeFor(parentID)},
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Parents": out})
}

// Policy ops -----------------------------------------------------------------

func handleOrgCreatePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		Content     string `json:"Content"`
		Description string `json:"Description"`
		Name        string `json:"Name"`
		Type        string `json:"Type"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Type == "" || req.Content == "" {
		sim.AWSError(w, "InvalidInputException", "Name, Type and Content are required", http.StatusBadRequest)
		return
	}
	p := OrgPolicy{
		Id:          "p-" + orgRandHex(8),
		Name:        req.Name,
		Description: req.Description,
		Type:        req.Type,
		AwsManaged:  false,
		Content:     req.Content,
	}
	p.Arn = orgPolicyArn(p.Id, p.Type, p.AwsManaged)
	orgPolicies.Put(p.Id, p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Policy": orgPolicyToMap(p)})
}

func handleOrgDeletePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyId string `json:"PolicyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	p, ok := orgPolicies.Get(req.PolicyId)
	if !ok {
		sim.AWSError(w, "PolicyNotFoundException", "We can't find a policy with the PolicyId that you specified.", http.StatusBadRequest)
		return
	}
	if p.AwsManaged {
		sim.AWSError(w, "ConstraintViolationException", "You can't delete an AWS managed policy.", http.StatusBadRequest)
		return
	}
	if orgPolicyHasTargets(req.PolicyId) {
		sim.AWSError(w, "PolicyInUseException", "The policy is attached to one or more entities. Detach it first.", http.StatusBadRequest)
		return
	}
	orgPolicies.Delete(req.PolicyId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func orgPolicyHasTargets(policyID string) bool {
	for _, at := range orgPolicyAttachments.List() {
		if at.PolicyId == policyID {
			return true
		}
	}
	return false
}

func handleOrgDescribePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyId string `json:"PolicyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	p, ok := orgPolicies.Get(req.PolicyId)
	if !ok {
		sim.AWSError(w, "PolicyNotFoundException", "We can't find a policy with the PolicyId that you specified.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Policy": orgPolicyToMap(p)})
}

func handleOrgUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyId    string  `json:"PolicyId"`
		Name        *string `json:"Name"`
		Description *string `json:"Description"`
		Content     *string `json:"Content"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	p, ok := orgPolicies.Get(req.PolicyId)
	if !ok {
		sim.AWSError(w, "PolicyNotFoundException", "We can't find a policy with the PolicyId that you specified.", http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Description != nil {
		p.Description = *req.Description
	}
	if req.Content != nil {
		p.Content = *req.Content
	}
	orgPolicies.Put(p.Id, p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Policy": orgPolicyToMap(p)})
}

func orgAttachmentKey(policyID, targetID string) string { return policyID + "|" + targetID }

// orgPolicyTypeEnabled reports whether a policy of this type may govern a
// target: SERVICE_CONTROL_POLICY is enabled in every root, and every other
// type has to be enabled with EnablePolicyType first.
func orgPolicyTypeEnabled(policyType string) bool {
	if policyType == "" || policyType == "SERVICE_CONTROL_POLICY" {
		return true
	}
	for _, root := range orgRoots.List() {
		for _, enabled := range root.EnabledPolicyTypes {
			if enabled == policyType {
				return true
			}
		}
	}
	return false
}

func handleOrgAttachPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyId string `json:"PolicyId"`
		TargetId string `json:"TargetId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := orgPolicies.Get(req.PolicyId); !ok {
		sim.AWSError(w, "PolicyNotFoundException", "We can't find a policy with the PolicyId that you specified.", http.StatusBadRequest)
		return
	}
	if !orgTargetExists(req.TargetId) {
		sim.AWSError(w, "TargetNotFoundException", "We can't find a root, OU, or account with the TargetId that you specified.", http.StatusBadRequest)
		return
	}
	// A policy governs a target only if its type is enabled in the root, so
	// attaching one of a type nobody enabled is refused rather than stored:
	// stored, it would resolve through DescribeEffectivePolicy as though it
	// governed the target, which is a policy decision the organization never
	// made. Service control policies are enabled in every root by default.
	policy, _ := orgPolicies.Get(req.PolicyId)
	if !orgPolicyTypeEnabled(policy.Type) {
		sim.AWSError(w, "PolicyTypeNotEnabledException", "The specified policy type isn't currently enabled in this root.", http.StatusBadRequest)
		return
	}
	key := orgAttachmentKey(req.PolicyId, req.TargetId)
	if _, ok := orgPolicyAttachments.Get(key); ok {
		sim.AWSError(w, "DuplicatePolicyAttachmentException", "The selected policy is already attached to the specified target.", http.StatusBadRequest)
		return
	}
	orgPolicyAttachments.Put(key, OrgPolicyAttachment{PolicyId: req.PolicyId, TargetId: req.TargetId})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func orgTargetExists(id string) bool {
	if orgParentExists(id) {
		return true
	}
	_, ok := orgAccounts.Get(id)
	return ok
}

func handleOrgDetachPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyId string `json:"PolicyId"`
		TargetId string `json:"TargetId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := orgAttachmentKey(req.PolicyId, req.TargetId)
	if _, ok := orgPolicyAttachments.Get(key); !ok {
		sim.AWSError(w, "PolicyNotAttachedException", "The policy isn't attached to the specified target.", http.StatusBadRequest)
		return
	}
	orgPolicyAttachments.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgListPolicies(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		Filter string `json:"Filter"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Filter == "" {
		sim.AWSError(w, "InvalidInputException", "Filter is required", http.StatusBadRequest)
		return
	}
	pols := orgPolicies.Filter(func(p OrgPolicy) bool { return p.Type == req.Filter })
	sort.Slice(pols, func(i, j int) bool { return pols[i].Id < pols[j].Id })
	out := []map[string]any{}
	for _, p := range pols {
		out = append(out, orgPolicySummaryMap(p))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Policies": out})
}

func handleOrgListPoliciesForTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetId string `json:"TargetId"`
		Filter   string `json:"Filter"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TargetId == "" || req.Filter == "" {
		sim.AWSError(w, "InvalidInputException", "TargetId and Filter are required", http.StatusBadRequest)
		return
	}
	if !orgTargetExists(req.TargetId) {
		sim.AWSError(w, "TargetNotFoundException", "We can't find a root, OU, or account with the TargetId that you specified.", http.StatusBadRequest)
		return
	}
	matched := []OrgPolicy{}
	for _, at := range orgPolicyAttachments.List() {
		if at.TargetId != req.TargetId {
			continue
		}
		p, ok := orgPolicies.Get(at.PolicyId)
		if !ok || p.Type != req.Filter {
			continue
		}
		matched = append(matched, p)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Id < matched[j].Id })
	out := []map[string]any{}
	for _, p := range matched {
		out = append(out, orgPolicySummaryMap(p))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Policies": out})
}

func handleOrgListTargetsForPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyId string `json:"PolicyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := orgPolicies.Get(req.PolicyId); !ok {
		sim.AWSError(w, "PolicyNotFoundException", "We can't find a policy with the PolicyId that you specified.", http.StatusBadRequest)
		return
	}
	targetIDs := []string{}
	for _, at := range orgPolicyAttachments.List() {
		if at.PolicyId != req.PolicyId {
			continue
		}
		targetIDs = append(targetIDs, at.TargetId)
	}
	sort.Strings(targetIDs)
	out := []map[string]any{}
	for _, tid := range targetIDs {
		out = append(out, orgPolicyTargetSummary(tid))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Targets": out})
}

func orgPolicyTargetSummary(targetID string) map[string]any {
	var name, arn, typ string
	switch {
	case targetID == orgRootID():
		if r, ok := orgRoots.Get(targetID); ok {
			name, arn, typ = r.Name, r.Arn, "ROOT"
		}
	case strings.HasPrefix(targetID, "r-"):
		if r, ok := orgRoots.Get(targetID); ok {
			name, arn, typ = r.Name, r.Arn, "ROOT"
		}
	case strings.HasPrefix(targetID, "ou-"):
		if o, ok := orgOUs.Get(targetID); ok {
			name, arn, typ = o.Name, o.Arn, "ORGANIZATIONAL_UNIT"
		}
	default:
		if a, ok := orgAccounts.Get(targetID); ok {
			name, arn, typ = a.Name, a.Arn, "ACCOUNT"
		}
	}
	return map[string]any{
		"TargetId": targetID,
		"Arn":      arn,
		"Name":     name,
		"Type":     typ,
	}
}

func handleOrgEnablePolicyType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootId     string `json:"RootId"`
		PolicyType string `json:"PolicyType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	root, ok := orgRoots.Get(req.RootId)
	if !ok {
		sim.AWSError(w, "RootNotFoundException", "We can't find a root with the RootId that you specified.", http.StatusBadRequest)
		return
	}
	for _, t := range root.EnabledPolicyTypes {
		if t == req.PolicyType {
			sim.AWSError(w, "PolicyTypeAlreadyEnabledException", "The specified policy type is already enabled.", http.StatusBadRequest)
			return
		}
	}
	root.EnabledPolicyTypes = append(root.EnabledPolicyTypes, req.PolicyType)
	orgRoots.Put(root.Id, root)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Root": orgRootToMap(root)})
}

func handleOrgDisablePolicyType(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootId     string `json:"RootId"`
		PolicyType string `json:"PolicyType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	root, ok := orgRoots.Get(req.RootId)
	if !ok {
		sim.AWSError(w, "RootNotFoundException", "We can't find a root with the RootId that you specified.", http.StatusBadRequest)
		return
	}
	found := false
	kept := root.EnabledPolicyTypes[:0:0]
	for _, t := range root.EnabledPolicyTypes {
		if t == req.PolicyType {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		sim.AWSError(w, "PolicyTypeNotEnabledException", "The specified policy type isn't currently enabled in this root.", http.StatusBadRequest)
		return
	}
	root.EnabledPolicyTypes = kept
	orgRoots.Put(root.Id, root)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Root": orgRootToMap(root)})
}

func handleOrgDescribeEffectivePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyType string `json:"PolicyType"`
		TargetId   string `json:"TargetId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PolicyType == "" {
		sim.AWSError(w, "InvalidInputException", "PolicyType is required", http.StatusBadRequest)
		return
	}
	if !orgEffectivePolicyTypes[req.PolicyType] {
		// EffectivePolicyType is a narrower set than PolicyType. A service
		// control policy has no effective form — it is evaluated as an
		// intersection along the path at authorization time rather than merged
		// into one document — so asking for one is a validation failure, not an
		// empty answer.
		sim.AWSErrorf(w, "InvalidInputException", http.StatusBadRequest,
			"1 validation error detected: Value '%s' at 'policyType' failed to satisfy constraint: Member must satisfy enum value set: [%s]",
			req.PolicyType, strings.Join(orgEffectivePolicyTypeNames, ", "))
		return
	}
	targetID := req.TargetId
	if targetID == "" {
		targetID = awsAccountID()
	}
	if !orgTargetExists(targetID) {
		sim.AWSError(w, "TargetNotFoundException", "We can't find a root, OU, or account with the TargetId that you specified.", http.StatusBadRequest)
		return
	}
	// The effective policy is the one this type resolves to for the target:
	// the nearest attachment walking from the target up to the root. A target
	// with no attachment of the type anywhere on its path has no effective
	// policy at all, which is its own error below.
	content := orgEffectivePolicyContent(targetID, req.PolicyType)
	if content == "" {
		sim.AWSError(w, "EffectivePolicyNotFoundException", "If you ran this action on the management account, this policy type is not enabled. If you ran the action on a member account, the account doesn't have an effective policy of this type.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"EffectivePolicy": map[string]any{
			"PolicyContent":        content,
			"LastUpdatedTimestamp": orgEpoch(),
			"TargetId":             targetID,
			"PolicyType":           req.PolicyType,
		},
	})
}

// orgEffectivePolicyTypeNames is the EffectivePolicyType enum — the policy
// types that resolve to a single effective document for a target. It is
// deliberately smaller than the PolicyType enum, which also carries
// SERVICE_CONTROL_POLICY and RESOURCE_CONTROL_POLICY.
var orgEffectivePolicyTypeNames = []string{
	"TAG_POLICY",
	"BACKUP_POLICY",
	"AISERVICES_OPT_OUT_POLICY",
	"CHATBOT_POLICY",
	"DECLARATIVE_POLICY_EC2",
	"SECURITYHUB_POLICY",
	"INSPECTOR_POLICY",
	"UPGRADE_ROLLOUT_POLICY",
	"BEDROCK_POLICY",
	"S3_POLICY",
	"NETWORK_SECURITY_DIRECTOR_POLICY",
}

var orgEffectivePolicyTypes = func() map[string]bool {
	set := make(map[string]bool, len(orgEffectivePolicyTypeNames))
	for _, name := range orgEffectivePolicyTypeNames {
		set[name] = true
	}
	return set
}()

func orgEffectivePolicyContent(targetID, policyType string) string {
	// Walk from the target up to the root, collecting the first policy content
	// of this type attached along the way.
	for _, id := range orgPathToRoot(targetID) {
		for _, at := range orgPolicyAttachments.List() {
			if at.TargetId != id {
				continue
			}
			p, ok := orgPolicies.Get(at.PolicyId)
			if ok && p.Type == policyType {
				return p.Content
			}
		}
	}
	return ""
}

func orgPathToRoot(targetID string) []string {
	path := []string{targetID}
	cur := targetID
	for i := 0; i < 64; i++ {
		var parent string
		if a, ok := orgAccounts.Get(cur); ok {
			parent = a.ParentId
		} else if o, ok := orgOUs.Get(cur); ok {
			parent = o.ParentId
		} else {
			break
		}
		if parent == "" {
			break
		}
		path = append(path, parent)
		cur = parent
		if strings.HasPrefix(parent, "r-") {
			break
		}
	}
	return path
}

// Handshake ops --------------------------------------------------------------

func handleOrgInviteAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		Target struct {
			Id   string `json:"Id"`
			Type string `json:"Type"`
		} `json:"Target"`
		Notes string `json:"Notes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Target.Id == "" || req.Target.Type == "" {
		sim.AWSError(w, "InvalidInputException", "Target Id and Type are required", http.StatusBadRequest)
		return
	}
	now := orgEpoch()
	h := OrgHandshake{
		Id:                  "h-" + orgRandHex(16),
		State:               "OPEN",
		RequestedTimestamp:  now,
		ExpirationTimestamp: now + 15*24*3600,
		Action:              "INVITE",
		Parties: []OrgHandshakeParty{
			{Id: awsOrgID(), Type: "ORGANIZATION"},
			{Id: req.Target.Id, Type: req.Target.Type},
		},
	}
	h.Arn = orgHandshakeArn(h.Id, h.Action)
	orgHandshakes.Put(h.Id, h)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(h)})
}

func orgHandshakeTransition(w http.ResponseWriter, r *http.Request, newState string) {
	var req struct {
		HandshakeId string `json:"HandshakeId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	h, ok := orgHandshakes.Get(req.HandshakeId)
	if !ok {
		sim.AWSError(w, "HandshakeNotFoundException", "We can't find a handshake with the HandshakeId that you specified.", http.StatusBadRequest)
		return
	}
	h.State = newState
	orgHandshakes.Put(h.Id, h)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(h)})
}

func handleOrgAcceptHandshake(w http.ResponseWriter, r *http.Request) {
	orgHandshakeTransition(w, r, "ACCEPTED")
}
func handleOrgDeclineHandshake(w http.ResponseWriter, r *http.Request) {
	orgHandshakeTransition(w, r, "DECLINED")
}
func handleOrgCancelHandshake(w http.ResponseWriter, r *http.Request) {
	orgHandshakeTransition(w, r, "CANCELED")
}

func handleOrgDescribeHandshake(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HandshakeId string `json:"HandshakeId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	h, ok := orgHandshakes.Get(req.HandshakeId)
	if !ok {
		sim.AWSError(w, "HandshakeNotFoundException", "We can't find a handshake with the HandshakeId that you specified.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshake": orgHandshakeToMap(h)})
}

func handleOrgListHandshakesForAccount(w http.ResponseWriter, _ *http.Request) {
	hs := orgHandshakes.List()
	sort.Slice(hs, func(i, j int) bool { return hs[i].Id < hs[j].Id })
	out := []map[string]any{}
	for _, h := range hs {
		out = append(out, orgHandshakeToMap(h))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshakes": out})
}

func handleOrgListHandshakesForOrganization(w http.ResponseWriter, _ *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	hs := orgHandshakes.List()
	sort.Slice(hs, func(i, j int) bool { return hs[i].Id < hs[j].Id })
	out := []map[string]any{}
	for _, h := range hs {
		out = append(out, orgHandshakeToMap(h))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Handshakes": out})
}

// Delegated admin & service access ------------------------------------------

func orgDelegatedAdminKey(accountID, sp string) string { return accountID + "|" + sp }

func handleOrgRegisterDelegatedAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountId        string `json:"AccountId"`
		ServicePrincipal string `json:"ServicePrincipal"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AccountId == "" || req.ServicePrincipal == "" {
		sim.AWSError(w, "InvalidInputException", "AccountId and ServicePrincipal are required", http.StatusBadRequest)
		return
	}
	if _, ok := orgAccounts.Get(req.AccountId); !ok {
		sim.AWSError(w, "AccountNotFoundException", "You specified an account that doesn't exist.", http.StatusBadRequest)
		return
	}
	key := orgDelegatedAdminKey(req.AccountId, req.ServicePrincipal)
	if _, ok := orgDelegatedAdmins.Get(key); ok {
		sim.AWSError(w, "AccountAlreadyRegisteredException", "The specified account is already a delegated administrator for this AWS service.", http.StatusBadRequest)
		return
	}
	orgDelegatedAdmins.Put(key, OrgDelegatedAdmin{
		AccountId:             req.AccountId,
		ServicePrincipal:      req.ServicePrincipal,
		DelegationEnabledDate: orgEpoch(),
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgDeregisterDelegatedAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountId        string `json:"AccountId"`
		ServicePrincipal string `json:"ServicePrincipal"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := orgDelegatedAdminKey(req.AccountId, req.ServicePrincipal)
	if _, ok := orgDelegatedAdmins.Get(key); !ok {
		sim.AWSError(w, "AccountNotRegisteredException", "The specified account is not a delegated administrator for this AWS service.", http.StatusBadRequest)
		return
	}
	orgDelegatedAdmins.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgListDelegatedAdmins(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServicePrincipal string `json:"ServicePrincipal"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "The request body is not valid JSON.", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	out := []map[string]any{}
	admins := orgDelegatedAdmins.List()
	sort.Slice(admins, func(i, j int) bool { return admins[i].AccountId < admins[j].AccountId })
	for _, da := range admins {
		if req.ServicePrincipal != "" && da.ServicePrincipal != req.ServicePrincipal {
			continue
		}
		if seen[da.AccountId] {
			continue
		}
		seen[da.AccountId] = true
		acct, ok := orgAccounts.Get(da.AccountId)
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"Id":                    acct.Id,
			"Arn":                   acct.Arn,
			"Email":                 acct.Email,
			"Name":                  acct.Name,
			"Status":                acct.Status,
			"JoinedMethod":          acct.JoinedMethod,
			"JoinedTimestamp":       acct.JoinedTimestamp,
			"DelegationEnabledDate": da.DelegationEnabledDate,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"DelegatedAdministrators": out})
}

func handleOrgListDelegatedServices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountId string `json:"AccountId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := orgAccounts.Get(req.AccountId); !ok {
		sim.AWSError(w, "AccountNotFoundException", "You specified an account that doesn't exist.", http.StatusBadRequest)
		return
	}
	out := []map[string]any{}
	admins := orgDelegatedAdmins.Filter(func(da OrgDelegatedAdmin) bool { return da.AccountId == req.AccountId })
	sort.Slice(admins, func(i, j int) bool { return admins[i].ServicePrincipal < admins[j].ServicePrincipal })
	for _, da := range admins {
		out = append(out, map[string]any{
			"ServicePrincipal":      da.ServicePrincipal,
			"DelegationEnabledDate": da.DelegationEnabledDate,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"DelegatedServices": out})
}

func handleOrgEnableServiceAccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServicePrincipal string `json:"ServicePrincipal"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServicePrincipal == "" {
		sim.AWSError(w, "InvalidInputException", "ServicePrincipal is required", http.StatusBadRequest)
		return
	}
	if _, ok := orgServiceAccess.Get(req.ServicePrincipal); !ok {
		orgServiceAccess.Put(req.ServicePrincipal, OrgServiceAccess{
			ServicePrincipal: req.ServicePrincipal,
			DateEnabled:      orgEpoch(),
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgDisableServiceAccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServicePrincipal string `json:"ServicePrincipal"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	orgServiceAccess.Delete(req.ServicePrincipal)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgListServiceAccess(w http.ResponseWriter, _ *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	sps := orgServiceAccess.List()
	sort.Slice(sps, func(i, j int) bool { return sps[i].ServicePrincipal < sps[j].ServicePrincipal })
	out := []map[string]any{}
	for _, sp := range sps {
		out = append(out, map[string]any{
			"ServicePrincipal": sp.ServicePrincipal,
			"DateEnabled":      sp.DateEnabled,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"EnabledServicePrincipals": out})
}

// Resource policy ------------------------------------------------------------

func handleOrgPutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	var req struct {
		Content string `json:"Content"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		sim.AWSError(w, "InvalidInputException", "Content is required", http.StatusBadRequest)
		return
	}
	rp, ok := orgResourcePolicies.Get(orgSingletonKey)
	if !ok {
		rp = OrgResourcePolicy{Id: "rp-" + orgRandHex(8)}
		rp.Arn = orgResourcePolicyArn(rp.Id)
	}
	rp.Content = req.Content
	orgResourcePolicies.Put(orgSingletonKey, rp)
	iamPutResourcePolicy(rp.Arn, req.Content)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ResourcePolicy": map[string]any{
			"ResourcePolicySummary": map[string]any{"Id": rp.Id, "Arn": rp.Arn},
			"Content":               rp.Content,
		},
	})
}

func handleOrgDeleteResourcePolicy(w http.ResponseWriter, _ *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	rp, ok := orgResourcePolicies.Get(orgSingletonKey)
	if !ok {
		sim.AWSError(w, "ResourcePolicyNotFoundException", "We can't find a resource policy request with the parameter that you specified.", http.StatusBadRequest)
		return
	}
	iamDeleteResourcePolicy(rp.Arn)
	orgResourcePolicies.Delete(orgSingletonKey)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgDescribeResourcePolicy(w http.ResponseWriter, _ *http.Request) {
	if _, ok := orgRequireOrg(w); !ok {
		return
	}
	rp, ok := orgResourcePolicies.Get(orgSingletonKey)
	if !ok {
		sim.AWSError(w, "ResourcePolicyNotFoundException", "We can't find a resource policy request with the parameter that you specified.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ResourcePolicy": map[string]any{
			"ResourcePolicySummary": map[string]any{"Id": rp.Id, "Arn": rp.Arn},
			"Content":               rp.Content,
		},
	})
}

// Tags -----------------------------------------------------------------------

func handleOrgTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceId string `json:"ResourceId"`
		Tags       []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceId == "" {
		sim.AWSError(w, "InvalidInputException", "ResourceId is required", http.StatusBadRequest)
		return
	}
	t, ok := orgTags.Get(req.ResourceId)
	if !ok {
		t = OrgTags{ResourceId: req.ResourceId, Tags: map[string]string{}}
	}
	if t.Tags == nil {
		t.Tags = map[string]string{}
	}
	for _, tag := range req.Tags {
		t.Tags[tag.Key] = tag.Value
	}
	orgTags.Put(req.ResourceId, t)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceId string   `json:"ResourceId"`
		TagKeys    []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if t, ok := orgTags.Get(req.ResourceId); ok {
		for _, k := range req.TagKeys {
			delete(t.Tags, k)
		}
		orgTags.Put(req.ResourceId, t)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleOrgListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceId string `json:"ResourceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidInputException", "Invalid request body", http.StatusBadRequest)
		return
	}
	out := []map[string]any{}
	if t, ok := orgTags.Get(req.ResourceId); ok {
		keys := make([]string, 0, len(t.Tags))
		for k := range t.Tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, map[string]any{"Key": k, "Value": t.Tags[k]})
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Tags": out})
}

package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Compute the account summary, authorization details and credential report from
// the IAM stores, never fabricated counts. Derive service-last-accessed rows from
// the service namespaces of the principal's policies.

// AWS returns a JobId from a Generate* call and the Get* call reports JobStatus
// with the rows. The simulator settles each job COMPLETED at once and records the
// principal or entity path the Generate* call named, from which Get* derives the
// rows.
type IAMServiceJob struct {
	JobId      string
	JobType    string // SERVICE_LEVEL or ACTION_LEVEL (service-last-accessed); empty for org reports
	Arn        string // principal ARN (service-last-accessed)
	EntityPath string // Organizations entity path (org access report)
	PolicyId   string // Organizations policy id (org access report)
	CreateDate string
}

// IAMDelegationRequest is a cross-account delegation request with a status
// lifecycle. CreateDelegationRequest creates it PENDING_APPROVAL;
// Accept/RejectDelegationRequest move it to ACCEPTED/REJECTED.
type IAMDelegationRequest struct {
	DelegationRequestId string
	OwnerAccountId      string
	Description         string
	RequestMessage      string
	PolicyTemplateArn   string
	State               string // PENDING_APPROVAL → ACCEPTED / REJECTED / ASSIGNED / FINALIZED
	SessionDuration     int
	RedirectUrl         string
	OnlySendByOwner     bool
	Notes               string
	RejectionReason     string
	CreateDate          string
	UpdatedTime         string
	ExpirationTime      string
}

// One row per feature key: the Organizations root toggles, the outbound web
// identity federation state and the STS global endpoint token version.
type IAMAccountFeature struct {
	Key   string
	Value string
}

var (
	iamServiceJobs   sim.Store[IAMServiceJob]
	iamDelegations   sim.Store[IAMDelegationRequest]
	iamAccountFlags  sim.Store[IAMAccountFeature]
	iamCredReportGen sim.Store[IAMAccountFeature] // tracks GenerateCredentialReport state by a fixed key
)

func registerIAMAccountReports(r *AWSQueryRouter, srv *sim.Server) {
	for action, h := range map[string]http.HandlerFunc{
		"GetAccountSummary":                             handleIAMGetAccountSummary,
		"GetAccountAuthorizationDetails":                handleIAMGetAccountAuthorizationDetails,
		"GenerateCredentialReport":                      handleIAMGenerateCredentialReport,
		"GetCredentialReport":                           handleIAMGetCredentialReport,
		"GenerateServiceLastAccessedDetails":            handleIAMGenerateServiceLastAccessed,
		"GetServiceLastAccessedDetails":                 handleIAMGetServiceLastAccessed,
		"GetServiceLastAccessedDetailsWithEntities":     handleIAMGetServiceLastAccessedWithEntities,
		"GenerateOrganizationsAccessReport":             handleIAMGenerateOrganizationsAccessReport,
		"GetOrganizationsAccessReport":                  handleIAMGetOrganizationsAccessReport,
		"EnableOrganizationsRootCredentialsManagement":  handleIAMEnableOrgRootCredentials,
		"DisableOrganizationsRootCredentialsManagement": handleIAMDisableOrgRootCredentials,
		"EnableOrganizationsRootSessions":               handleIAMEnableOrgRootSessions,
		"DisableOrganizationsRootSessions":              handleIAMDisableOrgRootSessions,
		"ListOrganizationsFeatures":                     handleIAMListOrganizationsFeatures,
		"CreateDelegationRequest":                       handleIAMCreateDelegationRequest,
		"GetDelegationRequest":                          handleIAMGetDelegationRequest,
		"ListDelegationRequests":                        handleIAMListDelegationRequests,
		"AcceptDelegationRequest":                       handleIAMAcceptDelegationRequest,
		"RejectDelegationRequest":                       handleIAMRejectDelegationRequest,
		"AssociateDelegationRequest":                    handleIAMAssociateDelegationRequest,
		"UpdateDelegationRequest":                       handleIAMUpdateDelegationRequest,
		"SendDelegationToken":                           handleIAMSendDelegationToken,
		"EnableOutboundWebIdentityFederation":           handleIAMEnableOutboundWebIdentityFederation,
		"DisableOutboundWebIdentityFederation":          handleIAMDisableOutboundWebIdentityFederation,
		"GetOutboundWebIdentityFederationInfo":          handleIAMGetOutboundWebIdentityFederationInfo,
		"SetSecurityTokenServicePreferences":            handleIAMSetSecurityTokenServicePreferences,
		"GetHumanReadableSummary":                       handleIAMGetHumanReadableSummary,
	} {
		r.Register(action, h)
	}
	iamServiceJobs = sim.MakeStore[IAMServiceJob](srv.DB(), "iam_service_jobs")
	iamDelegations = sim.MakeStore[IAMDelegationRequest](srv.DB(), "iam_delegation_requests")
	iamAccountFlags = sim.MakeStore[IAMAccountFeature](srv.DB(), "iam_account_flags")
	iamCredReportGen = sim.MakeStore[IAMAccountFeature](srv.DB(), "iam_cred_report_gen")
}

func iamResultXML(w http.ResponseWriter, op, inner string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><%sResult>%s</%sResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>`,
		op, op, inner, op, sim.NewUUID(), op)
}

// handleIAMGetAccountSummary counts the entities in the IAM stores and reports the fixed account quotas.
func handleIAMGetAccountSummary(w http.ResponseWriter, r *http.Request) {
	users := len(iamUsers.List())
	groups := len(iamGroups.List())
	roles := len(iamRoles.List())
	policies := len(iamPolicies.List())
	instanceProfiles := len(iamInstanceProfiles.List())
	mfaInUse := 0 // MFA devices aren't modeled with an enabled flag; report 0.

	// Each managed policy has one default version.
	policyVersions := policies

	entries := []struct {
		Key   string
		Value int
	}{
		{"Users", users},
		{"UsersQuota", 5000},
		{"Groups", groups},
		{"GroupsQuota", 300},
		{"Roles", roles},
		{"RolesQuota", 1000},
		{"Policies", policies},
		{"PoliciesQuota", 1500},
		{"PolicyVersionsInUse", policyVersions},
		{"PolicyVersionsInUseQuota", 10000},
		{"InstanceProfiles", instanceProfiles},
		{"InstanceProfilesQuota", 1000},
		{"ServerCertificates", 0},
		{"ServerCertificatesQuota", 20},
		{"MFADevices", mfaInUse},
		{"MFADevicesInUse", mfaInUse},
		{"AccountMFAEnabled", 0},
		{"AccessKeysPerUserQuota", 2},
		{"AttachedPoliciesPerUserQuota", 10},
		{"AttachedPoliciesPerGroupQuota", 10},
		{"AttachedPoliciesPerRoleQuota", 10},
		{"GroupsPerUserQuota", 10},
		{"SigningCertificatesPerUserQuota", 2},
		{"UserPolicySizeQuota", 2048},
		{"GroupPolicySizeQuota", 5120},
		{"RolePolicySizeQuota", 10240},
		{"VersionsPerPolicyQuota", 5},
		{"GlobalEndpointTokenVersion", iamGlobalEndpointTokenVersionNum()},
	}

	var b strings.Builder
	b.WriteString("<SummaryMap>")
	for _, e := range entries {
		fmt.Fprintf(&b, "<entry><key>%s</key><value>%d</value></entry>", e.Key, e.Value)
	}
	b.WriteString("</SummaryMap>")
	iamResultXML(w, "GetAccountSummary", b.String())
}

// iamGlobalEndpointTokenVersionNum reports the stored STS token version as the
// summary's integer (1 = v1Token, 2 = v2Token).
func iamGlobalEndpointTokenVersionNum() int {
	if f, ok := iamAccountFlags.Get("GlobalEndpointTokenVersion"); ok && f.Value == "v2Token" {
		return 2
	}
	return 1
}

func handleIAMGetAccountAuthorizationDetails(w http.ResponseWriter, r *http.Request) {
	var userXML, groupXML, roleXML, policyXML strings.Builder

	users := iamUsers.List()
	sort.Slice(users, func(i, j int) bool { return users[i].UserName < users[j].UserName })
	for _, u := range users {
		userXML.WriteString("<member>")
		userXML.WriteString(iamUserInnerXML(u))
		userXML.WriteString(iamInlinePolicyListXML("UserPolicyList", iamInlineUserPolicies(u.UserName)))
		userXML.WriteString(iamGroupListXML(u.UserName))
		userXML.WriteString(iamAttachedListXML(iamAttachedForUser(u.UserName)))
		if len(u.Tags) > 0 {
			userXML.WriteString(iamTagsXML(u.Tags))
		}
		userXML.WriteString("</member>")
	}

	groups := iamGroups.List()
	sort.Slice(groups, func(i, j int) bool { return groups[i].GroupName < groups[j].GroupName })
	for _, g := range groups {
		groupXML.WriteString("<member>")
		groupXML.WriteString(iamGroupInnerXML(g))
		groupXML.WriteString(iamInlinePolicyListXML("GroupPolicyList", iamInlineGroupPolicies(g.GroupName)))
		groupXML.WriteString(iamAttachedListXML(iamAttachedForGroup(g.GroupName)))
		groupXML.WriteString("</member>")
	}

	roles := iamRoles.List()
	sort.Slice(roles, func(i, j int) bool { return roles[i].RoleName < roles[j].RoleName })
	for _, role := range roles {
		roleXML.WriteString("<member>")
		roleXML.WriteString(iamRoleDetailInnerXML(role))
		roleXML.WriteString(iamInlinePolicyListXML("RolePolicyList", iamInlineRolePolicies(role.RoleName)))
		roleXML.WriteString(iamAttachedListXML(iamAttachedForRole(role.RoleName)))
		if len(role.Tags) > 0 {
			roleXML.WriteString(iamTagsXML(role.Tags))
		}
		roleXML.WriteString("</member>")
	}

	policies := iamPolicies.List()
	sort.Slice(policies, func(i, j int) bool { return policies[i].Arn < policies[j].Arn })
	for _, p := range policies {
		policyXML.WriteString("<member>")
		policyXML.WriteString(iamManagedPolicyDetailXML(p))
		policyXML.WriteString("</member>")
	}

	inner := fmt.Sprintf("<UserDetailList>%s</UserDetailList><GroupDetailList>%s</GroupDetailList><RoleDetailList>%s</RoleDetailList><Policies>%s</Policies><IsTruncated>false</IsTruncated>",
		userXML.String(), groupXML.String(), roleXML.String(), policyXML.String())
	iamResultXML(w, "GetAccountAuthorizationDetails", inner)
}

// iamRoleDetailInnerXML emits RoleDetail's fields without the <Role> wrapper:
// the role's identity fields plus its assume-role policy document.
func iamRoleDetailInnerXML(role IAMRole) string {
	doc := urlQueryEscapeIAM(role.AssumeRolePolicyDocument)
	return fmt.Sprintf("<Path>%s</Path><RoleName>%s</RoleName><RoleId>%s</RoleId><Arn>%s</Arn><CreateDate>%s</CreateDate><AssumeRolePolicyDocument>%s</AssumeRolePolicyDocument>",
		xmlEscape(role.Path), xmlEscape(role.RoleName), role.RoleId, xmlEscape(role.Arn), role.CreateDate, doc)
}

type iamInlinePolicy struct {
	Name string
	Doc  string
}

func iamInlineUserPolicies(user string) []iamInlinePolicy {
	var out []iamInlinePolicy
	for _, p := range iamUserPolicies.List() {
		if p.UserName == user {
			out = append(out, iamInlinePolicy{Name: p.PolicyName, Doc: p.PolicyDocument})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func iamInlineGroupPolicies(group string) []iamInlinePolicy {
	var out []iamInlinePolicy
	for _, p := range iamGroupPolicies.List() {
		if p.GroupName == group {
			out = append(out, iamInlinePolicy{Name: p.PolicyName, Doc: p.PolicyDocument})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func iamInlineRolePolicies(role string) []iamInlinePolicy {
	var out []iamInlinePolicy
	for _, p := range iamRolePolicies.List() {
		if p.RoleName == role {
			out = append(out, iamInlinePolicy{Name: p.PolicyName, Doc: p.PolicyDocument})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func iamInlinePolicyListXML(field string, policies []iamInlinePolicy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", field)
	for _, p := range policies {
		fmt.Fprintf(&b, "<member><PolicyName>%s</PolicyName><PolicyDocument>%s</PolicyDocument></member>",
			xmlEscape(p.Name), urlQueryEscapeIAM(p.Doc))
	}
	fmt.Fprintf(&b, "</%s>", field)
	return b.String()
}

type iamAttachedRef struct {
	Name string
	Arn  string
}

func iamAttachedForUser(user string) []iamAttachedRef {
	var out []iamAttachedRef
	for _, ap := range iamUserAttached.List() {
		if ap.UserName == user {
			out = append(out, iamAttachedRef{Name: ap.PolicyName, Arn: ap.PolicyArn})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Arn < out[j].Arn })
	return out
}

func iamAttachedForGroup(group string) []iamAttachedRef {
	var out []iamAttachedRef
	for _, ap := range iamGroupAttached.List() {
		if ap.GroupName == group {
			out = append(out, iamAttachedRef{Name: ap.PolicyName, Arn: ap.PolicyArn})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Arn < out[j].Arn })
	return out
}

func iamAttachedForRole(role string) []iamAttachedRef {
	var out []iamAttachedRef
	for _, ap := range iamAttachedPolicies.List() {
		if ap.RoleName == role {
			out = append(out, iamAttachedRef{Name: ap.PolicyName, Arn: ap.PolicyArn})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Arn < out[j].Arn })
	return out
}

func iamAttachedListXML(refs []iamAttachedRef) string {
	var b strings.Builder
	b.WriteString("<AttachedManagedPolicies>")
	for _, a := range refs {
		fmt.Fprintf(&b, "<member><PolicyName>%s</PolicyName><PolicyArn>%s</PolicyArn></member>",
			xmlEscape(a.Name), xmlEscape(a.Arn))
	}
	b.WriteString("</AttachedManagedPolicies>")
	return b.String()
}

func iamGroupListXML(user string) string {
	var groups []string
	for _, m := range iamGroupMembers.List() {
		if m.UserName == user {
			groups = append(groups, m.GroupName)
		}
	}
	sort.Strings(groups)
	var b strings.Builder
	b.WriteString("<GroupList>")
	for _, g := range groups {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(g))
	}
	b.WriteString("</GroupList>")
	return b.String()
}

func iamManagedPolicyDetailXML(p IAMPolicy) string {
	return fmt.Sprintf("<PolicyName>%s</PolicyName><PolicyId>%s</PolicyId><Arn>%s</Arn><Path>%s</Path><DefaultVersionId>%s</DefaultVersionId><AttachmentCount>0</AttachmentCount><PermissionsBoundaryUsageCount>0</PermissionsBoundaryUsageCount><IsAttachable>true</IsAttachable><Description>%s</Description><CreateDate>%s</CreateDate><UpdateDate>%s</UpdateDate><PolicyVersionList><member><Document>%s</Document><VersionId>%s</VersionId><IsDefaultVersion>true</IsDefaultVersion><CreateDate>%s</CreateDate></member></PolicyVersionList>",
		xmlEscape(p.PolicyName), p.PolicyId, xmlEscape(p.Arn), xmlEscape(p.Path), p.DefaultVersionId,
		xmlEscape(p.Description), p.CreateDate, p.CreateDate, urlQueryEscapeIAM(p.PolicyDocument), p.DefaultVersionId, p.CreateDate)
}

// handleIAMGenerateCredentialReport reports STARTED on the first call and
// COMPLETE after, IAM's two-call generate-then-ready flow.
func handleIAMGenerateCredentialReport(w http.ResponseWriter, r *http.Request) {
	state := "STARTED"
	desc := "No report exists. Starting a new report generation task"
	if _, ok := iamCredReportGen.Get("state"); ok {
		state = "COMPLETE"
		desc = "report has been generated"
	}
	iamCredReportGen.Put("state", IAMAccountFeature{Key: "state", Value: "COMPLETE"})
	iamResultXML(w, "GenerateCredentialReport",
		fmt.Sprintf("<State>%s</State><Description>%s</Description>", state, xmlEscape(desc)))
}

// handleIAMGetCredentialReport returns the report as base64-encoded text/csv, as AWS does.
func handleIAMGetCredentialReport(w http.ResponseWriter, r *http.Request) {
	csv := iamBuildCredentialReportCSV()
	content := base64.StdEncoding.EncodeToString([]byte(csv))
	inner := fmt.Sprintf("<Content>%s</Content><ReportFormat>text/csv</ReportFormat><GeneratedTime>%s</GeneratedTime>",
		content, time.Now().UTC().Format(time.RFC3339))
	iamResultXML(w, "GetCredentialReport", inner)
}

func iamBuildCredentialReportCSV() string {
	header := "user,arn,user_creation_time,password_enabled,password_last_used,password_last_changed,password_next_rotation,mfa_active,access_key_1_active,access_key_1_last_rotated,access_key_1_last_used_date,access_key_1_last_used_region,access_key_1_last_used_service,access_key_2_active,access_key_2_last_rotated,access_key_2_last_used_date,access_key_2_last_used_region,access_key_2_last_used_service,cert_1_active,cert_1_last_rotated,cert_2_active,cert_2_last_rotated"

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")

	// <root_account> row — AWS always emits it first.
	fmt.Fprintf(&b, "<root_account>,arn:aws:iam::%s:root,%s,not_supported,not_supported,not_supported,not_supported,false,false,N/A,N/A,N/A,N/A,false,N/A,N/A,N/A,N/A,false,N/A,false,N/A\n",
		awsAccountID(), time.Now().UTC().Format(time.RFC3339))

	users := iamUsers.List()
	sort.Slice(users, func(i, j int) bool { return users[i].UserName < users[j].UserName })
	for _, u := range users {
		keys := iamAccessKeysForUser(u.UserName)
		k1Active, k1Rotated := "false", "N/A"
		k2Active, k2Rotated := "false", "N/A"
		if len(keys) > 0 {
			k1Active = boolToCSV(keys[0].Status == "Active")
			k1Rotated = csvField(keys[0].CreateDate)
		}
		if len(keys) > 1 {
			k2Active = boolToCSV(keys[1].Status == "Active")
			k2Rotated = csvField(keys[1].CreateDate)
		}
		fmt.Fprintf(&b, "%s,%s,%s,false,N/A,N/A,N/A,false,%s,%s,N/A,N/A,N/A,%s,%s,N/A,N/A,N/A,false,N/A,false,N/A\n",
			csvField(u.UserName), csvField(u.Arn), csvField(u.CreateDate),
			k1Active, k1Rotated, k2Active, k2Rotated)
	}
	return b.String()
}

func iamAccessKeysForUser(user string) []IAMAccessKey {
	var out []IAMAccessKey
	for _, k := range iamAccessKeys.List() {
		if k.UserName == user {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreateDate < out[j].CreateDate })
	return out
}

func boolToCSV(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func csvField(s string) string {
	if s == "" {
		return "N/A"
	}
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// handleIAMGenerateServiceLastAccessed records a job for the principal that
// settles COMPLETED immediately.
func handleIAMGenerateServiceLastAccessed(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("Arn")
	if arn == "" {
		iamErrorXML(w, "ValidationError", "Arn is required", http.StatusBadRequest)
		return
	}
	granularity := r.FormValue("Granularity")
	if granularity == "" {
		granularity = "SERVICE_LEVEL"
	}
	job := IAMServiceJob{
		JobId:      sim.NewUUID(),
		JobType:    granularity,
		Arn:        arn,
		CreateDate: time.Now().UTC().Format(time.RFC3339),
	}
	iamServiceJobs.Put(job.JobId, job)
	iamResultXML(w, "GenerateServiceLastAccessedDetails", fmt.Sprintf("<JobId>%s</JobId>", job.JobId))
}

// Derive the rows from the service namespaces of the principal's policies.
func handleIAMGetServiceLastAccessed(w http.ResponseWriter, r *http.Request) {
	job, ok := iamServiceJobs.Get(r.FormValue("JobId"))
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "The specified job could not be found.", http.StatusNotFound)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	namespaces := iamServiceNamespacesForPrincipal(job.Arn)

	var rows strings.Builder
	rows.WriteString("<ServicesLastAccessed>")
	for _, ns := range namespaces {
		fmt.Fprintf(&rows, "<member><ServiceName>%s</ServiceName><ServiceNamespace>%s</ServiceNamespace><LastAuthenticated>%s</LastAuthenticated><LastAuthenticatedEntity>%s</LastAuthenticatedEntity><TotalAuthenticatedEntities>1</TotalAuthenticatedEntities></member>",
			xmlEscape(iamServiceDisplayName(ns)), xmlEscape(ns), now, xmlEscape(job.Arn))
	}
	rows.WriteString("</ServicesLastAccessed>")

	inner := fmt.Sprintf("<JobStatus>COMPLETED</JobStatus><JobType>%s</JobType><JobCreationDate>%s</JobCreationDate>%s<JobCompletionDate>%s</JobCompletionDate><IsTruncated>false</IsTruncated>",
		job.JobType, job.CreateDate, rows.String(), now)
	iamResultXML(w, "GetServiceLastAccessedDetails", inner)
}

// The principal itself is the only entity reported.
func handleIAMGetServiceLastAccessedWithEntities(w http.ResponseWriter, r *http.Request) {
	job, ok := iamServiceJobs.Get(r.FormValue("JobId"))
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "The specified job could not be found.", http.StatusNotFound)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	name, etype, id, path := iamEntityForArn(job.Arn)

	entity := fmt.Sprintf("<member><EntityInfo><Arn>%s</Arn><Name>%s</Name><Type>%s</Type><Id>%s</Id><Path>%s</Path></EntityInfo><LastAuthenticated>%s</LastAuthenticated></member>",
		xmlEscape(job.Arn), xmlEscape(name), etype, xmlEscape(id), xmlEscape(path), now)

	inner := fmt.Sprintf("<JobStatus>COMPLETED</JobStatus><JobCreationDate>%s</JobCreationDate><JobCompletionDate>%s</JobCompletionDate><EntityDetailsList>%s</EntityDetailsList><IsTruncated>false</IsTruncated>",
		job.CreateDate, now, entity)
	iamResultXML(w, "GetServiceLastAccessedDetailsWithEntities", inner)
}

// iamServiceNamespacesForPrincipal collects the distinct service prefixes ("s3"
// in "s3:GetObject") from the principal's inline and attached policies.
func iamServiceNamespacesForPrincipal(arn string) []string {
	docs := iamPolicyDocsForArn(arn)
	seen := map[string]bool{}
	for _, doc := range docs {
		for _, stmt := range doc.Statement {
			for _, act := range stmt.Action {
				if i := strings.Index(act, ":"); i > 0 {
					ns := act[:i]
					if ns != "*" {
						seen[ns] = true
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func iamPolicyDocsForArn(arn string) []iamPolicyDoc {
	var docs []iamPolicyDoc
	addManaged := func(policyArn string) {
		if p, ok := iamPolicies.Get(policyArn); ok {
			if doc, err := parseIAMPolicy(p.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	switch {
	case strings.Contains(arn, ":role/"):
		name := iamNameFromArn(arn)
		for _, p := range iamRolePolicies.List() {
			if p.RoleName == name {
				if doc, err := parseIAMPolicy(p.PolicyDocument); err == nil {
					docs = append(docs, doc)
				}
			}
		}
		for _, ap := range iamAttachedPolicies.List() {
			if ap.RoleName == name {
				addManaged(ap.PolicyArn)
			}
		}
	case strings.Contains(arn, ":group/"):
		name := iamNameFromArn(arn)
		for _, p := range iamGroupPolicies.List() {
			if p.GroupName == name {
				if doc, err := parseIAMPolicy(p.PolicyDocument); err == nil {
					docs = append(docs, doc)
				}
			}
		}
		for _, ap := range iamGroupAttached.List() {
			if ap.GroupName == name {
				addManaged(ap.PolicyArn)
			}
		}
	default: // user
		name := iamNameFromArn(arn)
		docs = append(docs, iamPolicyDocsForUser(name)...)
	}
	return docs
}

func iamNameFromArn(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// Type is the policyOwnerEntityType (USER, ROLE or GROUP).
func iamEntityForArn(arn string) (name, etype, id, path string) {
	name = iamNameFromArn(arn)
	switch {
	case strings.Contains(arn, ":role/"):
		if role, ok := iamRoles.Get(name); ok {
			return role.RoleName, "ROLE", role.RoleId, role.Path
		}
		return name, "ROLE", "", "/"
	case strings.Contains(arn, ":group/"):
		if g, ok := iamGroups.Get(name); ok {
			return g.GroupName, "GROUP", g.GroupId, g.Path
		}
		return name, "GROUP", "", "/"
	default:
		if u, ok := iamUsers.Get(name); ok {
			return u.UserName, "USER", u.UserId, u.Path
		}
		return name, "USER", "", "/"
	}
}

// AWS returns a friendly ServiceName; the simulator title-cases the namespace, the authoritative key.
func iamServiceDisplayName(ns string) string {
	if ns == "" {
		return ns
	}
	return strings.ToUpper(ns[:1]) + ns[1:]
}

func handleIAMGenerateOrganizationsAccessReport(w http.ResponseWriter, r *http.Request) {
	entityPath := r.FormValue("EntityPath")
	if entityPath == "" {
		iamErrorXML(w, "ValidationError", "EntityPath is required", http.StatusBadRequest)
		return
	}
	job := IAMServiceJob{
		JobId:      sim.NewUUID(),
		EntityPath: entityPath,
		PolicyId:   r.FormValue("OrganizationsPolicyId"),
		CreateDate: time.Now().UTC().Format(time.RFC3339),
	}
	iamServiceJobs.Put(job.JobId, job)
	iamResultXML(w, "GenerateOrganizationsAccessReport", fmt.Sprintf("<JobId>%s</JobId>", job.JobId))
}

func handleIAMGetOrganizationsAccessReport(w http.ResponseWriter, r *http.Request) {
	job, ok := iamServiceJobs.Get(r.FormValue("JobId"))
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "The specified job could not be found.", http.StatusNotFound)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Accessible services are the union of service namespaces across the account's managed policies.
	namespaces := iamAllConfiguredServiceNamespaces()
	var rows strings.Builder
	rows.WriteString("<AccessDetails>")
	for _, ns := range namespaces {
		fmt.Fprintf(&rows, "<member><ServiceName>%s</ServiceName><ServiceNamespace>%s</ServiceNamespace><EntityPath>%s</EntityPath><LastAuthenticatedTime>%s</LastAuthenticatedTime><TotalAuthenticatedEntities>1</TotalAuthenticatedEntities></member>",
			xmlEscape(iamServiceDisplayName(ns)), xmlEscape(ns), xmlEscape(job.EntityPath), now)
	}
	rows.WriteString("</AccessDetails>")

	inner := fmt.Sprintf("<JobStatus>COMPLETED</JobStatus><JobCreationDate>%s</JobCreationDate><JobCompletionDate>%s</JobCompletionDate><NumberOfServicesAccessible>%d</NumberOfServicesAccessible><NumberOfServicesNotAccessed>0</NumberOfServicesNotAccessed>%s<IsTruncated>false</IsTruncated>",
		job.CreateDate, now, len(namespaces), rows.String())
	iamResultXML(w, "GetOrganizationsAccessReport", inner)
}

func iamAllConfiguredServiceNamespaces() []string {
	seen := map[string]bool{}
	for _, p := range iamPolicies.List() {
		if doc, err := parseIAMPolicy(p.PolicyDocument); err == nil {
			for _, stmt := range doc.Statement {
				for _, act := range stmt.Action {
					if i := strings.Index(act, ":"); i > 0 && act[:i] != "*" {
						seen[act[:i]] = true
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func iamOrgId() string {
	if f, ok := iamAccountFlags.Get("OrganizationId"); ok && f.Value != "" {
		return f.Value
	}
	orgId := "o-" + strings.ToLower(sim.NewUUID()[:10])
	iamAccountFlags.Put("OrganizationId", IAMAccountFeature{Key: "OrganizationId", Value: orgId})
	return orgId
}

func iamSetOrgFeature(key string, enabled bool) {
	if enabled {
		iamAccountFlags.Put(key, IAMAccountFeature{Key: key, Value: "enabled"})
	} else {
		iamAccountFlags.Delete(key)
	}
}

// AWS's EnabledFeatures values are RootCredentialsManagement and RootSessions.
func iamEnabledFeaturesXML() string {
	var b strings.Builder
	b.WriteString("<EnabledFeatures>")
	if _, ok := iamAccountFlags.Get("RootCredentialsManagement"); ok {
		b.WriteString("<member>RootCredentialsManagement</member>")
	}
	if _, ok := iamAccountFlags.Get("RootSessions"); ok {
		b.WriteString("<member>RootSessions</member>")
	}
	b.WriteString("</EnabledFeatures>")
	return b.String()
}

func iamOrgFeaturesResult(w http.ResponseWriter, op string) {
	inner := fmt.Sprintf("<OrganizationId>%s</OrganizationId>%s", iamOrgId(), iamEnabledFeaturesXML())
	iamResultXML(w, op, inner)
}

func handleIAMEnableOrgRootCredentials(w http.ResponseWriter, r *http.Request) {
	iamSetOrgFeature("RootCredentialsManagement", true)
	iamOrgFeaturesResult(w, "EnableOrganizationsRootCredentialsManagement")
}

func handleIAMDisableOrgRootCredentials(w http.ResponseWriter, r *http.Request) {
	iamSetOrgFeature("RootCredentialsManagement", false)
	iamOrgFeaturesResult(w, "DisableOrganizationsRootCredentialsManagement")
}

func handleIAMEnableOrgRootSessions(w http.ResponseWriter, r *http.Request) {
	iamSetOrgFeature("RootSessions", true)
	iamOrgFeaturesResult(w, "EnableOrganizationsRootSessions")
}

func handleIAMDisableOrgRootSessions(w http.ResponseWriter, r *http.Request) {
	iamSetOrgFeature("RootSessions", false)
	iamOrgFeaturesResult(w, "DisableOrganizationsRootSessions")
}

func handleIAMListOrganizationsFeatures(w http.ResponseWriter, r *http.Request) {
	iamOrgFeaturesResult(w, "ListOrganizationsFeatures")
}

func iamDelegationRequestXML(d IAMDelegationRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<DelegationRequestId>%s</DelegationRequestId>", xmlEscape(d.DelegationRequestId))
	if d.OwnerAccountId != "" {
		fmt.Fprintf(&b, "<OwnerAccountId>%s</OwnerAccountId>", xmlEscape(d.OwnerAccountId))
	}
	if d.Description != "" {
		fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(d.Description))
	}
	if d.RequestMessage != "" {
		fmt.Fprintf(&b, "<RequestMessage>%s</RequestMessage>", xmlEscape(d.RequestMessage))
	}
	if d.PolicyTemplateArn != "" {
		fmt.Fprintf(&b, "<Permissions><PolicyTemplateArn>%s</PolicyTemplateArn></Permissions>", xmlEscape(d.PolicyTemplateArn))
	}
	fmt.Fprintf(&b, "<State>%s</State>", d.State)
	if d.SessionDuration > 0 {
		fmt.Fprintf(&b, "<SessionDuration>%d</SessionDuration>", d.SessionDuration)
	}
	if d.RedirectUrl != "" {
		fmt.Fprintf(&b, "<RedirectUrl>%s</RedirectUrl>", xmlEscape(d.RedirectUrl))
	}
	if d.Notes != "" {
		fmt.Fprintf(&b, "<Notes>%s</Notes>", xmlEscape(d.Notes))
	}
	if d.RejectionReason != "" {
		fmt.Fprintf(&b, "<RejectionReason>%s</RejectionReason>", xmlEscape(d.RejectionReason))
	}
	fmt.Fprintf(&b, "<OnlySendByOwner>%t</OnlySendByOwner>", d.OnlySendByOwner)
	fmt.Fprintf(&b, "<CreateDate>%s</CreateDate>", d.CreateDate)
	if d.UpdatedTime != "" {
		fmt.Fprintf(&b, "<UpdatedTime>%s</UpdatedTime>", d.UpdatedTime)
	}
	if d.ExpirationTime != "" {
		fmt.Fprintf(&b, "<ExpirationTime>%s</ExpirationTime>", d.ExpirationTime)
	}
	return b.String()
}

func handleIAMCreateDelegationRequest(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	d := IAMDelegationRequest{
		DelegationRequestId: "dr-" + strings.ToLower(sim.NewUUID()[:16]),
		OwnerAccountId:      r.FormValue("OwnerAccountId"),
		Description:         r.FormValue("Description"),
		RequestMessage:      r.FormValue("RequestMessage"),
		PolicyTemplateArn:   r.FormValue("Permissions.PolicyTemplateArn"),
		State:               "PENDING_APPROVAL",
		SessionDuration:     atoiDefault(r.FormValue("SessionDuration"), 0),
		RedirectUrl:         r.FormValue("RedirectUrl"),
		OnlySendByOwner:     r.FormValue("OnlySendByOwner") == "true",
		CreateDate:          now.Format(time.RFC3339),
		ExpirationTime:      now.Add(24 * time.Hour).Format(time.RFC3339),
	}
	iamDelegations.Put(d.DelegationRequestId, d)
	deepLink := fmt.Sprintf("https://console.aws.amazon.com/iam/home#/delegation-requests/%s", d.DelegationRequestId)
	inner := fmt.Sprintf("<ConsoleDeepLink>%s</ConsoleDeepLink><DelegationRequestId>%s</DelegationRequestId>",
		xmlEscape(deepLink), xmlEscape(d.DelegationRequestId))
	iamResultXML(w, "CreateDelegationRequest", inner)
}

func handleIAMGetDelegationRequest(w http.ResponseWriter, r *http.Request) {
	d, ok := iamDelegations.Get(r.FormValue("DelegationRequestId"))
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "The specified delegation request could not be found.", http.StatusNotFound)
		return
	}
	inner := fmt.Sprintf("<DelegationRequest>%s</DelegationRequest><PermissionCheckStatus>COMPLETE</PermissionCheckStatus>",
		iamDelegationRequestXML(d))
	iamResultXML(w, "GetDelegationRequest", inner)
}

func handleIAMListDelegationRequests(w http.ResponseWriter, r *http.Request) {
	reqs := iamDelegations.List()
	owner := r.FormValue("OwnerId")
	if owner != "" {
		filtered := reqs[:0]
		for _, d := range reqs {
			if d.OwnerAccountId == owner {
				filtered = append(filtered, d)
			}
		}
		reqs = filtered
	}
	page, next, pageOK := awsKeyPage(w, iamBadToken, reqs, func(it IAMDelegationRequest) string { return it.DelegationRequestId }, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0), 0)
	if !pageOK {
		return
	}

	var b strings.Builder
	b.WriteString("<DelegationRequests>")
	for _, d := range page {
		b.WriteString("<member>" + iamDelegationRequestXML(d) + "</member>")
	}
	b.WriteString("</DelegationRequests>")
	inner := fmt.Sprintf("%s<isTruncated>%t</isTruncated>%s", b.String(), next != "", iamMarkerXML(next))
	iamResultXML(w, "ListDelegationRequests", inner)
}

// iamTransitionDelegation applies fn and stamps UpdatedTime; it writes
// NoSuchEntity and returns false when the request does not exist.
func iamTransitionDelegation(w http.ResponseWriter, id string, fn func(*IAMDelegationRequest)) bool {
	if !iamDelegations.Update(id, func(d *IAMDelegationRequest) {
		fn(d)
		d.UpdatedTime = time.Now().UTC().Format(time.RFC3339)
	}) {
		iamErrorXML(w, "NoSuchEntity", "The specified delegation request could not be found.", http.StatusNotFound)
		return false
	}
	return true
}

func handleIAMAcceptDelegationRequest(w http.ResponseWriter, r *http.Request) {
	if !iamTransitionDelegation(w, r.FormValue("DelegationRequestId"), func(d *IAMDelegationRequest) {
		d.State = "ACCEPTED"
	}) {
		return
	}
	iamEmptyResultXML(w, "AcceptDelegationRequest")
}

func handleIAMRejectDelegationRequest(w http.ResponseWriter, r *http.Request) {
	notes := r.FormValue("Notes")
	if !iamTransitionDelegation(w, r.FormValue("DelegationRequestId"), func(d *IAMDelegationRequest) {
		d.State = "REJECTED"
		d.RejectionReason = notes
		d.Notes = notes
	}) {
		return
	}
	iamEmptyResultXML(w, "RejectDelegationRequest")
}

func handleIAMAssociateDelegationRequest(w http.ResponseWriter, r *http.Request) {
	if !iamTransitionDelegation(w, r.FormValue("DelegationRequestId"), func(d *IAMDelegationRequest) {
		d.State = "ASSIGNED"
	}) {
		return
	}
	iamEmptyResultXML(w, "AssociateDelegationRequest")
}

func handleIAMUpdateDelegationRequest(w http.ResponseWriter, r *http.Request) {
	notes := r.FormValue("Notes")
	if !iamTransitionDelegation(w, r.FormValue("DelegationRequestId"), func(d *IAMDelegationRequest) {
		d.Notes = notes
	}) {
		return
	}
	iamEmptyResultXML(w, "UpdateDelegationRequest")
}

func handleIAMSendDelegationToken(w http.ResponseWriter, r *http.Request) {
	if !iamTransitionDelegation(w, r.FormValue("DelegationRequestId"), func(d *IAMDelegationRequest) {
		d.State = "FINALIZED"
	}) {
		return
	}
	iamEmptyResultXML(w, "SendDelegationToken")
}

func handleIAMSetSecurityTokenServicePreferences(w http.ResponseWriter, r *http.Request) {
	version := r.FormValue("GlobalEndpointTokenVersion")
	if version != "v1Token" && version != "v2Token" {
		iamErrorXML(w, "ValidationError", "GlobalEndpointTokenVersion must be v1Token or v2Token", http.StatusBadRequest)
		return
	}
	iamAccountFlags.Put("GlobalEndpointTokenVersion", IAMAccountFeature{Key: "GlobalEndpointTokenVersion", Value: version})
	iamEmptyResultXML(w, "SetSecurityTokenServicePreferences")
}

// Derive the summary from the service namespaces the entity's policies reference.
func handleIAMGetHumanReadableSummary(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("EntityArn")
	if arn == "" {
		iamErrorXML(w, "ValidationError", "EntityArn is required", http.StatusBadRequest)
		return
	}
	locale := r.FormValue("Locale")
	if locale == "" {
		locale = "en"
	}
	namespaces := iamServiceNamespacesForPrincipal(arn)
	name, _, _, _ := iamEntityForArn(arn)
	var summary string
	if len(namespaces) == 0 {
		summary = fmt.Sprintf("%s has no configured permissions.", name)
	} else {
		summary = fmt.Sprintf("%s has permissions to the following services: %s.", name, strings.Join(namespaces, ", "))
	}
	inner := fmt.Sprintf("<SummaryContent>%s</SummaryContent><Locale>%s</Locale><SummaryState>AVAILABLE</SummaryState>",
		xmlEscape(summary), xmlEscape(locale))
	iamResultXML(w, "GetHumanReadableSummary", inner)
}

// urlQueryEscapeIAM URL-encodes a policy document the way IAM returns embedded
// documents (GetRolePolicy, GetPolicyVersion).
func urlQueryEscapeIAM(s string) string {
	return url.QueryEscape(s)
}

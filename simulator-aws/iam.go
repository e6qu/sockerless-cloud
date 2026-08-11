package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

type IAMRole struct {
	RoleName                 string
	RoleId                   string
	Arn                      string
	Path                     string
	AssumeRolePolicyDocument string
	CreateDate               string
	MaxSessionDuration       int
	Description              string
	PermissionsBoundaryARN   string
	Tags                     []IAMTag
}

type IAMRolePolicy struct {
	RoleName       string
	PolicyName     string
	PolicyDocument string
}

type IAMAttachedPolicy struct {
	RoleName   string
	PolicyArn  string
	PolicyName string
}

// IAMPolicy is a managed policy (Microsoft.IAM/policies).
type IAMPolicy struct {
	PolicyName       string   `json:"policyName"`
	PolicyId         string   `json:"policyId"`
	Arn              string   `json:"arn"`
	Path             string   `json:"path"`
	Description      string   `json:"description"`
	PolicyDocument   string   `json:"policyDocument"` // URL-decoded JSON
	DefaultVersionId string   `json:"defaultVersionId"`
	CreateDate       string   `json:"createDate"`
	Tags             []IAMTag `json:"tags,omitempty"`
}

// IAMInstanceProfile is a Microsoft.IAM/instanceProfiles resource. Each
// instance profile can hold at most one role (real AWS constraint).
type IAMInstanceProfile struct {
	InstanceProfileName string `json:"instanceProfileName"`
	InstanceProfileId   string `json:"instanceProfileId"`
	Arn                 string `json:"arn"`
	Path                string `json:"path"`
	CreateDate          string `json:"createDate"`
	RoleName            string `json:"roleName,omitempty"`
}

var (
	iamRoles            sim.Store[IAMRole]
	iamRolePolicies     sim.Store[IAMRolePolicy]
	iamAttachedPolicies sim.Store[IAMAttachedPolicy]
	iamPolicies         sim.Store[IAMPolicy]
	iamInstanceProfiles sim.Store[IAMInstanceProfile]
)

func registerIAM(r *sim.AWSQueryRouter, srv *sim.Server) {
	iamRoles = sim.MakeStore[IAMRole](srv.DB(), "iam_roles")
	iamRolePolicies = sim.MakeStore[IAMRolePolicy](srv.DB(), "iam_role_policies")
	iamAttachedPolicies = sim.MakeStore[IAMAttachedPolicy](srv.DB(), "iam_attached_policies")
	iamPolicies = sim.MakeStore[IAMPolicy](srv.DB(), "iam_policies")
	iamInstanceProfiles = sim.MakeStore[IAMInstanceProfile](srv.DB(), "iam_instance_profiles")

	r.Register("CreateRole", handleIAMCreateRole)
	r.Register("GetRole", handleIAMGetRole)
	r.Register("DeleteRole", handleIAMDeleteRole)
	r.Register("UpdateRole", handleIAMUpdateRole)
	r.Register("TagRole", handleIAMTagRole)
	r.Register("UntagRole", handleIAMUntagRole)
	r.Register("UpdateAssumeRolePolicy", handleIAMUpdateAssumeRolePolicy)
	r.Register("PutRolePolicy", handleIAMPutRolePolicy)
	r.Register("GetRolePolicy", handleIAMGetRolePolicy)
	r.Register("DeleteRolePolicy", handleIAMDeleteRolePolicy)
	r.Register("AttachRolePolicy", handleIAMAttachRolePolicy)
	r.Register("DetachRolePolicy", handleIAMDetachRolePolicy)
	r.Register("ListAttachedRolePolicies", handleIAMListAttachedRolePolicies)
	r.Register("ListRolePolicies", handleIAMListRolePolicies)
	r.Register("ListInstanceProfilesForRole", handleIAMListInstanceProfilesForRole)

	// Managed policies — canonical TF flow CreateRole → CreatePolicy →
	// AttachRolePolicy needs at least Create + Get + Delete + List.
	r.Register("CreatePolicy", handleIAMCreatePolicy)
	r.Register("GetPolicy", handleIAMGetPolicy)
	r.Register("DeletePolicy", handleIAMDeletePolicy)
	r.Register("ListPolicies", handleIAMListPolicies)
	r.Register("GetPolicyVersion", handleIAMGetPolicyVersion)

	// Instance profiles — needed to bind a role to an EC2 / ECS task
	// launch template. CreateInstanceProfile → AddRoleToInstanceProfile
	// is the canonical pair.
	r.Register("CreateInstanceProfile", handleIAMCreateInstanceProfile)
	r.Register("GetInstanceProfile", handleIAMGetInstanceProfile)
	r.Register("DeleteInstanceProfile", handleIAMDeleteInstanceProfile)
	r.Register("ListInstanceProfiles", handleIAMListInstanceProfiles)
	r.Register("AddRoleToInstanceProfile", handleIAMAddRoleToInstanceProfile)
	r.Register("RemoveRoleFromInstanceProfile", handleIAMRemoveRoleFromInstanceProfile)

	// Service-linked roles + OIDC providers (iam_slr_oidc.go)
	registerIAMPolicySimulation(r)
	registerIAMSLRandOIDC(r, srv)
	registerIAMLists(r)
	registerIAMUsers(r, srv)
	registerIAMGroups(r, srv)
	registerIAMRolesPolicies(r, srv)
	registerIAMUsersCreds(r, srv)
	registerIAMMFAKeys(r, srv)
	registerIAMProvidersCerts(r, srv)
	registerIAMAccountReports(r, srv)
}

// iamRoleFieldsXML emits the inner role fields without the `<Role>`
// wrapper. Used inline inside `<member>` elements where AWS's XML
// schema puts role fields directly under member (instance profile
// Roles list, ListAttachedRolePolicies list members, etc.).
func iamRoleFieldsXML(role IAMRole) string {
	doc := url.QueryEscape(role.AssumeRolePolicyDocument)
	maxSession := role.MaxSessionDuration
	if maxSession == 0 {
		maxSession = 3600
	}
	var extra string
	if role.Description != "" {
		extra += fmt.Sprintf("<Description>%s</Description>", xmlEscape(role.Description))
	}
	if role.PermissionsBoundaryARN != "" {
		extra += fmt.Sprintf("<PermissionsBoundary><PermissionsBoundaryType>PermissionsBoundaryPolicy</PermissionsBoundaryType><PermissionsBoundaryArn>%s</PermissionsBoundaryArn></PermissionsBoundary>",
			xmlEscape(role.PermissionsBoundaryARN))
	}
	return fmt.Sprintf(`<RoleName>%s</RoleName><RoleId>%s</RoleId><Arn>%s</Arn><Path>%s</Path><AssumeRolePolicyDocument>%s</AssumeRolePolicyDocument><CreateDate>%s</CreateDate><MaxSessionDuration>%d</MaxSessionDuration>%s%s`,
		role.RoleName, role.RoleId, role.Arn, role.Path, doc, role.CreateDate, maxSession, extra, iamTagsXML(role.Tags))
}

func iamRoleXML(role IAMRole) string {
	return "<Role>" + iamRoleFieldsXML(role) + "</Role>"
}

func handleIAMCreateRole(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	if _, ok := iamRoles.Get(name); ok {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("Role with name %s already exists.", name), http.StatusConflict)
		return
	}
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}
	assumeDoc := r.FormValue("AssumeRolePolicyDocument")
	if decoded, err := url.QueryUnescape(assumeDoc); err == nil {
		assumeDoc = decoded
	}
	if assumeDoc == "" {
		iamErrorXML(w, "ValidationError", "AssumeRolePolicyDocument is required", http.StatusBadRequest)
		return
	}
	if _, err := parseIAMPolicy(assumeDoc); err != nil {
		iamErrorXML(w, "MalformedPolicyDocument", "The trust policy could not be parsed: "+err.Error(), http.StatusBadRequest)
		return
	}

	role := IAMRole{
		RoleName:                 name,
		RoleId:                   "AROA" + strings.ToUpper(generateUUID()[:16]),
		Arn:                      fmt.Sprintf("arn:aws:iam::"+awsAccountID()+":role/%s", name),
		Path:                     path,
		AssumeRolePolicyDocument: assumeDoc,
		CreateDate:               time.Now().UTC().Format(time.RFC3339),
		MaxSessionDuration:       atoiDefault(r.FormValue("MaxSessionDuration"), 0),
		Description:              r.FormValue("Description"),
		PermissionsBoundaryARN:   r.FormValue("PermissionsBoundary"),
		Tags:                     iamParseTags(r),
	}
	iamRoles.Put(name, role)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <CreateRoleResult>%s</CreateRoleResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</CreateRoleResponse>`, iamRoleXML(role), generateUUID())
}

// handleIAMUpdateRole updates a role's description / max-session-duration. The
// provider calls it when aws_iam_role.{description,max_session_duration} change
// (assume-role policy goes through UpdateAssumeRolePolicy instead).
func handleIAMUpdateRole(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	if !iamRoles.Update(name, func(role *IAMRole) {
		if d := r.FormValue("Description"); d != "" {
			role.Description = d
		}
		if ms := r.FormValue("MaxSessionDuration"); ms != "" {
			role.MaxSessionDuration = atoiDefault(ms, role.MaxSessionDuration)
		}
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Role %s not found", name), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "UpdateRole")
}

// handleIAMTagRole adds/overwrites tags on a role (aws_iam_role.tags changes).
func handleIAMTagRole(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	newTags := iamParseTags(r)
	if !iamRoles.Update(name, func(role *IAMRole) {
		role.Tags = iamMergeTags(role.Tags, newTags)
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Role %s not found", name), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "TagRole")
}

// handleIAMUntagRole removes the named tag keys from a role.
func handleIAMUntagRole(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	remove := map[string]bool{}
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", i))
		if k == "" {
			break
		}
		remove[k] = true
	}
	if !iamRoles.Update(name, func(role *IAMRole) {
		kept := role.Tags[:0]
		for _, t := range role.Tags {
			if !remove[t.Key] {
				kept = append(kept, t)
			}
		}
		role.Tags = kept
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Role %s not found", name), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "UntagRole")
}

// iamMergeTags overwrites existing tag values by key and appends new keys,
// preserving order — the TagRole upsert semantics.
func iamMergeTags(existing, incoming []IAMTag) []IAMTag {
	out := append([]IAMTag(nil), existing...)
	for _, nt := range incoming {
		found := false
		for i := range out {
			if out[i].Key == nt.Key {
				out[i].Value = nt.Value
				found = true
				break
			}
		}
		if !found {
			out = append(out, nt)
		}
	}
	return out
}

// iamEmptyResultXML emits the canonical empty-result response IAM mutating ops
// return. The empty `<{op}Result/>` node is required by some deserializers
// (e.g. UpdateRole) and harmlessly skipped by the others (TagRole/UntagRole).
func iamEmptyResultXML(w http.ResponseWriter, op string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><%sResult/><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>`,
		op, op, generateUUID(), op)
}

func iamErrorXML(w http.ResponseWriter, code string, message string, statusCode int) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(statusCode)
	fmt.Fprintf(w, `<ErrorResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error>
  <RequestId>%s</RequestId>
</ErrorResponse>`, code, message, generateUUID())
}

func handleIAMGetRole(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	role, ok := iamRoles.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The role with name %s cannot be found.", name), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <GetRoleResult>%s</GetRoleResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetRoleResponse>`, iamRoleXML(role), generateUUID())
}

func handleIAMDeleteRole(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	if _, ok := iamRoles.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The role with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	// Real IAM refuses to delete a role that still has policies attached or is
	// referenced by an instance profile — detach/remove them first.
	for _, p := range iamRolePolicies.List() {
		if p.RoleName == name {
			iamErrorXML(w, "DeleteConflict", "Cannot delete entity, must delete policies first.", http.StatusConflict)
			return
		}
	}
	for _, ap := range iamAttachedPolicies.List() {
		if ap.RoleName == name {
			iamErrorXML(w, "DeleteConflict", "Cannot delete entity, must detach all policies first.", http.StatusConflict)
			return
		}
	}
	for _, ip := range iamInstanceProfiles.List() {
		if ip.RoleName == name {
			iamErrorXML(w, "DeleteConflict", "Cannot delete entity, must remove roles from instance profile first.", http.StatusConflict)
			return
		}
	}
	iamRoles.Delete(name)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DeleteRoleResponse>`, generateUUID())
}

func handleIAMUpdateAssumeRolePolicy(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	policyDoc := r.FormValue("PolicyDocument")
	if decoded, err := url.QueryUnescape(policyDoc); err == nil {
		policyDoc = decoded
	}
	if _, err := parseIAMPolicy(policyDoc); err != nil {
		iamErrorXML(w, "MalformedPolicyDocument", "The trust policy could not be parsed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if ok := iamRoles.Update(name, func(role *IAMRole) {
		role.AssumeRolePolicyDocument = policyDoc
	}); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The role with name %s cannot be found.", name), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UpdateAssumeRolePolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</UpdateAssumeRolePolicyResponse>`, generateUUID())
}

func handleIAMPutRolePolicy(w http.ResponseWriter, r *http.Request) {
	roleName := r.FormValue("RoleName")
	policyName := r.FormValue("PolicyName")
	policyDoc := r.FormValue("PolicyDocument")
	if decoded, err := url.QueryUnescape(policyDoc); err == nil {
		policyDoc = decoded
	}
	if _, err := parseIAMPolicy(policyDoc); err != nil {
		iamErrorXML(w, "MalformedPolicyDocument", "The policy document could not be parsed: "+err.Error(), http.StatusBadRequest)
		return
	}

	key := roleName + "/" + policyName
	iamRolePolicies.Put(key, IAMRolePolicy{
		RoleName:       roleName,
		PolicyName:     policyName,
		PolicyDocument: policyDoc,
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<PutRolePolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</PutRolePolicyResponse>`, generateUUID())
}

func handleIAMGetRolePolicy(w http.ResponseWriter, r *http.Request) {
	roleName := r.FormValue("RoleName")
	policyName := r.FormValue("PolicyName")
	key := roleName + "/" + policyName

	policy, ok := iamRolePolicies.Get(key)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The role policy with name %s cannot be found.", policyName), http.StatusNotFound)
		return
	}

	doc := url.QueryEscape(policy.PolicyDocument)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetRolePolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <GetRolePolicyResult>
    <RoleName>%s</RoleName>
    <PolicyName>%s</PolicyName>
    <PolicyDocument>%s</PolicyDocument>
  </GetRolePolicyResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetRolePolicyResponse>`, roleName, policyName, doc, generateUUID())
}

func handleIAMDeleteRolePolicy(w http.ResponseWriter, r *http.Request) {
	roleName := r.FormValue("RoleName")
	policyName := r.FormValue("PolicyName")
	iamRolePolicies.Delete(roleName + "/" + policyName)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteRolePolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DeleteRolePolicyResponse>`, generateUUID())
}

func handleIAMAttachRolePolicy(w http.ResponseWriter, r *http.Request) {
	roleName := r.FormValue("RoleName")
	policyArn := r.FormValue("PolicyArn")
	policyName := policyArn
	if idx := strings.LastIndex(policyArn, "/"); idx >= 0 {
		policyName = policyArn[idx+1:]
	}

	key := roleName + "/" + policyArn
	iamAttachedPolicies.Put(key, IAMAttachedPolicy{
		RoleName:   roleName,
		PolicyArn:  policyArn,
		PolicyName: policyName,
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AttachRolePolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AttachRolePolicyResponse>`, generateUUID())
}

func handleIAMDetachRolePolicy(w http.ResponseWriter, r *http.Request) {
	roleName := r.FormValue("RoleName")
	policyArn := r.FormValue("PolicyArn")
	iamAttachedPolicies.Delete(roleName + "/" + policyArn)

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DetachRolePolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DetachRolePolicyResponse>`, generateUUID())
}

func handleIAMListAttachedRolePolicies(w http.ResponseWriter, r *http.Request) {
	roleName := r.FormValue("RoleName")
	policies := iamAttachedPolicies.Filter(func(p IAMAttachedPolicy) bool {
		return p.RoleName == roleName
	})
	sort.Slice(policies, func(i, j int) bool { return policies[i].PolicyArn < policies[j].PolicyArn })

	page, next := awsPageExplicit(policies, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))

	var members strings.Builder
	for _, p := range page {
		fmt.Fprintf(&members, "<member><PolicyName>%s</PolicyName><PolicyArn>%s</PolicyArn></member>", p.PolicyName, p.PolicyArn)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListAttachedRolePoliciesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ListAttachedRolePoliciesResult>
    <AttachedPolicies>%s</AttachedPolicies>
    <IsTruncated>%t</IsTruncated>%s
  </ListAttachedRolePoliciesResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListAttachedRolePoliciesResponse>`, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

func handleIAMListInstanceProfilesForRole(w http.ResponseWriter, r *http.Request) {
	roleName := r.FormValue("RoleName")
	profiles := iamInstanceProfiles.Filter(func(ip IAMInstanceProfile) bool {
		return ip.RoleName == roleName
	})
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].InstanceProfileName < profiles[j].InstanceProfileName })
	page, next := awsPageExplicit(profiles, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))
	var members strings.Builder
	for _, ip := range page {
		fmt.Fprint(&members, "<member>", iamInstanceProfileXML(ip), "</member>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListInstanceProfilesForRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ListInstanceProfilesForRoleResult>
    <InstanceProfiles>%s</InstanceProfiles>
    <IsTruncated>%t</IsTruncated>%s
  </ListInstanceProfilesForRoleResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListInstanceProfilesForRoleResponse>`, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

func handleIAMListRolePolicies(w http.ResponseWriter, r *http.Request) {
	roleName := r.FormValue("RoleName")
	policies := iamRolePolicies.Filter(func(p IAMRolePolicy) bool {
		return p.RoleName == roleName
	})
	sort.Slice(policies, func(i, j int) bool { return policies[i].PolicyName < policies[j].PolicyName })
	page, next := awsPageExplicit(policies, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))

	var members strings.Builder
	for _, p := range page {
		fmt.Fprintf(&members, "<member>%s</member>", p.PolicyName)
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListRolePoliciesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ListRolePoliciesResult>
    <PolicyNames>%s</PolicyNames>
    <IsTruncated>%t</IsTruncated>%s
  </ListRolePoliciesResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListRolePoliciesResponse>`, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

// Managed policies + instance profiles. The canonical TF flow is
// CreateRole → CreatePolicy → AttachRolePolicy → CreateInstanceProfile
// → AddRoleToInstanceProfile; without these handlers, terraform-
// provider-aws fails at step 2 (CreatePolicy InvalidAction).

func iamPolicyXML(p IAMPolicy) string {
	return fmt.Sprintf(`<PolicyName>%s</PolicyName><PolicyId>%s</PolicyId><Arn>%s</Arn><Path>%s</Path><DefaultVersionId>%s</DefaultVersionId><CreateDate>%s</CreateDate><AttachmentCount>0</AttachmentCount><PermissionsBoundaryUsageCount>0</PermissionsBoundaryUsageCount><IsAttachable>true</IsAttachable><Description>%s</Description>%s`,
		p.PolicyName, p.PolicyId, p.Arn, p.Path, p.DefaultVersionId, p.CreateDate, p.Description, iamTagsXML(p.Tags))
}

func iamInstanceProfileXML(ip IAMInstanceProfile) string {
	roleBlock := ""
	if ip.RoleName != "" {
		if role, ok := iamRoles.Get(ip.RoleName); ok {
			// AWS XML: <Roles><member>{inner role fields, no <Role> wrapper}</member></Roles>
			roleBlock = "<Roles><member>" + iamRoleFieldsXML(role) + "</member></Roles>"
		}
	}
	if roleBlock == "" {
		roleBlock = "<Roles/>"
	}
	return fmt.Sprintf(`<InstanceProfileName>%s</InstanceProfileName><InstanceProfileId>%s</InstanceProfileId><Arn>%s</Arn><Path>%s</Path><CreateDate>%s</CreateDate>%s`,
		ip.InstanceProfileName, ip.InstanceProfileId, ip.Arn, ip.Path, ip.CreateDate, roleBlock)
}

func handleIAMCreatePolicy(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("PolicyName")
	if name == "" {
		iamErrorXML(w, "ValidationError", "PolicyName is required", http.StatusBadRequest)
		return
	}
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}
	doc := r.FormValue("PolicyDocument")
	if decoded, err := url.QueryUnescape(doc); err == nil {
		doc = decoded
	}
	if doc == "" {
		iamErrorXML(w, "ValidationError", "PolicyDocument is required", http.StatusBadRequest)
		return
	}
	if _, err := parseIAMPolicy(doc); err != nil {
		iamErrorXML(w, "MalformedPolicyDocument", "The policy document could not be parsed: "+err.Error(), http.StatusBadRequest)
		return
	}
	arn := fmt.Sprintf("arn:aws:iam::%s:policy%s%s", awsAccountID(), path, name)
	if _, ok := iamPolicies.Get(arn); ok {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("A policy called %s already exists. Duplicate names are not allowed.", name), http.StatusConflict)
		return
	}
	policy := IAMPolicy{
		PolicyName:       name,
		PolicyId:         "ANPA" + strings.ToUpper(generateUUID()[:16]),
		Arn:              arn,
		Path:             path,
		Description:      r.FormValue("Description"),
		PolicyDocument:   doc,
		DefaultVersionId: "v1",
		CreateDate:       time.Now().UTC().Format(time.RFC3339),
		Tags:             iamParseTags(r),
	}
	iamPolicies.Put(policy.Arn, policy)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreatePolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <CreatePolicyResult><Policy>%s</Policy></CreatePolicyResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</CreatePolicyResponse>`, iamPolicyXML(policy), generateUUID())
}

func handleIAMGetPolicy(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	policy, ok := iamPolicies.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetPolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <GetPolicyResult><Policy>%s</Policy></GetPolicyResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetPolicyResponse>`, iamPolicyXML(policy), generateUUID())
}

func handleIAMDeletePolicy(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	if !iamPolicies.Delete(arn) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeletePolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DeletePolicyResponse>`, generateUUID())
}

func handleIAMListPolicies(w http.ResponseWriter, r *http.Request) {
	policies := iamPolicies.List()
	sort.Slice(policies, func(i, j int) bool { return policies[i].Arn < policies[j].Arn })

	page, next := awsPageExplicit(policies, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))

	var members strings.Builder
	for _, p := range page {
		fmt.Fprint(&members, "<member>", iamPolicyXML(p), "</member>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListPoliciesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ListPoliciesResult><Policies>%s</Policies><IsTruncated>%t</IsTruncated>%s</ListPoliciesResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListPoliciesResponse>`, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

func handleIAMGetPolicyVersion(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	versionID := r.FormValue("VersionId")
	policy, ok := iamPolicies.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	if versionID == "" {
		versionID = policy.DefaultVersionId
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetPolicyVersionResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <GetPolicyVersionResult><PolicyVersion><Document>%s</Document><VersionId>%s</VersionId><IsDefaultVersion>true</IsDefaultVersion><CreateDate>%s</CreateDate></PolicyVersion></GetPolicyVersionResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetPolicyVersionResponse>`, url.QueryEscape(policy.PolicyDocument), versionID, policy.CreateDate, generateUUID())
}

func handleIAMCreateInstanceProfile(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("InstanceProfileName")
	if name == "" {
		iamErrorXML(w, "ValidationError", "InstanceProfileName is required", http.StatusBadRequest)
		return
	}
	if _, ok := iamInstanceProfiles.Get(name); ok {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("Instance Profile %s already exists.", name), http.StatusConflict)
		return
	}
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}
	ip := IAMInstanceProfile{
		InstanceProfileName: name,
		InstanceProfileId:   "AIPA" + strings.ToUpper(generateUUID()[:16]),
		Arn:                 fmt.Sprintf("arn:aws:iam::%s:instance-profile%s%s", awsAccountID(), path, name),
		Path:                path,
		CreateDate:          time.Now().UTC().Format(time.RFC3339),
	}
	iamInstanceProfiles.Put(name, ip)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateInstanceProfileResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <CreateInstanceProfileResult><InstanceProfile>%s</InstanceProfile></CreateInstanceProfileResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</CreateInstanceProfileResponse>`, iamInstanceProfileXML(ip), generateUUID())
}

func handleIAMGetInstanceProfile(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("InstanceProfileName")
	ip, ok := iamInstanceProfiles.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Instance Profile %s was not found.", name), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetInstanceProfileResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <GetInstanceProfileResult><InstanceProfile>%s</InstanceProfile></GetInstanceProfileResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetInstanceProfileResponse>`, iamInstanceProfileXML(ip), generateUUID())
}

func handleIAMDeleteInstanceProfile(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("InstanceProfileName")
	ip, ok := iamInstanceProfiles.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Instance Profile %s was not found.", name), http.StatusNotFound)
		return
	}
	// Real IAM refuses to delete an instance profile that still has a role —
	// the caller must RemoveRoleFromInstanceProfile first.
	if ip.RoleName != "" {
		iamErrorXML(w, "DeleteConflict", "Cannot delete entity, must remove roles from instance profile first.", http.StatusConflict)
		return
	}
	iamInstanceProfiles.Delete(name)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteInstanceProfileResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DeleteInstanceProfileResponse>`, generateUUID())
}

func handleIAMListInstanceProfiles(w http.ResponseWriter, r *http.Request) {
	profiles := iamInstanceProfiles.List()
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].InstanceProfileName < profiles[j].InstanceProfileName })

	page, next := awsPageExplicit(profiles, r.FormValue("Marker"), atoiDefault(r.FormValue("MaxItems"), 0))

	var members strings.Builder
	for _, ip := range page {
		fmt.Fprint(&members, "<member>", iamInstanceProfileXML(ip), "</member>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListInstanceProfilesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ListInstanceProfilesResult><InstanceProfiles>%s</InstanceProfiles><IsTruncated>%t</IsTruncated>%s</ListInstanceProfilesResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListInstanceProfilesResponse>`, members.String(), next != "", iamMarkerXML(next), generateUUID())
}

func handleIAMAddRoleToInstanceProfile(w http.ResponseWriter, r *http.Request) {
	ipName := r.FormValue("InstanceProfileName")
	roleName := r.FormValue("RoleName")
	ip, ok := iamInstanceProfiles.Get(ipName)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Instance Profile %s was not found.", ipName), http.StatusNotFound)
		return
	}
	if _, ok := iamRoles.Get(roleName); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Role %s was not found.", roleName), http.StatusNotFound)
		return
	}
	if ip.RoleName != "" && ip.RoleName != roleName {
		iamErrorXML(w, "LimitExceeded",
			fmt.Sprintf("Cannot exceed quota for InstanceSessionsPerInstanceProfile: 1. Instance profile %s already holds role %s.", ipName, ip.RoleName),
			http.StatusConflict)
		return
	}
	ip.RoleName = roleName
	iamInstanceProfiles.Put(ipName, ip)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AddRoleToInstanceProfileResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AddRoleToInstanceProfileResponse>`, generateUUID())
}

func handleIAMRemoveRoleFromInstanceProfile(w http.ResponseWriter, r *http.Request) {
	ipName := r.FormValue("InstanceProfileName")
	ip, ok := iamInstanceProfiles.Get(ipName)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Instance Profile %s was not found.", ipName), http.StatusNotFound)
		return
	}
	ip.RoleName = ""
	iamInstanceProfiles.Put(ipName, ip)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RemoveRoleFromInstanceProfileResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</RemoveRoleFromInstanceProfileResponse>`, generateUUID())
}

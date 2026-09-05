package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// IAM role permissions-boundary + description updates, instance-profile
// tagging, managed-policy versioning, policy↔entity introspection, and
// policy context-key extraction. These round out the managed-policy and role
// surfaces registered in iam.go: terraform-provider-aws's aws_iam_policy uses
// CreatePolicyVersion/SetDefaultPolicyVersion when a policy document changes,
// aws_iam_role.permissions_boundary uses Put/DeleteRolePermissionsBoundary,
// and audit tooling reads ListEntitiesForPolicy / GetContextKeysForPolicy.

// IAMPolicyVersion is one stored version of a managed policy. Version v1 is
// created implicitly by CreatePolicy (its document lives on IAMPolicy until a
// second version exists); CreatePolicyVersion adds v2..v5. The default version
// is tracked by IAMPolicy.DefaultVersionId.
type IAMPolicyVersion struct {
	PolicyArn  string `json:"policyArn"`
	VersionId  string `json:"versionId"`
	Document   string `json:"document"` // URL-decoded JSON
	CreateDate string `json:"createDate"`
}

// IAMInstanceProfileTagSet holds the tags attached to one instance profile.
// The IAMInstanceProfile struct (iam.go) has no Tags field, so tags are kept
// in their own store keyed by instance-profile name.
type IAMInstanceProfileTagSet struct {
	InstanceProfileName string   `json:"instanceProfileName"`
	Tags                []IAMTag `json:"tags"`
}

var (
	iamPolicyVersions     sim.Store[IAMPolicyVersion]
	iamInstanceProfileTag sim.Store[IAMInstanceProfileTagSet]
)

func registerIAMRolesPolicies(r *AWSQueryRouter, srv *sim.Server) {
	iamPolicyVersions = sim.MakeStore[IAMPolicyVersion](srv.DB(), "iam_policy_versions")
	iamInstanceProfileTag = sim.MakeStore[IAMInstanceProfileTagSet](srv.DB(), "iam_instance_profile_tags")

	for action, h := range map[string]http.HandlerFunc{
		"PutRolePermissionsBoundary":        handleIAMPutRolePermissionsBoundary,
		"DeleteRolePermissionsBoundary":     handleIAMDeleteRolePermissionsBoundary,
		"UpdateRoleDescription":             handleIAMUpdateRoleDescription,
		"TagInstanceProfile":                handleIAMTagInstanceProfile,
		"UntagInstanceProfile":              handleIAMUntagInstanceProfile,
		"ListInstanceProfileTags":           handleIAMListInstanceProfileTags,
		"CreatePolicyVersion":               handleIAMCreatePolicyVersion,
		"DeletePolicyVersion":               handleIAMDeletePolicyVersion,
		"SetDefaultPolicyVersion":           handleIAMSetDefaultPolicyVersion,
		"ListEntitiesForPolicy":             handleIAMListEntitiesForPolicy,
		"ListPoliciesGrantingServiceAccess": handleIAMListPoliciesGrantingServiceAccess,
		"TagPolicy":                         handleIAMTagPolicy,
		"UntagPolicy":                       handleIAMUntagPolicy,
		"GetContextKeysForCustomPolicy":     handleIAMGetContextKeysForCustomPolicy,
		"GetContextKeysForPrincipalPolicy":  handleIAMGetContextKeysForPrincipalPolicy,
	} {
		r.Register(action, h)
	}
}

func handleIAMPutRolePermissionsBoundary(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	boundary := r.FormValue("PermissionsBoundary")
	if boundary == "" {
		iamErrorXML(w, "ValidationError", "PermissionsBoundary is required", http.StatusBadRequest)
		return
	}
	if !iamRoles.Update(name, func(role *IAMRole) {
		role.PermissionsBoundaryARN = boundary
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Role %s not found", name), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "PutRolePermissionsBoundary")
}

func handleIAMDeleteRolePermissionsBoundary(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	if !iamRoles.Update(name, func(role *IAMRole) {
		role.PermissionsBoundaryARN = ""
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Role %s not found", name), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "DeleteRolePermissionsBoundary")
}

func handleIAMUpdateRoleDescription(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RoleName")
	desc := r.FormValue("Description")
	role, ok := iamRoles.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Role %s not found", name), http.StatusNotFound)
		return
	}
	role.Description = desc
	iamRoles.Put(name, role)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UpdateRoleDescriptionResponse %s>
  <UpdateRoleDescriptionResult>%s</UpdateRoleDescriptionResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</UpdateRoleDescriptionResponse>`, iamXmlns, iamRoleXML(role), generateUUID())
}

func handleIAMTagInstanceProfile(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("InstanceProfileName")
	if _, ok := iamInstanceProfiles.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Instance Profile %s was not found.", name), http.StatusNotFound)
		return
	}
	newTags := iamParseTags(r)
	set, _ := iamInstanceProfileTag.Get(name)
	set.InstanceProfileName = name
	set.Tags = iamMergeTags(set.Tags, newTags)
	iamInstanceProfileTag.Put(name, set)
	iamEmptyResultXML(w, "TagInstanceProfile")
}

func handleIAMUntagInstanceProfile(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("InstanceProfileName")
	if _, ok := iamInstanceProfiles.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Instance Profile %s was not found.", name), http.StatusNotFound)
		return
	}
	remove := map[string]bool{}
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", i))
		if k == "" {
			break
		}
		remove[k] = true
	}
	set, ok := iamInstanceProfileTag.Get(name)
	if ok {
		kept := set.Tags[:0]
		for _, t := range set.Tags {
			if !remove[t.Key] {
				kept = append(kept, t)
			}
		}
		set.Tags = kept
		iamInstanceProfileTag.Put(name, set)
	}
	iamEmptyResultXML(w, "UntagInstanceProfile")
}

func handleIAMListInstanceProfileTags(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("InstanceProfileName")
	if _, ok := iamInstanceProfiles.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Instance Profile %s was not found.", name), http.StatusNotFound)
		return
	}
	set, _ := iamInstanceProfileTag.Get(name)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListInstanceProfileTagsResponse %s>
  <ListInstanceProfileTagsResult>%s<IsTruncated>false</IsTruncated></ListInstanceProfileTagsResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListInstanceProfileTagsResponse>`, iamXmlns, iamTagsXML(set.Tags), generateUUID())
}

// iamPolicyVersionList returns every stored version of the policy, including
// the implicit v1 (whose document lives on IAMPolicy until a later version is
// created), sorted by ascending numeric version id.
func iamPolicyVersionList(p IAMPolicy) []IAMPolicyVersion {
	versions := iamPolicyVersions.Filter(func(v IAMPolicyVersion) bool {
		return v.PolicyArn == p.Arn
	})
	hasV1 := false
	for _, v := range versions {
		if v.VersionId == "v1" {
			hasV1 = true
		}
	}
	if !hasV1 {
		versions = append(versions, IAMPolicyVersion{
			PolicyArn:  p.Arn,
			VersionId:  "v1",
			Document:   p.PolicyDocument,
			CreateDate: p.CreateDate,
		})
	}
	sort.Slice(versions, func(i, j int) bool {
		return iamVersionNum(versions[i].VersionId) < iamVersionNum(versions[j].VersionId)
	})
	return versions
}

// iamVersionNum parses the numeric portion of a "vN" policy version id.
func iamVersionNum(id string) int {
	return atoiDefault(strings.TrimPrefix(id, "v"), 0)
}

func handleIAMCreatePolicyVersion(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	policy, ok := iamPolicies.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	doc := r.FormValue("PolicyDocument")
	if dec, err := url.QueryUnescape(doc); err == nil {
		doc = dec
	}
	if doc == "" {
		iamErrorXML(w, "ValidationError", "PolicyDocument is required", http.StatusBadRequest)
		return
	}
	if _, err := parseIAMPolicy(doc); err != nil {
		iamErrorXML(w, "MalformedPolicyDocument", "The policy document could not be parsed: "+err.Error(), http.StatusBadRequest)
		return
	}

	existing := iamPolicyVersionList(policy)
	if len(existing) >= 5 {
		iamErrorXML(w, "LimitExceeded",
			fmt.Sprintf("A managed policy can have up to 5 versions. Before you create a new version, you must delete an existing version. Policy %s.", arn),
			http.StatusConflict)
		return
	}
	highest := 0
	for _, v := range existing {
		if n := iamVersionNum(v.VersionId); n > highest {
			highest = n
		}
	}
	newID := fmt.Sprintf("v%d", highest+1)
	createDate := time.Now().UTC().Format(time.RFC3339)
	ver := IAMPolicyVersion{PolicyArn: arn, VersionId: newID, Document: doc, CreateDate: createDate}
	iamPolicyVersions.Put(arn+"#"+newID, ver)

	setAsDefault := strings.EqualFold(r.FormValue("SetAsDefault"), "true")
	if setAsDefault {
		iamPolicies.Update(arn, func(p *IAMPolicy) {
			p.DefaultVersionId = newID
			p.PolicyDocument = doc
		})
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreatePolicyVersionResponse %s>
  <CreatePolicyVersionResult>%s</CreatePolicyVersionResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</CreatePolicyVersionResponse>`, iamXmlns, iamPolicyVersionXML(ver, setAsDefault), generateUUID())
}

// iamPolicyVersionXML renders a <PolicyVersion>. CreatePolicyVersion omits the
// Document element (per the real API); GetPolicyVersion includes it. The
// includeDoc flag distinguishes the two.
func iamPolicyVersionXML(v IAMPolicyVersion, isDefault bool) string {
	return fmt.Sprintf("<PolicyVersion><VersionId>%s</VersionId><IsDefaultVersion>%t</IsDefaultVersion><CreateDate>%s</CreateDate></PolicyVersion>",
		v.VersionId, isDefault, v.CreateDate)
}

func handleIAMDeletePolicyVersion(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	versionID := r.FormValue("VersionId")
	policy, ok := iamPolicies.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	if versionID == policy.DefaultVersionId {
		iamErrorXML(w, "DeleteConflict",
			"Cannot delete the default version of a policy. To delete the default version, you must first set a different version as the default, or you must delete the entire policy.",
			http.StatusConflict)
		return
	}
	if !iamPolicyVersions.Delete(arn + "#" + versionID) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s version %s was not found.", arn, versionID), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "DeletePolicyVersion")
}

func handleIAMSetDefaultPolicyVersion(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	versionID := r.FormValue("VersionId")
	policy, ok := iamPolicies.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	var target *IAMPolicyVersion
	for _, v := range iamPolicyVersionList(policy) {
		if v.VersionId == versionID {
			vv := v
			target = &vv
			break
		}
	}
	if target == nil {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s version %s was not found.", arn, versionID), http.StatusNotFound)
		return
	}
	iamPolicies.Update(arn, func(p *IAMPolicy) {
		p.DefaultVersionId = versionID
		p.PolicyDocument = target.Document
	})
	iamEmptyResultXML(w, "SetDefaultPolicyVersion")
}

func handleIAMTagPolicy(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	newTags := iamParseTags(r)
	if !iamPolicies.Update(arn, func(p *IAMPolicy) {
		p.Tags = iamMergeTags(p.Tags, newTags)
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "TagPolicy")
}

func handleIAMUntagPolicy(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	remove := map[string]bool{}
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", i))
		if k == "" {
			break
		}
		remove[k] = true
	}
	if !iamPolicies.Update(arn, func(p *IAMPolicy) {
		kept := p.Tags[:0]
		for _, t := range p.Tags {
			if !remove[t.Key] {
				kept = append(kept, t)
			}
		}
		p.Tags = kept
	}) {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	iamEmptyResultXML(w, "UntagPolicy")
}

func handleIAMListEntitiesForPolicy(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	if _, ok := iamPolicies.Get(arn); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	filter := r.FormValue("EntityFilter")
	wantUsers := filter == "" || filter == "User"
	wantRoles := filter == "" || filter == "Role"
	wantGroups := filter == "" || filter == "Group"

	var groups strings.Builder
	if wantGroups {
		seen := map[string]bool{}
		for _, ga := range iamGroupAttached.List() {
			if ga.PolicyArn != arn || seen[ga.GroupName] {
				continue
			}
			seen[ga.GroupName] = true
			if g, ok := iamGroups.Get(ga.GroupName); ok {
				fmt.Fprintf(&groups, "<member><GroupName>%s</GroupName><GroupId>%s</GroupId></member>",
					xmlEscape(g.GroupName), g.GroupId)
			}
		}
	}
	var users strings.Builder
	if wantUsers {
		seen := map[string]bool{}
		for _, ua := range iamUserAttached.List() {
			if ua.PolicyArn != arn || seen[ua.UserName] {
				continue
			}
			seen[ua.UserName] = true
			if u, ok := iamUsers.Get(ua.UserName); ok {
				fmt.Fprintf(&users, "<member><UserName>%s</UserName><UserId>%s</UserId></member>",
					xmlEscape(u.UserName), u.UserId)
			}
		}
	}
	var roles strings.Builder
	if wantRoles {
		seen := map[string]bool{}
		for _, ra := range iamAttachedPolicies.List() {
			if ra.PolicyArn != arn || seen[ra.RoleName] {
				continue
			}
			seen[ra.RoleName] = true
			if role, ok := iamRoles.Get(ra.RoleName); ok {
				fmt.Fprintf(&roles, "<member><RoleName>%s</RoleName><RoleId>%s</RoleId></member>",
					xmlEscape(role.RoleName), role.RoleId)
			}
		}
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListEntitiesForPolicyResponse %s>
  <ListEntitiesForPolicyResult>
    <PolicyGroups>%s</PolicyGroups>
    <PolicyUsers>%s</PolicyUsers>
    <PolicyRoles>%s</PolicyRoles>
    <IsTruncated>false</IsTruncated>
  </ListEntitiesForPolicyResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListEntitiesForPolicyResponse>`, iamXmlns, groups.String(), users.String(), roles.String(), generateUUID())
}

// handleIAMListPoliciesGrantingServiceAccess reports, per requested service
// namespace, which of the identity's attached managed + inline policies grant
// at least one action in that namespace. The grant is derived from the real
// policy documents (an action like "s3:*" or "*" matches the "s3" namespace).
func handleIAMListPoliciesGrantingServiceAccess(w http.ResponseWriter, r *http.Request) {
	principalArn := r.FormValue("Arn")
	namespaces := iamQueryList(r, "ServiceNamespaces")
	if len(namespaces) == 0 {
		iamErrorXML(w, "ValidationError", "ServiceNamespaces is required", http.StatusBadRequest)
		return
	}

	type grantingPolicy struct {
		name, policyType, arn, entityType, entityName string
	}
	policies := iamPoliciesForPrincipal(principalArn)

	var entries strings.Builder
	for _, ns := range namespaces {
		var matched []grantingPolicy
		for _, gp := range policies {
			if iamPolicyGrantsNamespace(gp.doc, ns) {
				matched = append(matched, grantingPolicy{
					name:       gp.name,
					policyType: gp.policyType,
					arn:        gp.arn,
					entityType: gp.entityType,
					entityName: gp.entityName,
				})
			}
		}
		var pols strings.Builder
		for _, m := range matched {
			pols.WriteString("<member><PolicyName>" + xmlEscape(m.name) + "</PolicyName><PolicyType>" + m.policyType + "</PolicyType>")
			if m.arn != "" {
				pols.WriteString("<PolicyArn>" + xmlEscape(m.arn) + "</PolicyArn>")
			}
			if m.entityType != "" {
				pols.WriteString("<EntityType>" + m.entityType + "</EntityType><EntityName>" + xmlEscape(m.entityName) + "</EntityName>")
			}
			pols.WriteString("</member>")
		}
		fmt.Fprintf(&entries, "<member><ServiceNamespace>%s</ServiceNamespace><Policies>%s</Policies></member>",
			xmlEscape(ns), pols.String())
	}

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListPoliciesGrantingServiceAccessResponse %s>
  <ListPoliciesGrantingServiceAccessResult>
    <PoliciesGrantingServiceAccess>%s</PoliciesGrantingServiceAccess>
    <IsTruncated>false</IsTruncated>
  </ListPoliciesGrantingServiceAccessResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListPoliciesGrantingServiceAccessResponse>`, iamXmlns, entries.String(), generateUUID())
}

// iamGrantingPolicy bundles a policy document with the metadata
// ListPoliciesGrantingServiceAccess reports for it.
type iamGrantingPolicy struct {
	doc        iamPolicyDoc
	name       string
	policyType string // INLINE | MANAGED
	arn        string
	entityType string // USER | ROLE (inline only)
	entityName string
}

// iamPoliciesForPrincipal collects the managed (attached) and inline policies
// of a user or role principal ARN, with the metadata
// ListPoliciesGrantingServiceAccess reports.
func iamPoliciesForPrincipal(principalArn string) []iamGrantingPolicy {
	var out []iamGrantingPolicy
	addManaged := func(policyArn string) {
		if mp, ok := iamPolicies.Get(policyArn); ok {
			if doc, err := parseIAMPolicy(mp.PolicyDocument); err == nil {
				out = append(out, iamGrantingPolicy{doc: doc, name: mp.PolicyName, policyType: "MANAGED", arn: mp.Arn})
			}
		}
	}
	switch {
	case strings.Contains(principalArn, ":user/"):
		user := principalArn[strings.LastIndex(principalArn, "/")+1:]
		for _, p := range iamUserPolicies.List() {
			if p.UserName != user {
				continue
			}
			if doc, err := parseIAMPolicy(p.PolicyDocument); err == nil {
				out = append(out, iamGrantingPolicy{doc: doc, name: p.PolicyName, policyType: "INLINE", entityType: "USER", entityName: user})
			}
		}
		for _, ua := range iamUserAttached.List() {
			if ua.UserName == user {
				addManaged(ua.PolicyArn)
			}
		}
	case strings.Contains(principalArn, ":role/"):
		role := iamRoleNameFromArn(principalArn)
		for _, p := range iamRolePolicies.List() {
			if p.RoleName != role {
				continue
			}
			if doc, err := parseIAMPolicy(p.PolicyDocument); err == nil {
				out = append(out, iamGrantingPolicy{doc: doc, name: p.PolicyName, policyType: "INLINE", entityType: "ROLE", entityName: role})
			}
		}
		for _, ra := range iamAttachedPolicies.List() {
			if ra.RoleName == role {
				addManaged(ra.PolicyArn)
			}
		}
	}
	return out
}

// iamPolicyGrantsNamespace reports whether any Allow statement's action set
// includes an action in the given service namespace ("s3", "ec2", …). The
// wildcard "*" and a namespace-prefixed glob ("s3:*", "s3:GetObject") match.
func iamPolicyGrantsNamespace(doc iamPolicyDoc, namespace string) bool {
	ns := strings.ToLower(namespace)
	for _, stmt := range doc.Statement {
		if !strings.EqualFold(stmt.Effect, "allow") {
			continue
		}
		for _, a := range stmt.Action {
			if a == "*" {
				return true
			}
			if i := strings.Index(a, ":"); i >= 0 {
				if strings.ToLower(a[:i]) == ns {
					return true
				}
			}
		}
	}
	return false
}

func handleIAMGetContextKeysForCustomPolicy(w http.ResponseWriter, r *http.Request) {
	var docs []iamPolicyDoc
	for _, p := range iamQueryList(r, "PolicyInputList") {
		doc, err := parseIAMPolicy(p)
		if err != nil {
			iamErrorXML(w, "InvalidInput", "Invalid policy document: "+err.Error(), http.StatusBadRequest)
			return
		}
		docs = append(docs, doc)
	}
	iamWriteContextKeys(w, "GetContextKeysForCustomPolicy", docs)
}

func handleIAMGetContextKeysForPrincipalPolicy(w http.ResponseWriter, r *http.Request) {
	src := r.FormValue("PolicySourceArn")
	var docs []iamPolicyDoc
	switch {
	case strings.Contains(src, ":user/"):
		user := src[strings.LastIndex(src, "/")+1:]
		docs = append(docs, iamEffectivePolicyDocsForUser(user)...)
	case strings.Contains(src, ":role/"):
		docs = append(docs, iamPolicyDocsForRole(iamRoleNameFromArn(src))...)
	case strings.Contains(src, ":group/"):
		group := src[strings.LastIndex(src, "/")+1:]
		docs = append(docs, iamPolicyDocsForGroup(group)...)
	}
	for _, p := range iamQueryList(r, "PolicyInputList") {
		if doc, err := parseIAMPolicy(p); err == nil {
			docs = append(docs, doc)
		}
	}
	iamWriteContextKeys(w, "GetContextKeysForPrincipalPolicy", docs)
}

// iamContextKeysOf returns the distinct condition-context keys referenced in a
// set of policy documents' Condition blocks, sorted for a stable response.
func iamContextKeysOf(docs []iamPolicyDoc) []string {
	seen := map[string]bool{}
	var keys []string
	for _, doc := range docs {
		for _, stmt := range doc.Statement {
			for _, kv := range stmt.Condition {
				for key := range kv {
					if !seen[key] {
						seen[key] = true
						keys = append(keys, key)
					}
				}
			}
		}
	}
	sort.Strings(keys)
	return keys
}

func iamWriteContextKeys(w http.ResponseWriter, op string, docs []iamPolicyDoc) {
	var members strings.Builder
	for _, k := range iamContextKeysOf(docs) {
		members.WriteString("<member>" + xmlEscape(k) + "</member>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s>
  <%sResult><ContextKeyNames>%s</ContextKeyNames></%sResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</%sResponse>`, op, iamXmlns, op, members.String(), op, generateUUID(), op)
}

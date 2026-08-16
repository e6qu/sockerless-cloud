package main

// Cloud Resource Manager v1 Organization Policy surface.
//
// Every node of the resource hierarchy — organization, folder, project —
// exposes the same six org-policy methods (getOrgPolicy, setOrgPolicy,
// clearOrgPolicy, getEffectiveOrgPolicy, listOrgPolicies,
// listAvailableOrgPolicyConstraints), all POSTs addressed as colon-verbs on
// the resource. gcloud's `resource-manager org-policies` command group and
// terraform-provider-google's google_project_organization_policy /
// google_folder_organization_policy / google_organization_policy resources
// speak exactly this wire; the org-policy v2 API is a separate service.
//
// A policy is stored against the resource it is set on, and
// getEffectiveOrgPolicy resolves one by walking the hierarchy from the
// organization down to the resource, applying each level in turn — the
// resolution real Organization Policy performs.

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// CRMBooleanPolicy mirrors the cloudresourcemanager#BooleanPolicy (v1)
// message.
type CRMBooleanPolicy struct {
	Enforced bool `json:"enforced,omitempty"`
}

// CRMListPolicy mirrors the cloudresourcemanager#ListPolicy (v1) message.
type CRMListPolicy struct {
	AllowedValues     []string `json:"allowedValues,omitempty"`
	DeniedValues      []string `json:"deniedValues,omitempty"`
	AllValues         string   `json:"allValues,omitempty"`
	SuggestedValue    string   `json:"suggestedValue,omitempty"`
	InheritFromParent bool     `json:"inheritFromParent,omitempty"`
}

// CRMRestoreDefault mirrors the cloudresourcemanager#RestoreDefault (v1)
// message: an empty message whose presence means "discard everything the
// ancestry says and fall back to the constraint's own default".
type CRMRestoreDefault struct{}

// CRMOrgPolicy mirrors the cloudresourcemanager#OrgPolicy (v1) resource.
// booleanPolicy, listPolicy and restoreDefault are the branches of a oneof:
// exactly one is set on a stored policy.
type CRMOrgPolicy struct {
	Version        int                `json:"version,omitempty"`
	Constraint     string             `json:"constraint,omitempty"`
	Etag           string             `json:"etag,omitempty"`
	UpdateTime     string             `json:"updateTime,omitempty"`
	BooleanPolicy  *CRMBooleanPolicy  `json:"booleanPolicy,omitempty"`
	ListPolicy     *CRMListPolicy     `json:"listPolicy,omitempty"`
	RestoreDefault *CRMRestoreDefault `json:"restoreDefault,omitempty"`
}

// CRMOrgPolicyRow is one policy bound to one hierarchy node. The store key is
// "{resource}|{constraint}" so a resource's policies list by prefix and a
// single constraint reads by exact key.
type CRMOrgPolicyRow struct {
	Resource string       `json:"resource"`
	Policy   CRMOrgPolicy `json:"policy"`
}

// crmOrgPolicies holds every org policy set anywhere in the hierarchy.
var crmOrgPolicies sim.Store[CRMOrgPolicyRow]

func crmOrgPolicyKey(resource, constraint string) string {
	return resource + "|" + constraint
}

// CRMBooleanConstraint mirrors the cloudresourcemanager#BooleanConstraint (v1)
// message: empty, its presence marking the constraint as boolean-valued.
type CRMBooleanConstraint struct{}

// CRMListConstraint mirrors the cloudresourcemanager#ListConstraint (v1)
// message. Value-group support ("in:") is an Organization Policy v2 concept
// the v1 Constraint message does not describe, so it has no field here.
type CRMListConstraint struct {
	SuggestedValue string `json:"suggestedValue,omitempty"`
	SupportsUnder  bool   `json:"supportsUnder,omitempty"`
}

// CRMConstraint mirrors the cloudresourcemanager#Constraint (v1) resource.
type CRMConstraint struct {
	Name              string                `json:"name"`
	DisplayName       string                `json:"displayName,omitempty"`
	Description       string                `json:"description,omitempty"`
	Version           int                   `json:"version,omitempty"`
	ConstraintDefault string                `json:"constraintDefault,omitempty"`
	BooleanConstraint *CRMBooleanConstraint `json:"booleanConstraint,omitempty"`
	ListConstraint    *CRMListConstraint    `json:"listConstraint,omitempty"`
}

// The constraints this cloud slice recognizes, by name.
const (
	crmConstraintDisableServiceAccountCreation    = "constraints/iam.disableServiceAccountCreation"
	crmConstraintDisableServiceAccountKeyCreation = "constraints/iam.disableServiceAccountKeyCreation"
	crmConstraintDisableServiceAccountKeyUpload   = "constraints/iam.disableServiceAccountKeyUpload"
	crmConstraintAllowTokenLifetimeExtension      = "constraints/iam.allowServiceAccountCredentialLifetimeExtension"
	crmConstraintRequireOsLogin                   = "constraints/compute.requireOsLogin"
	crmConstraintVMExternalIPAccess               = "constraints/compute.vmExternalIpAccess"
	crmConstraintResourceLocations                = "constraints/gcp.resourceLocations"
)

// crmOrgPolicyCatalog is the set of constraints listAvailableOrgPolicyConstraints
// reports and setOrgPolicy accepts. It covers the services this cloud slice
// implements — Identity and Access Management, Compute Engine, and the
// cross-service gcp.* family — and each entry's display name, description,
// value type and default are Google's own, from the Organization Policy
// constraints reference. A constraint outside this set is not recognized, and
// setting a policy for one is the real API's INVALID_ARGUMENT.
var crmOrgPolicyCatalog = []CRMConstraint{
	{
		Name:              crmConstraintDisableServiceAccountCreation,
		DisplayName:       "Disable service account creation",
		Description:       "This boolean constraint disables the creation of service accounts where this constraint is set to `True`. By default, service accounts can be created by users based on their Cloud IAM roles and permissions.",
		Version:           1,
		ConstraintDefault: "ALLOW",
		BooleanConstraint: &CRMBooleanConstraint{},
	},
	{
		Name:              crmConstraintDisableServiceAccountKeyCreation,
		DisplayName:       "Disable service account key creation",
		Description:       "This constraint, when enforced, blocks service account key creation.",
		Version:           1,
		ConstraintDefault: "ALLOW",
		BooleanConstraint: &CRMBooleanConstraint{},
	},
	{
		Name:              crmConstraintDisableServiceAccountKeyUpload,
		DisplayName:       "Disable service account key upload",
		Description:       "This boolean constraint disables the feature that allows uploading public keys to service accounts where this constraint is set to `True`. By default, users can upload public keys to service accounts based on their Cloud IAM roles and permissions.",
		Version:           1,
		ConstraintDefault: "ALLOW",
		BooleanConstraint: &CRMBooleanConstraint{},
	},
	{
		Name:        crmConstraintAllowTokenLifetimeExtension,
		DisplayName: "Allow extending lifetime of OAuth 2.0 access tokens to up to 12 hours",
		Description: "This list constraint defines the set of service accounts that can be granted OAuth 2.0 access tokens with a lifetime of up to 12 hours. By default, the maximum lifetime for these access tokens is 1 hour. The allowed/denied list of service accounts must specify one or more service account email addresses. Supported prefix: is:",
		Version:     1,
		// No service account may hold a twelve-hour token until one is
		// listed, which is what "By default, the maximum lifetime for these
		// access tokens is 1 hour" says.
		ConstraintDefault: "DENY",
		ListConstraint:    &CRMListConstraint{},
	},
	{
		Name:              crmConstraintRequireOsLogin,
		DisplayName:       "Require OS Login",
		Description:       "This constraint, when enforced, requires enablement of OS Login on all newly created Projects.",
		Version:           1,
		ConstraintDefault: "ALLOW",
		BooleanConstraint: &CRMBooleanConstraint{},
	},
	{
		Name:              crmConstraintVMExternalIPAccess,
		DisplayName:       "Restrict External IPs For VM instances",
		Description:       "This constraint defines whether Compute Engine VM instances are allowed to use IPv4 external IP addresses. By default, all VM instances are allowed to use external IP addresses.",
		Version:           1,
		ConstraintDefault: "ALLOW",
		ListConstraint:    &CRMListConstraint{SupportsUnder: true},
	},
	{
		Name:              crmConstraintResourceLocations,
		DisplayName:       "Google Cloud Platform - Resource Location Restriction",
		Description:       "This constraint defines the set of locations where location-based Google Cloud resources can be created.",
		Version:           1,
		ConstraintDefault: "ALLOW",
		ListConstraint:    &CRMListConstraint{},
	},
}

// crmConstraintByName looks a constraint up in the catalog.
func crmConstraintByName(name string) (CRMConstraint, bool) {
	for _, c := range crmOrgPolicyCatalog {
		if c.Name == name {
			return c, true
		}
	}
	return CRMConstraint{}, false
}

// crmOrgPolicyParent returns the hierarchy parent of a node, or "" at the
// root. Projects and folders carry their parent in the store; an organization
// is the root.
func crmOrgPolicyParent(resource string) string {
	kind, id, found := strings.Cut(resource, "/")
	if !found {
		return ""
	}
	switch kind {
	case "projects":
		if p, ok := crmResolveProject(id); ok {
			return p.Parent
		}
	case "folders":
		if f, ok := crmFolders.Get(resource); ok {
			return f.Parent
		}
	}
	return ""
}

// crmCanonicalHierarchyNode rewrites a node name to the spelling policies are
// stored under. Every Cloud Resource Manager method accepts a project by id or
// by number, so a policy set through one spelling has to be found through the
// other; the project id is the canonical form. Other node kinds have one
// spelling already.
func crmCanonicalHierarchyNode(resource string) string {
	id, ok := strings.CutPrefix(resource, "projects/")
	if !ok {
		return resource
	}
	if p, found := crmResolveProject(id); found {
		return "projects/" + p.ProjectId
	}
	return resource
}

// crmOrgPolicyAncestry returns the hierarchy path from the root down to the
// resource itself, so callers apply policies outermost-first. A cycle in the
// stored parents terminates the walk rather than spinning.
func crmOrgPolicyAncestry(resource string) []string {
	var up []string
	seen := map[string]bool{}
	for node := crmCanonicalHierarchyNode(resource); node != "" && !seen[node]; node = crmOrgPolicyParent(node) {
		seen[node] = true
		up = append(up, node)
	}
	for i, j := 0, len(up)-1; i < j; i, j = i+1, j-1 {
		up[i], up[j] = up[j], up[i]
	}
	return up
}

// crmDefaultOrgPolicy is the policy in force for a constraint no ancestor sets:
// the constraint's own default, expressed in the value type it carries.
func crmDefaultOrgPolicy(c CRMConstraint) CRMOrgPolicy {
	p := CRMOrgPolicy{Constraint: c.Name, Version: c.Version}
	denyByDefault := c.ConstraintDefault == "DENY"
	if c.BooleanConstraint != nil {
		p.BooleanPolicy = &CRMBooleanPolicy{Enforced: denyByDefault}
		return p
	}
	all := "ALLOW"
	if denyByDefault {
		all = "DENY"
	}
	p.ListPolicy = &CRMListPolicy{AllValues: all}
	return p
}

// crmEffectiveOrgPolicy resolves the policy in force at a resource: start from
// the constraint default at the root and apply each ancestor's policy in turn.
// A boolean policy replaces what it inherits. A list policy that does not
// inherit from its parent replaces it too; one that does merges its values
// into the inherited set. restoreDefault at any level discards everything
// above it.
func crmEffectiveOrgPolicy(resource string, c CRMConstraint) CRMOrgPolicy {
	effective := crmDefaultOrgPolicy(c)
	for _, node := range crmOrgPolicyAncestry(resource) {
		row, ok := crmOrgPolicies.Get(crmOrgPolicyKey(node, c.Name))
		if !ok {
			continue
		}
		set := row.Policy
		switch {
		case set.RestoreDefault != nil:
			effective = crmDefaultOrgPolicy(c)
		case set.BooleanPolicy != nil:
			effective.BooleanPolicy = &CRMBooleanPolicy{Enforced: set.BooleanPolicy.Enforced}
			effective.ListPolicy = nil
		case set.ListPolicy != nil:
			effective.ListPolicy = crmMergeListPolicy(effective.ListPolicy, set.ListPolicy)
			effective.BooleanPolicy = nil
		}
	}
	return effective
}

// crmMergeListPolicy applies one level's list policy over what it inherits.
// inheritFromParent=false — the default — makes the level authoritative;
// otherwise the level's allow and deny sets extend the inherited ones.
func crmMergeListPolicy(inherited, set *CRMListPolicy) *CRMListPolicy {
	out := *set
	if !set.InheritFromParent || inherited == nil {
		return &out
	}
	out.AllowedValues = crmUnionValues(inherited.AllowedValues, set.AllowedValues)
	out.DeniedValues = crmUnionValues(inherited.DeniedValues, set.DeniedValues)
	if out.AllValues == "" {
		out.AllValues = inherited.AllValues
	}
	return &out
}

// crmUnionValues appends the values of b that a does not already carry,
// preserving a's order.
func crmUnionValues(a, b []string) []string {
	out := slices.Clone(a)
	for _, v := range b {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// crmOrgPolicyBooleanEnforced reports whether a boolean constraint is enforced
// at a resource. The services this cloud slice implements call it at the point
// the constraint governs, so a policy set through Cloud Resource Manager has
// the effect the constraint describes.
func crmOrgPolicyBooleanEnforced(resource, constraint string) bool {
	c, ok := crmConstraintByName(constraint)
	if !ok || c.BooleanConstraint == nil {
		return false
	}
	p := crmEffectiveOrgPolicy(resource, c)
	return p.BooleanPolicy != nil && p.BooleanPolicy.Enforced
}

// crmOrgPolicyListAllows reports whether a list constraint admits one value at
// a resource. The services this cloud slice implements call it at the point the
// constraint governs, the way crmOrgPolicyBooleanEnforced is called for a
// boolean one.
//
// The evaluation follows the ListPolicy oneof: allValues settles the question
// outright, an allowedValues set admits only its members, a deniedValues set
// admits everything but its members, and an empty list policy leaves the
// constraint's own default in force. Values carry the documented `is:` prefix
// or none — Organization Policy's literal-value spelling.
func crmOrgPolicyListAllows(resource, constraint, value string) bool {
	c, ok := crmConstraintByName(constraint)
	if !ok || c.ListConstraint == nil {
		return false
	}
	p := crmEffectiveOrgPolicy(resource, c)
	if p.ListPolicy == nil {
		return c.ConstraintDefault == "ALLOW"
	}
	switch p.ListPolicy.AllValues {
	case "ALLOW":
		return true
	case "DENY":
		return false
	}
	if len(p.ListPolicy.AllowedValues) > 0 {
		return crmListValueMatches(p.ListPolicy.AllowedValues, value)
	}
	if len(p.ListPolicy.DeniedValues) > 0 {
		return !crmListValueMatches(p.ListPolicy.DeniedValues, value)
	}
	return c.ConstraintDefault == "ALLOW"
}

// crmListValueMatches reports whether a list-policy value set names a literal
// value, accepting the `is:` prefix Organization Policy documents for a literal
// that would otherwise be read as a prefixed form.
func crmListValueMatches(values []string, value string) bool {
	for _, v := range values {
		if strings.TrimPrefix(v, "is:") == value {
			return true
		}
	}
	return false
}

// crmOrgPolicyVerbs is the set of org-policy colon-verbs every hierarchy node
// serves.
var crmOrgPolicyVerbs = map[string]bool{
	"getOrgPolicy":                      true,
	"setOrgPolicy":                      true,
	"clearOrgPolicy":                    true,
	"getEffectiveOrgPolicy":             true,
	"listOrgPolicies":                   true,
	"listAvailableOrgPolicyConstraints": true,
}

// crmOrgPolicyVerb serves one org-policy method against a hierarchy node.
// It reports whether it answered, so a resource's colon-verb fan-in can fall
// through to the resource's own methods.
func crmOrgPolicyVerb(w http.ResponseWriter, r *http.Request, resource, action string) bool {
	if !crmOrgPolicyVerbs[action] {
		return false
	}
	// Store and read every policy under the node's canonical spelling, so a
	// project addressed by number reaches the policies set on it by id.
	var req struct {
		Constraint string        `json:"constraint"`
		Etag       string        `json:"etag"`
		Policy     *CRMOrgPolicy `json:"policy"`
		PageSize   int           `json:"pageSize"`
		PageToken  string        `json:"pageToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return true
	}

	switch action {
	case "listAvailableOrgPolicyConstraints":
		page, next, ok := crmOrgPolicyPage(w, crmOrgPolicyCatalog, req.PageSize, req.PageToken)
		if !ok {
			return true
		}
		resp := map[string]any{"constraints": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
		return true

	case "listOrgPolicies":
		rows := crmOrgPolicies.Filter(func(row CRMOrgPolicyRow) bool { return row.Resource == resource })
		sort.Slice(rows, func(i, j int) bool { return rows[i].Policy.Constraint < rows[j].Policy.Constraint })
		policies := make([]CRMOrgPolicy, 0, len(rows))
		for _, row := range rows {
			policies = append(policies, row.Policy)
		}
		page, next, ok := crmOrgPolicyPage(w, policies, req.PageSize, req.PageToken)
		if !ok {
			return true
		}
		resp := map[string]any{"policies": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
		return true
	}

	constraint := req.Constraint
	if action == "setOrgPolicy" {
		if req.Policy == nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Request must contain a policy.")
			return true
		}
		constraint = req.Policy.Constraint
	}
	if constraint == "" {
		sim.GCPError(w, http.StatusBadRequest, "Request must specify a constraint.", "INVALID_ARGUMENT")
		return true
	}
	c, known := crmConstraintByName(constraint)
	if !known {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Constraint %s is not recognized.", constraint)
		return true
	}

	key := crmOrgPolicyKey(resource, c.Name)
	switch action {
	case "getOrgPolicy":
		// A resource with no policy of its own reads back the empty policy for
		// the constraint, not a 404.
		if row, ok := crmOrgPolicies.Get(key); ok {
			sim.WriteJSON(w, http.StatusOK, row.Policy)
			return true
		}
		sim.WriteJSON(w, http.StatusOK, CRMOrgPolicy{Constraint: c.Name})

	case "getEffectiveOrgPolicy":
		sim.WriteJSON(w, http.StatusOK, crmEffectiveOrgPolicy(resource, c))

	case "setOrgPolicy":
		p := *req.Policy
		if msg := crmValidateOrgPolicy(c, p); msg != "" {
			sim.GCPError(w, http.StatusBadRequest, msg, "INVALID_ARGUMENT")
			return true
		}
		if !crmOrgPolicyEtagOK(w, key, p.Etag) {
			return true
		}
		p.Constraint = c.Name
		if p.Version == 0 {
			p.Version = c.Version
		}
		p.Etag = crmEtag()
		p.UpdateTime = nowTimestamp()
		crmOrgPolicies.Put(key, CRMOrgPolicyRow{Resource: resource, Policy: p})
		sim.WriteJSON(w, http.StatusOK, p)

	case "clearOrgPolicy":
		if !crmOrgPolicyEtagOK(w, key, req.Etag) {
			return true
		}
		crmOrgPolicies.Delete(key)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	}
	return true
}

// crmOrgPolicyEtagOK enforces the optimistic concurrency the API documents: an
// empty etag writes unconditionally, a supplied one must match the stored
// policy's.
func crmOrgPolicyEtagOK(w http.ResponseWriter, key, etag string) bool {
	if etag == "" {
		return true
	}
	row, ok := crmOrgPolicies.Get(key)
	if !ok || row.Policy.Etag != etag {
		sim.GCPErrorf(w, http.StatusConflict, "ABORTED",
			"Policy etag does not match the current policy; read the policy again and retry.")
		return false
	}
	return true
}

// crmValidateOrgPolicy rejects a policy whose value type does not match the
// constraint's, and one that sets more than one branch of the oneof. It
// returns the INVALID_ARGUMENT message to answer with, empty when the policy
// is well formed.
func crmValidateOrgPolicy(c CRMConstraint, p CRMOrgPolicy) string {
	set := 0
	for _, present := range []bool{p.BooleanPolicy != nil, p.ListPolicy != nil, p.RestoreDefault != nil} {
		if present {
			set++
		}
	}
	if set == 0 {
		return fmt.Sprintf("Policy for constraint %s must set one of booleanPolicy, listPolicy or restoreDefault.", c.Name)
	}
	if set > 1 {
		return fmt.Sprintf("Policy for constraint %s must set exactly one of booleanPolicy, listPolicy or restoreDefault.", c.Name)
	}
	if p.RestoreDefault != nil {
		return ""
	}
	if c.BooleanConstraint != nil && p.BooleanPolicy == nil {
		return fmt.Sprintf("Constraint %s is a boolean constraint and requires a booleanPolicy.", c.Name)
	}
	if c.ListConstraint != nil && p.ListPolicy == nil {
		return fmt.Sprintf("Constraint %s is a list constraint and requires a listPolicy.", c.Name)
	}
	if p.ListPolicy != nil {
		if p.ListPolicy.AllValues != "" && (len(p.ListPolicy.AllowedValues) > 0 || len(p.ListPolicy.DeniedValues) > 0) {
			return "A listPolicy that sets allValues cannot also set allowedValues or deniedValues."
		}
		if len(p.ListPolicy.AllowedValues) > 0 && len(p.ListPolicy.DeniedValues) > 0 {
			return "A listPolicy cannot set both allowedValues and deniedValues."
		}
		if msg := crmListValuesAdmissible(c, p.ListPolicy); msg != "" {
			return msg
		}
	}
	return ""
}

// crmListValuesAdmissible checks the value prefixes a list policy carries
// against what the constraint declares. "under:" addresses a subtree of the
// resource hierarchy and is admissible only on a constraint whose
// ListConstraint sets supportsUnder.
func crmListValuesAdmissible(c CRMConstraint, l *CRMListPolicy) string {
	if c.ListConstraint != nil && c.ListConstraint.SupportsUnder {
		return ""
	}
	for _, v := range slices.Concat(l.AllowedValues, l.DeniedValues) {
		if strings.HasPrefix(v, "under:") {
			return fmt.Sprintf("Constraint %s does not support the \"under:\" value prefix.", c.Name)
		}
	}
	return ""
}

// crmOrgPolicyPage applies the page-size / page-token pair the org-policy list
// methods carry in the request BODY rather than the query string.
func crmOrgPolicyPage[T any](w http.ResponseWriter, items []T, pageSize int, pageToken string) ([]T, string, bool) {
	start := 0
	if pageToken != "" {
		n, err := strconv.Atoi(pageToken)
		if err != nil || n < 0 || n > len(items) {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid page token %q.", pageToken)
			return nil, "", false
		}
		start = n
	}
	rest := items[start:]
	if pageSize <= 0 || pageSize >= len(rest) {
		return rest, "", true
	}
	return rest[:pageSize], strconv.Itoa(start + pageSize), true
}

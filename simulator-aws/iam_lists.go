package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// IAM list ops + tag reads.
//
// ListPolicyVersions is needed by terraform-provider-aws on aws_iam_policy
// plan/refresh/destroy. ListRoles enumerates roles for audit tooling and
// data.aws_iam_roles. ListRoleTags / ListPolicyTags read back the tags that
// CreateRole / CreatePolicy now persist.

type IAMTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

const iamXmlns = `xmlns="https://iam.amazonaws.com/doc/2010-05-08/"`

func registerIAMLists(r *sim.AWSQueryRouter) {
	r.Register("ListPolicyVersions", handleIAMListPolicyVersions)
	r.Register("ListRoles", handleIAMListRoles)
	r.Register("ListRoleTags", handleIAMListRoleTags)
	r.Register("ListPolicyTags", handleIAMListPolicyTags)
}

func iamParseTags(r *http.Request) []IAMTag {
	var tags []IAMTag
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
		if key == "" {
			break
		}
		tags = append(tags, IAMTag{Key: key, Value: r.FormValue(fmt.Sprintf("Tags.member.%d.Value", i))})
	}
	return tags
}

// iamMarkerXML renders the optional `<Marker>` element IAM list ops emit when
// the result is truncated. An empty next token yields no element.
func iamMarkerXML(next string) string {
	if next == "" {
		return ""
	}
	return "<Marker>" + xmlEscape(next) + "</Marker>"
}

func iamTagsXML(tags []IAMTag) string {
	var b strings.Builder
	b.WriteString("<Tags>")
	for _, t := range tags {
		fmt.Fprintf(&b, "<member><Key>%s</Key><Value>%s</Value></member>", xmlEscape(t.Key), xmlEscape(t.Value))
	}
	b.WriteString("</Tags>")
	return b.String()
}

// handleIAMListPolicyVersions returns the policy's versions. The sim's default
// model stores a single version (v1, the default); explicit CreatePolicyVersion
// is not modeled, so one version is returned.
func handleIAMListPolicyVersions(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	policy, ok := iamPolicies.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	// Enumerate every version: v1 is implicit (the policy's own document), v2..vN
	// live in the iamPolicyVersions store CreatePolicyVersion writes to. The
	// default is whichever the policy currently points at (v1 when unset).
	defaultID := policy.DefaultVersionId
	if defaultID == "" {
		defaultID = "v1"
	}
	versions := []IAMPolicyVersion{{VersionId: "v1", CreateDate: policy.CreateDate}}
	for _, v := range iamPolicyVersions.List() {
		if v.PolicyArn == arn {
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		return iamVersionNum(versions[i].VersionId) < iamVersionNum(versions[j].VersionId)
	})
	var members strings.Builder
	for _, v := range versions {
		fmt.Fprintf(&members, "<member><VersionId>%s</VersionId><IsDefaultVersion>%t</IsDefaultVersion><CreateDate>%s</CreateDate></member>",
			v.VersionId, v.VersionId == defaultID, v.CreateDate)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListPolicyVersionsResponse %s>
  <ListPolicyVersionsResult><Versions>%s</Versions><IsTruncated>false</IsTruncated></ListPolicyVersionsResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListPolicyVersionsResponse>`, iamXmlns, members.String(), generateUUID())
}

func handleIAMListRoles(w http.ResponseWriter, r *http.Request) {
	prefix := r.FormValue("PathPrefix")
	var roles []IAMRole
	for _, role := range iamRoles.List() {
		if prefix != "" && !strings.HasPrefix(role.Path, prefix) {
			continue
		}
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].RoleName < roles[j].RoleName })

	maxItems := 0
	if mi := r.FormValue("MaxItems"); mi != "" {
		n, err := strconv.Atoi(mi)
		if err != nil {
			iamErrorXML(w, "ValidationError",
				fmt.Sprintf("Value '%s' at 'maxItems' failed to satisfy constraint: Member must be a valid integer.", mi),
				http.StatusBadRequest)
			return
		}
		maxItems = n
	}
	page, next := awsPage(roles, r.FormValue("Marker"), maxItems, 100)

	var members strings.Builder
	for _, role := range page {
		members.WriteString("<member>" + iamRoleFieldsXML(role) + "</member>")
	}
	marker := ""
	if next != "" {
		marker = "<Marker>" + xmlEscape(next) + "</Marker>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListRolesResponse %s>
  <ListRolesResult><Roles>%s</Roles><IsTruncated>%t</IsTruncated>%s</ListRolesResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListRolesResponse>`, iamXmlns, members.String(), next != "", marker, generateUUID())
}

func handleIAMListRoleTags(w http.ResponseWriter, r *http.Request) {
	role, ok := iamRoles.Get(r.FormValue("RoleName"))
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The role with name %s cannot be found.", r.FormValue("RoleName")), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListRoleTagsResponse %s>
  <ListRoleTagsResult>%s<IsTruncated>false</IsTruncated></ListRoleTagsResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListRoleTagsResponse>`, iamXmlns, iamTagsXML(role.Tags), generateUUID())
}

func handleIAMListPolicyTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("PolicyArn")
	policy, ok := iamPolicies.Get(arn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Policy %s was not found.", arn), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListPolicyTagsResponse %s>
  <ListPolicyTagsResult>%s<IsTruncated>false</IsTruncated></ListPolicyTagsResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</ListPolicyTagsResponse>`, iamXmlns, iamTagsXML(policy.Tags), generateUUID())
}

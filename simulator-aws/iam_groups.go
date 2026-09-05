package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// IAM groups + the cross-principal effective-policy resolution that call-time
// enforcement and STS use. A user's effective policies are its own inline +
// attached policies PLUS the inline + attached policies of every group it
// belongs to (real IAM group inheritance). Role policy docs are resolved the
// same way the SimulatePrincipalPolicy path does.

type IAMGroup struct {
	GroupName  string
	GroupId    string
	Arn        string
	Path       string
	CreateDate string
}

type IAMGroupMember struct {
	GroupName string
	UserName  string
}

type IAMGroupPolicy struct {
	GroupName      string
	PolicyName     string
	PolicyDocument string
}

type IAMGroupAttachedPolicy struct {
	GroupName  string
	PolicyArn  string
	PolicyName string
}

// IAMUserBoundary records a user's permission-boundary managed-policy ARN.
type IAMUserBoundary struct {
	UserName  string
	PolicyArn string
}

var (
	iamGroups         sim.Store[IAMGroup]
	iamGroupMembers   sim.Store[IAMGroupMember]
	iamGroupPolicies  sim.Store[IAMGroupPolicy]
	iamGroupAttached  sim.Store[IAMGroupAttachedPolicy]
	iamUserBoundaries sim.Store[IAMUserBoundary]
)

func registerIAMGroups(r *AWSQueryRouter, srv *sim.Server) {
	iamGroups = sim.MakeStore[IAMGroup](srv.DB(), "iam_groups")
	iamGroupMembers = sim.MakeStore[IAMGroupMember](srv.DB(), "iam_group_members")
	iamGroupPolicies = sim.MakeStore[IAMGroupPolicy](srv.DB(), "iam_group_policies")
	iamGroupAttached = sim.MakeStore[IAMGroupAttachedPolicy](srv.DB(), "iam_group_attached_policies")
	iamUserBoundaries = sim.MakeStore[IAMUserBoundary](srv.DB(), "iam_user_boundaries")
	r.Register("PutUserPermissionsBoundary", handleIAMPutUserBoundary)
	r.Register("DeleteUserPermissionsBoundary", handleIAMDeleteUserBoundary)

	r.Register("CreateGroup", handleIAMCreateGroup)
	r.Register("GetGroup", handleIAMGetGroup)
	r.Register("DeleteGroup", handleIAMDeleteGroup)
	r.Register("ListGroups", handleIAMListGroups)
	r.Register("AddUserToGroup", handleIAMAddUserToGroup)
	r.Register("RemoveUserFromGroup", handleIAMRemoveUserFromGroup)
	r.Register("ListGroupsForUser", handleIAMListGroupsForUser)
	r.Register("PutGroupPolicy", handleIAMPutGroupPolicy)
	r.Register("GetGroupPolicy", handleIAMGetGroupPolicy)
	r.Register("DeleteGroupPolicy", handleIAMDeleteGroupPolicy)
	r.Register("ListGroupPolicies", handleIAMListGroupPolicies)
	r.Register("AttachGroupPolicy", handleIAMAttachGroupPolicy)
	r.Register("DetachGroupPolicy", handleIAMDetachGroupPolicy)
	r.Register("ListAttachedGroupPolicies", handleIAMListAttachedGroupPolicies)
	r.Register("ListUsers", handleIAMListUsers)
}

// iamPolicyDocsForRole collects a role's inline + attached managed policy docs.
func iamPolicyDocsForRole(roleName string) []iamPolicyDoc {
	var docs []iamPolicyDoc
	for _, rp := range iamRolePolicies.List() {
		if rp.RoleName == roleName {
			if doc, err := parseIAMPolicy(rp.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	for _, ap := range iamAttachedPolicies.List() {
		if ap.RoleName != roleName {
			continue
		}
		if mp, ok := iamPolicies.Get(ap.PolicyArn); ok {
			if doc, err := parseIAMPolicy(mp.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	return docs
}

// iamPermissionBoundaryDocs returns the policy documents of a user's permission
// boundary (empty when none is set). A boundary caps the user's effective
// permissions to the intersection of its identity policies and the boundary.
func iamPermissionBoundaryDocs(userName string) []iamPolicyDoc {
	if userName == "" {
		return nil
	}
	b, ok := iamUserBoundaries.Get(userName)
	if !ok {
		return nil
	}
	mp, ok := iamPolicies.Get(b.PolicyArn)
	if !ok {
		return nil
	}
	if doc, err := parseIAMPolicy(mp.PolicyDocument); err == nil {
		return []iamPolicyDoc{doc}
	}
	return nil
}

func handleIAMPutUserBoundary(w http.ResponseWriter, r *http.Request) {
	user := r.FormValue("UserName")
	if _, ok := iamUsers.Get(user); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("User %s not found", user), http.StatusNotFound)
		return
	}
	iamUserBoundaries.Put(user, IAMUserBoundary{UserName: user, PolicyArn: r.FormValue("PermissionsBoundary")})
	iamEmptyResultXML(w, "PutUserPermissionsBoundary")
}

func handleIAMDeleteUserBoundary(w http.ResponseWriter, r *http.Request) {
	iamUserBoundaries.Delete(r.FormValue("UserName"))
	iamEmptyResultXML(w, "DeleteUserPermissionsBoundary")
}

// iamEffectivePolicyDocsForUser is the user's own policies plus the policies of
// every group the user belongs to (IAM group inheritance).
func iamEffectivePolicyDocsForUser(userName string) []iamPolicyDoc {
	docs := iamPolicyDocsForUser(userName)
	for _, m := range iamGroupMembers.List() {
		if m.UserName != userName {
			continue
		}
		docs = append(docs, iamPolicyDocsForGroup(m.GroupName)...)
	}
	return docs
}

func iamPolicyDocsForGroup(groupName string) []iamPolicyDoc {
	var docs []iamPolicyDoc
	for _, gp := range iamGroupPolicies.List() {
		if gp.GroupName == groupName {
			if doc, err := parseIAMPolicy(gp.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	for _, ga := range iamGroupAttached.List() {
		if ga.GroupName != groupName {
			continue
		}
		if mp, ok := iamPolicies.Get(ga.PolicyArn); ok {
			if doc, err := parseIAMPolicy(mp.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	return docs
}

func iamGroupArn(name, path string) string {
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("arn:aws:iam::%s:group%s%s", awsAccountID(), path, name)
}

func handleIAMCreateGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("GroupName")
	if name == "" {
		iamErrorXML(w, "ValidationError", "GroupName is required", http.StatusBadRequest)
		return
	}
	if _, ok := iamGroups.Get(name); ok {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("Group with name %s already exists.", name), http.StatusConflict)
		return
	}
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}
	g := IAMGroup{GroupName: name, GroupId: "AGPA" + strings.ToUpper(generateUUID()[:16]), Arn: iamGroupArn(name, path), Path: path, CreateDate: time.Now().UTC().Format(time.RFC3339)}
	iamGroups.Put(name, g)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateGroupResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><CreateGroupResult>%s</CreateGroupResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></CreateGroupResponse>`,
		iamGroupXML(g), generateUUID())
}

func handleIAMGetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("GroupName")
	g, ok := iamGroups.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", name), http.StatusNotFound)
		return
	}
	var members strings.Builder
	for _, m := range iamGroupMembers.List() {
		if m.GroupName != name {
			continue
		}
		if u, uok := iamUsers.Get(m.UserName); uok {
			fmt.Fprintf(&members, `<member>%s</member>`, iamUserInnerXML(u))
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetGroupResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetGroupResult>%s<Users>%s</Users><IsTruncated>false</IsTruncated></GetGroupResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetGroupResponse>`,
		iamGroupXML(g), members.String(), generateUUID())
}

func handleIAMDeleteGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("GroupName")
	if _, ok := iamGroups.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", name), http.StatusNotFound)
		return
	}
	iamGroups.Delete(name)
	iamEmptyResultXML(w, "DeleteGroup")
}

func handleIAMListGroups(w http.ResponseWriter, r *http.Request) {
	var members strings.Builder
	for _, g := range iamGroups.List() {
		fmt.Fprintf(&members, `<member>%s</member>`, iamGroupInnerXML(g))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListGroupsResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListGroupsResult><Groups>%s</Groups><IsTruncated>false</IsTruncated></ListGroupsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListGroupsResponse>`,
		members.String(), generateUUID())
}

func handleIAMAddUserToGroup(w http.ResponseWriter, r *http.Request) {
	group, user := r.FormValue("GroupName"), r.FormValue("UserName")
	if _, ok := iamGroups.Get(group); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", group), http.StatusNotFound)
		return
	}
	if _, ok := iamUsers.Get(user); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("User %s not found", user), http.StatusNotFound)
		return
	}
	iamGroupMembers.Put(group+"/"+user, IAMGroupMember{GroupName: group, UserName: user})
	iamEmptyResultXML(w, "AddUserToGroup")
}

func handleIAMRemoveUserFromGroup(w http.ResponseWriter, r *http.Request) {
	iamGroupMembers.Delete(r.FormValue("GroupName") + "/" + r.FormValue("UserName"))
	iamEmptyResultXML(w, "RemoveUserFromGroup")
}

func handleIAMListGroupsForUser(w http.ResponseWriter, r *http.Request) {
	user := r.FormValue("UserName")
	var members strings.Builder
	for _, m := range iamGroupMembers.List() {
		if m.UserName != user {
			continue
		}
		if g, ok := iamGroups.Get(m.GroupName); ok {
			fmt.Fprintf(&members, `<member>%s</member>`, iamGroupInnerXML(g))
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListGroupsForUserResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListGroupsForUserResult><Groups>%s</Groups><IsTruncated>false</IsTruncated></ListGroupsForUserResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListGroupsForUserResponse>`,
		members.String(), generateUUID())
}

func handleIAMPutGroupPolicy(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("GroupName")
	if _, ok := iamGroups.Get(group); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", group), http.StatusNotFound)
		return
	}
	doc := r.FormValue("PolicyDocument")
	if dec, err := url.QueryUnescape(doc); err == nil {
		doc = dec
	}
	if _, err := parseIAMPolicy(doc); err != nil {
		iamErrorXML(w, "MalformedPolicyDocument", "The policy could not be parsed: "+err.Error(), http.StatusBadRequest)
		return
	}
	iamGroupPolicies.Put(group+"/"+r.FormValue("PolicyName"), IAMGroupPolicy{GroupName: group, PolicyName: r.FormValue("PolicyName"), PolicyDocument: doc})
	iamEmptyResultXML(w, "PutGroupPolicy")
}

func handleIAMGetGroupPolicy(w http.ResponseWriter, r *http.Request) {
	group, pn := r.FormValue("GroupName"), r.FormValue("PolicyName")
	p, ok := iamGroupPolicies.Get(group + "/" + pn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "Policy not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetGroupPolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetGroupPolicyResult><GroupName>%s</GroupName><PolicyName>%s</PolicyName><PolicyDocument>%s</PolicyDocument></GetGroupPolicyResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetGroupPolicyResponse>`,
		xmlEscape(group), xmlEscape(pn), url.QueryEscape(p.PolicyDocument), generateUUID())
}

func handleIAMDeleteGroupPolicy(w http.ResponseWriter, r *http.Request) {
	iamGroupPolicies.Delete(r.FormValue("GroupName") + "/" + r.FormValue("PolicyName"))
	iamEmptyResultXML(w, "DeleteGroupPolicy")
}

func handleIAMListGroupPolicies(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("GroupName")
	var members strings.Builder
	for _, p := range iamGroupPolicies.List() {
		if p.GroupName == group {
			members.WriteString("<member>" + xmlEscape(p.PolicyName) + "</member>")
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListGroupPoliciesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListGroupPoliciesResult><PolicyNames>%s</PolicyNames><IsTruncated>false</IsTruncated></ListGroupPoliciesResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListGroupPoliciesResponse>`,
		members.String(), generateUUID())
}

func handleIAMAttachGroupPolicy(w http.ResponseWriter, r *http.Request) {
	group, arn := r.FormValue("GroupName"), r.FormValue("PolicyArn")
	if _, ok := iamGroups.Get(group); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", group), http.StatusNotFound)
		return
	}
	pn := arn
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		pn = arn[i+1:]
	}
	iamGroupAttached.Put(group+"/"+arn, IAMGroupAttachedPolicy{GroupName: group, PolicyArn: arn, PolicyName: pn})
	iamEmptyResultXML(w, "AttachGroupPolicy")
}

func handleIAMDetachGroupPolicy(w http.ResponseWriter, r *http.Request) {
	iamGroupAttached.Delete(r.FormValue("GroupName") + "/" + r.FormValue("PolicyArn"))
	iamEmptyResultXML(w, "DetachGroupPolicy")
}

func handleIAMListAttachedGroupPolicies(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("GroupName")
	var members strings.Builder
	for _, ga := range iamGroupAttached.List() {
		if ga.GroupName == group {
			fmt.Fprintf(&members, `<member><PolicyName>%s</PolicyName><PolicyArn>%s</PolicyArn></member>`, xmlEscape(ga.PolicyName), xmlEscape(ga.PolicyArn))
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListAttachedGroupPoliciesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListAttachedGroupPoliciesResult><AttachedPolicies>%s</AttachedPolicies><IsTruncated>false</IsTruncated></ListAttachedGroupPoliciesResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListAttachedGroupPoliciesResponse>`,
		members.String(), generateUUID())
}

func handleIAMListUsers(w http.ResponseWriter, r *http.Request) {
	prefix := r.FormValue("PathPrefix")
	var members strings.Builder
	for _, u := range iamUsers.List() {
		if prefix != "" && !strings.HasPrefix(u.Path, prefix) {
			continue
		}
		fmt.Fprintf(&members, `<member>%s</member>`, iamUserInnerXML(u))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListUsersResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListUsersResult><Users>%s</Users><IsTruncated>false</IsTruncated></ListUsersResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListUsersResponse>`,
		members.String(), generateUUID())
}

func iamGroupXML(g IAMGroup) string {
	return "<Group>" + iamGroupInnerXML(g) + "</Group>"
}

func iamGroupInnerXML(g IAMGroup) string {
	return fmt.Sprintf("<Path>%s</Path><GroupName>%s</GroupName><GroupId>%s</GroupId><Arn>%s</Arn><CreateDate>%s</CreateDate>",
		xmlEscape(g.Path), xmlEscape(g.GroupName), g.GroupId, xmlEscape(g.Arn), g.CreateDate)
}

// iamUserInnerXML is the user's fields without the <User> wrapper, for embedding
// inside <member> elements (ListUsers, GetGroup Users list).
func iamUserInnerXML(u IAMUser) string {
	return fmt.Sprintf("<Path>%s</Path><UserName>%s</UserName><UserId>%s</UserId><Arn>%s</Arn><CreateDate>%s</CreateDate>",
		xmlEscape(u.Path), xmlEscape(u.UserName), u.UserId, xmlEscape(u.Arn), u.CreateDate)
}

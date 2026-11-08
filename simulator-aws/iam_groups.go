package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A user's effective policies are its own inline and attached policies plus
// those of every group it belongs to.

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

func iamPolicyDocsForRole(acct string, roleName string) []iamPolicyDoc {
	var docs []iamPolicyDoc
	for _, row := range iamRolePoliciesIn(acct).ListPrefix(iamOwnedKeyPrefix(roleName)) {
		if rp := row.Item; rp.RoleName == roleName {
			if doc, err := parseIAMPolicy(rp.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	for _, row := range iamAttachedPoliciesIn(acct).ListPrefix(iamOwnedKeyPrefix(roleName)) {
		ap := row.Item
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

// iamPermissionBoundaryDocs returns a user's permissions boundary documents,
// empty when none is set. The boundary caps effective permissions at its
// intersection with the identity policies.
func iamPermissionBoundaryDocs(acct string, userName string) []iamPolicyDoc {
	if userName == "" {
		return nil
	}
	b, ok := iamUserBoundariesIn(acct).Get(userName)
	if !ok {
		return nil
	}
	return iamBoundaryPolicyDocs(b.PolicyArn)
}

// iamRolePermissionBoundaryDocs returns a role's permissions boundary
// documents, which cap every session assumed from the role.
func iamRolePermissionBoundaryDocs(acct string, roleName string) []iamPolicyDoc {
	role, ok := iamRolesIn(acct).Get(roleName)
	if !ok || role.PermissionsBoundaryARN == "" {
		return nil
	}
	return iamBoundaryPolicyDocs(role.PermissionsBoundaryARN)
}

func iamBoundaryPolicyDocs(policyArn string) []iamPolicyDoc {
	mp, ok := iamPolicies.Get(policyArn)
	if !ok {
		return nil
	}
	if doc, err := parseIAMPolicy(mp.PolicyDocument); err == nil {
		return []iamPolicyDoc{doc}
	}
	return nil
}

func handleIAMPutUserBoundary(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	user := r.FormValue("UserName")
	if _, ok := iamUsersIn(acct).Get(user); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("User %s not found", user), http.StatusNotFound)
		return
	}
	iamUserBoundariesIn(acct).Put(user, IAMUserBoundary{UserName: user, PolicyArn: r.FormValue("PermissionsBoundary")})
	iamEmptyResultXML(w, "PutUserPermissionsBoundary")
}

func handleIAMDeleteUserBoundary(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	iamUserBoundariesIn(acct).Delete(r.FormValue("UserName"))
	iamEmptyResultXML(w, "DeleteUserPermissionsBoundary")
}

func iamEffectivePolicyDocsForUser(acct string, userName string) []iamPolicyDoc {
	docs := iamPolicyDocsForUser(acct, userName)
	for _, m := range iamGroupMembershipsOf(acct, userName) {
		docs = append(docs, iamPolicyDocsForGroup(acct, m.GroupName)...)
	}
	return docs
}

// iamGroupMembersByUser answers which groups a management account user
// belongs to; the store is keyed by group first. A member account's
// memberships are few enough to scan.
var iamGroupMembersByUser sim.GenerationIndex[IAMGroupMember]

func iamGroupMembershipsOf(acct, userName string) []IAMGroupMember {
	if !iamIsManagementAccount(acct) {
		return iamGroupMembersIn(acct).Filter(func(m IAMGroupMember) bool { return m.UserName == userName })
	}
	return iamGroupMembersByUser.LookupAll(iamGroupMembers, userName, func(m IAMGroupMember) []string {
		return []string{m.UserName}
	})
}

// iamOwnedKeyPrefix is the key prefix of the rows a user, group or role owns
// in the stores keyed "<owner>/<policy>". IAM names cannot contain "/", so the
// prefix names exactly one owner.
func iamOwnedKeyPrefix(owner string) string {
	return owner + "/"
}

func iamPolicyDocsForGroup(acct string, groupName string) []iamPolicyDoc {
	var docs []iamPolicyDoc
	for _, row := range iamGroupPoliciesIn(acct).ListPrefix(iamOwnedKeyPrefix(groupName)) {
		if gp := row.Item; gp.GroupName == groupName {
			if doc, err := parseIAMPolicy(gp.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	for _, row := range iamGroupAttachedIn(acct).ListPrefix(iamOwnedKeyPrefix(groupName)) {
		ga := row.Item
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

func iamGroupArn(acct string, name, path string) string {
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("arn:aws:iam::%s:group%s%s", acct, path, name)
}

func handleIAMCreateGroup(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	name := r.FormValue("GroupName")
	if name == "" {
		iamErrorXML(w, "ValidationError", "GroupName is required", http.StatusBadRequest)
		return
	}
	if _, ok := iamGroupsIn(acct).Get(name); ok {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("Group with name %s already exists.", name), http.StatusConflict)
		return
	}
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}
	g := IAMGroup{GroupName: name, GroupId: "AGPA" + strings.ToUpper(sim.NewUUID()[:16]), Arn: iamGroupArn(acct, name, path), Path: path, CreateDate: time.Now().UTC().Format(time.RFC3339)}
	iamGroupsIn(acct).Put(name, g)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateGroupResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><CreateGroupResult>%s</CreateGroupResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></CreateGroupResponse>`,
		iamGroupXML(g), sim.NewUUID())
}

func handleIAMGetGroup(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	name := r.FormValue("GroupName")
	g, ok := iamGroupsIn(acct).Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", name), http.StatusNotFound)
		return
	}
	var members strings.Builder
	for _, m := range iamGroupMembersIn(acct).List() {
		if m.GroupName != name {
			continue
		}
		if u, uok := iamUsersIn(acct).Get(m.UserName); uok {
			fmt.Fprintf(&members, `<member>%s</member>`, iamUserInnerXML(u))
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetGroupResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetGroupResult>%s<Users>%s</Users><IsTruncated>false</IsTruncated></GetGroupResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetGroupResponse>`,
		iamGroupXML(g), members.String(), sim.NewUUID())
}

func handleIAMDeleteGroup(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	name := r.FormValue("GroupName")
	if _, ok := iamGroupsIn(acct).Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", name), http.StatusNotFound)
		return
	}
	iamGroupsIn(acct).Delete(name)
	iamEmptyResultXML(w, "DeleteGroup")
}

func handleIAMListGroups(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	var members strings.Builder
	for _, g := range iamGroupsIn(acct).List() {
		fmt.Fprintf(&members, `<member>%s</member>`, iamGroupInnerXML(g))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListGroupsResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListGroupsResult><Groups>%s</Groups><IsTruncated>false</IsTruncated></ListGroupsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListGroupsResponse>`,
		members.String(), sim.NewUUID())
}

func handleIAMAddUserToGroup(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	group, user := r.FormValue("GroupName"), r.FormValue("UserName")
	if _, ok := iamGroupsIn(acct).Get(group); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", group), http.StatusNotFound)
		return
	}
	if _, ok := iamUsersIn(acct).Get(user); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("User %s not found", user), http.StatusNotFound)
		return
	}
	iamGroupMembersIn(acct).Put(group+"/"+user, IAMGroupMember{GroupName: group, UserName: user})
	iamEmptyResultXML(w, "AddUserToGroup")
}

func handleIAMRemoveUserFromGroup(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	iamGroupMembersIn(acct).Delete(r.FormValue("GroupName") + "/" + r.FormValue("UserName"))
	iamEmptyResultXML(w, "RemoveUserFromGroup")
}

func handleIAMListGroupsForUser(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	user := r.FormValue("UserName")
	var members strings.Builder
	for _, m := range iamGroupMembersIn(acct).List() {
		if m.UserName != user {
			continue
		}
		if g, ok := iamGroupsIn(acct).Get(m.GroupName); ok {
			fmt.Fprintf(&members, `<member>%s</member>`, iamGroupInnerXML(g))
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListGroupsForUserResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListGroupsForUserResult><Groups>%s</Groups><IsTruncated>false</IsTruncated></ListGroupsForUserResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListGroupsForUserResponse>`,
		members.String(), sim.NewUUID())
}

func handleIAMPutGroupPolicy(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	group := r.FormValue("GroupName")
	if _, ok := iamGroupsIn(acct).Get(group); !ok {
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
	iamGroupPoliciesIn(acct).Put(group+"/"+r.FormValue("PolicyName"), IAMGroupPolicy{GroupName: group, PolicyName: r.FormValue("PolicyName"), PolicyDocument: doc})
	iamEmptyResultXML(w, "PutGroupPolicy")
}

func handleIAMGetGroupPolicy(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	group, pn := r.FormValue("GroupName"), r.FormValue("PolicyName")
	p, ok := iamGroupPoliciesIn(acct).Get(group + "/" + pn)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "Policy not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetGroupPolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetGroupPolicyResult><GroupName>%s</GroupName><PolicyName>%s</PolicyName><PolicyDocument>%s</PolicyDocument></GetGroupPolicyResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetGroupPolicyResponse>`,
		xmlEscape(group), xmlEscape(pn), iamEncodePolicyDocument(p.PolicyDocument), sim.NewUUID())
}

func handleIAMDeleteGroupPolicy(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	iamGroupPoliciesIn(acct).Delete(r.FormValue("GroupName") + "/" + r.FormValue("PolicyName"))
	iamEmptyResultXML(w, "DeleteGroupPolicy")
}

func handleIAMListGroupPolicies(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	group := r.FormValue("GroupName")
	var members strings.Builder
	for _, p := range iamGroupPoliciesIn(acct).List() {
		if p.GroupName == group {
			members.WriteString("<member>" + xmlEscape(p.PolicyName) + "</member>")
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListGroupPoliciesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListGroupPoliciesResult><PolicyNames>%s</PolicyNames><IsTruncated>false</IsTruncated></ListGroupPoliciesResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListGroupPoliciesResponse>`,
		members.String(), sim.NewUUID())
}

func handleIAMAttachGroupPolicy(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	group, arn := r.FormValue("GroupName"), r.FormValue("PolicyArn")
	if _, ok := iamGroupsIn(acct).Get(group); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", group), http.StatusNotFound)
		return
	}
	pn := arn
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		pn = arn[i+1:]
	}
	iamGroupAttachedIn(acct).Put(group+"/"+arn, IAMGroupAttachedPolicy{GroupName: group, PolicyArn: arn, PolicyName: pn})
	iamEmptyResultXML(w, "AttachGroupPolicy")
}

func handleIAMDetachGroupPolicy(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	iamGroupAttachedIn(acct).Delete(r.FormValue("GroupName") + "/" + r.FormValue("PolicyArn"))
	iamEmptyResultXML(w, "DetachGroupPolicy")
}

func handleIAMListAttachedGroupPolicies(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	group := r.FormValue("GroupName")
	var members strings.Builder
	for _, ga := range iamGroupAttachedIn(acct).List() {
		if ga.GroupName == group {
			fmt.Fprintf(&members, `<member><PolicyName>%s</PolicyName><PolicyArn>%s</PolicyArn></member>`, xmlEscape(ga.PolicyName), xmlEscape(ga.PolicyArn))
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListAttachedGroupPoliciesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListAttachedGroupPoliciesResult><AttachedPolicies>%s</AttachedPolicies><IsTruncated>false</IsTruncated></ListAttachedGroupPoliciesResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListAttachedGroupPoliciesResponse>`,
		members.String(), sim.NewUUID())
}

func handleIAMListUsers(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	prefix := r.FormValue("PathPrefix")
	var members strings.Builder
	for _, u := range iamUsersIn(acct).List() {
		if prefix != "" && !strings.HasPrefix(u.Path, prefix) {
			continue
		}
		fmt.Fprintf(&members, `<member>%s</member>`, iamUserInnerXML(u))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListUsersResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListUsersResult><Users>%s</Users><IsTruncated>false</IsTruncated></ListUsersResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListUsersResponse>`,
		members.String(), sim.NewUUID())
}

func iamGroupXML(g IAMGroup) string {
	return "<Group>" + iamGroupInnerXML(g) + "</Group>"
}

func iamGroupInnerXML(g IAMGroup) string {
	return fmt.Sprintf("<Path>%s</Path><GroupName>%s</GroupName><GroupId>%s</GroupId><Arn>%s</Arn><CreateDate>%s</CreateDate>",
		xmlEscape(g.Path), xmlEscape(g.GroupName), g.GroupId, xmlEscape(g.Arn), g.CreateDate)
}

// iamUserInnerXML emits the user's fields without the <User> wrapper, for <member> elements.
func iamUserInnerXML(u IAMUser) string {
	return fmt.Sprintf("<Path>%s</Path><UserName>%s</UserName><UserId>%s</UserId><Arn>%s</Arn><CreateDate>%s</CreateDate>",
		xmlEscape(u.Path), xmlEscape(u.UserName), u.UserId, xmlEscape(u.Arn), u.CreateDate)
}

package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// IAM users, access keys, and user policies — the credential→principal→policy
// binding that call-time IAM enforcement (iam_enforcement.go) resolves from a
// request's SigV4 access-key id. Roles already existed (iam.go); this adds the
// user surface plus inline (PutUserPolicy) and managed (AttachUserPolicy)
// attachments, all on the query protocol like the rest of IAM.

type IAMUser struct {
	UserName   string
	UserId     string
	Arn        string
	Path       string
	CreateDate string
	Tags       []IAMTag
}

type IAMAccessKey struct {
	AccessKeyId     string
	SecretAccessKey string
	UserName        string
	Status          string
	CreateDate      string
}

type IAMUserPolicy struct {
	UserName       string
	PolicyName     string
	PolicyDocument string
}

type IAMUserAttachedPolicy struct {
	UserName   string
	PolicyArn  string
	PolicyName string
}

var (
	iamUsers        sim.Store[IAMUser]
	iamAccessKeys   sim.Store[IAMAccessKey]
	iamUserPolicies sim.Store[IAMUserPolicy]
	iamUserAttached sim.Store[IAMUserAttachedPolicy]
)

func registerIAMUsers(r *sim.AWSQueryRouter, srv *sim.Server) {
	iamUsers = sim.MakeStore[IAMUser](srv.DB(), "iam_users")
	iamAccessKeys = sim.MakeStore[IAMAccessKey](srv.DB(), "iam_access_keys")
	iamUserPolicies = sim.MakeStore[IAMUserPolicy](srv.DB(), "iam_user_policies")
	iamUserAttached = sim.MakeStore[IAMUserAttachedPolicy](srv.DB(), "iam_user_attached_policies")

	r.Register("CreateUser", handleIAMCreateUser)
	r.Register("GetUser", handleIAMGetUser)
	r.Register("DeleteUser", handleIAMDeleteUser)
	r.Register("CreateAccessKey", handleIAMCreateAccessKey)
	r.Register("DeleteAccessKey", handleIAMDeleteAccessKey)
	r.Register("ListAccessKeys", handleIAMListAccessKeys)
	r.Register("PutUserPolicy", handleIAMPutUserPolicy)
	r.Register("GetUserPolicy", handleIAMGetUserPolicy)
	r.Register("DeleteUserPolicy", handleIAMDeleteUserPolicy)
	r.Register("ListUserPolicies", handleIAMListUserPolicies)
	r.Register("AttachUserPolicy", handleIAMAttachUserPolicy)
	r.Register("DetachUserPolicy", handleIAMDetachUserPolicy)
	r.Register("ListAttachedUserPolicies", handleIAMListAttachedUserPolicies)

	seedRootAdminCredential()
}

// Bootstrap administrator credential. A real AWS account is created with a root
// credential already able to act; the simulator provisions an equivalent
// well-known administrator so a client has a credential to sign its first
// request with (creating any further IAM key itself requires an existing signed
// caller — the chicken-and-egg the SigV4 gate would otherwise deadlock on).
//
// The access-key/secret pair is the long-standing "test"/"test" coordinate the
// SDK, CLI, and Terraform test surfaces already configure (AWS_ACCESS_KEY_ID),
// so the credential clients present is the credential the simulator verifies
// against — the "differ only in coordinates" rule, with real SigV4 crypto in
// between. It carries an inline AdministratorAccess policy so it authorizes
// every action, matching an account administrator.
const (
	seedAdminUserName  = "sockerless-sim-admin"
	seedAdminAccessKey = "test"
	seedAdminSecretKey = "test"
)

func seedRootAdminCredential() {
	if _, ok := iamAccessKeys.Get(seedAdminAccessKey); ok {
		return // already provisioned (persistent store across restarts)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	iamUsers.Put(seedAdminUserName, IAMUser{
		UserName:   seedAdminUserName,
		UserId:     "AIDAROOTSIMADMIN00000",
		Arn:        iamUserArn(seedAdminUserName, "/"),
		Path:       "/",
		CreateDate: now,
	})
	iamUserPolicies.Put(seedAdminUserName+"/AdministratorAccess", IAMUserPolicy{
		UserName:       seedAdminUserName,
		PolicyName:     "AdministratorAccess",
		PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`,
	})
	iamAccessKeys.Put(seedAdminAccessKey, IAMAccessKey{
		AccessKeyId:     seedAdminAccessKey,
		SecretAccessKey: seedAdminSecretKey,
		UserName:        seedAdminUserName,
		Status:          "Active",
		CreateDate:      now,
	})
}

func iamUserArn(name, path string) string {
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("arn:aws:iam::%s:user%s%s", awsAccountID(), path, name)
}

func handleIAMCreateUser(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	if name == "" {
		iamErrorXML(w, "ValidationError", "UserName is required", http.StatusBadRequest)
		return
	}
	if _, ok := iamUsers.Get(name); ok {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("User with name %s already exists.", name), http.StatusConflict)
		return
	}
	path := r.FormValue("Path")
	if path == "" {
		path = "/"
	}
	user := IAMUser{
		UserName:   name,
		UserId:     "AIDA" + strings.ToUpper(generateUUID()[:16]),
		Arn:        iamUserArn(name, path),
		Path:       path,
		CreateDate: time.Now().UTC().Format(time.RFC3339),
		Tags:       iamParseTags(r),
	}
	iamUsers.Put(name, user)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateUserResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><CreateUserResult>%s</CreateUserResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></CreateUserResponse>`,
		iamUserXML(user), generateUUID())
}

func handleIAMGetUser(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	user, ok := iamUsers.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetUserResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetUserResult>%s</GetUserResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetUserResponse>`,
		iamUserXML(user), generateUUID())
}

func handleIAMDeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	if _, ok := iamUsers.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	// Real IAM refuses to delete a user that still has dependents attached —
	// the caller must detach them first — rather than silently orphaning them.
	if msg := iamUserDeleteConflict(name); msg != "" {
		iamErrorXML(w, "DeleteConflict", msg, http.StatusConflict)
		return
	}
	iamUsers.Delete(name)
	iamEmptyResultXML(w, "DeleteUser")
}

// iamUserDeleteConflict returns a non-empty DeleteConflict message if the user
// still has an attachment real IAM requires removed before deletion.
func iamUserDeleteConflict(name string) string {
	for _, k := range iamAccessKeys.List() {
		if k.UserName == name {
			return "Cannot delete entity, must delete access keys first."
		}
	}
	for _, p := range iamUserPolicies.List() {
		if p.UserName == name {
			return "Cannot delete entity, must delete policies first."
		}
	}
	for _, ap := range iamUserAttached.List() {
		if ap.UserName == name {
			return "Cannot delete entity, must detach all policies first."
		}
	}
	for _, m := range iamGroupMembers.List() {
		if m.UserName == name {
			return "Cannot delete entity, must remove users from all groups first."
		}
	}
	if _, ok := iamLoginProfiles.Get(name); ok {
		return "Cannot delete entity, must delete login profile first."
	}
	return ""
}

func handleIAMCreateAccessKey(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	if _, ok := iamUsers.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	akid := "AKIA" + strings.ToUpper(iamRandomB32(16))
	secret := iamRandomSecret()
	key := IAMAccessKey{
		AccessKeyId:     akid,
		SecretAccessKey: secret,
		UserName:        name,
		Status:          "Active",
		CreateDate:      time.Now().UTC().Format(time.RFC3339),
	}
	iamAccessKeys.Put(akid, key)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateAccessKeyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><CreateAccessKeyResult><AccessKey><UserName>%s</UserName><AccessKeyId>%s</AccessKeyId><Status>Active</Status><SecretAccessKey>%s</SecretAccessKey><CreateDate>%s</CreateDate></AccessKey></CreateAccessKeyResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></CreateAccessKeyResponse>`,
		xmlEscape(name), akid, xmlEscape(secret), key.CreateDate, generateUUID())
}

func handleIAMDeleteAccessKey(w http.ResponseWriter, r *http.Request) {
	akid := r.FormValue("AccessKeyId")
	if _, ok := iamAccessKeys.Get(akid); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Access Key with id %s cannot be found.", akid), http.StatusNotFound)
		return
	}
	iamAccessKeys.Delete(akid)
	iamEmptyResultXML(w, "DeleteAccessKey")
}

func handleIAMListAccessKeys(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	var members strings.Builder
	for _, k := range iamAccessKeys.List() {
		if k.UserName != name {
			continue
		}
		fmt.Fprintf(&members, `<member><UserName>%s</UserName><AccessKeyId>%s</AccessKeyId><Status>%s</Status><CreateDate>%s</CreateDate></member>`,
			xmlEscape(k.UserName), k.AccessKeyId, k.Status, k.CreateDate)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListAccessKeysResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListAccessKeysResult><AccessKeyMetadata>%s</AccessKeyMetadata><IsTruncated>false</IsTruncated></ListAccessKeysResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListAccessKeysResponse>`,
		members.String(), generateUUID())
}

func handleIAMPutUserPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	if _, ok := iamUsers.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	policyName := r.FormValue("PolicyName")
	doc := r.FormValue("PolicyDocument")
	if dec, err := url.QueryUnescape(doc); err == nil {
		doc = dec
	}
	if _, err := parseIAMPolicy(doc); err != nil {
		iamErrorXML(w, "MalformedPolicyDocument", "The policy could not be parsed: "+err.Error(), http.StatusBadRequest)
		return
	}
	iamUserPolicies.Put(name+"/"+policyName, IAMUserPolicy{UserName: name, PolicyName: policyName, PolicyDocument: doc})
	iamEmptyResultXML(w, "PutUserPolicy")
}

func handleIAMGetUserPolicy(w http.ResponseWriter, r *http.Request) {
	name, policyName := r.FormValue("UserName"), r.FormValue("PolicyName")
	p, ok := iamUserPolicies.Get(name + "/" + policyName)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "Policy not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetUserPolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetUserPolicyResult><UserName>%s</UserName><PolicyName>%s</PolicyName><PolicyDocument>%s</PolicyDocument></GetUserPolicyResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetUserPolicyResponse>`,
		xmlEscape(name), xmlEscape(policyName), url.QueryEscape(p.PolicyDocument), generateUUID())
}

func handleIAMDeleteUserPolicy(w http.ResponseWriter, r *http.Request) {
	name, policyName := r.FormValue("UserName"), r.FormValue("PolicyName")
	iamUserPolicies.Delete(name + "/" + policyName)
	iamEmptyResultXML(w, "DeleteUserPolicy")
}

func handleIAMListUserPolicies(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	var members strings.Builder
	for _, p := range iamUserPolicies.List() {
		if p.UserName == name {
			members.WriteString("<member>" + xmlEscape(p.PolicyName) + "</member>")
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListUserPoliciesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListUserPoliciesResult><PolicyNames>%s</PolicyNames><IsTruncated>false</IsTruncated></ListUserPoliciesResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListUserPoliciesResponse>`,
		members.String(), generateUUID())
}

func handleIAMAttachUserPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	if _, ok := iamUsers.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	arn := r.FormValue("PolicyArn")
	pn := arn
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		pn = arn[i+1:]
	}
	iamUserAttached.Put(name+"/"+arn, IAMUserAttachedPolicy{UserName: name, PolicyArn: arn, PolicyName: pn})
	iamEmptyResultXML(w, "AttachUserPolicy")
}

func handleIAMDetachUserPolicy(w http.ResponseWriter, r *http.Request) {
	name, arn := r.FormValue("UserName"), r.FormValue("PolicyArn")
	iamUserAttached.Delete(name + "/" + arn)
	iamEmptyResultXML(w, "DetachUserPolicy")
}

func handleIAMListAttachedUserPolicies(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	var members strings.Builder
	for _, ap := range iamUserAttached.List() {
		if ap.UserName == name {
			fmt.Fprintf(&members, `<member><PolicyName>%s</PolicyName><PolicyArn>%s</PolicyArn></member>`,
				xmlEscape(ap.PolicyName), xmlEscape(ap.PolicyArn))
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListAttachedUserPoliciesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListAttachedUserPoliciesResult><AttachedPolicies>%s</AttachedPolicies><IsTruncated>false</IsTruncated></ListAttachedUserPoliciesResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListAttachedUserPoliciesResponse>`,
		members.String(), generateUUID())
}

func iamUserXML(u IAMUser) string {
	var tags string
	if len(u.Tags) > 0 {
		var b strings.Builder
		b.WriteString("<Tags>")
		for _, t := range u.Tags {
			b.WriteString("<member><Key>" + xmlEscape(t.Key) + "</Key><Value>" + xmlEscape(t.Value) + "</Value></member>")
		}
		b.WriteString("</Tags>")
		tags = b.String()
	}
	return fmt.Sprintf("<User><Path>%s</Path><UserName>%s</UserName><UserId>%s</UserId><Arn>%s</Arn><CreateDate>%s</CreateDate>%s</User>",
		xmlEscape(u.Path), xmlEscape(u.UserName), u.UserId, xmlEscape(u.Arn), u.CreateDate, tags)
}

// iamPolicyDocsForUser collects a user's effective policy documents: inline
// (PutUserPolicy) plus attached managed (AttachUserPolicy → managed policy doc).
func iamPolicyDocsForUser(userName string) []iamPolicyDoc {
	var docs []iamPolicyDoc
	for _, p := range iamUserPolicies.List() {
		if p.UserName == userName {
			if doc, err := parseIAMPolicy(p.PolicyDocument); err == nil {
				docs = append(docs, doc)
			}
		}
	}
	for _, ap := range iamUserAttached.List() {
		if ap.UserName != userName {
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

func iamRandomB32(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

func iamRandomSecret() string {
	b := make([]byte, 30)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)[:40]
}

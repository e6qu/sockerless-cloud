package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// IAM credential and account-management surface that hangs off the existing
// user / group / access-key stores: user and group renames (UpdateUser /
// UpdateGroup), console login profiles (CreateLoginProfile family +
// ChangePassword), access-key status and last-used metadata, user tagging,
// and the account-level password policy singleton.

// IAMLoginProfile is a user's console-password profile. The password itself is
// stored so ChangePassword can validate the caller's current password; IAM
// never returns it.
type IAMLoginProfile struct {
	UserName              string
	Password              string
	CreateDate            string
	PasswordResetRequired bool
}

// IAMPasswordPolicy is the account-level password policy. AWS stores at most one
// per account; the sim keys it under the account id.
type IAMPasswordPolicy struct {
	MinimumPasswordLength      int
	RequireSymbols             bool
	RequireNumbers             bool
	RequireUppercaseCharacters bool
	RequireLowercaseCharacters bool
	AllowUsersToChangePassword bool
	ExpirePasswords            bool
	MaxPasswordAge             int
	PasswordReusePrevention    int
	HardExpiry                 bool
}

var (
	iamLoginProfiles    sim.Store[IAMLoginProfile]
	iamPasswordPolicies sim.Store[IAMPasswordPolicy]
)

func registerIAMUsersCreds(r *AWSQueryRouter, srv *sim.Server) {
	iamLoginProfiles = sim.MakeStore[IAMLoginProfile](srv.DB(), "iam_login_profiles")
	iamPasswordPolicies = sim.MakeStore[IAMPasswordPolicy](srv.DB(), "iam_password_policies")

	for action, h := range map[string]http.HandlerFunc{
		"UpdateUser":                  handleIAMUpdateUser,
		"UpdateGroup":                 handleIAMUpdateGroup,
		"CreateLoginProfile":          handleIAMCreateLoginProfile,
		"GetLoginProfile":             handleIAMGetLoginProfile,
		"UpdateLoginProfile":          handleIAMUpdateLoginProfile,
		"DeleteLoginProfile":          handleIAMDeleteLoginProfile,
		"ChangePassword":              handleIAMChangePassword,
		"UpdateAccessKey":             handleIAMUpdateAccessKey,
		"GetAccessKeyLastUsed":        handleIAMGetAccessKeyLastUsed,
		"TagUser":                     handleIAMTagUser,
		"UntagUser":                   handleIAMUntagUser,
		"ListUserTags":                handleIAMListUserTags,
		"GetAccountPasswordPolicy":    handleIAMGetAccountPasswordPolicy,
		"UpdateAccountPasswordPolicy": handleIAMUpdateAccountPasswordPolicy,
		"DeleteAccountPasswordPolicy": handleIAMDeleteAccountPasswordPolicy,
	} {
		r.Register(action, h)
	}
}

func handleIAMUpdateUser(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	user, ok := iamUsers.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	newName := r.FormValue("NewUserName")
	newPath := r.FormValue("NewPath")
	if newName != "" && newName != name {
		if _, exists := iamUsers.Get(newName); exists {
			iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("User with name %s already exists.", newName), http.StatusConflict)
			return
		}
	}
	if newPath != "" {
		user.Path = newPath
	}
	if newName != "" {
		user.UserName = newName
	}
	user.Arn = iamUserArn(user.UserName, user.Path)
	if newName != "" && newName != name {
		iamUsers.Delete(name)
		// Re-key dependent records that index by user name.
		iamRekeyUser(name, newName)
	}
	iamUsers.Put(user.UserName, user)
	iamEmptyResultXML(w, "UpdateUser")
}

// iamRekeyUser moves a user's login profile, access keys, tags-bearing records,
// and group memberships from the old name to the new name on a rename.
func iamRekeyUser(oldName, newName string) {
	if lp, ok := iamLoginProfiles.Get(oldName); ok {
		lp.UserName = newName
		iamLoginProfiles.Delete(oldName)
		iamLoginProfiles.Put(newName, lp)
	}
	for _, k := range iamAccessKeys.List() {
		if k.UserName == oldName {
			k.UserName = newName
			iamAccessKeys.Put(k.AccessKeyId, k)
		}
	}
	for _, m := range iamGroupMembers.List() {
		if m.UserName == oldName {
			iamGroupMembers.Delete(m.GroupName + "/" + oldName)
			iamGroupMembers.Put(m.GroupName+"/"+newName, IAMGroupMember{GroupName: m.GroupName, UserName: newName})
		}
	}
}

func handleIAMUpdateGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("GroupName")
	g, ok := iamGroups.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", name), http.StatusNotFound)
		return
	}
	newName := r.FormValue("NewGroupName")
	newPath := r.FormValue("NewPath")
	if newName != "" && newName != name {
		if _, exists := iamGroups.Get(newName); exists {
			iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("Group with name %s already exists.", newName), http.StatusConflict)
			return
		}
	}
	if newPath != "" {
		g.Path = newPath
	}
	if newName != "" {
		g.GroupName = newName
	}
	g.Arn = iamGroupArn(g.GroupName, g.Path)
	if newName != "" && newName != name {
		iamGroups.Delete(name)
		for _, m := range iamGroupMembers.List() {
			if m.GroupName == name {
				iamGroupMembers.Delete(name + "/" + m.UserName)
				iamGroupMembers.Put(newName+"/"+m.UserName, IAMGroupMember{GroupName: newName, UserName: m.UserName})
			}
		}
	}
	iamGroups.Put(g.GroupName, g)
	iamEmptyResultXML(w, "UpdateGroup")
}

func iamLoginProfileXML(lp IAMLoginProfile) string {
	return fmt.Sprintf("<LoginProfile><UserName>%s</UserName><CreateDate>%s</CreateDate><PasswordResetRequired>%t</PasswordResetRequired></LoginProfile>",
		xmlEscape(lp.UserName), lp.CreateDate, lp.PasswordResetRequired)
}

func handleIAMCreateLoginProfile(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	if _, ok := iamUsers.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	if _, ok := iamLoginProfiles.Get(name); ok {
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("Login Profile for user %s already exists.", name), http.StatusConflict)
		return
	}
	lp := IAMLoginProfile{
		UserName:              name,
		Password:              r.FormValue("Password"),
		CreateDate:            time.Now().UTC().Format(time.RFC3339),
		PasswordResetRequired: r.FormValue("PasswordResetRequired") == "true",
	}
	iamLoginProfiles.Put(name, lp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateLoginProfileResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><CreateLoginProfileResult>%s</CreateLoginProfileResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></CreateLoginProfileResponse>`,
		iamLoginProfileXML(lp), generateUUID())
}

func handleIAMGetLoginProfile(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	lp, ok := iamLoginProfiles.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Login Profile for user %s cannot be found.", name), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetLoginProfileResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetLoginProfileResult>%s</GetLoginProfileResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetLoginProfileResponse>`,
		iamLoginProfileXML(lp), generateUUID())
}

func handleIAMUpdateLoginProfile(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	lp, ok := iamLoginProfiles.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Login Profile for user %s cannot be found.", name), http.StatusNotFound)
		return
	}
	if pw := r.FormValue("Password"); pw != "" {
		lp.Password = pw
	}
	if v := r.FormValue("PasswordResetRequired"); v != "" {
		lp.PasswordResetRequired = v == "true"
	}
	iamLoginProfiles.Put(name, lp)
	iamEmptyResultXML(w, "UpdateLoginProfile")
}

func handleIAMDeleteLoginProfile(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	if _, ok := iamLoginProfiles.Get(name); !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Login Profile for user %s cannot be found.", name), http.StatusNotFound)
		return
	}
	iamLoginProfiles.Delete(name)
	iamEmptyResultXML(w, "DeleteLoginProfile")
}

// handleIAMChangePassword changes the caller's console password. The sim
// resolves the caller from the SigV4 access-key id and validates the new
// password against the account password policy.
func handleIAMChangePassword(w http.ResponseWriter, r *http.Request) {
	oldPw := r.FormValue("OldPassword")
	newPw := r.FormValue("NewPassword")
	if oldPw == "" || newPw == "" {
		iamErrorXML(w, "ValidationError", "OldPassword and NewPassword are required", http.StatusBadRequest)
		return
	}
	caller := iamCallerUserName(r)
	if caller == "" {
		iamErrorXML(w, "NoSuchEntity", "The calling user does not have a login profile.", http.StatusNotFound)
		return
	}
	lp, ok := iamLoginProfiles.Get(caller)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Login Profile for user %s cannot be found.", caller), http.StatusNotFound)
		return
	}
	if lp.Password != oldPw {
		iamErrorXML(w, "InvalidUserType", "The OldPassword is incorrect.", http.StatusBadRequest)
		return
	}
	if msg := iamValidatePasswordAgainstPolicy(newPw); msg != "" {
		iamErrorXML(w, "PasswordPolicyViolation", msg, http.StatusBadRequest)
		return
	}
	lp.Password = newPw
	lp.PasswordResetRequired = false
	iamLoginProfiles.Put(caller, lp)
	iamEmptyResultXML(w, "ChangePassword")
}

// iamCallerUserName resolves the IAM user that owns the access key the request
// was signed with, by parsing the SigV4 Credential from the Authorization
// header and looking it up in the access-key store.
func iamCallerUserName(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const cred = "Credential="
	i := strings.Index(auth, cred)
	if i < 0 {
		return ""
	}
	rest := auth[i+len(cred):]
	akid := rest
	if j := strings.Index(akid, "/"); j >= 0 {
		akid = akid[:j]
	}
	if k, ok := iamAccessKeys.Get(akid); ok {
		return k.UserName
	}
	return ""
}

// iamValidatePasswordAgainstPolicy returns an empty string when the password
// satisfies the account password policy (or no policy is set), otherwise a
// human-readable reason.
func iamValidatePasswordAgainstPolicy(pw string) string {
	pol, ok := iamPasswordPolicies.Get(awsAccountID())
	if !ok {
		return ""
	}
	if pol.MinimumPasswordLength > 0 && len(pw) < pol.MinimumPasswordLength {
		return fmt.Sprintf("Password must be at least %d characters long.", pol.MinimumPasswordLength)
	}
	if pol.RequireNumbers && !strings.ContainsAny(pw, "0123456789") {
		return "Password must contain at least one numeric character."
	}
	if pol.RequireUppercaseCharacters && !strings.ContainsAny(pw, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return "Password must contain at least one uppercase character."
	}
	if pol.RequireLowercaseCharacters && !strings.ContainsAny(pw, "abcdefghijklmnopqrstuvwxyz") {
		return "Password must contain at least one lowercase character."
	}
	if pol.RequireSymbols && !strings.ContainsAny(pw, "!@#$%^&*()_+-=[]{}|'") {
		return "Password must contain at least one symbol character."
	}
	return ""
}

func handleIAMUpdateAccessKey(w http.ResponseWriter, r *http.Request) {
	akid := r.FormValue("AccessKeyId")
	key, ok := iamAccessKeys.Get(akid)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Access Key with id %s cannot be found.", akid), http.StatusNotFound)
		return
	}
	status := r.FormValue("Status")
	if status != "Active" && status != "Inactive" {
		iamErrorXML(w, "ValidationError", "Status must be Active or Inactive", http.StatusBadRequest)
		return
	}
	key.Status = status
	iamAccessKeys.Put(akid, key)
	iamEmptyResultXML(w, "UpdateAccessKey")
}

func handleIAMGetAccessKeyLastUsed(w http.ResponseWriter, r *http.Request) {
	akid := r.FormValue("AccessKeyId")
	key, ok := iamAccessKeys.Get(akid)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Access Key with id %s cannot be found.", akid), http.StatusNotFound)
		return
	}
	// The sim does not record per-key usage, so the key has never been used:
	// real IAM omits LastUsedDate and returns the "N/A" sentinel for ServiceName
	// and Region in that case.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAccessKeyLastUsedResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetAccessKeyLastUsedResult><UserName>%s</UserName><AccessKeyLastUsed><ServiceName>N/A</ServiceName><Region>N/A</Region></AccessKeyLastUsed></GetAccessKeyLastUsedResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetAccessKeyLastUsedResponse>`,
		xmlEscape(key.UserName), generateUUID())
}

func handleIAMTagUser(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	user, ok := iamUsers.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	for _, t := range iamParseTags(r) {
		replaced := false
		for i := range user.Tags {
			if user.Tags[i].Key == t.Key {
				user.Tags[i].Value = t.Value
				replaced = true
				break
			}
		}
		if !replaced {
			user.Tags = append(user.Tags, t)
		}
	}
	iamUsers.Put(name, user)
	iamEmptyResultXML(w, "TagUser")
}

func handleIAMUntagUser(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	user, ok := iamUsers.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
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
	kept := user.Tags[:0]
	for _, t := range user.Tags {
		if !remove[t.Key] {
			kept = append(kept, t)
		}
	}
	user.Tags = kept
	iamUsers.Put(name, user)
	iamEmptyResultXML(w, "UntagUser")
}

func handleIAMListUserTags(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("UserName")
	user, ok := iamUsers.Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListUserTagsResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListUserTagsResult>%s<IsTruncated>false</IsTruncated></ListUserTagsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListUserTagsResponse>`,
		iamTagsXML(user.Tags), generateUUID())
}

func iamPasswordPolicyXML(p IAMPasswordPolicy) string {
	return fmt.Sprintf("<PasswordPolicy><MinimumPasswordLength>%d</MinimumPasswordLength><RequireSymbols>%t</RequireSymbols><RequireNumbers>%t</RequireNumbers><RequireUppercaseCharacters>%t</RequireUppercaseCharacters><RequireLowercaseCharacters>%t</RequireLowercaseCharacters><AllowUsersToChangePassword>%t</AllowUsersToChangePassword><ExpirePasswords>%t</ExpirePasswords><MaxPasswordAge>%d</MaxPasswordAge><PasswordReusePrevention>%d</PasswordReusePrevention><HardExpiry>%t</HardExpiry></PasswordPolicy>",
		p.MinimumPasswordLength, p.RequireSymbols, p.RequireNumbers, p.RequireUppercaseCharacters,
		p.RequireLowercaseCharacters, p.AllowUsersToChangePassword, p.ExpirePasswords,
		p.MaxPasswordAge, p.PasswordReusePrevention, p.HardExpiry)
}

func handleIAMGetAccountPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := iamPasswordPolicies.Get(awsAccountID())
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "The Password Policy with domain name "+awsAccountID()+" cannot be found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAccountPasswordPolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetAccountPasswordPolicyResult>%s</GetAccountPasswordPolicyResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetAccountPasswordPolicyResponse>`,
		iamPasswordPolicyXML(p), generateUUID())
}

func handleIAMUpdateAccountPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	// AWS applies documented defaults for unspecified parameters.
	p := IAMPasswordPolicy{MinimumPasswordLength: 6}
	if v := r.FormValue("MinimumPasswordLength"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.MinimumPasswordLength = n
		}
	}
	p.RequireSymbols = r.FormValue("RequireSymbols") == "true"
	p.RequireNumbers = r.FormValue("RequireNumbers") == "true"
	p.RequireUppercaseCharacters = r.FormValue("RequireUppercaseCharacters") == "true"
	p.RequireLowercaseCharacters = r.FormValue("RequireLowercaseCharacters") == "true"
	p.AllowUsersToChangePassword = r.FormValue("AllowUsersToChangePassword") == "true"
	p.HardExpiry = r.FormValue("HardExpiry") == "true"
	if v := r.FormValue("MaxPasswordAge"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.MaxPasswordAge = n
		}
	}
	if v := r.FormValue("PasswordReusePrevention"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.PasswordReusePrevention = n
		}
	}
	// ExpirePasswords is derived: true iff MaxPasswordAge > 0.
	p.ExpirePasswords = p.MaxPasswordAge > 0
	iamPasswordPolicies.Put(awsAccountID(), p)
	iamEmptyResultXML(w, "UpdateAccountPasswordPolicy")
}

func handleIAMDeleteAccountPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := iamPasswordPolicies.Get(awsAccountID()); !ok {
		iamErrorXML(w, "NoSuchEntity", "The Password Policy cannot be found.", http.StatusNotFound)
		return
	}
	iamPasswordPolicies.Delete(awsAccountID())
	iamEmptyResultXML(w, "DeleteAccountPasswordPolicy")
}

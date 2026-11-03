package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// IAMLoginProfile is a user's console-password profile. The password itself is
// stored so ChangePassword can validate the caller's current password; IAM
// never returns it.
type IAMLoginProfile struct {
	UserName              string
	Password              string
	CreateDate            string
	PasswordResetRequired bool
}

// AWS stores at most one per account; keyed by account id.
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
	acct := iamRequestAccount(r)
	name := r.FormValue("UserName")
	user, ok := iamUsersIn(acct).Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	newName := r.FormValue("NewUserName")
	newPath := r.FormValue("NewPath")
	if newName != "" && newName != name {
		if _, exists := iamUsersIn(acct).Get(newName); exists {
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
	user.Arn = iamUserArn(acct, user.UserName, user.Path)
	if newName != "" && newName != name {
		iamUsersIn(acct).Delete(name)
		iamRekeyUser(acct, name, newName)
	}
	iamUsersIn(acct).Put(user.UserName, user)
	iamEmptyResultXML(w, "UpdateUser")
}

// iamRekeyUser moves a user's login profile, access keys, tags-bearing records,
// and group memberships from the old name to the new name on a rename.
func iamRekeyUser(acct string, oldName, newName string) {
	if lp, ok := iamLoginProfilesIn(acct).Get(oldName); ok {
		lp.UserName = newName
		iamLoginProfilesIn(acct).Delete(oldName)
		iamLoginProfilesIn(acct).Put(newName, lp)
	}
	old := iamCredentialOwner{Account: acct, UserName: oldName}
	for _, k := range iamAccessKeys.List() {
		if old.ownsKey(k) {
			k.UserName = newName
			iamAccessKeys.Put(k.AccessKeyId, k)
		}
	}
	for _, m := range iamGroupMembersIn(acct).List() {
		if m.UserName == oldName {
			iamGroupMembersIn(acct).Delete(m.GroupName + "/" + oldName)
			iamGroupMembersIn(acct).Put(m.GroupName+"/"+newName, IAMGroupMember{GroupName: m.GroupName, UserName: newName})
		}
	}
}

func handleIAMUpdateGroup(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	name := r.FormValue("GroupName")
	g, ok := iamGroupsIn(acct).Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Group %s not found", name), http.StatusNotFound)
		return
	}
	newName := r.FormValue("NewGroupName")
	newPath := r.FormValue("NewPath")
	if newName != "" && newName != name {
		if _, exists := iamGroupsIn(acct).Get(newName); exists {
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
	g.Arn = iamGroupArn(acct, g.GroupName, g.Path)
	if newName != "" && newName != name {
		iamGroupsIn(acct).Delete(name)
		for _, m := range iamGroupMembersIn(acct).List() {
			if m.GroupName == name {
				iamGroupMembersIn(acct).Delete(name + "/" + m.UserName)
				iamGroupMembersIn(acct).Put(newName+"/"+m.UserName, IAMGroupMember{GroupName: newName, UserName: m.UserName})
			}
		}
	}
	iamGroupsIn(acct).Put(g.GroupName, g)
	iamEmptyResultXML(w, "UpdateGroup")
}

func iamLoginProfileXML(owner iamCredentialOwner, lp IAMLoginProfile) string {
	return fmt.Sprintf("<LoginProfile>%s<CreateDate>%s</CreateDate><PasswordResetRequired>%t</PasswordResetRequired></LoginProfile>",
		owner.userNameXML(), lp.CreateDate, lp.PasswordResetRequired)
}

// iamLoginProfileOwner resolves whose password a request manages. A root
// user's is managed only from an AssumeRoot session, as the IAM API
// Reference states for the login-profile operations.
func iamLoginProfileOwner(w http.ResponseWriter, r *http.Request) (iamCredentialOwner, bool) {
	owner, ok := iamRequestCredentialOwner(w, r)
	if !ok {
		return owner, false
	}
	if owner.root() && !stsCallerIsRootSession(r) {
		iamErrorXML(w, "ValidationError", "Must specify userName when calling with non-User credentials", http.StatusBadRequest)
		return owner, false
	}
	return owner, true
}

func iamNoLoginProfile(w http.ResponseWriter, owner iamCredentialOwner) {
	name := owner.UserName
	if owner.root() {
		name = owner.Account
	}
	iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Login Profile for user %s cannot be found.", name), http.StatusNotFound)
}

func handleIAMCreateLoginProfile(w http.ResponseWriter, r *http.Request) {
	owner, ok := iamLoginProfileOwner(w, r)
	if !ok {
		return
	}
	profiles := iamLoginProfilesIn(owner.Account)
	if _, ok := profiles.Get(owner.key()); ok {
		name := owner.UserName
		if owner.root() {
			name = owner.Account
		}
		iamErrorXML(w, "EntityAlreadyExists", fmt.Sprintf("Login Profile for user %s already exists.", name), http.StatusConflict)
		return
	}
	lp := IAMLoginProfile{
		UserName:              owner.UserName,
		Password:              r.FormValue("Password"),
		CreateDate:            time.Now().UTC().Format(time.RFC3339),
		PasswordResetRequired: r.FormValue("PasswordResetRequired") == "true",
	}
	profiles.Put(owner.key(), lp)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateLoginProfileResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><CreateLoginProfileResult>%s</CreateLoginProfileResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></CreateLoginProfileResponse>`,
		iamLoginProfileXML(owner, lp), sim.NewUUID())
}

func handleIAMGetLoginProfile(w http.ResponseWriter, r *http.Request) {
	owner, ok := iamLoginProfileOwner(w, r)
	if !ok {
		return
	}
	lp, ok := iamLoginProfilesIn(owner.Account).Get(owner.key())
	if !ok {
		iamNoLoginProfile(w, owner)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetLoginProfileResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetLoginProfileResult>%s</GetLoginProfileResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetLoginProfileResponse>`,
		iamLoginProfileXML(owner, lp), sim.NewUUID())
}

func handleIAMUpdateLoginProfile(w http.ResponseWriter, r *http.Request) {
	owner, ok := iamRequestCredentialOwner(w, r)
	if !ok {
		return
	}
	profiles := iamLoginProfilesIn(owner.Account)
	lp, ok := profiles.Get(owner.key())
	if !ok {
		iamNoLoginProfile(w, owner)
		return
	}
	if pw := r.FormValue("Password"); pw != "" {
		lp.Password = pw
	}
	if v := r.FormValue("PasswordResetRequired"); v != "" {
		lp.PasswordResetRequired = v == "true"
	}
	profiles.Put(owner.key(), lp)
	iamEmptyResultXML(w, "UpdateLoginProfile")
}

func handleIAMDeleteLoginProfile(w http.ResponseWriter, r *http.Request) {
	owner, ok := iamLoginProfileOwner(w, r)
	if !ok {
		return
	}
	if !iamLoginProfilesIn(owner.Account).Delete(owner.key()) {
		iamNoLoginProfile(w, owner)
		return
	}
	iamEmptyResultXML(w, "DeleteLoginProfile")
}

// handleIAMChangePassword changes the caller's console password. The sim
// resolves the caller from the SigV4 access-key id and validates the new
// password against the account password policy.
func handleIAMChangePassword(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
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
	lp, ok := iamLoginProfilesIn(acct).Get(caller)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("Login Profile for user %s cannot be found.", caller), http.StatusNotFound)
		return
	}
	if lp.Password != oldPw {
		iamErrorXML(w, "InvalidUserType", "The OldPassword is incorrect.", http.StatusBadRequest)
		return
	}
	if msg := iamValidatePasswordAgainstPolicy(acct, newPw); msg != "" {
		iamErrorXML(w, "PasswordPolicyViolation", msg, http.StatusBadRequest)
		return
	}
	lp.Password = newPw
	lp.PasswordResetRequired = false
	iamLoginProfilesIn(acct).Put(caller, lp)
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
func iamValidatePasswordAgainstPolicy(acct string, pw string) string {
	pol, ok := iamPasswordPoliciesIn(acct).Get(acct)
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
	key, _, ok := iamOwnedAccessKey(w, r)
	if !ok {
		return
	}
	status := r.FormValue("Status")
	if status != "Active" && status != "Inactive" {
		iamErrorXML(w, "ValidationError", "Status must be Active or Inactive", http.StatusBadRequest)
		return
	}
	key.Status = status
	iamAccessKeys.Put(key.AccessKeyId, key)
	iamEmptyResultXML(w, "UpdateAccessKey")
}

func handleIAMGetAccessKeyLastUsed(w http.ResponseWriter, r *http.Request) {
	akid := r.FormValue("AccessKeyId")
	key, ok := iamAccessKeys.Get(akid)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The Access Key with id %s cannot be found.", akid), http.StatusNotFound)
		return
	}
	// The simulator records no per-key usage, so the key reads as never used: IAM
	// then omits LastUsedDate and reports ServiceName and Region as "N/A". A
	// root user's key names no user.
	owner := iamCredentialOwner{Account: key.account(), UserName: key.UserName}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAccessKeyLastUsedResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetAccessKeyLastUsedResult>%s<AccessKeyLastUsed><ServiceName>N/A</ServiceName><Region>N/A</Region></AccessKeyLastUsed></GetAccessKeyLastUsedResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetAccessKeyLastUsedResponse>`,
		owner.userNameXML(), sim.NewUUID())
}

func handleIAMTagUser(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	name := r.FormValue("UserName")
	user, ok := iamUsersIn(acct).Get(name)
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
	iamUsersIn(acct).Put(name, user)
	iamEmptyResultXML(w, "TagUser")
}

func handleIAMUntagUser(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	name := r.FormValue("UserName")
	user, ok := iamUsersIn(acct).Get(name)
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
	iamUsersIn(acct).Put(name, user)
	iamEmptyResultXML(w, "UntagUser")
}

func handleIAMListUserTags(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	name := r.FormValue("UserName")
	user, ok := iamUsersIn(acct).Get(name)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListUserTagsResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListUserTagsResult>%s<IsTruncated>false</IsTruncated></ListUserTagsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListUserTagsResponse>`,
		iamTagsXML(user.Tags), sim.NewUUID())
}

func iamPasswordPolicyXML(p IAMPasswordPolicy) string {
	return fmt.Sprintf("<PasswordPolicy><MinimumPasswordLength>%d</MinimumPasswordLength><RequireSymbols>%t</RequireSymbols><RequireNumbers>%t</RequireNumbers><RequireUppercaseCharacters>%t</RequireUppercaseCharacters><RequireLowercaseCharacters>%t</RequireLowercaseCharacters><AllowUsersToChangePassword>%t</AllowUsersToChangePassword><ExpirePasswords>%t</ExpirePasswords><MaxPasswordAge>%d</MaxPasswordAge><PasswordReusePrevention>%d</PasswordReusePrevention><HardExpiry>%t</HardExpiry></PasswordPolicy>",
		p.MinimumPasswordLength, p.RequireSymbols, p.RequireNumbers, p.RequireUppercaseCharacters,
		p.RequireLowercaseCharacters, p.AllowUsersToChangePassword, p.ExpirePasswords,
		p.MaxPasswordAge, p.PasswordReusePrevention, p.HardExpiry)
}

func handleIAMGetAccountPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	p, ok := iamPasswordPoliciesIn(acct).Get(acct)
	if !ok {
		iamErrorXML(w, "NoSuchEntity", "The Password Policy with domain name "+acct+" cannot be found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAccountPasswordPolicyResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetAccountPasswordPolicyResult>%s</GetAccountPasswordPolicyResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetAccountPasswordPolicyResponse>`,
		iamPasswordPolicyXML(p), sim.NewUUID())
}

func handleIAMUpdateAccountPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
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
	iamPasswordPoliciesIn(acct).Put(acct, p)
	iamEmptyResultXML(w, "UpdateAccountPasswordPolicy")
}

func handleIAMDeleteAccountPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	acct := iamRequestAccount(r)
	if _, ok := iamPasswordPoliciesIn(acct).Get(acct); !ok {
		iamErrorXML(w, "NoSuchEntity", "The Password Policy cannot be found.", http.StatusNotFound)
		return
	}
	iamPasswordPoliciesIn(acct).Delete(acct)
	iamEmptyResultXML(w, "DeleteAccountPasswordPolicy")
}

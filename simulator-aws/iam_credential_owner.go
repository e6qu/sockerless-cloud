package main

import (
	"fmt"
	"net/http"
	"time"
)

// The IAM operations that manage credentials act on the user the request
// names or, with no UserName, on the principal making the request: an IAM
// user, or the account's root user. A root user's access keys, signing
// certificates, password and MFA devices are the account's own.

// iamCredentialOwner is whose credentials a request manages. An empty
// UserName is the account's root user.
type iamCredentialOwner struct {
	Account  string
	UserName string
}

func (o iamCredentialOwner) root() bool { return o.UserName == "" }

// iamImplicitOwnerOperations are the IAM operations whose UserName defaults
// to the caller.
var iamImplicitOwnerOperations = map[string]bool{
	"CreateAccessKey": true, "DeleteAccessKey": true, "UpdateAccessKey": true, "ListAccessKeys": true,
	"GetUser":            true,
	"CreateLoginProfile": true, "GetLoginProfile": true, "UpdateLoginProfile": true, "DeleteLoginProfile": true,
	"ListMFADevices": true, "DeactivateMFADevice": true,
	"UploadSigningCertificate": true, "ListSigningCertificates": true, "UpdateSigningCertificate": true, "DeleteSigningCertificate": true,
}

// iamRootEntityKey is the key the root user's login profile is stored under.
// No IAM user name contains angle brackets.
const iamRootEntityKey = "<root>"

func (o iamCredentialOwner) key() string {
	if o.root() {
		return iamRootEntityKey
	}
	return o.UserName
}

// arn is the ARN IAM authorizes a call on the owner against.
func (o iamCredentialOwner) arn() string {
	if o.root() {
		return iamRootArn(o.Account)
	}
	if u, ok := iamUsersIn(o.Account).Get(o.UserName); ok {
		return u.Arn
	}
	return iamUserArn(o.Account, o.UserName, "/")
}

// userNameXML is the UserName element of a response about the owner's
// credentials; a root user's carry none.
func (o iamCredentialOwner) userNameXML() string {
	if o.root() {
		return ""
	}
	return "<UserName>" + xmlEscape(o.UserName) + "</UserName>"
}

func (o iamCredentialOwner) ownsKey(k IAMAccessKey) bool {
	return k.account() == o.Account && k.UserName == o.UserName
}

// iamRequestCredentialOwner resolves the owner a request names, writing
// IAM's error when the named user does not exist or the caller is neither an
// IAM user nor a root user and names nobody.
func iamRequestCredentialOwner(w http.ResponseWriter, r *http.Request) (iamCredentialOwner, bool) {
	acct := iamRequestAccount(r)
	if name := r.FormValue("UserName"); name != "" {
		if _, ok := iamUsersIn(acct).Get(name); !ok {
			iamErrorXML(w, "NoSuchEntity", fmt.Sprintf("The user with name %s cannot be found.", name), http.StatusNotFound)
			return iamCredentialOwner{}, false
		}
		return iamCredentialOwner{Account: acct, UserName: name}, true
	}
	if owner, ok := iamCallerCredentialOwner(r); ok {
		return owner, true
	}
	iamErrorXML(w, "ValidationError", "Must specify userName when calling with non-User credentials", http.StatusBadRequest)
	return iamCredentialOwner{}, false
}

// iamCallerCredentialOwner is the caller as a credential owner: an IAM user
// or a root user, and nothing for a role or federated user session.
func iamCallerCredentialOwner(r *http.Request) (iamCredentialOwner, bool) {
	caller, ok := iamCallerForAccessKey(iamAccessKeyIDFromRequest(r))
	switch {
	case !ok:
		return iamCredentialOwner{}, false
	case caller.Root:
		return iamCredentialOwner{Account: caller.Account}, true
	case caller.UserName != "":
		return iamCredentialOwner{Account: caller.Account, UserName: caller.UserName}, true
	}
	return iamCredentialOwner{}, false
}

// iamRootUserXML is GetUser's answer for a root user, whose unique ID is the
// account ID and which has no user name or path.
func iamRootUserXML(account string) string {
	return fmt.Sprintf("<User><UserId>%s</UserId><Arn>%s</Arn><CreateDate>%s</CreateDate></User>",
		account, iamRootArn(account), iamAccountCreateDate(account))
}

// iamAccountCreateDate is when account was created: a member account's
// joining date, and for the management account the date its first identity,
// the seeded administrator, was created.
func iamAccountCreateDate(account string) string {
	if a, ok := iamHostedAccountRecord(account); ok && a.JoinedTimestamp > 0 {
		return time.Unix(a.JoinedTimestamp, 0).UTC().Format(time.RFC3339)
	}
	if u, ok := iamUsersIn(awsAccountID()).Get(seedAdminUserName); ok {
		return u.CreateDate
	}
	return time.Unix(0, 0).UTC().Format(time.RFC3339)
}

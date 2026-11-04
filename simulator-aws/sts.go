package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// AWS STS mints temporary credentials (ASIA access keys) and records in
// iamTempCreds the principal each stands in for, so the call-time IAM
// enforcement (iam_enforcement.go) evaluates the policies of that role or user.

// IAMTempCred binds a temporary access key to the principal it stands in for:
// an assumed role (RoleName set) or a user session (UserName set).
type IAMTempCred struct {
	AccessKeyID string
	// The SigV4 check verifies a request signed with the temporary credential and
	// its X-Amz-Security-Token against these.
	SecretAccessKey string
	SessionToken    string
	UserName        string // set for GetSessionToken (the caller's user)
	RoleName        string // set for AssumeRole / AssumeRoleWithWebIdentity
	PrincipalArn    string // the assumed-role/… or user/… ARN the caller sees
	Expiration      string
	MFA             bool   // the session was authenticated with MFA
	CreatedAt       string // RFC3339, for aws:MultiFactorAuthAge
	// FederatedClaims are the identity provider's claims an AssumeRole made
	// with this session carries; see stsChainedFederationKeys.
	FederatedClaims map[string][]string `json:",omitempty"`
	// SessionTags are the session's tags, reported as its principal tags, and
	// TransitiveTagKeys the ones a role it assumes keeps.
	SessionTags       []IAMTag `json:",omitempty"`
	TransitiveTagKeys []string `json:",omitempty"`
	// SessionPolicy and SessionPolicyArns are the session policies passed for
	// the session. SessionPolicyCaps is set when they cap the session even if
	// none was passed, as for a federated user session.
	SessionPolicy     string   `json:",omitempty"`
	SessionPolicyArns []string `json:",omitempty"`
	SessionPolicyCaps bool     `json:",omitempty"`
	// FederatingUser is the IAM user whose GetFederationToken call opened a
	// federated user session; its policies are the session's identity policies.
	FederatingUser string `json:",omitempty"`
	// UnrestrictedIdentity marks a session whose identity allows every action:
	// a root user session, or a federated user session opened with a
	// credential that resolves to no principal, which the gate allows
	// everything.
	UnrestrictedIdentity bool `json:",omitempty"`
	// TaskPolicyArn is the root-task policy that scopes an AssumeRoot session.
	TaskPolicyArn string `json:",omitempty"`
	// FromGetSessionToken marks a GetSessionToken session, which calls IAM
	// only with MFA and AWS STS only for AssumeRole and GetCallerIdentity.
	FromGetSessionToken bool `json:",omitempty"`
	// SourceIdentity is the identity the session's first assumption set,
	// which every session chained from it keeps.
	SourceIdentity string `json:",omitempty"`
}

var (
	iamTempCreds sim.Store[IAMTempCred]
	// stsTokenKeys holds the key session tokens are sealed with, under
	// stsTokenKeyID, so tokens minted before a restart still open after it.
	stsTokenKeys sim.Store[string]
)

const stsTokenKeyID = "session-token-mac"

func registerSTS(r *AWSQueryRouter, srv *sim.Server) {
	iamTempCreds = sim.MakeStore[IAMTempCred](srv.DB(), "iam_temp_creds")
	stsTokenKeys = sim.MakeStore[string](srv.DB(), "sts_token_keys")
	registerSTSOutboundFederationState(srv)
	startTempCredSweeper(srv)
	r.Register("GetCallerIdentity", handleGetCallerIdentity)
	r.Register("AssumeRole", handleSTSAssumeRole)
	r.Register("AssumeRoleWithWebIdentity", handleSTSAssumeRoleWithWebIdentity)
	r.Register("GetSessionToken", handleSTSGetSessionToken)
	r.Register("GetFederationToken", handleSTSGetFederationToken)
	r.Register("AssumeRoleWithSAML", handleSTSAssumeRoleWithSAML)
	r.Register("GetWebIdentityToken", handleSTSGetWebIdentityToken)
	r.Register("GetDelegatedAccessToken", handleSTSGetDelegatedAccessToken)
	r.Register("AssumeRoot", handleSTSAssumeRoot)
	r.Register("DecodeAuthorizationMessage", handleSTSDecodeAuthorizationMessage)
	r.Register("GetAccessKeyInfo", handleSTSGetAccessKeyInfo)
}

// iamPrincipalForAccessKey resolves an access key ID to the caller's ARN and
// identity-based policy documents: an IAM user's key (AKIA) or a temporary
// credential's role or user (ASIA). ok is false for a key no principal holds.
func iamPrincipalForAccessKey(akid string) (arn string, docs []iamPolicyDoc, userName string, ok bool) {
	caller, ok := iamCallerForAccessKey(akid)
	return caller.Arn, caller.Identity, caller.UserName, ok
}

// iamCaller is every policy that governs a request signed with one access
// key.
type iamCaller struct {
	Arn string
	// UserName is the IAM user the request acts as, for aws:username.
	UserName string
	Identity []iamPolicyDoc
	// Unrestricted marks an identity that allows every action, which only an
	// explicit deny limits.
	Unrestricted bool
	Boundary     []iamPolicyDoc
	// SessionCaps reports that Session caps the identity: an action no session
	// policy allows is implicitly denied.
	SessionCaps bool
	Session     []iamPolicyDoc
	// Federated marks a GetFederationToken session, which calls no IAM or AWS
	// STS operation other than GetCallerIdentity.
	Federated bool
	// EntityArn is the IAM user whose GetFederationToken call opened the
	// session: a resource-based policy naming it grants the session within
	// its boundary and session policies.
	EntityArn string
	// SessionToken marks a GetSessionToken session, and MFA one that MFA
	// authenticated.
	SessionToken bool
	MFA          bool
	// Account is the account the caller acts in, and Root marks the
	// account's root user, whether signing with its own access key or in an
	// AssumeRoot session.
	Account string
	Root    bool
	// ServiceLinked marks a session of a service-linked role, which no
	// service control policy restricts.
	ServiceLinked bool
	// SourceIdentity is the source identity the session carries.
	SourceIdentity string
}

// refuses reports whether the kind of session the caller holds may not call
// action at all, whatever its policies allow.
func (c iamCaller) refuses(action string) bool {
	switch {
	case c.Federated:
		return stsFederatedSessionRefuses(action)
	case c.SessionToken:
		return stsSessionTokenRefuses(action, c.MFA)
	}
	return false
}

// iamCallerForAccessKey resolves an access key ID to the policies that govern
// it. ok is false for a key no principal holds.
func iamCallerForAccessKey(akid string) (iamCaller, bool) {
	acct := iamAccessKeyAccount(akid)
	if akid == "" {
		return iamCaller{}, false
	}
	if tc, found := iamTempCreds.Get(akid); found {
		caller := iamCaller{
			Arn:            tc.PrincipalArn,
			Unrestricted:   tc.UnrestrictedIdentity,
			SessionCaps:    tc.SessionPolicyCaps || tc.SessionPolicy != "" || len(tc.SessionPolicyArns) > 0,
			Federated:      strings.Contains(tc.PrincipalArn, ":federated-user/"),
			SessionToken:   tc.FromGetSessionToken,
			MFA:            tc.MFA,
			Account:        acct,
			Root:           iamIsRootArn(tc.PrincipalArn),
			SourceIdentity: tc.SourceIdentity,
		}
		if caller.SessionCaps {
			caller.Session = stsSessionPolicyDocs(tc)
		}
		switch {
		case tc.RoleName != "":
			caller.Identity = iamPolicyDocsForRole(acct, tc.RoleName)
			caller.Boundary = iamRolePermissionBoundaryDocs(acct, tc.RoleName)
			if role, ok := iamRolesIn(acct).Get(tc.RoleName); ok {
				caller.ServiceLinked = iamIsServiceLinkedRole(role)
			}
		case tc.UserName != "":
			if u, uok := iamUsersIn(acct).Get(tc.UserName); uok {
				caller.UserName = u.UserName
				caller.Identity = iamEffectivePolicyDocsForUser(acct, u.UserName)
				caller.Boundary = iamPermissionBoundaryDocs(acct, u.UserName)
			}
		case tc.FederatingUser != "":
			if u, uok := iamUsersIn(acct).Get(tc.FederatingUser); uok {
				caller.EntityArn = u.Arn
				caller.Identity = iamEffectivePolicyDocsForUser(acct, tc.FederatingUser)
				caller.Boundary = iamPermissionBoundaryDocs(acct, tc.FederatingUser)
			}
		}
		if tc.TaskPolicyArn != "" {
			if doc, ok := stsRootTaskPolicy(tc.TaskPolicyArn); ok {
				caller.Identity = append(caller.Identity, doc)
			}
		}
		return caller, true
	}
	if key, found := iamAccessKeys.Get(akid); found {
		if key.UserName == "" {
			return iamCaller{Arn: iamRootArn(acct), Unrestricted: true, Account: acct, Root: true}, true
		}
		if u, uok := iamUsersIn(acct).Get(key.UserName); uok {
			return iamCaller{
				Arn: u.Arn, UserName: u.UserName,
				Identity: iamEffectivePolicyDocsForUser(acct, u.UserName),
				Boundary: iamPermissionBoundaryDocs(acct, u.UserName),
				Account:  acct,
			}, true
		}
	}
	return iamCaller{}, false
}

// stsCallerIdentity is the ARN and unique ID of the identity that signed the
// request: a user's own ID, `<role ID>:<session name>` for an assumed role, and
// `<account>:<name>` for a federated user. ok is false for a credential that
// resolves to no identity.
func stsCallerIdentity(r *http.Request) (arn, userID string, ok bool) {
	acct := iamRequestAccount(r)
	akid := iamAccessKeyIDFromRequest(r)
	if tc, found := iamTempCreds.Get(akid); found {
		_, name, _ := strings.Cut(tc.PrincipalArn[strings.LastIndex(tc.PrincipalArn, ":")+1:], "/")
		switch {
		case tc.RoleName != "":
			role, rok := iamRolesIn(acct).Get(tc.RoleName)
			if !rok {
				return "", "", false
			}
			return tc.PrincipalArn, role.RoleId + ":" + name[strings.LastIndex(name, "/")+1:], true
		case tc.UserName != "":
			u, uok := iamUsersIn(acct).Get(tc.UserName)
			if !uok {
				return "", "", false
			}
			return tc.PrincipalArn, u.UserId, true
		case strings.Contains(tc.PrincipalArn, ":federated-user/"):
			return tc.PrincipalArn, acct + ":" + name, true
		case iamIsRootArn(tc.PrincipalArn):
			// A root user's unique ID is its account ID.
			return tc.PrincipalArn, acct, true
		}
		return "", "", false
	}
	if key, found := iamAccessKeys.Get(akid); found {
		if key.UserName == "" {
			return iamRootArn(acct), acct, true
		}
		if u, uok := iamUsersIn(acct).Get(key.UserName); uok {
			return u.Arn, u.UserId, true
		}
	}
	return "", "", false
}

func handleGetCallerIdentity(w http.ResponseWriter, r *http.Request) {
	arn, userID, ok := stsCallerIdentity(r)
	if !ok {
		stsErrorXML(w, "InvalidClientTokenId", sigMsgInvalidTok, http.StatusForbidden)
		return
	}
	acct := iamArnAccount(arn)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetCallerIdentityResult>
    <Arn>%s</Arn>
    <UserId>%s</UserId>
    <Account>%s</Account>
  </GetCallerIdentityResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetCallerIdentityResponse>`, xmlEscape(arn), userID, acct, sim.NewUUID())
}

func stsDurationSeconds(r *http.Request) int {
	d := atoiDefault(r.FormValue("DurationSeconds"), 3600)
	if d < 900 {
		d = 900
	}
	if d > 43200 {
		d = 43200
	}
	return d
}

// stsTrustAllows evaluates a role's trust policy for a caller. A statement that
// names the caller's account rather than the caller delegates to the account's
// own IAM, which the call-time enforcement has already applied.
func stsTrustAllows(role IAMRole, action, caller string, ctx map[string][]string) bool {
	trust, err := parseIAMPolicy(role.AssumeRolePolicyDocument)
	if err != nil {
		return false
	}
	decision, _ := iamEvalDecisionForPrincipal([]iamPolicyDoc{trust}, action, role.Arn, caller, ctx)
	if decision == "allowed" {
		return true
	}
	if decision == "explicitDeny" {
		return false
	}
	return stsTrustDelegates(trust, action, caller, ctx)
}

// stsTrustDelegates reports whether an Allow statement admits the caller by
// its account alone.
func stsTrustDelegates(trust iamPolicyDoc, action, caller string, ctx map[string][]string) bool {
	for _, stmt := range trust.Statement {
		if !strings.EqualFold(stmt.Effect, "allow") || iamPrincipalMatchKind(stmt, caller, "", ctx) != iamPrincipalDelegated {
			continue
		}
		if !iamActionMatches(stmt, action) {
			continue
		}
		if ok, _ := iamConditionMatches(stmt, ctx); ok {
			return true
		}
	}
	return false
}

// iamRoleTrustDocs returns the trust policy of the role roleArn names.
func iamRoleTrustDocs(roleArn string) []iamPolicyDoc {
	acct := iamArnAccount(roleArn)
	if !iamIsRoleARN(roleArn) {
		return nil
	}
	role, ok := iamRolesIn(acct).Get(iamRoleNameFromArn(roleArn))
	if !ok || role.Arn != roleArn {
		return nil
	}
	trust, err := parseIAMPolicy(role.AssumeRolePolicyDocument)
	if err != nil {
		return nil
	}
	return []iamPolicyDoc{trust}
}

// stsMintTempCred mints a temporary credential whose session token carries its
// own expiration, sealed with the simulator's key. AWS reads a presented
// token's expiration from the token itself, so a credential already pruned is
// still refused as expired rather than as unknown.
func stsMintTempCred(expiration time.Time) (akid, secret, token string) {
	akid = "ASIA" + strings.ToUpper(iamRandomB32(16))
	payload := make([]byte, stsTokenPayloadLen)
	binary.BigEndian.PutUint64(payload, uint64(expiration.Unix()))
	_, _ = rand.Read(payload[8:])
	sealed := append(payload, stsTokenMAC(akid, payload)...)
	return akid, iamRandomSecret(), stsTokenEncoding.EncodeToString(sealed)
}

const (
	stsTokenPayloadLen = 24
	stsTokenMACLen     = 16
)

var stsTokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func stsTokenMAC(akid string, payload []byte) []byte {
	mac := hmac.New(sha256.New, stsTokenKey())
	mac.Write([]byte(akid))
	mac.Write(payload)
	return mac.Sum(nil)[:stsTokenMACLen]
}

func stsTokenKey() []byte {
	encoded, ok := stsTokenKeys.Get(stsTokenKeyID)
	if !ok {
		stsTokenKeys.Upsert(stsTokenKeyID, func(key *string) {
			if *key == "" {
				fresh := make([]byte, sha256.Size)
				_, _ = rand.Read(fresh)
				*key = hex.EncodeToString(fresh)
			}
			encoded = *key
		})
	}
	key, err := hex.DecodeString(encoded)
	if err != nil {
		panic(fmt.Sprintf("stored session token key is not hex: %v", err))
	}
	return key
}

// stsTokenExpiration returns the expiration sealed into a session token
// stsMintTempCred minted for akid, and false for any other token.
func stsTokenExpiration(akid, token string) (time.Time, bool) {
	sealed, err := stsTokenEncoding.DecodeString(token)
	if err != nil || len(sealed) != stsTokenPayloadLen+stsTokenMACLen {
		return time.Time{}, false
	}
	payload := sealed[:stsTokenPayloadLen]
	if !hmac.Equal(sealed[stsTokenPayloadLen:], stsTokenMAC(akid, payload)) {
		return time.Time{}, false
	}
	return time.Unix(int64(binary.BigEndian.Uint64(payload)), 0).UTC(), true
}

// startTempCredSweeper deletes temporary credentials once they expire. An
// expired credential authorizes nothing, and a caller that presents one is
// still told so by stsTokenExpiration.
func startTempCredSweeper(srv *sim.Server) {
	kvstore.StartSweeper(srv, "AWS STS temporary credential expiry", storeSweepInterval, func(now time.Time) { stsSweepExpiredTempCreds(now) })
}

// stsSweepExpiredTempCreds deletes expired temporary credentials and the
// records that describe a credential no longer held.
func stsSweepExpiredTempCreds(now time.Time) int {
	swept := iamTempCreds.Prune(func(tc IAMTempCred) bool {
		exp, err := time.Parse(time.RFC3339, tc.Expiration)
		return err == nil && now.After(exp)
	})
	credentialGone := func(accessKeyID string) bool {
		_, ok := iamTempCreds.Get(accessKeyID)
		return !ok
	}
	s3ExpressSessions.Prune(func(session S3ExpressSession) bool {
		return credentialGone(session.AccessKeyID)
	})
	s3AccessGrantsCredentials.Prune(func(issued S3AccessGrantsCredential) bool {
		return credentialGone(issued.AccessKeyID)
	})
	return swept
}

func handleSTSAssumeRole(w http.ResponseWriter, r *http.Request) {
	roleArn := r.FormValue("RoleArn")
	sessionName := r.FormValue("RoleSessionName")
	if roleArn == "" || sessionName == "" {
		stsErrorXML(w, "ValidationError", "RoleArn and RoleSessionName are required", http.StatusBadRequest)
		return
	}
	policies, reqErr := stsRequestSessionPolicies(r)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	if _, reqErr := stsRequestDuration(r, stsRoleDurationMin, stsRoleDurationMax, stsRoleDurationDefault); reqErr != nil {
		reqErr.write(w)
		return
	}
	mfa, reqErr := stsRequestMFA(r)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	sourceIdentity, reqErr := stsSessionSourceIdentity(r, r.FormValue("SourceIdentity"))
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	role, ok := iamRoleByArn(roleArn)
	if !ok {
		stsErrorXML(w, "AccessDenied",
			fmt.Sprintf("User is not authorized to perform: sts:AssumeRole on resource: %s (role not found)", roleArn),
			http.StatusForbidden)
		return
	}
	requestedTags, transitive := stsRequestSessionTags(r)
	sessionTags, err := stsNewSessionTags(stsInheritedSessionTags(r), requestedTags, transitive)
	if err != nil {
		stsErrorXML(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return
	}
	// The trust policy must admit the caller. The call-time check evaluates no
	// policy for a key no principal holds, and neither does this one.
	callerKey := iamAccessKeyIDFromRequest(r)
	if callerArn, _, userName, registered := iamPrincipalForAccessKey(callerKey); registered {
		ctx := iamRequestConditionContext(r, callerKey, callerArn, userName, "sts:AssumeRole")
		ctx["sts:RoleSessionName"] = []string{sessionName}
		for key, param := range map[string]string{"sts:ExternalId": "ExternalId", "sts:SourceIdentity": "SourceIdentity"} {
			if value := r.FormValue(param); value != "" {
				ctx[key] = []string{value}
			}
		}
		if !stsTrustAllows(role, "sts:AssumeRole", callerArn, ctx) {
			stsErrorXML(w, "AccessDenied",
				fmt.Sprintf("User: %s is not authorized to perform: sts:AssumeRole on resource: %s", callerArn, roleArn),
				http.StatusForbidden)
			return
		}
		// Passed session tags also need the trust policy to allow sts:TagSession;
		// the transitive tags a chained session keeps were authorized when first
		// passed.
		if len(requestedTags) > 0 && !stsTrustAllows(role, "sts:TagSession", callerArn, sessionTags.conditionContext(ctx)) {
			stsErrorXML(w, "AccessDenied",
				fmt.Sprintf("User: %s is not authorized to perform: sts:TagSession on resource: %s", callerArn, roleArn),
				http.StatusForbidden)
			return
		}
		if sourceIdentity != "" && !stsTrustAllows(role, "sts:SetSourceIdentity", callerArn, stsWithSourceIdentity(ctx, sourceIdentity)) {
			stsErrorXML(w, "AccessDenied",
				fmt.Sprintf("User: %s is not authorized to perform: sts:SetSourceIdentity on resource: %s", callerArn, roleArn),
				http.StatusForbidden)
			return
		}
	}
	duration, reqErr := stsRoleSessionDuration(r, role, stsCallerIsRoleSession(r))
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	packed, reqErr := stsCheckPackedPolicySize(policies, requestedTags)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	if reqErr := stsSpendRequestMFA(r, mfa); reqErr != nil {
		reqErr.write(w)
		return
	}
	exp := time.Now().UTC().Add(duration)
	akid, secret, token := stsMintTempCred(exp)
	assumedArn := stsAssumedRoleArn(role, sessionName)
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
		RoleName: role.RoleName, PrincipalArn: assumedArn,
		Expiration: exp.Format(time.RFC3339),
		MFA:        mfa, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		FederatedClaims: stsCallerFederatedClaims(r),
		SessionTags:     sessionTags.Tags, TransitiveTagKeys: sessionTags.Transitive,
		SessionPolicy: policies.Inline, SessionPolicyArns: policies.Arns,
		SourceIdentity: sourceIdentity,
	})
	assumedRoleID := role.RoleId + ":" + sessionName
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleResult>
    <Credentials>
      <AccessKeyId>%s</AccessKeyId>
      <SecretAccessKey>%s</SecretAccessKey>
      <SessionToken>%s</SessionToken>
      <Expiration>%s</Expiration>
    </Credentials>
    <AssumedRoleUser>
      <Arn>%s</Arn>
      <AssumedRoleId>%s</AssumedRoleId>
    </AssumedRoleUser>%s%s
  </AssumeRoleResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AssumeRoleResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(assumedArn), assumedRoleID, stsPackedPolicySizeXML(packed), stsSourceIdentityXML(sourceIdentity), sim.NewUUID())
}

// stsAssumedRoleArn is the ARN of a session of role, in the role's account.
func stsAssumedRoleArn(role IAMRole, sessionName string) string {
	return fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/%s", iamArnAccount(role.Arn), role.RoleName, sessionName)
}

func handleSTSAssumeRoleWithWebIdentity(w http.ResponseWriter, r *http.Request) {
	roleArn := r.FormValue("RoleArn")
	sessionName := r.FormValue("RoleSessionName")
	if roleArn == "" || sessionName == "" || r.FormValue("WebIdentityToken") == "" {
		stsErrorXML(w, "ValidationError", "RoleArn, RoleSessionName and WebIdentityToken are required", http.StatusBadRequest)
		return
	}
	policies, reqErr := stsRequestSessionPolicies(r)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	role, ok := iamRoleByArn(roleArn)
	if !ok {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:AssumeRoleWithWebIdentity on %s", roleArn), http.StatusForbidden)
		return
	}
	// Verify the web identity token against the registered OpenID Connect provider, as AWS STS does.
	identity, err := verifyWebIdentityToken(r.Context(), iamArnAccount(role.Arn), r.FormValue("WebIdentityToken"))
	if err != nil {
		stsErrorXML(w, "InvalidIdentityToken", err.Error(), http.StatusBadRequest)
		return
	}
	tokenSubject := identity.Subject
	federation := identity.conditionContext(sessionName)
	if !stsTrustAllows(role, "sts:AssumeRoleWithWebIdentity", "federated:"+identity.Provider.Arn, federation) {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:AssumeRoleWithWebIdentity on %s", roleArn), http.StatusForbidden)
		return
	}
	tokenTags, tokenTransitive, err := identity.sessionTags()
	if err != nil {
		stsErrorXML(w, "InvalidIdentityToken", err.Error(), http.StatusBadRequest)
		return
	}
	sessionTags, err := stsNewSessionTags(stsSessionTags{}, tokenTags, tokenTransitive)
	if err != nil {
		stsErrorXML(w, "InvalidIdentityToken", err.Error(), http.StatusBadRequest)
		return
	}
	if sessionTags.passed() && !stsTrustAllows(role, "sts:TagSession", "federated:"+identity.Provider.Arn, sessionTags.conditionContext(federation)) {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:TagSession on %s", roleArn), http.StatusForbidden)
		return
	}
	sourceIdentity, err := stsFederatedSourceIdentity(identity.claimStrings(stsSourceIdentityTokenClaim))
	if err != nil {
		stsErrorXML(w, "InvalidIdentityToken", err.Error(), http.StatusBadRequest)
		return
	}
	if sourceIdentity != "" && !stsTrustAllows(role, "sts:SetSourceIdentity", "federated:"+identity.Provider.Arn, stsWithSourceIdentity(federation, sourceIdentity)) {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:SetSourceIdentity on %s", roleArn), http.StatusForbidden)
		return
	}
	duration, reqErr := stsRoleSessionDuration(r, role, false)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	packed, reqErr := stsCheckPackedPolicySize(policies, tokenTags)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	exp := time.Now().UTC().Add(duration)
	akid, secret, token := stsMintTempCred(exp)
	assumedArn := stsAssumedRoleArn(role, sessionName)
	iamTempCreds.Put(akid, IAMTempCred{AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
		RoleName: role.RoleName, PrincipalArn: assumedArn, Expiration: exp.Format(time.RFC3339),
		FederatedClaims: stsChainedClaims(federation),
		SessionTags:     sessionTags.Tags, TransitiveTagKeys: sessionTags.Transitive,
		SessionPolicy: policies.Inline, SessionPolicyArns: policies.Arns,
		SourceIdentity: sourceIdentity})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleWithWebIdentityResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
    <AssumedRoleUser><Arn>%s</Arn><AssumedRoleId>%s</AssumedRoleId></AssumedRoleUser>
    <SubjectFromWebIdentityToken>%s</SubjectFromWebIdentityToken>%s%s
  </AssumeRoleWithWebIdentityResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AssumeRoleWithWebIdentityResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(assumedArn), role.RoleId+":"+sessionName, xmlEscape(tokenSubject), stsPackedPolicySizeXML(packed),
		stsSourceIdentityXML(sourceIdentity), sim.NewUUID())
}

func handleSTSGetSessionToken(w http.ResponseWriter, r *http.Request) {
	duration, reqErr := stsRequestDuration(r, stsSessionTokenDurationMin, stsSessionTokenDurationMax, stsSessionTokenDurationDefault)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	callerKey := iamAccessKeyIDFromRequest(r)
	if _, temporary := iamTempCreds.Get(callerKey); temporary {
		stsErrorXML(w, "AccessDenied", "Cannot call GetSessionToken with session credentials", http.StatusForbidden)
		return
	}
	mfa, reqErr := stsRequestMFA(r)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	if reqErr := stsSpendRequestMFA(r, mfa); reqErr != nil {
		reqErr.write(w)
		return
	}
	caller, _ := iamCallerForAccessKey(callerKey)
	tc := IAMTempCred{UserName: caller.UserName, PrincipalArn: caller.Arn,
		MFA: mfa, CreatedAt: time.Now().UTC().Format(time.RFC3339), FromGetSessionToken: true,
		UnrestrictedIdentity: caller.Root}
	// An account root user's session lasts an hour by default and at most;
	// a longer request gets an hour.
	if caller.Root && (r.FormValue("DurationSeconds") == "" || duration > stsRootSessionTokenDurationMax) {
		duration = stsRootSessionTokenDurationMax
	}
	exp := time.Now().UTC().Add(time.Duration(duration) * time.Second)
	akid, secret, token := stsMintTempCred(exp)
	tc.AccessKeyID, tc.SecretAccessKey, tc.SessionToken = akid, secret, token
	tc.Expiration = exp.Format(time.RFC3339)
	iamTempCreds.Put(akid, tc)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetSessionTokenResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetSessionTokenResult><Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials></GetSessionTokenResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetSessionTokenResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339), sim.NewUUID())
}

// handleSTSGetFederationToken mints temporary credentials for a federated user,
// a named session that is not an IAM role.
func handleSTSGetFederationToken(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		stsErrorXML(w, "ValidationError", "Name is required", http.StatusBadRequest)
		return
	}
	policies, reqErr := stsRequestSessionPolicies(r)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	duration, reqErr := stsRequestDuration(r, 900, 129600, 43200)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	// A session's credentials cannot call GetFederationToken; an IAM user's
	// long-term key federates with the user's policies as the session's
	// identity.
	callerKey := iamAccessKeyIDFromRequest(r)
	if _, temporary := iamTempCreds.Get(callerKey); temporary {
		stsErrorXML(w, "AccessDenied", "Cannot call GetFederationToken with session credentials", http.StatusForbidden)
		return
	}
	caller, registered := iamCallerForAccessKey(callerKey)
	requestedTags, transitive := stsRequestSessionTags(r)
	sessionTags, err := stsNewSessionTags(stsSessionTags{}, requestedTags, transitive)
	if err != nil {
		stsErrorXML(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return
	}
	packed, reqErr := stsCheckPackedPolicySize(policies, requestedTags)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	exp := time.Now().UTC().Add(time.Duration(duration) * time.Second)
	akid, secret, token := stsMintTempCred(exp)
	fedAccount := iamRequestAccount(r)
	fedArn := fmt.Sprintf("arn:aws:sts::%s:federated-user/%s", fedAccount, name)
	fedUserID := fedAccount + ":" + name
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token, PrincipalArn: fedArn,
		Expiration: exp.Format(time.RFC3339), CreatedAt: time.Now().UTC().Format(time.RFC3339),
		SessionTags: sessionTags.Tags, TransitiveTagKeys: sessionTags.Transitive,
		SessionPolicy: policies.Inline, SessionPolicyArns: policies.Arns, SessionPolicyCaps: true,
		FederatingUser: caller.UserName, UnrestrictedIdentity: !registered,
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetFederationTokenResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetFederationTokenResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
    <FederatedUser><Arn>%s</Arn><FederatedUserId>%s</FederatedUserId></FederatedUser>%s
  </GetFederationTokenResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetFederationTokenResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(fedArn), xmlEscape(fedUserID), stsPackedPolicySizeXML(packed), sim.NewUUID())
}

// handleSTSAssumeRoleWithSAML assumes a role from a SAML assertion the named
// provider signed, minting temporary credentials as AssumeRole does once the
// role's trust policy admits the provider.
func handleSTSAssumeRoleWithSAML(w http.ResponseWriter, r *http.Request) {
	roleArn := r.FormValue("RoleArn")
	providerArn := r.FormValue("PrincipalArn")
	if roleArn == "" || providerArn == "" || r.FormValue("SAMLAssertion") == "" {
		stsErrorXML(w, "ValidationError", "RoleArn, PrincipalArn and SAMLAssertion are required", http.StatusBadRequest)
		return
	}
	policies, reqErr := stsRequestSessionPolicies(r)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	role, ok := iamRoleByArn(roleArn)
	if !ok {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:AssumeRoleWithSAML on %s", roleArn), http.StatusForbidden)
		return
	}
	acct := iamArnAccount(role.Arn)
	provider, ok := iamSAMLProviders.Get(providerArn)
	if !ok {
		stsErrorXML(w, "InvalidIdentityToken", fmt.Sprintf("Specified provider doesn't exist: %s", providerArn), http.StatusBadRequest)
		return
	}
	assertion, err := verifySAMLAssertion(r.FormValue("SAMLAssertion"), provider, time.Now().UTC())
	if errors.Is(err, errSAMLExpired) {
		stsErrorXML(w, "ExpiredTokenException", err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		stsErrorXML(w, "InvalidIdentityToken", err.Error(), http.StatusBadRequest)
		return
	}
	assertion.ProviderArn = provider.Arn
	assertion.ProviderName = provider.Name
	if assertion.SessionName == "" {
		stsErrorXML(w, "InvalidIdentityToken", "The SAML assertion has no RoleSessionName attribute", http.StatusBadRequest)
		return
	}
	// The assertion must offer this role through this provider, and the role must trust the provider for this subject.
	federation := assertion.conditionContext(acct)
	if !assertion.namesRole(role.Arn, provider.Arn) ||
		!stsTrustAllows(role, "sts:AssumeRoleWithSAML", "federated:"+provider.Arn, federation) {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:AssumeRoleWithSAML on %s", roleArn), http.StatusForbidden)
		return
	}
	assertionTags, assertionTransitive, err := assertion.sessionTags()
	if err != nil {
		stsErrorXML(w, "InvalidIdentityToken", err.Error(), http.StatusBadRequest)
		return
	}
	sessionTags, err := stsNewSessionTags(stsSessionTags{}, assertionTags, assertionTransitive)
	if err != nil {
		stsErrorXML(w, "InvalidIdentityToken", err.Error(), http.StatusBadRequest)
		return
	}
	if sessionTags.passed() && !stsTrustAllows(role, "sts:TagSession", "federated:"+provider.Arn, sessionTags.conditionContext(federation)) {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:TagSession on %s", roleArn), http.StatusForbidden)
		return
	}
	sourceIdentity, err := stsFederatedSourceIdentity(assertion.Attributes[stsSourceIdentitySAMLAttribute])
	if err != nil {
		stsErrorXML(w, "InvalidIdentityToken", err.Error(), http.StatusBadRequest)
		return
	}
	if sourceIdentity != "" && !stsTrustAllows(role, "sts:SetSourceIdentity", "federated:"+provider.Arn, stsWithSourceIdentity(federation, sourceIdentity)) {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:SetSourceIdentity on %s", roleArn), http.StatusForbidden)
		return
	}
	sessionName := assertion.SessionName
	duration, reqErr := stsRoleSessionDuration(r, role, false)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	packed, reqErr := stsCheckPackedPolicySize(policies, assertionTags)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	exp := time.Now().UTC().Add(duration)
	akid, secret, token := stsMintTempCred(exp)
	assumedArn := stsAssumedRoleArn(role, sessionName)
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
		RoleName: role.RoleName, PrincipalArn: assumedArn,
		Expiration: exp.Format(time.RFC3339), CreatedAt: time.Now().UTC().Format(time.RFC3339),
		FederatedClaims: stsChainedClaims(federation),
		SessionTags:     sessionTags.Tags, TransitiveTagKeys: sessionTags.Transitive,
		SessionPolicy: policies.Inline, SessionPolicyArns: policies.Arns,
		SourceIdentity: sourceIdentity,
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRoleWithSAMLResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleWithSAMLResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
    <AssumedRoleUser><Arn>%s</Arn><AssumedRoleId>%s</AssumedRoleId></AssumedRoleUser>
    <Subject>%s</Subject>
    <SubjectType>%s</SubjectType>
    <Issuer>%s</Issuer>
    <Audience>%s</Audience>
    <NameQualifier>%s</NameQualifier>%s%s
  </AssumeRoleWithSAMLResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AssumeRoleWithSAMLResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(assumedArn), role.RoleId+":"+xmlEscape(sessionName), xmlEscape(assertion.Subject),
		xmlEscape(assertion.SubjectType), xmlEscape(assertion.Issuer), xmlEscape(assertion.Recipient),
		xmlEscape(assertion.nameQualifier(acct)), stsPackedPolicySizeXML(packed), stsSourceIdentityXML(sourceIdentity), sim.NewUUID())
}

// handleSTSGetDelegatedAccessToken trades a token in for temporary credentials
// of the caller's principal. The simulator checks only that TradeInToken is
// present.
func handleSTSGetDelegatedAccessToken(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("TradeInToken") == "" {
		stsErrorXML(w, "ValidationError", "TradeInToken is required", http.StatusBadRequest)
		return
	}
	principal, _, ok := stsCallerIdentity(r)
	if !ok {
		stsErrorXML(w, "InvalidClientTokenId", sigMsgInvalidTok, http.StatusForbidden)
		return
	}
	exp := time.Now().UTC().Add(time.Duration(stsDurationSeconds(r)) * time.Second)
	akid, secret, token := stsMintTempCred(exp)
	traded := stsOnBehalfOf(iamAccessKeyIDFromRequest(r))
	traded.AccessKeyID, traded.SecretAccessKey, traded.SessionToken = akid, secret, token
	traded.PrincipalArn = principal
	traded.Expiration, traded.CreatedAt = exp.Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)
	iamTempCreds.Put(akid, traded)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetDelegatedAccessTokenResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetDelegatedAccessTokenResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
    <AssumedPrincipal>%s</AssumedPrincipal>
    <PackedPolicySize>0</PackedPolicySize>
  </GetDelegatedAccessTokenResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetDelegatedAccessTokenResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(principal), sim.NewUUID())
}

// handleSTSAssumeRoot mints temporary credentials for a member account's root
// user, scoped by the root-task policy TaskPolicyArn names. An IAM user or
// role of the organization's management account, or of its delegated
// administrator for IAM, opens the session once the organization enables
// root sessions.
func handleSTSAssumeRoot(w http.ResponseWriter, r *http.Request) {
	target := r.FormValue("TargetPrincipal")
	if target == "" {
		stsErrorXML(w, "ValidationError", "TargetPrincipal is required", http.StatusBadRequest)
		return
	}
	taskPolicy := r.FormValue("TaskPolicyArn.arn")
	if taskPolicy == "" {
		stsErrorXML(w, "ValidationError", "TaskPolicyArn is required", http.StatusBadRequest)
		return
	}
	if _, ok := stsRootTaskPolicies[taskPolicy]; !ok {
		stsErrorXML(w, "ValidationError",
			fmt.Sprintf("The TaskPolicyArn %s is not one of the root-task policies under arn:aws:iam::aws:policy/root-task/.", taskPolicy),
			http.StatusBadRequest)
		return
	}
	duration, reqErr := stsRequestDuration(r, 0, 900, 900)
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	target = strings.TrimSuffix(strings.TrimPrefix(target, "arn:aws:iam::"), ":root")
	if reqErr := stsAssumeRootAllowed(r, target); reqErr != nil {
		reqErr.write(w)
		return
	}
	exp := time.Now().UTC().Add(time.Duration(duration) * time.Second)
	akid, secret, token := stsMintTempCred(exp)
	rootArn := fmt.Sprintf("arn:aws:iam::%s:root", target)
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token, PrincipalArn: rootArn,
		Expiration: exp.Format(time.RFC3339), CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UnrestrictedIdentity: true, TaskPolicyArn: taskPolicy,
		SourceIdentity: stsCallerSourceIdentity(r),
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRootResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRootResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
  </AssumeRootResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AssumeRootResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339), sim.NewUUID())
}

// stsAssumeRootAllowed checks what AssumeRoot requires beyond the caller's
// sts:AssumeRoot permission: a caller that is not a root user, from the
// management account or the delegated administrator for IAM of an
// organization that enables root sessions, and a target that is one of the
// organization's active member accounts.
func stsAssumeRootAllowed(r *http.Request, target string) *stsRequestError {
	caller, registered := iamCallerForAccessKey(iamAccessKeyIDFromRequest(r))
	if !registered || strings.HasSuffix(caller.Arn, ":root") {
		return stsAccessDenied("A root user cannot call AssumeRoot.")
	}
	org, ok := orgOrg.Get(orgSingletonKey)
	if !ok {
		return stsAccessDenied("User: %s is not authorized to perform: sts:AssumeRoot because the account is not a member of an organization.", caller.Arn)
	}
	account := iamArnAccount(caller.Arn)
	if account != org.MasterAccountId && !iamOrgDelegatedAdministrator(account, iamCentralizedRootAccessService) {
		return stsAccessDenied("User: %s is not authorized to perform: sts:AssumeRoot because account %s is neither the management account nor a delegated administrator for IAM.", caller.Arn, account)
	}
	if !iamOrgFeatureEnabled("RootSessions") {
		return stsAccessDenied("User: %s is not authorized to perform: sts:AssumeRoot because the organization does not enable root sessions.", caller.Arn)
	}
	member, ok := orgAccounts.Get(target)
	if !ok || target == org.MasterAccountId || member.Status != "ACTIVE" {
		return stsAccessDenied("User: %s is not authorized to perform: sts:AssumeRoot on resource: arn:aws:iam::%s:root because it is not a member account of the organization.", caller.Arn, target)
	}
	return nil
}

// handleSTSDecodeAuthorizationMessage returns the JSON a base64 EncodedMessage
// holds. Any other message decodes to a fixed placeholder document, where AWS
// STS answers InvalidAuthorizationMessageException.
func handleSTSDecodeAuthorizationMessage(w http.ResponseWriter, r *http.Request) {
	encoded := r.FormValue("EncodedMessage")
	if encoded == "" {
		stsErrorXML(w, "ValidationError", "EncodedMessage is required", http.StatusBadRequest)
		return
	}
	decoded := `{"allowed":false,"decodedMessage":"simulator-decoded"}`
	if raw, err := base64.StdEncoding.DecodeString(encoded); err == nil {
		if json.Valid(raw) {
			decoded = string(raw)
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DecodeAuthorizationMessageResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <DecodeAuthorizationMessageResult><DecodedMessage>%s</DecodedMessage></DecodeAuthorizationMessageResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</DecodeAuthorizationMessageResponse>`, xmlEscape(decoded), sim.NewUUID())
}

// handleSTSGetAccessKeyInfo returns the account that owns an access key ID. A
// key the simulator does not hold answers the simulator's own account.
func handleSTSGetAccessKeyInfo(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("AccessKeyId") == "" {
		stsErrorXML(w, "ValidationError", "AccessKeyId is required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAccessKeyInfoResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetAccessKeyInfoResult><Account>%s</Account></GetAccessKeyInfoResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetAccessKeyInfoResponse>`, stsAccessKeyInfoAccount(r.FormValue("AccessKeyId")), sim.NewUUID())
}

func stsAccessKeyInfoAccount(akid string) string {
	if acct := iamAccessKeyAccount(akid); acct != "" {
		return acct
	}
	return awsAccountID()
}

func stsErrorXML(w http.ResponseWriter, code, message string, status int) {
	awsWriteError(w, awsErrorEnvelope{Protocol: awsQuery, Namespace: "https://sts.amazonaws.com/doc/2011-06-15/"}, status, code, message, "")
}

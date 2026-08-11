package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// STS: AssumeRole / AssumeRoleWithWebIdentity / GetSessionToken / GetCallerIdentity.
// Assume-role and get-session-token mint temporary credentials (ASIA… access
// keys) and record, in iamTempCreds, which principal they stand in for, so the
// call-time IAM enforcement gate (iam_enforcement.go) can resolve a temporary
// key back to the role (or user) whose policies should be evaluated.

// IAMTempCred binds a temporary (ASIA…) access key to the principal it
// represents: an assumed role (RoleName set) or a user session (UserName set).
type IAMTempCred struct {
	AccessKeyID string
	// SecretAccessKey and SessionToken are the credential material the caller
	// signs subsequent requests with. They are stored so the SigV4
	// authentication gate can verify a request signed with this temporary
	// credential (ASIA…) and confirm the presented X-Amz-Security-Token.
	SecretAccessKey string
	SessionToken    string
	UserName        string // set for GetSessionToken (the caller's user)
	RoleName        string // set for AssumeRole / AssumeRoleWithWebIdentity
	PrincipalArn    string // the caller-facing ARN (assumed-role/… or user/…)
	Expiration      string
	MFA             bool   // the session was authenticated with MFA
	CreatedAt       string // RFC3339, for aws:MultiFactorAuthAge
}

// stsRequestMFA reports whether the request presented an MFA device + code,
// which sets aws:MultiFactorAuthPresent on the resulting session.
func stsRequestMFA(r *http.Request) bool {
	return r.FormValue("SerialNumber") != "" && r.FormValue("TokenCode") != ""
}

var iamTempCreds sim.Store[IAMTempCred]

func registerSTS(r *sim.AWSQueryRouter, srv *sim.Server) {
	iamTempCreds = sim.MakeStore[IAMTempCred](srv.DB(), "iam_temp_creds")
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

// iamPrincipalForAccessKey resolves a SigV4 access-key id to the caller-facing
// ARN and the policy documents that govern it: a registered IAM user (AKIA…),
// or a temporary credential's role/user (ASIA…). ok is false for unknown/test
// credentials (the permissive default).
func iamPrincipalForAccessKey(akid string) (arn string, docs []iamPolicyDoc, userName string, ok bool) {
	if akid == "" {
		return "", nil, "", false
	}
	if tc, found := iamTempCreds.Get(akid); found {
		if tc.RoleName != "" {
			return tc.PrincipalArn, iamPolicyDocsForRole(tc.RoleName), "", true
		}
		if tc.UserName != "" {
			if u, uok := iamUsers.Get(tc.UserName); uok {
				return tc.PrincipalArn, iamEffectivePolicyDocsForUser(u.UserName), u.UserName, true
			}
		}
		return tc.PrincipalArn, nil, "", true
	}
	if key, found := iamAccessKeys.Get(akid); found {
		if u, uok := iamUsers.Get(key.UserName); uok {
			return u.Arn, iamEffectivePolicyDocsForUser(u.UserName), u.UserName, true
		}
	}
	return "", nil, "", false
}

func handleGetCallerIdentity(w http.ResponseWriter, r *http.Request) {
	acct := awsAccountID()
	arn := fmt.Sprintf("arn:aws:iam::%s:user/simulator", acct)
	userID := "AIDASIMULATORCALLER0"
	if principalArn, _, _, ok := iamPrincipalForAccessKey(iamAccessKeyIDFromRequest(r)); ok {
		arn = principalArn
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetCallerIdentityResult>
    <Arn>%s</Arn>
    <UserId>%s</UserId>
    <Account>%s</Account>
  </GetCallerIdentityResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetCallerIdentityResponse>`, xmlEscape(arn), userID, acct, generateUUID())
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

func stsMintTempCred() (akid, secret, token string) {
	return "ASIA" + strings.ToUpper(iamRandomB32(16)), iamRandomSecret(), iamRandomB32(64)
}

func handleSTSAssumeRole(w http.ResponseWriter, r *http.Request) {
	roleArn := r.FormValue("RoleArn")
	sessionName := r.FormValue("RoleSessionName")
	if roleArn == "" || sessionName == "" {
		stsErrorXML(w, "ValidationError", "RoleArn and RoleSessionName are required", http.StatusBadRequest)
		return
	}
	roleName := iamRoleNameFromArn(roleArn)
	role, ok := iamRoles.Get(roleName)
	if !ok {
		stsErrorXML(w, "AccessDenied",
			fmt.Sprintf("User is not authorized to perform: sts:AssumeRole on resource: %s (role not found)", roleArn),
			http.StatusForbidden)
		return
	}
	akid, secret, token := stsMintTempCred()
	exp := time.Now().UTC().Add(time.Duration(stsDurationSeconds(r)) * time.Second)
	assumedArn := fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/%s", awsAccountID(), role.RoleName, sessionName)
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
		RoleName: role.RoleName, PrincipalArn: assumedArn,
		Expiration: exp.Format(time.RFC3339),
		MFA:        stsRequestMFA(r), CreatedAt: time.Now().UTC().Format(time.RFC3339),
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
    </AssumedRoleUser>
  </AssumeRoleResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AssumeRoleResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(assumedArn), assumedRoleID, generateUUID())
}

func handleSTSAssumeRoleWithWebIdentity(w http.ResponseWriter, r *http.Request) {
	roleArn := r.FormValue("RoleArn")
	sessionName := r.FormValue("RoleSessionName")
	if roleArn == "" || sessionName == "" || r.FormValue("WebIdentityToken") == "" {
		stsErrorXML(w, "ValidationError", "RoleArn, RoleSessionName and WebIdentityToken are required", http.StatusBadRequest)
		return
	}
	roleName := iamRoleNameFromArn(roleArn)
	role, ok := iamRoles.Get(roleName)
	if !ok {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:AssumeRoleWithWebIdentity on %s", roleArn), http.StatusForbidden)
		return
	}
	// Verify the web identity token against the registered OpenID Connect
	// provider, the way STS does, rather than minting credentials for any token.
	tokenSubject, err := verifyWebIdentityToken(r.Context(), r.FormValue("WebIdentityToken"))
	if err != nil {
		stsErrorXML(w, "InvalidIdentityToken", err.Error(), http.StatusBadRequest)
		return
	}
	akid, secret, token := stsMintTempCred()
	exp := time.Now().UTC().Add(time.Duration(stsDurationSeconds(r)) * time.Second)
	assumedArn := fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/%s", awsAccountID(), role.RoleName, sessionName)
	iamTempCreds.Put(akid, IAMTempCred{AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token, RoleName: role.RoleName, PrincipalArn: assumedArn, Expiration: exp.Format(time.RFC3339)})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleWithWebIdentityResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
    <AssumedRoleUser><Arn>%s</Arn><AssumedRoleId>%s</AssumedRoleId></AssumedRoleUser>
    <SubjectFromWebIdentityToken>%s</SubjectFromWebIdentityToken>
  </AssumeRoleWithWebIdentityResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AssumeRoleWithWebIdentityResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(assumedArn), role.RoleId+":"+sessionName, xmlEscape(tokenSubject), generateUUID())
}

func handleSTSGetSessionToken(w http.ResponseWriter, r *http.Request) {
	akid, secret, token := stsMintTempCred()
	exp := time.Now().UTC().Add(time.Duration(stsDurationSeconds(r)) * time.Second)
	// Bind the session token to the caller's user (if registered) so it inherits
	// the user's policies under enforcement.
	tc := IAMTempCred{AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
		Expiration: exp.Format(time.RFC3339),
		MFA:        stsRequestMFA(r), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if _, _, userName, ok := iamPrincipalForAccessKey(iamAccessKeyIDFromRequest(r)); ok && userName != "" {
		if u, uok := iamUsers.Get(userName); uok {
			tc.UserName = userName
			tc.PrincipalArn = u.Arn
		}
	}
	iamTempCreds.Put(akid, tc)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetSessionTokenResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetSessionTokenResult><Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials></GetSessionTokenResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetSessionTokenResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339), generateUUID())
}

// handleSTSGetFederationToken mints temporary credentials for a federated user
// (a named session that is not an IAM role), returning Credentials, the
// FederatedUser identity, and PackedPolicySize.
func handleSTSGetFederationToken(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		stsErrorXML(w, "ValidationError", "Name is required", http.StatusBadRequest)
		return
	}
	akid, secret, token := stsMintTempCred()
	exp := time.Now().UTC().Add(time.Duration(stsDurationSeconds(r)) * time.Second)
	fedArn := fmt.Sprintf("arn:aws:sts::%s:federated-user/%s", awsAccountID(), name)
	fedUserID := awsAccountID() + ":" + name
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token, PrincipalArn: fedArn,
		Expiration: exp.Format(time.RFC3339), CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetFederationTokenResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetFederationTokenResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
    <FederatedUser><Arn>%s</Arn><FederatedUserId>%s</FederatedUserId></FederatedUser>
    <PackedPolicySize>0</PackedPolicySize>
  </GetFederationTokenResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetFederationTokenResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(fedArn), xmlEscape(fedUserID), generateUUID())
}

// handleSTSAssumeRoleWithSAML assumes a role from a SAML assertion, minting
// temporary credentials exactly as AssumeRole does (the SAML assertion stands
// in for the trust-policy check the sim does not enforce).
func handleSTSAssumeRoleWithSAML(w http.ResponseWriter, r *http.Request) {
	roleArn := r.FormValue("RoleArn")
	if roleArn == "" || r.FormValue("PrincipalArn") == "" || r.FormValue("SAMLAssertion") == "" {
		stsErrorXML(w, "ValidationError", "RoleArn, PrincipalArn and SAMLAssertion are required", http.StatusBadRequest)
		return
	}
	roleName := iamRoleNameFromArn(roleArn)
	role, ok := iamRoles.Get(roleName)
	if !ok {
		stsErrorXML(w, "AccessDenied", fmt.Sprintf("Not authorized to perform sts:AssumeRoleWithSAML on %s", roleArn), http.StatusForbidden)
		return
	}
	// The session name for SAML is derived from the assertion subject; AWS uses
	// the SAML subject's NameID. Use a stable simulator subject.
	subject := "sim-saml-subject"
	sessionName := subject
	akid, secret, token := stsMintTempCred()
	exp := time.Now().UTC().Add(time.Duration(stsDurationSeconds(r)) * time.Second)
	assumedArn := fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/%s", awsAccountID(), role.RoleName, sessionName)
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
		RoleName: role.RoleName, PrincipalArn: assumedArn,
		Expiration: exp.Format(time.RFC3339), CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRoleWithSAMLResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleWithSAMLResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
    <AssumedRoleUser><Arn>%s</Arn><AssumedRoleId>%s</AssumedRoleId></AssumedRoleUser>
    <Subject>%s</Subject>
    <SubjectType>persistent</SubjectType>
    <Issuer>https://sim.local/saml</Issuer>
    <Audience>https://signin.aws.amazon.com/saml</Audience>
    <NameQualifier>sim-name-qualifier</NameQualifier>
    <PackedPolicySize>0</PackedPolicySize>
  </AssumeRoleWithSAMLResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AssumeRoleWithSAMLResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(assumedArn), role.RoleId+":"+sessionName, subject, generateUUID())
}

// handleSTSGetWebIdentityToken issues a signed web-identity token (a JWT) and
// its expiration. Unlike the assume-role variants this returns the token
// itself, not credentials.
func handleSTSGetWebIdentityToken(w http.ResponseWriter, r *http.Request) {
	exp := time.Now().UTC().Add(time.Duration(stsDurationSeconds(r)) * time.Second)
	// A simulator JWT: three base64url segments so a client parsing the dot-form
	// gets a structurally-valid token. Not cryptographically signed.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"iss":"https://sim.local","aud":%q,"exp":%d}`, r.FormValue("Audience"), exp.Unix())))
	jwt := header + "." + claims + "." + base64.RawURLEncoding.EncodeToString([]byte("sim-signature"))
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetWebIdentityTokenResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetWebIdentityTokenResult><WebIdentityToken>%s</WebIdentityToken><Expiration>%s</Expiration></GetWebIdentityTokenResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetWebIdentityTokenResponse>`, xmlEscape(jwt), exp.Format(time.RFC3339), generateUUID())
}

// handleSTSGetDelegatedAccessToken trades a token in for temporary credentials,
// returning the credentials and the principal they grant access to.
func handleSTSGetDelegatedAccessToken(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("TradeInToken") == "" {
		stsErrorXML(w, "ValidationError", "TradeInToken is required", http.StatusBadRequest)
		return
	}
	akid, secret, token := stsMintTempCred()
	exp := time.Now().UTC().Add(time.Duration(stsDurationSeconds(r)) * time.Second)
	principal := fmt.Sprintf("arn:aws:iam::%s:user/simulator", awsAccountID())
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token, PrincipalArn: principal,
		Expiration: exp.Format(time.RFC3339), CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetDelegatedAccessTokenResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetDelegatedAccessTokenResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
    <AssumedPrincipal>%s</AssumedPrincipal>
    <PackedPolicySize>0</PackedPolicySize>
  </GetDelegatedAccessTokenResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetDelegatedAccessTokenResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(principal), generateUUID())
}

// handleSTSAssumeRoot mints temporary credentials for a member account's root
// user, returning Credentials and the SourceIdentity.
func handleSTSAssumeRoot(w http.ResponseWriter, r *http.Request) {
	target := r.FormValue("TargetPrincipal")
	if target == "" {
		stsErrorXML(w, "ValidationError", "TargetPrincipal is required", http.StatusBadRequest)
		return
	}
	akid, secret, token := stsMintTempCred()
	exp := time.Now().UTC().Add(time.Duration(stsDurationSeconds(r)) * time.Second)
	rootArn := fmt.Sprintf("arn:aws:iam::%s:root", target)
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token, PrincipalArn: rootArn,
		Expiration: exp.Format(time.RFC3339), CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRootResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRootResult>
    <Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials>
    <SourceIdentity>%s</SourceIdentity>
  </AssumeRootResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</AssumeRootResponse>`, akid, xmlEscape(secret), xmlEscape(token), exp.Format(time.RFC3339),
		xmlEscape(target), generateUUID())
}

// handleSTSDecodeAuthorizationMessage decodes an encoded authorization failure
// message into its JSON DecodedMessage. The sim stores the original JSON in the
// encoded blob (base64 of the JSON), so decoding round-trips it; a non-base64
// or non-JSON blob yields an empty decoded object rather than erroring.
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
</DecodeAuthorizationMessageResponse>`, xmlEscape(decoded), generateUUID())
}

// handleSTSGetAccessKeyInfo returns the account that owns a supplied access key
// id. The sim resolves it to the simulator account.
func handleSTSGetAccessKeyInfo(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("AccessKeyId") == "" {
		stsErrorXML(w, "ValidationError", "AccessKeyId is required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAccessKeyInfoResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetAccessKeyInfoResult><Account>%s</Account></GetAccessKeyInfoResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetAccessKeyInfoResponse>`, awsAccountID(), generateUUID())
}

func stsErrorXML(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		code, xmlEscape(message), generateUUID())
}

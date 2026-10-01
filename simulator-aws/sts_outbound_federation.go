package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/simjwt"
)

// IAM outbound identity federation: once an account enables it, AWS STS
// GetWebIdentityToken issues JWTs for the caller's identity, signed by keys the
// account's issuer publishes. The issuer is unique per account —
// `https://<id>.tokens.sts.global.api.aws` — and serves the OpenID Connect
// discovery document at /.well-known/openid-configuration and the JSON Web Key
// Set at /.well-known/jwks.json, so any relying party verifies the token the
// way it verifies any OpenID Connect issuer.

const (
	outboundFederationEnabledKey = "OutboundWebIdentityFederation"
	outboundFederationIssuerKey  = "OutboundWebIdentityIssuer"
	outboundIssuerHostSuffix     = ".tokens.sts.global.api.aws"
	outboundDiscoveryPath        = "/.well-known/openid-configuration"
	outboundJWKSPath             = "/.well-known/jwks.json"

	// stsClaimNamespace keys the AWS-specific claims of an outbound token.
	stsClaimNamespace = "https://sts.amazonaws.com/"

	webIdentityTokenDefaultSeconds = 300
	webIdentityTokenMinSeconds     = 60
	webIdentityTokenMaxSeconds     = 3600
	webIdentityTokenMaxAudiences   = 10
	webIdentityTokenMaxTags        = 50
)

var (
	stsOutboundKeys       sim.Store[string]
	stsOutboundSignersMu  sync.Mutex
	stsOutboundSignerByID map[string]*simjwt.Signer
)

func registerSTSOutboundFederationState(srv *sim.Server) {
	stsOutboundKeys = sim.MakeStore[string](srv.DB(), "sts_outbound_signing_keys")
	stsOutboundSignersMu.Lock()
	stsOutboundSignerByID = map[string]*simjwt.Signer{}
	stsOutboundSignersMu.Unlock()
}

// stsOutboundSigner returns the account issuer's key for one signing
// algorithm, generating and persisting it on first use so tokens issued
// before a restart keep verifying.
func stsOutboundSigner(alg string) (*simjwt.Signer, error) {
	stsOutboundSignersMu.Lock()
	defer stsOutboundSignersMu.Unlock()
	if s, ok := stsOutboundSignerByID[alg]; ok {
		return s, nil
	}
	s, err := simjwt.LoadOrCreate(stsOutboundKeys, alg, alg)
	if err != nil {
		return nil, err
	}
	stsOutboundSignerByID[alg] = s
	return s, nil
}

func stsOutboundSigners() ([]*simjwt.Signer, error) {
	var out []*simjwt.Signer
	for _, alg := range []string{simjwt.RS256, simjwt.ES384} {
		s, err := stsOutboundSigner(alg)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// outboundIssuer returns the account's issuer URL, which exists from the
// first EnableOutboundWebIdentityFederation on and survives a disable.
func outboundIssuer() (string, bool) {
	f, ok := iamAccountFlags.Get(outboundFederationIssuerKey)
	if !ok || f.Value == "" {
		return "", false
	}
	return "https://" + f.Value + outboundIssuerHostSuffix, true
}

func outboundFederationEnabled() bool {
	_, ok := iamAccountFlags.Get(outboundFederationEnabledKey)
	return ok
}

func handleIAMEnableOutboundWebIdentityFederation(w http.ResponseWriter, r *http.Request) {
	if outboundFederationEnabled() {
		iamErrorXML(w, "FeatureEnabled", "Outbound identity federation is already enabled for this account.", http.StatusConflict)
		return
	}
	issuer, ok := outboundIssuer()
	if !ok {
		iamAccountFlags.Put(outboundFederationIssuerKey, IAMAccountFeature{Key: outboundFederationIssuerKey, Value: sim.NewUUID()})
		issuer, _ = outboundIssuer()
	}
	iamAccountFlags.Put(outboundFederationEnabledKey, IAMAccountFeature{Key: outboundFederationEnabledKey, Value: "enabled"})
	iamResultXML(w, "EnableOutboundWebIdentityFederation",
		fmt.Sprintf("<IssuerIdentifier>%s</IssuerIdentifier>", xmlEscape(issuer)))
}

func handleIAMDisableOutboundWebIdentityFederation(w http.ResponseWriter, r *http.Request) {
	if !outboundFederationEnabled() {
		iamErrorXML(w, "FeatureDisabled", "Outbound identity federation is already disabled for this account.", http.StatusNotFound)
		return
	}
	iamAccountFlags.Delete(outboundFederationEnabledKey)
	iamEmptyResultXML(w, "DisableOutboundWebIdentityFederation")
}

func handleIAMGetOutboundWebIdentityFederationInfo(w http.ResponseWriter, r *http.Request) {
	issuer, ok := outboundIssuer()
	if !ok {
		iamErrorXML(w, "FeatureDisabled", "Outbound identity federation has never been enabled for this account.", http.StatusNotFound)
		return
	}
	iamResultXML(w, "GetOutboundWebIdentityFederationInfo",
		fmt.Sprintf("<IssuerIdentifier>%s</IssuerIdentifier><JwtVendingEnabled>%t</JwtVendingEnabled>",
			xmlEscape(issuer), outboundFederationEnabled()))
}

// handleSTSGetWebIdentityToken issues a JWT for the calling identity, signed
// with the account issuer's key for the requested algorithm.
func handleSTSGetWebIdentityToken(w http.ResponseWriter, r *http.Request) {
	audiences := snsListMembers(r, "Audience")
	if len(audiences) == 0 || len(audiences) > webIdentityTokenMaxAudiences {
		stsErrorXML(w, "ValidationError",
			fmt.Sprintf("1 validation error detected: Value at 'audience' failed to satisfy constraint: Member must have length between 1 and %d", webIdentityTokenMaxAudiences),
			http.StatusBadRequest)
		return
	}
	alg := r.FormValue("SigningAlgorithm")
	if alg != simjwt.RS256 && alg != simjwt.ES384 {
		stsErrorXML(w, "ValidationError",
			fmt.Sprintf("1 validation error detected: Value '%s' at 'signingAlgorithm' failed to satisfy constraint: Member must satisfy enum value set: [RS256, ES384]", alg),
			http.StatusBadRequest)
		return
	}
	duration := webIdentityTokenDefaultSeconds
	if raw := r.FormValue("DurationSeconds"); raw != "" {
		d, err := strconv.Atoi(raw)
		if err != nil || d < webIdentityTokenMinSeconds || d > webIdentityTokenMaxSeconds {
			stsErrorXML(w, "ValidationError",
				fmt.Sprintf("1 validation error detected: Value '%s' at 'durationSeconds' failed to satisfy constraint: Member must have value between %d and %d",
					raw, webIdentityTokenMinSeconds, webIdentityTokenMaxSeconds),
				http.StatusBadRequest)
			return
		}
		duration = d
	}
	tags, ok := stsRequestTags(r)
	if !ok {
		stsErrorXML(w, "ValidationError",
			fmt.Sprintf("1 validation error detected: Value at 'tags' failed to satisfy constraint: Member must have length less than or equal to %d", webIdentityTokenMaxTags),
			http.StatusBadRequest)
		return
	}
	issuer, _ := outboundIssuer()
	if !outboundFederationEnabled() {
		stsErrorXML(w, "OutboundWebIdentityFederationDisabledException",
			"Outbound web identity federation is not enabled for this account.", http.StatusForbidden)
		return
	}

	now := time.Now().UTC().Truncate(time.Second)
	exp := now.Add(time.Duration(duration) * time.Second)
	if tc, found := iamTempCreds.Get(iamAccessKeyIDFromRequest(r)); found {
		if sessionEnd, err := time.Parse(time.RFC3339, tc.Expiration); err == nil && exp.After(sessionEnd) {
			stsErrorXML(w, "SessionDurationEscalationException",
				"The requested token duration would extend the session beyond its original expiration time.", http.StatusForbidden)
			return
		}
	}
	signer, err := stsOutboundSigner(alg)
	if err != nil {
		stsErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError)
		return
	}
	principal, _, ok := stsCallerIdentity(r)
	if !ok {
		stsErrorXML(w, "InvalidClientTokenId", sigMsgInvalidTok, http.StatusForbidden)
		return
	}
	aws := map[string]any{
		"aws_account":   awsAccountID(),
		"source_region": awsRegion(),
		"principal_id":  principal,
	}
	if len(tags) > 0 {
		aws["request_tags"] = tags
	}
	var aud any = audiences
	if len(audiences) == 1 {
		aud = audiences[0]
	}
	token, err := signer.Sign(map[string]any{
		"iss":             issuer,
		"sub":             principal,
		"aud":             aud,
		"iat":             now.Unix(),
		"nbf":             now.Unix(),
		"exp":             exp.Unix(),
		"jti":             sim.NewUUID(),
		stsClaimNamespace: aws,
	})
	if err != nil {
		stsErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetWebIdentityTokenResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetWebIdentityTokenResult><WebIdentityToken>%s</WebIdentityToken><Expiration>%s</Expiration></GetWebIdentityTokenResult>
  <ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>
</GetWebIdentityTokenResponse>`, xmlEscape(token), exp.Format(time.RFC3339), sim.NewUUID())
}

// stsRequestTags reads the Tags.member.N.Key / .Value pairs of an AWS Query
// request.
func stsRequestTags(r *http.Request) (map[string]string, bool) {
	tags := map[string]string{}
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
		if key == "" {
			break
		}
		if i > webIdentityTokenMaxTags {
			return nil, false
		}
		tags[key] = r.FormValue(fmt.Sprintf("Tags.member.%d.Value", i))
	}
	return tags, true
}

// registerSTSOutboundIssuer serves the account issuer's discovery document
// and key set on the issuer's own host.
func registerSTSOutboundIssuer(srv *sim.Server) {
	srv.WrapHandler(stsOutboundIssuerMiddleware)
}

func stsOutboundIssuerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer, ok := outboundIssuer()
		if !ok || !strings.EqualFold(requestHostname(r.Host), strings.TrimPrefix(issuer, "https://")) {
			next.ServeHTTP(w, r)
			return
		}
		handleSTSOutboundIssuer(w, r, issuer)
	})
}

func requestHostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

func handleSTSOutboundIssuer(w http.ResponseWriter, r *http.Request, issuer string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	signers, err := stsOutboundSigners()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	switch r.URL.Path {
	case outboundDiscoveryPath:
		sim.WriteJSON(w, http.StatusOK, simjwt.Discovery(issuer, issuer+outboundJWKSPath, []string{"id_token"}, signers...))
	case outboundJWKSPath:
		sim.WriteJSON(w, http.StatusOK, simjwt.JWKS(signers...))
	default:
		http.NotFound(w, r)
	}
}

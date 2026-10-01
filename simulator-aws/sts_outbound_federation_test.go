package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/simjwt"
)

func resetOutboundFederationState(t *testing.T) {
	flags, temp, keys, outbound := iamAccountFlags, iamTempCreds, iamAccessKeys, stsOutboundKeys
	users, userPolicies := iamUsers, iamUserPolicies
	t.Cleanup(func() {
		iamAccountFlags, iamTempCreds, iamAccessKeys, stsOutboundKeys = flags, temp, keys, outbound
		iamUsers, iamUserPolicies = users, userPolicies
	})
	iamAccountFlags = sim.NewStateStore[IAMAccountFeature]()
	iamTempCreds = sim.NewStateStore[IAMTempCred]()
	iamAccessKeys = sim.NewStateStore[IAMAccessKey]()
	iamUsers = sim.NewStateStore[IAMUser]()
	iamUserPolicies = sim.NewStateStore[IAMUserPolicy]()
	seedRootAdminCredential()
	stsOutboundKeys = sim.NewStateStore[string]()
	stsOutboundSignersMu.Lock()
	stsOutboundSignerByID = map[string]*simjwt.Signer{}
	stsOutboundSignersMu.Unlock()
}

func callQuery(handler http.HandlerFunc, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+seedAdminAccessKey+"/20260101/us-east-1/sts/aws4_request, SignedHeaders=host, Signature=0")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func xmlErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `xml:"Code"`
		} `xml:"Error"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body: %v\n%s", err, rec.Body.String())
	}
	return body.Error.Code
}

func TestOutboundFederationLifecycle(t *testing.T) {
	resetOutboundFederationState(t)

	rec := callQuery(handleIAMGetOutboundWebIdentityFederationInfo, nil)
	if rec.Code != http.StatusNotFound || xmlErrorCode(t, rec) != "FeatureDisabled" {
		t.Fatalf("info before enable: %d %s", rec.Code, rec.Body.String())
	}
	rec = callQuery(handleSTSGetWebIdentityToken, url.Values{"Audience.member.1": {"rp"}, "SigningAlgorithm": {"RS256"}})
	if rec.Code != http.StatusForbidden || xmlErrorCode(t, rec) != "OutboundWebIdentityFederationDisabledException" {
		t.Fatalf("token before enable: %d %s", rec.Code, rec.Body.String())
	}

	rec = callQuery(handleIAMEnableOutboundWebIdentityFederation, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	issuer, _ := outboundIssuer()
	host := strings.TrimPrefix(issuer, "https://")
	if !strings.HasSuffix(host, ".tokens.sts.global.api.aws") || !strings.Contains(rec.Body.String(), issuer) {
		t.Fatalf("issuer %q, body %s", issuer, rec.Body.String())
	}
	if rec = callQuery(handleIAMEnableOutboundWebIdentityFederation, nil); rec.Code != http.StatusConflict || xmlErrorCode(t, rec) != "FeatureEnabled" {
		t.Fatalf("second enable: %d %s", rec.Code, rec.Body.String())
	}
	if rec = callQuery(handleIAMDisableOutboundWebIdentityFederation, nil); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d", rec.Code)
	}
	if rec = callQuery(handleIAMDisableOutboundWebIdentityFederation, nil); rec.Code != http.StatusNotFound || xmlErrorCode(t, rec) != "FeatureDisabled" {
		t.Fatalf("second disable: %d %s", rec.Code, rec.Body.String())
	}
	rec = callQuery(handleIAMGetOutboundWebIdentityFederationInfo, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<JwtVendingEnabled>false</JwtVendingEnabled>") ||
		!strings.Contains(rec.Body.String(), issuer) {
		t.Fatalf("info after disable: %s", rec.Body.String())
	}
	callQuery(handleIAMEnableOutboundWebIdentityFederation, nil)
	if again, _ := outboundIssuer(); again != issuer {
		t.Fatalf("re-enabling changed the issuer: %q → %q", issuer, again)
	}
}

func TestGetWebIdentityTokenValidates(t *testing.T) {
	resetOutboundFederationState(t)
	callQuery(handleIAMEnableOutboundWebIdentityFederation, nil)
	for name, form := range map[string]url.Values{
		"no audience":  {"SigningAlgorithm": {"RS256"}},
		"algorithm":    {"Audience.member.1": {"rp"}, "SigningAlgorithm": {"HS256"}},
		"short":        {"Audience.member.1": {"rp"}, "SigningAlgorithm": {"RS256"}, "DurationSeconds": {"59"}},
		"long":         {"Audience.member.1": {"rp"}, "SigningAlgorithm": {"RS256"}, "DurationSeconds": {"3601"}},
		"not a number": {"Audience.member.1": {"rp"}, "SigningAlgorithm": {"RS256"}, "DurationSeconds": {"x"}},
	} {
		if rec := callQuery(handleSTSGetWebIdentityToken, form); rec.Code != http.StatusBadRequest || xmlErrorCode(t, rec) != "ValidationError" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	iamTempCreds.Put("ASIASHORT", IAMTempCred{
		AccessKeyID: "ASIASHORT", PrincipalArn: "arn:aws:sts::123456789012:assumed-role/r/s",
		Expiration: time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
	})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(url.Values{
		"Audience.member.1": {"rp"}, "SigningAlgorithm": {"RS256"}, "DurationSeconds": {"600"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIASHORT/20260101/us-east-1/sts/aws4_request, SignedHeaders=host, Signature=00")
	rec := httptest.NewRecorder()
	handleSTSGetWebIdentityToken(rec, req)
	if rec.Code != http.StatusForbidden || xmlErrorCode(t, rec) != "SessionDurationEscalationException" {
		t.Fatalf("token outliving the session: %d %s", rec.Code, rec.Body.String())
	}
}

// A relying party verifies the token the way it verifies any OpenID Connect
// issuer: discovery and the key set fetched from the issuer's own host.
func TestGetWebIdentityTokenVerifiesAgainstTheIssuer(t *testing.T) {
	resetOutboundFederationState(t)
	callQuery(handleIAMEnableOutboundWebIdentityFederation, nil)
	issuer, _ := outboundIssuer()

	srv := httptest.NewTLSServer(stsOutboundIssuerMiddleware(http.NotFoundHandler()))
	defer srv.Close()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	client := &http.Client{Transport: &http.Transport{
		// Resolve the issuer's host to the test listener, and trust its
		// certificate for the name the listener's certificate carries.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"},
	}}
	ctx := oidc.ClientContext(context.Background(), client)
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		t.Fatalf("discover %s: %v", issuer, err)
	}

	for _, alg := range []string{"RS256", "ES384"} {
		rec := callQuery(handleSTSGetWebIdentityToken, url.Values{
			"Audience.member.1": {"https://rp.example"}, "SigningAlgorithm": {alg},
			"Tags.member.1.Key": {"team"}, "Tags.member.1.Value": {"platform"},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s token: %d %s", alg, rec.Code, rec.Body.String())
		}
		var out struct {
			Token      string `xml:"GetWebIdentityTokenResult>WebIdentityToken"`
			Expiration string `xml:"GetWebIdentityTokenResult>Expiration"`
		}
		if err := xml.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		verifier := provider.Verifier(&oidc.Config{ClientID: "https://rp.example", SupportedSigningAlgs: []string{alg}})
		idToken, err := verifier.Verify(ctx, out.Token)
		if err != nil {
			t.Fatalf("%s token did not verify against the issuer: %v", alg, err)
		}
		if idToken.Subject != iamUserArn(seedAdminUserName, "/") {
			t.Errorf("sub = %q", idToken.Subject)
		}
		if got := idToken.Expiry.Sub(idToken.IssuedAt); got != 300*time.Second {
			t.Errorf("default lifetime = %v, want 5m", got)
		}
		var claims map[string]any
		if err := idToken.Claims(&claims); err != nil {
			t.Fatal(err)
		}
		aws, _ := claims["https://sts.amazonaws.com/"].(map[string]any)
		tags, _ := aws["request_tags"].(map[string]any)
		if aws["aws_account"] != "123456789012" || tags["team"] != "platform" {
			t.Errorf("AWS claims = %v", aws)
		}
	}

	// Disabling stops issuance but keeps the keys published, so tokens
	// already issued keep verifying.
	callQuery(handleIAMDisableOutboundWebIdentityFederation, nil)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	req.Host = strings.TrimPrefix(issuer, "https://")
	rec := httptest.NewRecorder()
	stsOutboundIssuerMiddleware(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"kid"`) != 2 {
		t.Fatalf("JWKS after disable: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSTSCallerIdentityResolvesEachKindOfCaller(t *testing.T) {
	resetOutboundFederationState(t)
	roles := iamRoles
	t.Cleanup(func() { iamRoles = roles })
	iamRoles = sim.NewStateStore[IAMRole]()
	iamRoles.Put("deployer", IAMRole{RoleName: "deployer", RoleId: "AROADEPLOYER000000000"})
	iamTempCreds.Put("ASIAROLE", IAMTempCred{AccessKeyID: "ASIAROLE", RoleName: "deployer",
		PrincipalArn: "arn:aws:sts::123456789012:assumed-role/deployer/nightly"})
	iamTempCreds.Put("ASIAFED", IAMTempCred{AccessKeyID: "ASIAFED",
		PrincipalArn: "arn:aws:sts::123456789012:federated-user/alice"})
	signedBy := func(akid string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+akid+"/20260101/us-east-1/sts/aws4_request, SignedHeaders=host, Signature=0")
		return req
	}
	for akid, want := range map[string][2]string{
		seedAdminAccessKey: {iamUserArn(seedAdminUserName, "/"), "AIDAROOTSIMADMIN00000"},
		"ASIAROLE":         {"arn:aws:sts::123456789012:assumed-role/deployer/nightly", "AROADEPLOYER000000000:nightly"},
		"ASIAFED":          {"arn:aws:sts::123456789012:federated-user/alice", awsAccountID() + ":alice"},
	} {
		arn, userID, ok := stsCallerIdentity(signedBy(akid))
		if !ok || arn != want[0] || userID != want[1] {
			t.Errorf("%s: identity = %q %q %v, want %q %q", akid, arn, userID, ok, want[0], want[1])
		}
	}
	if _, _, ok := stsCallerIdentity(signedBy("AKIAUNKNOWN")); ok {
		t.Error("an unknown access key resolved to an identity")
	}
	rec := httptest.NewRecorder()
	handleGetCallerIdentity(rec, signedBy("AKIAUNKNOWN"))
	if rec.Code != http.StatusForbidden || xmlErrorCode(t, rec) != "InvalidClientTokenId" {
		t.Fatalf("GetCallerIdentity for an unknown key = %d %s", rec.Code, rec.Body.String())
	}
}

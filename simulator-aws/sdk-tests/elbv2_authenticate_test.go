package aws_sdk_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ciptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// issueDNSValidatedCertificate requests an ACM certificate for domain and
// answers its DNS validation in a hosted zone, so it reaches ISSUED.
func issueDNSValidatedCertificate(t *testing.T, domain, zone string) string {
	t.Helper()
	acmC, r53C := acmClient(), r53Client()
	requested, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName: aws.String(domain), ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	certArn := aws.ToString(requested.CertificateArn)
	desc, err := acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(certArn)})
	require.NoError(t, err)
	created, err := r53C.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name: aws.String(zone), CallerReference: aws.String(uniquename.New("zone")),
	})
	require.NoError(t, err)
	var changes []r53types.Change
	for _, dvo := range desc.Certificate.DomainValidationOptions {
		require.NotNil(t, dvo.ResourceRecord)
		changes = append(changes, r53types.Change{Action: r53types.ChangeActionUpsert, ResourceRecordSet: &r53types.ResourceRecordSet{
			Name: dvo.ResourceRecord.Name, Type: r53types.RRTypeCname, TTL: aws.Int64(60),
			ResourceRecords: []r53types.ResourceRecord{{Value: dvo.ResourceRecord.Value}},
		}})
	}
	_, err = r53C.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: created.HostedZone.Id, ChangeBatch: &r53types.ChangeBatch{Changes: changes},
	})
	require.NoError(t, err)
	waitForACMCertificateIssued(t, acmC, certArn)
	return certArn
}

// albBrowser is a browser in front of an HTTPS Application Load Balancer: it
// reaches the load balancer's DNS name at the address the Route 53 Resolver
// answers, reaches the user pool's domain, issuer and the load balancers'
// public-keys endpoint at the simulator, and keeps cookies.
func albBrowser(t *testing.T, lbName, lbAddress string, stopAt func(*url.URL) bool) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	albTransport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(lbAddress, port))
		},
	}
	return &http.Client{
		Jar: jar,
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Hostname() == lbName {
				return albTransport.RoundTrip(r)
			}
			require.True(t, isCognitoName(r.URL.Hostname()) || isELBPublicKeysName(r.URL.Hostname()), r.URL.String())
			clone := r.Clone(r.Context())
			clone.URL.Scheme = "http"
			return simHTTPClient.GetTransport().RoundTrip(clone)
		}),
		CheckRedirect: func(r *http.Request, _ []*http.Request) error {
			if stopAt != nil && stopAt(r.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		},
		Timeout: 30 * time.Second,
	}
}

// verifyALBClaims checks x-amzn-oidc-data the way the guide tells a target
// to: fetch the public key the kid names from the Region's public-keys
// endpoint, verify the ES256 signature, and compare the signer with the load
// balancer's ARN.
func verifyALBClaims(t *testing.T, browser *http.Client, data, lbArn string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(data, ".")
	require.Len(t, parts, 3)
	decode := func(s string) []byte {
		raw, err := base64.URLEncoding.DecodeString(s)
		require.NoError(t, err, "the token's parts are base64 URL encoded with padding")
		return raw
	}
	var header, payload map[string]any
	require.NoError(t, json.Unmarshal(decode(parts[0]), &header))
	require.NoError(t, json.Unmarshal(decode(parts[1]), &payload))
	assert.Equal(t, "ES256", header["alg"])
	assert.Equal(t, lbArn, header["signer"], "the signer is the load balancer's ARN")

	resp, err := browser.Get("https://public-keys.auth.elb.us-east-1.amazonaws.com/" + header["kid"].(string))
	require.NoError(t, err)
	body := cognitoRead(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	block, _ := pem.Decode([]byte(body))
	require.NotNil(t, block, "the endpoint serves the key as PEM")
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	key, ok := parsed.(*ecdsa.PublicKey)
	require.True(t, ok)
	signature := decode(parts[2])
	require.Len(t, signature, 64)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	assert.True(t, ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])),
		"x-amzn-oidc-data verifies against the published key")
	return header, payload
}

// TestELBv2_AuthenticateActionsSignUsersIn puts an HTTPS Application Load
// Balancer in front of a target and signs a user in through an Amazon Cognito
// user pool twice: through an authenticate-cognito default action and through
// an authenticate-oidc rule that names the pool's endpoints. The target sees
// the user's claims signed by the load balancer; a deny rule answers 401 and
// an allow rule forwards without claims; expiring the session cookie, as an
// application's logout does, sends the user back to the IdP.
func TestELBv2_AuthenticateActionsSignUsersIn(t *testing.T) {
	t.Parallel()
	elb, ec2c, cog := elbv2Client(), ec2Client(), cognitoClient()

	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.37.0.0/16")})
	require.NoError(t, err)
	var subnets []string
	for i, az := range []string{"us-east-1a", "us-east-1b"} {
		sn, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
			VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.37." + strconv.Itoa(i+1) + ".0/24"), AvailabilityZone: aws.String(az),
		})
		require.NoError(t, err)
		subnets = append(subnets, aws.ToString(sn.Subnet.SubnetId))
	}

	type seen struct {
		Path, Identity, AccessToken, Data string
	}
	requests := make(chan seen, 16)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			return
		}
		if r.URL.Path == "/logout" {
			// "the application must set the expiry to -1 for all authentication
			// cookies."
			for _, name := range []string{"AWSELBAuthSessionCookie-0", "AWSELBAuthSessionCookie-1"} {
				http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Secure: true})
			}
		}
		requests <- seen{r.URL.Path, r.Header.Get("X-Amzn-Oidc-Identity"), r.Header.Get("X-Amzn-Oidc-Accesstoken"), r.Header.Get("X-Amzn-Oidc-Data")}
		_, _ = w.Write([]byte("target:" + r.URL.Path))
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)
	targetHost, targetPortText, err := net.SplitHostPort(targetURL.Host)
	require.NoError(t, err)
	targetPort, err := strconv.Atoi(targetPortText)
	require.NoError(t, err)

	certArn := issueDNSValidatedCertificate(t, "app.elb-auth.test", "elb-auth.test.")
	alb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniquename.New("alb-auth")), Type: elbtypes.LoadBalancerTypeEnumApplication, Subnets: subnets,
	})
	require.NoError(t, err)
	lb := alb.LoadBalancers[0]
	lbArn, lbName := aws.ToString(lb.LoadBalancerArn), aws.ToString(lb.DNSName)
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(context.Background(), &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	})
	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String(uniquename.New("alb-auth-tg")), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(int32(targetPort)),
		VpcId: vpc.Vpc.VpcId, TargetType: elbtypes.TargetTypeEnumIp, HealthCheckPath: aws.String("/healthz"),
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)
	targetDesc := elbtypes.TargetDescription{Id: aws.String(targetHost), Port: aws.Int32(int32(targetPort))}
	_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{TargetGroupArn: aws.String(tgArn), Targets: []elbtypes.TargetDescription{targetDesc}})
	require.NoError(t, err)

	const listenerPort = 9443
	origin := "https://" + lbName + ":" + strconv.Itoa(listenerPort)
	pool, err := cog.CreateUserPool(ctx, &cip.CreateUserPoolInput{PoolName: aws.String(uniquename.New("alb-users"))})
	require.NoError(t, err)
	poolID, poolArn := aws.ToString(pool.UserPool.Id), aws.ToString(pool.UserPool.Arn)
	domain := strings.ToLower(uniquename.New("alb-signin"))
	_, err = cog.CreateUserPoolDomain(ctx, &cip.CreateUserPoolDomainInput{UserPoolId: aws.String(poolID), Domain: aws.String(domain)})
	require.NoError(t, err)
	newClient := func(name string) (string, string) {
		created, err := cog.CreateUserPoolClient(ctx, &cip.CreateUserPoolClientInput{
			UserPoolId: aws.String(poolID), ClientName: aws.String(name), GenerateSecret: true,
			AllowedOAuthFlows: []ciptypes.OAuthFlowType{ciptypes.OAuthFlowTypeCode}, AllowedOAuthFlowsUserPoolClient: true,
			AllowedOAuthScopes: []string{"openid", "email"}, CallbackURLs: []string{origin + "/oauth2/idpresponse"},
			SupportedIdentityProviders: []string{"COGNITO"},
		})
		require.NoError(t, err)
		return aws.ToString(created.UserPoolClient.ClientId), aws.ToString(created.UserPoolClient.ClientSecret)
	}
	cognitoClientID, _ := newClient("alb-cognito")
	oidcClientID, oidcSecret := newClient("alb-oidc")
	_, err = cog.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId: aws.String(poolID), Username: aws.String("frank"), MessageAction: ciptypes.MessageActionTypeSuppress,
		UserAttributes: []ciptypes.AttributeType{{Name: aws.String("email"), Value: aws.String("frank@example.com")}},
	})
	require.NoError(t, err)
	_, err = cog.AdminSetUserPassword(ctx, &cip.AdminSetUserPasswordInput{
		UserPoolId: aws.String(poolID), Username: aws.String("frank"), Password: aws.String("Frank-Pass-1234"), Permanent: true,
	})
	require.NoError(t, err)

	cognitoAction := func(onUnauthenticated elbtypes.AuthenticateCognitoActionConditionalBehaviorEnum, cookie string) elbtypes.Action {
		return elbtypes.Action{Type: elbtypes.ActionTypeEnumAuthenticateCognito, Order: aws.Int32(1),
			AuthenticateCognitoConfig: &elbtypes.AuthenticateCognitoActionConfig{
				UserPoolArn: aws.String(poolArn), UserPoolClientId: aws.String(cognitoClientID), UserPoolDomain: aws.String(domain),
				Scope: aws.String("openid email"), OnUnauthenticatedRequest: onUnauthenticated, SessionCookieName: aws.String(cookie),
			}}
	}
	forward := elbtypes.Action{Type: elbtypes.ActionTypeEnumForward, Order: aws.Int32(2), TargetGroupArn: aws.String(tgArn)}

	plain, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(9080),
		DefaultActions: []elbtypes.Action{forward},
	})
	require.NoError(t, err)
	_, err = elb.CreateRule(ctx, &elbv2.CreateRuleInput{
		ListenerArn: plain.Listeners[0].ListenerArn, Priority: aws.Int32(1),
		Conditions: []elbtypes.RuleCondition{{Field: aws.String("path-pattern"), Values: []string{"/*"}}},
		Actions:    []elbtypes.Action{cognitoAction("", "AWSELBAuthSessionCookie"), forward},
	})
	var badAction *elbtypes.InvalidLoadBalancerActionException
	require.ErrorAs(t, err, &badAction, "authenticate actions are supported only with HTTPS listeners")

	listener, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumHttps, Port: aws.Int32(listenerPort),
		Certificates:   []elbtypes.Certificate{{CertificateArn: aws.String(certArn)}},
		DefaultActions: []elbtypes.Action{cognitoAction("", "AWSELBAuthSessionCookie"), forward},
	})
	require.NoError(t, err)
	listenerArn := listener.Listeners[0].ListenerArn
	described, err := elb.DescribeListeners(ctx, &elbv2.DescribeListenersInput{ListenerArns: []string{aws.ToString(listenerArn)}})
	require.NoError(t, err)
	reported := described.Listeners[0].DefaultActions[0].AuthenticateCognitoConfig
	require.NotNil(t, reported)
	assert.Equal(t, int64(604800), aws.ToInt64(reported.SessionTimeout), "the session lasts 604800 seconds by default")
	assert.Equal(t, elbtypes.AuthenticateCognitoActionConditionalBehaviorEnumAuthenticate, reported.OnUnauthenticatedRequest)

	for i, rule := range []struct {
		path    string
		actions []elbtypes.Action
	}{
		{"/api/*", []elbtypes.Action{cognitoAction(elbtypes.AuthenticateCognitoActionConditionalBehaviorEnumDeny, "ApiSession"), forward}},
		{"/public/*", []elbtypes.Action{cognitoAction(elbtypes.AuthenticateCognitoActionConditionalBehaviorEnumAllow, "PublicSession"), forward}},
		{"/oidc/*", []elbtypes.Action{{Type: elbtypes.ActionTypeEnumAuthenticateOidc, Order: aws.Int32(1),
			AuthenticateOidcConfig: &elbtypes.AuthenticateOidcActionConfig{
				Issuer:                aws.String("https://cognito-idp.us-east-1.amazonaws.com/" + poolID),
				AuthorizationEndpoint: aws.String("https://" + domain + ".auth.us-east-1.amazoncognito.com/oauth2/authorize"),
				TokenEndpoint:         aws.String("https://" + domain + ".auth.us-east-1.amazoncognito.com/oauth2/token"),
				UserInfoEndpoint:      aws.String("https://" + domain + ".auth.us-east-1.amazoncognito.com/oauth2/userInfo"),
				ClientId:              aws.String(oidcClientID), ClientSecret: aws.String(oidcSecret),
				Scope: aws.String("openid email"), SessionCookieName: aws.String("OidcSession"),
				AuthenticationRequestExtraParams: map[string]string{"lang": "en"},
			}}, forward}},
	} {
		_, err := elb.CreateRule(ctx, &elbv2.CreateRuleInput{
			ListenerArn: listenerArn, Priority: aws.Int32(int32(i + 1)),
			Conditions: []elbtypes.RuleCondition{{Field: aws.String("path-pattern"), Values: []string{rule.path}}},
			Actions:    rule.actions,
		})
		require.NoError(t, err, rule.path)
	}

	waitForELBv2TargetHealth(t, tgArn, targetDesc, elbtypes.TargetHealthStateEnumHealthy)
	lbAddress, _ := dnsResolveA(t, lbName)
	signIn := func(browser *http.Client, path string) seen {
		t.Helper()
		resp, err := browser.Get(origin + path)
		require.NoError(t, err)
		page := cognitoRead(t, resp)
		require.Equal(t, "/login", resp.Request.URL.Path, "the load balancer sends a user without a session to the IdP: %s", page)
		resp, err = browser.PostForm(resp.Request.URL.String(), url.Values{"username": {"frank"}, "password": {"Frank-Pass-1234"}})
		require.NoError(t, err)
		body := cognitoRead(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		assert.Equal(t, "target:"+strings.SplitN(path, "?", 2)[0], body)
		assert.Equal(t, origin+path, resp.Request.URL.String(), "the load balancer sends the user back to the original URI")
		return <-requests
	}

	t.Run("authenticate-cognito", func(t *testing.T) {
		var first *url.URL
		browser := albBrowser(t, lbName, lbAddress, func(u *url.URL) bool {
			if first == nil {
				first = u
			}
			return false
		})
		got := signIn(browser, "/app?view=1")
		require.NotNil(t, first)
		q := first.Query()
		assert.Equal(t, domain+".auth.us-east-1.amazoncognito.com", first.Host)
		assert.Equal(t, "/oauth2/authorize", first.Path)
		assert.Equal(t, "code", q.Get("response_type"))
		assert.Equal(t, cognitoClientID, q.Get("client_id"))
		assert.Equal(t, origin+"/oauth2/idpresponse", q.Get("redirect_uri"))
		assert.Equal(t, "openid email", q.Get("scope"))
		assert.NotEmpty(t, q.Get("state"))
		assert.NotEmpty(t, got.AccessToken)
		header, claims := verifyALBClaims(t, browser, got.Data, lbArn)
		assert.Equal(t, cognitoClientID, header["client"])
		assert.Equal(t, "https://cognito-idp.us-east-1.amazonaws.com/"+poolID, header["iss"])
		assert.Equal(t, "frank@example.com", claims["email"])
		assert.Equal(t, claims["sub"], got.Identity, "x-amzn-oidc-identity is the user info's subject")

		resp, err := browser.Get(origin + "/again")
		require.NoError(t, err)
		assert.Equal(t, "target:/again", cognitoRead(t, resp), "the session cookie carries the user on")
		again := <-requests
		assert.Equal(t, got.Identity, again.Identity)

		spoof, err := http.NewRequest(http.MethodGet, origin+"/again", nil)
		require.NoError(t, err)
		spoof.Header.Set("X-Amzn-Oidc-Identity", "mallory")
		resp, err = browser.Do(spoof)
		require.NoError(t, err)
		_ = cognitoRead(t, resp)
		assert.Equal(t, got.Identity, (<-requests).Identity, "the load balancer replaces a client's claims headers")

		resp, err = browser.Get(origin + "/logout")
		require.NoError(t, err)
		_ = cognitoRead(t, resp)
		<-requests
		stopped := albBrowser(t, lbName, lbAddress, func(u *url.URL) bool { return u.Hostname() != lbName })
		stopped.Jar = browser.Jar
		resp, err = stopped.Get(origin + "/app")
		require.NoError(t, err)
		_ = cognitoRead(t, resp)
		require.Equal(t, http.StatusFound, resp.StatusCode, "after logout the load balancer sends the user to the IdP again")
		assert.Contains(t, resp.Header.Get("Location"), domain+".auth.us-east-1.amazoncognito.com/oauth2/authorize")
	})

	t.Run("authenticate-oidc", func(t *testing.T) {
		var first *url.URL
		browser := albBrowser(t, lbName, lbAddress, func(u *url.URL) bool {
			if first == nil {
				first = u
			}
			return false
		})
		got := signIn(browser, "/oidc/home")
		assert.Equal(t, oidcClientID, first.Query().Get("client_id"))
		assert.Equal(t, "en", first.Query().Get("lang"), "AuthenticationRequestExtraParams ride on the authorization request")
		header, claims := verifyALBClaims(t, browser, got.Data, lbArn)
		assert.Equal(t, oidcClientID, header["client"])
		assert.Equal(t, "frank@example.com", claims["email"])
		cookies := browser.Jar.Cookies(&url.URL{Scheme: "https", Host: lbName + ":" + strconv.Itoa(listenerPort), Path: "/"})
		var names []string
		for _, c := range cookies {
			names = append(names, c.Name)
		}
		assert.Contains(t, names, "OidcSession-0", "the session cookie takes the action's SessionCookieName, sharded from -0")
	})

	t.Run("deny and allow", func(t *testing.T) {
		browser := albBrowser(t, lbName, lbAddress, func(u *url.URL) bool { return u.Hostname() != lbName })
		resp, err := browser.Get(origin + "/api/items")
		require.NoError(t, err)
		_ = cognitoRead(t, resp)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "deny answers a request without a session with 401")
		resp, err = browser.Get(origin + "/public/page")
		require.NoError(t, err)
		assert.Equal(t, "target:/public/page", cognitoRead(t, resp))
		got := <-requests
		assert.Empty(t, got.Data, "allow forwards a request without a session and without claims")
		assert.Empty(t, got.Identity)
	})

	forger := albBrowser(t, lbName, lbAddress, func(u *url.URL) bool { return u.Hostname() != lbName })
	resp, err := forger.Get(origin + "/oauth2/idpresponse?code=forged&state=forged")
	require.NoError(t, err)
	_ = cognitoRead(t, resp)
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "/oauth2/authorize", "a callback without the load balancer's state signs nobody in")
	for _, c := range forger.Jar.Cookies(&url.URL{Scheme: "https", Host: lbName + ":" + strconv.Itoa(listenerPort), Path: "/"}) {
		assert.NotContains(t, c.Name, "AWSELBAuthSessionCookie")
	}
}

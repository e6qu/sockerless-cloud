package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/e6qu/sockerless-cloud/testutil/samlidp"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionTagRole creates a role with the given trust policy whose own policy
// allows iam:ListRoles to a principal tagged team=blue and iam:ListUsers to one
// tagged project=apollo, so what a session may do reports its principal tags.
func sessionTagRole(t *testing.T, prefix, trust string) string {
	t.Helper()
	admin := iamClient()
	name := uniqueName(prefix)
	role, err := admin.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(trust)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}) })
	_, err = admin.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("by-tag"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"iam:ListRoles","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalTag/team":"blue"}}},
		{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalTag/project":"apollo"}}},
		{"Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession"],"Resource":"*"}]}`)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("by-tag")})
	})
	return aws.ToString(role.Role.Arn)
}

// sessionGrants reports which of the two tag-scoped grants a session's
// principal tags satisfy.
func sessionGrants(t *testing.T, creds *ststypes.Credentials) (team, project bool) {
	t.Helper()
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(creds.AccessKeyId), aws.ToString(creds.SecretAccessKey), aws.ToString(creds.SessionToken))}
	session := iam.NewFromConfig(cfg, func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, roles := session.ListRoles(ctx, &iam.ListRolesInput{})
	_, users := session.ListUsers(ctx, &iam.ListUsersInput{})
	return roles == nil, users == nil
}

func sessionSTS(creds *ststypes.Credentials) *sts.Client {
	return sts.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(creds.AccessKeyId), aws.ToString(creds.SecretAccessKey), aws.ToString(creds.SessionToken))},
		func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
}

// TestSTS_AssumeRoleSessionTagsArePrincipalTags passes session tags on
// AssumeRole: the session reports them as aws:PrincipalTag, a role whose trust
// policy does not allow sts:TagSession refuses them, and a chained session
// keeps only the transitive ones.
func TestSTS_AssumeRoleSessionTagsArePrincipalTags(t *testing.T) {
	akid, secret := restrictedCredential(t, "session-tagger",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession"],"Resource":"*"}]}`)
	caller := sts.NewFromConfig(keyConfig(akid, secret), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	identity, err := caller.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	userArn := aws.ToString(identity.Arn)

	tagged := sessionTagRole(t, "tagged", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"AWS":"`+userArn+`"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`)
	untaggable := sessionTagRole(t, "untaggable", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"AWS":"`+userArn+`"},"Action":"sts:AssumeRole"}]}`)
	chained := sessionTagRole(t, "chained", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"AWS":"`+tagged+`"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`)

	tags := []ststypes.Tag{{Key: aws.String("team"), Value: aws.String("blue")}, {Key: aws.String("project"), Value: aws.String("apollo")}}
	first, err := caller.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(tagged), RoleSessionName: aws.String("tagged"),
		Tags: tags, TransitiveTagKeys: []string{"team"}})
	require.NoError(t, err)
	team, project := sessionGrants(t, first.Credentials)
	assert.True(t, team, "the session's team tag is a principal tag")
	assert.True(t, project, "the session's project tag is a principal tag")

	plain, err := caller.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(tagged), RoleSessionName: aws.String("plain")})
	require.NoError(t, err)
	team, project = sessionGrants(t, plain.Credentials)
	assert.False(t, team || project, "a session passed no tags has none")

	_, err = caller.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(untaggable), RoleSessionName: aws.String("refused"), Tags: tags})
	assert.Equal(t, "AccessDenied", errCodeOf(err), "the trust policy does not allow sts:TagSession")

	second, err := sessionSTS(first.Credentials).AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(chained), RoleSessionName: aws.String("chained")})
	require.NoError(t, err)
	team, project = sessionGrants(t, second.Credentials)
	assert.True(t, team, "the transitive team tag passes to the chained session")
	assert.False(t, project, "the project tag is not transitive")

	_, err = sessionSTS(first.Credentials).AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(chained), RoleSessionName: aws.String("override"),
		Tags: []ststypes.Tag{{Key: aws.String("team"), Value: aws.String("red")}}})
	assert.Equal(t, "InvalidParameterValue", errCodeOf(err), "a chained session cannot replace a transitive tag")

	_, err = caller.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(tagged), RoleSessionName: aws.String("duplicate"),
		Tags: []ststypes.Tag{{Key: aws.String("team"), Value: aws.String("blue")}, {Key: aws.String("Team"), Value: aws.String("red")}}})
	assert.Equal(t, "InvalidParameterValue", errCodeOf(err), "tag keys are case-insensitive")
}

// TestSTS_AssumeRoleWithSAMLSessionTags reads session tags from an
// assertion's PrincipalTag attributes, authorizing sts:TagSession against a
// trust policy conditioned on saml:aud.
func TestSTS_AssumeRoleWithSAMLSessionTags(t *testing.T) {
	idp, err := samlidp.New("https://idp.tags.example.test/saml")
	require.NoError(t, err)
	admin := iamClient()
	provider, err := admin.CreateSAMLProvider(ctx, &iam.CreateSAMLProviderInput{
		Name: aws.String(uniqueName("tags-idp")), SAMLMetadataDocument: aws.String(idp.Metadata())})
	require.NoError(t, err)
	providerArn := aws.ToString(provider.SAMLProviderArn)
	t.Cleanup(func() {
		_, _ = admin.DeleteSAMLProvider(ctx, &iam.DeleteSAMLProviderInput{SAMLProviderArn: aws.String(providerArn)})
	})
	taggable := sessionTagRole(t, "saml-taggable", `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"Federated":"`+providerArn+`"},"Action":"sts:AssumeRoleWithSAML"},
		{"Effect":"Allow","Principal":{"Federated":"`+providerArn+`"},"Action":"sts:TagSession",
		 "Condition":{"StringEquals":{"saml:aud":"https://signin.aws.amazon.com/saml","aws:RequestTag/team":"blue"}}}]}`)
	untaggable := sessionTagRole(t, "saml-untaggable", `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"Federated":"`+providerArn+`"},"Action":"sts:AssumeRoleWithSAML"}]}`)

	assume := func(roleArn string, attributes map[string][]string) (*sts.AssumeRoleWithSAMLOutput, error) {
		attributes[samlidp.RoleAttribute] = []string{roleArn + "," + providerArn}
		attributes[samlidp.RoleSessionNameAttribute] = []string{"alice"}
		response, err := idp.Response(samlidp.Assertion{Subject: "alice@example.test", Attributes: attributes})
		require.NoError(t, err)
		return stsClient().AssumeRoleWithSAML(ctx, &sts.AssumeRoleWithSAMLInput{
			RoleArn: aws.String(roleArn), PrincipalArn: aws.String(providerArn), SAMLAssertion: aws.String(response)})
	}
	out, err := assume(taggable, map[string][]string{
		"https://aws.amazon.com/SAML/Attributes/PrincipalTag:team":    {"blue"},
		"https://aws.amazon.com/SAML/Attributes/PrincipalTag:project": {"apollo"},
	})
	require.NoError(t, err)
	team, project := sessionGrants(t, out.Credentials)
	assert.True(t, team)
	assert.True(t, project)

	_, err = assume(taggable, map[string][]string{"https://aws.amazon.com/SAML/Attributes/PrincipalTag:team": {"red"}})
	assert.Equal(t, "AccessDenied", errCodeOf(err), "the trust policy allows tagging the session team=blue only")
	_, err = assume(untaggable, map[string][]string{"https://aws.amazon.com/SAML/Attributes/PrincipalTag:team": {"blue"}})
	assert.Equal(t, "AccessDenied", errCodeOf(err), "the trust policy does not allow sts:TagSession")
	untagged, err := assume(untaggable, map[string][]string{})
	require.NoError(t, err, "an assertion without session tags needs no sts:TagSession")
	team, _ = sessionGrants(t, untagged.Credentials)
	assert.False(t, team)
}

// TestSTS_AssumeRoleWithWebIdentitySessionTags reads session tags from a
// token's https://aws.amazon.com/tags claim.
func TestSTS_AssumeRoleWithWebIdentitySessionTags(t *testing.T) {
	issuer := newWebIdentityIssuer(t)
	const audience = "session-tags"
	admin := iamClient()
	provider, err := admin.CreateOpenIDConnectProvider(ctx, &iam.CreateOpenIDConnectProviderInput{
		Url: aws.String(issuer.server.URL), ClientIDList: []string{audience},
		ThumbprintList: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}})
	require.NoError(t, err)
	providerArn := aws.ToString(provider.OpenIDConnectProviderArn)
	t.Cleanup(func() {
		_, _ = admin.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(providerArn)})
	})
	taggable := sessionTagRole(t, "oidc-taggable", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Federated":"`+providerArn+`"},"Action":["sts:AssumeRoleWithWebIdentity","sts:TagSession"]}]}`)
	untaggable := sessionTagRole(t, "oidc-untaggable", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Federated":"`+providerArn+`"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`)

	token := func(tags map[string]any) string {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: issuer.key},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", issuer.keyID))
		require.NoError(t, err)
		raw, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer.server.URL, Subject: "operator",
			Audience: jwt.Audience{audience}, IssuedAt: jwt.NewNumericDate(time.Now()),
			Expiry: jwt.NewNumericDate(time.Now().Add(10 * time.Minute))}).
			Claims(map[string]any{"https://aws.amazon.com/tags": tags}).Serialize()
		require.NoError(t, err)
		return raw
	}
	tags := map[string]any{"principal_tags": map[string]any{"team": []string{"blue"}}, "transitive_tag_keys": []string{"team"}}
	assume := func(roleArn string) (*sts.AssumeRoleWithWebIdentityOutput, error) {
		return stsClient().AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{RoleArn: aws.String(roleArn),
			RoleSessionName: aws.String("operator"), WebIdentityToken: aws.String(token(tags))})
	}
	out, err := assume(taggable)
	require.NoError(t, err)
	team, project := sessionGrants(t, out.Credentials)
	assert.True(t, team, "the token's team tag is a principal tag")
	assert.False(t, project)
	_, err = assume(untaggable)
	assert.Equal(t, "AccessDenied", errCodeOf(err), "the trust policy does not allow sts:TagSession")
}

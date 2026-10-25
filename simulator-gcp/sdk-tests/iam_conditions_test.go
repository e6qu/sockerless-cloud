package gcp_sdk_test

import (
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/apigateway/v1"
	"google.golang.org/api/cloudkms/v1"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
)

// conditionPrincipal is a service account with a minted key, a role of its
// own holding exactly the permissions a test grants, and the KMS and API
// Gateway clients that authenticate as it.
type conditionPrincipal struct {
	member string
	role   string
	kms    *cloudkms.Service
	apigw  *apigateway.Service
}

func newConditionPrincipal(t *testing.T, permissions ...string) conditionPrincipal {
	t.Helper()
	saName, _, keyFile := mintServiceAccountKeyFile(t, uniquename.New("cond"))
	credentials := tokenSourceFromKeyFile(t, keyFile)
	role, err := iamService(t).Projects.Roles.Create("projects/test-project", &iam.CreateRoleRequest{
		RoleId: strings.ReplaceAll(uniquename.New("condRole"), "-", "_"),
		Role:   &iam.Role{Title: "Condition test role", IncludedPermissions: permissions, Stage: "GA"},
	}).Do()
	require.NoError(t, err)
	kmsClient, err := cloudkms.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(credentials.TokenSource))
	require.NoError(t, err)
	apigwClient, err := apigateway.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(credentials.TokenSource))
	require.NoError(t, err)
	return conditionPrincipal{
		member: "serviceAccount:" + saName[strings.LastIndex(saName, "/")+1:],
		role:   role.Name,
		kms:    kmsClient,
		apigw:  apigwClient,
	}
}

// conditionTestKey is a crypto key in test-project to bind conditional roles on.
func conditionTestKey(t *testing.T, svc *cloudkms.Service) (ringName, keyName string) {
	t.Helper()
	parent := "projects/test-project/locations/global"
	ringID := uniquename.New("cond-ring")
	ringName = parent + "/keyRings/" + ringID
	_, err := svc.Projects.Locations.KeyRings.Create(parent, &cloudkms.KeyRing{}).KeyRingId(ringID).Do()
	require.NoError(t, err)
	keyName = ringName + "/cryptoKeys/cond-key"
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{Purpose: "ENCRYPT_DECRYPT"}).CryptoKeyId("cond-key").Do()
	require.NoError(t, err)
	return ringName, keyName
}

func kmsConditionalPolicy(role, member string, condition *cloudkms.Expr) *cloudkms.SetIamPolicyRequest {
	return &cloudkms.SetIamPolicyRequest{Policy: &cloudkms.Policy{
		Version:  3,
		Bindings: []*cloudkms.Binding{{Role: role, Members: []string{member}, Condition: condition}},
	}}
}

// A binding conditioned on the resource attributes grants its role on the
// resources the condition names: resource.type and resource.name are the
// values the IAM Conditions resource attribute reference gives a Cloud KMS
// crypto key, and the key ring, a different type, is outside the grant.
func TestIAMConditions_CloudKMSResourceAttributes(t *testing.T) {
	t.Parallel()
	owner := kmsService(t)
	ringName, keyName := conditionTestKey(t, owner)
	principal := newConditionPrincipal(t, "cloudkms.cryptoKeys.get", "cloudkms.keyRings.get")
	asked := []string{"cloudkms.cryptoKeys.get", "cloudkms.cryptoKeys.update"}

	condition := &cloudkms.Expr{
		Title:      "this key",
		Expression: `resource.type == "cloudkms.googleapis.com/CryptoKey" && resource.name == "` + keyName + `"`,
	}
	set, err := owner.Projects.Locations.KeyRings.CryptoKeys.SetIamPolicy(keyName, kmsConditionalPolicy(principal.role, principal.member, condition)).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)
	require.NotNil(t, set.Bindings[0].Condition, "setIamPolicy keeps the binding's condition")
	assert.Equal(t, condition.Expression, set.Bindings[0].Condition.Expression)

	got, err := owner.Projects.Locations.KeyRings.CryptoKeys.GetIamPolicy(keyName).OptionsRequestedPolicyVersion(3).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	require.NotNil(t, got.Bindings[0].Condition, "getIamPolicy returns the condition")
	assert.Equal(t, "this key", got.Bindings[0].Condition.Title)

	held, err := principal.kms.Projects.Locations.KeyRings.CryptoKeys.TestIamPermissions(keyName,
		&cloudkms.TestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"cloudkms.cryptoKeys.get"}, held.Permissions,
		"the condition names this key, so the role's permissions are held on it")

	// The same role bound on the key ring under a condition for crypto keys
	// grants nothing there: the ring's resource.type is KeyRing.
	_, err = owner.Projects.Locations.KeyRings.SetIamPolicy(ringName, kmsConditionalPolicy(principal.role, principal.member, &cloudkms.Expr{
		Title: "keys only", Expression: `resource.type == "cloudkms.googleapis.com/CryptoKey"`,
	})).Do()
	require.NoError(t, err)
	none, err := principal.kms.Projects.Locations.KeyRings.TestIamPermissions(ringName,
		&cloudkms.TestIamPermissionsRequest{Permissions: []string{"cloudkms.keyRings.get"}}).Do()
	require.NoError(t, err)
	assert.Empty(t, none.Permissions, "a condition on another resource type does not hold on the key ring")

	// A condition naming another key does not hold on this one.
	_, err = owner.Projects.Locations.KeyRings.CryptoKeys.SetIamPolicy(keyName, kmsConditionalPolicy(principal.role, principal.member, &cloudkms.Expr{
		Title: "other key", Expression: `resource.name.endsWith("/cryptoKeys/other")`,
	})).Do()
	require.NoError(t, err)
	none, err = principal.kms.Projects.Locations.KeyRings.CryptoKeys.TestIamPermissions(keyName,
		&cloudkms.TestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Empty(t, none.Permissions)
}

// A resource inherits the tags bound to its project, and a binding
// conditioned on resource.matchTag, resource.matchTagId, resource.hasTagKey
// or resource.hasTagKeyId grants its role while the resource carries the tag.
func TestIAMConditions_TagsBoundToTheProjectReachItsResources(t *testing.T) {
	t.Parallel()
	owner := kmsService(t)
	ringName, keyName := conditionTestKey(t, owner)
	principal := newConditionPrincipal(t, "cloudkms.cryptoKeys.get")
	asked := []string{"cloudkms.cryptoKeys.get"}

	tags := crmV3Service(t)
	keyShortName := strings.ReplaceAll(uniquename.New("env"), "-", "_")
	keyOp, err := tags.TagKeys.Create(&crm.TagKey{Parent: "organizations/123456789012", ShortName: keyShortName}).Do()
	require.NoError(t, err)
	tagKey := crmOpResourceName(t, keyOp)
	t.Cleanup(func() { _, _ = tags.TagKeys.Delete(tagKey).Do() })
	valueOp, err := tags.TagValues.Create(&crm.TagValue{Parent: tagKey, ShortName: "prod"}).Do()
	require.NoError(t, err)
	tagValue := crmOpResourceName(t, valueOp)
	t.Cleanup(func() { _, _ = tags.TagValues.Delete(tagValue).Do() })

	projectNumber := requireProject(t, "test-project")
	bindingOp, err := tags.TagBindings.Create(&crm.TagBinding{
		Parent:   "//cloudresourcemanager.googleapis.com/projects/" + projectNumber,
		TagValue: tagValue,
	}).Do()
	require.NoError(t, err)
	binding := crmOpResourceName(t, bindingOp)
	t.Cleanup(func() { _, _ = tags.TagBindings.Delete(binding).Do() })

	// The key ring inherits the project's tag.
	inForce, err := tags.EffectiveTags.List().Parent("//cloudkms.googleapis.com/" + ringName).Do()
	require.NoError(t, err)
	require.Len(t, inForce.EffectiveTags, 1, "the ring carries the tag bound to its project")
	assert.Equal(t, tagValue, inForce.EffectiveTags[0].TagValue)
	assert.True(t, inForce.EffectiveTags[0].Inherited)
	assert.Equal(t, "123456789012/"+keyShortName, inForce.EffectiveTags[0].NamespacedTagKey)

	for _, condition := range []struct {
		expression string
		held       bool
	}{
		{`resource.matchTag("123456789012/` + keyShortName + `", "prod")`, true},
		{`resource.matchTagId("` + tagKey + `", "` + tagValue + `")`, true},
		{`resource.hasTagKey("123456789012/` + keyShortName + `")`, true},
		{`resource.hasTagKeyId("` + tagKey + `")`, true},
		{`resource.matchTag("123456789012/` + keyShortName + `", "dev")`, false},
		{`resource.hasTagKey("123456789012/other_key")`, false},
	} {
		_, err := owner.Projects.Locations.KeyRings.CryptoKeys.SetIamPolicy(keyName, kmsConditionalPolicy(principal.role, principal.member, &cloudkms.Expr{
			Title: "tagged", Expression: condition.expression,
		})).Do()
		require.NoError(t, err)
		held, err := principal.kms.Projects.Locations.KeyRings.CryptoKeys.TestIamPermissions(keyName,
			&cloudkms.TestIamPermissionsRequest{Permissions: asked}).Do()
		require.NoError(t, err)
		if condition.held {
			assert.Equal(t, asked, held.Permissions, "%s holds on a key whose project carries the tag", condition.expression)
		} else {
			assert.Empty(t, held.Permissions, "%s does not hold", condition.expression)
		}
	}

	// Removing the binding takes the tag out of force, and the grant with it.
	_, err = tags.TagBindings.Delete(binding).Do()
	require.NoError(t, err)
	_, err = owner.Projects.Locations.KeyRings.CryptoKeys.SetIamPolicy(keyName, kmsConditionalPolicy(principal.role, principal.member, &cloudkms.Expr{
		Title: "tagged", Expression: `resource.matchTag("123456789012/` + keyShortName + `", "prod")`,
	})).Do()
	require.NoError(t, err)
	held, err := principal.kms.Projects.Locations.KeyRings.CryptoKeys.TestIamPermissions(keyName,
		&cloudkms.TestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Empty(t, held.Permissions, "an unbound tag grants nothing")
}

// API Gateway keeps a binding's condition through setIamPolicy and
// getIamPolicy and evaluates it in testIamPermissions: a gateway supplies no
// resource attributes, so its conditions are on the request.
func TestIAMConditions_APIGatewayKeepsConditions(t *testing.T) {
	t.Parallel()
	owner := apigatewayService(t)
	apiID := uniquename.New("cond-api")
	apiOp, err := owner.Projects.Locations.Apis.Create("projects/test-project/locations/global", &apigateway.ApigatewayApi{DisplayName: "Conditions"}).ApiId(apiID).Do()
	require.NoError(t, err)
	awaitAPIGatewayLRO(t, owner, apiOp)
	apiName := "projects/test-project/locations/global/apis/" + apiID
	configOp, err := owner.Projects.Locations.Apis.Configs.Create(apiName, &apigateway.ApigatewayApiConfig{DisplayName: "v1"}).ApiConfigId("v1").Do()
	require.NoError(t, err)
	awaitAPIGatewayLRO(t, owner, configOp)
	gatewayID := uniquename.New("cond-gw")
	gatewayOp, err := owner.Projects.Locations.Gateways.Create("projects/test-project/locations/us-central1", &apigateway.ApigatewayGateway{
		ApiConfig: apiName + "/configs/v1",
	}).GatewayId(gatewayID).Do()
	require.NoError(t, err)
	awaitAPIGatewayLRO(t, owner, gatewayOp)
	gatewayName := "projects/test-project/locations/us-central1/gateways/" + gatewayID

	principal := newConditionPrincipal(t, "apigateway.gateways.get")
	asked := []string{"apigateway.gateways.get"}
	policy := func(condition *apigateway.ApigatewayExpr) *apigateway.ApigatewaySetIamPolicyRequest {
		return &apigateway.ApigatewaySetIamPolicyRequest{Policy: &apigateway.ApigatewayPolicy{
			Version:  3,
			Bindings: []*apigateway.ApigatewayBinding{{Role: principal.role, Members: []string{principal.member}, Condition: condition}},
		}}
	}

	expired := &apigateway.ApigatewayExpr{Title: "expired", Expression: `request.time < timestamp("2001-01-01T00:00:00Z")`}
	set, err := owner.Projects.Locations.Gateways.SetIamPolicy(gatewayName, policy(expired)).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)
	require.NotNil(t, set.Bindings[0].Condition, "setIamPolicy keeps the condition")
	got, err := owner.Projects.Locations.Gateways.GetIamPolicy(gatewayName).OptionsRequestedPolicyVersion(3).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	require.NotNil(t, got.Bindings[0].Condition, "getIamPolicy returns the condition")
	assert.Equal(t, expired.Expression, got.Bindings[0].Condition.Expression)
	none, err := principal.apigw.Projects.Locations.Gateways.TestIamPermissions(gatewayName,
		&apigateway.ApigatewayTestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Empty(t, none.Permissions, "an expired grant holds nothing")

	_, err = owner.Projects.Locations.Gateways.SetIamPolicy(gatewayName, policy(&apigateway.ApigatewayExpr{
		Title: "current", Expression: `request.time > timestamp("2001-01-01T00:00:00Z")`,
	})).Do()
	require.NoError(t, err)
	held, err := principal.apigw.Projects.Locations.Gateways.TestIamPermissions(gatewayName,
		&apigateway.ApigatewayTestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Equal(t, asked, held.Permissions, "a condition that holds grants the role")

	// A gateway is outside the resource attribute reference, so a condition
	// reading resource.type cannot be satisfied on it.
	_, err = owner.Projects.Locations.Gateways.SetIamPolicy(gatewayName, policy(&apigateway.ApigatewayExpr{
		Title: "typed", Expression: `resource.type == "apigateway.googleapis.com/Gateway"`,
	})).Do()
	require.NoError(t, err)
	none, err = principal.apigw.Projects.Locations.Gateways.TestIamPermissions(gatewayName,
		&apigateway.ApigatewayTestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Empty(t, none.Permissions)
}

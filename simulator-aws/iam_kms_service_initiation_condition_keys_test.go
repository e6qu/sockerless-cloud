package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// serviceInitiatedKMSContext is the condition context a KMS operation settles
// when the named AWS service made the call on the principal's behalf.
func serviceInitiatedKMSContext(service, operation, body string) map[string][]string {
	r := iamStampServiceInitiation(jsonRequest(body), iamServiceSource{Service: service})
	return requestConditionContext("kms", operation, r, body)
}

// TestKMSServiceInitiationKeysAreAbsentOnADirectCall proves a direct client call
// to AWS KMS settles neither kms:ViaService nor kms:GrantIsForAWSResource: real
// AWS sets kms:ViaService only for a call another service makes, so a grant
// conditioned on it must refuse the principal calling KMS itself.
func TestKMSServiceInitiationKeysAreAbsentOnADirectCall(t *testing.T) {
	resetKMSConditionStores()
	for _, operation := range []string{"Decrypt", "Encrypt", "DescribeKey", "CreateGrant", "ListGrants", "RevokeGrant"} {
		t.Run(operation, func(t *testing.T) {
			assertConditionValues(t, jsonConditionContext("kms", operation, `{"KeyId":"k"}`),
				map[string][]string{})
		})
	}
}

// TestKMSViaServiceRefusesADirectCallItGrantsThroughAService proves the key is
// worth populating: one policy, conditioned on kms:ViaService, allows the
// decrypt Amazon S3 performs on the principal's behalf and denies the identical
// decrypt the principal asks for directly.
func TestKMSViaServiceRefusesADirectCallItGrantsThroughAService(t *testing.T) {
	resetKMSConditionStores()
	const keyARN = "arn:aws:kms:us-east-1:123456789012:key/viaservice"
	doc, err := parseIAMPolicy(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
		`"Action":"kms:Decrypt","Resource":"` + keyARN + `",` +
		`"Condition":{"StringEquals":{"kms:ViaService":"s3.us-east-1.amazonaws.com"}}}]}`)
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	docs := []iamPolicyDoc{doc}

	viaS3 := serviceInitiatedKMSContext("s3.amazonaws.com", "Decrypt", `{"KeyId":"`+keyARN+`"}`)
	if decision, _ := iamEvalDecision(docs, "kms:Decrypt", keyARN, viaS3); decision != "allowed" {
		t.Errorf("a decrypt Amazon S3 performs on the principal's behalf = %q, want allowed", decision)
	}

	direct := jsonConditionContext("kms", "Decrypt", `{"KeyId":"`+keyARN+`"}`)
	if decision, _ := iamEvalDecision(docs, "kms:Decrypt", keyARN, direct); decision == "allowed" {
		t.Error("a direct client decrypt must not be allowed by a kms:ViaService grant")
	}

	viaFirehose := serviceInitiatedKMSContext("firehose.amazonaws.com", "Decrypt", `{"KeyId":"`+keyARN+`"}`)
	if decision, _ := iamEvalDecision(docs, "kms:Decrypt", keyARN, viaFirehose); decision == "allowed" {
		t.Error("a decrypt through another service must not be allowed by an Amazon S3 kms:ViaService grant")
	}
}

// TestKMSViaServiceIsTheCallingServicesRegionalEndpoint proves the value's
// shape: the calling service's name, the simulator's region, amazonaws.com.
func TestKMSViaServiceIsTheCallingServicesRegionalEndpoint(t *testing.T) {
	resetKMSConditionStores()
	t.Setenv("SOCKERLESS_AWS_REGION", "eu-west-1")
	assertConditionValues(t, serviceInitiatedKMSContext("s3.amazonaws.com", "GenerateDataKey", `{"KeyId":"k"}`),
		map[string][]string{"kms:ViaService": {"s3.eu-west-1.amazonaws.com"}})
	assertConditionValues(t, serviceInitiatedKMSContext("secretsmanager.amazonaws.com", "Encrypt", `{"KeyId":"k"}`),
		map[string][]string{"kms:ViaService": {"secretsmanager.eu-west-1.amazonaws.com"}})
	// An action the vendored service reference does not declare the key on does
	// not carry it, however the call was made.
	assertConditionValues(t, serviceInitiatedKMSContext("s3.amazonaws.com", "ListKeys", `{}`),
		map[string][]string{})
	assertConditionValues(t, serviceInitiatedKMSContext("s3.amazonaws.com", "ListAliases", `{}`),
		map[string][]string{})
	// A service principal that is not one label under amazonaws.com has no
	// endpoint form, so nothing is invented for it.
	assertConditionValues(t, serviceInitiatedKMSContext("streams.metrics.cloudwatch.amazonaws.com", "Decrypt", `{"KeyId":"k"}`),
		map[string][]string{})
}

// TestKMSGrantIsForAWSResourceOnAServiceInitiatedGrant proves the grant keys:
// a grant request an AWS service makes on the principal's behalf is for an AWS
// resource, and the key is absent from every other action.
func TestKMSGrantIsForAWSResourceOnAServiceInitiatedGrant(t *testing.T) {
	resetKMSConditionStores()
	for _, operation := range []string{"CreateGrant", "ListGrants", "RevokeGrant"} {
		t.Run(operation, func(t *testing.T) {
			assertConditionValues(t, serviceInitiatedKMSContext("rds.amazonaws.com", operation, `{"KeyId":"k"}`),
				map[string][]string{
					"kms:GrantIsForAWSResource": {"true"},
					"kms:ViaService":            {"rds.us-east-1.amazonaws.com"},
				})
		})
	}
	// RetireGrant is a grant action the reference does not declare the key on.
	assertConditionValues(t, serviceInitiatedKMSContext("rds.amazonaws.com", "RetireGrant", `{"KeyId":"k"}`),
		map[string][]string{"kms:ViaService": {"rds.us-east-1.amazonaws.com"}})
	assertConditionValues(t, serviceInitiatedKMSContext("rds.amazonaws.com", "Decrypt", `{"KeyId":"k"}`),
		map[string][]string{"kms:ViaService": {"rds.us-east-1.amazonaws.com"}})
	// The grant members a CreateGrant request settles are unchanged by it.
	assertConditionValues(t, serviceInitiatedKMSContext("rds.amazonaws.com", "CreateGrant",
		`{"KeyId":"k","GranteePrincipal":"arn:aws:iam::123456789012:role/rds"}`),
		map[string][]string{
			"kms:GrantIsForAWSResource": {"true"},
			"kms:ViaService":            {"rds.us-east-1.amazonaws.com"},
			"kms:GranteePrincipal":      {"arn:aws:iam::123456789012:role/rds"},
		})
}

// TestIAMServiceRoleKMSPermissionCarriesViaService proves the internal call path
// that made these keys worth having: Amazon Data Firehose encrypting a buffered
// record under a customer-managed CMK is authorized as Firehose calling KMS on
// the principal's behalf, so the delivery role may hold a KMS permission scoped
// to use through Firehose and nothing else.
func TestIAMServiceRoleKMSPermissionCarriesViaService(t *testing.T) {
	AwaitSimulatorBackground()
	iamRoles = sim.MakeStore[IAMRole](nil, "iam_roles")
	iamRolePolicies = sim.MakeStore[IAMRolePolicy](nil, "iam_role_policies")
	iamAttachedPolicies = sim.MakeStore[IAMAttachedPolicy](nil, "iam_attached_policies")
	iamPolicies = sim.MakeStore[IAMPolicy](nil, "iam_policies")

	const roleARN = "arn:aws:iam::123456789012:role/firehose-delivery"
	const keyARN = "arn:aws:kms:us-east-1:123456789012:key/stream"
	iamRoles.Put("firehose-delivery", IAMRole{
		RoleName: "firehose-delivery",
		Arn:      roleARN,
		AssumeRolePolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"firehose.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
	})
	stream := FirehoseDeliveryStream{
		S3:         FirehoseS3Destination{RoleARN: roleARN},
		Encryption: FirehoseEncryption{KeyType: "CUSTOMER_MANAGED_CMK", KeyARN: keyARN, Status: "ENABLED"},
	}

	scoped := func(endpoint string) {
		iamRolePolicies.Put("firehose-delivery/kms", IAMRolePolicy{
			RoleName:   "firehose-delivery",
			PolicyName: "kms",
			PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
				`"Action":["kms:GenerateDataKey","kms:Decrypt"],"Resource":"` + keyARN + `",` +
				`"Condition":{"StringEquals":{"kms:ViaService":"` + endpoint + `"}}}]}`,
		})
	}

	scoped("firehose.us-east-1.amazonaws.com")
	for _, action := range []string{"kms:GenerateDataKey", "kms:Decrypt"} {
		if err := firehoseAuthorizeCustomerKeyUse(stream, action); err != nil {
			t.Errorf("%s through Amazon Data Firehose: %v", action, err)
		}
	}

	// The same permission scoped to another service's endpoint does not admit
	// the call Firehose makes.
	scoped("s3.us-east-1.amazonaws.com")
	if err := firehoseAuthorizeCustomerKeyUse(stream, "kms:GenerateDataKey"); err == nil {
		t.Error("a kms:ViaService grant for Amazon S3 must not admit Amazon Data Firehose")
	}

	// A stream on the AWS-owned key calls no customer key, so it needs no grant.
	iamRolePolicies.Delete("firehose-delivery/kms")
	owned := FirehoseDeliveryStream{
		S3:         FirehoseS3Destination{RoleARN: roleARN},
		Encryption: FirehoseEncryption{KeyType: "AWS_OWNED_CMK", Status: "ENABLED"},
	}
	if err := firehoseAuthorizeCustomerKeyUse(owned, "kms:GenerateDataKey"); err != nil {
		t.Errorf("an AWS-owned-key stream must need no customer-key grant: %v", err)
	}
	// With the grant gone, the customer-managed key is refused.
	if err := firehoseAuthorizeCustomerKeyUse(stream, "kms:GenerateDataKey"); err == nil {
		t.Error("a role with no KMS permission must not use the customer-managed key")
	}
}

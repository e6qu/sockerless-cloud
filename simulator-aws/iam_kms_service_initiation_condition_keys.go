package main

import (
	"net/http"
	"strings"
)

// The two AWS KMS condition keys that describe who reached KMS rather than what
// the request asked for: kms:ViaService and kms:GrantIsForAWSResource.
//
// Both are settled by the origin of the call, not by a request member, so they
// live here rather than in iam_kms_request_condition_keys.go. Neither is set for
// a direct client call to KMS — real AWS omits kms:ViaService entirely when the
// principal calls KMS itself, which is what makes a key grant conditioned on it
// refuse a direct call — and both are set when an AWS service calls KMS on the
// principal's behalf (iamStampServiceInitiation marks such a call).
//
// The action lists come from the vendored AWS Service Reference
// (specs/cloud-api/aws/service-reference/kms.servicereference.json.gz,
// Actions[].ActionConditionKeys), not from memory: a key an action does not
// declare stays unset on that action.

func init() {
	registerIAMRequestConditionPopulator("kms", iamPopulateKMSServiceInitiationConditionKeys)
}

// kmsViaServiceActions are the 45 AWS KMS actions the service reference
// declares kms:ViaService on. ReEncryptFrom and ReEncryptTo are the two
// authorization actions a ReEncrypt request settles, which is how the reference
// and this simulator both name them.
var kmsViaServiceActions = map[string]bool{
	"CancelKeyDeletion":                   true,
	"CreateAlias":                         true,
	"CreateGrant":                         true,
	"CreateKey":                           true,
	"Decrypt":                             true,
	"DeleteAlias":                         true,
	"DeleteImportedKeyMaterial":           true,
	"DeriveSharedSecret":                  true,
	"DescribeKey":                         true,
	"DisableKey":                          true,
	"DisableKeyRotation":                  true,
	"EnableKey":                           true,
	"EnableKeyRotation":                   true,
	"Encrypt":                             true,
	"GenerateDataKey":                     true,
	"GenerateDataKeyPair":                 true,
	"GenerateDataKeyPairWithoutPlaintext": true,
	"GenerateDataKeyWithoutPlaintext":     true,
	"GenerateMac":                         true,
	"GetKeyLastUsage":                     true,
	"GetKeyPolicy":                        true,
	"GetKeyRotationStatus":                true,
	"GetParametersForImport":              true,
	"GetPublicKey":                        true,
	"ImportKeyMaterial":                   true,
	"ListGrants":                          true,
	"ListKeyPolicies":                     true,
	"ListKeyRotations":                    true,
	"ListResourceTags":                    true,
	"PutKeyPolicy":                        true,
	"ReEncryptFrom":                       true,
	"ReEncryptTo":                         true,
	"ReplicateKey":                        true,
	"RetireGrant":                         true,
	"RevokeGrant":                         true,
	"RotateKeyOnDemand":                   true,
	"ScheduleKeyDeletion":                 true,
	"Sign":                                true,
	"TagResource":                         true,
	"UntagResource":                       true,
	"UpdateAlias":                         true,
	"UpdateKeyDescription":                true,
	"UpdatePrimaryRegion":                 true,
	"Verify":                              true,
	"VerifyMac":                           true,
}

// kmsGrantIsForAWSResourceActions are the three grant actions the service
// reference declares kms:GrantIsForAWSResource on.
var kmsGrantIsForAWSResourceActions = map[string]bool{
	"CreateGrant": true,
	"ListGrants":  true,
	"RevokeGrant": true,
}

// iamPopulateKMSServiceInitiationConditionKeys adds the keys that say an AWS
// service reached KMS on the principal's behalf. A direct client call settles
// neither, and an action the reference does not declare a key on does not carry
// it.
func iamPopulateKMSServiceInitiationConditionKeys(r *http.Request, operation string, _ []byte, ctx map[string][]string) {
	src := iamServiceInitiation(r)
	if src == nil {
		return
	}
	if endpoint, known := kmsViaServiceEndpoint(src.Service); known && kmsViaServiceActions[operation] {
		ctx["kms:ViaService"] = []string{endpoint}
	}
	if kmsGrantIsForAWSResourceActions[operation] {
		ctx["kms:GrantIsForAWSResource"] = []string{"true"}
	}
}

// kmsViaServiceEndpoint turns the calling service's principal
// ("s3.amazonaws.com") into the regional endpoint form kms:ViaService carries
// ("s3.<region>.amazonaws.com"). A principal that is not one service label
// under amazonaws.com — "streams.metrics.cloudwatch.amazonaws.com" — has no
// such endpoint form, so it settles nothing rather than a value invented by
// splicing the region into the middle of it.
func kmsViaServiceEndpoint(principal string) (string, bool) {
	const domain = ".amazonaws.com"
	name, ok := strings.CutSuffix(principal, domain)
	if !ok || name == "" || strings.Contains(name, ".") {
		return "", false
	}
	return name + "." + awsRegion() + domain, true
}

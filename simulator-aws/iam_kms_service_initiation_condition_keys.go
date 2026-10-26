package main

import (
	"net/http"
	"strings"
)

// kms:ViaService and kms:GrantIsForAWSResource describe who reached AWS KMS, not
// what the request asked. AWS omits both on a direct client call, which is what
// makes a grant conditioned on kms:ViaService refuse one, and sets both when an
// AWS service calls KMS on the principal's behalf (iamStampServiceInitiation
// marks such a call). The action lists come from the vendored AWS Service
// Reference; a key an action does not declare stays unset on it.

func init() {
	registerIAMRequestConditionPopulator("kms", iamPopulateKMSServiceInitiationConditionKeys)
}

// kmsViaServiceActions are the 45 actions the service reference declares
// kms:ViaService on; ReEncryptFrom and ReEncryptTo are the two authorizations a
// ReEncrypt settles.
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

// kmsViaServiceEndpoint turns a service principal ("s3.amazonaws.com") into the
// regional endpoint kms:ViaService carries ("s3.<region>.amazonaws.com"). A
// principal that is not one label under amazonaws.com
// ("streams.metrics.cloudwatch.amazonaws.com") has no such form and settles
// nothing.
func kmsViaServiceEndpoint(principal string) (string, bool) {
	const domain = ".amazonaws.com"
	name, ok := strings.CutSuffix(principal, domain)
	if !ok || name == "" || strings.Contains(name, ".") {
		return "", false
	}
	return name + "." + awsRegion() + domain, true
}

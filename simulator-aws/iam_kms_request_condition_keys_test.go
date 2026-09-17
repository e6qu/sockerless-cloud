package main

import (
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// requestConditionContext runs the populators a service registered for one
// operation and returns the keys they added.
func requestConditionContext(service, operation string, r *http.Request, body string) map[string][]string {
	ctx := map[string][]string{}
	iamRunRequestConditionPopulators(r, service, operation, []byte(body), ctx)
	return ctx
}

func jsonConditionContext(service, operation, body string) map[string][]string {
	return requestConditionContext(service, operation, jsonRequest(body), body)
}

// assertConditionValues compares each wanted key as a set, and fails on any
// key the context carries that the test did not name.
func assertConditionValues(t *testing.T, ctx map[string][]string, want map[string][]string) {
	t.Helper()
	for key, values := range want {
		got := append([]string(nil), ctx[key]...)
		sort.Strings(got)
		expected := append([]string(nil), values...)
		sort.Strings(expected)
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("%s = %v, want %v", key, got, expected)
		}
	}
	for key, values := range ctx {
		if _, named := want[key]; !named {
			t.Errorf("unexpected %s = %v", key, values)
		}
	}
}

func resetKMSConditionStores() {
	AwaitSimulatorBackground()
	kmsKeys = sim.MakeStore[KMSKey](nil, "kms_keys")
	kmsAliases = sim.MakeStore[string](nil, "kms_aliases")
	kmsGrants = sim.MakeStore[KMSGrant](nil, "kms_grants")
}

func TestKMSRequestConditionKeysDescribeTheCreatedKey(t *testing.T) {
	assertConditionValues(t, jsonConditionContext("kms", "CreateKey",
		`{"KeySpec":"ECC_NIST_P256","KeyUsage":"SIGN_VERIFY","Origin":"EXTERNAL","MultiRegion":true,"BypassPolicyLockoutSafetyCheck":false}`),
		map[string][]string{
			"kms:KeySpec":                        {"ECC_NIST_P256"},
			"kms:KeyUsage":                       {"SIGN_VERIFY"},
			"kms:KeyOrigin":                      {"EXTERNAL"},
			"kms:MultiRegion":                    {"true"},
			"kms:MultiRegionKeyType":             {"PRIMARY"},
			"kms:BypassPolicyLockoutSafetyCheck": {"false"},
		})
	// An empty CreateKey makes a single-Region symmetric encryption key.
	assertConditionValues(t, jsonConditionContext("kms", "CreateKey", `{}`), map[string][]string{
		"kms:KeySpec":     {"SYMMETRIC_DEFAULT"},
		"kms:KeyUsage":    {"ENCRYPT_DECRYPT"},
		"kms:KeyOrigin":   {"AWS_KMS"},
		"kms:MultiRegion": {"false"},
	})
	assertConditionValues(t, jsonConditionContext("kms", "CreateKey", `{"CustomerMasterKeySpec":"RSA_2048"}`),
		map[string][]string{
			"kms:KeySpec":     {"RSA_2048"},
			"kms:KeyUsage":    {"ENCRYPT_DECRYPT"},
			"kms:KeyOrigin":   {"AWS_KMS"},
			"kms:MultiRegion": {"false"},
		})
}

func TestKMSRequestConditionKeysReadTheRequestMembers(t *testing.T) {
	cases := []struct {
		operation, body string
		want            map[string][]string
	}{
		{"PutKeyPolicy", `{"KeyId":"k","Policy":"{}","BypassPolicyLockoutSafetyCheck":true}`,
			map[string][]string{"kms:BypassPolicyLockoutSafetyCheck": {"true"}}},
		{"GenerateDataKeyPair", `{"KeyId":"k","KeyPairSpec":"RSA_4096"}`,
			map[string][]string{"kms:DataKeyPairSpec": {"RSA_4096"}}},
		{"GenerateDataKeyPairWithoutPlaintext", `{"KeyId":"k","KeyPairSpec":"ECC_NIST_P384"}`,
			map[string][]string{"kms:DataKeyPairSpec": {"ECC_NIST_P384"}}},
		{"ImportKeyMaterial", `{"KeyId":"k","ExpirationModel":"KEY_MATERIAL_EXPIRES","ValidTo":1893456000}`,
			map[string][]string{"kms:ExpirationModel": {"KEY_MATERIAL_EXPIRES"}, "kms:ValidTo": {"2030-01-01T00:00:00Z"}}},
		{"DeriveSharedSecret", `{"KeyId":"k","KeyAgreementAlgorithm":"ECDH"}`,
			map[string][]string{"kms:KeyAgreementAlgorithm": {"ECDH"}}},
		{"GenerateMac", `{"KeyId":"k","MacAlgorithm":"HMAC_SHA_256"}`,
			map[string][]string{"kms:MacAlgorithm": {"HMAC_SHA_256"}}},
		{"VerifyMac", `{"KeyId":"k","MacAlgorithm":"HMAC_SHA_512"}`,
			map[string][]string{"kms:MacAlgorithm": {"HMAC_SHA_512"}}},
		{"Sign", `{"KeyId":"k","MessageType":"DIGEST","SigningAlgorithm":"ECDSA_SHA_256"}`,
			map[string][]string{"kms:MessageType": {"DIGEST"}, "kms:SigningAlgorithm": {"ECDSA_SHA_256"}}},
		{"Verify", `{"KeyId":"k","SigningAlgorithm":"RSASSA_PSS_SHA_256"}`,
			map[string][]string{"kms:SigningAlgorithm": {"RSASSA_PSS_SHA_256"}}},
		{"UpdatePrimaryRegion", `{"KeyId":"k","PrimaryRegion":"eu-west-1"}`,
			map[string][]string{"kms:PrimaryRegion": {"eu-west-1"}}},
		{"ReplicateKey", `{"KeyId":"k","ReplicaRegion":"us-west-2"}`,
			map[string][]string{"kms:ReplicaRegion": {"us-west-2"}}},
		{"EnableKeyRotation", `{"KeyId":"k","RotationPeriodInDays":90}`,
			map[string][]string{"kms:RotationPeriodInDays": {"90"}}},
		{"ScheduleKeyDeletion", `{"KeyId":"k","PendingWindowInDays":7}`,
			map[string][]string{"kms:ScheduleKeyDeletionPendingWindowInDays": {"7"}}},
		{"GetParametersForImport", `{"KeyId":"k","WrappingAlgorithm":"RSA_AES_KEY_WRAP_SHA_256","WrappingKeySpec":"RSA_4096"}`,
			map[string][]string{"kms:WrappingAlgorithm": {"RSA_AES_KEY_WRAP_SHA_256"}, "kms:WrappingKeySpec": {"RSA_4096"}}},
		// A member one action declares never leaks into another's context.
		{"Encrypt", `{"KeyId":"k","KeyPairSpec":"RSA_4096","MacAlgorithm":"HMAC_SHA_256"}`,
			map[string][]string{}},
		{"Sign", `{"KeyId":"k"}`, map[string][]string{}},
	}
	resetKMSConditionStores()
	for _, c := range cases {
		t.Run(c.operation, func(t *testing.T) {
			assertConditionValues(t, jsonConditionContext("kms", c.operation, c.body), c.want)
		})
	}
}

func TestKMSRequestConditionKeysReadTheGrant(t *testing.T) {
	resetKMSConditionStores()
	assertConditionValues(t, jsonConditionContext("kms", "CreateGrant", `{
		"KeyId": "k",
		"GranteePrincipal": "arn:aws:iam::123456789012:role/app",
		"RetiringPrincipal": "arn:aws:iam::123456789012:role/ops",
		"GranteeServicePrincipal": "rds.amazonaws.com",
		"RetiringServicePrincipal": "ec2.amazonaws.com",
		"Operations": ["Decrypt", "GenerateDataKey"],
		"Constraints": {
			"EncryptionContextSubset": {"Department": "IT", "Project": "Alpha"},
			"SourceArn": "arn:aws:rds:us-east-1:123456789012:db:orders"
		}
	}`), map[string][]string{
		"kms:GranteePrincipal":             {"arn:aws:iam::123456789012:role/app"},
		"kms:RetiringPrincipal":            {"arn:aws:iam::123456789012:role/ops"},
		"kms:GranteeServicePrincipal":      {"rds.amazonaws.com"},
		"kms:RetiringServicePrincipal":     {"ec2.amazonaws.com"},
		"kms:GrantOperations":              {"Decrypt", "GenerateDataKey"},
		"kms:GrantConstraintSourceArn":     {"arn:aws:rds:us-east-1:123456789012:db:orders"},
		"kms:GrantConstraintType":          {"EncryptionContextSubset"},
		"kms:EncryptionContext:Department": {"IT"},
		"kms:EncryptionContext:Project":    {"Alpha"},
		"kms:EncryptionContextKeys":        {"Department", "Project"},
	})

	kmsGrants.Put("g-1", KMSGrant{GrantId: "g-1", KeyId: "k", Operations: []string{"Decrypt"},
		Constraints: map[string]any{"EncryptionContextEquals": map[string]any{"Stage": "prod"}}})
	assertConditionValues(t, jsonConditionContext("kms", "RetireGrant", `{"KeyId":"k","GrantId":"g-1"}`),
		map[string][]string{
			"kms:GrantConstraintType":     {"EncryptionContextEquals"},
			"kms:EncryptionContext:Stage": {"prod"},
			"kms:EncryptionContextKeys":   {"Stage"},
		})
	assertConditionValues(t, jsonConditionContext("kms", "RetireGrant", `{"KeyId":"k","GrantId":"g-missing"}`),
		map[string][]string{})
}

func TestKMSRequestConditionKeysReadTheKeysUsageAndTheReEncryptTarget(t *testing.T) {
	resetKMSConditionStores()
	used := time.Now().Add(-50 * time.Hour)
	kmsKeys.Put("used", KMSKey{KeyId: "used", Arn: kmsKeyArn("used"),
		LastUsedOperation: "Decrypt", LastUsedDate: float64(used.Unix())})
	kmsKeys.Put("fresh", KMSKey{KeyId: "fresh", Arn: kmsKeyArn("fresh")})
	kmsAliases.Put("alias/used", "used")

	assertConditionValues(t, jsonConditionContext("kms", "DisableKey", `{"KeyId":"alias/used"}`),
		map[string][]string{"kms:TrailingDaysWithoutKeyUsage": {"2"}})
	assertConditionValues(t, jsonConditionContext("kms", "ScheduleKeyDeletion", `{"KeyId":"`+kmsKeyArn("used")+`"}`),
		map[string][]string{"kms:TrailingDaysWithoutKeyUsage": {"2"}})
	assertConditionValues(t, jsonConditionContext("kms", "DisableKey", `{"KeyId":"fresh"}`),
		map[string][]string{})

	blob := []byte(kmsBlobMagic)
	blob = append(blob, kmsBlobVersion)
	blob = binary.BigEndian.AppendUint16(blob, uint16(len("used")))
	blob = append(blob, "used"...)
	blob = append(blob, make([]byte, 28)...)
	ciphertext := strconv.Quote(base64.StdEncoding.EncodeToString(blob))

	for _, c := range []struct {
		name, body, want string
	}{
		{"ciphertext names the destination", `{"CiphertextBlob":` + ciphertext + `,"DestinationKeyId":"alias/used"}`, "true"},
		{"ciphertext names another key", `{"CiphertextBlob":` + ciphertext + `,"DestinationKeyId":"fresh"}`, "false"},
		{"source key named", `{"CiphertextBlob":` + ciphertext + `,"SourceKeyId":"fresh","DestinationKeyId":"fresh"}`, "true"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertConditionValues(t, jsonConditionContext("kms", "ReEncryptFrom", c.body),
				map[string][]string{"kms:ReEncryptOnSameKey": {c.want}})
		})
	}
	assertConditionValues(t, jsonConditionContext("kms", "ReEncryptFrom",
		`{"CiphertextBlob":`+ciphertext+`,"DestinationKeyId":"missing"}`), map[string][]string{})

	// Each side of a ReEncrypt carries its own algorithm, context and alias.
	assertConditionValues(t, jsonConditionContext("kms", "ReEncryptTo",
		`{"CiphertextBlob":`+ciphertext+`,"DestinationKeyId":"alias/used","DestinationEncryptionAlgorithm":"RSAES_OAEP_SHA_256",
		  "DestinationEncryptionContext":{"Stage":"prod"},"SourceEncryptionContext":{"Stage":"test"}}`),
		map[string][]string{
			"kms:ReEncryptOnSameKey":      {"true"},
			"kms:RequestAlias":            {"alias/used"},
			"kms:EncryptionAlgorithm":     {"RSAES_OAEP_SHA_256"},
			"kms:EncryptionContext:Stage": {"prod"},
			"kms:EncryptionContextKeys":   {"Stage"},
		})
}

// AWS KMS authorizes a ReEncrypt twice: ReEncryptFrom on the key that protects
// the ciphertext, ReEncryptTo on the key it moves to.
func TestKMSReEncryptIsAuthorizedOnBothKeys(t *testing.T) {
	resetKMSConditionStores()
	kmsKeys.Put("from", KMSKey{KeyId: "from", Arn: kmsKeyArn("from")})
	kmsKeys.Put("to", KMSKey{KeyId: "to", Arn: kmsKeyArn("to")})
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"CiphertextBlob":"AAAA","SourceKeyId":"from","DestinationKeyId":"to"}`))
	r.Header.Set("X-Amz-Target", "TrentService.ReEncrypt")
	got := iamAuthorizationTargets(r, "kms:ReEncrypt")
	want := []iamAuthorizationTarget{
		{action: "kms:ReEncryptFrom", resource: kmsKeyArn("from")},
		{action: "kms:ReEncryptTo", resource: kmsKeyArn("to")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %+v, want %+v", got, want)
	}
}

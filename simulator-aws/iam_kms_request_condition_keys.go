package main

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func init() {
	registerIAMRequestConditionPopulator("kms", iamPopulateKMSRequestConditionKeys)
}

func iamPopulateKMSRequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	request := iamParseBodyMembers(body)
	if request == nil {
		return
	}
	set := request.setters(ctx)
	switch operation {
	case "CreateKey":
		set.boolean("kms:BypassPolicyLockoutSafetyCheck", "BypassPolicyLockoutSafetyCheck")
		iamPopulateKMSCreatedKeyProperties(request, ctx)
	case "PutKeyPolicy":
		set.boolean("kms:BypassPolicyLockoutSafetyCheck", "BypassPolicyLockoutSafetyCheck")
	case "GenerateDataKeyPair", "GenerateDataKeyPairWithoutPlaintext":
		set.str("kms:DataKeyPairSpec", "KeyPairSpec")
	case "ImportKeyMaterial":
		set.str("kms:ExpirationModel", "ExpirationModel")
		set.timestamp("kms:ValidTo", "ValidTo")
	case "CreateGrant":
		set.str("kms:GranteePrincipal", "GranteePrincipal")
		set.str("kms:RetiringPrincipal", "RetiringPrincipal")
		set.str("kms:GranteeServicePrincipal", "GranteeServicePrincipal")
		set.str("kms:RetiringServicePrincipal", "RetiringServicePrincipal")
		set.strings("kms:GrantOperations", "Operations")
		constraints := request.object("Constraints")
		constraints.setters(ctx).str("kms:GrantConstraintSourceArn", "SourceArn")
		var decoded map[string]any
		if raw, ok := request["Constraints"]; ok && json.Unmarshal(raw, &decoded) == nil {
			iamPopulateKMSGrantConstraints(decoded, ctx)
		}
	case "RetireGrant":
		if id, ok := request.str("GrantId"); ok {
			if grant, found := kmsGrants.Get(id); found {
				iamPopulateKMSGrantConstraints(grant.Constraints, ctx)
			}
		}
	case "DeriveSharedSecret":
		set.str("kms:KeyAgreementAlgorithm", "KeyAgreementAlgorithm")
	case "GenerateMac", "VerifyMac":
		set.str("kms:MacAlgorithm", "MacAlgorithm")
	case "Sign", "Verify":
		set.str("kms:MessageType", "MessageType")
		set.str("kms:SigningAlgorithm", "SigningAlgorithm")
	case "UpdatePrimaryRegion":
		set.str("kms:PrimaryRegion", "PrimaryRegion")
	case "ReplicateKey":
		set.str("kms:ReplicaRegion", "ReplicaRegion")
	case "EnableKeyRotation":
		set.number("kms:RotationPeriodInDays", "RotationPeriodInDays")
	case "ScheduleKeyDeletion":
		set.number("kms:ScheduleKeyDeletionPendingWindowInDays", "PendingWindowInDays")
		iamPopulateKMSTrailingDaysWithoutUsage(request, ctx)
	case "DisableKey":
		iamPopulateKMSTrailingDaysWithoutUsage(request, ctx)
	case "GetParametersForImport":
		set.str("kms:WrappingAlgorithm", "WrappingAlgorithm")
		set.str("kms:WrappingKeySpec", "WrappingKeySpec")
	case "ReEncryptFrom":
		iamPopulateKMSReEncryptDirection(request, "Source", ctx)
	case "ReEncryptTo":
		iamPopulateKMSReEncryptDirection(request, "Destination", ctx)
	}
}

// iamPopulateKMSReEncryptDirection adds the keys one side of a ReEncrypt
// settles: the algorithm, encryption context and alias the request gives that
// side, and whether both sides are one key.
func iamPopulateKMSReEncryptDirection(request iamBodyMembers, side string, ctx map[string][]string) {
	set := request.setters(ctx)
	set.str("kms:EncryptionAlgorithm", side+"EncryptionAlgorithm")
	if ref, ok := request.str(side + "KeyId"); ok && strings.HasPrefix(ref, "alias/") {
		ctx["kms:RequestAlias"] = []string{ref}
	}
	var encryptionContext map[string]string
	if raw, ok := request[side+"EncryptionContext"]; ok && json.Unmarshal(raw, &encryptionContext) == nil && len(encryptionContext) > 0 {
		names := make([]string, 0, len(encryptionContext))
		for name, value := range encryptionContext {
			ctx["kms:EncryptionContext:"+name] = []string{value}
			names = append(names, name)
		}
		sort.Strings(names)
		ctx["kms:EncryptionContextKeys"] = names
	}
	body, err := json.Marshal(request)
	if err != nil {
		return
	}
	if source, destination := iamKMSReEncryptKeys(body); source != "" && destination != "" {
		ctx["kms:ReEncryptOnSameKey"] = []string{strconv.FormatBool(source == destination)}
	}
}

// iamPopulateKMSCreatedKeyProperties adds the properties of the key a CreateKey
// request makes, with the defaults AWS KMS gives a member the request omits.
func iamPopulateKMSCreatedKeyProperties(request iamBodyMembers, ctx map[string][]string) {
	spec, ok := request.str("KeySpec")
	if !ok {
		spec, ok = request.str("CustomerMasterKeySpec")
	}
	if !ok {
		spec = "SYMMETRIC_DEFAULT"
	}
	usage, ok := request.str("KeyUsage")
	if !ok {
		usage = "ENCRYPT_DECRYPT"
	}
	origin, ok := request.str("Origin")
	if !ok {
		origin = "AWS_KMS"
	}
	multiRegion, ok := request.boolean("MultiRegion")
	if !ok {
		multiRegion = "false"
	}
	ctx["kms:KeySpec"] = []string{spec}
	ctx["kms:KeyUsage"] = []string{usage}
	ctx["kms:KeyOrigin"] = []string{origin}
	ctx["kms:MultiRegion"] = []string{multiRegion}
	// CreateKey makes only primary keys; ReplicateKey makes the replicas.
	if multiRegion == "true" {
		ctx["kms:MultiRegionKeyType"] = []string{"PRIMARY"}
	}
}

// iamPopulateKMSGrantConstraints adds what a grant's constraints say: which
// kind of encryption-context constraint it carries, and the context pairs
// that constraint names.
func iamPopulateKMSGrantConstraints(constraints map[string]any, ctx map[string][]string) {
	var kinds, names []string
	for _, kind := range []string{"EncryptionContextEquals", "EncryptionContextSubset"} {
		pairs, ok := constraints[kind].(map[string]any)
		if !ok {
			continue
		}
		kinds = append(kinds, kind)
		for name, value := range pairs {
			if text, ok := value.(string); ok {
				ctx["kms:EncryptionContext:"+name] = []string{text}
				names = append(names, name)
			}
		}
	}
	if len(kinds) > 0 {
		ctx["kms:GrantConstraintType"] = kinds
	}
	if len(names) > 0 {
		sort.Strings(names)
		ctx["kms:EncryptionContextKeys"] = names
	}
}

// iamPopulateKMSTrailingDaysWithoutUsage adds the whole days since the key the
// request names last performed a cryptographic operation. A key with no
// recorded use settles no value.
func iamPopulateKMSTrailingDaysWithoutUsage(request iamBodyMembers, ctx map[string][]string) {
	ref, ok := request.str("KeyId")
	if !ok {
		return
	}
	id, ok := resolveKMSKey(ref)
	if !ok {
		return
	}
	key, ok := kmsKeys.Get(id)
	if !ok || key.LastUsedOperation == "" {
		return
	}
	idle := time.Since(time.Unix(int64(key.LastUsedDate), 0))
	if idle < 0 {
		idle = 0
	}
	ctx["kms:TrailingDaysWithoutKeyUsage"] = []string{strconv.FormatInt(int64(idle/(24*time.Hour)), 10)}
}

// iamKMSReEncryptKeys resolves the two keys of a ReEncrypt request. A request
// that names no source key is about the key recorded in the ciphertext's
// header. An unresolvable side is "".
func iamKMSReEncryptKeys(body []byte) (source, destination string) {
	request := iamParseBodyMembers(body)
	if request == nil {
		return "", ""
	}
	if ref, ok := request.str("DestinationKeyId"); ok {
		destination, _ = resolveKMSKey(ref)
	}
	if ref, named := request.str("SourceKeyId"); named {
		source, _ = resolveKMSKey(ref)
		return source, destination
	}
	var blob []byte
	if raw, present := request["CiphertextBlob"]; present && json.Unmarshal(raw, &blob) == nil {
		if id, ok := kmsCiphertextKeyID(blob); ok {
			source, _ = resolveKMSKey(id)
		}
	}
	return source, destination
}

// kmsCiphertextKeyID reads the key id from the header kmsEncryptBytes writes,
// without decrypting the payload.
func kmsCiphertextKeyID(blob []byte) (string, bool) {
	const header = 3 + 1 + 2
	if len(blob) < header || string(blob[:3]) != kmsBlobMagic || blob[3] != kmsBlobVersion {
		return "", false
	}
	end := header + int(binary.BigEndian.Uint16(blob[4:6]))
	if len(blob) < end {
		return "", false
	}
	return string(blob[header:end]), true
}

package main

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	kmspb "cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Cloud KMS v1 gRPC data plane. This slice mounts the
// google.cloud.kms.v1.KeyManagementService service on the shared gRPC server so
// the high-level cloud.google.com/go/kms.KeyManagementClient (which is gRPC-only
// — it has no REST transport) can target the simulator. The REST slice
// (cloudkms.go) already serves the low-level REST clients; this gRPC slice is a
// transport-only bridge on top of the same stores and the same real Go-stdlib
// cryptography.
//
// Every data-plane RPC does genuine cryptographic work by calling the same
// helpers the REST slice uses (kmsEncryptBytes / kmsDecryptBytes,
// kmsPrivateFromPEM, kmsHashForAlg, kmsCreateVersionForAlg, etc.). No crypto is
// invented or stubbed here: a Sign produces a real RSA/ECDSA signature that a
// Verify confirms against the public key; an Encrypt produces real AES-GCM
// ciphertext that Decrypt reverses; MacSign/MacVerify use real HMAC; RawEncrypt
// / RawDecrypt use real AES-GCM with a caller-supplied IV; GenerateRandomBytes
// reads from crypto/rand. The admin RPCs read and write the kmsKeyRings /
// kmsCryptoKeys / kmsCryptoKeyVersions stores the REST slice owns.
//
// Name normalization: the gRPC client sends full resource paths in every
// request field (projects/{p}/locations/{loc}/...). The shared stores are keyed
// by those full paths, so the names are used verbatim — the helper
// kmsNormalizeName only strips an accidental leading "projects/" doubling the
// REST handler produced, never invents prefixes.

// cloudKmsGRPC implements the KeyManagementService gRPC service on the shared
// REST stores and crypto helpers.
//
// Implemented RPCs cover the full admin CRUD the high-level client drives
// (KeyRings / CryptoKeys / CryptoKeyVersions lifecycle + GetPublicKey +
// UpdateCryptoKeyPrimaryVersion) and every data-plane crypto op (Encrypt /
// Decrypt / AsymmetricSign / AsymmetricDecrypt / MacSign / MacVerify /
// RawEncrypt / RawDecrypt / GenerateRandomBytes), each doing real work via the
// REST slice's stores and Go-stdlib crypto helpers.
//
// Left as the UnimplementedKeyManagementServiceServer default:
//   - ListRetiredResources / GetRetiredResource — read paths over the REST
//     slice's kmsRetiredResources store; not exercised by the gRPC client's
//     crypto flows and out of scope for the data-plane slice.
//   - DeleteCryptoKey / DeleteCryptoKeyVersion — both return longrunning
//     Operations in the real API; the gRPC LRO plumbing (operation name
//     polling) is a separate slice and not part of this data-plane landing.
//   - ImportCryptoKeyVersion / CreateImportJob — import needs the wrapping-key
//     unwrap round-trip the REST slice records metadata for but does not
//     perform (the wrapped blob is opaque to the sim); the REST import handler
//     provisions fresh material instead, and surfacing that honestly over gRPC
//     is deferred to a dedicated import slice.
//   - Decapsulate — post-quantum key encapsulation (ML-KEM / X-Wing) is a
//     primitive Go's standard library does not expose; the REST slice rejects
//     it with FAILED_PRECONDITION and the gRPC base's Unimplemented status is
//     the faithful equivalent.
type cloudKmsGRPC struct {
	kmspb.UnimplementedKeyManagementServiceServer
}

func registerCloudKMSGRPC(gs *grpc.Server) {
	kmspb.RegisterKeyManagementServiceServer(gs, &cloudKmsGRPC{})
}

// ---------------------------------------------------------------------------
// name helpers
// ---------------------------------------------------------------------------

// kmsNormalizeName strips a redundant leading "projects/" that callers
// sometimes prepend to a name that is already a full path, avoiding the
// projects/projects/... doubling the REST slice has seen. A name that is not
// doubled is returned unchanged.
func kmsNormalizeName(name string) string {
	if strings.HasPrefix(name, "projects/projects/") {
		return strings.TrimPrefix(name, "projects/")
	}
	return name
}

// kmsVerifyCRC32C checks a client-supplied CRC32C against the data. It returns
// the "verified" boolean the API reports back to the client (true when the
// client supplied a checksum that matched) and a gRPC error on mismatch.
func kmsVerifyCRC32C(data []byte, supplied *wrapperspb.Int64Value, field string) (bool, error) {
	if supplied == nil {
		return false, nil
	}
	if supplied.Value != kmsCRC(data) {
		return false, status.Errorf(codes.InvalidArgument, "%s.crc32c mismatch: got %d, want %d", field, supplied.Value, kmsCRC(data))
	}
	return true, nil
}

// kmsInt64Value wraps an int64 in the protobuf Int64Value the KMS CRC fields
// use.
func kmsInt64Value(v int64) *wrapperspb.Int64Value {
	return &wrapperspb.Int64Value{Value: v}
}

// ---------------------------------------------------------------------------
// proto <-> REST-store converters
// ---------------------------------------------------------------------------

// kmsProtectionLevelFromString maps the REST slice's string enum to the proto
// ProtectionLevel. Unrecognized values default to SOFTWARE (the REST slice's
// default).
func kmsProtectionLevelFromString(s string) kmspb.ProtectionLevel {
	switch s {
	case "SOFTWARE":
		return kmspb.ProtectionLevel_SOFTWARE
	case "HSM":
		return kmspb.ProtectionLevel_HSM
	case "EXTERNAL":
		return kmspb.ProtectionLevel_EXTERNAL
	case "EXTERNAL_VPC":
		return kmspb.ProtectionLevel_EXTERNAL_VPC
	case "HSM_SINGLE_TENANT":
		return kmspb.ProtectionLevel_HSM_SINGLE_TENANT
	default:
		return kmspb.ProtectionLevel_SOFTWARE
	}
}

// kmsProtectionLevelString is the inverse of kmsProtectionLevelFromString.
func kmsProtectionLevelString(p kmspb.ProtectionLevel) string {
	switch p {
	case kmspb.ProtectionLevel_HSM:
		return "HSM"
	case kmspb.ProtectionLevel_EXTERNAL:
		return "EXTERNAL"
	case kmspb.ProtectionLevel_EXTERNAL_VPC:
		return "EXTERNAL_VPC"
	case kmspb.ProtectionLevel_HSM_SINGLE_TENANT:
		return "HSM_SINGLE_TENANT"
	default:
		return "SOFTWARE"
	}
}

// kmsAlgorithmFromString maps the REST slice's algorithm string to the proto
// enum value. The REST slice stores algorithms by their proto name
// (GOOGLE_SYMMETRIC_ENCRYPTION, RSA_SIGN_PSS_2048_SHA256, HMAC_SHA256, ...),
// which is exactly the key of the proto-generated value map.
func kmsAlgorithmFromString(s string) kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm {
	if v, ok := kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm_value[s]; ok {
		return kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm(v)
	}
	return kmspb.CryptoKeyVersion_CRYPTO_KEY_VERSION_ALGORITHM_UNSPECIFIED
}

// kmsPurposeFromString maps the REST slice's purpose string to the proto enum.
func kmsPurposeFromString(s string) kmspb.CryptoKey_CryptoKeyPurpose {
	switch s {
	case "ENCRYPT_DECRYPT":
		return kmspb.CryptoKey_ENCRYPT_DECRYPT
	case "ASYMMETRIC_SIGN":
		return kmspb.CryptoKey_ASYMMETRIC_SIGN
	case "ASYMMETRIC_DECRYPT":
		return kmspb.CryptoKey_ASYMMETRIC_DECRYPT
	case "RAW_ENCRYPT_DECRYPT":
		return kmspb.CryptoKey_RAW_ENCRYPT_DECRYPT
	case "MAC":
		return kmspb.CryptoKey_MAC
	default:
		return kmspb.CryptoKey_ENCRYPT_DECRYPT
	}
}

// kmsPurposeString is the inverse of kmsPurposeFromString.
func kmsPurposeString(p kmspb.CryptoKey_CryptoKeyPurpose) string {
	switch p {
	case kmspb.CryptoKey_ASYMMETRIC_SIGN:
		return "ASYMMETRIC_SIGN"
	case kmspb.CryptoKey_ASYMMETRIC_DECRYPT:
		return "ASYMMETRIC_DECRYPT"
	case kmspb.CryptoKey_RAW_ENCRYPT_DECRYPT:
		return "RAW_ENCRYPT_DECRYPT"
	case kmspb.CryptoKey_MAC:
		return "MAC"
	default:
		return "ENCRYPT_DECRYPT"
	}
}

// kmsStateFromString maps the REST slice's state string to the proto enum.
func kmsStateFromString(s string) kmspb.CryptoKeyVersion_CryptoKeyVersionState {
	switch s {
	case "ENABLED":
		return kmspb.CryptoKeyVersion_ENABLED
	case "DISABLED":
		return kmspb.CryptoKeyVersion_DISABLED
	case "DESTROYED":
		return kmspb.CryptoKeyVersion_DESTROYED
	case "DESTROY_SCHEDULED":
		return kmspb.CryptoKeyVersion_DESTROY_SCHEDULED
	case "IMPORT_FAILED":
		return kmspb.CryptoKeyVersion_IMPORT_FAILED
	case "GENERATION_FAILED":
		return kmspb.CryptoKeyVersion_GENERATION_FAILED
	default:
		return kmspb.CryptoKeyVersion_CRYPTO_KEY_VERSION_STATE_UNSPECIFIED
	}
}

// kmsStateString is the inverse of kmsStateFromString.
func kmsStateString(s kmspb.CryptoKeyVersion_CryptoKeyVersionState) string {
	switch s {
	case kmspb.CryptoKeyVersion_ENABLED:
		return "ENABLED"
	case kmspb.CryptoKeyVersion_DISABLED:
		return "DISABLED"
	case kmspb.CryptoKeyVersion_DESTROYED:
		return "DESTROYED"
	case kmspb.CryptoKeyVersion_DESTROY_SCHEDULED:
		return "DESTROY_SCHEDULED"
	case kmspb.CryptoKeyVersion_IMPORT_FAILED:
		return "IMPORT_FAILED"
	case kmspb.CryptoKeyVersion_GENERATION_FAILED:
		return "GENERATION_FAILED"
	default:
		return ""
	}
}

// kmsParseRFC3339 parses the REST slice's RFC3339 timestamp strings into a
// proto Timestamp. Returns nil for an empty string (the proto field is
// optional).
func kmsParseRFC3339(s string) *timestamppb.Timestamp {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return timestamppb.New(t)
}

// kmsParseDuration parses the REST slice's duration string ("{seconds}s") into
// a proto Duration. Returns nil for an empty string.
func kmsParseDuration(s string) *durationpb.Duration {
	if s == "" {
		return nil
	}
	// Real KMS / the proto clients express rotation_period and
	// destroy_scheduled_duration as "{seconds}s".
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil
	}
	return durationpb.New(d)
}

// kmsKeyRingToProto converts the REST-store KeyRing to the proto KeyRing.
func kmsKeyRingToProto(kr kmsKeyRing) *kmspb.KeyRing {
	return &kmspb.KeyRing{
		Name:       kr.Name,
		CreateTime: kmsParseRFC3339(kr.CreateTime),
	}
}

// kmsCryptoKeyVersionToProto converts the REST-store CryptoKeyVersion wire
// shape to the proto CryptoKeyVersion.
func kmsCryptoKeyVersionToProto(v kmsCryptoKeyVersion) *kmspb.CryptoKeyVersion {
	return &kmspb.CryptoKeyVersion{
		Name:             v.Name,
		State:            kmsStateFromString(v.State),
		ProtectionLevel:  kmsProtectionLevelFromString(v.ProtectionLevel),
		Algorithm:        kmsAlgorithmFromString(v.Algorithm),
		CreateTime:       kmsParseRFC3339(v.CreateTime),
		GenerateTime:     kmsParseRFC3339(v.GenerateTime),
		DestroyTime:      kmsParseRFC3339(v.DestroyTime),
		DestroyEventTime: kmsParseRFC3339(v.DestroyEventTime),
		ImportJob:        v.ImportJob,
		ImportTime:       kmsParseRFC3339(v.ImportTime),
	}
}

// kmsCryptoKeyToProto converts the REST-store CryptoKey (plus its assembled
// primary) to the proto CryptoKey.
func kmsCryptoKeyToProto(k kmsStoredCryptoKey) *kmspb.CryptoKey {
	out := &kmspb.CryptoKey{
		Name:       k.Name,
		Purpose:    kmsPurposeFromString(k.Purpose),
		CreateTime: kmsParseRFC3339(k.CreateTime),
		Labels:     k.Labels,
		VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
			ProtectionLevel: kmsProtectionLevelFromString(k.ProtectionLevel),
			Algorithm:       kmsAlgorithmFromString(k.Algorithm),
		},
	}
	if k.NextRotationTime != "" {
		out.NextRotationTime = kmsParseRFC3339(k.NextRotationTime)
	}
	if k.RotationPeriod != "" {
		if d := kmsParseDuration(k.RotationPeriod); d != nil {
			out.RotationSchedule = &kmspb.CryptoKey_RotationPeriod{RotationPeriod: d}
		}
	}
	if k.PrimaryVersionID != "" {
		if v, ok := kmsCryptoKeyVersions.Get(k.Name + "/cryptoKeyVersions/" + k.PrimaryVersionID); ok {
			out.Primary = kmsCryptoKeyVersionToProto(v)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// admin RPCs
// ---------------------------------------------------------------------------

func (s *cloudKmsGRPC) ListKeyRings(ctx context.Context, req *kmspb.ListKeyRingsRequest) (*kmspb.ListKeyRingsResponse, error) {
	parent := kmsNormalizeName(req.GetParent())
	prefix := parent + "/keyRings/"
	var all []*kmspb.KeyRing
	for _, kr := range kmsKeyRings.List() {
		if strings.HasPrefix(kr.Name, prefix) {
			all = append(all, kmsKeyRingToProto(kr))
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	start := kmsPageOffset(req.GetPageToken())
	end, next := kmsPageBounds(start, int(req.GetPageSize()), len(all))
	return &kmspb.ListKeyRingsResponse{
		KeyRings:      all[start:end],
		NextPageToken: next,
		TotalSize:     int32(len(all)),
	}, nil
}

func (s *cloudKmsGRPC) GetKeyRing(ctx context.Context, req *kmspb.GetKeyRingRequest) (*kmspb.KeyRing, error) {
	name := kmsNormalizeName(req.GetName())
	kr, ok := kmsKeyRings.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "KeyRing %s not found", name)
	}
	return kmsKeyRingToProto(kr), nil
}

func (s *cloudKmsGRPC) CreateKeyRing(ctx context.Context, req *kmspb.CreateKeyRingRequest) (*kmspb.KeyRing, error) {
	parent := kmsNormalizeName(req.GetParent())
	if req.GetKeyRingId() == "" {
		return nil, status.Error(codes.InvalidArgument, "key_ring_id is required")
	}
	name := parent + "/keyRings/" + req.GetKeyRingId()
	if _, exists := kmsKeyRings.Get(name); exists {
		return nil, status.Errorf(codes.AlreadyExists, "KeyRing %s already exists", name)
	}
	kr := kmsKeyRing{Name: name, CreateTime: kmsNow()}
	kmsKeyRings.Put(name, kr)
	return kmsKeyRingToProto(kr), nil
}

func (s *cloudKmsGRPC) ListCryptoKeys(ctx context.Context, req *kmspb.ListCryptoKeysRequest) (*kmspb.ListCryptoKeysResponse, error) {
	parent := kmsNormalizeName(req.GetParent())
	prefix := parent + "/cryptoKeys/"
	var all []*kmspb.CryptoKey
	for _, k := range kmsCryptoKeys.List() {
		if strings.HasPrefix(k.Name, prefix) {
			all = append(all, kmsCryptoKeyToProto(k))
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	start := kmsPageOffset(req.GetPageToken())
	end, next := kmsPageBounds(start, int(req.GetPageSize()), len(all))
	return &kmspb.ListCryptoKeysResponse{
		CryptoKeys:    all[start:end],
		NextPageToken: next,
		TotalSize:     int32(len(all)),
	}, nil
}

func (s *cloudKmsGRPC) GetCryptoKey(ctx context.Context, req *kmspb.GetCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	name := kmsNormalizeName(req.GetName())
	k, ok := kmsCryptoKeys.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found", name)
	}
	return kmsCryptoKeyToProto(k), nil
}

func (s *cloudKmsGRPC) CreateCryptoKey(ctx context.Context, req *kmspb.CreateCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	parent := kmsNormalizeName(req.GetParent())
	ringName := parent
	if _, ok := kmsKeyRings.Get(ringName); !ok {
		return nil, status.Errorf(codes.NotFound, "KeyRing %s not found", ringName)
	}
	if req.GetCryptoKeyId() == "" {
		return nil, status.Error(codes.InvalidArgument, "crypto_key_id is required")
	}
	ck := req.GetCryptoKey()
	if ck == nil {
		return nil, status.Error(codes.InvalidArgument, "crypto_key is required")
	}
	name := ringName + "/cryptoKeys/" + req.GetCryptoKeyId()
	if _, exists := kmsCryptoKeys.Get(name); exists {
		return nil, status.Errorf(codes.AlreadyExists, "CryptoKey %s already exists", name)
	}

	purpose := kmsPurposeString(ck.GetPurpose())
	if purpose == "ENCRYPT_DECRYPT" && ck.GetPurpose() == 0 {
		purpose = kmsPurposeEncryptDecrypt
	}
	protection := kmsDefaultProtectionLevel
	algorithm := kmsSymmetricAlgorithm
	if vt := ck.GetVersionTemplate(); vt != nil {
		if ps := kmsProtectionLevelString(vt.GetProtectionLevel()); ps != "" {
			protection = ps
		}
		if alg := vt.GetAlgorithm().String(); alg != "CRYPTO_KEY_VERSION_ALGORITHM_UNSPECIFIED" {
			algorithm = alg
		}
	}

	stored := kmsStoredCryptoKey{
		Name:            name,
		Purpose:         purpose,
		CreateTime:      kmsNow(),
		ProtectionLevel: protection,
		Algorithm:       algorithm,
		Labels:          ck.GetLabels(),
	}
	if ck.GetNextRotationTime() != nil {
		stored.NextRotationTime = formatTimestamp(ck.GetNextRotationTime())
	}
	if ck.GetRotationPeriod() != nil {
		stored.RotationPeriod = kmsDurationString(ck.GetRotationPeriod())
	}

	if !req.GetSkipInitialVersionCreation() {
		ver, err := kmsCreateVersionForAlg(name, "1", protection, algorithm)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "could not generate key version: %v", err)
		}
		stored.VersionSeq = 1
		if purpose == kmsPurposeEncryptDecrypt || purpose == "RAW_ENCRYPT_DECRYPT" {
			stored.PrimaryVersionID = ver
		}
	}
	kmsCryptoKeys.Put(name, stored)
	return kmsCryptoKeyToProto(stored), nil
}

func (s *cloudKmsGRPC) UpdateCryptoKey(ctx context.Context, req *kmspb.UpdateCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	ck := req.GetCryptoKey()
	if ck == nil {
		return nil, status.Error(codes.InvalidArgument, "crypto_key is required")
	}
	name := kmsNormalizeName(ck.GetName())
	k, ok := kmsCryptoKeys.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found", name)
	}
	mask := req.GetUpdateMask()
	if mask == nil || len(mask.GetPaths()) == 0 {
		return kmsCryptoKeyToProto(k), nil
	}
	for _, field := range mask.GetPaths() {
		switch field {
		case "labels":
			k.Labels = ck.GetLabels()
		case "next_rotation_time":
			if ck.GetNextRotationTime() != nil {
				k.NextRotationTime = formatTimestamp(ck.GetNextRotationTime())
			}
		case "rotation_period":
			if ck.GetRotationPeriod() != nil {
				k.RotationPeriod = kmsDurationString(ck.GetRotationPeriod())
			}
		}
	}
	kmsCryptoKeys.Put(name, k)
	return kmsCryptoKeyToProto(k), nil
}

func (s *cloudKmsGRPC) ListCryptoKeyVersions(ctx context.Context, req *kmspb.ListCryptoKeyVersionsRequest) (*kmspb.ListCryptoKeyVersionsResponse, error) {
	parent := kmsNormalizeName(req.GetParent())
	if _, ok := kmsCryptoKeys.Get(parent); !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found", parent)
	}
	prefix := parent + "/cryptoKeyVersions/"
	var all []*kmspb.CryptoKeyVersion
	for _, v := range kmsCryptoKeyVersions.List() {
		if strings.HasPrefix(v.Name, prefix) {
			all = append(all, kmsCryptoKeyVersionToProto(v))
		}
	}
	sort.Slice(all, func(i, j int) bool {
		na, _ := kmsVersionNumber(all[i].Name)
		nb, _ := kmsVersionNumber(all[j].Name)
		return na < nb
	})
	start := kmsPageOffset(req.GetPageToken())
	end, next := kmsPageBounds(start, int(req.GetPageSize()), len(all))
	return &kmspb.ListCryptoKeyVersionsResponse{
		CryptoKeyVersions: all[start:end],
		NextPageToken:     next,
		TotalSize:         int32(len(all)),
	}, nil
}

func (s *cloudKmsGRPC) GetCryptoKeyVersion(ctx context.Context, req *kmspb.GetCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	name := kmsNormalizeName(req.GetName())
	v, ok := kmsCryptoKeyVersions.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", name)
	}
	return kmsCryptoKeyVersionToProto(v), nil
}

func (s *cloudKmsGRPC) CreateCryptoKeyVersion(ctx context.Context, req *kmspb.CreateCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	keyName := kmsNormalizeName(req.GetParent())
	key, ok := kmsCryptoKeys.Get(keyName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found", keyName)
	}
	var assigned int
	kmsCryptoKeys.Update(keyName, func(k *kmsStoredCryptoKey) {
		if k.VersionSeq < kmsHighestVersionID(keyName) {
			k.VersionSeq = kmsHighestVersionID(keyName)
		}
		k.VersionSeq++
		assigned = k.VersionSeq
	})
	next := fmt.Sprintf("%d", assigned)
	protection := key.ProtectionLevel
	algorithm := key.Algorithm
	if ckv := req.GetCryptoKeyVersion(); ckv != nil {
		if ckv.GetAlgorithm() != kmspb.CryptoKeyVersion_CRYPTO_KEY_VERSION_ALGORITHM_UNSPECIFIED {
			algorithm = ckv.GetAlgorithm().String()
		}
		if ckv.GetProtectionLevel() != kmspb.ProtectionLevel_PROTECTION_LEVEL_UNSPECIFIED {
			protection = kmsProtectionLevelString(ckv.GetProtectionLevel())
		}
	}
	versionID, err := kmsCreateVersionForAlg(keyName, next, protection, algorithm)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not generate key version: %v", err)
	}
	v, _ := kmsCryptoKeyVersions.Get(keyName + "/cryptoKeyVersions/" + versionID)
	return kmsCryptoKeyVersionToProto(v), nil
}

func (s *cloudKmsGRPC) UpdateCryptoKeyVersion(ctx context.Context, req *kmspb.UpdateCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	ckv := req.GetCryptoKeyVersion()
	if ckv == nil {
		return nil, status.Error(codes.InvalidArgument, "crypto_key_version is required")
	}
	name := kmsNormalizeName(ckv.GetName())
	v, ok := kmsCryptoKeyVersions.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", name)
	}
	mask := req.GetUpdateMask()
	if mask != nil {
		for _, field := range mask.GetPaths() {
			if field != "state" {
				continue
			}
			newState := ckv.GetState()
			if newState != kmspb.CryptoKeyVersion_ENABLED && newState != kmspb.CryptoKeyVersion_DISABLED {
				return nil, status.Errorf(codes.InvalidArgument, "state may only be set to ENABLED or DISABLED, got %s", newState.String())
			}
			v.State = kmsStateString(newState)
		}
	}
	kmsCryptoKeyVersions.Put(name, v)
	return kmsCryptoKeyVersionToProto(v), nil
}

func (s *cloudKmsGRPC) UpdateCryptoKeyPrimaryVersion(ctx context.Context, req *kmspb.UpdateCryptoKeyPrimaryVersionRequest) (*kmspb.CryptoKey, error) {
	keyName := kmsNormalizeName(req.GetName())
	k, ok := kmsCryptoKeys.Get(keyName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found", keyName)
	}
	if req.GetCryptoKeyVersionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "crypto_key_version_id is required")
	}
	if _, ok := kmsCryptoKeyVersions.Get(keyName + "/cryptoKeyVersions/" + req.GetCryptoKeyVersionId()); !ok {
		return nil, status.Errorf(codes.InvalidArgument, "CryptoKeyVersion %s not found", req.GetCryptoKeyVersionId())
	}
	k.PrimaryVersionID = req.GetCryptoKeyVersionId()
	kmsCryptoKeys.Put(keyName, k)
	return kmsCryptoKeyToProto(k), nil
}

func (s *cloudKmsGRPC) DestroyCryptoKeyVersion(ctx context.Context, req *kmspb.DestroyCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	name := kmsNormalizeName(req.GetName())
	v, ok := kmsCryptoKeyVersions.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", name)
	}
	if v.State == "DESTROY_SCHEDULED" || v.State == "DESTROYED" {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is already %s", name, v.State)
	}
	v.State = "DESTROY_SCHEDULED"
	v.DestroyTime = time.Now().UTC().Add(kmsDestroyScheduledDelay).Format(time.RFC3339)
	kmsCryptoKeyVersions.Put(name, v)
	return kmsCryptoKeyVersionToProto(v), nil
}

func (s *cloudKmsGRPC) RestoreCryptoKeyVersion(ctx context.Context, req *kmspb.RestoreCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	name := kmsNormalizeName(req.GetName())
	v, ok := kmsCryptoKeyVersions.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", name)
	}
	if v.State != "DESTROY_SCHEDULED" {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is not DESTROY_SCHEDULED (state %s)", name, v.State)
	}
	v.State = "DISABLED"
	v.DestroyTime = ""
	kmsCryptoKeyVersions.Put(name, v)
	return kmsCryptoKeyVersionToProto(v), nil
}

func (s *cloudKmsGRPC) GetPublicKey(ctx context.Context, req *kmspb.GetPublicKeyRequest) (*kmspb.PublicKey, error) {
	name := kmsNormalizeName(req.GetName())
	version, ok := kmsCryptoKeyVersions.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", name)
	}
	material, ok := kmsKeyMaterial.Get(name)
	if !ok || len(material.PrivatePEM) == 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s has no public key (not an asymmetric key)", name)
	}
	pub, err := kmsPublicFromPEM(material.PrivatePEM)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not derive public key: %v", err)
	}
	pemStr, err := kmsPublicKeyPEM(pub)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not encode public key: %v", err)
	}
	return &kmspb.PublicKey{
		Name:            name,
		Pem:             pemStr,
		Algorithm:       kmsAlgorithmFromString(version.Algorithm),
		ProtectionLevel: kmsProtectionLevelFromString(version.ProtectionLevel),
		PublicKeyFormat: kmspb.PublicKey_PEM,
	}, nil
}

// ---------------------------------------------------------------------------
// data-plane RPCs — real crypto via the REST slice's helpers
// ---------------------------------------------------------------------------

func (s *cloudKmsGRPC) Encrypt(ctx context.Context, req *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	name := kmsNormalizeName(req.GetName())
	// The name may be a CryptoKey or a CryptoKeyVersion. Resolve the version.
	versionName, versionNum, err := kmsResolveEncryptVersion(name)
	if err != nil {
		return nil, err
	}
	keyName := kmsCryptoKeyNameFromVersion(versionName)
	key, ok := kmsCryptoKeys.Get(keyName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found", keyName)
	}
	if key.Purpose != kmsPurposeEncryptDecrypt {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKey %s is not for ENCRYPT_DECRYPT", keyName)
	}
	version, ok := kmsCryptoKeyVersions.Get(versionName)
	if !ok || version.State != "ENABLED" {
		return nil, status.Errorf(codes.FailedPrecondition, "primary version of %s is not enabled", keyName)
	}
	material, ok := kmsKeyMaterial.Get(versionName)
	if !ok {
		return nil, status.Errorf(codes.Internal, "missing key material for %s", versionName)
	}
	plaintext := req.GetPlaintext()
	aad := req.GetAdditionalAuthenticatedData()
	if _, err := kmsVerifyCRC32C(plaintext, req.GetPlaintextCrc32C(), "plaintext"); err != nil {
		return nil, err
	}
	verifiedAAD, err := kmsVerifyCRC32C(aad, req.GetAdditionalAuthenticatedDataCrc32C(), "additional_authenticated_data")
	if err != nil {
		return nil, err
	}
	ciphertext, err := kmsEncryptBytes(material.Key, versionNum, plaintext, aad)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encryption failed: %v", err)
	}
	verifiedPlaintext := req.GetPlaintextCrc32C() != nil
	return &kmspb.EncryptResponse{
		Name:                    versionName,
		Ciphertext:              ciphertext,
		CiphertextCrc32C:        kmsInt64Value(kmsCRC(ciphertext)),
		VerifiedPlaintextCrc32C: verifiedPlaintext,
		VerifiedAdditionalAuthenticatedDataCrc32C: verifiedAAD,
		ProtectionLevel: kmsProtectionLevelFromString(key.ProtectionLevel),
	}, nil
}

func (s *cloudKmsGRPC) Decrypt(ctx context.Context, req *kmspb.DecryptRequest) (*kmspb.DecryptResponse, error) {
	keyName := kmsNormalizeName(req.GetName())
	if _, ok := kmsCryptoKeys.Get(keyName); !ok {
		return nil, status.Errorf(codes.NotFound, "CryptoKey %s not found", keyName)
	}
	ciphertext := req.GetCiphertext()
	aad := req.GetAdditionalAuthenticatedData()
	if _, err := kmsVerifyCRC32C(ciphertext, req.GetCiphertextCrc32C(), "ciphertext"); err != nil {
		return nil, err
	}
	versionNum, blob, err := kmsParseCiphertext(ciphertext)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Decryption failed: the ciphertext is malformed")
	}
	versionName := fmt.Sprintf("%s/cryptoKeyVersions/%d", keyName, versionNum)
	version, ok := kmsCryptoKeyVersions.Get(versionName)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "Decryption failed: the version used to encrypt does not exist")
	}
	if version.State != "ENABLED" {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is not enabled (state %s)", versionName, version.State)
	}
	material, ok := kmsKeyMaterial.Get(versionName)
	if !ok {
		return nil, status.Errorf(codes.Internal, "missing key material for %s", versionName)
	}
	plaintext, err := kmsDecryptBytes(material.Key, blob, aad)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Decryption failed: verify the ciphertext and AAD match what was used to encrypt")
	}
	return &kmspb.DecryptResponse{
		Plaintext:       plaintext,
		PlaintextCrc32C: kmsInt64Value(kmsCRC(plaintext)),
		UsedPrimary:     true,
		ProtectionLevel: kmsProtectionLevelFromString(kmsDefaultProtectionLevel),
	}, nil
}

func (s *cloudKmsGRPC) AsymmetricSign(ctx context.Context, req *kmspb.AsymmetricSignRequest) (*kmspb.AsymmetricSignResponse, error) {
	versionName := kmsNormalizeName(req.GetName())
	version, material, err := kmsLoadEnabledVersionGRPC(versionName)
	if err != nil {
		return nil, err
	}
	if len(material.PrivatePEM) == 0 || !strings.Contains(version.Algorithm, "SIGN") {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is not an asymmetric-sign key", versionName)
	}
	priv, err := kmsPrivateFromPEM(material.PrivatePEM)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not parse private key: %v", err)
	}

	var digest []byte
	verifiedDigest := false
	verifiedData := false
	if d := req.GetDigest(); d != nil {
		digest, err = kmsDigestBytes(d, version.Algorithm)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid digest: %v", err)
		}
		verifiedDigest, err = kmsVerifyCRC32C(digest, req.GetDigestCrc32C(), "digest")
		if err != nil {
			return nil, err
		}
	} else {
		data := req.GetData()
		verifiedData, err = kmsVerifyCRC32C(data, req.GetDataCrc32C(), "data")
		if err != nil {
			return nil, err
		}
		digest = kmsHashForMessage(version.Algorithm, data)
	}

	var sig []byte
	ch := cryptoHashForAlg(version.Algorithm)
	switch key := priv.(type) {
	case *rsa.PrivateKey:
		if strings.Contains(version.Algorithm, "PSS") {
			sig, err = rsa.SignPSS(rand.Reader, key, ch, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: ch})
		} else {
			sig, err = rsa.SignPKCS1v15(rand.Reader, key, ch, digest)
		}
	case *ecdsa.PrivateKey:
		sig, err = ecdsa.SignASN1(rand.Reader, key, digest)
	default:
		return nil, status.Error(codes.FailedPrecondition, "unsupported asymmetric-sign key type")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "signing failed: %v", err)
	}
	return &kmspb.AsymmetricSignResponse{
		Signature:            sig,
		SignatureCrc32C:      kmsInt64Value(kmsCRC(sig)),
		VerifiedDigestCrc32C: verifiedDigest,
		VerifiedDataCrc32C:   verifiedData,
		ProtectionLevel:      kmsProtectionLevelFromString(version.ProtectionLevel),
	}, nil
}

func (s *cloudKmsGRPC) AsymmetricDecrypt(ctx context.Context, req *kmspb.AsymmetricDecryptRequest) (*kmspb.AsymmetricDecryptResponse, error) {
	versionName := kmsNormalizeName(req.GetName())
	version, material, err := kmsLoadEnabledVersionGRPC(versionName)
	if err != nil {
		return nil, err
	}
	if len(material.PrivatePEM) == 0 || !strings.Contains(version.Algorithm, "DECRYPT") {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is not an asymmetric-decrypt key", versionName)
	}
	ciphertext := req.GetCiphertext()
	verifiedCiphertext, err := kmsVerifyCRC32C(ciphertext, req.GetCiphertextCrc32C(), "ciphertext")
	if err != nil {
		return nil, err
	}
	priv, err := kmsPrivateFromPEM(material.PrivatePEM)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not parse private key: %v", err)
	}
	rsaKey, ok := priv.(*rsa.PrivateKey)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "asymmetric-decrypt requires an RSA key")
	}
	ch := cryptoHashForAlg(version.Algorithm)
	plaintext, err := rsa.DecryptOAEP(ch.New(), rand.Reader, rsaKey, ciphertext, nil)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Decryption failed: verify the ciphertext")
	}
	return &kmspb.AsymmetricDecryptResponse{
		Plaintext:                plaintext,
		PlaintextCrc32C:          kmsInt64Value(kmsCRC(plaintext)),
		VerifiedCiphertextCrc32C: verifiedCiphertext,
		ProtectionLevel:          kmsProtectionLevelFromString(version.ProtectionLevel),
	}, nil
}

func (s *cloudKmsGRPC) MacSign(ctx context.Context, req *kmspb.MacSignRequest) (*kmspb.MacSignResponse, error) {
	versionName := kmsNormalizeName(req.GetName())
	version, material, err := kmsLoadEnabledVersionGRPC(versionName)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(version.Algorithm, "HMAC_") {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is not a MAC key", versionName)
	}
	data := req.GetData()
	verifiedData, err := kmsVerifyCRC32C(data, req.GetDataCrc32C(), "data")
	if err != nil {
		return nil, err
	}
	h := hmac.New(kmsHashForAlg(version.Algorithm), material.Key)
	h.Write(data)
	mac := h.Sum(nil)
	return &kmspb.MacSignResponse{
		Name:               versionName,
		Mac:                mac,
		MacCrc32C:          kmsInt64Value(kmsCRC(mac)),
		VerifiedDataCrc32C: verifiedData,
		ProtectionLevel:    kmsProtectionLevelFromString(version.ProtectionLevel),
	}, nil
}

func (s *cloudKmsGRPC) MacVerify(ctx context.Context, req *kmspb.MacVerifyRequest) (*kmspb.MacVerifyResponse, error) {
	versionName := kmsNormalizeName(req.GetName())
	version, material, err := kmsLoadEnabledVersionGRPC(versionName)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(version.Algorithm, "HMAC_") {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is not a MAC key", versionName)
	}
	data := req.GetData()
	mac := req.GetMac()
	verifiedData, err := kmsVerifyCRC32C(data, req.GetDataCrc32C(), "data")
	if err != nil {
		return nil, err
	}
	verifiedMac, err := kmsVerifyCRC32C(mac, req.GetMacCrc32C(), "mac")
	if err != nil {
		return nil, err
	}
	h := hmac.New(kmsHashForAlg(version.Algorithm), material.Key)
	h.Write(data)
	success := hmac.Equal(mac, h.Sum(nil))
	return &kmspb.MacVerifyResponse{
		Name:                     versionName,
		Success:                  success,
		VerifiedDataCrc32C:       verifiedData,
		VerifiedMacCrc32C:        verifiedMac,
		VerifiedSuccessIntegrity: success,
		ProtectionLevel:          kmsProtectionLevelFromString(version.ProtectionLevel),
	}, nil
}

func (s *cloudKmsGRPC) RawEncrypt(ctx context.Context, req *kmspb.RawEncryptRequest) (*kmspb.RawEncryptResponse, error) {
	versionName := kmsNormalizeName(req.GetName())
	version, material, err := kmsLoadEnabledVersionGRPC(versionName)
	if err != nil {
		return nil, err
	}
	plaintext := req.GetPlaintext()
	aad := req.GetAdditionalAuthenticatedData()
	verifiedPlaintext, err := kmsVerifyCRC32C(plaintext, req.GetPlaintextCrc32C(), "plaintext")
	if err != nil {
		return nil, err
	}
	verifiedAAD, err := kmsVerifyCRC32C(aad, req.GetAdditionalAuthenticatedDataCrc32C(), "additional_authenticated_data")
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(material.Key)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cipher init failed: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "GCM init failed: %v", err)
	}
	var iv []byte
	verifiedIV := false
	if supplied := req.GetInitializationVector(); len(supplied) > 0 {
		iv = supplied
		if len(iv) != gcm.NonceSize() {
			return nil, status.Errorf(codes.InvalidArgument, "initialization_vector must be %d bytes", gcm.NonceSize())
		}
		verifiedIV, err = kmsVerifyCRC32C(iv, req.GetInitializationVectorCrc32C(), "initialization_vector")
		if err != nil {
			return nil, err
		}
	} else {
		iv = make([]byte, gcm.NonceSize())
		if _, err := io.ReadFull(rand.Reader, iv); err != nil {
			return nil, status.Errorf(codes.Internal, "could not generate IV: %v", err)
		}
	}
	sealed := gcm.Seal(nil, iv, plaintext, aad)
	tagLen := int32(gcm.Overhead())
	return &kmspb.RawEncryptResponse{
		Name:                       versionName,
		Ciphertext:                 sealed,
		CiphertextCrc32C:           kmsInt64Value(kmsCRC(sealed)),
		InitializationVector:       iv,
		InitializationVectorCrc32C: kmsInt64Value(kmsCRC(iv)),
		TagLength:                  tagLen,
		VerifiedPlaintextCrc32C:    verifiedPlaintext,
		VerifiedAdditionalAuthenticatedDataCrc32C: verifiedAAD,
		VerifiedInitializationVectorCrc32C:        verifiedIV,
		ProtectionLevel:                           kmsProtectionLevelFromString(version.ProtectionLevel),
	}, nil
}

func (s *cloudKmsGRPC) RawDecrypt(ctx context.Context, req *kmspb.RawDecryptRequest) (*kmspb.RawDecryptResponse, error) {
	versionName := kmsNormalizeName(req.GetName())
	version, material, err := kmsLoadEnabledVersionGRPC(versionName)
	if err != nil {
		return nil, err
	}
	ciphertext := req.GetCiphertext()
	aad := req.GetAdditionalAuthenticatedData()
	iv := req.GetInitializationVector()
	verifiedCiphertext, err := kmsVerifyCRC32C(ciphertext, req.GetCiphertextCrc32C(), "ciphertext")
	if err != nil {
		return nil, err
	}
	verifiedAAD, err := kmsVerifyCRC32C(aad, req.GetAdditionalAuthenticatedDataCrc32C(), "additional_authenticated_data")
	if err != nil {
		return nil, err
	}
	verifiedIV, err := kmsVerifyCRC32C(iv, req.GetInitializationVectorCrc32C(), "initialization_vector")
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(material.Key)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cipher init failed: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "GCM init failed: %v", err)
	}
	if len(iv) != gcm.NonceSize() {
		return nil, status.Errorf(codes.InvalidArgument, "initialization_vector must be %d bytes", gcm.NonceSize())
	}
	plaintext, err := gcm.Open(nil, iv, ciphertext, aad)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Decryption failed: verify the ciphertext, IV and AAD")
	}
	return &kmspb.RawDecryptResponse{
		Plaintext:                plaintext,
		PlaintextCrc32C:          kmsInt64Value(kmsCRC(plaintext)),
		VerifiedCiphertextCrc32C: verifiedCiphertext,
		VerifiedAdditionalAuthenticatedDataCrc32C: verifiedAAD,
		VerifiedInitializationVectorCrc32C:        verifiedIV,
		ProtectionLevel:                           kmsProtectionLevelFromString(version.ProtectionLevel),
	}, nil
}

func (s *cloudKmsGRPC) GenerateRandomBytes(ctx context.Context, req *kmspb.GenerateRandomBytesRequest) (*kmspb.GenerateRandomBytesResponse, error) {
	location := kmsNormalizeName(req.GetLocation())
	n := req.GetLengthBytes()
	if n < 8 || n > 1024 {
		return nil, status.Errorf(codes.InvalidArgument, "length_bytes must be between 8 and 1024, got %d", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return nil, status.Errorf(codes.Internal, "could not read random bytes: %v", err)
	}
	_ = location // the location only scopes the randomness source; the sim has one
	return &kmspb.GenerateRandomBytesResponse{
		Data:       data,
		DataCrc32C: kmsInt64Value(kmsCRC(data)),
	}, nil
}

// ---------------------------------------------------------------------------
// helpers — resolve version names, format timestamps / durations, paginate
// ---------------------------------------------------------------------------

// kmsResolveEncryptVersion resolves a CryptoKey or CryptoKeyVersion name to the
// version used for symmetric Encrypt. When the name is a CryptoKey, its primary
// version is used.
func kmsResolveEncryptVersion(name string) (versionName string, versionNum int, err error) {
	if strings.Contains(name, "/cryptoKeyVersions/") {
		num, ok := kmsVersionNumber(name)
		if !ok {
			return "", 0, status.Errorf(codes.InvalidArgument, "invalid CryptoKeyVersion name %s", name)
		}
		return name, num, nil
	}
	// name is a CryptoKey; resolve its primary.
	key, ok := kmsCryptoKeys.Get(name)
	if !ok {
		return "", 0, status.Errorf(codes.NotFound, "CryptoKey %s not found", name)
	}
	if key.PrimaryVersionID == "" {
		return "", 0, status.Errorf(codes.FailedPrecondition, "CryptoKey %s has no primary version", name)
	}
	vn := name + "/cryptoKeyVersions/" + key.PrimaryVersionID
	num, _ := kmsVersionNumber(vn)
	return vn, num, nil
}

// kmsCryptoKeyNameFromVersion strips the trailing /cryptoKeyVersions/{id} from
// a version name to recover its CryptoKey name.
func kmsCryptoKeyNameFromVersion(versionName string) string {
	i := strings.LastIndex(versionName, "/cryptoKeyVersions/")
	if i < 0 {
		return versionName
	}
	return versionName[:i]
}

// kmsLoadEnabledVersionGRPC is the gRPC analogue of kmsLoadEnabledVersion: it
// returns the ENABLED version and its material, or a gRPC error.
func kmsLoadEnabledVersionGRPC(versionName string) (kmsCryptoKeyVersion, kmsKeyMaterialRecord, error) {
	version, ok := kmsCryptoKeyVersions.Get(versionName)
	if !ok {
		return version, kmsKeyMaterialRecord{}, status.Errorf(codes.NotFound, "CryptoKeyVersion %s not found", versionName)
	}
	if version.State != "ENABLED" {
		return version, kmsKeyMaterialRecord{}, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is not enabled (state %s)", versionName, version.State)
	}
	material, ok := kmsKeyMaterial.Get(versionName)
	if !ok {
		return version, kmsKeyMaterialRecord{}, status.Errorf(codes.Internal, "missing key material for %s", versionName)
	}
	return version, material, nil
}

// kmsDigestBytes extracts the digest payload from the oneof, asserting it
// matches the algorithm's expected hash size.
func kmsDigestBytes(d *kmspb.Digest, algorithm string) ([]byte, error) {
	switch algorithm {
	case "RSA_SIGN_PKCS1_4096_SHA512", "RSA_SIGN_PSS_4096_SHA512":
		if d.GetSha512() == nil {
			return nil, fmt.Errorf("algorithm %s requires digest.sha512", algorithm)
		}
		return d.GetSha512(), nil
	case "EC_SIGN_P384_SHA384":
		if d.GetSha384() == nil {
			return nil, fmt.Errorf("algorithm %s requires digest.sha384", algorithm)
		}
		return d.GetSha384(), nil
	default:
		if d.GetSha256() == nil {
			return nil, fmt.Errorf("algorithm %s requires digest.sha256", algorithm)
		}
		return d.GetSha256(), nil
	}
}

// cryptoHashForAlg maps an asymmetric-sign / asymmetric-decrypt algorithm to
// its crypto.Hash. Real KMS hashes the message with the algorithm's digest
// before signing/decrypting.
func cryptoHashForAlg(algorithm string) crypto.Hash {
	switch {
	case strings.HasSuffix(algorithm, "_SHA512"):
		return crypto.SHA512
	case strings.HasSuffix(algorithm, "_SHA384"):
		return crypto.SHA384
	case strings.HasSuffix(algorithm, "_SHA1"):
		return crypto.SHA1
	default:
		return crypto.SHA256
	}
}

// kmsHashForMessage hashes a raw message with the algorithm's digest for the
// "data supplied instead of digest" AsymmetricSign path.
func kmsHashForMessage(algorithm string, msg []byte) []byte {
	h := cryptoHashForAlg(algorithm).New()
	h.Write(msg)
	return h.Sum(nil)
}

// formatTimestamp renders a proto Timestamp as the RFC3339 string the REST
// stores use.
func formatTimestamp(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339)
}

// kmsDurationString renders a proto Duration as the "{seconds}s" string the
// REST stores use.
func kmsDurationString(d *durationpb.Duration) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("%ds", int64(d.AsDuration().Seconds()))
}

// kmsPageOffset decodes a page token into a list offset. An empty or invalid
// token means "start at the beginning". The token is an opaque base64 of the
// offset; it is never parsed by clients.
func kmsPageOffset(token string) int {
	if token == "" {
		return 0
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(string(raw), "%d", &n); err != nil {
		return 0
	}
	if n < 0 {
		return 0
	}
	return n
}

// kmsPageBounds returns the end index and next-page token for a page starting
// at `start`, given the page size and total. When pageSize <= 0 the whole
// remaining slice is returned.
func kmsPageBounds(start, pageSize, total int) (end int, nextToken string) {
	end = total
	if pageSize > 0 && start+pageSize < total {
		end = start + pageSize
	}
	if end < total {
		nextToken = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", end)))
	}
	return end, nextToken
}

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	smpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	exprpb "google.golang.org/genproto/googleapis/type/expr"
)

// google.cloud.secretmanager.v1.SecretManagerService on the shared gRPC
// server, for the gRPC-only cloud.google.com/go/secretmanager client. It uses
// the REST slice's stores and helpers (smAddVersionPayload, smSetVersionState,
// the "latest" resolution), so GetSecretVersion and ListSecretVersions carry
// metadata only and AccessSecretVersion returns the payload of an ENABLED
// version. Names are full resource paths, used verbatim.

type secretManagerGRPC struct {
	smpb.UnimplementedSecretManagerServiceServer
}

func registerSecretManagerGRPC(gs *grpc.Server) {
	smpb.RegisterSecretManagerServiceServer(gs, &secretManagerGRPC{})
}

// smNormalizeName strips a doubled leading "projects/" from a full path.
func smNormalizeName(name string) string {
	if strings.HasPrefix(name, "projects/projects/") {
		return strings.TrimPrefix(name, "projects/")
	}
	return name
}

// smSecretParent is the {parent}/secrets prefix implied by a `parent` field
// (projects/{p} or projects/{p}/locations/{l}); it is the key prefix the
// smSecrets store uses.
func smSecretParent(parent string) string {
	return strings.TrimSuffix(parent, "/") + "/secrets"
}

func smVersionPrefix(secretName string) string {
	return secretName + "/versions/"
}

func smVersionNumber(name string) (int, bool) {
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(name[i+1:])
	return n, err == nil
}

// smPageOffset decodes a base64 numeric page token; empty or invalid starts
// at the beginning.
func smPageOffset(token string) int {
	if token == "" {
		return 0
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// smPageBounds returns the end index and the next-page token (base64 of the
// offset) for a page starting at `start`, given page size and total.
func smPageBounds(start, pageSize, total int) (int, string) {
	end := total
	if pageSize > 0 && start+pageSize < total {
		end = start + pageSize
	}
	if end >= total {
		return end, ""
	}
	return end, base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(end)))
}

func smStateToProto(s string) smpb.SecretVersion_State {
	switch s {
	case "ENABLED":
		return smpb.SecretVersion_ENABLED
	case "DISABLED":
		return smpb.SecretVersion_DISABLED
	case "DESTROYED":
		return smpb.SecretVersion_DESTROYED
	default:
		return smpb.SecretVersion_STATE_UNSPECIFIED
	}
}

// smParseRFC3339 returns nil for an empty string; the proto field is optional.
func smParseRFC3339(s string) *timestamppb.Timestamp {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return timestamppb.New(t)
}

// smReplicationToProto converts the stored replication map, which echoes what
// the client supplied; a missing one is nil.
func smReplicationToProto(rep map[string]any) *smpb.Replication {
	if len(rep) == 0 {
		return nil
	}
	out := &smpb.Replication{}
	if _, ok := rep["automatic"]; ok {
		out.Replication = &smpb.Replication_Automatic_{Automatic: &smpb.Replication_Automatic{}}
		return out
	}
	if um, ok := rep["userManaged"].(map[string]any); ok {
		umProto := &smpb.Replication_UserManaged_{UserManaged: &smpb.Replication_UserManaged{}}
		if reps, ok := um["replicas"].([]any); ok {
			for _, r := range reps {
				rm, ok := r.(map[string]any)
				if !ok {
					continue
				}
				replica := &smpb.Replication_UserManaged_Replica{}
				if loc, ok := rm["location"].(string); ok {
					replica.Location = loc
				}
				umProto.UserManaged.Replicas = append(umProto.UserManaged.Replicas, replica)
			}
		}
		out.Replication = umProto
	}
	return out
}

// smReplicationFromProto converts a proto Replication to the stored map; nil
// is automatic replication, CreateSecret's default.
func smReplicationFromProto(rep *smpb.Replication) map[string]any {
	if rep == nil {
		return map[string]any{"automatic": map[string]any{}}
	}
	switch r := rep.GetReplication().(type) {
	case *smpb.Replication_Automatic_:
		return map[string]any{"automatic": map[string]any{}}
	case *smpb.Replication_UserManaged_:
		um := map[string]any{}
		var reps []any
		for _, replica := range r.UserManaged.GetReplicas() {
			m := map[string]any{"location": replica.GetLocation()}
			reps = append(reps, m)
		}
		um["replicas"] = reps
		return map[string]any{"userManaged": um}
	default:
		return map[string]any{"automatic": map[string]any{}}
	}
}

func smTopicsToProto(raw json.RawMessage) []*smpb.Topic {
	if len(raw) == 0 {
		return nil
	}
	var arr []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]*smpb.Topic, 0, len(arr))
	for _, t := range arr {
		out = append(out, &smpb.Topic{Name: t.Name})
	}
	return out
}

func smTopicsFromProto(topics []*smpb.Topic) json.RawMessage {
	if len(topics) == 0 {
		return nil
	}
	type wireTopic struct {
		Name string `json:"name"`
	}
	arr := make([]wireTopic, 0, len(topics))
	for _, t := range topics {
		arr = append(arr, wireTopic{Name: t.GetName()})
	}
	b, err := json.Marshal(arr)
	if err != nil {
		return nil
	}
	return b
}

func smRotationToProto(raw json.RawMessage) *smpb.Rotation {
	if len(raw) == 0 {
		return nil
	}
	var r struct {
		NextRotationTime string `json:"nextRotationTime"`
		RotationPeriod   string `json:"rotationPeriod"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	out := &smpb.Rotation{}
	if r.NextRotationTime != "" {
		out.NextRotationTime = smParseRFC3339(r.NextRotationTime)
	}
	if r.RotationPeriod != "" {
		if d, err := time.ParseDuration(r.RotationPeriod); err == nil {
			out.RotationPeriod = durationpb.New(d)
		}
	}
	return out
}

func smRotationFromProto(r *smpb.Rotation) json.RawMessage {
	if r == nil {
		return nil
	}
	out := struct {
		NextRotationTime string `json:"nextRotationTime,omitempty"`
		RotationPeriod   string `json:"rotationPeriod,omitempty"`
	}{}
	if r.GetNextRotationTime() != nil {
		out.NextRotationTime = r.GetNextRotationTime().AsTime().UTC().Format(time.RFC3339)
	}
	if r.GetRotationPeriod() != nil {
		out.RotationPeriod = strconv.FormatInt(int64(r.GetRotationPeriod().AsDuration().Seconds()), 10) + "s"
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

// smSecretToProto fills the expiration oneof (expire_time or ttl) from
// whichever the store carries.
func smSecretToProto(s Secret) *smpb.Secret {
	out := &smpb.Secret{
		Name:           s.Name,
		CreateTime:     smParseRFC3339(s.CreateTime),
		Labels:         s.Labels,
		Annotations:    s.Annotations,
		Replication:    smReplicationToProto(s.Replication),
		Topics:         smTopicsToProto(s.Topics),
		Rotation:       smRotationToProto(s.Rotation),
		VersionAliases: smVersionAliasesToProto(s.VersionAliases),
		SecretType:     smpb.Secret_SecretType(smpb.Secret_SecretType_value[s.SecretType]),
	}
	if s.ExpireTime != "" {
		out.Expiration = &smpb.Secret_ExpireTime{ExpireTime: smParseRFC3339(s.ExpireTime)}
	} else if s.Ttl != "" {
		if d, err := time.ParseDuration(s.Ttl); err == nil {
			out.Expiration = &smpb.Secret_Ttl{Ttl: durationpb.New(d)}
		}
	}
	return out
}

// smSecretFromProto applies a proto Secret's mutable fields; the caller sets
// Name, CreateTime and Etag.
func smSecretFromProto(name string, s *smpb.Secret) Secret {
	out := Secret{
		Name:        name,
		Labels:      s.GetLabels(),
		Annotations: s.GetAnnotations(),
		Replication: smReplicationFromProto(s.GetReplication()),
		Topics:      smTopicsFromProto(s.GetTopics()),
		Rotation:    smRotationFromProto(s.GetRotation()),
		SecretType:  s.GetSecretType().String(),
	}
	switch exp := s.GetExpiration().(type) {
	case *smpb.Secret_ExpireTime:
		if exp.ExpireTime != nil {
			out.ExpireTime = exp.ExpireTime.AsTime().UTC().Format(time.RFC3339)
		}
	case *smpb.Secret_Ttl:
		if exp.Ttl != nil {
			out.Ttl = strconv.FormatInt(int64(exp.Ttl.AsDuration().Seconds()), 10) + "s"
		}
	}
	return out
}

// smVersionAliasesToProto converts alias→version strings to the proto's
// alias→int64.
func smVersionAliasesToProto(in map[string]string) map[string]int64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int64, len(in))
	for k, v := range in {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			out[k] = n
		}
	}
	return out
}

func smVersionAliasesFromProto(in map[string]int64) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = strconv.FormatInt(v, 10)
	}
	return out
}

// smVersionToProto never attaches the payload, which only AccessSecretVersion
// returns.
func smVersionToProto(v SecretVersion) *smpb.SecretVersion {
	out := &smpb.SecretVersion{
		Name:                           v.Name,
		CreateTime:                     smParseRFC3339(v.CreateTime),
		State:                          smStateToProto(v.State),
		ClientSpecifiedPayloadChecksum: v.ClientSpecifiedPayloadChecksum,
	}
	if v.State == "DESTROYED" {
		out.DestroyTime = smParseRFC3339(v.CreateTime)
	}
	return out
}

func (s *secretManagerGRPC) ListSecrets(ctx context.Context, req *smpb.ListSecretsRequest) (*smpb.ListSecretsResponse, error) {
	parent := smNormalizeName(req.GetParent())
	prefix := smSecretParent(parent) + "/"
	var all []*smpb.Secret
	for _, sc := range smSecrets.List() {
		if strings.HasPrefix(sc.Name, prefix) {
			all = append(all, smSecretToProto(sc))
		}
	}
	// Secret Manager lists newest first by CreateTime.
	sort.Slice(all, func(i, j int) bool {
		ti := all[i].GetCreateTime().AsTime()
		tj := all[j].GetCreateTime().AsTime()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return all[i].GetName() > all[j].GetName()
	})
	start := smPageOffset(req.GetPageToken())
	end, next := smPageBounds(start, int(req.GetPageSize()), len(all))
	return &smpb.ListSecretsResponse{
		Secrets:       all[start:end],
		NextPageToken: next,
		TotalSize:     int32(len(all)),
	}, nil
}

func (s *secretManagerGRPC) CreateSecret(ctx context.Context, req *smpb.CreateSecretRequest) (*smpb.Secret, error) {
	parent := smNormalizeName(req.GetParent())
	if req.GetSecretId() == "" {
		return nil, status.Error(codes.InvalidArgument, "secret_id is required")
	}
	name := smSecretParent(parent) + "/" + req.GetSecretId()
	if _, exists := smSecrets.Get(name); exists {
		return nil, status.Errorf(codes.AlreadyExists, "Secret %s already exists", name)
	}
	sc := smSecretFromProto(name, req.GetSecret())
	sc.CreateTime = time.Now().UTC().Format(time.RFC3339)
	smSecrets.Put(name, sc)
	smVersionSeq.Put(name, smSeqRecord{Next: 0})
	return smSecretToProto(sc), nil
}

func (s *secretManagerGRPC) GetSecret(ctx context.Context, req *smpb.GetSecretRequest) (*smpb.Secret, error) {
	name := smNormalizeName(req.GetName())
	sc, ok := smSecrets.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Secret %s not found", name)
	}
	return smSecretToProto(sc), nil
}

func (s *secretManagerGRPC) UpdateSecret(ctx context.Context, req *smpb.UpdateSecretRequest) (*smpb.Secret, error) {
	protoSecret := req.GetSecret()
	if protoSecret == nil {
		return nil, status.Error(codes.InvalidArgument, "secret is required")
	}
	name := smNormalizeName(protoSecret.GetName())
	sc, ok := smSecrets.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Secret %s not found", name)
	}
	mask := req.GetUpdateMask()
	if mask == nil || len(mask.GetPaths()) == 0 {
		return smSecretToProto(sc), nil
	}
	for _, field := range mask.GetPaths() {
		switch field {
		case "labels":
			sc.Labels = protoSecret.GetLabels()
		case "annotations":
			sc.Annotations = protoSecret.GetAnnotations()
		case "replication":
			// Secret Manager rejects replication changes after creation; apply one only
			// when the mask names it.
			sc.Replication = smReplicationFromProto(protoSecret.GetReplication())
		case "topics":
			sc.Topics = smTopicsFromProto(protoSecret.GetTopics())
		case "rotation":
			sc.Rotation = smRotationFromProto(protoSecret.GetRotation())
		case "expire_time":
			if et := protoSecret.GetExpireTime(); et != nil {
				sc.ExpireTime = et.AsTime().UTC().Format(time.RFC3339)
			}
		case "ttl":
			if ttl := protoSecret.GetTtl(); ttl != nil {
				sc.Ttl = strconv.FormatInt(int64(ttl.AsDuration().Seconds()), 10) + "s"
			}
		case "version_aliases":
			sc.VersionAliases = smVersionAliasesFromProto(protoSecret.GetVersionAliases())
		default:
			return nil, status.Errorf(codes.InvalidArgument, "unsupported update_mask path %q", field)
		}
	}
	smSecrets.Put(name, sc)
	return smSecretToProto(sc), nil
}

func (s *secretManagerGRPC) DeleteSecret(ctx context.Context, req *smpb.DeleteSecretRequest) (*emptypb.Empty, error) {
	name := smNormalizeName(req.GetName())
	if !smSecrets.Delete(name) {
		return nil, status.Errorf(codes.NotFound, "Secret %s not found", name)
	}
	smVersionSeq.Delete(name)
	smManagedRotation.Delete(name)
	gcpResourceIAMStore().Delete(name)
	prefix := smVersionPrefix(name)
	for _, v := range smSecretVersions.List() {
		if strings.HasPrefix(v.Name, prefix) {
			smSecretVersions.Delete(v.Name)
			smSecretPayloads.Delete(v.Name)
		}
	}
	return &emptypb.Empty{}, nil
}

func (s *secretManagerGRPC) ListSecretVersions(ctx context.Context, req *smpb.ListSecretVersionsRequest) (*smpb.ListSecretVersionsResponse, error) {
	parent := smNormalizeName(req.GetParent())
	if _, ok := smSecrets.Get(parent); !ok {
		return nil, status.Errorf(codes.NotFound, "Secret %s not found", parent)
	}
	prefix := smVersionPrefix(parent)
	var all []*smpb.SecretVersion
	for _, v := range smSecretVersions.List() {
		if strings.HasPrefix(v.Name, prefix) {
			all = append(all, smVersionToProto(v))
		}
	}
	// Secret Manager lists versions newest first.
	sort.Slice(all, func(i, j int) bool {
		na, _ := smVersionNumber(all[i].GetName())
		nb, _ := smVersionNumber(all[j].GetName())
		if na != nb {
			return na > nb
		}
		return all[i].GetName() > all[j].GetName()
	})
	start := smPageOffset(req.GetPageToken())
	end, next := smPageBounds(start, int(req.GetPageSize()), len(all))
	return &smpb.ListSecretVersionsResponse{
		Versions:      all[start:end],
		NextPageToken: next,
		TotalSize:     int32(len(all)),
	}, nil
}

func (s *secretManagerGRPC) GetSecretVersion(ctx context.Context, req *smpb.GetSecretVersionRequest) (*smpb.SecretVersion, error) {
	name := smNormalizeName(req.GetName())
	versionName, err := smResolveVersionName(name)
	if err != nil {
		return nil, err
	}
	v, ok := smSecretVersions.Get(versionName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "SecretVersion %s not found", versionName)
	}
	return smVersionToProto(v), nil
}

func (s *secretManagerGRPC) AddSecretVersion(ctx context.Context, req *smpb.AddSecretVersionRequest) (*smpb.SecretVersion, error) {
	parent := smNormalizeName(req.GetParent())
	secret, ok := smSecrets.Get(parent)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Secret %s not found", parent)
	}
	if secret.SecretType == "CLOUD_SQL_DB_CREDENTIALS" {
		return nil, status.Error(codes.FailedPrecondition, "versions for CLOUD_SQL_DB_CREDENTIALS secrets are managed by Secret Manager")
	}
	payload := req.GetPayload()
	if payload == nil {
		return nil, status.Error(codes.InvalidArgument, "payload is required")
	}
	ver, err := smAddVersionPayload(parent, payload.GetData(), false, 0)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return smVersionToProto(ver), nil
}

func (s *secretManagerGRPC) EnableManagedRotation(ctx context.Context, req *smpb.EnableManagedRotationRequest) (*smpb.SecretVersion, error) {
	parent := smNormalizeName(req.GetParent())
	secret, ok := smSecrets.Get(parent)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Secret %s not found", parent)
	}
	if secret.SecretType != "CLOUD_SQL_DB_CREDENTIALS" {
		return nil, status.Error(codes.FailedPrecondition, "managed rotation requires a CLOUD_SQL_DB_CREDENTIALS secret")
	}
	if _, exists := smManagedRotation.Get(parent); exists {
		return nil, status.Error(codes.FailedPrecondition, "managed rotation has already been enabled")
	}
	credentials := req.GetCloudSqlSingleUserCredentials()
	if credentials == nil || credentials.GetInstanceId() == "" || credentials.GetUsername() == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id and username are required")
	}
	project := strings.Split(parent, "/")[1]
	user, found := firstSQLUser(project, credentials.GetInstanceId(), credentials.GetUsername(), "")
	if !found {
		return nil, status.Errorf(codes.NotFound, "Cloud SQL user %s on instance %s not found", credentials.GetUsername(), credentials.GetInstanceId())
	}
	record := smManagedRotationRecord{
		Project: project, InstanceID: credentials.GetInstanceId(),
		Username: credentials.GetUsername(), UserHost: user.Host,
	}
	password := credentials.GetPassword()
	if password == "" {
		password = smGeneratedPassword()
	}
	version, err := smApplyManagedRotation(parent, record, password)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	smManagedRotation.Put(parent, record)
	return smVersionToProto(version), nil
}

func (s *secretManagerGRPC) RotateSecret(ctx context.Context, req *smpb.RotateSecretRequest) (*smpb.SecretVersion, error) {
	parent := smNormalizeName(req.GetParent())
	record, ok := smManagedRotation.Get(parent)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "managed rotation is not enabled")
	}
	version, err := smApplyManagedRotation(parent, record, smGeneratedPassword())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return smVersionToProto(version), nil
}

func (s *secretManagerGRPC) AccessSecretVersion(ctx context.Context, req *smpb.AccessSecretVersionRequest) (*smpb.AccessSecretVersionResponse, error) {
	name := smNormalizeName(req.GetName())
	idx := strings.LastIndex(name, "/versions/")
	if idx < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "name %q is not a SecretVersion", name)
	}
	secretName := name[:idx]
	versionID := name[idx+len("/versions/"):]
	rec, resolvedID, err := accessSecretPayloadResolvedForSecret(secretName, versionID)
	if err != nil {
		if errors.Is(err, errSecretVersionNotEnabled) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &smpb.AccessSecretVersionResponse{
		Name: fmt.Sprintf("%s/versions/%s", secretName, resolvedID),
		Payload: &smpb.SecretPayload{
			Data:       rec.Data,
			DataCrc32C: &rec.DataCrc32c,
		},
	}, nil
}

func (s *secretManagerGRPC) DisableSecretVersion(ctx context.Context, req *smpb.DisableSecretVersionRequest) (*smpb.SecretVersion, error) {
	return smTransitionVersion(smNormalizeName(req.GetName()), "disable")
}

func (s *secretManagerGRPC) EnableSecretVersion(ctx context.Context, req *smpb.EnableSecretVersionRequest) (*smpb.SecretVersion, error) {
	return smTransitionVersion(smNormalizeName(req.GetName()), "enable")
}

func (s *secretManagerGRPC) DestroySecretVersion(ctx context.Context, req *smpb.DestroySecretVersionRequest) (*smpb.SecretVersion, error) {
	return smTransitionVersion(smNormalizeName(req.GetName()), "destroy")
}

// smTransitionVersion resolves a version name, "latest" included, and applies
// the state transition.
func smTransitionVersion(name, action string) (*smpb.SecretVersion, error) {
	versionName, err := smResolveVersionName(name)
	if err != nil {
		return nil, err
	}
	ver, ok := smSecretVersions.Get(versionName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "SecretVersion %s not found", versionName)
	}
	updated, handled, err := smSetVersionState(versionName, ver, action)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !handled {
		return nil, status.Errorf(codes.InvalidArgument, "unknown version action %q", action)
	}
	return smVersionToProto(updated), nil
}

// smResolveVersionName expands "latest" to the highest version number;
// NotFound when the secret has none.
func smResolveVersionName(name string) (string, error) {
	idx := strings.LastIndex(name, "/versions/")
	if idx < 0 {
		return "", status.Errorf(codes.InvalidArgument, "name %q is not a SecretVersion", name)
	}
	secretName := name[:idx]
	versionID := name[idx+len("/versions/"):]
	if versionID != "latest" {
		return name, nil
	}
	resolved, ok := resolveLatestVersionIDForSecret(secretName)
	if !ok {
		return "", status.Errorf(codes.NotFound, "Secret %s has no versions", secretName)
	}
	return fmt.Sprintf("%s/versions/%s", secretName, resolved), nil
}

func (s *secretManagerGRPC) SetIamPolicy(ctx context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	resource := smNormalizeName(req.GetResource())
	if _, ok := smSecrets.Get(resource); !ok {
		return nil, status.Errorf(codes.NotFound, "Secret %s not found", resource)
	}
	policy := req.GetPolicy()
	if policy == nil {
		return nil, status.Error(codes.InvalidArgument, "policy is required")
	}
	gcpIAMPolicyWrites.Lock()
	defer gcpIAMPolicyWrites.Unlock()
	store := gcpResourceIAMStore()
	current, present := store.Get(resource)
	reqEtag := string(req.GetPolicy().GetEtag())
	if reqEtag != "" && (!present || reqEtag != current.Etag) {
		return nil, status.Error(codes.Aborted, "There were concurrent policy changes. Please retry the whole read-modify-write with exponential backoff.")
	}
	bindings := smIamBindingsFromProto(policy.GetBindings())
	stored := IAMPolicy{Bindings: bindings, Etag: gcpPolicyETag()}
	if v := policy.GetVersion(); v != 0 {
		stored.Version = int(v)
	}
	store.Put(resource, stored)
	return smPolicyToProto(stored), nil
}

func (s *secretManagerGRPC) GetIamPolicy(ctx context.Context, req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	resource := smNormalizeName(req.GetResource())
	if _, ok := smSecrets.Get(resource); !ok {
		return nil, status.Errorf(codes.NotFound, "Secret %s not found", resource)
	}
	gcpIAMPolicyWrites.Lock()
	defer gcpIAMPolicyWrites.Unlock()
	store := gcpResourceIAMStore()
	policy, ok := store.Get(resource)
	if !ok {
		policy = IAMPolicy{Bindings: []IAMBinding{}, Etag: gcpPolicyETag(), Version: 1}
		store.Put(resource, policy)
	}
	return smPolicyToProto(policy), nil
}

func (s *secretManagerGRPC) TestIamPermissions(ctx context.Context, req *iampb.TestIamPermissionsRequest) (*iampb.TestIamPermissionsResponse, error) {
	resource := smNormalizeName(req.GetResource())
	// TestIamPermissions on a missing secret answers an empty set, not NOT_FOUND.
	if _, ok := smSecrets.Get(resource); !ok {
		return &iampb.TestIamPermissionsResponse{}, nil
	}
	policy, _ := gcpResourceIAMStore().Get(resource)
	return &iampb.TestIamPermissionsResponse{
		Permissions: gcpAnswerTestIamPermissionsForContext(ctx, resource, policy, req.GetPermissions(), gcpIAMResourceNamed(resource))}, nil
}

// smPolicyToProto converts the REST-store IAMPolicy into the proto Policy,
// including conditions (CEL Expr) preserved by JSON round-trip.
func smPolicyToProto(p IAMPolicy) *iampb.Policy {
	out := &iampb.Policy{
		Version: int32(p.Version),
		Etag:    []byte(p.Etag),
	}
	for _, b := range p.Bindings {
		binding := &iampb.Binding{
			Role:    b.Role,
			Members: append([]string(nil), b.Members...),
		}
		if len(b.Condition) > 0 {
			var e exprpb.Expr
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(b.Condition, &e); err == nil {
				binding.Condition = &e
			}
		}
		out.Bindings = append(out.Bindings, binding)
	}
	return out
}

// smIamBindingsFromProto converts proto Bindings to the REST-store shape,
// preserving conditions as JSON-serialized CEL Expr payloads.
func smIamBindingsFromProto(bindings []*iampb.Binding) []IAMBinding {
	out := make([]IAMBinding, 0, len(bindings))
	for _, b := range bindings {
		entry := IAMBinding{
			Role:    b.GetRole(),
			Members: append([]string(nil), b.GetMembers()...),
		}
		if cond := b.GetCondition(); cond != nil {
			if raw, err := protojson.Marshal(cond); err == nil {
				entry.Condition = raw
			}
		}
		out = append(out, entry)
	}
	return out
}

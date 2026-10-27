package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// randIntn returns a uniform integer in [0, n) from crypto/rand.
func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int(binary.BigEndian.Uint64(b[:])>>1) % n
}

// SMSecret keeps its full version history so ListSecretVersionIds and
// version-selected GetSecretValue round-trip. The newest version carries
// AWSCURRENT and the one before it AWSPREVIOUS. VersionId mirrors the AWSCURRENT
// version for handlers that read it; Versions is the canonical view.
type SMSecret struct {
	ARN              string            `json:"ARN"`
	Name             string            `json:"Name"`
	Region           string            `json:"Region,omitempty"`
	PrimaryRegion    string            `json:"PrimaryRegion,omitempty"`
	Description      string            `json:"Description,omitempty"`
	KmsKeyId         string            `json:"KmsKeyId,omitempty"`
	CreatedDate      float64           `json:"CreatedDate,omitempty"`
	LastChangedDate  float64           `json:"LastChangedDate,omitempty"`
	LastAccessedDate float64           `json:"LastAccessedDate,omitempty"`
	DeletedDate      float64           `json:"DeletedDate,omitempty"`
	VersionId        string            `json:"VersionId"` // mirror of the AWSCURRENT version's ID
	SecretString     string            `json:"SecretString,omitempty"`
	SecretBinary     []byte            `json:"SecretBinary,omitempty"`
	Tags             []SMTag           `json:"Tags,omitempty"`
	Versions         []SMSecretVersion `json:"Versions,omitempty"`
	// ResourcePolicy is the raw policy PutResourcePolicy attached, mirrored into
	// the IAM resource-policy store.
	ResourcePolicy string `json:"ResourcePolicy,omitempty"`
	// RotateSecret with rules turns rotation on and CancelRotateSecret turns it
	// off; DescribeSecret reports the rules.
	RotationEnabled   bool            `json:"RotationEnabled,omitempty"`
	RotationLambdaARN string          `json:"RotationLambdaARN,omitempty"`
	RotationRules     SMRotationRules `json:"RotationRules,omitempty"`
	NextRotationDate  float64         `json:"NextRotationDate,omitempty"`
	LastRotatedDate   float64         `json:"LastRotatedDate,omitempty"`
	// Replicas is the replication-status list ReplicateSecretToRegions and
	// RemoveRegionsFromReplication manage.
	Replicas []SMReplicationStatus `json:"Replicas,omitempty"`
}

type SMRotationRules struct {
	AutomaticallyAfterDays int64  `json:"AutomaticallyAfterDays,omitempty"`
	Duration               string `json:"Duration,omitempty"`
	ScheduleExpression     string `json:"ScheduleExpression,omitempty"`
}

type SMReplicationStatus struct {
	Region           string  `json:"Region,omitempty"`
	KmsKeyId         string  `json:"KmsKeyId,omitempty"`
	Status           string  `json:"Status,omitempty"`
	StatusMessage    string  `json:"StatusMessage,omitempty"`
	LastAccessedDate float64 `json:"LastAccessedDate,omitempty"`
}

type SMReplicaRegionRequest struct {
	Region   string `json:"Region"`
	KmsKeyId string `json:"KmsKeyId"`
}

type SMSecretVersion struct {
	VersionId    string   `json:"VersionId"`
	CreatedDate  float64  `json:"CreatedDate"`
	SecretString string   `json:"SecretString,omitempty"`
	SecretBinary []byte   `json:"SecretBinary,omitempty"`
	Stages       []string `json:"VersionStages,omitempty"`
}

// addNewVersion appends a version, makes it AWSCURRENT, demotes the prior
// AWSCURRENT to AWSPREVIOUS, clears AWSPREVIOUS from older ones, and returns the
// new ID. CreateSecret, PutSecretValue and RotateSecret create a version;
// UpdateSecret does only when SecretString or SecretBinary changes.
func (s *SMSecret) addNewVersion(secretString string, secretBinary []byte) string {
	now := float64(time.Now().Unix())
	newID := sim.NewUUID()

	// Demote the prior AWSCURRENT to AWSPREVIOUS; older versions keep no stage once
	// they leave the recent two.
	for i := range s.Versions {
		newStages := s.Versions[i].Stages[:0]
		for _, stg := range s.Versions[i].Stages {
			switch stg {
			case "AWSCURRENT":
				newStages = append(newStages, "AWSPREVIOUS")
			case "AWSPREVIOUS":
			default:
				newStages = append(newStages, stg)
			}
		}
		s.Versions[i].Stages = newStages
	}

	s.Versions = append(s.Versions, SMSecretVersion{
		VersionId:    newID,
		CreatedDate:  now,
		SecretString: secretString,
		SecretBinary: secretBinary,
		Stages:       []string{"AWSCURRENT"},
	})
	s.VersionId = newID
	return newID
}

// versionByIDOrStage selects a version by VersionId or VersionStage;
// GetSecretValue prefers VersionId when both are set.
func (s *SMSecret) versionByIDOrStage(versionID, stage string) (SMSecretVersion, bool) {
	if versionID != "" {
		for _, v := range s.Versions {
			if v.VersionId == versionID {
				return v, true
			}
		}
		return SMSecretVersion{}, false
	}
	if stage == "" {
		stage = "AWSCURRENT"
	}
	for _, v := range s.Versions {
		for _, stg := range v.Stages {
			if stg == stage {
				return v, true
			}
		}
	}
	return SMSecretVersion{}, false
}

// SMTag is the Secrets Manager tag shape (Key/Value).
type SMTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

var smSecrets sim.Store[SMSecret]

func smArnForRegion(name, region string) string {
	// Secrets Manager appends a random 6-character suffix to the secret name in its
	// ARN; the simulator derives the suffix deterministically.
	return fmt.Sprintf("arn:aws:secretsmanager:%s:%s:secret:%s-%s",
		region, awsAccountID(), name, sim.NewUUID()[:6])
}

func registerSecretsManager(r *AWSRouter, srv *sim.Server) {
	smSecrets = sim.MakeStore[SMSecret](srv.DB(), "sm_secrets")
	smRecoverReplicas()

	r.Register("secretsmanager.CreateSecret", handleSMCreateSecret)
	r.Register("secretsmanager.GetSecretValue", handleSMGetSecretValue)
	r.Register("secretsmanager.DescribeSecret", handleSMDescribeSecret)
	r.Register("secretsmanager.UpdateSecret", handleSMUpdateSecret)
	r.Register("secretsmanager.PutSecretValue", handleSMPutSecretValue)
	r.Register("secretsmanager.DeleteSecret", handleSMDeleteSecret)
	r.Register("secretsmanager.ListSecrets", handleSMListSecrets)
	r.Register("secretsmanager.ListSecretVersionIds", handleSMListSecretVersionIds)
	r.Register("secretsmanager.TagResource", handleSMTagResource)
	r.Register("secretsmanager.UntagResource", handleSMUntagResource)
	r.Register("secretsmanager.GetResourcePolicy", handleSMGetResourcePolicy)
	r.Register("secretsmanager.GetRandomPassword", handleSMGetRandomPassword)
	r.Register("secretsmanager.PutResourcePolicy", handleSMPutResourcePolicy)
	r.Register("secretsmanager.DeleteResourcePolicy", handleSMDeleteResourcePolicy)
	r.Register("secretsmanager.ValidateResourcePolicy", handleSMValidateResourcePolicy)
	r.Register("secretsmanager.RestoreSecret", handleSMRestoreSecret)
	r.Register("secretsmanager.RotateSecret", handleSMRotateSecret)
	r.Register("secretsmanager.CancelRotateSecret", handleSMCancelRotateSecret)
	r.Register("secretsmanager.BatchGetSecretValue", handleSMBatchGetSecretValue)
	r.Register("secretsmanager.UpdateSecretVersionStage", handleSMUpdateSecretVersionStage)
	r.Register("secretsmanager.ReplicateSecretToRegions", handleSMReplicateSecretToRegions)
	r.Register("secretsmanager.RemoveRegionsFromReplication", handleSMRemoveRegionsFromReplication)
	r.Register("secretsmanager.StopReplicationToReplica", handleSMStopReplicationToReplica)
}

func smReplicaARN(primaryARN, region string) string {
	parts := strings.Split(primaryARN, ":")
	if len(parts) > 3 {
		parts[3] = region
	}
	return strings.Join(parts, ":")
}

func smCloneSecret(secret SMSecret) SMSecret {
	encoded, err := json.Marshal(secret)
	if err != nil {
		return secret
	}
	var clone SMSecret
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return secret
	}
	return clone
}

func smSyncReplica(primary SMSecret, status SMReplicationStatus) {
	replica := smCloneSecret(primary)
	replica.Region = status.Region
	replica.PrimaryRegion = smSecretRegion(primary)
	replica.ARN = smReplicaARN(primary.ARN, status.Region)
	replica.KmsKeyId = status.KmsKeyId
	replica.Replicas = nil
	replica.LastAccessedDate = 0
	key := smStoreKey(status.Region, primary.Name)
	if existing, ok := smSecrets.Get(key); ok && existing.PrimaryRegion == replica.PrimaryRegion {
		replica.LastAccessedDate = existing.LastAccessedDate
	}
	smSecrets.Put(key, replica)
	if replica.ResourcePolicy != "" {
		iamPutResourcePolicy(replica.ARN, replica.ResourcePolicy)
	}
}

func smRecoverReplicas() {
	for _, primary := range smSecrets.List() {
		if primary.PrimaryRegion != "" {
			continue
		}
		changed := false
		for i := range primary.Replicas {
			smSyncReplica(primary, primary.Replicas[i])
			if primary.Replicas[i].Status != "InSync" || primary.Replicas[i].StatusMessage != "" {
				primary.Replicas[i].Status = "InSync"
				primary.Replicas[i].StatusMessage = ""
				changed = true
			}
		}
		if changed {
			smSecrets.Put(smStoreKey(smSecretRegion(primary), primary.Name), primary)
		}
	}
}

func smUpdate(key string, update func(*SMSecret)) {
	smSecrets.Update(key, update)
	secret, ok := smSecrets.Get(key)
	if !ok || secret.PrimaryRegion != "" {
		return
	}
	for _, replica := range secret.Replicas {
		if replica.Status == "InSync" {
			smSyncReplica(secret, replica)
		}
	}
}

// handleSMGetResourcePolicy returns the secret's {ARN, Name, ResourcePolicy}.
func handleSMGetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId string `json:"SecretId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	secret, ok := resolveSMSecretForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret: %s", req.SecretId)
		return
	}
	// Secrets Manager omits ResourcePolicy when no policy is attached; an empty
	// string makes terraform-provider-aws fail parsing it as JSON.
	resp := map[string]any{
		"ARN":  secret.ARN,
		"Name": secret.Name,
	}
	if secret.ResourcePolicy != "" {
		resp["ResourcePolicy"] = secret.ResourcePolicy
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// resolveSMSecret returns the stored secret a name or ARN names.
func smRequestRegion(r *http.Request) string {
	if region := iamRequestedRegion(r); region != "" {
		return region
	}
	return awsRegion()
}

func smStoreKey(region, name string) string {
	if region == awsRegion() {
		return name
	}
	return region + "\x00" + name
}

func smSecretRegion(secret SMSecret) string {
	if secret.Region != "" {
		return secret.Region
	}
	parts := strings.Split(secret.ARN, ":")
	if len(parts) > 3 && parts[3] != "" {
		return parts[3]
	}
	return awsRegion()
}

func resolveSMSecret(idOrArn string) (SMSecret, bool) {
	return resolveSMSecretInRegion(awsRegion(), idOrArn)
}

func resolveSMSecretForRequest(r *http.Request, idOrArn string) (SMSecret, bool) {
	return resolveSMSecretInRegion(smRequestRegion(r), idOrArn)
}

func resolveSMSecretInRegion(region, idOrArn string) (SMSecret, bool) {
	if secret, ok := smSecrets.Get(smStoreKey(region, idOrArn)); ok {
		return secret, true
	}
	return smSecretsByRegionalARN.Lookup(smSecrets, region+" "+idOrArn, func(s SMSecret) []string {
		return []string{smSecretRegion(s) + " " + s.ARN}
	})
}

// smSecretsByRegionalARN answers a secret by its Region and ARN; a replica
// shares its primary's ARN in another Region.
var smSecretsByRegionalARN sim.GenerationIndex[SMSecret]

func handleSMCreateSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                        string                   `json:"Name"`
		Description                 string                   `json:"Description"`
		KmsKeyId                    string                   `json:"KmsKeyId"`
		SecretString                string                   `json:"SecretString"`
		SecretBinary                []byte                   `json:"SecretBinary"`
		Tags                        []SMTag                  `json:"Tags"`
		AddReplicaRegions           []SMReplicaRegionRequest `json:"AddReplicaRegions"`
		ForceOverwriteReplicaSecret bool                     `json:"ForceOverwriteReplicaSecret"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "InvalidRequestException", "Name is required", http.StatusBadRequest)
		return
	}
	region := smRequestRegion(r)
	storeKey := smStoreKey(region, req.Name)
	if _, exists := smSecrets.Get(storeKey); exists {
		AWSErrorf(w, "ResourceExistsException", http.StatusBadRequest,
			"The operation failed because the secret %s already exists.", req.Name)
		return
	}
	seenReplicaRegions := map[string]bool{}
	for _, replica := range req.AddReplicaRegions {
		if replica.Region == "" || replica.Region == region {
			AWSError(w, "InvalidParameterException",
				"A replica Region must be different from the primary Region.",
				http.StatusBadRequest)
			return
		}
		if seenReplicaRegions[replica.Region] {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"Replica Region %s was specified more than once.", replica.Region)
			return
		}
		seenReplicaRegions[replica.Region] = true
		if _, exists := smSecrets.Get(smStoreKey(replica.Region, req.Name)); exists &&
			!req.ForceOverwriteReplicaSecret {
			AWSErrorf(w, "ResourceExistsException", http.StatusBadRequest,
				"The secret %s already exists in Region %s.", req.Name, replica.Region)
			return
		}
	}

	now := float64(time.Now().Unix())
	secret := SMSecret{
		ARN:             smArnForRegion(req.Name, region),
		Name:            req.Name,
		Region:          region,
		Description:     req.Description,
		KmsKeyId:        req.KmsKeyId,
		CreatedDate:     now,
		LastChangedDate: now,
		SecretString:    req.SecretString,
		SecretBinary:    req.SecretBinary,
		Tags:            req.Tags,
	}
	for _, replica := range req.AddReplicaRegions {
		secret.Replicas = append(secret.Replicas, SMReplicationStatus{
			Region:           replica.Region,
			KmsKeyId:         replica.KmsKeyId,
			Status:           "InProgress",
			LastAccessedDate: now,
		})
	}
	// Seed the first AWSCURRENT version. addNewVersion sets VersionId.
	secret.addNewVersion(req.SecretString, req.SecretBinary)
	smSecrets.Put(storeKey, secret)
	for index := range secret.Replicas {
		replica := secret.Replicas[index]
		if existing, exists := smSecrets.Get(smStoreKey(replica.Region, secret.Name)); exists {
			iamDeleteResourcePolicy(existing.ARN)
		}
		smSyncReplica(secret, replica)
		secret.Replicas[index].Status = "InSync"
		secret.Replicas[index].StatusMessage = ""
	}
	smSecrets.Put(storeKey, secret)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":       secret.ARN,
		"Name":      secret.Name,
		"VersionId": secret.VersionId,
	})
}

func handleSMGetSecretValue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId     string `json:"SecretId"`
		VersionId    string `json:"VersionId"`
		VersionStage string `json:"VersionStage"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	secret, ok := resolveSMSecretForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	if secret.DeletedDate != 0 {
		// Secrets Manager refuses to read a secret scheduled for deletion until it is
		// restored (InvalidRequestException).
		AWSErrorf(w, "InvalidRequestException", http.StatusBadRequest,
			"You can't perform this operation on the secret because it was marked for deletion.")
		return
	}
	version, found := secret.versionByIDOrStage(req.VersionId, req.VersionStage)
	if !found {
		// Secrets Manager answers a miss with this code and message.
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret value for VersionId: %s VersionStage: %s",
			req.VersionId, req.VersionStage)
		return
	}
	resp := map[string]any{
		"ARN":           secret.ARN,
		"Name":          secret.Name,
		"VersionId":     version.VersionId,
		"CreatedDate":   version.CreatedDate,
		"VersionStages": version.Stages,
	}
	if version.SecretString != "" {
		resp["SecretString"] = version.SecretString
	}
	if len(version.SecretBinary) > 0 {
		resp["SecretBinary"] = base64.StdEncoding.EncodeToString(version.SecretBinary)
	}

	// Secrets Manager records LastAccessedDate on read.
	smSecrets.Update(smStoreKey(smSecretRegion(secret), secret.Name), func(s *SMSecret) {
		s.LastAccessedDate = float64(time.Now().Unix())
	})

	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSMDescribeSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId string `json:"SecretId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	secret, ok := resolveSMSecretForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	versionStages := make(map[string][]string, len(secret.Versions))
	for _, v := range secret.Versions {
		if len(v.Stages) > 0 {
			versionStages[v.VersionId] = append([]string(nil), v.Stages...)
		}
	}
	resp := map[string]any{
		"ARN":                secret.ARN,
		"Name":               secret.Name,
		"Description":        secret.Description,
		"KmsKeyId":           secret.KmsKeyId,
		"CreatedDate":        secret.CreatedDate,
		"LastChangedDate":    secret.LastChangedDate,
		"LastAccessedDate":   secret.LastAccessedDate,
		"VersionIdsToStages": versionStages,
		"Tags":               secret.Tags,
		"RotationEnabled":    secret.RotationEnabled,
	}
	if secret.DeletedDate != 0 {
		resp["DeletedDate"] = secret.DeletedDate
	}
	if secret.RotationLambdaARN != "" {
		resp["RotationLambdaARN"] = secret.RotationLambdaARN
	}
	if secret.NextRotationDate != 0 {
		resp["NextRotationDate"] = secret.NextRotationDate
	}
	if secret.LastRotatedDate != 0 {
		resp["LastRotatedDate"] = secret.LastRotatedDate
	}
	if secret.RotationRules != (SMRotationRules{}) {
		resp["RotationRules"] = smRotationRulesToJSON(secret.RotationRules)
	}
	if len(secret.Replicas) > 0 {
		resp["ReplicationStatus"] = smReplicationStatusToJSON(secret.Replicas)
	}
	if secret.PrimaryRegion != "" {
		resp["PrimaryRegion"] = secret.PrimaryRegion
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// smRotationRulesToJSON renders RotationRules to the spec's member shape.
func smRotationRulesToJSON(rr SMRotationRules) map[string]any {
	m := map[string]any{}
	if rr.AutomaticallyAfterDays != 0 {
		m["AutomaticallyAfterDays"] = rr.AutomaticallyAfterDays
	}
	if rr.Duration != "" {
		m["Duration"] = rr.Duration
	}
	if rr.ScheduleExpression != "" {
		m["ScheduleExpression"] = rr.ScheduleExpression
	}
	return m
}

// smReplicationStatusToJSON renders the replication-status list to the
// spec's ReplicationStatusType member shape.
func smReplicationStatusToJSON(reps []SMReplicationStatus) []map[string]any {
	out := make([]map[string]any, 0, len(reps))
	for _, rep := range reps {
		entry := map[string]any{
			"Region": rep.Region,
			"Status": rep.Status,
		}
		if rep.KmsKeyId != "" {
			entry["KmsKeyId"] = rep.KmsKeyId
		}
		if rep.StatusMessage != "" {
			entry["StatusMessage"] = rep.StatusMessage
		}
		if rep.LastAccessedDate != 0 {
			entry["LastAccessedDate"] = rep.LastAccessedDate
		}
		out = append(out, entry)
	}
	return out
}

func handleSMUpdateSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId     string `json:"SecretId"`
		Description  string `json:"Description"`
		KmsKeyId     string `json:"KmsKeyId"`
		SecretString string `json:"SecretString"`
		SecretBinary []byte `json:"SecretBinary"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	now := float64(time.Now().Unix())
	smUpdate(name, func(s *SMSecret) {
		if req.Description != "" {
			s.Description = req.Description
		}
		if req.KmsKeyId != "" {
			s.KmsKeyId = req.KmsKeyId
		}
		// Secrets Manager mints a new version only when the secret bytes change.
		if req.SecretString != "" || len(req.SecretBinary) > 0 {
			s.SecretString = req.SecretString
			s.SecretBinary = req.SecretBinary
			s.addNewVersion(req.SecretString, req.SecretBinary)
		}
		s.LastChangedDate = now
	})
	updated, _ := smSecrets.Get(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":       updated.ARN,
		"Name":      updated.Name,
		"VersionId": updated.VersionId,
	})
}

func handleSMPutSecretValue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId     string `json:"SecretId"`
		SecretString string `json:"SecretString"`
		SecretBinary []byte `json:"SecretBinary"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	now := float64(time.Now().Unix())
	smUpdate(name, func(s *SMSecret) {
		if req.SecretString != "" {
			s.SecretString = req.SecretString
		}
		if len(req.SecretBinary) > 0 {
			s.SecretBinary = req.SecretBinary
		}
		s.addNewVersion(req.SecretString, req.SecretBinary)
		s.LastChangedDate = now
	})
	updated, _ := smSecrets.Get(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":           updated.ARN,
		"Name":          updated.Name,
		"VersionId":     updated.VersionId,
		"VersionStages": []string{"AWSCURRENT"},
	})
}

func handleSMDeleteSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId                   string `json:"SecretId"`
		RecoveryWindowInDays       int64  `json:"RecoveryWindowInDays"`
		ForceDeleteWithoutRecovery bool   `json:"ForceDeleteWithoutRecovery"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	secret, _ := smSecrets.Get(name)
	if secret.PrimaryRegion != "" {
		AWSError(w, "InvalidRequestException",
			"You can't delete a replica secret. Stop replication to this Region first.",
			http.StatusBadRequest)
		return
	}
	if len(secret.Replicas) > 0 {
		AWSError(w, "InvalidRequestException",
			"You can't delete a primary secret that is replicated to other Regions.",
			http.StatusBadRequest)
		return
	}
	now := time.Now()
	if req.ForceDeleteWithoutRecovery {
		// A forced delete removes the secret at once; DeletionDate is now.
		iamDeleteResourcePolicy(secret.ARN)
		smSecrets.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"ARN":          secret.ARN,
			"Name":         secret.Name,
			"DeletionDate": float64(now.Unix()),
		})
		return
	}
	// A scheduled delete keeps the secret, marked with DeletedDate, for a recovery
	// window (default 30 days, 7 to 30) until RestoreSecret clears it or the window
	// elapses; GetSecretValue refuses it meanwhile.
	window := req.RecoveryWindowInDays
	if window == 0 {
		window = 30
	}
	deletionDate := now.Add(time.Duration(window) * 24 * time.Hour)
	smUpdate(name, func(s *SMSecret) {
		s.DeletedDate = float64(now.Unix())
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":          secret.ARN,
		"Name":         secret.Name,
		"DeletionDate": float64(deletionDate.Unix()),
	})
}

// smFilter is one ListSecrets Filter (Key + Values). Documented keys:
// tag-key, tag-value, name, description, primary-region, all.
type smFilter struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values"`
}

// smSecretMatchesFilters reports whether a secret satisfies every filter
// (AND across filters; OR within a filter's Values).
func smSecretMatchesFilters(s SMSecret, filters []smFilter) bool {
	for _, f := range filters {
		if !smSecretMatchesFilter(s, f) {
			return false
		}
	}
	return true
}

func smSecretMatchesFilter(s SMSecret, f smFilter) bool {
	if len(f.Values) == 0 {
		return true // no values → no constraint
	}
	anyValue := func(test func(v string) bool) bool {
		for _, v := range f.Values {
			if test(v) {
				return true
			}
		}
		return false
	}
	switch f.Key {
	case "tag-key":
		return anyValue(func(v string) bool {
			for _, t := range s.Tags {
				if t.Key == v {
					return true
				}
			}
			return false
		})
	case "tag-value":
		return anyValue(func(v string) bool {
			for _, t := range s.Tags {
				if t.Value == v {
					return true
				}
			}
			return false
		})
	case "name":
		return anyValue(func(v string) bool { return strings.HasPrefix(s.Name, v) })
	case "description":
		return anyValue(func(v string) bool { return strings.Contains(s.Description, v) })
	case "all":
		return anyValue(func(v string) bool {
			if strings.Contains(s.Name, v) || strings.Contains(s.Description, v) {
				return true
			}
			for _, t := range s.Tags {
				if strings.Contains(t.Key, v) || strings.Contains(t.Value, v) {
					return true
				}
			}
			return false
		})
	case "primary-region":
		return anyValue(func(v string) bool {
			primaryRegion := s.PrimaryRegion
			if primaryRegion == "" {
				primaryRegion = smSecretRegion(s)
			}
			return primaryRegion == v
		})
	}
	return true // an unknown key excludes nothing
}

func handleSMListSecrets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string     `json:"NextToken"`
		MaxResults int        `json:"MaxResults"`
		Filters    []smFilter `json:"Filters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	region := smRequestRegion(r)
	allSecrets := smSecrets.List()
	all := make([]SMSecret, 0, len(allSecrets))
	for _, secret := range allSecrets {
		if smSecretRegion(secret) == region {
			all = append(all, secret)
		}
	}
	if all == nil {
		all = []SMSecret{}
	}
	// Filters AND across keys and OR within a key's Values, as Secrets Manager
	// applies them; a tag-key filter matching nothing returns [].
	if len(req.Filters) > 0 {
		filtered := make([]SMSecret, 0, len(all))
		for _, s := range all {
			if smSecretMatchesFilters(s, req.Filters) {
				filtered = append(filtered, s)
			}
		}
		all = filtered
	}
	page, next, pageOK := awsKeyPage(w, smBadToken, all, func(it SMSecret) string { return it.Name }, req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
	out := make([]map[string]any, 0, len(page))
	for _, s := range page {
		item := map[string]any{
			"ARN":              s.ARN,
			"Name":             s.Name,
			"Description":      s.Description,
			"KmsKeyId":         s.KmsKeyId,
			"CreatedDate":      s.CreatedDate,
			"LastChangedDate":  s.LastChangedDate,
			"LastAccessedDate": s.LastAccessedDate,
			"Tags":             s.Tags,
		}
		if s.PrimaryRegion != "" {
			item["PrimaryRegion"] = s.PrimaryRegion
		}
		out = append(out, item)
	}
	resp := map[string]any{"SecretList": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSMTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId string  `json:"SecretId"`
		Tags     []SMTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	smUpdate(name, func(s *SMSecret) {
		// Secrets Manager overwrites a tag by Key.
		merged := make(map[string]string)
		for _, t := range s.Tags {
			merged[t.Key] = t.Value
		}
		for _, t := range req.Tags {
			merged[t.Key] = t.Value
		}
		s.Tags = nil
		for k, v := range merged {
			s.Tags = append(s.Tags, SMTag{Key: k, Value: v})
		}
	})
	// TagResource answers 200 with an empty body. Set the awsJson1_1 Content-Type,
	// which net/http would otherwise default to text/plain.
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(http.StatusOK)
}

func handleSMUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId string   `json:"SecretId"`
		TagKeys  []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	keep := make(map[string]bool)
	for _, t := range req.TagKeys {
		keep[t] = true
	}
	smUpdate(name, func(s *SMSecret) {
		var filtered []SMTag
		for _, t := range s.Tags {
			if !keep[t.Key] {
				filtered = append(filtered, t)
			}
		}
		s.Tags = filtered
	})
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(http.StatusOK)
}

// resolveSecretKeyForRequest accepts either a secret name or full ARN and
// returns the region-scoped durable store key selected by the request's SigV4
// credential scope.
func resolveSecretKeyForRequest(r *http.Request, idOrArn string) (string, bool) {
	secret, ok := resolveSMSecretForRequest(r, idOrArn)
	if !ok {
		return "", false
	}
	return smStoreKey(smSecretRegion(secret), secret.Name), true
}

// handleSMListSecretVersionIds returns the secret's version history;
// IncludeDeprecated adds the versions with no stage. The simulator returns every
// version in one page.
func handleSMListSecretVersionIds(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId          string `json:"SecretId"`
		MaxResults        int32  `json:"MaxResults"`
		NextToken         string `json:"NextToken"`
		IncludeDeprecated bool   `json:"IncludeDeprecated"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	secret, ok := resolveSMSecretForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret: %s", req.SecretId)
		return
	}
	out := make([]map[string]any, 0, len(secret.Versions))
	for _, v := range secret.Versions {
		// A version with no stage is deprecated; Secrets Manager excludes it unless IncludeDeprecated is set.
		if !req.IncludeDeprecated && len(v.Stages) == 0 {
			continue
		}
		entry := map[string]any{
			"VersionId":   v.VersionId,
			"CreatedDate": v.CreatedDate,
		}
		if len(v.Stages) > 0 {
			entry["VersionStages"] = v.Stages
		}
		// Secrets Manager also reports LastAccessedDate per version; the simulator
		// tracks it per secret, so omit it.
		out = append(out, entry)
	}
	resp := map[string]any{
		"ARN":      secret.ARN,
		"Name":     secret.Name,
		"Versions": out,
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handleSMGetRandomPassword generates a password to the request's constraints,
// reading and writing no store.
func handleSMGetRandomPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PasswordLength          int64  `json:"PasswordLength"`
		ExcludeCharacters       string `json:"ExcludeCharacters"`
		ExcludeNumbers          bool   `json:"ExcludeNumbers"`
		ExcludePunctuation      bool   `json:"ExcludePunctuation"`
		ExcludeUppercase        bool   `json:"ExcludeUppercase"`
		ExcludeLowercase        bool   `json:"ExcludeLowercase"`
		IncludeSpace            bool   `json:"IncludeSpace"`
		RequireEachIncludedType bool   `json:"RequireEachIncludedType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PasswordLength == 0 {
		req.PasswordLength = 32 // Secrets Manager's default
	}
	if req.PasswordLength < 4 || req.PasswordLength > 4096 {
		AWSError(w, "InvalidParameterException",
			"PasswordLength must be between 4 and 4096", http.StatusBadRequest)
		return
	}
	pw := generateRandomPassword(int(req.PasswordLength), req.ExcludeCharacters,
		!req.ExcludeNumbers, !req.ExcludePunctuation,
		!req.ExcludeUppercase, !req.ExcludeLowercase,
		req.IncludeSpace, req.RequireEachIncludedType)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"RandomPassword": pw,
	})
}

// handleSMPutResourcePolicy attaches a resource-based policy and mirrors it into
// the IAM resource-policy store for the enforcement gate.
func handleSMPutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId          string `json:"SecretId"`
		ResourcePolicy    string `json:"ResourcePolicy"`
		BlockPublicPolicy bool   `json:"BlockPublicPolicy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	if req.ResourcePolicy == "" {
		AWSError(w, "InvalidParameterException",
			"ResourcePolicy must not be empty", http.StatusBadRequest)
		return
	}
	secret, _ := smSecrets.Get(name)
	smUpdate(name, func(s *SMSecret) {
		s.ResourcePolicy = req.ResourcePolicy
	})
	iamPutResourcePolicy(secret.ARN, req.ResourcePolicy)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":  secret.ARN,
		"Name": secret.Name,
	})
}

func handleSMDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId string `json:"SecretId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	secret, _ := smSecrets.Get(name)
	smUpdate(name, func(s *SMSecret) {
		s.ResourcePolicy = ""
	})
	iamDeleteResourcePolicy(secret.ARN)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":  secret.ARN,
		"Name": secret.Name,
	})
}

// handleSMValidateResourcePolicy reports PolicyValidationPassed and
// ValidationErrors. The simulator checks only that the document parses as JSON;
// a malformed one fails with a SYNTAX entry.
func handleSMValidateResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId       string `json:"SecretId"`
		ResourcePolicy string `json:"ResourcePolicy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourcePolicy == "" {
		AWSError(w, "InvalidParameterException",
			"ResourcePolicy must not be empty", http.StatusBadRequest)
		return
	}
	var probe any
	passed := json.Unmarshal([]byte(req.ResourcePolicy), &probe) == nil
	validationErrors := []map[string]any{}
	if !passed {
		validationErrors = append(validationErrors, map[string]any{
			"CheckName":    "SYNTAX",
			"ErrorMessage": "The resource policy is not valid JSON.",
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"PolicyValidationPassed": passed,
		"ValidationErrors":       validationErrors,
	})
}

// handleSMRestoreSecret clears DeletedDate, cancelling a scheduled deletion. It
// answers 200 whether or not a deletion was scheduled.
func handleSMRestoreSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId string `json:"SecretId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	secret, _ := smSecrets.Get(name)
	smUpdate(name, func(s *SMSecret) {
		s.DeletedDate = 0
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":  secret.ARN,
		"Name": secret.Name,
	})
}

// handleSMRotateSecret turns rotation on. With RotateImmediately (the default)
// it mints a new AWSCURRENT version and returns its VersionId.
func handleSMRotateSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId           string          `json:"SecretId"`
		ClientRequestToken string          `json:"ClientRequestToken"`
		RotationLambdaARN  string          `json:"RotationLambdaARN"`
		RotationRules      SMRotationRules `json:"RotationRules"`
		RotateImmediately  *bool           `json:"RotateImmediately"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	rotateNow := req.RotateImmediately == nil || *req.RotateImmediately
	now := time.Now()
	var newVersionID string
	smUpdate(name, func(s *SMSecret) {
		s.RotationEnabled = true
		if req.RotationLambdaARN != "" {
			s.RotationLambdaARN = req.RotationLambdaARN
		}
		if req.RotationRules != (SMRotationRules{}) {
			s.RotationRules = req.RotationRules
		}
		if rotateNow {
			// AWS rotates by invoking the rotation Lambda, which calls PutSecretValue; the
			// simulator re-stores the current value under a new version without invoking
			// it.
			newVersionID = s.addNewVersion(s.SecretString, s.SecretBinary)
			s.LastRotatedDate = float64(now.Unix())
			s.LastChangedDate = float64(now.Unix())
		}
		if s.RotationRules.AutomaticallyAfterDays > 0 {
			s.NextRotationDate = float64(now.Add(time.Duration(s.RotationRules.AutomaticallyAfterDays) * 24 * time.Hour).Unix())
		}
	})
	updated, _ := smSecrets.Get(name)
	if newVersionID == "" {
		newVersionID = updated.VersionId
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":       updated.ARN,
		"Name":      updated.Name,
		"VersionId": newVersionID,
	})
}

// handleSMCancelRotateSecret turns off scheduled rotation. Returns the secret's
// current AWSCURRENT VersionId.
func handleSMCancelRotateSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId string `json:"SecretId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	smUpdate(name, func(s *SMSecret) {
		s.RotationEnabled = false
		s.NextRotationDate = 0
	})
	updated, _ := smSecrets.Get(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":       updated.ARN,
		"Name":      updated.Name,
		"VersionId": updated.VersionId,
	})
}

// handleSMBatchGetSecretValue returns the AWSCURRENT value of each secret
// SecretIdList names or Filters match. A secret that cannot be found or read
// goes to Errors rather than failing the call, as in AWS.
func handleSMBatchGetSecretValue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretIdList []string   `json:"SecretIdList"`
		Filters      []smFilter `json:"Filters"`
		MaxResults   int        `json:"MaxResults"`
		NextToken    string     `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// Secrets Manager rejects SecretIdList combined with Filters.
	if len(req.SecretIdList) > 0 && len(req.Filters) > 0 {
		AWSError(w, "InvalidParameterException",
			"Either 'SecretIdList' or 'Filters' must be provided, but not both.", http.StatusBadRequest)
		return
	}
	now := float64(time.Now().Unix())
	secretValues := []map[string]any{}
	apiErrors := []map[string]any{}

	emit := func(secret SMSecret) {
		if secret.DeletedDate != 0 {
			apiErrors = append(apiErrors, map[string]any{
				"SecretId":  secret.ARN,
				"ErrorCode": "ResourceNotFoundException",
				"Message":   "Secrets Manager can't find the specified secret value because it was marked for deletion.",
			})
			return
		}
		version, found := secret.versionByIDOrStage("", "AWSCURRENT")
		if !found {
			apiErrors = append(apiErrors, map[string]any{
				"SecretId":  secret.ARN,
				"ErrorCode": "ResourceNotFoundException",
				"Message":   "Secrets Manager can't find the AWSCURRENT version of the specified secret.",
			})
			return
		}
		entry := map[string]any{
			"ARN":           secret.ARN,
			"Name":          secret.Name,
			"VersionId":     version.VersionId,
			"VersionStages": version.Stages,
			"CreatedDate":   version.CreatedDate,
		}
		if version.SecretString != "" {
			entry["SecretString"] = version.SecretString
		}
		if len(version.SecretBinary) > 0 {
			entry["SecretBinary"] = base64.StdEncoding.EncodeToString(version.SecretBinary)
		}
		secretValues = append(secretValues, entry)
		smUpdate(smStoreKey(smSecretRegion(secret), secret.Name), func(s *SMSecret) {
			s.LastAccessedDate = now
		})
	}

	if len(req.SecretIdList) > 0 {
		for _, id := range req.SecretIdList {
			secret, ok := resolveSMSecretForRequest(r, id)
			if !ok {
				apiErrors = append(apiErrors, map[string]any{
					"SecretId":  id,
					"ErrorCode": "ResourceNotFoundException",
					"Message":   "Secrets Manager can't find the specified secret.",
				})
				continue
			}
			emit(secret)
		}
	} else {
		allSecrets := smSecrets.List()
		all := make([]SMSecret, 0, len(allSecrets))
		for _, secret := range allSecrets {
			if smSecretRegion(secret) == smRequestRegion(r) {
				all = append(all, secret)
			}
		}
		sortBy(all, func(s SMSecret) string { return s.Name })
		for _, secret := range all {
			if len(req.Filters) > 0 && !smSecretMatchesFilters(secret, req.Filters) {
				continue
			}
			emit(secret)
		}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"SecretValues": secretValues,
		"Errors":       apiErrors,
	})
}

// handleSMUpdateSecretVersionStage attaches a staging label to MoveToVersionId
// and detaches it from RemoveFromVersionId, which is how a rotation repositions
// AWSCURRENT and AWSPREVIOUS.
func handleSMUpdateSecretVersionStage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId            string `json:"SecretId"`
		VersionStage        string `json:"VersionStage"`
		RemoveFromVersionId string `json:"RemoveFromVersionId"`
		MoveToVersionId     string `json:"MoveToVersionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	if req.VersionStage == "" {
		AWSError(w, "InvalidParameterException",
			"VersionStage must be provided.", http.StatusBadRequest)
		return
	}
	secret, _ := smSecrets.Get(name)
	versionExists := func(id string) bool {
		for _, v := range secret.Versions {
			if v.VersionId == id {
				return true
			}
		}
		return false
	}
	if req.MoveToVersionId != "" && !versionExists(req.MoveToVersionId) {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The version %s is not in the secret.", req.MoveToVersionId)
		return
	}
	if req.RemoveFromVersionId != "" && !versionExists(req.RemoveFromVersionId) {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The version %s is not in the secret.", req.RemoveFromVersionId)
		return
	}
	smUpdate(name, func(s *SMSecret) {
		for i := range s.Versions {
			v := &s.Versions[i]
			if req.RemoveFromVersionId != "" && v.VersionId == req.RemoveFromVersionId {
				var kept []string
				for _, stg := range v.Stages {
					if stg != req.VersionStage {
						kept = append(kept, stg)
					}
				}
				v.Stages = kept
			}
			if req.MoveToVersionId != "" && v.VersionId == req.MoveToVersionId {
				has := false
				for _, stg := range v.Stages {
					if stg == req.VersionStage {
						has = true
						break
					}
				}
				if !has {
					v.Stages = append(v.Stages, req.VersionStage)
				}
			}
		}
		// Keep the top-level VersionId mirror pointing at AWSCURRENT.
		for _, v := range s.Versions {
			for _, stg := range v.Stages {
				if stg == "AWSCURRENT" {
					s.VersionId = v.VersionId
				}
			}
		}
	})
	updated, _ := smSecrets.Get(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":  updated.ARN,
		"Name": updated.Name,
	})
}

// handleSMReplicateSecretToRegions durably records each requested replication,
// copies the complete secret into the destination Region, and only then marks
// that replica InSync.
func handleSMReplicateSecretToRegions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId                    string                   `json:"SecretId"`
		AddReplicaRegions           []SMReplicaRegionRequest `json:"AddReplicaRegions"`
		ForceOverwriteReplicaSecret bool                     `json:"ForceOverwriteReplicaSecret"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	primary, _ := smSecrets.Get(name)
	if primary.PrimaryRegion != "" {
		AWSError(w, "InvalidRequestException",
			"You must call ReplicateSecretToRegions in the primary Region.",
			http.StatusBadRequest)
		return
	}
	if len(req.AddReplicaRegions) == 0 {
		AWSError(w, "InvalidParameterException",
			"AddReplicaRegions must contain at least one Region.", http.StatusBadRequest)
		return
	}
	primaryRegion := smSecretRegion(primary)
	for _, add := range req.AddReplicaRegions {
		if add.Region == "" || add.Region == primaryRegion {
			AWSError(w, "InvalidParameterException",
				"A replica Region must be different from the primary Region.",
				http.StatusBadRequest)
			return
		}
		if existing, exists := smSecrets.Get(smStoreKey(add.Region, primary.Name)); exists &&
			existing.PrimaryRegion != primaryRegion && !req.ForceOverwriteReplicaSecret {
			AWSErrorf(w, "ResourceExistsException", http.StatusBadRequest,
				"The secret %s already exists in Region %s.", primary.Name, add.Region)
			return
		}
	}
	now := float64(time.Now().Unix())
	smSecrets.Update(name, func(s *SMSecret) {
		for _, add := range req.AddReplicaRegions {
			found := false
			for i := range s.Replicas {
				if s.Replicas[i].Region == add.Region {
					found = true
					s.Replicas[i].KmsKeyId = add.KmsKeyId
					s.Replicas[i].Status = "InProgress"
					s.Replicas[i].StatusMessage = ""
					s.Replicas[i].LastAccessedDate = now
				}
			}
			if !found {
				s.Replicas = append(s.Replicas, SMReplicationStatus{
					Region:           add.Region,
					KmsKeyId:         add.KmsKeyId,
					Status:           "InProgress",
					LastAccessedDate: now,
				})
			}
		}
	})
	primary, _ = smSecrets.Get(name)
	for _, add := range req.AddReplicaRegions {
		for _, status := range primary.Replicas {
			if status.Region == add.Region {
				smSyncReplica(primary, status)
				break
			}
		}
	}
	smSecrets.Update(name, func(s *SMSecret) {
		for i := range s.Replicas {
			for _, add := range req.AddReplicaRegions {
				if s.Replicas[i].Region == add.Region {
					s.Replicas[i].Status = "InSync"
					s.Replicas[i].StatusMessage = ""
				}
			}
		}
	})
	updated, _ := smSecrets.Get(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":               updated.ARN,
		"ReplicationStatus": smReplicationStatusToJSON(updated.Replicas),
	})
}

// handleSMRemoveRegionsFromReplication drops replica regions, returning the
// remaining replication-status list.
func handleSMRemoveRegionsFromReplication(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId             string   `json:"SecretId"`
		RemoveReplicaRegions []string `json:"RemoveReplicaRegions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	drop := make(map[string]bool, len(req.RemoveReplicaRegions))
	for _, region := range req.RemoveReplicaRegions {
		drop[region] = true
	}
	primary, _ := smSecrets.Get(name)
	if primary.PrimaryRegion != "" {
		AWSError(w, "InvalidRequestException",
			"You must call RemoveRegionsFromReplication in the primary Region.",
			http.StatusBadRequest)
		return
	}
	smSecrets.Update(name, func(s *SMSecret) {
		var kept []SMReplicationStatus
		for _, rep := range s.Replicas {
			if !drop[rep.Region] {
				kept = append(kept, rep)
			}
		}
		s.Replicas = kept
	})
	for region := range drop {
		replicaKey := smStoreKey(region, primary.Name)
		if replica, exists := smSecrets.Get(replicaKey); exists &&
			replica.PrimaryRegion == smSecretRegion(primary) {
			iamDeleteResourcePolicy(replica.ARN)
			smSecrets.Delete(replicaKey)
		}
	}
	updated, _ := smSecrets.Get(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN":               updated.ARN,
		"ReplicationStatus": smReplicationStatusToJSON(updated.Replicas),
	})
}

// handleSMStopReplicationToReplica promotes the destination-Region copy to an
// independent primary and removes it from the former primary's status list.
func handleSMStopReplicationToReplica(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SecretId string `json:"SecretId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequestException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Secrets Manager can't find the specified secret.")
		return
	}
	secret, _ := smSecrets.Get(name)
	if secret.PrimaryRegion == "" {
		AWSError(w, "InvalidRequestException",
			"You must call StopReplicationToReplica in a replica Region.",
			http.StatusBadRequest)
		return
	}
	formerPrimaryRegion := secret.PrimaryRegion
	smSecrets.Update(name, func(s *SMSecret) {
		s.PrimaryRegion = ""
		s.Replicas = nil
	})
	primaryKey := smStoreKey(formerPrimaryRegion, secret.Name)
	if _, exists := smSecrets.Get(primaryKey); exists {
		smSecrets.Update(primaryKey, func(primary *SMSecret) {
			kept := primary.Replicas[:0]
			for _, replica := range primary.Replicas {
				if replica.Region != smSecretRegion(secret) {
					kept = append(kept, replica)
				}
			}
			primary.Replicas = kept
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ARN": secret.ARN,
	})
}

func generateRandomPassword(length int, exclude string, useNum, usePunct, useUpper, useLower, useSpace, requireEach bool) string {
	const (
		numChars   = "0123456789"
		punctChars = `!"#$%&'()*+,-./:;<=>?@[\]^_` + "`" + `{|}~`
		upperChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		lowerChars = "abcdefghijklmnopqrstuvwxyz"
		spaceChar  = " "
	)
	build := func(charSet string) string {
		out := make([]byte, 0, len(charSet))
		for i := 0; i < len(charSet); i++ {
			ch := charSet[i]
			if strings.IndexByte(exclude, ch) < 0 {
				out = append(out, ch)
			}
		}
		return string(out)
	}
	var pool []string
	if useUpper {
		pool = append(pool, build(upperChars))
	}
	if useLower {
		pool = append(pool, build(lowerChars))
	}
	if useNum {
		pool = append(pool, build(numChars))
	}
	if usePunct {
		pool = append(pool, build(punctChars))
	}
	if useSpace {
		pool = append(pool, build(spaceChar))
	}
	if len(pool) == 0 {
		// Secrets Manager rejects an empty pool with InvalidParameterException; the
		// simulator falls back to lowercase letters instead.
		pool = append(pool, lowerChars)
	}
	all := strings.Join(pool, "")

	result := make([]byte, length)
	// If RequireEachIncludedType, seed one char from each pool first.
	idx := 0
	if requireEach {
		for _, p := range pool {
			if idx >= length {
				break
			}
			result[idx] = p[randIntn(len(p))]
			idx++
		}
	}
	for ; idx < length; idx++ {
		result[idx] = all[randIntn(len(all))]
	}
	// Shuffle so the required chars aren't all at the front.
	for i := length - 1; i > 0; i-- {
		j := randIntn(i + 1)
		result[i], result[j] = result[j], result[i]
	}
	return string(result)
}

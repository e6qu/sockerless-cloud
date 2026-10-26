package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

type cbBuildOverrides struct {
	ProjectName                      string           `json:"projectName"`
	SecondarySourcesOverride         []map[string]any `json:"secondarySourcesOverride,omitempty"`
	SecondarySourcesVersionOverride  []map[string]any `json:"secondarySourcesVersionOverride,omitempty"`
	SourceVersion                    string           `json:"sourceVersion,omitempty"`
	ArtifactsOverride                map[string]any   `json:"artifactsOverride,omitempty"`
	SecondaryArtifactsOverride       []map[string]any `json:"secondaryArtifactsOverride,omitempty"`
	EnvironmentVariablesOverride     []map[string]any `json:"environmentVariablesOverride,omitempty"`
	SourceTypeOverride               string           `json:"sourceTypeOverride,omitempty"`
	SourceLocationOverride           string           `json:"sourceLocationOverride,omitempty"`
	SourceAuthOverride               map[string]any   `json:"sourceAuthOverride,omitempty"`
	GitCloneDepthOverride            *int             `json:"gitCloneDepthOverride,omitempty"`
	GitSubmodulesConfigOverride      map[string]any   `json:"gitSubmodulesConfigOverride,omitempty"`
	BuildspecOverride                string           `json:"buildspecOverride,omitempty"`
	InsecureSslOverride              *bool            `json:"insecureSslOverride,omitempty"`
	EnvironmentTypeOverride          string           `json:"environmentTypeOverride,omitempty"`
	ImageOverride                    string           `json:"imageOverride,omitempty"`
	ComputeTypeOverride              string           `json:"computeTypeOverride,omitempty"`
	CertificateOverride              string           `json:"certificateOverride,omitempty"`
	CacheOverride                    map[string]any   `json:"cacheOverride,omitempty"`
	ServiceRoleOverride              string           `json:"serviceRoleOverride,omitempty"`
	PrivilegedModeOverride           *bool            `json:"privilegedModeOverride,omitempty"`
	QueuedTimeoutInMinutesOverride   *int             `json:"queuedTimeoutInMinutesOverride,omitempty"`
	EncryptionKeyOverride            string           `json:"encryptionKeyOverride,omitempty"`
	IdempotencyToken                 string           `json:"idempotencyToken,omitempty"`
	LogsConfigOverride               map[string]any   `json:"logsConfigOverride,omitempty"`
	RegistryCredentialOverride       map[string]any   `json:"registryCredentialOverride,omitempty"`
	ImagePullCredentialsTypeOverride string           `json:"imagePullCredentialsTypeOverride,omitempty"`
	DebugSessionEnabled              *bool            `json:"debugSessionEnabled,omitempty"`
}

type cbStartBuildInput struct {
	cbBuildOverrides
	ReportBuildStatusOverride *bool          `json:"reportBuildStatusOverride,omitempty"`
	BuildStatusConfigOverride map[string]any `json:"buildStatusConfigOverride,omitempty"`
	TimeoutInMinutesOverride  *int           `json:"timeoutInMinutesOverride,omitempty"`
	FleetOverride             map[string]any `json:"fleetOverride,omitempty"`
	AutoRetryLimitOverride    *int           `json:"autoRetryLimitOverride,omitempty"`
	HostKernelOverride        string         `json:"hostKernelOverride,omitempty"`
}

type cbStartBuildBatchInput struct {
	cbBuildOverrides
	ReportBuildBatchStatusOverride *bool          `json:"reportBuildBatchStatusOverride,omitempty"`
	BuildTimeoutInMinutesOverride  *int           `json:"buildTimeoutInMinutesOverride,omitempty"`
	BuildBatchConfigOverride       map[string]any `json:"buildBatchConfigOverride,omitempty"`
}

// The idempotencyToken window the members' documentation gives.
const cbIdempotencyWindow = 5 * time.Minute

type cbIdempotencyRecord struct {
	Digest  string    `json:"digest"`
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
}

var cbIdempotencyTokens sim.Store[cbIdempotencyRecord]

// A repeated request must match the members, the token left out.
func cbRequestDigest(request any) string {
	encoded, err := json.Marshal(request)
	if err != nil {
		panic(fmt.Sprintf("encode AWS CodeBuild request: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// false when the token is new or expired; an error when it arrived with
// different parameters. The caller serializes the operation's requests.
func cbIdempotentID(operation, token, digest string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	record, ok := cbIdempotencyTokens.Get(operation + "\x00" + token)
	if !ok || time.Since(record.Created) > cbIdempotencyWindow {
		return "", false, nil
	}
	if record.Digest != digest {
		return "", false, fmt.Errorf("idempotencyToken %s was used with different parameters", token)
	}
	return record.ID, true, nil
}

func cbRecordIdempotencyToken(operation, token, digest, id string) {
	if token == "" {
		return
	}
	cbIdempotencyTokens.Put(operation+"\x00"+token, cbIdempotencyRecord{Digest: digest, ID: id, Created: time.Now()})
}

// reportBuildStatusOverride rejects any other provider.
var cbReportStatusSourceTypes = []string{"GITHUB", "GITHUB_ENTERPRISE", "GITLAB", "GITLAB_SELF_MANAGED", "BITBUCKET"}

var cbReportBatchStatusSourceTypes = []string{"GITHUB", "GITHUB_ENTERPRISE", "BITBUCKET"}

func cbValidateRange(member string, value *int, low, high int) error {
	if value == nil {
		return nil
	}
	if *value < low || (high > 0 && *value > high) {
		if high > 0 {
			return fmt.Errorf("%s must be between %d and %d", member, low, high)
		}
		return fmt.Errorf("%s must be at least %d", member, low)
	}
	return nil
}

func cbCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

// Each override replaces the project's setting for this build only.
func cbApplyOverrides(project CBProject, o cbBuildOverrides) (CBProject, error) {
	if err := cbValidateRange("queuedTimeoutInMinutesOverride", o.QueuedTimeoutInMinutesOverride, 5, 480); err != nil {
		return CBProject{}, err
	}
	if err := cbValidateRange("gitCloneDepthOverride", o.GitCloneDepthOverride, 0, 0); err != nil {
		return CBProject{}, err
	}
	effective := project
	effective.Source = cbCopyMap(project.Source)
	if effective.Source == nil {
		effective.Source = map[string]any{}
	}
	effective.Environment = cbCopyMap(project.Environment)
	if effective.Environment == nil {
		effective.Environment = map[string]any{}
	}
	if o.SourceTypeOverride != "" {
		effective.Source["type"] = o.SourceTypeOverride
	}
	if o.SourceLocationOverride != "" {
		effective.Source["location"] = o.SourceLocationOverride
	}
	if o.SourceAuthOverride != nil {
		effective.Source["auth"] = o.SourceAuthOverride
	}
	if o.GitCloneDepthOverride != nil {
		effective.Source["gitCloneDepth"] = *o.GitCloneDepthOverride
	}
	if o.GitSubmodulesConfigOverride != nil {
		effective.Source["gitSubmodulesConfig"] = o.GitSubmodulesConfigOverride
	}
	if o.BuildspecOverride != "" {
		effective.Source["buildspec"] = o.BuildspecOverride
	}
	if o.InsecureSslOverride != nil {
		effective.Source["insecureSsl"] = *o.InsecureSslOverride
	}
	if o.SecondarySourcesOverride != nil {
		effective.SecondarySources = o.SecondarySourcesOverride
	}
	if o.SecondarySourcesVersionOverride != nil {
		effective.SecondarySourceVersions = o.SecondarySourcesVersionOverride
	}
	if o.ArtifactsOverride != nil {
		effective.Artifacts = o.ArtifactsOverride
	}
	if o.SecondaryArtifactsOverride != nil {
		effective.SecondaryArtifacts = o.SecondaryArtifactsOverride
	}
	if o.EnvironmentTypeOverride != "" {
		effective.Environment["type"] = o.EnvironmentTypeOverride
	}
	if o.ImageOverride != "" {
		effective.Environment["image"] = o.ImageOverride
	}
	if o.ComputeTypeOverride != "" {
		effective.Environment["computeType"] = o.ComputeTypeOverride
	}
	if o.CertificateOverride != "" {
		effective.Environment["certificate"] = o.CertificateOverride
	}
	if o.PrivilegedModeOverride != nil {
		effective.Environment["privilegedMode"] = *o.PrivilegedModeOverride
	}
	if o.RegistryCredentialOverride != nil {
		effective.Environment["registryCredential"] = o.RegistryCredentialOverride
	}
	if o.ImagePullCredentialsTypeOverride != "" {
		effective.Environment["imagePullCredentialsType"] = o.ImagePullCredentialsTypeOverride
	}
	if len(o.EnvironmentVariablesOverride) > 0 {
		effective.Environment["environmentVariables"] = cbMergeEnvironmentVariables(effective.Environment["environmentVariables"], o.EnvironmentVariablesOverride)
	}
	if o.CacheOverride != nil {
		effective.Cache = o.CacheOverride
	}
	if o.ServiceRoleOverride != "" {
		effective.ServiceRole = o.ServiceRoleOverride
	}
	if o.QueuedTimeoutInMinutesOverride != nil {
		effective.QueueTimeout = *o.QueuedTimeoutInMinutesOverride
	}
	if o.EncryptionKeyOverride != "" {
		effective.EncryptionKey = o.EncryptionKeyOverride
	}
	if o.LogsConfigOverride != nil {
		effective.LogsConfig = o.LogsConfigOverride
	}
	return effective, nil
}

func cbApplyBuildOverrides(project CBProject, input cbStartBuildInput) (CBProject, error) {
	if err := cbValidateRange("timeoutInMinutesOverride", input.TimeoutInMinutesOverride, 5, 2160); err != nil {
		return CBProject{}, err
	}
	effective, err := cbApplyOverrides(project, input.cbBuildOverrides)
	if err != nil {
		return CBProject{}, err
	}
	if input.ReportBuildStatusOverride != nil {
		if *input.ReportBuildStatusOverride && !cbSourceTypeIn(effective, cbReportStatusSourceTypes) {
			return CBProject{}, fmt.Errorf("reportBuildStatusOverride is valid only for %s sources", strings.Join(cbReportStatusSourceTypes, ", "))
		}
		effective.Source["reportBuildStatus"] = *input.ReportBuildStatusOverride
	}
	if input.BuildStatusConfigOverride != nil {
		effective.Source["buildStatusConfig"] = input.BuildStatusConfigOverride
	}
	if input.TimeoutInMinutesOverride != nil {
		effective.BuildTimeout = *input.TimeoutInMinutesOverride
	}
	if input.FleetOverride != nil {
		effective.Environment["fleet"] = input.FleetOverride
	}
	if input.HostKernelOverride != "" {
		effective.Environment["hostKernel"] = input.HostKernelOverride
	}
	if input.AutoRetryLimitOverride != nil {
		limit := *input.AutoRetryLimitOverride
		effective.AutoRetryLimit = &limit
	}
	return effective, nil
}

func cbSourceTypeIn(project CBProject, types []string) bool {
	sourceType := strings.ToUpper(cbString(project.Source["type"]))
	for _, candidate := range types {
		if sourceType == candidate {
			return true
		}
	}
	return false
}

// A same-named variable takes the override's value and type; others are
// appended.
func cbMergeEnvironmentVariables(project any, overrides []map[string]any) []any {
	var merged []any
	index := map[string]int{}
	if values, ok := project.([]any); ok {
		for _, value := range values {
			if item, ok := value.(map[string]any); ok {
				index[cbString(item["name"])] = len(merged)
			}
			merged = append(merged, value)
		}
	}
	for _, override := range overrides {
		name := cbString(override["name"])
		if at, ok := index[name]; ok {
			merged[at] = override
			continue
		}
		index[name] = len(merged)
		merged = append(merged, override)
	}
	return merged
}

// The simulator uploads no artifacts, so the build reports settings without
// location or checksums.
func cbBuildArtifacts(artifacts map[string]any) map[string]any {
	reported := map[string]any{}
	for _, member := range []string{"artifactIdentifier", "overrideArtifactName", "encryptionDisabled", "bucketOwnerAccess"} {
		if value, ok := artifacts[member]; ok {
			reported[member] = value
		}
	}
	return reported
}

func cbBuildArtifactsList(artifacts []map[string]any) []map[string]any {
	if artifacts == nil {
		return nil
	}
	reported := make([]map[string]any, 0, len(artifacts))
	for _, item := range artifacts {
		reported = append(reported, cbBuildArtifacts(item))
	}
	return reported
}

// The simulator opens no Session Manager session, so there is no
// sessionTarget.
func cbDebugSession(enabled *bool) map[string]any {
	if enabled == nil {
		return nil
	}
	return map[string]any{"sessionEnabled": *enabled}
}

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// SSM Patch Manager patch-group + patch-read surface, OpsMetadata,
// resource policies, and parameter history/labels. These complete the
// SSM slice on top of the patch-baseline store (ssm_patch_baselines.go)
// and the parameter store (ssm_parameters.go).
//
// All ops are awsJson1.1 (X-Amz-Target: AmazonSSM.<Op>). Timestamps are
// emitted as epoch-second JSON numbers, matching the existing SSM
// handlers.
//
// Patch-scan data (instance patch states/patches, patch-group state)
// comes from a managed-node agent reporting scan results in real AWS.
// The sim has no managed nodes, so those reads are honest-empty: real
// response shapes with zero rows / zero counts, never fabricated patch
// compliance.

// SSMPatchGroupRegistration ties a patch group to a baseline. Real SSM
// keys the registration on (patch group, operating system): a patch
// group can register with at most one baseline per OS.
type SSMPatchGroupRegistration struct {
	PatchGroup      string `json:"PatchGroup"`
	OperatingSystem string `json:"OperatingSystem"`
	BaselineId      string `json:"BaselineId"`
}

// SSMOpsMetadata is an Application Manager OpsMetadata object: a blob of
// key/value metadata tied to a resource ID, addressed by an ARN.
type SSMOpsMetadata struct {
	ResourceId       string            `json:"ResourceId"`
	OpsMetadataArn   string            `json:"OpsMetadataArn"`
	Metadata         map[string]string `json:"Metadata"`
	CreationDate     float64           `json:"CreationDate"`
	LastModifiedDate float64           `json:"LastModifiedDate"`
	LastModifiedUser string            `json:"LastModifiedUser"`
}

// SSMResourcePolicy is a JSON policy document attached to an SSM resource
// ARN (e.g. an OpsItemGroup). Real SSM versions policies with a PolicyId
// + PolicyHash pair for optimistic concurrency.
type SSMResourcePolicy struct {
	ResourceArn string `json:"ResourceArn"`
	PolicyId    string `json:"PolicyId"`
	PolicyHash  string `json:"PolicyHash"`
	Policy      string `json:"Policy"`
}

// SSMParameterVersionRow is one historical version of a parameter, plus
// the labels attached to that version. The live parameter store keeps
// only the latest version; this store keeps every version for
// GetParameterHistory and the label ops.
type SSMParameterVersionRow struct {
	Name             string   `json:"Name"`
	Type             string   `json:"Type"`
	Value            string   `json:"Value"`
	Version          int64    `json:"Version"`
	LastModifiedDate float64  `json:"LastModifiedDate"`
	Description      string   `json:"Description,omitempty"`
	KeyId            string   `json:"KeyId,omitempty"`
	AllowedPattern   string   `json:"AllowedPattern,omitempty"`
	Tier             string   `json:"Tier,omitempty"`
	DataType         string   `json:"DataType,omitempty"`
	Labels           []string `json:"Labels,omitempty"`
}

var (
	ssmPatchGroups   sim.Store[SSMPatchGroupRegistration]
	ssmOpsMetadata   sim.Store[SSMOpsMetadata]
	ssmResPolicies   sim.Store[SSMResourcePolicy]
	ssmParamVersions sim.Store[[]SSMParameterVersionRow]
)

func ssmPatchGroupKey(os, group string) string { return os + "/" + group }

func registerSSMPatchOps(r *sim.AWSRouter, srv *sim.Server) {
	ssmPatchGroups = sim.MakeStore[SSMPatchGroupRegistration](srv.DB(), "ssm_patch_groups")
	ssmOpsMetadata = sim.MakeStore[SSMOpsMetadata](srv.DB(), "ssm_ops_metadata")
	ssmResPolicies = sim.MakeStore[SSMResourcePolicy](srv.DB(), "ssm_resource_policies")
	ssmParamVersions = sim.MakeStore[[]SSMParameterVersionRow](srv.DB(), "ssm_param_versions")

	for target, h := range map[string]http.HandlerFunc{
		"AmazonSSM.RegisterPatchBaselineForPatchGroup":       handleSSMRegisterPatchBaselineForPatchGroup,
		"AmazonSSM.DeregisterPatchBaselineForPatchGroup":     handleSSMDeregisterPatchBaselineForPatchGroup,
		"AmazonSSM.GetPatchBaselineForPatchGroup":            handleSSMGetPatchBaselineForPatchGroup,
		"AmazonSSM.DescribePatchGroups":                      handleSSMDescribePatchGroups,
		"AmazonSSM.DescribePatchGroupState":                  handleSSMDescribePatchGroupState,
		"AmazonSSM.DescribeAvailablePatches":                 handleSSMDescribeAvailablePatches,
		"AmazonSSM.DescribeEffectivePatchesForPatchBaseline": handleSSMDescribeEffectivePatchesForPatchBaseline,
		"AmazonSSM.DescribeInstancePatches":                  handleSSMDescribeInstancePatches,
		"AmazonSSM.DescribeInstancePatchStates":              handleSSMDescribeInstancePatchStates,
		"AmazonSSM.DescribeInstancePatchStatesForPatchGroup": handleSSMDescribeInstancePatchStatesForPatchGroup,
		"AmazonSSM.DescribePatchProperties":                  handleSSMDescribePatchProperties,
		"AmazonSSM.GetDeployablePatchSnapshotForInstance":    handleSSMGetDeployablePatchSnapshotForInstance,
		"AmazonSSM.CreateOpsMetadata":                        handleSSMCreateOpsMetadata,
		"AmazonSSM.GetOpsMetadata":                           handleSSMGetOpsMetadata,
		"AmazonSSM.UpdateOpsMetadata":                        handleSSMUpdateOpsMetadata,
		"AmazonSSM.DeleteOpsMetadata":                        handleSSMDeleteOpsMetadata,
		"AmazonSSM.ListOpsMetadata":                          handleSSMListOpsMetadata,
		"AmazonSSM.GetOpsSummary":                            handleSSMGetOpsSummary,
		"AmazonSSM.PutResourcePolicy":                        handleSSMPutResourcePolicy,
		"AmazonSSM.GetResourcePolicies":                      handleSSMGetResourcePolicies,
		"AmazonSSM.DeleteResourcePolicy":                     handleSSMDeleteResourcePolicy,
		"AmazonSSM.GetParameterHistory":                      handleSSMGetParameterHistory,
		"AmazonSSM.LabelParameterVersion":                    handleSSMLabelParameterVersion,
		"AmazonSSM.UnlabelParameterVersion":                  handleSSMUnlabelParameterVersion,
	} {
		r.Register(target, h)
	}
}

// Patch group registration / reads

func handleSSMRegisterPatchBaselineForPatchGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaselineId string `json:"BaselineId"`
		PatchGroup string `json:"PatchGroup"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.BaselineId == "" || req.PatchGroup == "" {
		sim.AWSError(w, "ValidationException", "BaselineId and PatchGroup are required", http.StatusBadRequest)
		return
	}
	bl, ok := ssmPatchBaselines.Get(req.BaselineId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Patch baseline %s doesn't exist.", req.BaselineId)
		return
	}
	key := ssmPatchGroupKey(bl.OperatingSystem, req.PatchGroup)
	if existing, ok := ssmPatchGroups.Get(key); ok && existing.BaselineId != req.BaselineId {
		sim.AWSErrorf(w, "AlreadyExistsException", http.StatusBadRequest,
			"Patch group %s is already registered with patch baseline %s for operating system %s.",
			req.PatchGroup, existing.BaselineId, bl.OperatingSystem)
		return
	}
	ssmPatchGroups.Put(key, SSMPatchGroupRegistration{
		PatchGroup:      req.PatchGroup,
		OperatingSystem: bl.OperatingSystem,
		BaselineId:      req.BaselineId,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"BaselineId": req.BaselineId,
		"PatchGroup": req.PatchGroup,
	})
}

func handleSSMDeregisterPatchBaselineForPatchGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaselineId string `json:"BaselineId"`
		PatchGroup string `json:"PatchGroup"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.BaselineId == "" || req.PatchGroup == "" {
		sim.AWSError(w, "ValidationException", "BaselineId and PatchGroup are required", http.StatusBadRequest)
		return
	}
	// Remove any registration of this group pointing at the baseline,
	// regardless of OS (a group can only be registered once per OS).
	for _, reg := range ssmPatchGroups.List() {
		if reg.PatchGroup == req.PatchGroup && reg.BaselineId == req.BaselineId {
			ssmPatchGroups.Delete(ssmPatchGroupKey(reg.OperatingSystem, reg.PatchGroup))
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"BaselineId": req.BaselineId,
		"PatchGroup": req.PatchGroup,
	})
}

func handleSSMGetPatchBaselineForPatchGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PatchGroup      string `json:"PatchGroup"`
		OperatingSystem string `json:"OperatingSystem"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PatchGroup == "" {
		sim.AWSError(w, "ValidationException", "PatchGroup is required", http.StatusBadRequest)
		return
	}
	os := req.OperatingSystem
	if os == "" {
		os = "WINDOWS"
	}
	reg, ok := ssmPatchGroups.Get(ssmPatchGroupKey(os, req.PatchGroup))
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"No patch baseline is registered for patch group %s and operating system %s.", req.PatchGroup, os)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"BaselineId":      reg.BaselineId,
		"PatchGroup":      reg.PatchGroup,
		"OperatingSystem": reg.OperatingSystem,
	})
}

func handleSSMDescribePatchGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmPatchGroups.List()
	if all == nil {
		all = []SSMPatchGroupRegistration{}
	}
	sortBy(all, func(g SSMPatchGroupRegistration) string { return ssmPatchGroupKey(g.OperatingSystem, g.PatchGroup) })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	mappings := make([]map[string]any, 0, len(page))
	for _, reg := range page {
		entry := map[string]any{"PatchGroup": reg.PatchGroup}
		if bl, ok := ssmPatchBaselines.Get(reg.BaselineId); ok {
			def, _ := ssmDefaultBaselines.Get(bl.OperatingSystem)
			id := map[string]any{
				"BaselineId":      bl.BaselineId,
				"BaselineName":    bl.Name,
				"OperatingSystem": bl.OperatingSystem,
				"DefaultBaseline": def.BaselineId == bl.BaselineId,
			}
			if bl.Description != "" {
				id["BaselineDescription"] = bl.Description
			}
			entry["BaselineIdentity"] = id
		}
		mappings = append(mappings, entry)
	}
	resp := map[string]any{"Mappings": mappings}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMDescribePatchGroupState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PatchGroup string `json:"PatchGroup"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PatchGroup == "" {
		sim.AWSError(w, "ValidationException", "PatchGroup is required", http.StatusBadRequest)
		return
	}
	// No managed nodes report scan data in the sim: every count is an
	// honest zero, matching a patch group with no scanned instances.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Instances":                          0,
		"InstancesWithInstalledPatches":      0,
		"InstancesWithInstalledOtherPatches": 0,
		"InstancesWithMissingPatches":        0,
		"InstancesWithFailedPatches":         0,
		"InstancesWithNotApplicablePatches":  0,
	})
}

func handleSSMDescribeAvailablePatches(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// The catalog of vendor-published patches is sourced from the cloud's
	// patch repositories. The sim has no upstream repo to mirror, so the
	// available-patch list is honest-empty rather than fabricated.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Patches": []map[string]any{},
	})
}

func handleSSMDescribeEffectivePatchesForPatchBaseline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaselineId string `json:"BaselineId"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.BaselineId == "" {
		sim.AWSError(w, "ValidationException", "BaselineId is required", http.StatusBadRequest)
		return
	}
	if _, ok := ssmPatchBaselines.Get(req.BaselineId); !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Patch baseline %s doesn't exist.", req.BaselineId)
		return
	}
	// The effective patch set is computed by evaluating the baseline's
	// approval rules against the vendor patch catalog. With no upstream
	// catalog to evaluate against, the effective set is honest-empty.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"EffectivePatches": []map[string]any{},
	})
}

func handleSSMDescribeInstancePatches(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceId string `json:"InstanceId"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.InstanceId == "" {
		sim.AWSError(w, "ValidationException", "InstanceId is required", http.StatusBadRequest)
		return
	}
	// Per-node patch compliance comes from a node's scan results; the sim
	// has no scanned nodes, so this is honest-empty.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Patches": []map[string]any{},
	})
}

func handleSSMDescribeInstancePatchStates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceIds []string `json:"InstanceIds"`
		MaxResults  int      `json:"MaxResults"`
		NextToken   string   `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.InstanceIds) == 0 {
		sim.AWSError(w, "ValidationException", "InstanceIds is required", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"InstancePatchStates": []map[string]any{},
	})
}

func handleSSMDescribeInstancePatchStatesForPatchGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PatchGroup string `json:"PatchGroup"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PatchGroup == "" {
		sim.AWSError(w, "ValidationException", "PatchGroup is required", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"InstancePatchStates": []map[string]any{},
	})
}

// ssmPatchPropertyValues maps an OperatingSystem to the catalog of values
// for the requested PatchProperty. Real SSM returns the distinct property
// values seen across the vendor patch catalog for that OS. The sim mirrors
// the well-known AWS-published value sets so callers enumerating, for
// example, the available CLASSIFICATION values for AMAZON_LINUX_2 get the
// real set rather than nothing.
var ssmPatchPropertyValues = map[string]map[string][]string{
	"WINDOWS": {
		"PRODUCT":        {"Windows10", "WindowsServer2016", "WindowsServer2019", "WindowsServer2022"},
		"PRODUCT_FAMILY": {"Windows"},
		"CLASSIFICATION": {"CriticalUpdates", "SecurityUpdates", "Updates", "DefinitionUpdates", "ServicePacks"},
		"MSRC_SEVERITY":  {"Critical", "Important", "Moderate", "Low"},
		"PRIORITY":       {},
		"SEVERITY":       {},
	},
	"AMAZON_LINUX_2": {
		"PRODUCT":        {"AmazonLinux2"},
		"PRODUCT_FAMILY": {"AmazonLinux2"},
		"CLASSIFICATION": {"Security", "Bugfix", "Enhancement", "Recommended", "Newpackage"},
		"SEVERITY":       {"Critical", "Important", "Medium", "Low"},
		"PRIORITY":       {},
		"MSRC_SEVERITY":  {},
	},
	"UBUNTU": {
		"PRODUCT":        {"Ubuntu20.04", "Ubuntu22.04"},
		"PRODUCT_FAMILY": {"Ubuntu"},
		"PRIORITY":       {"Required", "Important", "Standard", "Optional", "Extra"},
		"CLASSIFICATION": {},
		"SEVERITY":       {},
		"MSRC_SEVERITY":  {},
	},
}

func handleSSMDescribePatchProperties(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OperatingSystem string `json:"OperatingSystem"`
		Property        string `json:"Property"`
		PatchSet        string `json:"PatchSet"`
		MaxResults      int    `json:"MaxResults"`
		NextToken       string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.OperatingSystem == "" || req.Property == "" {
		sim.AWSError(w, "ValidationException", "OperatingSystem and Property are required", http.StatusBadRequest)
		return
	}
	var values []string
	if byProp, ok := ssmPatchPropertyValues[req.OperatingSystem]; ok {
		values = byProp[req.Property]
	}
	props := make([]map[string]string, 0, len(values))
	for _, v := range values {
		// Each PatchPropertyEntry is a single-key map keyed by the property
		// name (the AttributeName), valued by the property value.
		props = append(props, map[string]string{req.Property: v})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Properties": props,
	})
}

func handleSSMGetDeployablePatchSnapshotForInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceId string `json:"InstanceId"`
		SnapshotId string `json:"SnapshotId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.InstanceId == "" || req.SnapshotId == "" {
		sim.AWSError(w, "ValidationException", "InstanceId and SnapshotId are required", http.StatusBadRequest)
		return
	}
	// The snapshot download URL is a presigned S3 object that AWS-RunPatchBaseline
	// pulls the baseline manifest from. The sim returns a deterministic,
	// well-formed S3 URL scoped by snapshot ID.
	url := "https://patch-baseline-snapshot-" + awsRegion() + ".s3." + awsRegion() +
		".amazonaws.com/" + req.SnapshotId
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"InstanceId":          req.InstanceId,
		"SnapshotId":          req.SnapshotId,
		"SnapshotDownloadUrl": url,
	})
}

// OpsMetadata

func ssmOpsMetadataArn(resourceID string) string {
	// Real OpsMetadata ARN: arn:aws:ssm:<region>:<account>:opsmetadata/<resource-path>.
	// The resource ID is itself an ARN of the Application Manager resource;
	// AWS appends its path under opsmetadata/. The sim builds a stable ARN
	// from a hash so the same resource maps to the same OpsMetadata ARN.
	h := sha256.Sum256([]byte(resourceID))
	return "arn:aws:ssm:" + awsRegion() + ":" + awsAccountID() +
		":opsmetadata/aws/ssm/" + hex.EncodeToString(h[:8]) + "/opsmetadata"
}

func handleSSMCreateOpsMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceId string                            `json:"ResourceId"`
		Metadata   map[string]struct{ Value string } `json:"Metadata"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceId == "" {
		sim.AWSError(w, "ValidationException", "ResourceId is required", http.StatusBadRequest)
		return
	}
	arn := ssmOpsMetadataArn(req.ResourceId)
	if _, ok := ssmOpsMetadata.Get(arn); ok {
		sim.AWSErrorf(w, "OpsMetadataAlreadyExistsException", http.StatusBadRequest,
			"An OpsMetadata object already exists for the resource ID %s.", req.ResourceId)
		return
	}
	md := map[string]string{}
	for k, v := range req.Metadata {
		md[k] = v.Value
	}
	now := float64(time.Now().Unix())
	ssmOpsMetadata.Put(arn, SSMOpsMetadata{
		ResourceId:       req.ResourceId,
		OpsMetadataArn:   arn,
		Metadata:         md,
		CreationDate:     now,
		LastModifiedDate: now,
		LastModifiedUser: "arn:aws:iam::" + awsAccountID() + ":root",
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"OpsMetadataArn": arn})
}

func ssmMetadataMapWire(md map[string]string) map[string]any {
	out := make(map[string]any, len(md))
	for k, v := range md {
		out[k] = map[string]any{"Value": v}
	}
	return out
}

func handleSSMGetOpsMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpsMetadataArn string `json:"OpsMetadataArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	md, ok := ssmOpsMetadata.Get(req.OpsMetadataArn)
	if !ok {
		sim.AWSErrorf(w, "OpsMetadataNotFoundException", http.StatusBadRequest,
			"The OpsMetadata object %s doesn't exist.", req.OpsMetadataArn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ResourceId": md.ResourceId,
		"Metadata":   ssmMetadataMapWire(md.Metadata),
	})
}

func handleSSMUpdateOpsMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpsMetadataArn   string                            `json:"OpsMetadataArn"`
		MetadataToUpdate map[string]struct{ Value string } `json:"MetadataToUpdate"`
		KeysToDelete     []string                          `json:"KeysToDelete"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	md, ok := ssmOpsMetadata.Get(req.OpsMetadataArn)
	if !ok {
		sim.AWSErrorf(w, "OpsMetadataNotFoundException", http.StatusBadRequest,
			"The OpsMetadata object %s doesn't exist.", req.OpsMetadataArn)
		return
	}
	if md.Metadata == nil {
		md.Metadata = map[string]string{}
	}
	for k, v := range req.MetadataToUpdate {
		md.Metadata[k] = v.Value
	}
	for _, k := range req.KeysToDelete {
		delete(md.Metadata, k)
	}
	md.LastModifiedDate = float64(time.Now().Unix())
	ssmOpsMetadata.Put(req.OpsMetadataArn, md)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"OpsMetadataArn": req.OpsMetadataArn})
}

func handleSSMDeleteOpsMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OpsMetadataArn string `json:"OpsMetadataArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmOpsMetadata.Get(req.OpsMetadataArn); !ok {
		sim.AWSErrorf(w, "OpsMetadataNotFoundException", http.StatusBadRequest,
			"The OpsMetadata object %s doesn't exist.", req.OpsMetadataArn)
		return
	}
	ssmOpsMetadata.Delete(req.OpsMetadataArn)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMListOpsMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
		Filters    []struct {
			Key    string   `json:"Key"`
			Values []string `json:"Values"`
		} `json:"Filters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmOpsMetadata.Filter(func(m SSMOpsMetadata) bool {
		for _, f := range req.Filters {
			// The only documented filter key is the resource ID.
			if f.Key != "resourceId" && f.Key != "ResourceId" {
				continue
			}
			matched := false
			for _, v := range f.Values {
				if m.ResourceId == v {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
		return true
	})
	sortBy(all, func(m SSMOpsMetadata) string { return m.OpsMetadataArn })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, m := range page {
		out = append(out, map[string]any{
			"ResourceId":       m.ResourceId,
			"OpsMetadataArn":   m.OpsMetadataArn,
			"LastModifiedDate": m.LastModifiedDate,
			"LastModifiedUser": m.LastModifiedUser,
			"CreationDate":     m.CreationDate,
		})
	}
	resp := map[string]any{"OpsMetadataList": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handleSSMGetOpsSummary aggregates OpsData. The sim has no OpsData
// aggregation pipeline (resource-data-sync ingest), so it returns an
// honest-empty entity list with the real GetOpsSummary shape.
func handleSSMGetOpsSummary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SyncName string `json:"SyncName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Entities": []map[string]any{},
	})
}

// Resource policies

func ssmPolicyHash(policy string) string {
	h := sha256.Sum256([]byte(policy))
	return hex.EncodeToString(h[:])
}

func handleSSMPutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
		Policy      string `json:"Policy"`
		PolicyId    string `json:"PolicyId"`
		PolicyHash  string `json:"PolicyHash"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" || req.Policy == "" {
		sim.AWSError(w, "ResourcePolicyInvalidParameterException", "ResourceArn and Policy are required", http.StatusBadRequest)
		return
	}
	existing, exists := ssmResPolicies.Get(req.ResourceArn)
	if exists {
		// Update path: PolicyId + PolicyHash must match the current version
		// (optimistic concurrency), matching real SSM.
		if req.PolicyId == "" || req.PolicyHash == "" {
			sim.AWSError(w, "ResourcePolicyConflictException",
				"PolicyId and PolicyHash are required to update an existing policy.", http.StatusBadRequest)
			return
		}
		if req.PolicyId != existing.PolicyId || req.PolicyHash != existing.PolicyHash {
			sim.AWSError(w, "ResourcePolicyConflictException",
				"The PolicyHash provided doesn't match the current policy version.", http.StatusBadRequest)
			return
		}
	}
	id := req.PolicyId
	if id == "" {
		id = newSSMPolicyID()
	}
	hash := ssmPolicyHash(req.Policy)
	ssmResPolicies.Put(req.ResourceArn, SSMResourcePolicy{
		ResourceArn: req.ResourceArn,
		PolicyId:    id,
		PolicyHash:  hash,
		Policy:      req.Policy,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"PolicyId":   id,
		"PolicyHash": hash,
	})
}

func newSSMPolicyID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func handleSSMGetResourcePolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
		MaxResults  int    `json:"MaxResults"`
		NextToken   string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" {
		sim.AWSError(w, "ResourcePolicyInvalidParameterException", "ResourceArn is required", http.StatusBadRequest)
		return
	}
	var policies []map[string]any
	if p, ok := ssmResPolicies.Get(req.ResourceArn); ok {
		policies = append(policies, map[string]any{
			"PolicyId":   p.PolicyId,
			"PolicyHash": p.PolicyHash,
			"Policy":     p.Policy,
		})
	}
	if policies == nil {
		policies = []map[string]any{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Policies": policies,
	})
}

func handleSSMDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
		PolicyId    string `json:"PolicyId"`
		PolicyHash  string `json:"PolicyHash"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	existing, ok := ssmResPolicies.Get(req.ResourceArn)
	if !ok {
		sim.AWSErrorf(w, "ResourcePolicyNotFoundException", http.StatusBadRequest,
			"No resource policy is attached to %s.", req.ResourceArn)
		return
	}
	if req.PolicyId != existing.PolicyId || req.PolicyHash != existing.PolicyHash {
		sim.AWSError(w, "ResourcePolicyConflictException",
			"The PolicyId or PolicyHash provided doesn't match the current policy version.", http.StatusBadRequest)
		return
	}
	ssmResPolicies.Delete(req.ResourceArn)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// Parameter history + labels

// ssmSyncParameterVersions reconciles the version-history store with the
// live parameter. The live parameter store keeps only the latest version
// of each parameter; the history store accumulates a row per version so
// GetParameterHistory and the label ops have something to read. Whenever
// the live parameter advances to a version not yet recorded, a row is
// appended here. This keeps history faithful for every version the sim
// has actually observed (each PutParameter bumps Version, and the next
// read reconciles the new version into history) without the parameter
// store needing to know about this slice.
func ssmSyncParameterVersions(p SSMParameter) []SSMParameterVersionRow {
	key := ensureLeadingSlash(p.Name)
	rows, _ := ssmParamVersions.Get(key)
	for _, row := range rows {
		if row.Version == p.Version {
			return rows
		}
	}
	rows = append(rows, SSMParameterVersionRow{
		Name:             p.Name,
		Type:             p.Type,
		Value:            p.Value,
		Version:          p.Version,
		LastModifiedDate: p.LastModifiedDate,
		Description:      p.Description,
		KeyId:            p.KeyId,
		AllowedPattern:   p.AllowedPattern,
		Tier:             p.Tier,
		DataType:         p.DataType,
	})
	ssmParamVersions.Put(key, rows)
	return rows
}

func handleSSMGetParameterHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"Name"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		sim.AWSError(w, "ValidationException", "Name is required", http.StatusBadRequest)
		return
	}
	cur, ok := ssmParams.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "ParameterNotFound", http.StatusBadRequest,
			"Parameter %s not found.", req.Name)
		return
	}
	rows := ssmSyncParameterVersions(cur)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Version < rows[j].Version })
	page, next := awsPage(rows, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, row := range page {
		entry := map[string]any{
			"Name":             row.Name,
			"Type":             row.Type,
			"Value":            row.Value,
			"Version":          row.Version,
			"LastModifiedDate": row.LastModifiedDate,
		}
		if row.Description != "" {
			entry["Description"] = row.Description
		}
		if row.KeyId != "" {
			entry["KeyId"] = row.KeyId
		}
		if row.AllowedPattern != "" {
			entry["AllowedPattern"] = row.AllowedPattern
		}
		if row.Tier != "" {
			entry["Tier"] = row.Tier
		}
		if row.DataType != "" {
			entry["DataType"] = row.DataType
		}
		labels := row.Labels
		if labels == nil {
			labels = []string{}
		}
		entry["Labels"] = labels
		out = append(out, entry)
	}
	resp := map[string]any{"Parameters": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ssmValidLabel mirrors the real label constraints: labels can't start
// with a number, can't be over 100 chars, and only allow the documented
// character set. The sim enforces the start-with-number + length rules
// (the most common reason a label is rejected).
func ssmValidLabel(label string) bool {
	if label == "" || len(label) > 100 {
		return false
	}
	if label[0] >= '0' && label[0] <= '9' {
		return false
	}
	return true
}

func handleSSMLabelParameterVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string   `json:"Name"`
		ParameterVersion *int64   `json:"ParameterVersion"`
		Labels           []string `json:"Labels"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		sim.AWSError(w, "ValidationException", "Name is required", http.StatusBadRequest)
		return
	}
	cur, ok := ssmParams.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "ParameterNotFound", http.StatusBadRequest,
			"Parameter %s not found.", req.Name)
		return
	}
	version := cur.Version
	if req.ParameterVersion != nil {
		version = *req.ParameterVersion
	}
	key := ensureLeadingSlash(req.Name)
	rows := ssmSyncParameterVersions(cur)
	idx := -1
	for i := range rows {
		if rows[i].Version == version {
			idx = i
			break
		}
	}
	if idx < 0 {
		sim.AWSErrorf(w, "ParameterVersionNotFound", http.StatusBadRequest,
			"Version %d of parameter %s not found.", version, req.Name)
		return
	}
	var invalid []string
	for _, l := range req.Labels {
		if !ssmValidLabel(l) {
			invalid = append(invalid, l)
			continue
		}
		// A label is unique to one version: detach it from any other
		// version before attaching it here, matching real SSM.
		for j := range rows {
			rows[j].Labels = ssmRemoveLabel(rows[j].Labels, l)
		}
		if !ssmHasLabel(rows[idx].Labels, l) {
			rows[idx].Labels = append(rows[idx].Labels, l)
		}
	}
	ssmParamVersions.Put(key, rows)
	if invalid == nil {
		invalid = []string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"InvalidLabels":    invalid,
		"ParameterVersion": version,
	})
}

func handleSSMUnlabelParameterVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string   `json:"Name"`
		ParameterVersion *int64   `json:"ParameterVersion"`
		Labels           []string `json:"Labels"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.ParameterVersion == nil {
		sim.AWSError(w, "ValidationException", "Name and ParameterVersion are required", http.StatusBadRequest)
		return
	}
	cur, ok := ssmParams.Get(req.Name)
	if !ok {
		sim.AWSErrorf(w, "ParameterNotFound", http.StatusBadRequest,
			"Parameter %s not found.", req.Name)
		return
	}
	key := ensureLeadingSlash(req.Name)
	rows := ssmSyncParameterVersions(cur)
	idx := -1
	for i := range rows {
		if rows[i].Version == *req.ParameterVersion {
			idx = i
			break
		}
	}
	if idx < 0 {
		sim.AWSErrorf(w, "ParameterVersionNotFound", http.StatusBadRequest,
			"Version %d of parameter %s not found.", *req.ParameterVersion, req.Name)
		return
	}
	var removed, invalid []string
	for _, l := range req.Labels {
		if ssmHasLabel(rows[idx].Labels, l) {
			rows[idx].Labels = ssmRemoveLabel(rows[idx].Labels, l)
			removed = append(removed, l)
		} else {
			invalid = append(invalid, l)
		}
	}
	ssmParamVersions.Put(key, rows)
	if removed == nil {
		removed = []string{}
	}
	if invalid == nil {
		invalid = []string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"RemovedLabels": removed,
		"InvalidLabels": invalid,
	})
}

func ssmHasLabel(labels []string, l string) bool {
	for _, x := range labels {
		if x == l {
			return true
		}
	}
	return false
}

func ssmRemoveLabel(labels []string, l string) []string {
	out := labels[:0]
	for _, x := range labels {
		if x != l {
			out = append(out, x)
		}
	}
	return out
}

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// SSM Patch Baselines — terraform's `aws_ssm_patch_baseline` and
// `aws_ssm_default_patch_baseline` resources hit this slice. A baseline
// scopes patch approval rules to an operating system; one baseline per
// OS can be the account default. The sim round-trips the nested
// GlobalFilters / ApprovalRules / Sources structures verbatim as raw
// JSON so it never silently drops a member the SDK sent.

// SSMPatchBaseline is a patch baseline definition.
type SSMPatchBaseline struct {
	BaselineId                       string          `json:"BaselineId"`
	Name                             string          `json:"Name"`
	OperatingSystem                  string          `json:"OperatingSystem"`
	Description                      string          `json:"Description,omitempty"`
	GlobalFilters                    json.RawMessage `json:"GlobalFilters,omitempty"`
	ApprovalRules                    json.RawMessage `json:"ApprovalRules,omitempty"`
	ApprovedPatches                  []string        `json:"ApprovedPatches,omitempty"`
	ApprovedPatchesComplianceLevel   string          `json:"ApprovedPatchesComplianceLevel,omitempty"`
	ApprovedPatchesEnableNonSecurity bool            `json:"ApprovedPatchesEnableNonSecurity"`
	RejectedPatches                  []string        `json:"RejectedPatches,omitempty"`
	RejectedPatchesAction            string          `json:"RejectedPatchesAction,omitempty"`
	Sources                          json.RawMessage `json:"Sources,omitempty"`
	CreatedDate                      float64         `json:"CreatedDate"`
	ModifiedDate                     float64         `json:"ModifiedDate"`
}

// SSMDefaultBaseline maps an OperatingSystem to its account-default
// baseline. Keyed by OperatingSystem in the store.
type SSMDefaultBaseline struct {
	OperatingSystem string `json:"OperatingSystem"`
	BaselineId      string `json:"BaselineId"`
}

var (
	ssmPatchBaselines sim.Store[SSMPatchBaseline]
	// ssmDefaultBaselines maps an OperatingSystem → default baseline.
	ssmDefaultBaselines sim.Store[SSMDefaultBaseline]
)

func registerSSMPatchBaselines(r *sim.AWSRouter, srv *sim.Server) {
	ssmPatchBaselines = sim.MakeStore[SSMPatchBaseline](srv.DB(), "ssm_patch_baselines")
	ssmDefaultBaselines = sim.MakeStore[SSMDefaultBaseline](srv.DB(), "ssm_default_patch_baselines")

	r.Register("AmazonSSM.CreatePatchBaseline", handleSSMCreatePatchBaseline)
	r.Register("AmazonSSM.DeletePatchBaseline", handleSSMDeletePatchBaseline)
	r.Register("AmazonSSM.GetPatchBaseline", handleSSMGetPatchBaseline)
	r.Register("AmazonSSM.UpdatePatchBaseline", handleSSMUpdatePatchBaseline)
	r.Register("AmazonSSM.DescribePatchBaselines", handleSSMDescribePatchBaselines)
	r.Register("AmazonSSM.GetDefaultPatchBaseline", handleSSMGetDefaultPatchBaseline)
	r.Register("AmazonSSM.RegisterDefaultPatchBaseline", handleSSMRegisterDefaultPatchBaseline)
}

// newSSMBaselineID returns a "pb-" + 17 lowercase hex chars ID, matching
// the real-AWS baseline ID shape (the BaselineId pattern allows it).
func newSSMBaselineID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return "pb-" + hex.EncodeToString(b)[:17]
}

func ssmPatchBaselineWire(p SSMPatchBaseline) map[string]any {
	out := map[string]any{
		"BaselineId":                       p.BaselineId,
		"Name":                             p.Name,
		"OperatingSystem":                  p.OperatingSystem,
		"ApprovedPatchesEnableNonSecurity": p.ApprovedPatchesEnableNonSecurity,
		"CreatedDate":                      p.CreatedDate,
		"ModifiedDate":                     p.ModifiedDate,
	}
	if p.Description != "" {
		out["Description"] = p.Description
	}
	if len(p.GlobalFilters) > 0 {
		out["GlobalFilters"] = p.GlobalFilters
	}
	if len(p.ApprovalRules) > 0 {
		out["ApprovalRules"] = p.ApprovalRules
	}
	if p.ApprovedPatches != nil {
		out["ApprovedPatches"] = p.ApprovedPatches
	}
	if p.ApprovedPatchesComplianceLevel != "" {
		out["ApprovedPatchesComplianceLevel"] = p.ApprovedPatchesComplianceLevel
	}
	if p.RejectedPatches != nil {
		out["RejectedPatches"] = p.RejectedPatches
	}
	if p.RejectedPatchesAction != "" {
		out["RejectedPatchesAction"] = p.RejectedPatchesAction
	}
	if len(p.Sources) > 0 {
		out["Sources"] = p.Sources
	}
	return out
}

func handleSSMCreatePatchBaseline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                             string          `json:"Name"`
		OperatingSystem                  string          `json:"OperatingSystem"`
		Description                      string          `json:"Description"`
		GlobalFilters                    json.RawMessage `json:"GlobalFilters"`
		ApprovalRules                    json.RawMessage `json:"ApprovalRules"`
		ApprovedPatches                  []string        `json:"ApprovedPatches"`
		ApprovedPatchesComplianceLevel   string          `json:"ApprovedPatchesComplianceLevel"`
		ApprovedPatchesEnableNonSecurity bool            `json:"ApprovedPatchesEnableNonSecurity"`
		RejectedPatches                  []string        `json:"RejectedPatches"`
		RejectedPatchesAction            string          `json:"RejectedPatchesAction"`
		Sources                          json.RawMessage `json:"Sources"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		sim.AWSError(w, "ValidationException", "Name is required", http.StatusBadRequest)
		return
	}
	if req.OperatingSystem == "" {
		req.OperatingSystem = "WINDOWS"
	}
	now := float64(time.Now().Unix())
	p := SSMPatchBaseline{
		BaselineId:                       newSSMBaselineID(),
		Name:                             req.Name,
		OperatingSystem:                  req.OperatingSystem,
		Description:                      req.Description,
		GlobalFilters:                    req.GlobalFilters,
		ApprovalRules:                    req.ApprovalRules,
		ApprovedPatches:                  req.ApprovedPatches,
		ApprovedPatchesComplianceLevel:   req.ApprovedPatchesComplianceLevel,
		ApprovedPatchesEnableNonSecurity: req.ApprovedPatchesEnableNonSecurity,
		RejectedPatches:                  req.RejectedPatches,
		RejectedPatchesAction:            req.RejectedPatchesAction,
		Sources:                          req.Sources,
		CreatedDate:                      now,
		ModifiedDate:                     now,
	}
	ssmPatchBaselines.Put(p.BaselineId, p)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"BaselineId": p.BaselineId})
}

func handleSSMDeletePatchBaseline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaselineId string `json:"BaselineId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmPatchBaselines.Get(req.BaselineId); !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Patch baseline %s doesn't exist.", req.BaselineId)
		return
	}
	ssmPatchBaselines.Delete(req.BaselineId)
	// Clear any default mapping that pointed at it.
	for _, d := range ssmDefaultBaselines.List() {
		if d.BaselineId == req.BaselineId {
			ssmDefaultBaselines.Delete(d.OperatingSystem)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"BaselineId": req.BaselineId})
}

func handleSSMGetPatchBaseline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaselineId string `json:"BaselineId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	p, ok := ssmPatchBaselines.Get(req.BaselineId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Patch baseline %s doesn't exist.", req.BaselineId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, ssmPatchBaselineWire(p))
}

func handleSSMUpdatePatchBaseline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaselineId                       string          `json:"BaselineId"`
		Name                             string          `json:"Name"`
		Description                      string          `json:"Description"`
		GlobalFilters                    json.RawMessage `json:"GlobalFilters"`
		ApprovalRules                    json.RawMessage `json:"ApprovalRules"`
		ApprovedPatches                  []string        `json:"ApprovedPatches"`
		ApprovedPatchesComplianceLevel   string          `json:"ApprovedPatchesComplianceLevel"`
		ApprovedPatchesEnableNonSecurity *bool           `json:"ApprovedPatchesEnableNonSecurity"`
		RejectedPatches                  []string        `json:"RejectedPatches"`
		RejectedPatchesAction            string          `json:"RejectedPatchesAction"`
		Sources                          json.RawMessage `json:"Sources"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	p, ok := ssmPatchBaselines.Get(req.BaselineId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Patch baseline %s doesn't exist.", req.BaselineId)
		return
	}
	if req.Name != "" {
		p.Name = req.Name
	}
	if req.Description != "" {
		p.Description = req.Description
	}
	if req.GlobalFilters != nil {
		p.GlobalFilters = req.GlobalFilters
	}
	if req.ApprovalRules != nil {
		p.ApprovalRules = req.ApprovalRules
	}
	if req.ApprovedPatches != nil {
		p.ApprovedPatches = req.ApprovedPatches
	}
	if req.ApprovedPatchesComplianceLevel != "" {
		p.ApprovedPatchesComplianceLevel = req.ApprovedPatchesComplianceLevel
	}
	if req.ApprovedPatchesEnableNonSecurity != nil {
		p.ApprovedPatchesEnableNonSecurity = *req.ApprovedPatchesEnableNonSecurity
	}
	if req.RejectedPatches != nil {
		p.RejectedPatches = req.RejectedPatches
	}
	if req.RejectedPatchesAction != "" {
		p.RejectedPatchesAction = req.RejectedPatchesAction
	}
	if req.Sources != nil {
		p.Sources = req.Sources
	}
	p.ModifiedDate = float64(time.Now().Unix())
	ssmPatchBaselines.Put(p.BaselineId, p)
	sim.WriteJSON(w, http.StatusOK, ssmPatchBaselineWire(p))
}

func handleSSMDescribePatchBaselines(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ssmPatchBaselines.List()
	if all == nil {
		all = []SSMPatchBaseline{}
	}
	sortBy(all, func(p SSMPatchBaseline) string { return p.BaselineId })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, p := range page {
		def, _ := ssmDefaultBaselines.Get(p.OperatingSystem)
		row := map[string]any{
			"BaselineId":      p.BaselineId,
			"BaselineName":    p.Name,
			"OperatingSystem": p.OperatingSystem,
			"DefaultBaseline": def.BaselineId == p.BaselineId,
		}
		if p.Description != "" {
			row["BaselineDescription"] = p.Description
		}
		out = append(out, row)
	}
	resp := map[string]any{"BaselineIdentities": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleSSMGetDefaultPatchBaseline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OperatingSystem string `json:"OperatingSystem"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	os := req.OperatingSystem
	if os == "" {
		os = "WINDOWS"
	}
	var id string
	if def, ok := ssmDefaultBaselines.Get(os); ok {
		id = def.BaselineId
	} else {
		// Real SSM always has an AWS-managed default per OS. Synthesize a
		// deterministic AWS-managed default ID so callers that never
		// registered one still get a baseline, matching real behavior.
		id = "arn:aws:ssm:" + awsRegion() + ":" + awsAccountID() + ":patchbaseline/pb-aws-default-" + os
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"BaselineId":      id,
		"OperatingSystem": os,
	})
}

func handleSSMRegisterDefaultPatchBaseline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaselineId string `json:"BaselineId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	p, ok := ssmPatchBaselines.Get(req.BaselineId)
	if !ok {
		sim.AWSErrorf(w, "DoesNotExistException", http.StatusBadRequest,
			"Patch baseline %s doesn't exist.", req.BaselineId)
		return
	}
	ssmDefaultBaselines.Put(p.OperatingSystem, SSMDefaultBaseline{
		OperatingSystem: p.OperatingSystem,
		BaselineId:      p.BaselineId,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"BaselineId": p.BaselineId})
}

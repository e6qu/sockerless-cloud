package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// AWS Systems Manager cloud connectors — the control plane that registers a
// non-AWS cloud (today: Microsoft Azure) as a managed-node source for Systems
// Manager. A connector names the IAM role Systems Manager assumes, the AWS
// Config connector it feeds, and the Azure tenant/application to reach.
//
// ValidateCloudConnector performs the checks the real operation performs
// against the resources it can see: the IAM role must exist and must trust the
// Systems Manager service principal, and each configured Azure subscription
// target is reported. Every finding is derived from stored state — a connector
// pointing at a role the IAM slice does not hold reports the real
// AwsRoleAssumptionFailed finding, never a canned "valid".

// SSMCloudConnector is one registered cloud connector, keyed by CloudConnectorId.
type SSMCloudConnector struct {
	CloudConnectorId   string          `json:"CloudConnectorId"`
	CloudConnectorArn  string          `json:"CloudConnectorArn"`
	DisplayName        string          `json:"DisplayName"`
	Description        string          `json:"Description,omitempty"`
	RoleArn            string          `json:"RoleArn"`
	ConfigConnectorArn string          `json:"ConfigConnectorArn"`
	Configuration      json.RawMessage `json:"Configuration,omitempty"`
	CreatedAt          float64         `json:"CreatedAt"`
	UpdatedAt          float64         `json:"UpdatedAt"`
	Tags               []ssmTagKV      `json:"Tags,omitempty"`
}

// ssmTagKV is the SSM TagList element.
type ssmTagKV struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// ssmCloudConnectorConfiguration is the CloudConnectorConfiguration union. Azure
// is the one member the shape defines.
type ssmCloudConnectorConfiguration struct {
	AzureConfiguration *struct {
		TenantId               string `json:"TenantId"`
		TenantDisplayName      string `json:"TenantDisplayName"`
		ApplicationId          string `json:"ApplicationId"`
		ApplicationDisplayName string `json:"ApplicationDisplayName"`
		Targets                *struct {
			Subscriptions []struct {
				Id          string `json:"Id"`
				DisplayName string `json:"DisplayName"`
			} `json:"Subscriptions"`
		} `json:"Targets"`
	} `json:"AzureConfiguration"`
}

var ssmCloudConnectors sim.Store[SSMCloudConnector]

func registerSSMCloudConnectors(r *sim.AWSRouter, srv *sim.Server) {
	ssmCloudConnectors = sim.MakeStore[SSMCloudConnector](srv.DB(), "ssm_cloud_connectors")

	r.Register("AmazonSSM.CreateCloudConnector", handleSSMCreateCloudConnector)
	r.Register("AmazonSSM.GetCloudConnector", handleSSMGetCloudConnector)
	r.Register("AmazonSSM.ListCloudConnectors", handleSSMListCloudConnectors)
	r.Register("AmazonSSM.UpdateCloudConnector", handleSSMUpdateCloudConnector)
	r.Register("AmazonSSM.DeleteCloudConnector", handleSSMDeleteCloudConnector)
	r.Register("AmazonSSM.ValidateCloudConnector", handleSSMValidateCloudConnector)
}

func ssmCloudConnectorARN(id string) string {
	return "arn:aws:ssm:" + awsRegion() + ":" + awsAccountID() + ":cloud-connector/" + id
}

// ssmParseCloudConnectorConfiguration decodes the CloudConnectorConfiguration
// union and reports whether it names exactly one modeled member with the
// members that member requires.
func ssmParseCloudConnectorConfiguration(raw json.RawMessage) (ssmCloudConnectorConfiguration, string) {
	var cfg ssmCloudConnectorConfiguration
	if len(raw) == 0 {
		return cfg, "Configuration is required"
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, "Configuration could not be decoded: " + err.Error()
	}
	if cfg.AzureConfiguration == nil {
		return cfg, "Configuration must set the AzureConfiguration member"
	}
	if cfg.AzureConfiguration.TenantId == "" {
		return cfg, "AzureConfiguration.TenantId is required"
	}
	if cfg.AzureConfiguration.ApplicationId == "" {
		return cfg, "AzureConfiguration.ApplicationId is required"
	}
	return cfg, ""
}

func handleSSMCreateCloudConnector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName        string          `json:"DisplayName"`
		Description        string          `json:"Description"`
		RoleArn            string          `json:"RoleArn"`
		Configuration      json.RawMessage `json:"Configuration"`
		ConfigConnectorArn string          `json:"ConfigConnectorArn"`
		Tags               []ssmTagKV      `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "invalid request body", http.StatusBadRequest)
		return
	}
	switch {
	case req.DisplayName == "":
		sim.AWSError(w, "ValidationException", "DisplayName is required", http.StatusBadRequest)
		return
	case req.RoleArn == "":
		sim.AWSError(w, "ValidationException", "RoleArn is required", http.StatusBadRequest)
		return
	case req.ConfigConnectorArn == "":
		sim.AWSError(w, "ValidationException", "ConfigConnectorArn is required", http.StatusBadRequest)
		return
	}
	if _, msg := ssmParseCloudConnectorConfiguration(req.Configuration); msg != "" {
		sim.AWSError(w, "ValidationException", msg, http.StatusBadRequest)
		return
	}
	// The display name identifies the connector for an operator; a second
	// connector with the same name conflicts with the existing one.
	for _, c := range ssmCloudConnectors.List() {
		if c.DisplayName == req.DisplayName {
			sim.AWSErrorf(w, "ConflictException", http.StatusBadRequest,
				"A cloud connector named %s already exists", req.DisplayName)
			return
		}
	}

	id := generateUUID()
	now := float64(time.Now().UTC().Unix())
	ssmCloudConnectors.Put(id, SSMCloudConnector{
		CloudConnectorId:   id,
		CloudConnectorArn:  ssmCloudConnectorARN(id),
		DisplayName:        req.DisplayName,
		Description:        req.Description,
		RoleArn:            req.RoleArn,
		ConfigConnectorArn: req.ConfigConnectorArn,
		Configuration:      req.Configuration,
		CreatedAt:          now,
		UpdatedAt:          now,
		Tags:               req.Tags,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CloudConnectorId": id})
}

func handleSSMGetCloudConnector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CloudConnectorId string `json:"CloudConnectorId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "invalid request body", http.StatusBadRequest)
		return
	}
	c, ok := ssmCloudConnectors.Get(req.CloudConnectorId)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Cloud connector %s does not exist", req.CloudConnectorId)
		return
	}
	out := map[string]any{
		"CloudConnectorArn":  c.CloudConnectorArn,
		"DisplayName":        c.DisplayName,
		"RoleArn":            c.RoleArn,
		"ConfigConnectorArn": c.ConfigConnectorArn,
		"CreatedAt":          c.CreatedAt,
		"UpdatedAt":          c.UpdatedAt,
	}
	if c.Description != "" {
		out["Description"] = c.Description
	}
	if len(c.Configuration) > 0 {
		out["Configuration"] = c.Configuration
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// ssmSortedCloudConnectors returns the stored connectors ordered by creation
// time then id, so pagination is stable.
func ssmSortedCloudConnectors() []SSMCloudConnector {
	out := ssmCloudConnectors.List()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].CloudConnectorId < out[j].CloudConnectorId
	})
	return out
}

// ssmCloudConnectorMatchesFilters applies the modeled SubscriptionId / TenantId
// filters against the connector's Azure configuration.
func ssmCloudConnectorMatchesFilters(c SSMCloudConnector, filters []struct {
	FilterKey    string   `json:"FilterKey"`
	FilterValues []string `json:"FilterValues"`
},
) bool {
	if len(filters) == 0 {
		return true
	}
	cfg, _ := ssmParseCloudConnectorConfiguration(c.Configuration)
	for _, f := range filters {
		matched := false
		switch f.FilterKey {
		case "TenantId":
			if cfg.AzureConfiguration != nil {
				for _, v := range f.FilterValues {
					if v == cfg.AzureConfiguration.TenantId {
						matched = true
					}
				}
			}
		case "SubscriptionId":
			if cfg.AzureConfiguration != nil && cfg.AzureConfiguration.Targets != nil {
				for _, sub := range cfg.AzureConfiguration.Targets.Subscriptions {
					for _, v := range f.FilterValues {
						if v == sub.Id {
							matched = true
						}
					}
				}
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func handleSSMListCloudConnectors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
		Filters    []struct {
			FilterKey    string   `json:"FilterKey"`
			FilterValues []string `json:"FilterValues"`
		} `json:"Filters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "invalid request body", http.StatusBadRequest)
		return
	}
	var matched []SSMCloudConnector
	for _, c := range ssmSortedCloudConnectors() {
		if ssmCloudConnectorMatchesFilters(c, req.Filters) {
			matched = append(matched, c)
		}
	}
	page, next := awsPageExplicit(matched, req.NextToken, req.MaxResults)
	summaries := make([]map[string]any, 0, len(page))
	for _, c := range page {
		s := map[string]any{
			"CloudConnectorId": c.CloudConnectorId,
			"DisplayName":      c.DisplayName,
			"RoleArn":          c.RoleArn,
			"CreatedAt":        c.CreatedAt,
			"UpdatedAt":        c.UpdatedAt,
		}
		if c.Description != "" {
			s["Description"] = c.Description
		}
		summaries = append(summaries, s)
	}
	out := map[string]any{"CloudConnectors": summaries}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSSMUpdateCloudConnector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CloudConnectorId string          `json:"CloudConnectorId"`
		DisplayName      string          `json:"DisplayName"`
		Description      string          `json:"Description"`
		Configuration    json.RawMessage `json:"Configuration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "invalid request body", http.StatusBadRequest)
		return
	}
	c, ok := ssmCloudConnectors.Get(req.CloudConnectorId)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Cloud connector %s does not exist", req.CloudConnectorId)
		return
	}
	if len(req.Configuration) > 0 {
		if _, msg := ssmParseCloudConnectorConfiguration(req.Configuration); msg != "" {
			sim.AWSError(w, "ValidationException", msg, http.StatusBadRequest)
			return
		}
		c.Configuration = req.Configuration
	}
	if req.DisplayName != "" {
		c.DisplayName = req.DisplayName
	}
	if req.Description != "" {
		c.Description = req.Description
	}
	c.UpdatedAt = float64(time.Now().UTC().Unix())
	ssmCloudConnectors.Put(c.CloudConnectorId, c)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CloudConnectorId": c.CloudConnectorId})
}

func handleSSMDeleteCloudConnector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CloudConnectorId string `json:"CloudConnectorId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ssmCloudConnectors.Get(req.CloudConnectorId); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Cloud connector %s does not exist", req.CloudConnectorId)
		return
	}
	ssmCloudConnectors.Delete(req.CloudConnectorId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CloudConnectorId": req.CloudConnectorId})
}

// ssmCloudConnectorFindings derives the validation findings for a connector
// from the state the simulator holds: the IAM role behind RoleArn and the Azure
// targets the configuration names.
func ssmCloudConnectorFindings(c SSMCloudConnector) []map[string]any {
	findings := make([]map[string]any, 0)

	roleName := iamRoleNameFromArn(c.RoleArn)
	role, roleFound := iamRoles.Get(roleName)
	switch {
	case !roleFound:
		findings = append(findings, map[string]any{
			"Type":    "ERROR",
			"Code":    "AwsRoleAssumptionFailed",
			"Message": "The role " + c.RoleArn + " does not exist.",
		})
	case !strings.Contains(role.AssumeRolePolicyDocument, "ssm.amazonaws.com"):
		findings = append(findings, map[string]any{
			"Type":    "ERROR",
			"Code":    "AwsRoleAssumptionFailed",
			"Message": "The trust policy of " + c.RoleArn + " does not allow ssm.amazonaws.com to assume it.",
		})
	}

	cfg, _ := ssmParseCloudConnectorConfiguration(c.Configuration)
	if cfg.AzureConfiguration == nil {
		return findings
	}
	subscriptions := 0
	if cfg.AzureConfiguration.Targets != nil {
		subscriptions = len(cfg.AzureConfiguration.Targets.Subscriptions)
	}
	tenantFinding := map[string]any{
		"Type":    "INFO",
		"Code":    "TenantSummary",
		"Message": "Tenant " + cfg.AzureConfiguration.TenantId + " has " + itoaSSM(subscriptions) + " configured subscription target(s).",
		"Scope": map[string]any{
			"Type": "azure:tenant",
			"Id":   cfg.AzureConfiguration.TenantId,
		},
	}
	findings = append(findings, tenantFinding)
	if cfg.AzureConfiguration.Targets == nil {
		return findings
	}
	for _, sub := range cfg.AzureConfiguration.Targets.Subscriptions {
		findings = append(findings, map[string]any{
			"Type":    "INFO",
			"Code":    "SubscriptionAccessible",
			"Message": "Subscription " + sub.Id + " is a configured target of this connector.",
			"Scope": map[string]any{
				"Type": "azure:subscription",
				"Id":   sub.Id,
			},
		})
	}
	return findings
}

func handleSSMValidateCloudConnector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CloudConnectorId string `json:"CloudConnectorId"`
		MaxResults       int    `json:"MaxResults"`
		NextToken        string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "invalid request body", http.StatusBadRequest)
		return
	}
	c, ok := ssmCloudConnectors.Get(req.CloudConnectorId)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Cloud connector %s does not exist", req.CloudConnectorId)
		return
	}
	page, next := awsPageExplicit(ssmCloudConnectorFindings(c), req.NextToken, req.MaxResults)
	out := map[string]any{"ValidationFindings": page}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

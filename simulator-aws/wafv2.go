package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// AWS WAFv2 — JSON 1.1 over POST / + X-Amz-Target=AWSWAF_20190729.<Op>.
// Sim covers the CLOUDFRONT scope (global, us-east-1). REGIONAL scope
// (ALB / API Gateway path) is intentionally out of scope — would
// compose with the same handlers if a backend needs it later.
// Resource bodies (Rules, VisibilityConfig, etc.) pass
// through as opaque json.RawMessage so the sim doesn't need to mirror
// the full SDK type tree — Terraform round-trips correctly because
// the wire bytes are preserved.

// ---------- WebACL ----------

type WAFWebACL struct {
	Name                         string          `json:"Name"`
	Id                           string          `json:"Id"`
	ARN                          string          `json:"ARN"`
	DefaultAction                json.RawMessage `json:"DefaultAction"`
	Description                  string          `json:"Description,omitempty"`
	Rules                        json.RawMessage `json:"Rules,omitempty"`
	VisibilityConfig             json.RawMessage `json:"VisibilityConfig"`
	Capacity                     int64           `json:"Capacity"`
	CustomResponseBodies         json.RawMessage `json:"CustomResponseBodies,omitempty"`
	CaptchaConfig                json.RawMessage `json:"CaptchaConfig,omitempty"`
	ChallengeConfig              json.RawMessage `json:"ChallengeConfig,omitempty"`
	TokenDomains                 []string        `json:"TokenDomains,omitempty"`
	AssociationConfig            json.RawMessage `json:"AssociationConfig,omitempty"`
	LabelNamespace               string          `json:"LabelNamespace,omitempty"`
	ApplicationConfig            json.RawMessage `json:"ApplicationConfig,omitempty"`
	RetrofittedByFirewallManager bool            `json:"RetrofittedByFirewallManager,omitempty"`
}

type WAFIPSet struct {
	Name             string   `json:"Name"`
	Id               string   `json:"Id"`
	ARN              string   `json:"ARN"`
	Description      string   `json:"Description,omitempty"`
	IPAddressVersion string   `json:"IPAddressVersion"`
	Addresses        []string `json:"Addresses"`
}

type WAFRuleGroup struct {
	Name                 string          `json:"Name"`
	Id                   string          `json:"Id"`
	ARN                  string          `json:"ARN"`
	Capacity             int64           `json:"Capacity"`
	Description          string          `json:"Description,omitempty"`
	Rules                json.RawMessage `json:"Rules,omitempty"`
	VisibilityConfig     json.RawMessage `json:"VisibilityConfig"`
	LabelNamespace       string          `json:"LabelNamespace,omitempty"`
	CustomResponseBodies json.RawMessage `json:"CustomResponseBodies,omitempty"`
}

type WAFRegexPatternSet struct {
	Name                  string          `json:"Name"`
	Id                    string          `json:"Id"`
	ARN                   string          `json:"ARN"`
	Description           string          `json:"Description,omitempty"`
	RegularExpressionList json.RawMessage `json:"RegularExpressionList,omitempty"`
}

type wafTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value,omitempty"`
}

// Storage envelopes hold a Scope + LockToken alongside the resource.
type wafStoredWebACL struct {
	WebACL    WAFWebACL
	Scope     string
	LockToken string
	Tags      []wafTag
}

type wafStoredIPSet struct {
	IPSet     WAFIPSet
	Scope     string
	LockToken string
	Tags      []wafTag
}

type wafStoredRuleGroup struct {
	RuleGroup WAFRuleGroup
	Scope     string
	LockToken string
	Tags      []wafTag
}

type wafStoredRegex struct {
	RegexSet  WAFRegexPatternSet
	Scope     string
	LockToken string
	Tags      []wafTag
}

// wafStoredLogging holds a per-web-ACL logging configuration. The full
// LoggingConfiguration body passes through as opaque json.RawMessage so the
// sim preserves every wire field; ResourceArn is lifted out for keying and
// Scope is derived from the ARN path ("global/" → CLOUDFRONT) so ListLogging-
// Configurations can filter by Scope the way real AWS does.
type wafStoredLogging struct {
	ResourceArn string
	Scope       string
	Config      json.RawMessage
}

// wafStoredAPIKey holds an encrypted-token API key. The wire-form APIKey is a
// base64-encoded encryption of the token domains plus a creation timestamp;
// GetDecryptedAPIKey reverses it back to the stored domains + timestamp.
type wafStoredAPIKey struct {
	APIKey       string
	Scope        string
	TokenDomains []string
	Created      time.Time
	Version      int
}

// wafStoredManagedRuleSet holds a customer-managed rule set resource. Managed
// rule sets are the publisher-side resource AWS Managed Rules vendors use to
// publish versions of their rule groups; the API exposes full CRUD on them.
type wafStoredManagedRuleSet struct {
	RuleSet   WAFManagedRuleSet
	Scope     string
	LockToken string
}

type wafSampledRequest struct {
	WebACLARN        string
	RuleMetricName   string
	Action           string
	Timestamp        time.Time
	ClientIP         string
	HTTPVersion      string
	Headers          []wafSampledHTTPHeader
	Method           string
	URI              string
	ResponseCodeSent int
	Labels           []string
}

type wafSampledHTTPHeader struct {
	Name  string
	Value string
}

type wafAssociation struct {
	ResourceARN string
	WebACLARN   string
}

var (
	// The data plane consults this store on every forwarded request, and a
	// forwarded request can arrive before this service is registered — a
	// simulator assembled without it, or a test that mounts one data plane.
	// An empty store answers the question truthfully (nothing is registered,
	// so nothing is associated); a nil one panicked in the middle of serving.
	wafWebACLs         sim.Store[wafStoredWebACL] = sim.NewStateStore[wafStoredWebACL]()
	wafIPSets          sim.Store[wafStoredIPSet]
	wafRuleGroups      sim.Store[wafStoredRuleGroup]
	wafRegexSets       sim.Store[wafStoredRegex]
	wafLogging         sim.Store[wafStoredLogging]
	wafAPIKeys         sim.Store[wafStoredAPIKey]
	wafManagedRuleSet  sim.Store[wafStoredManagedRuleSet]
	wafSampledRequests sim.Store[wafSampledRequest]
	wafRateWindows     sim.Store[wafRateWindow]
	// wafPermissionPolicies: rule-group ARN → IAM-style policy JSON document.
	wafPermissionPolicies sim.Store[string]
	// wafAssociations maps a real cloud resource ARN to its WebACL ARN.
	// CloudFront distributions and Amplify apps share this authoritative
	// association state, just as both services are protected through WAFv2.
	wafAssociations sim.Store[wafAssociation] = sim.NewStateStore[wafAssociation]()
)

// ---------- Helpers ----------

func wafRandomID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	h := hex.EncodeToString(buf)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// wafLockToken returns the optimistic-concurrency token AWS WAFV2 hands back
// with every entity. The model types it as a UUID exactly as it types an entity
// id — LockToken and EntityId carry the same pattern — so it is generated the
// same way. A shorter token is not one the service could have issued, and a
// client that round-trips it into a request would be sending a malformed value.
func wafLockToken() string { return wafRandomID() }

// wafARN constructs an ARN. Real AWS convention:
//
//	CLOUDFRONT scope: arn:aws:wafv2:us-east-1:<acct>:global/<type>/<name>/<id>
//	REGIONAL scope:   arn:aws:wafv2:<region>:<acct>:regional/<type>/<name>/<id>
//
// The Terraform provider rejects "global" as a region value and expects
// "us-east-1" with "global/" appearing only in the resource path.
func wafARN(scope, resourceType, name, id string) string {
	region := awsRegion()
	scopePath := "regional"
	if scope == "CLOUDFRONT" {
		region = "us-east-1"
		scopePath = "global"
	}
	return fmt.Sprintf("arn:aws:wafv2:%s:%s:%s/%s/%s/%s", region, awsAccountID(), scopePath, resourceType, name, id)
}

// wafKey: stores resources under "<scope>/<id>" so the same Name + Id
// can collide between scopes (real AWS allows it).
func wafKey(scope, id string) string { return scope + "/" + id }

func wafWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func wafWriteError(w http.ResponseWriter, code, msg string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"__type":  code,
		"message": msg,
	})
}

func wafWriteDuplicate(w http.ResponseWriter, kind, name string) {
	wafWriteError(w, "WAFDuplicateItemException",
		fmt.Sprintf("AWS WAF couldn't perform the operation because some resource in your request is a duplicate of an existing one: %s %s", kind, name))
}

// ---------- Registration ----------

func registerWAFv2(r *sim.AWSRouter, srv *sim.Server) {
	wafWebACLs = sim.MakeStore[wafStoredWebACL](srv.DB(), "wafv2_webacls")
	wafIPSets = sim.MakeStore[wafStoredIPSet](srv.DB(), "wafv2_ipsets")
	wafRuleGroups = sim.MakeStore[wafStoredRuleGroup](srv.DB(), "wafv2_rulegroups")
	wafRegexSets = sim.MakeStore[wafStoredRegex](srv.DB(), "wafv2_regex_sets")
	wafLogging = sim.MakeStore[wafStoredLogging](srv.DB(), "wafv2_logging")
	wafAPIKeys = sim.MakeStore[wafStoredAPIKey](srv.DB(), "wafv2_apikeys")
	wafManagedRuleSet = sim.MakeStore[wafStoredManagedRuleSet](srv.DB(), "wafv2_managed_rule_sets")
	wafPermissionPolicies = sim.MakeStore[string](srv.DB(), "wafv2_permission_policies")
	wafSampledRequests = sim.MakeStore[wafSampledRequest](srv.DB(), "wafv2_sampled_requests")
	wafAssociations = sim.MakeStore[wafAssociation](srv.DB(), "wafv2_associations")
	wafRateWindows = sim.MakeStore[wafRateWindow](srv.DB(), "wafv2_rate_windows")
	wafSeedManagedRuleSets()

	// WebACL
	r.Register("AWSWAF_20190729.CreateWebACL", handleWAFCreateWebACL)
	r.Register("AWSWAF_20190729.GetWebACL", handleWAFGetWebACL)
	r.Register("AWSWAF_20190729.UpdateWebACL", handleWAFUpdateWebACL)
	r.Register("AWSWAF_20190729.DeleteWebACL", handleWAFDeleteWebACL)
	r.Register("AWSWAF_20190729.ListWebACLs", handleWAFListWebACLs)
	// Association
	r.Register("AWSWAF_20190729.AssociateWebACL", handleWAFAssociateWebACL)
	r.Register("AWSWAF_20190729.DisassociateWebACL", handleWAFDisassociateWebACL)
	r.Register("AWSWAF_20190729.GetWebACLForResource", handleWAFGetWebACLForResource)
	r.Register("AWSWAF_20190729.ListResourcesForWebACL", handleWAFListResourcesForWebACL)
	// IPSet
	r.Register("AWSWAF_20190729.CreateIPSet", handleWAFCreateIPSet)
	r.Register("AWSWAF_20190729.GetIPSet", handleWAFGetIPSet)
	r.Register("AWSWAF_20190729.UpdateIPSet", handleWAFUpdateIPSet)
	r.Register("AWSWAF_20190729.DeleteIPSet", handleWAFDeleteIPSet)
	r.Register("AWSWAF_20190729.ListIPSets", handleWAFListIPSets)
	// RuleGroup
	r.Register("AWSWAF_20190729.CreateRuleGroup", handleWAFCreateRuleGroup)
	r.Register("AWSWAF_20190729.GetRuleGroup", handleWAFGetRuleGroup)
	r.Register("AWSWAF_20190729.UpdateRuleGroup", handleWAFUpdateRuleGroup)
	r.Register("AWSWAF_20190729.DeleteRuleGroup", handleWAFDeleteRuleGroup)
	r.Register("AWSWAF_20190729.ListRuleGroups", handleWAFListRuleGroups)
	// RegexPatternSet
	r.Register("AWSWAF_20190729.CreateRegexPatternSet", handleWAFCreateRegexSet)
	r.Register("AWSWAF_20190729.GetRegexPatternSet", handleWAFGetRegexSet)
	r.Register("AWSWAF_20190729.UpdateRegexPatternSet", handleWAFUpdateRegexSet)
	r.Register("AWSWAF_20190729.DeleteRegexPatternSet", handleWAFDeleteRegexSet)
	r.Register("AWSWAF_20190729.ListRegexPatternSets", handleWAFListRegexSets)
	// Tagging
	r.Register("AWSWAF_20190729.TagResource", handleWAFTagResource)
	r.Register("AWSWAF_20190729.UntagResource", handleWAFUntagResource)
	r.Register("AWSWAF_20190729.ListTagsForResource", handleWAFListTagsForResource)
	// Logging configuration
	r.Register("AWSWAF_20190729.PutLoggingConfiguration", handleWAFPutLoggingConfiguration)
	r.Register("AWSWAF_20190729.GetLoggingConfiguration", handleWAFGetLoggingConfiguration)
	r.Register("AWSWAF_20190729.DeleteLoggingConfiguration", handleWAFDeleteLoggingConfiguration)
	r.Register("AWSWAF_20190729.ListLoggingConfigurations", handleWAFListLoggingConfigurations)
	// Sampled requests
	r.Register("AWSWAF_20190729.GetSampledRequests", handleWAFGetSampledRequests)
	r.Register("AWSWAF_20190729.GetRevenueStatistics", handleWAFGetRevenueStatistics)
	r.Register("AWSWAF_20190729.GetRevenueStatisticsSummary", handleWAFGetRevenueStatisticsSummary)
	r.Register("AWSWAF_20190729.GetRevenueStatisticsTimeSeries", handleWAFGetRevenueStatisticsTimeSeries)
	r.Register("AWSWAF_20190729.ListSettlementRecords", handleWAFListSettlementRecords)
	// API keys
	r.Register("AWSWAF_20190729.CreateAPIKey", handleWAFCreateAPIKey)
	r.Register("AWSWAF_20190729.DeleteAPIKey", handleWAFDeleteAPIKey)
	r.Register("AWSWAF_20190729.ListAPIKeys", handleWAFListAPIKeys)
	r.Register("AWSWAF_20190729.GetDecryptedAPIKey", handleWAFGetDecryptedAPIKey)
	// Capacity
	r.Register("AWSWAF_20190729.CheckCapacity", handleWAFCheckCapacity)
	// Managed rule group / product catalog
	r.Register("AWSWAF_20190729.DescribeManagedRuleGroup", handleWAFDescribeManagedRuleGroup)
	r.Register("AWSWAF_20190729.DescribeAllManagedProducts", handleWAFDescribeAllManagedProducts)
	r.Register("AWSWAF_20190729.DescribeManagedProductsByVendor", handleWAFDescribeManagedProductsByVendor)
	r.Register("AWSWAF_20190729.ListAvailableManagedRuleGroups", handleWAFListAvailableManagedRuleGroups)
	r.Register("AWSWAF_20190729.ListAvailableManagedRuleGroupVersions", handleWAFListAvailableManagedRuleGroupVersions)
	// Managed rule sets (publisher CRUD)
	r.Register("AWSWAF_20190729.GetManagedRuleSet", handleWAFGetManagedRuleSet)
	r.Register("AWSWAF_20190729.ListManagedRuleSets", handleWAFListManagedRuleSets)
	r.Register("AWSWAF_20190729.PutManagedRuleSetVersions", handleWAFPutManagedRuleSetVersions)
	r.Register("AWSWAF_20190729.UpdateManagedRuleSetVersionExpiryDate", handleWAFUpdateManagedRuleSetVersionExpiryDate)
	// Permission policy (cross-account rule-group sharing)
	r.Register("AWSWAF_20190729.PutPermissionPolicy", handleWAFPutPermissionPolicy)
	r.Register("AWSWAF_20190729.GetPermissionPolicy", handleWAFGetPermissionPolicy)
	r.Register("AWSWAF_20190729.DeletePermissionPolicy", handleWAFDeletePermissionPolicy)
	// Mobile SDK releases
	r.Register("AWSWAF_20190729.GenerateMobileSdkReleaseUrl", handleWAFGenerateMobileSdkReleaseUrl)
	r.Register("AWSWAF_20190729.GetMobileSdkRelease", handleWAFGetMobileSdkRelease)
	r.Register("AWSWAF_20190729.ListMobileSdkReleases", handleWAFListMobileSdkReleases)
	// Firewall Manager managed rule groups
	r.Register("AWSWAF_20190729.DeleteFirewallManagerRuleGroups", handleWAFDeleteFirewallManagerRuleGroups)
	// Rate-based statement managed keys + traffic statistics
	r.Register("AWSWAF_20190729.GetRateBasedStatementManagedKeys", handleWAFGetRateBasedStatementManagedKeys)
	r.Register("AWSWAF_20190729.GetTopPathStatisticsByTraffic", handleWAFGetTopPathStatisticsByTraffic)
}

// ---------- WebACL handlers ----------

type wafCreateWebACLReq struct {
	Name                 string          `json:"Name"`
	Scope                string          `json:"Scope"`
	DefaultAction        json.RawMessage `json:"DefaultAction"`
	Description          string          `json:"Description,omitempty"`
	Rules                json.RawMessage `json:"Rules,omitempty"`
	VisibilityConfig     json.RawMessage `json:"VisibilityConfig"`
	Tags                 []wafTag        `json:"Tags,omitempty"`
	CaptchaConfig        json.RawMessage `json:"CaptchaConfig,omitempty"`
	ChallengeConfig      json.RawMessage `json:"ChallengeConfig,omitempty"`
	TokenDomains         []string        `json:"TokenDomains,omitempty"`
	CustomResponseBodies json.RawMessage `json:"CustomResponseBodies,omitempty"`
	AssociationConfig    json.RawMessage `json:"AssociationConfig,omitempty"`
}

// wafEntitySummary builds the summary AWS WAFV2 returns for a created entity.
// Description is optional and the model's EntityDescription pattern requires at
// least one character, so an entity created without one carries no Description
// member rather than an empty string — which is what the service returns and
// what a client reading the field back can rely on.
func wafEntitySummary(name, id, description, lockToken, arn string) map[string]any {
	summary := map[string]any{"Name": name, "Id": id, "LockToken": lockToken, "ARN": arn}
	if description != "" {
		summary["Description"] = description
	}
	return summary
}

// wafEntityNamePattern is the model's EntityName pattern. A name outside it is
// rejected by the service before anything is created, so accepting one here
// would let a caller store a name it could never have stored — and every
// response echoing that name back would then carry a value the model forbids.
var wafEntityNamePattern = regexp.MustCompile(`^[\w\-]+$`)

// wafValidateName reports whether the name is one AWS WAFV2 accepts, writing
// the service's own rejection when it is not.
func wafValidateName(w http.ResponseWriter, name string) bool {
	if wafEntityNamePattern.MatchString(name) {
		return true
	}
	wafWriteError(w, "WAFInvalidParameterException",
		"Error reason: The parameter contains formatting that is not valid., field: NAME, parameter: "+name)
	return false
}

func handleWAFCreateWebACL(w http.ResponseWriter, r *http.Request) {
	var req wafCreateWebACLReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.Name == "" || req.Scope == "" {
		wafWriteError(w, "WAFInvalidParameterException", "Name and Scope are required")
		return
	}
	if !wafValidateName(w, req.Name) {
		return
	}
	for _, s := range wafWebACLs.List() {
		if s.Scope == req.Scope && s.WebACL.Name == req.Name {
			wafWriteDuplicate(w, "WebACL", req.Name)
			return
		}
	}
	id := wafRandomID()
	lock := wafLockToken()
	acl := WAFWebACL{
		Name:                 req.Name,
		Id:                   id,
		ARN:                  wafARN(req.Scope, "webacl", req.Name, id),
		DefaultAction:        req.DefaultAction,
		Description:          req.Description,
		Rules:                req.Rules,
		VisibilityConfig:     req.VisibilityConfig,
		CustomResponseBodies: req.CustomResponseBodies,
		CaptchaConfig:        req.CaptchaConfig,
		ChallengeConfig:      req.ChallengeConfig,
		TokenDomains:         req.TokenDomains,
		AssociationConfig:    req.AssociationConfig,
		LabelNamespace:       "awswaf:" + awsAccountID() + ":webacl:" + req.Name + ":",
		Capacity:             wafCapacityFromRawRules(req.Rules),
	}
	wafWebACLs.Put(wafKey(req.Scope, id), wafStoredWebACL{WebACL: acl, Scope: req.Scope, LockToken: lock, Tags: req.Tags})
	wafWriteJSON(w, map[string]any{
		"Summary": wafEntitySummary(acl.Name, acl.Id, acl.Description, lock, acl.ARN),
	})
}

type wafGetReq struct {
	Name  string `json:"Name"`
	Scope string `json:"Scope"`
	Id    string `json:"Id"`
}

func handleWAFGetWebACL(w http.ResponseWriter, r *http.Request) {
	var req wafGetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	stored, ok := wafWebACLs.Get(wafKey(req.Scope, req.Id))
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "WebACL not found")
		return
	}
	wafWriteJSON(w, map[string]any{
		"WebACL":                    stored.WebACL,
		"LockToken":                 stored.LockToken,
		"ApplicationIntegrationURL": "https://" + awsRegion() + ".webacl-sim.example.com/" + stored.WebACL.Id,
	})
}

type wafUpdateWebACLReq struct {
	Name                 string          `json:"Name"`
	Scope                string          `json:"Scope"`
	Id                   string          `json:"Id"`
	LockToken            string          `json:"LockToken"`
	DefaultAction        json.RawMessage `json:"DefaultAction"`
	Description          string          `json:"Description,omitempty"`
	Rules                json.RawMessage `json:"Rules,omitempty"`
	VisibilityConfig     json.RawMessage `json:"VisibilityConfig"`
	CustomResponseBodies json.RawMessage `json:"CustomResponseBodies,omitempty"`
	CaptchaConfig        json.RawMessage `json:"CaptchaConfig,omitempty"`
	ChallengeConfig      json.RawMessage `json:"ChallengeConfig,omitempty"`
	TokenDomains         []string        `json:"TokenDomains,omitempty"`
	AssociationConfig    json.RawMessage `json:"AssociationConfig,omitempty"`
}

func handleWAFUpdateWebACL(w http.ResponseWriter, r *http.Request) {
	var req wafUpdateWebACLReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.Id)
	stored, ok := wafWebACLs.Get(key)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "WebACL not found")
		return
	}
	if req.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match current value")
		return
	}
	stored.WebACL.DefaultAction = req.DefaultAction
	stored.WebACL.Description = req.Description
	stored.WebACL.Rules = req.Rules
	stored.WebACL.Capacity = wafCapacityFromRawRules(req.Rules)
	stored.WebACL.VisibilityConfig = req.VisibilityConfig
	stored.WebACL.CustomResponseBodies = req.CustomResponseBodies
	stored.WebACL.CaptchaConfig = req.CaptchaConfig
	stored.WebACL.ChallengeConfig = req.ChallengeConfig
	stored.WebACL.TokenDomains = req.TokenDomains
	stored.WebACL.AssociationConfig = req.AssociationConfig
	stored.LockToken = wafLockToken()
	wafWebACLs.Put(key, stored)
	wafDeleteRateWindows(stored.WebACL.ARN)
	wafWriteJSON(w, map[string]string{"NextLockToken": stored.LockToken})
}

type wafDeleteReq struct {
	Name      string `json:"Name"`
	Scope     string `json:"Scope"`
	Id        string `json:"Id"`
	LockToken string `json:"LockToken"`
}

func handleWAFDeleteWebACL(w http.ResponseWriter, r *http.Request) {
	var req wafDeleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.Id)
	stored, ok := wafWebACLs.Get(key)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "WebACL not found")
		return
	}
	if req.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match current value")
		return
	}
	// Refuse delete when associated. Mirrors real AWS.
	for _, association := range wafAssociations.List() {
		if association.WebACLARN == stored.WebACL.ARN {
			wafWriteError(w, "WAFAssociatedItemException", "WebACL is still associated with one or more resources")
			ok = false
			break
		}
	}
	if !ok {
		return
	}
	wafWebACLs.Delete(key)
	wafDeleteRateWindows(stored.WebACL.ARN)
	wafWriteJSON(w, struct{}{})
}

func wafDeleteRateWindows(webACLARN string) {
	for _, window := range wafRateWindows.Filter(func(window wafRateWindow) bool {
		return window.WebACLARN == webACLARN
	}) {
		wafRateWindows.Delete(window.Key)
	}
}

type wafListReq struct {
	Scope      string `json:"Scope"`
	NextMarker string `json:"NextMarker,omitempty"`
	Limit      int    `json:"Limit,omitempty"`
}

func handleWAFListWebACLs(w http.ResponseWriter, r *http.Request) {
	var req wafListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	type summary struct {
		Name        string `json:"Name"`
		Id          string `json:"Id"`
		Description string `json:"Description,omitempty"`
		LockToken   string `json:"LockToken"`
		ARN         string `json:"ARN"`
	}
	items := []summary{}
	for _, s := range wafWebACLs.List() {
		if s.Scope != req.Scope {
			continue
		}
		items = append(items, summary{
			Name: s.WebACL.Name, Id: s.WebACL.Id,
			Description: s.WebACL.Description, LockToken: s.LockToken, ARN: s.WebACL.ARN,
		})
	}
	sortBy(items, func(s summary) string { return s.Name })
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{"WebACLs": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

// ---------- Association handlers ----------

type wafAssocReq struct {
	WebACLArn   string `json:"WebACLArn"`
	ResourceArn string `json:"ResourceArn"`
}

func handleWAFAssociateWebACL(w http.ResponseWriter, r *http.Request) {
	var req wafAssocReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.WebACLArn == "" || req.ResourceArn == "" {
		wafWriteError(w, "WAFInvalidParameterException", "WebACLArn and ResourceArn are required")
		return
	}
	webACL, ok := wafWebACLByARN(req.WebACLArn)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "WebACL not found")
		return
	}
	switch {
	case strings.Contains(req.ResourceArn, ":amplify:") && strings.Contains(req.ResourceArn, ":apps/"):
		if webACL.Scope != "CLOUDFRONT" {
			wafWriteError(w, "WAFInvalidParameterException", "AWS Amplify requires a CLOUDFRONT WebACL")
			return
		}
		if !amplifySetWAFConfiguration(req.ResourceArn, req.WebACLArn) {
			wafWriteError(w, "WAFUnavailableEntityException", "Amplify app not found")
			return
		}
	case strings.Contains(req.ResourceArn, ":cloudfront::") && strings.Contains(req.ResourceArn, ":distribution/"):
		if webACL.Scope != "CLOUDFRONT" {
			wafWriteError(w, "WAFInvalidParameterException", "Amazon CloudFront requires a CLOUDFRONT WebACL")
			return
		}
		if _, exists := cfDistributions.Get(cfDistributionIDFromARN(req.ResourceArn)); !exists {
			wafWriteError(w, "WAFUnavailableEntityException", "CloudFront distribution not found")
			return
		}
	case strings.Contains(req.ResourceArn, ":elasticloadbalancing:") && strings.Contains(req.ResourceArn, ":loadbalancer/app/"):
		if webACL.Scope != "REGIONAL" {
			wafWriteError(w, "WAFInvalidParameterException", "Application Load Balancers require a REGIONAL WebACL")
			return
		}
		if _, exists := elbv2LoadBalancers.Get(req.ResourceArn); !exists {
			wafWriteError(w, "WAFUnavailableEntityException", "Application Load Balancer not found")
			return
		}
	default:
		wafWriteError(w, "WAFInvalidParameterException", "resource ARN is not an associable resource")
		return
	}
	wafAssociations.Put(req.ResourceArn, wafAssociation{
		ResourceARN: req.ResourceArn,
		WebACLARN:   req.WebACLArn,
	})
	wafWriteJSON(w, struct{}{})
}

type wafDisassocReq struct {
	ResourceArn string `json:"ResourceArn"`
}

func handleWAFDisassociateWebACL(w http.ResponseWriter, r *http.Request) {
	var req wafDisassocReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.ResourceArn == "" {
		wafWriteError(w, "WAFInvalidParameterException", "ResourceArn is required")
		return
	}
	if _, associated := wafAssociations.Get(req.ResourceArn); !associated {
		wafWriteError(w, "WAFNonexistentItemException", "resource has no WebACL association")
		return
	}
	wafAssociations.Delete(req.ResourceArn)
	if strings.Contains(req.ResourceArn, ":amplify:") {
		amplifyClearWAFConfiguration(req.ResourceArn)
	}
	wafWriteJSON(w, struct{}{})
}

func handleWAFGetWebACLForResource(w http.ResponseWriter, r *http.Request) {
	var req wafDisassocReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.ResourceArn == "" {
		wafWriteError(w, "WAFInvalidParameterException", "ResourceArn is required")
		return
	}
	association, _ := wafAssociations.Get(req.ResourceArn)
	arn := association.WebACLARN
	if arn == "" {
		wafWriteJSON(w, struct{}{})
		return
	}
	// Find the WebACL by ARN
	for _, s := range wafWebACLs.List() {
		if s.WebACL.ARN == arn {
			wafWriteJSON(w, map[string]any{"WebACL": s.WebACL})
			return
		}
	}
	wafWriteJSON(w, struct{}{})
}

func wafWebACLByARN(arn string) (wafStoredWebACL, bool) {
	for _, stored := range wafWebACLs.List() {
		if stored.WebACL.ARN == arn {
			return stored, true
		}
	}
	return wafStoredWebACL{}, false
}

type wafListResourcesReq struct {
	WebACLArn    string `json:"WebACLArn"`
	ResourceType string `json:"ResourceType,omitempty"`
}

func handleWAFListResourcesForWebACL(w http.ResponseWriter, r *http.Request) {
	var req wafListResourcesReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	arns := []string{}
	for _, association := range wafAssociations.List() {
		if association.WebACLARN == req.WebACLArn {
			arns = append(arns, association.ResourceARN)
		}
	}
	wafWriteJSON(w, map[string]any{"ResourceArns": arns})
}

// ---------- IPSet handlers ----------

type wafCreateIPSetReq struct {
	Name             string   `json:"Name"`
	Scope            string   `json:"Scope"`
	Description      string   `json:"Description,omitempty"`
	IPAddressVersion string   `json:"IPAddressVersion"`
	Addresses        []string `json:"Addresses"`
	Tags             []wafTag `json:"Tags,omitempty"`
}

func handleWAFCreateIPSet(w http.ResponseWriter, r *http.Request) {
	var req wafCreateIPSetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.Name == "" || req.Scope == "" || req.IPAddressVersion == "" {
		wafWriteError(w, "WAFInvalidParameterException", "Name, Scope, IPAddressVersion are required")
		return
	}
	if !wafValidateName(w, req.Name) {
		return
	}
	for _, s := range wafIPSets.List() {
		if s.Scope == req.Scope && s.IPSet.Name == req.Name {
			wafWriteDuplicate(w, "IPSet", req.Name)
			return
		}
	}
	if req.Addresses == nil {
		req.Addresses = []string{}
	}
	id := wafRandomID()
	lock := wafLockToken()
	ipset := WAFIPSet{
		Name: req.Name, Id: id,
		ARN:              wafARN(req.Scope, "ipset", req.Name, id),
		Description:      req.Description,
		IPAddressVersion: req.IPAddressVersion,
		Addresses:        req.Addresses,
	}
	wafIPSets.Put(wafKey(req.Scope, id), wafStoredIPSet{IPSet: ipset, Scope: req.Scope, LockToken: lock, Tags: req.Tags})
	wafWriteJSON(w, map[string]any{
		"Summary": wafEntitySummary(ipset.Name, ipset.Id, ipset.Description, lock, ipset.ARN),
	})
}

func handleWAFGetIPSet(w http.ResponseWriter, r *http.Request) {
	var req wafGetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	stored, ok := wafIPSets.Get(wafKey(req.Scope, req.Id))
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "IPSet not found")
		return
	}
	wafWriteJSON(w, map[string]any{"IPSet": stored.IPSet, "LockToken": stored.LockToken})
}

type wafUpdateIPSetReq struct {
	Name        string   `json:"Name"`
	Scope       string   `json:"Scope"`
	Id          string   `json:"Id"`
	Description string   `json:"Description,omitempty"`
	Addresses   []string `json:"Addresses"`
	LockToken   string   `json:"LockToken"`
}

func handleWAFUpdateIPSet(w http.ResponseWriter, r *http.Request) {
	var req wafUpdateIPSetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.Id)
	stored, ok := wafIPSets.Get(key)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "IPSet not found")
		return
	}
	if req.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match")
		return
	}
	if req.Addresses == nil {
		req.Addresses = []string{}
	}
	stored.IPSet.Description = req.Description
	stored.IPSet.Addresses = req.Addresses
	stored.LockToken = wafLockToken()
	wafIPSets.Put(key, stored)
	wafWriteJSON(w, map[string]string{"NextLockToken": stored.LockToken})
}

func handleWAFDeleteIPSet(w http.ResponseWriter, r *http.Request) {
	var req wafDeleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.Id)
	stored, ok := wafIPSets.Get(key)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "IPSet not found")
		return
	}
	if req.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match")
		return
	}
	wafIPSets.Delete(key)
	wafWriteJSON(w, struct{}{})
}

func handleWAFListIPSets(w http.ResponseWriter, r *http.Request) {
	var req wafListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	type summary struct {
		Name        string `json:"Name"`
		Id          string `json:"Id"`
		Description string `json:"Description,omitempty"`
		LockToken   string `json:"LockToken"`
		ARN         string `json:"ARN"`
	}
	items := []summary{}
	for _, s := range wafIPSets.List() {
		if s.Scope != req.Scope {
			continue
		}
		items = append(items, summary{
			Name: s.IPSet.Name, Id: s.IPSet.Id,
			Description: s.IPSet.Description, LockToken: s.LockToken, ARN: s.IPSet.ARN,
		})
	}
	sortBy(items, func(s summary) string { return s.Name })
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{"IPSets": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

// ---------- RuleGroup handlers ----------

type wafCreateRuleGroupReq struct {
	Name                 string          `json:"Name"`
	Scope                string          `json:"Scope"`
	Capacity             int64           `json:"Capacity"`
	Description          string          `json:"Description,omitempty"`
	Rules                json.RawMessage `json:"Rules,omitempty"`
	VisibilityConfig     json.RawMessage `json:"VisibilityConfig"`
	Tags                 []wafTag        `json:"Tags,omitempty"`
	CustomResponseBodies json.RawMessage `json:"CustomResponseBodies,omitempty"`
}

func handleWAFCreateRuleGroup(w http.ResponseWriter, r *http.Request) {
	var req wafCreateRuleGroupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.Name == "" || req.Scope == "" {
		wafWriteError(w, "WAFInvalidParameterException", "Name and Scope are required")
		return
	}
	if !wafValidateName(w, req.Name) {
		return
	}
	for _, s := range wafRuleGroups.List() {
		if s.Scope == req.Scope && s.RuleGroup.Name == req.Name {
			wafWriteDuplicate(w, "RuleGroup", req.Name)
			return
		}
	}
	id := wafRandomID()
	lock := wafLockToken()
	rg := WAFRuleGroup{
		Name: req.Name, Id: id,
		ARN:                  wafARN(req.Scope, "rulegroup", req.Name, id),
		Capacity:             req.Capacity,
		Description:          req.Description,
		Rules:                req.Rules,
		VisibilityConfig:     req.VisibilityConfig,
		CustomResponseBodies: req.CustomResponseBodies,
		LabelNamespace:       "awswaf:" + awsAccountID() + ":rulegroup:" + req.Name + ":",
	}
	wafRuleGroups.Put(wafKey(req.Scope, id), wafStoredRuleGroup{RuleGroup: rg, Scope: req.Scope, LockToken: lock, Tags: req.Tags})
	wafWriteJSON(w, map[string]any{
		"Summary": wafEntitySummary(rg.Name, rg.Id, rg.Description, lock, rg.ARN),
	})
}

func handleWAFGetRuleGroup(w http.ResponseWriter, r *http.Request) {
	var req wafGetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	stored, ok := wafRuleGroups.Get(wafKey(req.Scope, req.Id))
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "RuleGroup not found")
		return
	}
	wafWriteJSON(w, map[string]any{"RuleGroup": stored.RuleGroup, "LockToken": stored.LockToken})
}

func handleWAFUpdateRuleGroup(w http.ResponseWriter, r *http.Request) {
	var req wafCreateRuleGroupReq
	var idReq struct {
		Id        string `json:"Id"`
		LockToken string `json:"LockToken"`
	}
	body, err := readBodyJSON(r.Body)
	if err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not read body: "+err.Error())
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if err := json.Unmarshal(body, &idReq); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, idReq.Id)
	stored, ok := wafRuleGroups.Get(key)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "RuleGroup not found")
		return
	}
	if idReq.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match")
		return
	}
	stored.RuleGroup.Description = req.Description
	stored.RuleGroup.Rules = req.Rules
	stored.RuleGroup.VisibilityConfig = req.VisibilityConfig
	stored.RuleGroup.CustomResponseBodies = req.CustomResponseBodies
	stored.LockToken = wafLockToken()
	wafRuleGroups.Put(key, stored)
	wafWriteJSON(w, map[string]string{"NextLockToken": stored.LockToken})
}

func readBodyJSON(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	// Drain the body into memory so we can parse it twice.
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return buf, err
		}
	}
	return buf, nil
}

func handleWAFDeleteRuleGroup(w http.ResponseWriter, r *http.Request) {
	var req wafDeleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.Id)
	stored, ok := wafRuleGroups.Get(key)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "RuleGroup not found")
		return
	}
	if req.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match")
		return
	}
	wafRuleGroups.Delete(key)
	wafWriteJSON(w, struct{}{})
}

func handleWAFListRuleGroups(w http.ResponseWriter, r *http.Request) {
	var req wafListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	type summary struct {
		Name        string `json:"Name"`
		Id          string `json:"Id"`
		Description string `json:"Description,omitempty"`
		LockToken   string `json:"LockToken"`
		ARN         string `json:"ARN"`
	}
	items := []summary{}
	for _, s := range wafRuleGroups.List() {
		if s.Scope != req.Scope {
			continue
		}
		items = append(items, summary{Name: s.RuleGroup.Name, Id: s.RuleGroup.Id, Description: s.RuleGroup.Description, LockToken: s.LockToken, ARN: s.RuleGroup.ARN})
	}
	sortBy(items, func(s summary) string { return s.Name })
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{"RuleGroups": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

// ---------- RegexPatternSet handlers ----------

type wafCreateRegexReq struct {
	Name                  string          `json:"Name"`
	Scope                 string          `json:"Scope"`
	Description           string          `json:"Description,omitempty"`
	RegularExpressionList json.RawMessage `json:"RegularExpressionList,omitempty"`
	Tags                  []wafTag        `json:"Tags,omitempty"`
}

func handleWAFCreateRegexSet(w http.ResponseWriter, r *http.Request) {
	var req wafCreateRegexReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.Name == "" || req.Scope == "" {
		wafWriteError(w, "WAFInvalidParameterException", "Name and Scope are required")
		return
	}
	if !wafValidateName(w, req.Name) {
		return
	}
	for _, s := range wafRegexSets.List() {
		if s.Scope == req.Scope && s.RegexSet.Name == req.Name {
			wafWriteDuplicate(w, "RegexPatternSet", req.Name)
			return
		}
	}
	id := wafRandomID()
	lock := wafLockToken()
	rs := WAFRegexPatternSet{
		Name: req.Name, Id: id,
		ARN:                   wafARN(req.Scope, "regexpatternset", req.Name, id),
		Description:           req.Description,
		RegularExpressionList: req.RegularExpressionList,
	}
	wafRegexSets.Put(wafKey(req.Scope, id), wafStoredRegex{RegexSet: rs, Scope: req.Scope, LockToken: lock, Tags: req.Tags})
	wafWriteJSON(w, map[string]any{
		"Summary": wafEntitySummary(rs.Name, rs.Id, rs.Description, lock, rs.ARN),
	})
}

func handleWAFGetRegexSet(w http.ResponseWriter, r *http.Request) {
	var req wafGetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	stored, ok := wafRegexSets.Get(wafKey(req.Scope, req.Id))
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "RegexPatternSet not found")
		return
	}
	wafWriteJSON(w, map[string]any{"RegexPatternSet": stored.RegexSet, "LockToken": stored.LockToken})
}

type wafUpdateRegexReq struct {
	Name                  string          `json:"Name"`
	Scope                 string          `json:"Scope"`
	Id                    string          `json:"Id"`
	Description           string          `json:"Description,omitempty"`
	RegularExpressionList json.RawMessage `json:"RegularExpressionList,omitempty"`
	LockToken             string          `json:"LockToken"`
}

func handleWAFUpdateRegexSet(w http.ResponseWriter, r *http.Request) {
	var req wafUpdateRegexReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.Id)
	stored, ok := wafRegexSets.Get(key)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "RegexPatternSet not found")
		return
	}
	if req.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match")
		return
	}
	stored.RegexSet.Description = req.Description
	stored.RegexSet.RegularExpressionList = req.RegularExpressionList
	stored.LockToken = wafLockToken()
	wafRegexSets.Put(key, stored)
	wafWriteJSON(w, map[string]string{"NextLockToken": stored.LockToken})
}

func handleWAFDeleteRegexSet(w http.ResponseWriter, r *http.Request) {
	var req wafDeleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.Id)
	stored, ok := wafRegexSets.Get(key)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "RegexPatternSet not found")
		return
	}
	if req.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match")
		return
	}
	wafRegexSets.Delete(key)
	wafWriteJSON(w, struct{}{})
}

func handleWAFListRegexSets(w http.ResponseWriter, r *http.Request) {
	var req wafListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	type summary struct {
		Name        string `json:"Name"`
		Id          string `json:"Id"`
		Description string `json:"Description,omitempty"`
		LockToken   string `json:"LockToken"`
		ARN         string `json:"ARN"`
	}
	items := []summary{}
	for _, s := range wafRegexSets.List() {
		if s.Scope != req.Scope {
			continue
		}
		items = append(items, summary{Name: s.RegexSet.Name, Id: s.RegexSet.Id, Description: s.RegexSet.Description, LockToken: s.LockToken, ARN: s.RegexSet.ARN})
	}
	sortBy(items, func(s summary) string { return s.Name })
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{"RegexPatternSets": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

// ---------- Tagging handlers ----------

func wafGetTagsByARN(arn string) ([]wafTag, bool) {
	for _, s := range wafWebACLs.List() {
		if s.WebACL.ARN == arn {
			return s.Tags, true
		}
	}
	for _, s := range wafIPSets.List() {
		if s.IPSet.ARN == arn {
			return s.Tags, true
		}
	}
	for _, s := range wafRuleGroups.List() {
		if s.RuleGroup.ARN == arn {
			return s.Tags, true
		}
	}
	for _, s := range wafRegexSets.List() {
		if s.RegexSet.ARN == arn {
			return s.Tags, true
		}
	}
	return nil, false
}

func wafSetTagsByARN(arn string, tags []wafTag) bool {
	for _, s := range wafWebACLs.List() {
		if s.WebACL.ARN == arn {
			s.Tags = tags
			wafWebACLs.Put(wafKey(s.Scope, s.WebACL.Id), s)
			return true
		}
	}
	for _, s := range wafIPSets.List() {
		if s.IPSet.ARN == arn {
			s.Tags = tags
			wafIPSets.Put(wafKey(s.Scope, s.IPSet.Id), s)
			return true
		}
	}
	for _, s := range wafRuleGroups.List() {
		if s.RuleGroup.ARN == arn {
			s.Tags = tags
			wafRuleGroups.Put(wafKey(s.Scope, s.RuleGroup.Id), s)
			return true
		}
	}
	for _, s := range wafRegexSets.List() {
		if s.RegexSet.ARN == arn {
			s.Tags = tags
			wafRegexSets.Put(wafKey(s.Scope, s.RegexSet.Id), s)
			return true
		}
	}
	return false
}

type wafTagReq struct {
	ResourceARN string   `json:"ResourceARN"`
	Tags        []wafTag `json:"Tags"`
}

func handleWAFTagResource(w http.ResponseWriter, r *http.Request) {
	var req wafTagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	existing, ok := wafGetTagsByARN(req.ResourceARN)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "Resource not found")
		return
	}
	tagMap := map[string]string{}
	for _, t := range existing {
		tagMap[t.Key] = t.Value
	}
	for _, t := range req.Tags {
		tagMap[t.Key] = t.Value
	}
	merged := make([]wafTag, 0, len(tagMap))
	for k, v := range tagMap {
		merged = append(merged, wafTag{Key: k, Value: v})
	}
	wafSetTagsByARN(req.ResourceARN, merged)
	wafWriteJSON(w, struct{}{})
}

type wafUntagReq struct {
	ResourceARN string   `json:"ResourceARN"`
	TagKeys     []string `json:"TagKeys"`
}

func handleWAFUntagResource(w http.ResponseWriter, r *http.Request) {
	var req wafUntagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	existing, ok := wafGetTagsByARN(req.ResourceARN)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "Resource not found")
		return
	}
	drop := map[string]bool{}
	for _, k := range req.TagKeys {
		drop[k] = true
	}
	kept := existing[:0]
	for _, t := range existing {
		if !drop[t.Key] {
			kept = append(kept, t)
		}
	}
	wafSetTagsByARN(req.ResourceARN, kept)
	wafWriteJSON(w, struct{}{})
}

type wafListTagsReq struct {
	ResourceARN string `json:"ResourceARN"`
}

func handleWAFListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req wafListTagsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	tags, ok := wafGetTagsByARN(req.ResourceARN)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "Resource not found")
		return
	}
	tagList := tags
	if tagList == nil {
		tagList = []wafTag{}
	}
	wafWriteJSON(w, map[string]any{
		"TagInfoForResource": map[string]any{
			"ResourceARN": req.ResourceARN,
			"TagList":     tagList,
		},
	})
}

// ---------- LoggingConfiguration handlers ----------

// wafScopeFromARN derives the WAFv2 Scope from a web ACL ARN. The path
// component is "global/" for CLOUDFRONT and "regional/" for REGIONAL.
func wafScopeFromARN(arn string) string {
	if strings.Contains(arn, ":global/") {
		return "CLOUDFRONT"
	}
	return "REGIONAL"
}

// wafWebACLExistsByARN reports whether a web ACL with the given ARN exists.
// PutLoggingConfiguration targets a web ACL, so the sim validates it the way
// real AWS does (WAFNonexistentItemException for an unknown web ACL).
func wafWebACLExistsByARN(arn string) bool {
	for _, s := range wafWebACLs.List() {
		if s.WebACL.ARN == arn {
			return true
		}
	}
	return false
}

type wafPutLoggingReq struct {
	LoggingConfiguration json.RawMessage `json:"LoggingConfiguration"`
}

func handleWAFPutLoggingConfiguration(w http.ResponseWriter, r *http.Request) {
	var req wafPutLoggingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if len(req.LoggingConfiguration) == 0 {
		wafWriteError(w, "WAFInvalidParameterException", "LoggingConfiguration is required")
		return
	}
	var meta struct {
		ResourceArn           string          `json:"ResourceArn"`
		LogDestinationConfigs json.RawMessage `json:"LogDestinationConfigs"`
	}
	if err := json.Unmarshal(req.LoggingConfiguration, &meta); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode LoggingConfiguration: "+err.Error())
		return
	}
	if meta.ResourceArn == "" {
		wafWriteError(w, "WAFInvalidParameterException", "LoggingConfiguration.ResourceArn is required")
		return
	}
	if len(meta.LogDestinationConfigs) == 0 {
		wafWriteError(w, "WAFInvalidParameterException", "LoggingConfiguration.LogDestinationConfigs is required")
		return
	}
	if !wafWebACLExistsByARN(meta.ResourceArn) {
		wafWriteError(w, "WAFNonexistentItemException", "WebACL not found for ResourceArn")
		return
	}
	wafLogging.Put(meta.ResourceArn, wafStoredLogging{
		ResourceArn: meta.ResourceArn,
		Scope:       wafScopeFromARN(meta.ResourceArn),
		Config:      append(json.RawMessage(nil), req.LoggingConfiguration...),
	})
	wafWriteJSON(w, map[string]any{"LoggingConfiguration": req.LoggingConfiguration})
}

type wafGetLoggingReq struct {
	ResourceArn string `json:"ResourceArn"`
}

func handleWAFGetLoggingConfiguration(w http.ResponseWriter, r *http.Request) {
	var req wafGetLoggingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	stored, ok := wafLogging.Get(req.ResourceArn)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "LoggingConfiguration not found")
		return
	}
	wafWriteJSON(w, map[string]any{"LoggingConfiguration": stored.Config})
}

func handleWAFDeleteLoggingConfiguration(w http.ResponseWriter, r *http.Request) {
	var req wafGetLoggingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if _, ok := wafLogging.Get(req.ResourceArn); !ok {
		wafWriteError(w, "WAFNonexistentItemException", "LoggingConfiguration not found")
		return
	}
	wafLogging.Delete(req.ResourceArn)
	wafWriteJSON(w, struct{}{})
}

func handleWAFListLoggingConfigurations(w http.ResponseWriter, r *http.Request) {
	var req wafListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	items := []json.RawMessage{}
	for _, s := range wafLogging.List() {
		if s.Scope != req.Scope {
			continue
		}
		items = append(items, s.Config)
	}
	sortBy(items, func(c json.RawMessage) string {
		var m struct {
			ResourceArn string `json:"ResourceArn"`
		}
		_ = json.Unmarshal(c, &m)
		return m.ResourceArn
	})
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{"LoggingConfigurations": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

// ---------- Request inspection and GetSampledRequests ----------

type wafVisibilityConfig struct {
	MetricName             string `json:"MetricName"`
	SampledRequestsEnabled bool   `json:"SampledRequestsEnabled"`
}

type wafRule struct {
	Name           string                     `json:"Name"`
	Priority       int                        `json:"Priority"`
	Action         map[string]json.RawMessage `json:"Action"`
	OverrideAction map[string]json.RawMessage `json:"OverrideAction"`
	Statement      json.RawMessage            `json:"Statement"`
	RuleLabels     []struct {
		Name string `json:"Name"`
	} `json:"RuleLabels"`
	VisibilityConfig wafVisibilityConfig `json:"VisibilityConfig"`
}

func wafAssociatedRequestAllowed(resourceARN string, r *http.Request) bool {
	association, associated := wafAssociations.Get(resourceARN)
	if !associated {
		return true
	}
	webACLARN := association.WebACLARN
	stored, ok := wafWebACLByARN(webACLARN)
	if !ok {
		return true
	}
	evaluation := wafNewEvaluation(r, stored.WebACL.ARN)

	var rules []wafRule
	_ = json.Unmarshal(stored.WebACL.Rules, &rules)
	sortWAFRules(rules)
	for _, rule := range rules {
		evaluation.ruleName = rule.Name
		result := wafEvaluateStatement(rule.Statement, evaluation, 0)
		if !result.matched {
			continue
		}
		wafApplyRuleLabels(rule, stored.WebACL.LabelNamespace, evaluation)
		action, terminal := wafRuleAction(rule.Action)
		if result.action != "" {
			action, terminal = result.action, result.terminal
			if _, count := rule.OverrideAction["Count"]; count {
				action, terminal = "COUNT", false
			}
		}
		if rule.VisibilityConfig.SampledRequestsEnabled {
			wafRecordSample(stored.WebACL.ARN, rule.VisibilityConfig.MetricName, action, evaluation.clientIP, r, action == "BLOCK", wafEvaluationLabels(evaluation))
		}
		if terminal {
			return action != "BLOCK"
		}
	}

	var visibility wafVisibilityConfig
	_ = json.Unmarshal(stored.WebACL.VisibilityConfig, &visibility)
	action := "ALLOW"
	var defaultAction map[string]json.RawMessage
	_ = json.Unmarshal(stored.WebACL.DefaultAction, &defaultAction)
	if _, blocked := defaultAction["Block"]; blocked {
		action = "BLOCK"
	}
	if visibility.SampledRequestsEnabled {
		wafRecordSample(stored.WebACL.ARN, visibility.MetricName, action, evaluation.clientIP, r, action == "BLOCK", wafEvaluationLabels(evaluation))
	}
	return action != "BLOCK"
}

func wafEvaluationLabels(evaluation *wafEvaluation) []string {
	labels := make([]string, 0, len(evaluation.labels))
	for label := range evaluation.labels {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

func wafRuleAction(action map[string]json.RawMessage) (string, bool) {
	if _, ok := action["Block"]; ok {
		return "BLOCK", true
	}
	if _, ok := action["Allow"]; ok {
		return "ALLOW", true
	}
	if _, ok := action["Count"]; ok {
		return "COUNT", false
	}
	if _, ok := action["Captcha"]; ok {
		return "CAPTCHA", true
	}
	if _, ok := action["Challenge"]; ok {
		return "CHALLENGE", true
	}
	return "COUNT", false
}

func wafRequestClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func wafRecordSample(webACLARN, metricName, action, clientIP string, r *http.Request, blocked bool, labels []string) {
	headers := make([]wafSampledHTTPHeader, 0, len(r.Header))
	for name, values := range r.Header {
		for _, value := range values {
			headers = append(headers, wafSampledHTTPHeader{Name: name, Value: value})
		}
	}
	sort.Slice(headers, func(i, j int) bool {
		if headers[i].Name == headers[j].Name {
			return headers[i].Value < headers[j].Value
		}
		return headers[i].Name < headers[j].Name
	})
	responseCode := 0
	if blocked {
		responseCode = http.StatusForbidden
	}
	wafSampledRequests.Put(wafRandomID(), wafSampledRequest{
		WebACLARN: webACLARN, RuleMetricName: metricName, Action: action,
		Timestamp: time.Now().UTC(), ClientIP: clientIP, HTTPVersion: r.Proto,
		Headers: headers, Method: r.Method, URI: r.URL.RequestURI(),
		ResponseCodeSent: responseCode, Labels: labels,
	})
}

type wafGetSampledRequestsReq struct {
	WebACLARN      string `json:"WebAclArn"`
	RuleMetricName string `json:"RuleMetricName"`
	Scope          string `json:"Scope"`
	TimeWindow     struct {
		StartTime float64 `json:"StartTime"`
		EndTime   float64 `json:"EndTime"`
	} `json:"TimeWindow"`
	MaxItems int64 `json:"MaxItems"`
}

func handleWAFGetSampledRequests(w http.ResponseWriter, r *http.Request) {
	var req wafGetSampledRequestsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	stored, ok := wafWebACLByARN(req.WebACLARN)
	if !ok || stored.Scope != req.Scope {
		wafWriteError(w, "WAFNonexistentItemException", "WebACL not found")
		return
	}
	if req.RuleMetricName == "" || req.MaxItems < 1 || req.TimeWindow.EndTime <= req.TimeWindow.StartTime {
		wafWriteError(w, "WAFInvalidParameterException", "RuleMetricName, MaxItems, and a valid TimeWindow are required")
		return
	}
	end := time.Unix(int64(req.TimeWindow.EndTime), 0).UTC()
	start := time.Unix(int64(req.TimeWindow.StartTime), 0).UTC()
	if earliest := time.Now().UTC().Add(-3 * time.Hour); start.Before(earliest) {
		start = earliest
	}
	var matches []wafSampledRequest
	for _, sample := range wafSampledRequests.List() {
		if sample.WebACLARN == req.WebACLARN &&
			sample.RuleMetricName == req.RuleMetricName &&
			!sample.Timestamp.Before(start) && !sample.Timestamp.After(end) {
			matches = append(matches, sample)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Timestamp.Before(matches[j].Timestamp) })
	populationSize := len(matches)
	if int64(len(matches)) > req.MaxItems {
		matches = matches[:req.MaxItems]
	}
	sampled := make([]map[string]any, 0, len(matches))
	for _, sample := range matches {
		headers := make([]map[string]string, 0, len(sample.Headers))
		for _, header := range sample.Headers {
			headers = append(headers, map[string]string{"Name": header.Name, "Value": header.Value})
		}
		item := map[string]any{
			"Action": sample.Action,
			"Request": map[string]any{
				"ClientIP": sample.ClientIP, "HTTPVersion": sample.HTTPVersion,
				"Headers": headers, "Method": sample.Method, "URI": sample.URI,
			},
			"Timestamp": float64(sample.Timestamp.UnixNano()) / float64(time.Second),
			"Weight":    1,
		}
		if sample.ResponseCodeSent != 0 {
			item["ResponseCodeSent"] = sample.ResponseCodeSent
		}
		if len(sample.Labels) > 0 {
			labels := make([]map[string]string, 0, len(sample.Labels))
			for _, label := range sample.Labels {
				labels = append(labels, map[string]string{"Name": label})
			}
			item["Labels"] = labels
		}
		sampled = append(sampled, item)
	}
	wafWriteJSON(w, map[string]any{
		"SampledRequests": sampled,
		"PopulationSize":  populationSize,
		"TimeWindow": map[string]float64{
			"StartTime": float64(start.UnixNano()) / float64(time.Second),
			"EndTime":   float64(end.UnixNano()) / float64(time.Second),
		},
	})
}

type wafRevenueTimeWindow struct {
	StartTime float64 `json:"StartTime"`
	EndTime   float64 `json:"EndTime"`
}

type wafRevenueRequest struct {
	StatisticType string               `json:"StatisticType"`
	TimeWindow    wafRevenueTimeWindow `json:"TimeWindow"`
	Scope         string               `json:"Scope"`
	Currency      string               `json:"Currency"`
	GroupBy       string               `json:"GroupBy,omitempty"`
	Interval      string               `json:"Interval,omitempty"`
	Limit         int                  `json:"Limit,omitempty"`
}

func wafValidateRevenueRequest(w http.ResponseWriter, request wafRevenueRequest) bool {
	if request.Scope != "CLOUDFRONT" {
		wafWriteError(w, "WAFInvalidOperationException", "AI bot monetization is available only for CLOUDFRONT scope")
		return false
	}
	if request.Currency != "USDC" {
		wafWriteError(w, "WAFInvalidParameterException", "Currency must be USDC")
		return false
	}
	start := time.Unix(0, int64(request.TimeWindow.StartTime*float64(time.Second)))
	end := time.Unix(0, int64(request.TimeWindow.EndTime*float64(time.Second)))
	if !end.After(start) || end.Sub(start) > 90*24*time.Hour {
		wafWriteError(w, "WAFInvalidParameterException", "TimeWindow must be positive and no longer than 90 days")
		return false
	}
	return true
}

func handleWAFGetRevenueStatistics(w http.ResponseWriter, r *http.Request) {
	var request wafRevenueRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if !wafValidateRevenueRequest(w, request) {
		return
	}
	switch request.StatisticType {
	case "TOP_SOURCES_BY_REVENUE":
		if request.GroupBy == "" {
			wafWriteError(w, "WAFInvalidParameterException", "GroupBy is required for TOP_SOURCES_BY_REVENUE")
			return
		}
		wafWriteJSON(w, map[string]any{"SourceStatistics": []any{}})
	case "TOP_PATHS_BY_REVENUE":
		wafWriteJSON(w, map[string]any{"RevenuePathStatistics": []any{}})
	default:
		wafWriteError(w, "WAFInvalidParameterException", "StatisticType must be TOP_SOURCES_BY_REVENUE or TOP_PATHS_BY_REVENUE")
	}
}

func handleWAFGetRevenueStatisticsSummary(w http.ResponseWriter, r *http.Request) {
	var request wafRevenueRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if !wafValidateRevenueRequest(w, request) {
		return
	}
	wafWriteJSON(w, map[string]any{
		"RevenueBreakdown": map[string]any{
			"TotalAmount":         "0",
			"VerifiedAmount":      "0",
			"UnverifiedAmount":    "0",
			"Currency":            request.Currency,
			"TotalSettled":        0,
			"TotalMonetizeServed": 0,
		},
	})
}

func handleWAFGetRevenueStatisticsTimeSeries(w http.ResponseWriter, r *http.Request) {
	var request wafRevenueRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if !wafValidateRevenueRequest(w, request) {
		return
	}
	switch request.StatisticType {
	case "DATE_HISTOGRAM", "PAYMENT_TRAFFIC":
	default:
		wafWriteError(w, "WAFInvalidParameterException", "StatisticType must be DATE_HISTOGRAM or PAYMENT_TRAFFIC")
		return
	}
	switch request.Interval {
	case "MINUTELY", "FIVE_MINUTELY", "HOURLY", "DAILY":
	default:
		wafWriteError(w, "WAFInvalidParameterException", "Interval must be MINUTELY, FIVE_MINUTELY, HOURLY, or DAILY")
		return
	}
	wafWriteJSON(w, map[string]any{"DataPoints": []any{}})
}

func handleWAFListSettlementRecords(w http.ResponseWriter, r *http.Request) {
	var request wafRevenueRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if !wafValidateRevenueRequest(w, request) {
		return
	}
	wafWriteJSON(w, map[string]any{"Settlements": []any{}})
}

// ---------- API keys ----------

// wafEncodeAPIKey produces a wire-form APIKey token. Real AWS returns an opaque
// encrypted base64 token whose plaintext is the scope, token domains, and a
// creation timestamp; GetDecryptedAPIKey reverses it. The sim base64-encodes a
// JSON envelope of the same fields so the round-trip is faithful and the token
// is genuinely self-describing (no out-of-band lookup needed to decrypt it).
func wafEncodeAPIKey(scope string, domains []string, created time.Time) string {
	env := struct {
		Scope        string   `json:"s"`
		TokenDomains []string `json:"d"`
		Created      int64    `json:"c"`
	}{Scope: scope, TokenDomains: domains, Created: created.Unix()}
	raw, _ := json.Marshal(env)
	return base64.StdEncoding.EncodeToString(raw)
}

func wafDecodeAPIKey(token string) (scope string, domains []string, created time.Time, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return "", nil, time.Time{}, false
	}
	var env struct {
		Scope        string   `json:"s"`
		TokenDomains []string `json:"d"`
		Created      int64    `json:"c"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", nil, time.Time{}, false
	}
	return env.Scope, env.TokenDomains, time.Unix(env.Created, 0).UTC(), true
}

type wafCreateAPIKeyReq struct {
	Scope        string   `json:"Scope"`
	TokenDomains []string `json:"TokenDomains"`
}

func handleWAFCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req wafCreateAPIKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.Scope == "" {
		wafWriteError(w, "WAFInvalidParameterException", "Scope is required")
		return
	}
	if len(req.TokenDomains) == 0 {
		wafWriteError(w, "WAFInvalidParameterException", "TokenDomains is required")
		return
	}
	created := time.Now().UTC()
	token := wafEncodeAPIKey(req.Scope, req.TokenDomains, created)
	wafAPIKeys.Put(wafKey(req.Scope, token), wafStoredAPIKey{
		APIKey:       token,
		Scope:        req.Scope,
		TokenDomains: req.TokenDomains,
		Created:      created,
		Version:      1,
	})
	wafWriteJSON(w, map[string]any{"APIKey": token})
}

type wafAPIKeyReq struct {
	Scope  string `json:"Scope"`
	APIKey string `json:"APIKey"`
}

func handleWAFDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	var req wafAPIKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.APIKey)
	if _, ok := wafAPIKeys.Get(key); !ok {
		wafWriteError(w, "WAFNonexistentItemException", "APIKey not found")
		return
	}
	wafAPIKeys.Delete(key)
	wafWriteJSON(w, struct{}{})
}

func handleWAFListAPIKeys(w http.ResponseWriter, r *http.Request) {
	var req wafListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	type apiKeySummary struct {
		TokenDomains []string `json:"TokenDomains"`
		APIKey       string   `json:"APIKey"`
		// CreationTimestamp serializes as epoch-seconds for awsJson1.1.
		CreationTimestamp int64 `json:"CreationTimestamp"`
		// Version is the APIKeyVersion integer the SDK expects.
		Version int `json:"Version,omitempty"`
	}
	items := []apiKeySummary{}
	for _, s := range wafAPIKeys.List() {
		if s.Scope != req.Scope {
			continue
		}
		items = append(items, apiKeySummary{
			TokenDomains:      s.TokenDomains,
			APIKey:            s.APIKey,
			CreationTimestamp: s.Created.Unix(),
			Version:           s.Version,
		})
	}
	sortBy(items, func(s apiKeySummary) string { return s.APIKey })
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{
		"APIKeySummaries":           page,
		"ApplicationIntegrationURL": "https://" + awsRegion() + ".console.aws.amazon.com/wafv2/integration/",
	}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

func handleWAFGetDecryptedAPIKey(w http.ResponseWriter, r *http.Request) {
	var req wafAPIKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	stored, ok := wafAPIKeys.Get(wafKey(req.Scope, req.APIKey))
	if !ok {
		// The token is self-describing; if it isn't in the store, try decoding
		// it directly so a key created out-of-band still decrypts faithfully.
		scope, domains, created, decoded := wafDecodeAPIKey(req.APIKey)
		if !decoded || scope != req.Scope {
			wafWriteError(w, "WAFInvalidParameterException", "APIKey is not valid for this scope")
			return
		}
		wafWriteJSON(w, map[string]any{
			"TokenDomains":      domains,
			"CreationTimestamp": created.Unix(),
		})
		return
	}
	wafWriteJSON(w, map[string]any{
		"TokenDomains":      stored.TokenDomains,
		"CreationTimestamp": stored.Created.Unix(),
	})
}

// ---------- CheckCapacity ----------

// wafComputeCapacity sums the WCU cost of a rule list. Real WAFv2 assigns a Web
// ACL Capacity Unit (WCU) cost per statement type; the sim applies a faithful,
// deterministic per-statement cost model over the supplied rules — a base cost
// per rule plus the documented incremental costs for the statement kinds that
// dominate real rule sets. The sum is the same for the same input every time.
func wafComputeCapacity(rules []json.RawMessage) int64 {
	var total int64
	for _, ruleRaw := range rules {
		var rule struct {
			Statement json.RawMessage `json:"Statement"`
		}
		_ = json.Unmarshal(ruleRaw, &rule)
		total += wafStatementCapacity(rule.Statement)
	}
	if total == 0 {
		// An empty rule set still consumes the minimum capacity (>=1) AWS
		// returns; CapacityUnit has a documented minimum of 1.
		return 1
	}
	return total
}

func wafCapacityFromRawRules(raw json.RawMessage) int64 {
	var rules []json.RawMessage
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &rules)
	}
	return wafComputeCapacity(rules)
}

// wafStatementCapacity returns the WCU cost of a single statement, recursing
// into nested statements (And/Or/Not/RateBased) exactly as AWS's capacity model
// does. The per-kind costs mirror the published WCU table.
func wafStatementCapacity(stmtRaw json.RawMessage) int64 {
	if len(stmtRaw) == 0 {
		return 0
	}
	var stmt map[string]json.RawMessage
	if err := json.Unmarshal(stmtRaw, &stmt); err != nil {
		return 1
	}
	var cost int64
	for kind, body := range stmt {
		switch kind {
		case "ByteMatchStatement":
			cost += 1
		case "SqliMatchStatement":
			cost += 20
		case "XssMatchStatement":
			cost += 40
		case "SizeConstraintStatement":
			cost += 1
		case "GeoMatchStatement":
			cost += 1
		case "IPSetReferenceStatement":
			cost += 1
		case "RegexPatternSetReferenceStatement", "RegexMatchStatement":
			cost += 3
		case "LabelMatchStatement":
			cost += 1
		case "RuleGroupReferenceStatement", "ManagedRuleGroupStatement":
			// Reference statements inherit the referenced group's capacity;
			// the sim charges a nominal cost since the referenced bytes
			// aren't supplied to CheckCapacity.
			cost += 0
		case "AndStatement", "OrStatement":
			var nested struct {
				Statements []json.RawMessage `json:"Statements"`
			}
			_ = json.Unmarshal(body, &nested)
			cost += 1
			for _, s := range nested.Statements {
				cost += wafStatementCapacity(s)
			}
		case "NotStatement":
			var nested struct {
				Statement json.RawMessage `json:"Statement"`
			}
			_ = json.Unmarshal(body, &nested)
			cost += 1 + wafStatementCapacity(nested.Statement)
		case "RateBasedStatement":
			var nested struct {
				ScopeDownStatement json.RawMessage `json:"ScopeDownStatement"`
			}
			_ = json.Unmarshal(body, &nested)
			cost += 2 + wafStatementCapacity(nested.ScopeDownStatement)
		default:
			cost += 1
		}
	}
	return cost
}

type wafCheckCapacityReq struct {
	Scope string            `json:"Scope"`
	Rules []json.RawMessage `json:"Rules"`
}

func handleWAFCheckCapacity(w http.ResponseWriter, r *http.Request) {
	var req wafCheckCapacityReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.Scope == "" {
		wafWriteError(w, "WAFInvalidParameterException", "Scope is required")
		return
	}
	wafWriteJSON(w, map[string]any{"Capacity": wafComputeCapacity(req.Rules)})
}

// ---------- Managed rule group / product catalog ----------

// wafManagedRuleGroupEntry is one row in AWS's published managed-rule-group
// catalog. The sim carries a known, static-but-real subset of the AWS Managed
// Rules catalog so Describe/List ops return the same group names, vendors, and
// versioning support a real account sees.
type wafManagedRuleGroupEntry struct {
	VendorName          string
	Name                string
	Description         string
	VersioningSupported bool
	Capacity            int64
	Versions            []string
	DefaultVersion      string
	Labels              []string
}

// wafManagedCatalog is the static-but-real managed rule group catalog. These are
// genuine AWS Managed Rules group names, vendors, and capacities.
var wafManagedCatalog = []wafManagedRuleGroupEntry{
	{
		VendorName: "AWS", Name: "AWSManagedRulesCommonRuleSet",
		Description:         "Contains rules that are generally applicable to web applications. This provides protection against exploitation of a wide range of vulnerabilities, including those described in OWASP publications.",
		VersioningSupported: true, Capacity: 700,
		Versions: []string{"Version_1.0", "Version_1.1", "Version_1.2"}, DefaultVersion: "Version_1.2",
		Labels: []string{"awswaf:managed:aws:core-rule-set:NoUserAgent_Header", "awswaf:managed:aws:core-rule-set:SizeRestrictions_Body"},
	},
	{
		VendorName: "AWS", Name: "AWSManagedRulesAdminProtectionRuleSet",
		Description:         "Contains rules that allow you to block external access to exposed administrative pages.",
		VersioningSupported: true, Capacity: 100,
		Versions: []string{"Version_1.0", "Version_1.1"}, DefaultVersion: "Version_1.1",
		Labels: []string{"awswaf:managed:aws:admin-protection:AdminProtection_URIPath"},
	},
	{
		VendorName: "AWS", Name: "AWSManagedRulesKnownBadInputsRuleSet",
		Description:         "Contains rules that allow you to block request patterns that are known to be invalid and are associated with exploitation or discovery of vulnerabilities.",
		VersioningSupported: true, Capacity: 200,
		Versions: []string{"Version_1.0", "Version_1.1", "Version_1.2"}, DefaultVersion: "Version_1.2",
		Labels: []string{"awswaf:managed:aws:known-bad-inputs:Host_Localhost", "awswaf:managed:aws:known-bad-inputs:ExploitablePaths_URIPath"},
	},
	{
		VendorName: "AWS", Name: "AWSManagedRulesSQLiRuleSet",
		Description:         "Contains rules that allow you to block request patterns associated with exploitation of SQL databases, like SQL injection attacks.",
		VersioningSupported: true, Capacity: 200,
		Versions: []string{"Version_1.0", "Version_2.0"}, DefaultVersion: "Version_2.0",
		Labels: []string{"awswaf:managed:aws:sql-database:SQLi_Body", "awswaf:managed:aws:sql-database:SQLi_QueryArguments"},
	},
	{
		VendorName: "AWS", Name: "AWSManagedRulesAmazonIpReputationList",
		Description:         "This group contains rules that are based on Amazon threat intelligence. This is useful if you would like to block sources associated with bots or other threats.",
		VersioningSupported: false, Capacity: 25,
		Versions: nil, DefaultVersion: "",
		Labels: []string{"awswaf:managed:aws:amazon-ip-list:AWSManagedIPReputationList", "awswaf:managed:aws:amazon-ip-list:AWSManagedReconnaissanceList"},
	},
}

func wafManagedEntry(vendor, name string) (wafManagedRuleGroupEntry, bool) {
	for _, e := range wafManagedCatalog {
		if e.VendorName == vendor && e.Name == name {
			return e, true
		}
	}
	return wafManagedRuleGroupEntry{}, false
}

type wafDescribeManagedRuleGroupReq struct {
	Scope       string `json:"Scope"`
	VendorName  string `json:"VendorName"`
	Name        string `json:"Name"`
	VersionName string `json:"VersionName,omitempty"`
}

func handleWAFDescribeManagedRuleGroup(w http.ResponseWriter, r *http.Request) {
	var req wafDescribeManagedRuleGroupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	entry, ok := wafManagedEntry(req.VendorName, req.Name)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "Managed rule group not found")
		return
	}
	labels := []map[string]string{}
	for _, l := range entry.Labels {
		labels = append(labels, map[string]string{"Name": l})
	}
	// Synthesize a representative rule list from the labels: each managed rule
	// is named after the label suffix and counts as a Block action.
	rules := []map[string]any{}
	for _, l := range entry.Labels {
		parts := strings.Split(l, ":")
		ruleName := parts[len(parts)-1]
		rules = append(rules, map[string]any{
			"Name":   ruleName,
			"Action": map[string]any{"Block": map[string]any{}},
		})
	}
	resp := map[string]any{
		"Capacity":        entry.Capacity,
		"Rules":           rules,
		"LabelNamespace":  "awswaf:managed:" + strings.ToLower(entry.VendorName) + ":" + entry.Name + ":",
		"AvailableLabels": labels,
		"ConsumedLabels":  []map[string]string{},
		"SnsTopicArn":     "arn:aws:sns:us-east-1:" + awsAccountID() + ":" + entry.Name,
	}
	if entry.VersioningSupported {
		vn := req.VersionName
		if vn == "" {
			vn = entry.DefaultVersion
		}
		resp["VersionName"] = vn
	}
	wafWriteJSON(w, resp)
}

// wafProductDescriptor builds a managed-product descriptor for a catalog entry.
func wafProductDescriptor(e wafManagedRuleGroupEntry) map[string]any {
	return map[string]any{
		"VendorName":               e.VendorName,
		"ManagedRuleSetName":       e.Name,
		"ProductId":                strings.ToLower(e.VendorName) + "-" + e.Name,
		"ProductLink":              "https://docs.aws.amazon.com/waf/latest/developerguide/aws-managed-rule-groups-list.html",
		"ProductTitle":             e.Name,
		"ProductDescription":       e.Description,
		"SnsTopicArn":              "arn:aws:sns:us-east-1:" + awsAccountID() + ":" + e.Name,
		"IsVersioningSupported":    e.VersioningSupported,
		"IsAdvancedManagedRuleSet": false,
	}
}

type wafDescribeAllManagedProductsReq struct {
	Scope string `json:"Scope"`
}

func handleWAFDescribeAllManagedProducts(w http.ResponseWriter, r *http.Request) {
	var req wafDescribeAllManagedProductsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	products := []map[string]any{}
	for _, e := range wafManagedCatalog {
		products = append(products, wafProductDescriptor(e))
	}
	wafWriteJSON(w, map[string]any{"ManagedProducts": products})
}

type wafDescribeManagedProductsByVendorReq struct {
	Scope      string `json:"Scope"`
	VendorName string `json:"VendorName"`
}

func handleWAFDescribeManagedProductsByVendor(w http.ResponseWriter, r *http.Request) {
	var req wafDescribeManagedProductsByVendorReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	products := []map[string]any{}
	for _, e := range wafManagedCatalog {
		if e.VendorName == req.VendorName {
			products = append(products, wafProductDescriptor(e))
		}
	}
	wafWriteJSON(w, map[string]any{"ManagedProducts": products})
}

func handleWAFListAvailableManagedRuleGroups(w http.ResponseWriter, r *http.Request) {
	var req wafListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	type managedSummary struct {
		Name                string `json:"Name"`
		VendorName          string `json:"VendorName"`
		Description         string `json:"Description,omitempty"`
		VersioningSupported bool   `json:"VersioningSupported"`
	}
	items := []managedSummary{}
	for _, e := range wafManagedCatalog {
		items = append(items, managedSummary{
			Name: e.Name, VendorName: e.VendorName,
			Description: e.Description, VersioningSupported: e.VersioningSupported,
		})
	}
	sortBy(items, func(s managedSummary) string { return s.Name })
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{"ManagedRuleGroups": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

type wafListManagedVersionsReq struct {
	Scope      string `json:"Scope"`
	VendorName string `json:"VendorName"`
	Name       string `json:"Name"`
	NextMarker string `json:"NextMarker,omitempty"`
	Limit      int    `json:"Limit,omitempty"`
}

func handleWAFListAvailableManagedRuleGroupVersions(w http.ResponseWriter, r *http.Request) {
	var req wafListManagedVersionsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	entry, ok := wafManagedEntry(req.VendorName, req.Name)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "Managed rule group not found")
		return
	}
	type versionSummary struct {
		Name string `json:"Name"`
		// LastUpdateTimestamp serializes as epoch-seconds for awsJson1.1.
		LastUpdateTimestamp int64 `json:"LastUpdateTimestamp"`
	}
	items := []versionSummary{}
	base := time.Now().Add(-30 * 24 * time.Hour).Unix()
	for i, v := range entry.Versions {
		items = append(items, versionSummary{Name: v, LastUpdateTimestamp: base + int64(i*86400)})
	}
	sortBy(items, func(s versionSummary) string { return s.Name })
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{
		"Versions":              page,
		"CurrentDefaultVersion": entry.DefaultVersion,
	}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

// ---------- Managed rule sets (publisher CRUD) ----------

// WAFManagedRuleSet mirrors the SDK ManagedRuleSet type; PublishedVersions maps
// a version name to its version metadata.
type WAFManagedRuleSet struct {
	Name               string                          `json:"Name"`
	Id                 string                          `json:"Id"`
	ARN                string                          `json:"ARN"`
	Description        string                          `json:"Description,omitempty"`
	PublishedVersions  map[string]WAFManagedRuleSetVer `json:"PublishedVersions,omitempty"`
	RecommendedVersion string                          `json:"RecommendedVersion,omitempty"`
	LabelNamespace     string                          `json:"LabelNamespace,omitempty"`
}

// WAFManagedRuleSetVer mirrors the SDK ManagedRuleSetVersion type. Timestamps
// are epoch-seconds for awsJson1.1.
type WAFManagedRuleSetVer struct {
	AssociatedRuleGroupArn string `json:"AssociatedRuleGroupArn,omitempty"`
	Capacity               int64  `json:"Capacity,omitempty"`
	ForecastedLifetime     int    `json:"ForecastedLifetime,omitempty"`
	PublishTimestamp       int64  `json:"PublishTimestamp,omitempty"`
	LastUpdateTimestamp    int64  `json:"LastUpdateTimestamp,omitempty"`
	ExpiryTimestamp        int64  `json:"ExpiryTimestamp,omitempty"`
}

// wafSeedManagedRuleSets seeds a known managed rule set per scope. Managed rule
// sets are publisher-owned resources surfaced through Get/List; AWS has no
// CreateManagedRuleSet op, so the sim materializes a real-shaped seed set the
// way an account that subscribed to a published vendor product would see one.
// PutManagedRuleSetVersions then publishes new versions against the seed.
func wafSeedManagedRuleSets() {
	for _, scope := range []string{"CLOUDFRONT", "REGIONAL"} {
		// EntityId is a UUID, so the seed's id is one; a readable placeholder
		// with "EXAMPLE" in it is not a value the service could return.
		id := "a1b2c3d4-5678-90ab-cdef-e0a3b1c5d7f9"
		name := "AWSManagedRulesExampleRuleSet"
		key := wafKey(scope, id)
		if _, ok := wafManagedRuleSet.Get(key); ok {
			continue
		}
		wafManagedRuleSet.Put(key, wafStoredManagedRuleSet{
			RuleSet: WAFManagedRuleSet{
				Name: name, Id: id,
				ARN:               wafARN(scope, "managedruleset", name, id),
				Description:       "Seed managed rule set for the example vendor product.",
				PublishedVersions: map[string]WAFManagedRuleSetVer{},
				LabelNamespace:    "awswaf:managed:" + name + ":",
			},
			Scope: scope,
			// A lock token is a UUID whoever issued it; a readable placeholder
			// is not a token a client could round-trip into an update.
			LockToken: "5eed10c0-0000-4000-8000-000000000001",
		})
	}
}

type wafGetManagedRuleSetReq struct {
	Name  string `json:"Name"`
	Scope string `json:"Scope"`
	Id    string `json:"Id"`
}

func handleWAFGetManagedRuleSet(w http.ResponseWriter, r *http.Request) {
	var req wafGetManagedRuleSetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	stored, ok := wafManagedRuleSet.Get(wafKey(req.Scope, req.Id))
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "ManagedRuleSet not found")
		return
	}
	wafWriteJSON(w, map[string]any{
		"ManagedRuleSet": stored.RuleSet,
		"LockToken":      stored.LockToken,
	})
}

func handleWAFListManagedRuleSets(w http.ResponseWriter, r *http.Request) {
	var req wafListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	type summary struct {
		Name           string `json:"Name"`
		Id             string `json:"Id"`
		Description    string `json:"Description,omitempty"`
		LockToken      string `json:"LockToken"`
		ARN            string `json:"ARN"`
		LabelNamespace string `json:"LabelNamespace,omitempty"`
	}
	items := []summary{}
	for _, s := range wafManagedRuleSet.List() {
		if s.Scope != req.Scope {
			continue
		}
		items = append(items, summary{
			Name: s.RuleSet.Name, Id: s.RuleSet.Id, Description: s.RuleSet.Description,
			LockToken: s.LockToken, ARN: s.RuleSet.ARN, LabelNamespace: s.RuleSet.LabelNamespace,
		})
	}
	sortBy(items, func(s summary) string { return s.Name })
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{"ManagedRuleSets": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

type wafPutManagedRuleSetVersionsReq struct {
	Name               string `json:"Name"`
	Scope              string `json:"Scope"`
	Id                 string `json:"Id"`
	LockToken          string `json:"LockToken"`
	RecommendedVersion string `json:"RecommendedVersion,omitempty"`
	VersionsToPublish  map[string]struct {
		AssociatedRuleGroupArn string `json:"AssociatedRuleGroupArn"`
		ForecastedLifetime     int    `json:"ForecastedLifetime"`
	} `json:"VersionsToPublish,omitempty"`
}

func handleWAFPutManagedRuleSetVersions(w http.ResponseWriter, r *http.Request) {
	var req wafPutManagedRuleSetVersionsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.Id)
	stored, ok := wafManagedRuleSet.Get(key)
	if !ok {
		// PutManagedRuleSetVersions on a non-existent set creates it: a
		// publisher's managed rule set is conjured by its first published
		// versions. AWS requires the rule set to exist first, but the sim has
		// no separate create op (there is none in the API), so this op both
		// creates and publishes — faithful to "managed rule sets are owned by
		// vendors and surfaced through this op".
		id := req.Id
		if id == "" {
			id = wafRandomID()
		}
		stored = wafStoredManagedRuleSet{
			RuleSet: WAFManagedRuleSet{
				Name: req.Name, Id: id,
				ARN:               wafARN(req.Scope, "managedruleset", req.Name, id),
				PublishedVersions: map[string]WAFManagedRuleSetVer{},
				LabelNamespace:    "awswaf:managed:" + req.Name + ":",
			},
			Scope:     req.Scope,
			LockToken: wafLockToken(),
		}
	} else if req.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match")
		return
	}
	if stored.RuleSet.PublishedVersions == nil {
		stored.RuleSet.PublishedVersions = map[string]WAFManagedRuleSetVer{}
	}
	now := time.Now().UTC().Unix()
	for vname, v := range req.VersionsToPublish {
		stored.RuleSet.PublishedVersions[vname] = WAFManagedRuleSetVer{
			AssociatedRuleGroupArn: v.AssociatedRuleGroupArn,
			ForecastedLifetime:     v.ForecastedLifetime,
			PublishTimestamp:       now,
			LastUpdateTimestamp:    now,
			ExpiryTimestamp:        now + int64(v.ForecastedLifetime)*86400,
		}
	}
	if req.RecommendedVersion != "" {
		stored.RuleSet.RecommendedVersion = req.RecommendedVersion
	}
	stored.LockToken = wafLockToken()
	wafManagedRuleSet.Put(key, stored)
	wafWriteJSON(w, map[string]string{"NextLockToken": stored.LockToken})
}

type wafUpdateManagedRuleSetExpiryReq struct {
	Name            string `json:"Name"`
	Scope           string `json:"Scope"`
	Id              string `json:"Id"`
	LockToken       string `json:"LockToken"`
	VersionToExpire string `json:"VersionToExpire"`
	ExpiryTimestamp any    `json:"ExpiryTimestamp"`
}

func handleWAFUpdateManagedRuleSetVersionExpiryDate(w http.ResponseWriter, r *http.Request) {
	var req wafUpdateManagedRuleSetExpiryReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	key := wafKey(req.Scope, req.Id)
	stored, ok := wafManagedRuleSet.Get(key)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "ManagedRuleSet not found")
		return
	}
	if req.LockToken != stored.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "LockToken does not match")
		return
	}
	ver, vok := stored.RuleSet.PublishedVersions[req.VersionToExpire]
	if !vok {
		wafWriteError(w, "WAFNonexistentItemException", "VersionToExpire not found")
		return
	}
	expiry := wafTimestampToUnix(req.ExpiryTimestamp)
	ver.ExpiryTimestamp = expiry
	ver.LastUpdateTimestamp = time.Now().UTC().Unix()
	stored.RuleSet.PublishedVersions[req.VersionToExpire] = ver
	stored.LockToken = wafLockToken()
	wafManagedRuleSet.Put(key, stored)
	wafWriteJSON(w, map[string]any{
		"ExpiringVersion": req.VersionToExpire,
		"ExpiryTimestamp": expiry,
		"NextLockToken":   stored.LockToken,
	})
}

// wafTimestampToUnix coerces an awsJson1.1 timestamp (epoch-seconds number) to
// an int64 epoch. The SDK serializes timestamps as JSON numbers.
func wafTimestampToUnix(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	default:
		return time.Now().UTC().Unix()
	}
}

// ---------- Permission policy ----------

type wafPutPermissionPolicyReq struct {
	ResourceArn string `json:"ResourceArn"`
	Policy      string `json:"Policy"`
}

func handleWAFPutPermissionPolicy(w http.ResponseWriter, r *http.Request) {
	var req wafPutPermissionPolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if req.ResourceArn == "" || req.Policy == "" {
		wafWriteError(w, "WAFInvalidParameterException", "ResourceArn and Policy are required")
		return
	}
	// PutPermissionPolicy targets a rule group ARN.
	if !strings.Contains(req.ResourceArn, "/rulegroup/") {
		wafWriteError(w, "WAFInvalidPermissionPolicyException", "Permission policies are only valid on rule groups")
		return
	}
	found := false
	for _, s := range wafRuleGroups.List() {
		if s.RuleGroup.ARN == req.ResourceArn {
			found = true
			break
		}
	}
	if !found {
		wafWriteError(w, "WAFNonexistentItemException", "RuleGroup not found for ResourceArn")
		return
	}
	wafPermissionPolicies.Put(req.ResourceArn, req.Policy)
	wafWriteJSON(w, struct{}{})
}

type wafResourceArnReq struct {
	ResourceArn string `json:"ResourceArn"`
}

func handleWAFGetPermissionPolicy(w http.ResponseWriter, r *http.Request) {
	var req wafResourceArnReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	policy, ok := wafPermissionPolicies.Get(req.ResourceArn)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "PermissionPolicy not found")
		return
	}
	wafWriteJSON(w, map[string]any{"Policy": policy})
}

func handleWAFDeletePermissionPolicy(w http.ResponseWriter, r *http.Request) {
	var req wafResourceArnReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if _, ok := wafPermissionPolicies.Get(req.ResourceArn); !ok {
		wafWriteError(w, "WAFNonexistentItemException", "PermissionPolicy not found")
		return
	}
	wafPermissionPolicies.Delete(req.ResourceArn)
	wafWriteJSON(w, struct{}{})
}

// ---------- Mobile SDK releases ----------

// wafMobileSdkRelease is one published WAF mobile SDK release. The sim carries a
// known, static-but-real set of releases per platform.
type wafMobileSdkRelease struct {
	Platform       string
	ReleaseVersion string
	Timestamp      time.Time
	ReleaseNotes   string
}

var wafMobileSdkReleases = []wafMobileSdkRelease{
	{Platform: "IOS", ReleaseVersion: "1.0.0", Timestamp: time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC), ReleaseNotes: "Initial release of the AWS WAF mobile SDK for iOS."},
	{Platform: "IOS", ReleaseVersion: "1.1.0", Timestamp: time.Date(2023, 3, 15, 0, 0, 0, 0, time.UTC), ReleaseNotes: "Token refresh improvements."},
	{Platform: "ANDROID", ReleaseVersion: "1.0.0", Timestamp: time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC), ReleaseNotes: "Initial release of the AWS WAF mobile SDK for Android."},
	{Platform: "ANDROID", ReleaseVersion: "1.1.0", Timestamp: time.Date(2023, 3, 15, 0, 0, 0, 0, time.UTC), ReleaseNotes: "Token refresh improvements."},
}

func wafFindMobileSdkRelease(platform, version string) (wafMobileSdkRelease, bool) {
	for _, rel := range wafMobileSdkReleases {
		if rel.Platform == platform && rel.ReleaseVersion == version {
			return rel, true
		}
	}
	return wafMobileSdkRelease{}, false
}

type wafMobileSdkReleaseReq struct {
	Platform       string `json:"Platform"`
	ReleaseVersion string `json:"ReleaseVersion"`
}

func handleWAFGenerateMobileSdkReleaseUrl(w http.ResponseWriter, r *http.Request) {
	var req wafMobileSdkReleaseReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	if _, ok := wafFindMobileSdkRelease(req.Platform, req.ReleaseVersion); !ok {
		wafWriteError(w, "WAFNonexistentItemException", "Mobile SDK release not found")
		return
	}
	url := fmt.Sprintf("https://wafv2-mobile-sdk.%s.amazonaws.com/%s/%s/aws-waf-sdk.zip?token=%s",
		awsRegion(), strings.ToLower(req.Platform), req.ReleaseVersion, hex.EncodeToString([]byte(req.Platform+req.ReleaseVersion)))
	wafWriteJSON(w, map[string]any{"Url": url})
}

func handleWAFGetMobileSdkRelease(w http.ResponseWriter, r *http.Request) {
	var req wafMobileSdkReleaseReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	rel, ok := wafFindMobileSdkRelease(req.Platform, req.ReleaseVersion)
	if !ok {
		wafWriteError(w, "WAFNonexistentItemException", "Mobile SDK release not found")
		return
	}
	wafWriteJSON(w, map[string]any{
		"MobileSdkRelease": map[string]any{
			"ReleaseVersion": rel.ReleaseVersion,
			"Timestamp":      rel.Timestamp.Unix(),
			"ReleaseNotes":   rel.ReleaseNotes,
			"Tags":           []wafTag{},
		},
	})
}

type wafListMobileSdkReleasesReq struct {
	Platform   string `json:"Platform"`
	NextMarker string `json:"NextMarker,omitempty"`
	Limit      int    `json:"Limit,omitempty"`
}

func handleWAFListMobileSdkReleases(w http.ResponseWriter, r *http.Request) {
	var req wafListMobileSdkReleasesReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	type releaseSummary struct {
		ReleaseVersion string `json:"ReleaseVersion"`
		Timestamp      int64  `json:"Timestamp"`
	}
	items := []releaseSummary{}
	for _, rel := range wafMobileSdkReleases {
		if rel.Platform != req.Platform {
			continue
		}
		items = append(items, releaseSummary{ReleaseVersion: rel.ReleaseVersion, Timestamp: rel.Timestamp.Unix()})
	}
	sortBy(items, func(s releaseSummary) string { return s.ReleaseVersion })
	page, next := awsPage(items, req.NextMarker, req.Limit, 100)
	resp := map[string]any{"ReleaseSummaries": page}
	if next != "" {
		resp["NextMarker"] = next
	}
	wafWriteJSON(w, resp)
}

// ---------- Firewall Manager managed rule groups ----------

type wafDeleteFMRuleGroupsReq struct {
	WebACLArn       string `json:"WebACLArn"`
	WebACLLockToken string `json:"WebACLLockToken"`
}

func handleWAFDeleteFirewallManagerRuleGroups(w http.ResponseWriter, r *http.Request) {
	var req wafDeleteFMRuleGroupsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	var target *wafStoredWebACL
	var key string
	for _, s := range wafWebACLs.List() {
		if s.WebACL.ARN == req.WebACLArn {
			scopy := s
			target = &scopy
			key = wafKey(s.Scope, s.WebACL.Id)
			break
		}
	}
	if target == nil {
		wafWriteError(w, "WAFNonexistentItemException", "WebACL not found")
		return
	}
	if req.WebACLLockToken != target.LockToken {
		wafWriteError(w, "WAFOptimisticLockException", "WebACLLockToken does not match")
		return
	}
	// Firewall Manager rule groups occupy the reserved priority bands at the
	// edges of the rule set; deleting them advances the web ACL's lock token.
	target.LockToken = wafLockToken()
	wafWebACLs.Put(key, *target)
	wafWriteJSON(w, map[string]any{"NextWebACLLockToken": target.LockToken})
}

// ---------- Rate-based statement managed keys + traffic statistics ----------

type wafGetRateBasedKeysReq struct {
	Scope             string `json:"Scope"`
	WebACLName        string `json:"WebACLName"`
	WebACLId          string `json:"WebACLId"`
	RuleName          string `json:"RuleName"`
	RuleGroupRuleName string `json:"RuleGroupRuleName,omitempty"`
}

func handleWAFGetRateBasedStatementManagedKeys(w http.ResponseWriter, r *http.Request) {
	var req wafGetRateBasedKeysReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	stored, ok := wafWebACLs.Get(wafKey(req.Scope, req.WebACLId))
	if !ok || stored.WebACL.Name != req.WebACLName {
		wafWriteError(w, "WAFNonexistentItemException", "WebACL not found")
		return
	}
	ipv4 := make([]string, 0)
	ipv6 := make([]string, 0)
	for _, window := range wafRateWindows.Filter(func(window wafRateWindow) bool {
		return window.WebACLARN == stored.WebACL.ARN &&
			window.RuleName == req.RuleName && window.Limited
	}) {
		for _, address := range window.Addresses {
			ip, err := netip.ParseAddr(address)
			if err != nil {
				continue
			}
			if ip.Is4() {
				ipv4 = append(ipv4, ip.String())
			} else {
				ipv6 = append(ipv6, ip.String())
			}
		}
	}
	slices.Sort(ipv4)
	ipv4 = slices.Compact(ipv4)
	slices.Sort(ipv6)
	ipv6 = slices.Compact(ipv6)
	wafWriteJSON(w, map[string]any{
		"ManagedKeysIPV4": map[string]any{"IPAddressVersion": "IPV4", "Addresses": ipv4},
		"ManagedKeysIPV6": map[string]any{"IPAddressVersion": "IPV6", "Addresses": ipv6},
	})
}

type wafGetTopPathStatsReq struct {
	Scope      string `json:"Scope"`
	WebAclArn  string `json:"WebAclArn"`
	NextMarker string `json:"NextMarker,omitempty"`
	Limit      int    `json:"Limit,omitempty"`
}

func handleWAFGetTopPathStatisticsByTraffic(w http.ResponseWriter, r *http.Request) {
	var req wafGetTopPathStatsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		wafWriteError(w, "WAFInvalidParameterException", "could not decode: "+err.Error())
		return
	}
	found := false
	for _, s := range wafWebACLs.List() {
		if s.WebACL.ARN == req.WebAclArn {
			found = true
			break
		}
	}
	if !found {
		wafWriteError(w, "WAFNonexistentItemException", "WebACL not found")
		return
	}
	// No traffic has flowed through the sim, so the statistics are empty —
	// the real-shaped response for a web ACL with no observed requests.
	wafWriteJSON(w, map[string]any{
		"PathStatistics":    []any{},
		"TopCategories":     []any{},
		"TotalRequestCount": 0,
	})
}

package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The simulator runs no AWS Signer and stores no OCI referrers, so image
// signing status and the referrers list are empty.

// Keyed by setting name.
type ECRAccountSetting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ECRRegistryScanningConfiguration struct {
	ScanType string                    `json:"scanType"`
	Rules    []ECRRegistryScanningRule `json:"rules"`
}

type ECRRegistryScanningRule struct {
	ScanFrequency     string                  `json:"scanFrequency"`
	RepositoryFilters []ECRScanningRepoFilter `json:"repositoryFilters"`
}

type ECRScanningRepoFilter struct {
	Filter     string `json:"filter"`
	FilterType string `json:"filterType"`
}

// Keyed by namespace prefix; createdAt and updatedAt are epoch-second numbers.
type ECRRepositoryCreationTemplate struct {
	Prefix                             string                                 `json:"prefix"`
	Description                        string                                 `json:"description,omitempty"`
	EncryptionConfiguration            *ECREncryptionConfiguration            `json:"encryptionConfiguration,omitempty"`
	ResourceTags                       []SMTag                                `json:"resourceTags"`
	ImageTagMutability                 string                                 `json:"imageTagMutability,omitempty"`
	ImageTagMutabilityExclusionFilters []ECRImageTagMutabilityExclusionFilter `json:"imageTagMutabilityExclusionFilters,omitempty"`
	RepositoryPolicy                   string                                 `json:"repositoryPolicy,omitempty"`
	LifecyclePolicy                    string                                 `json:"lifecyclePolicy,omitempty"`
	AppliedFor                         []string                               `json:"appliedFor"`
	CustomRoleArn                      string                                 `json:"customRoleArn,omitempty"`
	CreatedAt                          int64                                  `json:"createdAt"`
	UpdatedAt                          int64                                  `json:"updatedAt"`
}

type ECRSigningConfiguration struct {
	Rules []ECRSigningRule `json:"rules"`
}

type ECRSigningRule struct {
	SigningProfileArn string                 `json:"signingProfileArn"`
	RepositoryFilters []ECRSigningRepoFilter `json:"repositoryFilters,omitempty"`
}

type ECRSigningRepoFilter struct {
	Filter     string `json:"filter"`
	FilterType string `json:"filterType"`
}

// Keyed by principal ARN.
type ECRPullTimeUpdateExclusion struct {
	PrincipalArn string `json:"principalArn"`
	CreatedAt    int64  `json:"createdAt"`
}

var (
	ecrAccountSettings          sim.Store[ECRAccountSetting]
	ecrRegistryScanningConfig   sim.Store[ECRRegistryScanningConfiguration]
	ecrRepoCreationTemplates    sim.Store[ECRRepositoryCreationTemplate]
	ecrSigningConfig            sim.Store[ECRSigningConfiguration]
	ecrPullTimeUpdateExclusions sim.Store[ECRPullTimeUpdateExclusion]
)

func registerECRAdvanced(r *AWSRouter, srv *sim.Server) {
	ecrAccountSettings = sim.MakeStore[ECRAccountSetting](srv.DB(), "ecr_account_settings")
	ecrRegistryScanningConfig = sim.MakeStore[ECRRegistryScanningConfiguration](srv.DB(), "ecr_registry_scanning_config")
	ecrRepoCreationTemplates = sim.MakeStore[ECRRepositoryCreationTemplate](srv.DB(), "ecr_repo_creation_templates")
	ecrSigningConfig = sim.MakeStore[ECRSigningConfiguration](srv.DB(), "ecr_signing_config")
	ecrPullTimeUpdateExclusions = sim.MakeStore[ECRPullTimeUpdateExclusion](srv.DB(), "ecr_pull_time_update_exclusions")

	for op, h := range map[string]http.HandlerFunc{
		"GetAccountSetting":                       handleECRGetAccountSetting,
		"PutAccountSetting":                       handleECRPutAccountSetting,
		"GetRegistryScanningConfiguration":        handleECRGetRegistryScanningConfiguration,
		"PutRegistryScanningConfiguration":        handleECRPutRegistryScanningConfiguration,
		"BatchGetRepositoryScanningConfiguration": handleECRBatchGetRepositoryScanningConfiguration,
		"CreateRepositoryCreationTemplate":        handleECRCreateRepositoryCreationTemplate,
		"UpdateRepositoryCreationTemplate":        handleECRUpdateRepositoryCreationTemplate,
		"DeleteRepositoryCreationTemplate":        handleECRDeleteRepositoryCreationTemplate,
		"DescribeRepositoryCreationTemplates":     handleECRDescribeRepositoryCreationTemplates,
		"GetSigningConfiguration":                 handleECRGetSigningConfiguration,
		"PutSigningConfiguration":                 handleECRPutSigningConfiguration,
		"DeleteSigningConfiguration":              handleECRDeleteSigningConfiguration,
		"DescribeImageSigningStatus":              handleECRDescribeImageSigningStatus,
		"RegisterPullTimeUpdateExclusion":         handleECRRegisterPullTimeUpdateExclusion,
		"DeregisterPullTimeUpdateExclusion":       handleECRDeregisterPullTimeUpdateExclusion,
		"ListPullTimeUpdateExclusions":            handleECRListPullTimeUpdateExclusions,
		"UpdatePullThroughCacheRule":              handleECRUpdatePullThroughCacheRule,
		"ValidatePullThroughCacheRule":            handleECRValidatePullThroughCacheRule,
		"ListImageReferrers":                      handleECRListImageReferrers,
		"UpdateImageStorageClass":                 handleECRUpdateImageStorageClass,
	} {
		r.Register("AmazonEC2ContainerRegistry_V20150921."+op, h)
	}
}

func handleECRGetAccountSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "InvalidParameterException", "name is required", http.StatusBadRequest)
		return
	}
	value := ecrDefaultAccountSettingValue(req.Name)
	if s, ok := ecrAccountSettings.Get(req.Name); ok {
		value = s.Value
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":  req.Name,
		"value": value,
	})
}

func handleECRPutAccountSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Value == "" {
		AWSError(w, "InvalidParameterException", "name and value are required", http.StatusBadRequest)
		return
	}
	ecrAccountSettings.Put(req.Name, ECRAccountSetting{Name: req.Name, Value: req.Value})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":  req.Name,
		"value": req.Value,
	})
}

// A setting never Put reports a fresh account's default.
func ecrDefaultAccountSettingValue(name string) string {
	switch name {
	case "BASIC_SCAN_TYPE_VERSION":
		return "AWS_NATIVE"
	case "REGISTRY_POLICY_SCOPE":
		return "V1"
	case "BLOB_MOUNTING":
		return "DISABLED"
	default:
		return ""
	}
}

func handleECRGetRegistryScanningConfiguration(w http.ResponseWriter, r *http.Request) {
	if err := sim.ReadJSON(r, &struct{}{}); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	cfg, ok := ecrRegistryScanningConfig.Get(ecrRegistrySingletonKey)
	if !ok {
		// No configuration means BASIC with no rules.
		cfg = ECRRegistryScanningConfiguration{ScanType: "BASIC", Rules: []ECRRegistryScanningRule{}}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":            ecrRegistryId(),
		"scanningConfiguration": ecrNormalizeScanningConfig(cfg),
	})
}

func handleECRPutRegistryScanningConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ScanType string                    `json:"scanType"`
		Rules    []ECRRegistryScanningRule `json:"rules"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	cfg := ECRRegistryScanningConfiguration{ScanType: req.ScanType, Rules: req.Rules}
	if cfg.ScanType == "" {
		cfg.ScanType = "BASIC"
	}
	ecrRegistryScanningConfig.Put(ecrRegistrySingletonKey, cfg)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryScanningConfiguration": ecrNormalizeScanningConfig(cfg),
	})
}

func ecrNormalizeScanningConfig(cfg ECRRegistryScanningConfiguration) ECRRegistryScanningConfiguration {
	if cfg.Rules == nil {
		cfg.Rules = []ECRRegistryScanningRule{}
	}
	for i := range cfg.Rules {
		if cfg.Rules[i].RepositoryFilters == nil {
			cfg.Rules[i].RepositoryFilters = []ECRScanningRepoFilter{}
		}
	}
	return cfg
}

func handleECRBatchGetRepositoryScanningConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryNames []string `json:"repositoryNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// Combine the registry's scanFrequency and filters with the repository's own
	// scanOnPush, as Amazon ECR does.
	regCfg, _ := ecrRegistryScanningConfig.Get(ecrRegistrySingletonKey)

	configs := []map[string]any{}
	failures := []map[string]any{}
	for _, name := range req.RepositoryNames {
		repo, ok := ecrRepositories.Get(name)
		if !ok {
			failures = append(failures, map[string]any{
				"repositoryName": name,
				"failureCode":    "REPOSITORY_NOT_FOUND",
				"failureReason":  "REPOSITORY_NOT_FOUND",
			})
			continue
		}
		scanFrequency := "MANUAL"
		appliedFilters := []ECRScanningRepoFilter{}
		for _, rule := range regCfg.Rules {
			scanFrequency = rule.ScanFrequency
			appliedFilters = append(appliedFilters, rule.RepositoryFilters...)
		}
		configs = append(configs, map[string]any{
			"repositoryArn":      repo.RepositoryArn,
			"repositoryName":     repo.RepositoryName,
			"scanOnPush":         repo.ImageScanningConfiguration.ScanOnPush,
			"scanFrequency":      scanFrequency,
			"appliedScanFilters": appliedFilters,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"scanningConfigurations": configs,
		"failures":               failures,
	})
}

func handleECRCreateRepositoryCreationTemplate(w http.ResponseWriter, r *http.Request) {
	var req ecrRepoCreationTemplateInput
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Prefix == "" {
		AWSError(w, "InvalidParameterException", "prefix is required", http.StatusBadRequest)
		return
	}
	if len(req.AppliedFor) == 0 {
		AWSError(w, "InvalidParameterException", "appliedFor is required", http.StatusBadRequest)
		return
	}
	if _, exists := ecrRepoCreationTemplates.Get(req.Prefix); exists {
		AWSErrorf(w, "TemplateAlreadyExistsException", http.StatusBadRequest,
			"A repository creation template with the prefix '%s' already exists", req.Prefix)
		return
	}
	now := time.Now().Unix()
	tmpl := req.toTemplate()
	tmpl.CreatedAt = now
	tmpl.UpdatedAt = now
	ecrRepoCreationTemplates.Put(req.Prefix, tmpl)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":                 ecrRegistryId(),
		"repositoryCreationTemplate": ecrNormalizeTemplate(tmpl),
	})
}

func handleECRUpdateRepositoryCreationTemplate(w http.ResponseWriter, r *http.Request) {
	var req ecrRepoCreationTemplateInput
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Prefix == "" {
		AWSError(w, "InvalidParameterException", "prefix is required", http.StatusBadRequest)
		return
	}
	existing, ok := ecrRepoCreationTemplates.Get(req.Prefix)
	if !ok {
		AWSErrorf(w, "TemplateNotFoundException", http.StatusBadRequest,
			"The repository creation template with prefix '%s' does not exist", req.Prefix)
		return
	}
	// Leave omitted members unchanged.
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.EncryptionConfiguration != nil {
		existing.EncryptionConfiguration = req.EncryptionConfiguration
	}
	if req.ResourceTags != nil {
		existing.ResourceTags = req.ResourceTags
	}
	if req.ImageTagMutability != nil {
		existing.ImageTagMutability = *req.ImageTagMutability
	}
	if req.ImageTagMutabilityExclusionFilters != nil {
		existing.ImageTagMutabilityExclusionFilters = req.ImageTagMutabilityExclusionFilters
	}
	if req.RepositoryPolicy != nil {
		existing.RepositoryPolicy = *req.RepositoryPolicy
	}
	if req.LifecyclePolicy != nil {
		existing.LifecyclePolicy = *req.LifecyclePolicy
	}
	if req.AppliedFor != nil {
		existing.AppliedFor = req.AppliedFor
	}
	if req.CustomRoleArn != nil {
		existing.CustomRoleArn = *req.CustomRoleArn
	}
	existing.UpdatedAt = time.Now().Unix()
	ecrRepoCreationTemplates.Put(req.Prefix, existing)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":                 ecrRegistryId(),
		"repositoryCreationTemplate": ecrNormalizeTemplate(existing),
	})
}

func handleECRDeleteRepositoryCreationTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prefix string `json:"prefix"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	tmpl, ok := ecrRepoCreationTemplates.Get(req.Prefix)
	if !ok {
		AWSErrorf(w, "TemplateNotFoundException", http.StatusBadRequest,
			"The repository creation template with prefix '%s' does not exist", req.Prefix)
		return
	}
	ecrRepoCreationTemplates.Delete(req.Prefix)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":                 ecrRegistryId(),
		"repositoryCreationTemplate": ecrNormalizeTemplate(tmpl),
	})
}

func handleECRDescribeRepositoryCreationTemplates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prefixes   []string `json:"prefixes"`
		NextToken  string   `json:"nextToken"`
		MaxResults int      `json:"maxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var tmpls []ECRRepositoryCreationTemplate
	key := func(t ECRRepositoryCreationTemplate) string { return t.Prefix }
	if len(req.Prefixes) == 0 {
		tmpls = ecrRepoCreationTemplates.List()
	} else {
		for _, p := range req.Prefixes {
			if t, ok := ecrRepoCreationTemplates.Get(p); ok {
				tmpls = append(tmpls, t)
			}
		}
	}
	paged, next, pageOK := awsNamedOrKeyPage(w, ecrBadToken, tmpls, len(req.Prefixes) > 0, key, req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
	page := make([]ECRRepositoryCreationTemplate, 0, len(paged))
	for _, t := range paged {
		page = append(page, ecrNormalizeTemplate(t))
	}
	resp := map[string]any{
		"registryId":                  ecrRegistryId(),
		"repositoryCreationTemplates": page,
	}
	if next != "" {
		resp["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// Pointers tell omitted from empty.
type ecrRepoCreationTemplateInput struct {
	Prefix                             string                                 `json:"prefix"`
	Description                        *string                                `json:"description"`
	EncryptionConfiguration            *ECREncryptionConfiguration            `json:"encryptionConfiguration"`
	ResourceTags                       []SMTag                                `json:"resourceTags"`
	ImageTagMutability                 *string                                `json:"imageTagMutability"`
	ImageTagMutabilityExclusionFilters []ECRImageTagMutabilityExclusionFilter `json:"imageTagMutabilityExclusionFilters"`
	RepositoryPolicy                   *string                                `json:"repositoryPolicy"`
	LifecyclePolicy                    *string                                `json:"lifecyclePolicy"`
	AppliedFor                         []string                               `json:"appliedFor"`
	CustomRoleArn                      *string                                `json:"customRoleArn"`
}

func (in ecrRepoCreationTemplateInput) toTemplate() ECRRepositoryCreationTemplate {
	t := ECRRepositoryCreationTemplate{
		Prefix:                             in.Prefix,
		EncryptionConfiguration:            in.EncryptionConfiguration,
		ResourceTags:                       in.ResourceTags,
		ImageTagMutabilityExclusionFilters: in.ImageTagMutabilityExclusionFilters,
		AppliedFor:                         in.AppliedFor,
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.ImageTagMutability != nil {
		t.ImageTagMutability = *in.ImageTagMutability
	}
	if in.RepositoryPolicy != nil {
		t.RepositoryPolicy = *in.RepositoryPolicy
	}
	if in.LifecyclePolicy != nil {
		t.LifecyclePolicy = *in.LifecyclePolicy
	}
	if in.CustomRoleArn != nil {
		t.CustomRoleArn = *in.CustomRoleArn
	}
	if t.ImageTagMutability == "" {
		t.ImageTagMutability = "MUTABLE" // The AWS default.
	}
	return t
}

func ecrNormalizeTemplate(t ECRRepositoryCreationTemplate) ECRRepositoryCreationTemplate {
	if t.ResourceTags == nil {
		t.ResourceTags = []SMTag{}
	}
	if t.AppliedFor == nil {
		t.AppliedFor = []string{}
	}
	return t
}

func handleECRGetSigningConfiguration(w http.ResponseWriter, r *http.Request) {
	if err := sim.ReadJSON(r, &struct{}{}); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	cfg, ok := ecrSigningConfig.Get(ecrRegistrySingletonKey)
	if !ok {
		AWSError(w, "SigningConfigurationNotFoundException",
			"The signing configuration does not exist for the registry",
			http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":           ecrRegistryId(),
		"signingConfiguration": ecrNormalizeSigningConfig(cfg),
	})
}

func handleECRPutSigningConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SigningConfiguration ECRSigningConfiguration `json:"signingConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.SigningConfiguration.Rules) == 0 {
		AWSError(w, "InvalidParameterException", "signingConfiguration.rules is required", http.StatusBadRequest)
		return
	}
	ecrSigningConfig.Put(ecrRegistrySingletonKey, req.SigningConfiguration)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"signingConfiguration": ecrNormalizeSigningConfig(req.SigningConfiguration),
	})
}

func handleECRDeleteSigningConfiguration(w http.ResponseWriter, r *http.Request) {
	if err := sim.ReadJSON(r, &struct{}{}); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	cfg, ok := ecrSigningConfig.Get(ecrRegistrySingletonKey)
	if !ok {
		AWSError(w, "SigningConfigurationNotFoundException",
			"The signing configuration does not exist for the registry",
			http.StatusBadRequest)
		return
	}
	ecrSigningConfig.Delete(ecrRegistrySingletonKey)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":           ecrRegistryId(),
		"signingConfiguration": ecrNormalizeSigningConfig(cfg),
	})
}

func ecrNormalizeSigningConfig(cfg ECRSigningConfiguration) ECRSigningConfiguration {
	if cfg.Rules == nil {
		cfg.Rules = []ECRSigningRule{}
	}
	return cfg
}

func handleECRDescribeImageSigningStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryName string `json:"repositoryName"`
		ImageId        struct {
			ImageTag    string `json:"imageTag"`
			ImageDigest string `json:"imageDigest"`
		} `json:"imageId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	img, ok := ecrResolveImage(req.RepositoryName, req.ImageId.ImageTag, req.ImageId.ImageDigest)
	if !ok {
		AWSError(w, "ImageNotFoundException",
			"The image requested does not exist in the specified repository",
			http.StatusBadRequest)
		return
	}
	imageId := map[string]string{"imageDigest": img.ImageDigest}
	if req.ImageId.ImageTag != "" {
		imageId["imageTag"] = req.ImageId.ImageTag
	}
	// Without AWS Signer, no image matches a signing rule.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":      ecrRegistryId(),
		"repositoryName":  req.RepositoryName,
		"imageId":         imageId,
		"signingStatuses": []any{},
	})
}

func handleECRRegisterPullTimeUpdateExclusion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PrincipalArn string `json:"principalArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.PrincipalArn == "" {
		AWSError(w, "InvalidParameterException", "principalArn is required", http.StatusBadRequest)
		return
	}
	if _, exists := ecrPullTimeUpdateExclusions.Get(req.PrincipalArn); exists {
		AWSError(w, "ExclusionAlreadyExistsException",
			"The principal is already on the pull time update exclusion list",
			http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	ecrPullTimeUpdateExclusions.Put(req.PrincipalArn, ECRPullTimeUpdateExclusion{
		PrincipalArn: req.PrincipalArn,
		CreatedAt:    now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"principalArn": req.PrincipalArn,
		"createdAt":    now,
	})
}

func handleECRDeregisterPullTimeUpdateExclusion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PrincipalArn string `json:"principalArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrPullTimeUpdateExclusions.Get(req.PrincipalArn); !ok {
		AWSError(w, "ExclusionNotFoundException",
			"The principal is not on the pull time update exclusion list",
			http.StatusBadRequest)
		return
	}
	ecrPullTimeUpdateExclusions.Delete(req.PrincipalArn)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"principalArn": req.PrincipalArn,
	})
}

func handleECRListPullTimeUpdateExclusions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	exclusions := ecrPullTimeUpdateExclusions.List()
	sortBy(exclusions, func(e ECRPullTimeUpdateExclusion) string { return e.PrincipalArn })
	arns := make([]string, 0, len(exclusions))
	for _, e := range exclusions {
		arns = append(arns, e.PrincipalArn)
	}
	page, next, pageOK := awsKeyPage(w, ecrBadToken, arns, func(it string) string { return it }, req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"pullTimeUpdateExclusions": page}
	if next != "" {
		resp["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleECRUpdatePullThroughCacheRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId          string `json:"registryId"`
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		CredentialArn       string `json:"credentialArn"`
		CustomRoleArn       string `json:"customRoleArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.EcrRepositoryPrefix == "" {
		AWSError(w, "InvalidParameterException", "ecrRepositoryPrefix is required", http.StatusBadRequest)
		return
	}
	rule, ok := ecrPullThroughCacheRules.Get(req.EcrRepositoryPrefix)
	if !ok {
		AWSError(w, "PullThroughCacheRuleNotFoundException",
			"The pull-through cache rule does not exist",
			http.StatusNotFound)
		return
	}
	now := time.Now().Unix()
	rule.CredentialArn = req.CredentialArn
	rule.CustomRoleArn = req.CustomRoleArn
	rule.UpdatedAt = now
	ecrPullThroughCacheRules.Put(req.EcrRepositoryPrefix, rule)

	resp := map[string]any{
		"ecrRepositoryPrefix": rule.EcrRepositoryPrefix,
		"registryId":          rule.RegistryId,
		"updatedAt":           now,
	}
	if req.CredentialArn != "" {
		resp["credentialArn"] = req.CredentialArn
	}
	if req.CustomRoleArn != "" {
		resp["customRoleArn"] = req.CustomRoleArn
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleECRValidatePullThroughCacheRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		RegistryId          string `json:"registryId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.EcrRepositoryPrefix == "" {
		AWSError(w, "InvalidParameterException", "ecrRepositoryPrefix is required", http.StatusBadRequest)
		return
	}
	rule, ok := ecrPullThroughCacheRules.Get(req.EcrRepositoryPrefix)
	if !ok {
		AWSError(w, "PullThroughCacheRuleNotFoundException",
			"The pull-through cache rule does not exist",
			http.StatusNotFound)
		return
	}
	// The simulator does not contact the upstream; without a failure condition the
	// rule reports valid.
	resp := map[string]any{
		"ecrRepositoryPrefix": rule.EcrRepositoryPrefix,
		"registryId":          rule.RegistryId,
		"upstreamRegistryUrl": rule.UpstreamRegistryUrl,
		"isValid":             true,
	}
	if rule.CredentialArn != "" {
		resp["credentialArn"] = rule.CredentialArn
	}
	if rule.CustomRoleArn != "" {
		resp["customRoleArn"] = rule.CustomRoleArn
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleECRListImageReferrers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId     string `json:"registryId"`
		RepositoryName string `json:"repositoryName"`
		SubjectId      struct {
			ImageDigest string `json:"imageDigest"`
		} `json:"subjectId"`
		Filter struct {
			ArtifactTypes  []string `json:"artifactTypes"`
			ArtifactStatus string   `json:"artifactStatus"`
		} `json:"filter"`
		NextToken  string `json:"nextToken"`
		MaxResults int    `json:"maxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	// No referrer artifacts are stored.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"referrers": []any{},
	})
}

func handleECRUpdateImageStorageClass(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId     string `json:"registryId"`
		RepositoryName string `json:"repositoryName"`
		ImageId        struct {
			ImageTag    string `json:"imageTag"`
			ImageDigest string `json:"imageDigest"`
		} `json:"imageId"`
		TargetStorageClass string `json:"targetStorageClass"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ecrRepositories.Get(req.RepositoryName); !ok {
		AWSErrorf(w, "RepositoryNotFoundException", http.StatusBadRequest,
			"The repository with name '%s' does not exist", req.RepositoryName)
		return
	}
	img, ok := ecrResolveImage(req.RepositoryName, req.ImageId.ImageTag, req.ImageId.ImageDigest)
	if !ok {
		AWSError(w, "ImageNotFoundException",
			"The image requested does not exist in the specified repository",
			http.StatusBadRequest)
		return
	}
	target := strings.ToUpper(req.TargetStorageClass)
	if target != "STANDARD" && target != "ARCHIVE" {
		AWSError(w, "InvalidParameterException",
			"targetStorageClass must be STANDARD or ARCHIVE",
			http.StatusBadRequest)
		return
	}
	// ARCHIVE completes at once (ARCHIVED); STANDARD begins activation
	// (ACTIVATING).
	imageStatus := "ARCHIVED"
	if target == "STANDARD" {
		imageStatus = "ACTIVATING"
	}
	imageId := map[string]string{"imageDigest": img.ImageDigest}
	if req.ImageId.ImageTag != "" {
		imageId["imageTag"] = req.ImageId.ImageTag
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"registryId":     ecrRegistryId(),
		"repositoryName": req.RepositoryName,
		"imageId":        imageId,
		"imageStatus":    imageStatus,
	})
}

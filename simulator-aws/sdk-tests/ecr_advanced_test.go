package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECR_AccountSettings round-trips PutAccountSetting / GetAccountSetting
// for a named setting, then confirms an un-Put setting reports the real-ECR
// default.
func TestECR_AccountSettings(t *testing.T) {
	client := ecrClient()

	put, err := client.PutAccountSetting(ctx, &ecr.PutAccountSettingInput{
		Name:  aws.String("BASIC_SCAN_TYPE_VERSION"),
		Value: aws.String("AWS_NATIVE"),
	})
	require.NoError(t, err)
	assert.Equal(t, "BASIC_SCAN_TYPE_VERSION", aws.ToString(put.Name))
	assert.Equal(t, "AWS_NATIVE", aws.ToString(put.Value))

	get, err := client.GetAccountSetting(ctx, &ecr.GetAccountSettingInput{
		Name: aws.String("BASIC_SCAN_TYPE_VERSION"),
	})
	require.NoError(t, err)
	assert.Equal(t, "AWS_NATIVE", aws.ToString(get.Value))

	// A setting never written reports the real-ECR default.
	def, err := client.GetAccountSetting(ctx, &ecr.GetAccountSettingInput{
		Name: aws.String("REGISTRY_POLICY_SCOPE"),
	})
	require.NoError(t, err)
	assert.Equal(t, "V1", aws.ToString(def.Value))
}

// TestECR_RegistryScanningConfiguration round-trips
// PutRegistryScanningConfiguration / GetRegistryScanningConfiguration and
// confirms BatchGetRepositoryScanningConfiguration composes the per-repo
// config from the registry rules plus the repository's scanOnPush flag.
func TestECR_RegistryScanningConfiguration(t *testing.T) {
	client := ecrClient()
	repo := "ecr-scan-cfg-repo"
	_, _ = client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
		ImageScanningConfiguration: &ecrtypes.ImageScanningConfiguration{
			ScanOnPush: true,
		},
	})

	put, err := client.PutRegistryScanningConfiguration(ctx, &ecr.PutRegistryScanningConfigurationInput{
		ScanType: ecrtypes.ScanTypeEnhanced,
		Rules: []ecrtypes.RegistryScanningRule{
			{
				ScanFrequency: ecrtypes.ScanFrequencyContinuousScan,
				RepositoryFilters: []ecrtypes.ScanningRepositoryFilter{
					{
						Filter:     aws.String("*"),
						FilterType: ecrtypes.ScanningRepositoryFilterTypeWildcard,
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, put.RegistryScanningConfiguration)
	assert.Equal(t, ecrtypes.ScanTypeEnhanced, put.RegistryScanningConfiguration.ScanType)

	get, err := client.GetRegistryScanningConfiguration(ctx, &ecr.GetRegistryScanningConfigurationInput{})
	require.NoError(t, err)
	require.NotNil(t, get.ScanningConfiguration)
	assert.Equal(t, ecrtypes.ScanTypeEnhanced, get.ScanningConfiguration.ScanType)
	require.Len(t, get.ScanningConfiguration.Rules, 1)
	assert.Equal(t, ecrtypes.ScanFrequencyContinuousScan, get.ScanningConfiguration.Rules[0].ScanFrequency)

	batch, err := client.BatchGetRepositoryScanningConfiguration(ctx, &ecr.BatchGetRepositoryScanningConfigurationInput{
		RepositoryNames: []string{repo},
	})
	require.NoError(t, err)
	require.Len(t, batch.ScanningConfigurations, 1)
	assert.Equal(t, repo, aws.ToString(batch.ScanningConfigurations[0].RepositoryName))
	assert.True(t, batch.ScanningConfigurations[0].ScanOnPush)
	assert.Equal(t, ecrtypes.ScanFrequencyContinuousScan, batch.ScanningConfigurations[0].ScanFrequency)

	// An unknown repository surfaces as a failure, not a hard error.
	miss, err := client.BatchGetRepositoryScanningConfiguration(ctx, &ecr.BatchGetRepositoryScanningConfigurationInput{
		RepositoryNames: []string{"ecr-scan-no-such-repo"},
	})
	require.NoError(t, err)
	require.Len(t, miss.Failures, 1)
	assert.Equal(t, "ecr-scan-no-such-repo", aws.ToString(miss.Failures[0].RepositoryName))
}

// TestECR_RepositoryCreationTemplates exercises the full Create / Describe /
// Update / Delete lifecycle of a repository creation template keyed by prefix.
func TestECR_RepositoryCreationTemplates(t *testing.T) {
	client := ecrClient()
	prefix := "ecr-tmpl-prefix"

	// Tolerant cleanup of a pre-existing template from an earlier run.
	_, _ = client.DeleteRepositoryCreationTemplate(ctx, &ecr.DeleteRepositoryCreationTemplateInput{
		Prefix: aws.String(prefix),
	})

	created, err := client.CreateRepositoryCreationTemplate(ctx, &ecr.CreateRepositoryCreationTemplateInput{
		Prefix:      aws.String(prefix),
		Description: aws.String("template for prod repos"),
		AppliedFor: []ecrtypes.RCTAppliedFor{
			ecrtypes.RCTAppliedForPullThroughCache,
		},
		ImageTagMutability: ecrtypes.ImageTagMutabilityImmutable,
		EncryptionConfiguration: &ecrtypes.EncryptionConfigurationForRepositoryCreationTemplate{
			EncryptionType: ecrtypes.EncryptionTypeAes256,
		},
		RepositoryPolicy: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
		LifecyclePolicy:  aws.String(`{"rules":[]}`),
		ResourceTags: []ecrtypes.Tag{
			{Key: aws.String("env"), Value: aws.String("prod")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created.RepositoryCreationTemplate)
	assert.Equal(t, prefix, aws.ToString(created.RepositoryCreationTemplate.Prefix))
	assert.Equal(t, ecrtypes.ImageTagMutabilityImmutable, created.RepositoryCreationTemplate.ImageTagMutability)
	require.Len(t, created.RepositoryCreationTemplate.AppliedFor, 1)

	// Creating the same prefix again raises TemplateAlreadyExistsException.
	_, err = client.CreateRepositoryCreationTemplate(ctx, &ecr.CreateRepositoryCreationTemplateInput{
		Prefix:     aws.String(prefix),
		AppliedFor: []ecrtypes.RCTAppliedFor{ecrtypes.RCTAppliedForReplication},
	})
	require.Error(t, err)

	desc, err := client.DescribeRepositoryCreationTemplates(ctx, &ecr.DescribeRepositoryCreationTemplatesInput{
		Prefixes: []string{prefix},
	})
	require.NoError(t, err)
	require.Len(t, desc.RepositoryCreationTemplates, 1)
	assert.Equal(t, "template for prod repos", aws.ToString(desc.RepositoryCreationTemplates[0].Description))

	updated, err := client.UpdateRepositoryCreationTemplate(ctx, &ecr.UpdateRepositoryCreationTemplateInput{
		Prefix:      aws.String(prefix),
		Description: aws.String("updated description"),
	})
	require.NoError(t, err)
	require.NotNil(t, updated.RepositoryCreationTemplate)
	assert.Equal(t, "updated description", aws.ToString(updated.RepositoryCreationTemplate.Description))
	// Omitted fields stay unchanged across an update.
	assert.Equal(t, ecrtypes.ImageTagMutabilityImmutable, updated.RepositoryCreationTemplate.ImageTagMutability)

	deleted, err := client.DeleteRepositoryCreationTemplate(ctx, &ecr.DeleteRepositoryCreationTemplateInput{
		Prefix: aws.String(prefix),
	})
	require.NoError(t, err)
	assert.Equal(t, prefix, aws.ToString(deleted.RepositoryCreationTemplate.Prefix))

	// Updating a deleted template raises TemplateNotFoundException.
	_, err = client.UpdateRepositoryCreationTemplate(ctx, &ecr.UpdateRepositoryCreationTemplateInput{
		Prefix:      aws.String(prefix),
		Description: aws.String("nope"),
	})
	require.Error(t, err)
}

// TestECR_SigningConfiguration round-trips the managed-signing configuration
// (Put / Get / Delete) and confirms DescribeImageSigningStatus reports an
// honest empty signing status for a stored image.
func TestECR_SigningConfiguration(t *testing.T) {
	client := ecrClient()

	// GetSigningConfiguration before any Put raises NotFound.
	_, _ = client.DeleteSigningConfiguration(ctx, &ecr.DeleteSigningConfigurationInput{})
	_, err := client.GetSigningConfiguration(ctx, &ecr.GetSigningConfigurationInput{})
	require.Error(t, err)

	put, err := client.PutSigningConfiguration(ctx, &ecr.PutSigningConfigurationInput{
		SigningConfiguration: &ecrtypes.SigningConfiguration{
			Rules: []ecrtypes.SigningRule{
				{
					SigningProfileArn: aws.String("arn:aws:signer:us-east-1:123456789012:/signing-profiles/prof1"),
					RepositoryFilters: []ecrtypes.SigningRepositoryFilter{
						{
							Filter:     aws.String("myapp/*"),
							FilterType: ecrtypes.SigningRepositoryFilterTypeWildcardMatch,
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, put.SigningConfiguration)
	require.Len(t, put.SigningConfiguration.Rules, 1)

	get, err := client.GetSigningConfiguration(ctx, &ecr.GetSigningConfigurationInput{})
	require.NoError(t, err)
	require.NotNil(t, get.SigningConfiguration)
	require.Len(t, get.SigningConfiguration.Rules, 1)
	assert.Equal(t, "arn:aws:signer:us-east-1:123456789012:/signing-profiles/prof1",
		aws.ToString(get.SigningConfiguration.Rules[0].SigningProfileArn))

	repo := "ecr-signing-repo"
	digest := ecrPutTestImage(t, client, repo, "v1")
	status, err := client.DescribeImageSigningStatus(ctx, &ecr.DescribeImageSigningStatusInput{
		RepositoryName: aws.String(repo),
		ImageId:        &ecrtypes.ImageIdentifier{ImageDigest: aws.String(digest)},
	})
	require.NoError(t, err)
	assert.Equal(t, repo, aws.ToString(status.RepositoryName))
	// The simulator signs nothing, so the status list is honestly empty.
	assert.Empty(t, status.SigningStatuses)

	del, err := client.DeleteSigningConfiguration(ctx, &ecr.DeleteSigningConfigurationInput{})
	require.NoError(t, err)
	require.NotNil(t, del.SigningConfiguration)
}

// TestECR_PullTimeUpdateExclusions round-trips Register / List / Deregister
// of pull-time update exclusions keyed by IAM principal ARN.
func TestECR_PullTimeUpdateExclusions(t *testing.T) {
	client := ecrClient()
	principal := "arn:aws:iam::123456789012:role/ECRAccess"

	// Tolerant cleanup of a leftover exclusion.
	_, _ = client.DeregisterPullTimeUpdateExclusion(ctx, &ecr.DeregisterPullTimeUpdateExclusionInput{
		PrincipalArn: aws.String(principal),
	})

	reg, err := client.RegisterPullTimeUpdateExclusion(ctx, &ecr.RegisterPullTimeUpdateExclusionInput{
		PrincipalArn: aws.String(principal),
	})
	require.NoError(t, err)
	assert.Equal(t, principal, aws.ToString(reg.PrincipalArn))

	// Registering the same principal again raises ExclusionAlreadyExists.
	_, err = client.RegisterPullTimeUpdateExclusion(ctx, &ecr.RegisterPullTimeUpdateExclusionInput{
		PrincipalArn: aws.String(principal),
	})
	require.Error(t, err)

	list, err := client.ListPullTimeUpdateExclusions(ctx, &ecr.ListPullTimeUpdateExclusionsInput{})
	require.NoError(t, err)
	assert.Contains(t, list.PullTimeUpdateExclusions, principal)

	dereg, err := client.DeregisterPullTimeUpdateExclusion(ctx, &ecr.DeregisterPullTimeUpdateExclusionInput{
		PrincipalArn: aws.String(principal),
	})
	require.NoError(t, err)
	assert.Equal(t, principal, aws.ToString(dereg.PrincipalArn))

	// Deregistering a principal that is not excluded raises ExclusionNotFound.
	_, err = client.DeregisterPullTimeUpdateExclusion(ctx, &ecr.DeregisterPullTimeUpdateExclusionInput{
		PrincipalArn: aws.String(principal),
	})
	require.Error(t, err)
}

// TestECR_PullThroughCacheUpdateValidate exercises UpdatePullThroughCacheRule
// and ValidatePullThroughCacheRule against an existing rule.
func TestECR_PullThroughCacheUpdateValidate(t *testing.T) {
	client := ecrClient()
	prefix := "ecr-ptc-update"

	_, _ = client.DeletePullThroughCacheRule(ctx, &ecr.DeletePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
	})
	_, err := client.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
		UpstreamRegistryUrl: aws.String("registry-1.docker.io"),
	})
	require.NoError(t, err)

	upd, err := client.UpdatePullThroughCacheRule(ctx, &ecr.UpdatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
		CredentialArn:       aws.String("arn:aws:secretsmanager:us-east-1:123456789012:secret:ecr-pullthroughcache/cred-abc"),
	})
	require.NoError(t, err)
	assert.Equal(t, prefix, aws.ToString(upd.EcrRepositoryPrefix))
	assert.Equal(t, "arn:aws:secretsmanager:us-east-1:123456789012:secret:ecr-pullthroughcache/cred-abc",
		aws.ToString(upd.CredentialArn))

	val, err := client.ValidatePullThroughCacheRule(ctx, &ecr.ValidatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
	})
	require.NoError(t, err)
	assert.Equal(t, prefix, aws.ToString(val.EcrRepositoryPrefix))
	assert.Equal(t, "registry-1.docker.io", aws.ToString(val.UpstreamRegistryUrl))
	assert.True(t, val.IsValid)

	// Validating an unknown prefix raises PullThroughCacheRuleNotFound.
	_, err = client.ValidatePullThroughCacheRule(ctx, &ecr.ValidatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String("ecr-ptc-no-such-prefix"),
	})
	require.Error(t, err)

	_, _ = client.DeletePullThroughCacheRule(ctx, &ecr.DeletePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String(prefix),
	})
}

// TestECR_ListImageReferrersAndStorageClass exercises ListImageReferrers
// (honest empty referrer list for a stored subject image) and
// UpdateImageStorageClass (transition between STANDARD and ARCHIVE).
func TestECR_ListImageReferrersAndStorageClass(t *testing.T) {
	client := ecrClient()
	repo := "ecr-referrers-repo"
	digest := ecrPutTestImage(t, client, repo, "v1")

	ref, err := client.ListImageReferrers(ctx, &ecr.ListImageReferrersInput{
		RepositoryName: aws.String(repo),
		SubjectId:      &ecrtypes.SubjectIdentifier{ImageDigest: aws.String(digest)},
	})
	require.NoError(t, err)
	// No artifacts are pushed against the subject, so the list is empty.
	assert.Empty(t, ref.Referrers)

	arch, err := client.UpdateImageStorageClass(ctx, &ecr.UpdateImageStorageClassInput{
		RepositoryName:     aws.String(repo),
		ImageId:            &ecrtypes.ImageIdentifier{ImageDigest: aws.String(digest)},
		TargetStorageClass: ecrtypes.TargetStorageClassArchive,
	})
	require.NoError(t, err)
	assert.Equal(t, ecrtypes.ImageStatusArchived, arch.ImageStatus)

	restore, err := client.UpdateImageStorageClass(ctx, &ecr.UpdateImageStorageClassInput{
		RepositoryName:     aws.String(repo),
		ImageId:            &ecrtypes.ImageIdentifier{ImageDigest: aws.String(digest)},
		TargetStorageClass: ecrtypes.TargetStorageClassStandard,
	})
	require.NoError(t, err)
	assert.Equal(t, ecrtypes.ImageStatusActivating, restore.ImageStatus)

	// An unknown image raises ImageNotFound.
	_, err = client.UpdateImageStorageClass(ctx, &ecr.UpdateImageStorageClassInput{
		RepositoryName:     aws.String(repo),
		ImageId:            &ecrtypes.ImageIdentifier{ImageDigest: aws.String("sha256:" + "0000000000000000000000000000000000000000000000000000000000000000")},
		TargetStorageClass: ecrtypes.TargetStorageClassArchive,
	})
	require.Error(t, err)
}

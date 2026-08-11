package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCLI_ECR_AccountSettings drives put-account-setting / get-account-setting
// through the official AWS CLI and confirms the value round-trips.
func TestCLI_ECR_AccountSettings(t *testing.T) {
	runCLI(t, awsCLI(
		"ecr", "put-account-setting",
		"--name", "BASIC_SCAN_TYPE_VERSION",
		"--value", "AWS_NATIVE",
	))
	out := runCLI(t, awsCLI(
		"ecr", "get-account-setting",
		"--name", "BASIC_SCAN_TYPE_VERSION",
	))
	var resp struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	parseJSON(t, out, &resp)
	assert.Equal(t, "BASIC_SCAN_TYPE_VERSION", resp.Name)
	assert.Equal(t, "AWS_NATIVE", resp.Value)
}

// TestCLI_ECR_RegistryScanningConfiguration round-trips the registry
// scanning configuration and the per-repository batch-get path.
func TestCLI_ECR_RegistryScanningConfiguration(t *testing.T) {
	repo := "cli-scan-repo"
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))

	runCLI(t, awsCLI(
		"ecr", "put-registry-scanning-configuration",
		"--scan-type", "ENHANCED",
		"--rules", `[{"scanFrequency":"CONTINUOUS_SCAN","repositoryFilters":[{"filter":"*","filterType":"WILDCARD"}]}]`,
	))
	out := runCLI(t, awsCLI("ecr", "get-registry-scanning-configuration"))
	var get struct {
		ScanningConfiguration struct {
			ScanType string `json:"scanType"`
		} `json:"scanningConfiguration"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "ENHANCED", get.ScanningConfiguration.ScanType)

	bout := runCLI(t, awsCLI(
		"ecr", "batch-get-repository-scanning-configuration",
		"--repository-names", repo,
	))
	var batch struct {
		ScanningConfigurations []struct {
			RepositoryName string `json:"repositoryName"`
			ScanFrequency  string `json:"scanFrequency"`
		} `json:"scanningConfigurations"`
	}
	parseJSON(t, bout, &batch)
	require.Len(t, batch.ScanningConfigurations, 1)
	assert.Equal(t, repo, batch.ScanningConfigurations[0].RepositoryName)
	assert.Equal(t, "CONTINUOUS_SCAN", batch.ScanningConfigurations[0].ScanFrequency)
}

// TestCLI_ECR_RepositoryCreationTemplates exercises the Create / Describe /
// Update / Delete lifecycle of a repository creation template.
func TestCLI_ECR_RepositoryCreationTemplates(t *testing.T) {
	prefix := "cli-tmpl-prefix"

	// Tolerant cleanup of a leftover template.
	runCLIIgnore(awsCLI(
		"ecr", "delete-repository-creation-template", "--prefix", prefix,
	))

	cout := runCLI(t, awsCLI(
		"ecr", "create-repository-creation-template",
		"--prefix", prefix,
		"--description", "cli template",
		"--applied-for", "PULL_THROUGH_CACHE",
		"--image-tag-mutability", "IMMUTABLE",
	))
	var created struct {
		RepositoryCreationTemplate struct {
			Prefix             string `json:"prefix"`
			ImageTagMutability string `json:"imageTagMutability"`
		} `json:"repositoryCreationTemplate"`
	}
	parseJSON(t, cout, &created)
	assert.Equal(t, prefix, created.RepositoryCreationTemplate.Prefix)
	assert.Equal(t, "IMMUTABLE", created.RepositoryCreationTemplate.ImageTagMutability)

	dout := runCLI(t, awsCLI(
		"ecr", "describe-repository-creation-templates", "--prefixes", prefix,
	))
	var desc struct {
		RepositoryCreationTemplates []struct {
			Description string `json:"description"`
		} `json:"repositoryCreationTemplates"`
	}
	parseJSON(t, dout, &desc)
	require.Len(t, desc.RepositoryCreationTemplates, 1)
	assert.Equal(t, "cli template", desc.RepositoryCreationTemplates[0].Description)

	uout := runCLI(t, awsCLI(
		"ecr", "update-repository-creation-template",
		"--prefix", prefix,
		"--description", "cli updated",
	))
	var upd struct {
		RepositoryCreationTemplate struct {
			Description string `json:"description"`
		} `json:"repositoryCreationTemplate"`
	}
	parseJSON(t, uout, &upd)
	assert.Equal(t, "cli updated", upd.RepositoryCreationTemplate.Description)

	runCLI(t, awsCLI(
		"ecr", "delete-repository-creation-template", "--prefix", prefix,
	))
}

// TestCLI_ECR_SigningConfiguration round-trips the managed-signing
// configuration (put / get / delete) and the image-signing-status path.
func TestCLI_ECR_SigningConfiguration(t *testing.T) {
	// Tolerant cleanup of a leftover config.
	runCLIIgnore(awsCLI("ecr", "delete-signing-configuration"))

	runCLI(t, awsCLI(
		"ecr", "put-signing-configuration",
		"--signing-configuration", `{"rules":[{"signingProfileArn":"arn:aws:signer:us-east-1:123456789012:/signing-profiles/p1"}]}`,
	))
	out := runCLI(t, awsCLI("ecr", "get-signing-configuration"))
	var get struct {
		SigningConfiguration struct {
			Rules []struct {
				SigningProfileArn string `json:"signingProfileArn"`
			} `json:"rules"`
		} `json:"signingConfiguration"`
	}
	parseJSON(t, out, &get)
	require.Len(t, get.SigningConfiguration.Rules, 1)
	assert.Equal(t, "arn:aws:signer:us-east-1:123456789012:/signing-profiles/p1",
		get.SigningConfiguration.Rules[0].SigningProfileArn)

	repo := "cli-signing-repo"
	digest := cliECRPutTestImage(t, repo, "v1")
	sout := runCLI(t, awsCLI(
		"ecr", "describe-image-signing-status",
		"--repository-name", repo,
		"--image-id", "imageDigest="+digest,
	))
	var sig struct {
		RepositoryName  string `json:"repositoryName"`
		SigningStatuses []any  `json:"signingStatuses"`
	}
	parseJSON(t, sout, &sig)
	assert.Equal(t, repo, sig.RepositoryName)
	assert.Empty(t, sig.SigningStatuses)

	runCLI(t, awsCLI("ecr", "delete-signing-configuration"))
}

// TestCLI_ECR_PullTimeUpdateExclusions round-trips register / list /
// deregister of pull-time update exclusions.
func TestCLI_ECR_PullTimeUpdateExclusions(t *testing.T) {
	principal := "arn:aws:iam::123456789012:role/CLIECRAccess"

	// Tolerant cleanup.
	runCLIIgnore(awsCLI(
		"ecr", "deregister-pull-time-update-exclusion", "--principal-arn", principal,
	))

	rout := runCLI(t, awsCLI(
		"ecr", "register-pull-time-update-exclusion", "--principal-arn", principal,
	))
	var reg struct {
		PrincipalArn string `json:"principalArn"`
	}
	parseJSON(t, rout, &reg)
	assert.Equal(t, principal, reg.PrincipalArn)

	lout := runCLI(t, awsCLI("ecr", "list-pull-time-update-exclusions"))
	var list struct {
		PullTimeUpdateExclusions []string `json:"pullTimeUpdateExclusions"`
	}
	parseJSON(t, lout, &list)
	assert.Contains(t, list.PullTimeUpdateExclusions, principal)

	runCLI(t, awsCLI(
		"ecr", "deregister-pull-time-update-exclusion", "--principal-arn", principal,
	))
}

// TestCLI_ECR_PullThroughCacheUpdateValidate exercises
// update-pull-through-cache-rule and validate-pull-through-cache-rule.
func TestCLI_ECR_PullThroughCacheUpdateValidate(t *testing.T) {
	prefix := "cli-ptc-update"
	runCLIIgnore(awsCLI(
		"ecr", "delete-pull-through-cache-rule", "--ecr-repository-prefix", prefix,
	))
	runCLI(t, awsCLI(
		"ecr", "create-pull-through-cache-rule",
		"--ecr-repository-prefix", prefix,
		"--upstream-registry-url", "registry-1.docker.io",
		"--upstream-registry", "docker-hub",
	))

	uout := runCLI(t, awsCLI(
		"ecr", "update-pull-through-cache-rule",
		"--ecr-repository-prefix", prefix,
		"--credential-arn", "arn:aws:secretsmanager:us-east-1:123456789012:secret:ecr-pullthroughcache/cred-xyz",
	))
	var upd struct {
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		CredentialArn       string `json:"credentialArn"`
	}
	parseJSON(t, uout, &upd)
	assert.Equal(t, prefix, upd.EcrRepositoryPrefix)
	assert.Equal(t, "arn:aws:secretsmanager:us-east-1:123456789012:secret:ecr-pullthroughcache/cred-xyz", upd.CredentialArn)

	vout := runCLI(t, awsCLI(
		"ecr", "validate-pull-through-cache-rule",
		"--ecr-repository-prefix", prefix,
	))
	var val struct {
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		IsValid             bool   `json:"isValid"`
	}
	parseJSON(t, vout, &val)
	assert.Equal(t, prefix, val.EcrRepositoryPrefix)
	assert.True(t, val.IsValid)

	runCLI(t, awsCLI(
		"ecr", "delete-pull-through-cache-rule", "--ecr-repository-prefix", prefix,
	))
}

// TestCLI_ECR_ListImageReferrersAndStorageClass exercises list-image-referrers
// (honest empty) and update-image-storage-class.
func TestCLI_ECR_ListImageReferrersAndStorageClass(t *testing.T) {
	repo := "cli-referrers-repo"
	digest := cliECRPutTestImage(t, repo, "v1")

	rout := runCLI(t, awsCLI(
		"ecr", "list-image-referrers",
		"--repository-name", repo,
		"--subject-id", "imageDigest="+digest,
	))
	var ref struct {
		Referrers []any `json:"referrers"`
	}
	parseJSON(t, rout, &ref)
	assert.Empty(t, ref.Referrers)

	sout := runCLI(t, awsCLI(
		"ecr", "update-image-storage-class",
		"--repository-name", repo,
		"--image-id", "imageDigest="+digest,
		"--target-storage-class", "ARCHIVE",
	))
	var sc struct {
		ImageStatus string `json:"imageStatus"`
	}
	parseJSON(t, sout, &sc)
	assert.Equal(t, "ARCHIVED", sc.ImageStatus)
}

// cliECRPutTestImage creates a repository (ignoring already-exists) and pushes
// one image via the AWS CLI, returning its digest.
func cliECRPutTestImage(t *testing.T, repo, tag string) string {
	t.Helper()
	runCLIIgnore(awsCLI("ecr", "create-repository", "--repository-name", repo))
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"layers":[]}`
	out := runCLI(t, awsCLI(
		"ecr", "put-image",
		"--repository-name", repo,
		"--image-manifest", manifest,
		"--image-tag", tag,
	))
	var resp struct {
		Image struct {
			ImageId struct {
				ImageDigest string `json:"imageDigest"`
			} `json:"imageId"`
		} `json:"image"`
	}
	parseJSON(t, out, &resp)
	return resp.Image.ImageId.ImageDigest
}

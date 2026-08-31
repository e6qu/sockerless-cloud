package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cloudkms "google.golang.org/api/cloudkms/v1"
	"google.golang.org/api/option"
)

func cloudKMSService(t *testing.T) *cloudkms.Service {
	t.Helper()
	svc, err := cloudkms.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

// The effective Key Access Justifications configuration a project is under, and
// the enrolment behind it. Neither reports a policy nobody set.
func TestCloudKMS_EffectiveKeyAccessJustificationsConfig(t *testing.T) {
	svc := cloudKMSService(t)
	const project = "kaj-project"

	policy, err := svc.Projects.ShowEffectiveKeyAccessJustificationsPolicyConfig(
		"projects/" + project).Do()
	require.NoError(t, err)
	require.NotNil(t, policy.EffectiveKajPolicy)
	assert.True(t, policy.EffectiveKajPolicy.DefaultPolicyAvailable,
		"a project with no policy of its own has the default available to it")

	// Enrolment is off until something turns it on: reporting it on would say
	// the project's keys demand justifications when they do not.
	enrolment, err := svc.Projects.ShowEffectiveKeyAccessJustificationsEnrollmentConfig(
		"projects/" + project).Do()
	require.NoError(t, err)
	require.NotNil(t, enrolment.SoftwareConfig)
	assert.False(t, enrolment.SoftwareConfig.PolicyEnforcement)
	require.NotNil(t, enrolment.HardwareConfig)
	assert.False(t, enrolment.HardwareConfig.AuditLogging)
	require.NotNil(t, enrolment.ExternalConfig)
}

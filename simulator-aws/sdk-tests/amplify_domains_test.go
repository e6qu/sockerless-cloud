package aws_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/amplify"
	amplifytypes "github.com/aws/aws-sdk-go-v2/service/amplify/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAmplifyDomainAssociationLifecycle(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("dom-app-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() {
		_, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	}()
	_, _ = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})

	dom, err := c.CreateDomainAssociation(ctx, &amplify.CreateDomainAssociationInput{
		AppId:               aws.String(appID),
		DomainName:          aws.String("sdk-test.example.com"),
		EnableAutoSubDomain: aws.Bool(false),
		SubDomainSettings: []amplifytypes.SubDomainSetting{
			{Prefix: aws.String("www"), BranchName: aws.String("main")},
			{Prefix: aws.String(""), BranchName: aws.String("main")}, // apex
		},
	})
	require.NoError(t, err)
	require.NotNil(t, dom.DomainAssociation)
	assert.Equal(t, "sdk-test.example.com", aws.ToString(dom.DomainAssociation.DomainName))
	// No verification records exist in Route 53 → the association honestly
	// reports PENDING_VERIFICATION (the route53-backed flip to AVAILABLE is
	// covered by TestAmplifyDomainVerificationRoute53Flow).
	assert.Equal(t, amplifytypes.DomainStatusPendingVerification, dom.DomainAssociation.DomainStatus)
	require.NotEmpty(t, aws.ToString(dom.DomainAssociation.CertificateVerificationDNSRecord))
	require.Len(t, dom.DomainAssociation.SubDomains, 2)

	getDom, err := c.GetDomainAssociation(ctx, &amplify.GetDomainAssociationInput{
		AppId: aws.String(appID), DomainName: aws.String("sdk-test.example.com"),
	})
	require.NoError(t, err)
	assert.Equal(t, "sdk-test.example.com", aws.ToString(getDom.DomainAssociation.DomainName))

	listDom, err := c.ListDomainAssociations(ctx, &amplify.ListDomainAssociationsInput{
		AppId: aws.String(appID),
	})
	require.NoError(t, err)
	require.Len(t, listDom.DomainAssociations, 1)

	updDom, err := c.UpdateDomainAssociation(ctx, &amplify.UpdateDomainAssociationInput{
		AppId:               aws.String(appID),
		DomainName:          aws.String("sdk-test.example.com"),
		EnableAutoSubDomain: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(updDom.DomainAssociation.EnableAutoSubDomain))

	_, err = c.DeleteDomainAssociation(ctx, &amplify.DeleteDomainAssociationInput{
		AppId: aws.String(appID), DomainName: aws.String("sdk-test.example.com"),
	})
	require.NoError(t, err)
}

func TestAmplifyBackendEnvironmentLifecycle(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("be-app-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() {
		_, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	}()

	be, err := c.CreateBackendEnvironment(ctx, &amplify.CreateBackendEnvironmentInput{
		AppId:           aws.String(appID),
		EnvironmentName: aws.String("staging"),
		StackName:       aws.String("amplify-staging-stack"),
	})
	require.NoError(t, err)
	require.NotNil(t, be.BackendEnvironment)
	assert.Equal(t, "staging", aws.ToString(be.BackendEnvironment.EnvironmentName))

	getBe, err := c.GetBackendEnvironment(ctx, &amplify.GetBackendEnvironmentInput{
		AppId: aws.String(appID), EnvironmentName: aws.String("staging"),
	})
	require.NoError(t, err)
	assert.Equal(t, "amplify-staging-stack", aws.ToString(getBe.BackendEnvironment.StackName))

	listBe, err := c.ListBackendEnvironments(ctx, &amplify.ListBackendEnvironmentsInput{
		AppId: aws.String(appID),
	})
	require.NoError(t, err)
	require.Len(t, listBe.BackendEnvironments, 1)

	_, err = c.DeleteBackendEnvironment(ctx, &amplify.DeleteBackendEnvironmentInput{
		AppId: aws.String(appID), EnvironmentName: aws.String("staging"),
	})
	require.NoError(t, err)
}

func TestAmplifyDeleteAppCascadesDomainsAndBackends(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("cascade-" + time.Now().Format("150405.000000")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"),
	})
	require.NoError(t, err)
	_, err = c.CreateDomainAssociation(ctx, &amplify.CreateDomainAssociationInput{
		AppId:      aws.String(appID),
		DomainName: aws.String("cascade.example.com"),
		SubDomainSettings: []amplifytypes.SubDomainSetting{
			{Prefix: aws.String("www"), BranchName: aws.String("main")},
		},
	})
	require.NoError(t, err)
	_, err = c.CreateBackendEnvironment(ctx, &amplify.CreateBackendEnvironmentInput{
		AppId:           aws.String(appID),
		EnvironmentName: aws.String("staging"),
	})
	require.NoError(t, err)

	_, err = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	require.NoError(t, err)

	// The domain/backend stores must hold no orphans for the deleted app:
	// the Get handlers consult only their own stores, so a leftover row
	// would still be served here.
	var notFound *amplifytypes.NotFoundException
	_, err = c.GetDomainAssociation(ctx, &amplify.GetDomainAssociationInput{
		AppId: aws.String(appID), DomainName: aws.String("cascade.example.com"),
	})
	require.ErrorAs(t, err, &notFound, "domain association must be cascade-deleted with the app")
	_, err = c.GetBackendEnvironment(ctx, &amplify.GetBackendEnvironmentInput{
		AppId: aws.String(appID), EnvironmentName: aws.String("staging"),
	})
	require.ErrorAs(t, err, &notFound, "backend environment must be cascade-deleted with the app")
}

func TestAmplifyAppCustomRulesRoundTrip(t *testing.T) {
	c := amplifyClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name: aws.String("rules-" + time.Now().Format("150405.000000")),
		CustomRules: []amplifytypes.CustomRule{
			{
				Source: aws.String("/old"),
				Target: aws.String("/new"),
				Status: aws.String("301"),
			},
			{
				Source:    aws.String("/<*>"),
				Target:    aws.String("/index.html"),
				Status:    aws.String("200"),
				Condition: aws.String("<US>"),
			},
		},
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	defer func() {
		_, _ = c.DeleteApp(ctx, &amplify.DeleteAppInput{AppId: aws.String(appID)})
	}()

	getOut, err := c.GetApp(ctx, &amplify.GetAppInput{AppId: aws.String(appID)})
	require.NoError(t, err)
	require.Len(t, getOut.App.CustomRules, 2)
	assert.Equal(t, "/old", aws.ToString(getOut.App.CustomRules[0].Source))
	assert.Equal(t, "301", aws.ToString(getOut.App.CustomRules[0].Status))
}

package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodeBuild_RequestConditionKeysScopeTheGrant covers the AWS CodeBuild keys
// that read what a request asks for: the policies AWS documents for them
// restrict the VPC a project uses and refuse a build that overrides its
// buildspec.
func TestCodeBuild_RequestConditionKeysScopeTheGrant(t *testing.T) {
	akid, secret := restrictedCredential(t, "codebuild-vpc-and-buildspec",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"codebuild:CreateProject","Resource":"*","Condition":{
		    "StringEquals":{"codebuild:vpcConfig.vpcId":"vpc-allowed"}}},
		  {"Effect":"Allow","Action":"iam:PassRole","Resource":"arn:aws:iam::123456789012:role/codebuild-service"},
		  {"Effect":"Allow","Action":"codebuild:StartBuild","Resource":"*"},
		  {"Effect":"Deny","Action":"codebuild:StartBuild","Resource":"*","Condition":{
		    "Null":{"codebuild:source.buildspec":"false"}}}]}`)
	restricted := codebuild.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *codebuild.Options) { o.BaseEndpoint = aws.String(baseURL) })

	create := func(name, vpcID string) error {
		_, err := restricted.CreateProject(ctx, &codebuild.CreateProjectInput{
			Name:        aws.String(name),
			Source:      &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource, Buildspec: aws.String("version: 0.2")},
			Artifacts:   &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
			Environment: &cbtypes.ProjectEnvironment{Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("aws/codebuild/standard:7.0"), ComputeType: cbtypes.ComputeTypeBuildGeneral1Small},
			ServiceRole: aws.String("arn:aws:iam::123456789012:role/codebuild-service"),
			VpcConfig: &cbtypes.VpcConfig{VpcId: aws.String(vpcID),
				Subnets: []string{"subnet-cond"}, SecurityGroupIds: []string{"sg-cond"}},
		})
		return err
	}
	if err := create("cond-vpc-allowed", "vpc-allowed"); err != nil {
		assert.NotContains(t, err.Error(), "not authorized", "the project uses the VPC the grant allows")
	}
	err := create("cond-vpc-other", "vpc-other")
	require.Error(t, err, "a project in another VPC is not allowed by the grant")
	assert.Contains(t, err.Error(), "not authorized")

	start := func(buildspec *string) error {
		_, err := restricted.StartBuild(ctx, &codebuild.StartBuildInput{
			ProjectName:       aws.String("cond-buildspec-absent-project"),
			BuildspecOverride: buildspec,
		})
		return err
	}
	if err := start(nil); err != nil {
		assert.NotContains(t, err.Error(), "not authorized", "a build that keeps the project's buildspec is allowed")
	}
	err = start(aws.String("version: 0.2\nphases: {build: {commands: [curl evil]}}"))
	require.Error(t, err, "a build that overrides its buildspec is denied")
	assert.Contains(t, err.Error(), "not authorized")
}

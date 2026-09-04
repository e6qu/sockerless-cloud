package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_RequestShapeConditionKeysScopeTheGrant covers the Amazon ECS keys
// that describe what a request asks for rather than what it names: the size the
// task takes, the capacity provider it is placed on, and whether exec is on. A
// policy holds callers to a shape with them — only small tasks, only on Fargate
// Spot, never with exec enabled.
func TestECS_RequestShapeConditionKeysScopeTheGrant(t *testing.T) {
	admin := ecsClient()
	const family = "cond-shape-task"
	_, err := admin.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		Cpu:    aws.String("256"), Memory: aws.String("512"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  aws.String("app"),
			Image: aws.String("public.ecr.aws/docker/library/busybox:latest"),
		}},
	})
	require.NoError(t, err)

	// The shape a real policy takes. Exec is refused with a Deny on the key
	// being true rather than an Allow on it being false, because a request that
	// does not ask for exec carries no such member and so settles no key —
	// which is what Amazon ECS does too.
	akid, secret := restrictedCredential(t, "ecs-small-spot-no-exec",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"ecs:CreateService","Resource":"*","Condition":{
		    "StringEquals":{"ecs:task-cpu":"256"},
		    "ForAllValues:StringEquals":{"ecs:capacity-provider":["FARGATE_SPOT"]}}},
		  {"Effect":"Deny","Action":"ecs:CreateService","Resource":"*","Condition":{
		    "Bool":{"ecs:enable-execute-command":"true"}}}]}`)
	restricted := ecs.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	// Authorization is what is under test, so a request that clears IAM and
	// then fails on the cluster it names is a different outcome from a refusal.
	create := func(name string, exec bool, provider string) error {
		_, err := restricted.CreateService(ctx, &ecs.CreateServiceInput{
			ServiceName: aws.String(name), Cluster: aws.String("cond-shape-absent-cluster"),
			TaskDefinition:       aws.String(family),
			EnableExecuteCommand: exec,
			CapacityProviderStrategy: []ecstypes.CapacityProviderStrategyItem{
				{CapacityProvider: aws.String(provider), Weight: 1}},
		})
		return err
	}

	if err := create("cond-shape-allowed", false, "FARGATE_SPOT"); err != nil {
		assert.NotContains(t, err.Error(), "not authorized",
			"the task is the size, the provider and the exec setting the grant allows")
	}

	err = create("cond-shape-exec", true, "FARGATE_SPOT")
	require.Error(t, err, "a request asking for exec is denied outright")
	assert.Contains(t, err.Error(), "not authorized")

	err = create("cond-shape-provider", false, "FARGATE")
	require.Error(t, err, "another capacity provider is not allowed by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func useLambdaConditionStores(t *testing.T) {
	t.Helper()
	previousPolicies, previousSubnets := lambdaPolicies, ec2Subnets
	t.Cleanup(func() { lambdaPolicies, ec2Subnets = previousPolicies, previousSubnets })
	AwaitSimulatorBackground()
	lambdaPolicies = sim.MakeStore[[]LambdaPolicyStatement](nil, "lambda_policies")
	ec2Subnets = sim.MakeStore[EC2Subnet](nil, "ec2_subnets")
}

func TestLambdaConditionKeysReadTheFunctionConfiguration(t *testing.T) {
	useLambdaConditionStores(t)
	ec2Subnets.Put("subnet-a", EC2Subnet{SubnetId: "subnet-a", VpcId: "vpc-1"})
	ec2Subnets.Put("subnet-b", EC2Subnet{SubnetId: "subnet-b", VpcId: "vpc-1"})

	r := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions", nil)
	ctx := populatedConditionContext(r, "lambda", "CreateFunction", `{
		"FunctionName": "f",
		"CodeSigningConfigArn": "arn:aws:lambda:us-east-1:123456789012:code-signing-config:csc-1",
		"Layers": ["arn:aws:lambda:us-east-1:123456789012:layer:l:1", "arn:aws:lambda:us-east-1:123456789012:layer:m:2"],
		"VpcConfig": {"SubnetIds": ["subnet-a", "subnet-b"], "SecurityGroupIds": ["sg-1"]}
	}`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{
		"lambda:CodeSigningConfigArn": {"arn:aws:lambda:us-east-1:123456789012:code-signing-config:csc-1"},
		"lambda:Layer": {
			"arn:aws:lambda:us-east-1:123456789012:layer:l:1",
			"arn:aws:lambda:us-east-1:123456789012:layer:m:2",
		},
		"lambda:SubnetIds":        {"subnet-a", "subnet-b"},
		"lambda:SecurityGroupIds": {"sg-1"},
		"lambda:VpcIds":           {"vpc-1"},
	})

	r = httptest.NewRequest(http.MethodPost, "/2025-11-30/capacity-providers", nil)
	ctx = populatedConditionContext(r, "lambda", "CreateCapacityProvider",
		`{"CapacityProviderName": "cp", "VpcConfig": {"SubnetIds": ["subnet-a"], "SecurityGroupIds": ["sg-2"]}}`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{
		"lambda:SubnetIds":        {"subnet-a"},
		"lambda:SecurityGroupIds": {"sg-2"},
	})
	assertConditionKeysAbsent(t, ctx, "lambda:VpcIds", "lambda:Layer")

	r = httptest.NewRequest(http.MethodPut, "/2020-06-30/functions/f/code-signing-config", nil)
	r.SetPathValue("name", "f")
	ctx = populatedConditionContext(r, "lambda", "PutFunctionCodeSigningConfig",
		`{"CodeSigningConfigArn": "arn:aws:lambda:us-east-1:123456789012:code-signing-config:csc-2"}`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{
		"lambda:CodeSigningConfigArn": {"arn:aws:lambda:us-east-1:123456789012:code-signing-config:csc-2"},
	})
}

func TestLambdaConditionKeysReadThePermissionPrincipal(t *testing.T) {
	useLambdaConditionStores(t)
	r := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions/f/policy", nil)
	r.SetPathValue("name", "f")
	ctx := populatedConditionContext(r, "lambda", "AddPermission",
		`{"StatementId": "s3", "Action": "lambda:InvokeFunction", "Principal": "s3.amazonaws.com"}`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{"lambda:Principal": {"s3.amazonaws.com"}})

	lambdaPolicies.Put("f", []LambdaPolicyStatement{
		{Sid: "other", Principal: map[string]any{"Service": "sns.amazonaws.com"}},
		{Sid: "s3", Principal: map[string]any{"Service": "s3.amazonaws.com"}},
	})
	r = httptest.NewRequest(http.MethodDelete, "/2015-03-31/functions/f/policy/s3", nil)
	r.SetPathValue("name", "f")
	r.SetPathValue("statement", "s3")
	ctx = populatedConditionContext(r, "lambda", "RemovePermission", "")
	assertPopulatedConditionValues(t, ctx, map[string][]string{"lambda:Principal": {"s3.amazonaws.com"}})

	r = httptest.NewRequest(http.MethodDelete, "/2015-03-31/functions/f/policy/missing", nil)
	r.SetPathValue("name", "f")
	r.SetPathValue("statement", "missing")
	assertConditionKeysAbsent(t, populatedConditionContext(r, "lambda", "RemovePermission", ""), "lambda:Principal")
}

func TestLambdaConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	useLambdaConditionStores(t)
	r := httptest.NewRequest(http.MethodPut, "/2015-03-31/functions/f/configuration", nil)
	r.SetPathValue("name", "f")
	ctx := populatedConditionContext(r, "lambda", "UpdateFunctionConfiguration", `{"Timeout": 30}`)
	assertConditionKeysAbsent(t, ctx, "lambda:CodeSigningConfigArn", "lambda:Layer",
		"lambda:SubnetIds", "lambda:SecurityGroupIds", "lambda:VpcIds", "lambda:Principal")

	// UpdateFunctionConfiguration carries no code signing config, so the
	// key is not settled by a member the operation does not declare.
	ctx = populatedConditionContext(r, "lambda", "UpdateFunctionConfiguration",
		`{"CodeSigningConfigArn": "arn:aws:lambda:us-east-1:123456789012:code-signing-config:csc-1"}`)
	assertConditionKeysAbsent(t, ctx, "lambda:CodeSigningConfigArn")
}

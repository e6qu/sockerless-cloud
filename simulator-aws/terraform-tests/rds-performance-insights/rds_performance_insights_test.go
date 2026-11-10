package rds_performance_insights_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDSPerformanceInsightsTerraform turns Performance Insights on with a
// customer managed key and a retention period of three months.
func TestRDSPerformanceInsightsTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve", "-var", "retention=93")
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	assert.Equal(t, "true", outputs["enabled"].Value)
	assert.Equal(t, "93", outputs["retention"].Value)
	assert.Equal(t, "true", outputs["kms_key_matches"].Value)
	client := rds.New(rds.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), HTTPClient: env.Client,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")})
	described, err := client.DescribeDBInstances(context.Background(), &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String("tf-pi-db")})
	require.NoError(t, err)
	require.Len(t, described.DBInstances, 1)
	assert.Equal(t, int32(93), aws.ToInt32(described.DBInstances[0].PerformanceInsightsRetentionPeriod))
	env.Terraform(t, "plan", "-detailed-exitcode", "-var", "retention=93")

	env.Terraform(t, "destroy", "-auto-approve", "-var", "retention=93")
}

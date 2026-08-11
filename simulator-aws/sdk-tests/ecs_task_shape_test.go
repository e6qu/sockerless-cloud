package aws_sdk_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_TaskWireShapeOmitsNetworkConfiguration pins the Task response
// shape: the real Task conveys networking via attachments and has no
// networkConfiguration member, even when RunTask was called with one.
// The SDK silently drops unknown members, so the absence is asserted at
// the wire level alongside the SDK view of the ENI attachment.
func TestECS_TaskWireShapeOmitsNetworkConfiguration(t *testing.T) {
	client, clusterName, taskArn := ecsRunTaskHelper(t, "task-wire-shape", ecstypes.ContainerDefinition{
		Name:    aws.String("main"),
		Image:   aws.String("alpine:latest"),
		Command: []string{"sleep", "30"}, // long-running so RUNNING window is real
	})
	waitForECSTaskStatus(t, client, clusterName, taskArn, "RUNNING")

	// SDK view: networking rides the ElasticNetworkInterface attachment.
	desc, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, desc.Tasks, 1)
	var eni *ecstypes.Attachment
	for i := range desc.Tasks[0].Attachments {
		if aws.ToString(desc.Tasks[0].Attachments[i].Type) == "ElasticNetworkInterface" {
			eni = &desc.Tasks[0].Attachments[i]
		}
	}
	require.NotNil(t, eni, "task must carry an ElasticNetworkInterface attachment")
	details := map[string]string{}
	for _, d := range eni.Details {
		details[aws.ToString(d.Name)] = aws.ToString(d.Value)
	}
	assert.NotEmpty(t, details["subnetId"], "ENI attachment must carry the subnet")

	// Wire view: the task object has no networkConfiguration member.
	rawReq, err := json.Marshal(map[string]any{
		"cluster": clusterName,
		"tasks":   []string{taskArn},
	})
	require.NoError(t, err)
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/", bytes.NewReader(rawReq))
	require.NoError(t, err)
	httpReq.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113.DescribeTasks")
	httpReq.Header.Set("Content-Type", "application/x-amz-json-1.1")
	signRawSigV4JSON(t, httpReq, "ecs", rawReq)
	resp, err := http.DefaultClient.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var wire struct {
		Tasks []map[string]json.RawMessage `json:"tasks"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&wire))
	require.Len(t, wire.Tasks, 1)
	assert.NotContains(t, wire.Tasks[0], "networkConfiguration",
		"Task must not echo the RunTask request's networkConfiguration")
	assert.Contains(t, wire.Tasks[0], "attachments")
}

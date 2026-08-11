package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQS_MessageAttributesAndFirstReceive covers ReceiveMessage's
// MessageAttributeNames (custom attributes were never stored/returned) and the
// ApproximateFirstReceiveTimestamp system attribute (previously omitted). The
// aws-sdk-go-v2 client validates MD5OfMessageAttributes, so a wrong digest here
// would error — this also pins the MD5 algorithm.
func TestSQS_MessageAttributesAndFirstReceive(t *testing.T) {
	c := sqsClient()
	q, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("fidelity-attrs-q")})
	require.NoError(t, err)
	url := aws.ToString(q.QueueUrl)
	defer c.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)})

	_, err = c.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(url),
		MessageBody: aws.String("hello"),
		MessageAttributes: map[string]sqstypes.MessageAttributeValue{
			"env": {DataType: aws.String("String"), StringValue: aws.String("prod")},
		},
	})
	require.NoError(t, err)

	out, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(url),
		MaxNumberOfMessages:         1,
		MessageAttributeNames:       []string{"All"},
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
	})
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	m := out.Messages[0]
	require.Contains(t, m.MessageAttributes, "env", "custom message attribute must round-trip")
	assert.Equal(t, "prod", aws.ToString(m.MessageAttributes["env"].StringValue))
	assert.NotEmpty(t, m.Attributes["ApproximateFirstReceiveTimestamp"],
		"ApproximateFirstReceiveTimestamp system attribute must be present")
}

// TestDynamoDB_ProjectionExpression covers GetItem/Query/Scan honoring
// ProjectionExpression (previously the whole item came back).
func TestDynamoDB_ProjectionExpression(t *testing.T) {
	c := ddbClient()
	table := "fidelity-projection"
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode:          ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)})

	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item: map[string]ddbtypes.AttributeValue{
			"PK":   &ddbtypes.AttributeValueMemberS{Value: "row1"},
			"keep": &ddbtypes.AttributeValueMemberS{Value: "yes"},
			"drop": &ddbtypes.AttributeValueMemberS{Value: "no"},
		},
	})
	require.NoError(t, err)

	got, err := c.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:            aws.String(table),
		Key:                  map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: "row1"}},
		ProjectionExpression: aws.String("keep"),
	})
	require.NoError(t, err)
	require.Len(t, got.Item, 1, "ProjectionExpression must restrict to the named attribute")
	require.Contains(t, got.Item, "keep")
	assert.NotContains(t, got.Item, "drop")

	scan, err := c.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(table), ProjectionExpression: aws.String("keep")})
	require.NoError(t, err)
	require.Len(t, scan.Items, 1)
	assert.NotContains(t, scan.Items[0], "drop")
}

// TestCloudWatch_GetLogEventsStartFromHead covers startFromHead: the default
// (false) returns the latest events first, true returns the earliest.
func TestCloudWatch_GetLogEventsStartFromHead(t *testing.T) {
	cw := cwLogsClient()
	group, stream := "/test/startfromhead", "s1"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream)})
	require.NoError(t, err)

	base := time.Now().UnixMilli()
	var events []cwlogtypes.InputLogEvent
	for i := 0; i < 5; i++ {
		events = append(events, cwlogtypes.InputLogEvent{Timestamp: aws.Int64(base + int64(i)), Message: aws.String(string(rune('a' + i)))})
	}
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), LogEvents: events})
	require.NoError(t, err)

	tail, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream), Limit: aws.Int32(2),
	})
	require.NoError(t, err)
	require.Len(t, tail.Events, 2)
	assert.Equal(t, "d", aws.ToString(tail.Events[0].Message), "default (startFromHead=false) returns the latest events")
	assert.Equal(t, "e", aws.ToString(tail.Events[1].Message))

	head, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream), Limit: aws.Int32(2), StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, head.Events, 2)
	assert.Equal(t, "a", aws.ToString(head.Events[0].Message), "startFromHead=true returns the earliest events")
	assert.Equal(t, "b", aws.ToString(head.Events[1].Message))
}

// TestEC2_NetworkInterfaceFilters covers DescribeNetworkInterfaces filters
// (previously every filter was ignored).
func TestEC2_NetworkInterfaceFilters(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.94.0.0/16")})
	require.NoError(t, err)
	subnet, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.94.1.0/24")})
	require.NoError(t, err)

	eni, err := c.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{
		SubnetId: subnet.Subnet.SubnetId, Description: aws.String("fidelity-eni-unique"),
	})
	require.NoError(t, err)
	eniID := aws.ToString(eni.NetworkInterface.NetworkInterfaceId)
	defer c.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{NetworkInterfaceId: aws.String(eniID)})

	match, err := c.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		Filters: []ec2types.Filter{{Name: aws.String("description"), Values: []string{"fidelity-eni-unique"}}},
	})
	require.NoError(t, err)
	require.Len(t, match.NetworkInterfaces, 1, "description filter must scope to the one ENI")
	assert.Equal(t, eniID, aws.ToString(match.NetworkInterfaces[0].NetworkInterfaceId))

	none, err := c.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		Filters: []ec2types.Filter{{Name: aws.String("description"), Values: []string{"no-such-description"}}},
	})
	require.NoError(t, err)
	assert.Empty(t, none.NetworkInterfaces, "a non-matching filter must return nothing")
}

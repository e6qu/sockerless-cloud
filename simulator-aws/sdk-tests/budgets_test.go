package aws_sdk_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	btypes "github.com/aws/aws-sdk-go-v2/service/budgets/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const budgetsAccountID = "123456789012"

func budgetsClient() *budgets.Client {
	return budgets.NewFromConfig(sdkConfig(), func(o *budgets.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestBudgetsCRUDSDK(t *testing.T) {
	c := budgetsClient()
	const name = "sdk-budget-crud"

	// Implicit AccountId path is what terraform-provider-aws uses with
	// skip_requesting_account_id = true, but the aws-sdk-go-v2 client itself
	// enforces AccountId as a required field, so we test the server-side
	// fallback via a manual HTTP request and the SDK path with an explicit
	// account ID.
	_, err := c.CreateBudget(ctx, &budgets.CreateBudgetInput{
		AccountId: aws.String(budgetsAccountID),
		Budget: &btypes.Budget{
			BudgetName: aws.String(name),
			BudgetLimit: &btypes.Spend{
				Amount: aws.String("100"),
				Unit:   aws.String("USD"),
			},
			TimeUnit:   btypes.TimeUnitMonthly,
			BudgetType: btypes.BudgetTypeCost,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteBudget(ctx, &budgets.DeleteBudgetInput{
			AccountId:  aws.String(budgetsAccountID),
			BudgetName: aws.String(name),
		})
	})

	desc, err := c.DescribeBudget(ctx, &budgets.DescribeBudgetInput{
		AccountId:  aws.String(budgetsAccountID),
		BudgetName: aws.String(name),
	})
	require.NoError(t, err)
	require.NotNil(t, desc.Budget)
	assert.Equal(t, name, aws.ToString(desc.Budget.BudgetName))
	assert.Equal(t, btypes.TimeUnitMonthly, desc.Budget.TimeUnit)
	assert.Equal(t, btypes.BudgetTypeCost, desc.Budget.BudgetType)
	require.NotNil(t, desc.Budget.BudgetLimit)
	assert.Equal(t, "100", aws.ToString(desc.Budget.BudgetLimit.Amount))
	assert.Equal(t, "USD", aws.ToString(desc.Budget.BudgetLimit.Unit))

	_, err = c.TagResource(ctx, &budgets.TagResourceInput{
		ResourceARN: aws.String("arn:aws:budgets::" + budgetsAccountID + ":budget/" + name),
		ResourceTags: []btypes.ResourceTag{
			{Key: aws.String("env"), Value: aws.String("test")},
			{Key: aws.String("owner"), Value: aws.String("tf")},
		},
	})
	require.NoError(t, err)

	tags, err := c.ListTagsForResource(ctx, &budgets.ListTagsForResourceInput{
		ResourceARN: aws.String("arn:aws:budgets::" + budgetsAccountID + ":budget/" + name),
	})
	require.NoError(t, err)
	require.Len(t, tags.ResourceTags, 2)
	found := map[string]string{}
	for _, rt := range tags.ResourceTags {
		found[aws.ToString(rt.Key)] = aws.ToString(rt.Value)
	}
	assert.Equal(t, "test", found["env"])
	assert.Equal(t, "tf", found["owner"])

	_, err = c.UntagResource(ctx, &budgets.UntagResourceInput{
		ResourceARN:     aws.String("arn:aws:budgets::" + budgetsAccountID + ":budget/" + name),
		ResourceTagKeys: []string{"owner"},
	})
	require.NoError(t, err)

	tags2, err := c.ListTagsForResource(ctx, &budgets.ListTagsForResourceInput{
		ResourceARN: aws.String("arn:aws:budgets::" + budgetsAccountID + ":budget/" + name),
	})
	require.NoError(t, err)
	require.Len(t, tags2.ResourceTags, 1)
	assert.Equal(t, "env", aws.ToString(tags2.ResourceTags[0].Key))

	_, err = c.UpdateBudget(ctx, &budgets.UpdateBudgetInput{
		AccountId: aws.String(budgetsAccountID),
		NewBudget: &btypes.Budget{
			BudgetName: aws.String(name),
			BudgetLimit: &btypes.Spend{
				Amount: aws.String("250"),
				Unit:   aws.String("USD"),
			},
			TimeUnit:   btypes.TimeUnitMonthly,
			BudgetType: btypes.BudgetTypeCost,
		},
	})
	require.NoError(t, err)

	desc2, err := c.DescribeBudget(ctx, &budgets.DescribeBudgetInput{
		AccountId:  aws.String(budgetsAccountID),
		BudgetName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, "250", aws.ToString(desc2.Budget.BudgetLimit.Amount))

	listed, err := c.DescribeBudgets(ctx, &budgets.DescribeBudgetsInput{
		AccountId: aws.String(budgetsAccountID),
	})
	require.NoError(t, err)
	var seen bool
	for _, b := range listed.Budgets {
		if aws.ToString(b.BudgetName) == name {
			seen = true
		}
	}
	assert.True(t, seen, "DescribeBudgets must include created budget")

	_, err = c.DeleteBudget(ctx, &budgets.DeleteBudgetInput{
		AccountId:  aws.String(budgetsAccountID),
		BudgetName: aws.String(name),
	})
	require.NoError(t, err)

	_, err = c.DescribeBudget(ctx, &budgets.DescribeBudgetInput{
		AccountId:  aws.String(budgetsAccountID),
		BudgetName: aws.String(name),
	})
	assert.Error(t, err)
}

func TestBudgetsCreateBudgetDerivesAccountId(t *testing.T) {
	// terraform-provider-aws with skip_requesting_account_id = true sends
	// CreateBudget without AccountId. The SDK validates it locally, so we
	// exercise the server-side fallback through a raw HTTP POST with the
	// awsJson1.1 envelope.
	reqBody, err := json.Marshal(map[string]any{
		"Budget": map[string]any{
			"BudgetName": "sdk-budget-implicit-account",
			"BudgetLimit": map[string]any{
				"Amount": "50",
				"Unit":   "USD",
			},
			"TimeUnit":   "MONTHLY",
			"BudgetType": "COST",
		},
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSBudgetServiceGateway.CreateBudget")
	signRawSigV4JSON(t, req, "budgets", reqBody)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "CreateBudget without AccountId must succeed")

	c := budgetsClient()
	t.Cleanup(func() {
		_, _ = c.DeleteBudget(ctx, &budgets.DeleteBudgetInput{
			AccountId:  aws.String(budgetsAccountID),
			BudgetName: aws.String("sdk-budget-implicit-account"),
		})
	})

	desc, err := c.DescribeBudget(ctx, &budgets.DescribeBudgetInput{
		AccountId:  aws.String(budgetsAccountID),
		BudgetName: aws.String("sdk-budget-implicit-account"),
	})
	require.NoError(t, err)
	assert.Equal(t, "sdk-budget-implicit-account", aws.ToString(desc.Budget.BudgetName))
}

func TestBudgetsDescribeAndDeleteDeriveAccountId(t *testing.T) {
	// The SDK marks AccountId as required for DescribeBudget/DeleteBudget too,
	// but the simulator should derive it from the configured account when it is
	// omitted. Exercise those paths through raw HTTP calls matching the provider.
	const name = "sdk-budget-implicit-describe-delete"
	createBody, err := json.Marshal(map[string]any{
		"AccountId": budgetsAccountID,
		"Budget": map[string]any{
			"BudgetName":  name,
			"BudgetLimit": map[string]any{"Amount": "75", "Unit": "USD"},
			"TimeUnit":    "MONTHLY",
			"BudgetType":  "COST",
		},
	})
	require.NoError(t, err)

	makeReq := func(target string, body []byte) *http.Request {
		req, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-amz-json-1.1")
		req.Header.Set("X-Amz-Target", "AWSBudgetServiceGateway."+target)
		signRawSigV4JSON(t, req, "budgets", body)
		return req
	}

	resp, err := http.DefaultClient.Do(makeReq("CreateBudget", createBody))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	describeBody, err := json.Marshal(map[string]any{"BudgetName": name})
	require.NoError(t, err)
	resp, err = http.DefaultClient.Do(makeReq("DescribeBudget", describeBody))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	deleteBody, err := json.Marshal(map[string]any{"BudgetName": name})
	require.NoError(t, err)
	resp, err = http.DefaultClient.Do(makeReq("DeleteBudget", deleteBody))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestBudgetsNotificationsAndSubscribersSDK(t *testing.T) {
	c := budgetsClient()
	const name = "sdk-budget-notif"

	_, err := c.CreateBudget(ctx, &budgets.CreateBudgetInput{
		AccountId: aws.String(budgetsAccountID),
		Budget: &btypes.Budget{
			BudgetName: aws.String(name),
			BudgetLimit: &btypes.Spend{
				Amount: aws.String("500"),
				Unit:   aws.String("USD"),
			},
			TimeUnit:   btypes.TimeUnitMonthly,
			BudgetType: btypes.BudgetTypeCost,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteBudget(ctx, &budgets.DeleteBudgetInput{
			AccountId:  aws.String(budgetsAccountID),
			BudgetName: aws.String(name),
		})
	})

	notif := &btypes.Notification{
		NotificationType:   btypes.NotificationTypeActual,
		ComparisonOperator: btypes.ComparisonOperatorGreaterThan,
		Threshold:          80,
		ThresholdType:      btypes.ThresholdTypePercentage,
	}

	_, err = c.CreateNotification(ctx, &budgets.CreateNotificationInput{
		AccountId:    aws.String(budgetsAccountID),
		BudgetName:   aws.String(name),
		Notification: notif,
		Subscribers: []btypes.Subscriber{
			{
				SubscriptionType: btypes.SubscriptionTypeEmail,
				Address:          aws.String("alerts@example.com"),
			},
		},
	})
	require.NoError(t, err)

	listed, err := c.DescribeNotificationsForBudget(ctx, &budgets.DescribeNotificationsForBudgetInput{
		AccountId:  aws.String(budgetsAccountID),
		BudgetName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, listed.Notifications, 1)
	assert.Equal(t, btypes.NotificationTypeActual, listed.Notifications[0].NotificationType)
	assert.Equal(t, 80.0, listed.Notifications[0].Threshold)

	subs, err := c.DescribeSubscribersForNotification(ctx, &budgets.DescribeSubscribersForNotificationInput{
		AccountId:    aws.String(budgetsAccountID),
		BudgetName:   aws.String(name),
		Notification: notif,
	})
	require.NoError(t, err)
	require.Len(t, subs.Subscribers, 1)
	assert.Equal(t, "alerts@example.com", aws.ToString(subs.Subscribers[0].Address))

	_, err = c.CreateSubscriber(ctx, &budgets.CreateSubscriberInput{
		AccountId:    aws.String(budgetsAccountID),
		BudgetName:   aws.String(name),
		Notification: notif,
		Subscriber: &btypes.Subscriber{
			SubscriptionType: btypes.SubscriptionTypeSns,
			Address:          aws.String("arn:aws:sns:us-east-1:123456789012:budget-alerts"),
		},
	})
	require.NoError(t, err)

	subs2, err := c.DescribeSubscribersForNotification(ctx, &budgets.DescribeSubscribersForNotificationInput{
		AccountId:    aws.String(budgetsAccountID),
		BudgetName:   aws.String(name),
		Notification: notif,
	})
	require.NoError(t, err)
	assert.Len(t, subs2.Subscribers, 2)

	_, err = c.DeleteSubscriber(ctx, &budgets.DeleteSubscriberInput{
		AccountId:    aws.String(budgetsAccountID),
		BudgetName:   aws.String(name),
		Notification: notif,
		Subscriber: &btypes.Subscriber{
			SubscriptionType: btypes.SubscriptionTypeEmail,
			Address:          aws.String("alerts@example.com"),
		},
	})
	require.NoError(t, err)

	subs3, err := c.DescribeSubscribersForNotification(ctx, &budgets.DescribeSubscribersForNotificationInput{
		AccountId:    aws.String(budgetsAccountID),
		BudgetName:   aws.String(name),
		Notification: notif,
	})
	require.NoError(t, err)
	assert.Len(t, subs3.Subscribers, 1)

	_, err = c.DeleteNotification(ctx, &budgets.DeleteNotificationInput{
		AccountId:    aws.String(budgetsAccountID),
		BudgetName:   aws.String(name),
		Notification: notif,
	})
	require.NoError(t, err)

	listed2, err := c.DescribeNotificationsForBudget(ctx, &budgets.DescribeNotificationsForBudgetInput{
		AccountId:  aws.String(budgetsAccountID),
		BudgetName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Empty(t, listed2.Notifications)
}

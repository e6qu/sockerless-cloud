package aws_sdk_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/amplify"
	amplifytypes "github.com/aws/aws-sdk-go-v2/service/amplify/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roleSessionConfig assumes a role carrying policyDocument and returns a config
// signed with the session's credentials, which the call-time gate evaluates
// against the role's policies.
func roleSessionConfig(t *testing.T, name, policyDocument string) aws.Config {
	t.Helper()
	admin := iamClient()
	name = uniquename.New(name)
	created, err := admin.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(name),
		// The account principal delegates to the caller's own policies, which
		// for the harness administrator allow sts:AssumeRole.
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	_, err = admin.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(name), PolicyName: aws.String("granted"), PolicyDocument: aws.String(policyDocument),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("granted")})
		_, _ = admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	})
	session, err := stsClient().AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: created.Role.Arn, RoleSessionName: aws.String("rest-gate")})
	require.NoError(t, err)
	return aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(session.Credentials.AccessKeyId), aws.ToString(session.Credentials.SecretAccessKey), aws.ToString(session.Credentials.SessionToken))}
}

// A role session reaches a REST-JSON service's operation only with the action
// on the resource the request names: Amazon EventBridge Scheduler refuses with
// AccessDeniedException, and AWS Amplify with the UnauthorizedException its
// model declares for a lack of access.
func TestIAM_RESTJSONRoutesAreGated_SDK(t *testing.T) {
	t.Parallel()
	sched := schedulerClient()
	name := uniquename.New("rest-gate")
	_, err := sched.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(name),
		ScheduleExpression: aws.String("rate(1 hour)"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn:     aws.String("arn:aws:sqs:us-east-1:123456789012:rest-gate"),
			RoleArn: aws.String(serviceRole(t, "scheduler.amazonaws.com")),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sched.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(name)}) })
	app, err := amplifyClient().CreateApp(ctx, &amplify.CreateAppInput{Name: aws.String(uniquename.New("rest-gate"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = amplifyClient().DeleteApp(ctx, &amplify.DeleteAppInput{AppId: app.App.AppId}) })

	denied := roleSessionConfig(t, "rest-gate-denied",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["scheduler:ListSchedules","amplify:ListApps"],"Resource":"*"}]}`)
	deniedScheduler := scheduler.NewFromConfig(denied, func(o *scheduler.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = deniedScheduler.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(name)})
	assert.Equal(t, "AccessDeniedException", errCode(t, err))
	_, err = deniedScheduler.ListSchedules(ctx, &scheduler.ListSchedulesInput{})
	assert.NoError(t, err, "the role is allowed scheduler:ListSchedules")
	deniedAmplify := amplify.NewFromConfig(denied, func(o *amplify.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = deniedAmplify.GetApp(ctx, &amplify.GetAppInput{AppId: app.App.AppId})
	var unauthorized *amplifytypes.UnauthorizedException
	require.True(t, errors.As(err, &unauthorized), "GetApp without amplify:GetApp: %v", err)
	_, err = deniedAmplify.ListApps(ctx, &amplify.ListAppsInput{})
	assert.NoError(t, err, "the role is allowed amplify:ListApps")

	allowed := roleSessionConfig(t, "rest-gate-allowed",
		`{"Version":"2012-10-17","Statement":[`+
			`{"Effect":"Allow","Action":"scheduler:GetSchedule","Resource":"arn:aws:scheduler:us-east-1:*:schedule/default/`+name+`"},`+
			`{"Effect":"Allow","Action":"amplify:GetApp","Resource":"`+aws.ToString(app.App.AppArn)+`"}]}`)
	got, err := scheduler.NewFromConfig(allowed, func(o *scheduler.Options) { o.BaseEndpoint = aws.String(baseURL) }).
		GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(got.Name))
	gotApp, err := amplify.NewFromConfig(allowed, func(o *amplify.Options) { o.BaseEndpoint = aws.String(baseURL) }).
		GetApp(ctx, &amplify.GetAppInput{AppId: app.App.AppId})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(app.App.AppId), aws.ToString(gotApp.App.AppId))
}

// A role session reaches a REST-XML service's operation only with the action
// on the resource the request names; Amazon Route 53 refuses with AccessDenied.
func TestIAM_RESTXMLRoutesAreGated_SDK(t *testing.T) {
	t.Parallel()
	r53 := route53Client()
	zone, err := r53.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name: aws.String(uniquename.New("rest-gate") + ".example."), CallerReference: aws.String(uniquename.New("rest-gate")),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = r53.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: zone.HostedZone.Id}) })
	// The zone's Id is "/hostedzone/Z…"; its ARN and the route's label carry
	// the bare identifier.
	zoneID := strings.TrimPrefix(aws.ToString(zone.HostedZone.Id), "/hostedzone/")

	denied := route53.NewFromConfig(roleSessionConfig(t, "rest-gate-r53-denied",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"route53:ListHostedZones","Resource":"*"}]}`),
		func(o *route53.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = denied.GetHostedZone(ctx, &route53.GetHostedZoneInput{Id: zone.HostedZone.Id})
	assert.Equal(t, "AccessDenied", errCode(t, err))
	_, err = denied.ListHostedZones(ctx, &route53.ListHostedZonesInput{})
	assert.NoError(t, err, "the role is allowed route53:ListHostedZones")

	allowed := route53.NewFromConfig(roleSessionConfig(t, "rest-gate-r53-allowed",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"route53:GetHostedZone","Resource":"arn:aws:route53:::hostedzone/`+zoneID+`"}]}`),
		func(o *route53.Options) { o.BaseEndpoint = aws.String(baseURL) })
	got, err := allowed.GetHostedZone(ctx, &route53.GetHostedZoneInput{Id: zone.HostedZone.Id})
	require.NoError(t, err)
	assert.Equal(t, zoneID, strings.TrimPrefix(aws.ToString(got.HostedZone.Id), "/hostedzone/"))
}

// A universal target to a REST-JSON action calls it as the execution role:
// AWS Amplify's createApp succeeds only when the role is allowed amplify:CreateApp,
// and a refusal goes to the dead-letter queue under Amplify's own code.
func TestScheduler_UniversalRESTTargetRunsAsTheExecutionRole_SDK(t *testing.T) {
	t.Parallel()
	dlqURL, dlqARN := createSchedulerTestQueue(t, "scheduler-sdk-rest-dlq")
	deniedName, allowedName := uniquename.New("rest-target-denied"), uniquename.New("rest-target-allowed")
	t.Cleanup(func() {
		apps, err := amplifyClient().ListApps(ctx, &amplify.ListAppsInput{})
		if err != nil {
			return
		}
		for _, app := range apps.Apps {
			if name := aws.ToString(app.Name); name == deniedName || name == allowedName {
				_, _ = amplifyClient().DeleteApp(ctx, &amplify.DeleteAppInput{AppId: app.AppId})
			}
		}
	})
	deniedRole := uniquename.New("scheduler-rest-denied")
	denied := createServiceRole(t, deniedRole, "scheduler.amazonaws.com", "amplify:ListApps")
	_, err := iamClient().PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(deniedRole), PolicyName: aws.String("dead-letter"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"` + dlqARN + `"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamClient().DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(deniedRole), PolicyName: aws.String("dead-letter")})
	})
	deniedInput, err := json.Marshal(map[string]string{"name": deniedName})
	require.NoError(t, err)
	createOneTimeSchedule(t, "sdk-rest-denied", &schedtypes.Target{
		Arn: aws.String("arn:aws:scheduler:::aws-sdk:amplify:createApp"), RoleArn: aws.String(denied), Input: aws.String(string(deniedInput)),
		DeadLetterConfig: &schedtypes.DeadLetterConfig{Arn: aws.String(dlqARN)},
		RetryPolicy:      &schedtypes.RetryPolicy{MaximumRetryAttempts: aws.Int32(0)},
	})
	message := receiveWithAttributes(t, sqsClient(), dlqURL, 30*time.Second)
	assert.Equal(t, string(deniedInput), aws.ToString(message.Body))
	assert.Equal(t, "UnauthorizedException", messageAttribute(message, "ERROR_CODE"))

	allowedInput, err := json.Marshal(map[string]string{"name": allowedName})
	require.NoError(t, err)
	createOneTimeSchedule(t, "sdk-rest-allowed", &schedtypes.Target{
		Arn:     aws.String("arn:aws:scheduler:::aws-sdk:amplify:createApp"),
		RoleArn: aws.String(createServiceRole(t, uniquename.New("scheduler-rest-allowed"), "scheduler.amazonaws.com", "amplify:CreateApp")),
		Input:   aws.String(string(allowedInput)),
	})
	require.Eventually(t, func() bool {
		apps, err := amplifyClient().ListApps(ctx, &amplify.ListAppsInput{})
		require.NoError(t, err)
		for _, app := range apps.Apps {
			if aws.ToString(app.Name) == allowedName {
				return true
			}
		}
		return false
	}, 30*time.Second, waiterMinDelay, "the allowed role's schedule creates the app")
	apps, err := amplifyClient().ListApps(ctx, &amplify.ListAppsInput{})
	require.NoError(t, err)
	for _, app := range apps.Apps {
		assert.NotEqual(t, deniedName, aws.ToString(app.Name), "the denied role's schedule created an app")
	}
}

package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

func TestCWSelectionCriteriaPerPolicyType(t *testing.T) {
	for _, c := range []struct {
		policyType, criteria string
		ok                   bool
	}{
		{cwPolicySubscription, `LogGroupName NOT IN ["LogGroupToExclude1", "LogGroupToExclude2"]`, true},
		{cwPolicySubscription, `LogGroupName IN ["a"]`, false},
		{cwPolicySubscription, `LogGroupNamePrefix NOT IN ["a"]`, false},
		{cwPolicyTransformer, `LogGroupNamePrefix= "test-"`, true},
		{cwPolicyRetention, `LogGroupNamePrefix="/aws/lambda"`, true},
		{cwPolicyTransformer, `LogGroupName NOT IN ["a"]`, false},
		{cwPolicyFieldIndex, `DataSourceName = "amazon_vpc" AND DataSourceType = "flow"`, true},
		{cwPolicyMetricExtraction, `LogGroupNamePrefix NOT IN ["/aws/containerinsights", "/aws/ecs/containerinsights"]`, true},
		{cwPolicyDataProtection, `LogGroupName NOT IN ["a"]`, false},
		{cwPolicySubscription, `LogGroupName NOT IN ["unterminated]`, false},
	} {
		_, err := cwParsePolicySelection(c.policyType, c.criteria)
		if (err == nil) != c.ok {
			t.Errorf("%s %q: err = %v, want ok=%v", c.policyType, c.criteria, err, c.ok)
		}
	}
	if _, err := cwParsePolicySelection(cwPolicySubscription, `LogGroupName NOT IN ["`+strings.Repeat("a", cwSelectionCriteriaMaxBytes)+`"]`); err == nil {
		t.Error("selection criteria past 25,600 bytes are accepted")
	}
}

func TestCWFieldSelectionCriteria(t *testing.T) {
	values := map[string]string{"@aws.account": "123456789012", "@aws.region": "us-east-1", "@source.log": "/g"}
	for criteria, want := range map[string]bool{
		`@aws.region NOT IN ["cn-north-1"]`:                                             true,
		`@aws.account = "123456789012" AND @aws.region = "us-east-1"`:                   true,
		`@aws.account = "987654321098" OR @aws.region != "eu-west-1"`:                   true,
		`(@aws.account IN ["123456789012", "987654321098"]) AND @source.log = "/other"`: false,
	} {
		sel, err := cwParseFieldSelection(criteria)
		if err != nil {
			t.Fatalf("%q: %v", criteria, err)
		}
		if got := sel.match(values); got != want {
			t.Errorf("%q matched %v, want %v", criteria, got, want)
		}
	}
	if _, err := cwParseFieldSelection(`@aws.partition = "aws"`); err == nil {
		t.Error("an unknown system field is accepted")
	}
}

// The account-level policy skips the groups its selection excludes and the
// log group of its destination function.
func TestCWAccountSubscriptionSelectsGroups(t *testing.T) {
	useLogMemoryStores(t)
	functions := lambdaFunctions
	lambdaFunctions = sim.MakeStore[LambdaFunction](nil, "lambda_functions")
	t.Cleanup(func() { lambdaFunctions = functions })
	lambdaFunctions.Put("helloWorld", LambdaFunction{FunctionName: "helloWorld"})
	cwAccountPolicies.Put(cwAccountPolicyKey(cwPolicySubscription, "ExamplePolicyLambda"), CWAccountPolicy{
		PolicyName: "ExamplePolicyLambda", PolicyType: cwPolicySubscription,
		PolicyDocument:    `{"DestinationArn":"` + lambdaArn("helloWorld") + `","FilterPattern":"Test","Distribution":"Random"}`,
		SelectionCriteria: `LogGroupName NOT IN ["LogGroupToExclude1", "LogGroupToExclude2"]`,
	})
	now := time.Now().UnixMilli()
	for group, want := range map[string]bool{
		"Example1":               true,
		"LogGroupToExclude1":     false,
		"/aws/lambda/helloWorld": false,
	} {
		spec, ok := cwAccountSubscription(group, now)
		if ok != want {
			t.Errorf("%s selected = %v, want %v", group, ok, want)
		}
		if ok && (spec.policyLevel != cwPolicyLevelAccount || spec.name != "ExamplePolicyLambda") {
			t.Errorf("%s spec = %+v", group, spec)
		}
	}
	cwAccountPolicies.Update(cwAccountPolicyKey(cwPolicySubscription, "ExamplePolicyLambda"), func(p *CWAccountPolicy) {
		p.DisabledUntil = now + time.Minute.Milliseconds()
	})
	if _, ok := cwAccountSubscription("Example1", now); ok {
		t.Error("a policy disabled after a refused delivery still forwards")
	}
}

// A retention policy takes effect 24 hours after it is put; until then the
// version it replaced governs. The longest of the policies that apply wins,
// NEVER_EXPIRE the longest, and NEW_ONLY spares groups older than the policy.
func TestCWRetentionPoliciesTakeEffectAfterADay(t *testing.T) {
	useLogMemoryStores(t)
	created := time.Now()
	put := func(name, document, selection string, at time.Time, previous *CWAccountPolicy) {
		p := CWAccountPolicy{PolicyName: name, PolicyType: cwPolicyRetention, PolicyDocument: document,
			SelectionCriteria: selection, CreatedTime: created.UnixMilli(), EffectiveFrom: at.Add(cwRetentionPolicyDelay).UnixMilli()}
		if previous != nil {
			p.PreviousInEffect, p.PreviousDocument, p.PreviousSelection = previous.documentAt(at)
		}
		cwAccountPolicies.Put(cwAccountPolicyKey(cwPolicyRetention, name), p)
	}
	old := CWLogGroup{LogGroupName: "/aws/lambda/old", RetentionInDays: 14, CreationTime: created.Add(-time.Hour).UnixMilli()}
	young := CWLogGroup{LogGroupName: "/aws/lambda/young", CreationTime: created.Add(time.Hour).UnixMilli()}
	put("baseline", `{"retentionInDays": 90, "enforcementMode": "RETROACTIVE"}`, `LogGroupNamePrefix="/aws"`, created, nil)
	put("keep", `{"retentionInDays": "NEVER_EXPIRE", "enforcementMode": "NEW_ONLY"}`, `LogGroupNamePrefix="/aws/lambda"`, created, nil)

	retention := func(g CWLogGroup, at time.Time) (cwRetention, string) {
		return cwEffectiveRetention(g, cwRetentionVersionsAt(at))
	}
	if days, policy := retention(old, created.Add(time.Hour)); days != 14 || policy != "" {
		t.Fatalf("before the policies take effect: %v %q", days, policy)
	}
	day := created.Add(cwRetentionPolicyDelay + time.Minute)
	if days, policy := retention(old, day); days != 90 || policy != "baseline" {
		t.Errorf("an older group: %v %q, want 90 baseline", days, policy)
	}
	if days, policy := retention(young, day); days != 0 || policy != "keep" {
		t.Errorf("a newer group: %v %q, want NEVER_EXPIRE keep", days, policy)
	}

	previous, _ := cwAccountPolicies.Get(cwAccountPolicyKey(cwPolicyRetention, "baseline"))
	put("baseline", `{"retentionInDays": 30, "enforcementMode": "RETROACTIVE"}`, `LogGroupNamePrefix="/aws"`, day, &previous)
	if days, _ := retention(old, day.Add(time.Hour)); days != 90 {
		t.Errorf("an update keeps the previous period for a day: %v", days)
	}
	if days, _ := retention(old, day.Add(cwRetentionPolicyDelay+time.Minute)); days != 30 {
		t.Errorf("an update takes effect after a day: %v", days)
	}
	delivery := CWLogGroup{LogGroupName: "/aws/lambda/delivery", LogGroupClass: cwClassDelivery}
	if days, _ := retention(delivery, day); days != cwDeliveryRetentionDays {
		t.Errorf("a Delivery group keeps its fixed retention: %v", days)
	}
}

// A batch halves until each gzipped part fits the record limit.
func TestCWSplitSubscriptionMessage(t *testing.T) {
	message := cwSubscriptionMessage{MessageType: "DATA_MESSAGE"}
	for i := range 64 {
		message.LogEvents = append(message.LogEvents, cwSubscriptionLogEvent{ID: string(rune('a' + i%26)), Message: cwTestNoise(i, 4096)})
	}
	limit := 40 * 1024
	parts := cwSplitSubscriptionMessage(message, limit)
	if len(parts) < 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	total := 0
	for _, p := range parts {
		if n := len(cwGzipSubscriptionMessage(p)); n > limit {
			t.Errorf("a part of %d bytes exceeds %d", n, limit)
		}
		total += len(p.LogEvents)
	}
	if total != len(message.LogEvents) {
		t.Errorf("parts carry %d events, want %d", total, len(message.LogEvents))
	}
}

func cwTestNoise(seed, n int) string {
	b := make([]byte, n)
	x := uint32(seed*2654435761 + 1)
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte('!' + x%90)
	}
	return strings.NewReplacer(`"`, "#", `\`, "#").Replace(string(b))
}

func TestCWSubscriptionCallResultClassifiesThrottling(t *testing.T) {
	for _, c := range []struct {
		status          int
		body            string
		retry, throttle bool
	}{
		{400, `{"__type":"ProvisionedThroughputExceededException","message":"Rate exceeded"}`, true, true},
		{503, `{"__type":"ServiceUnavailableException","message":"Slow down."}`, true, true},
		{429, `{"Type":"User","message":"Rate Exceeded."}`, true, true},
		{500, `{"__type":"InternalFailure"}`, true, false},
		{400, `{"__type":"AccessDeniedException","message":"denied"}`, false, false},
		{404, `{"__type":"ResourceNotFoundException","message":"gone"}`, false, false},
		{http.StatusOK, `{}`, false, false},
	} {
		outcome, throttled := cwSubscriptionCallResult(c.status, []byte(c.body), nil)
		if outcome.Retry != c.retry || throttled != c.throttle {
			t.Errorf("%d %s: retry=%v throttled=%v", c.status, c.body, outcome.Retry, throttled)
		}
	}
}

func TestCWMetricFilterNameDimensionIsASCII(t *testing.T) {
	if got := cwMetricFilterNameDimension("filtré-名"); got != "filtr?-?" {
		t.Errorf("got %q", got)
	}
}

// The standard retry mode's full-jitter delay never passes its bound.
func TestCWSubscriptionRetryDelayBounds(t *testing.T) {
	throttled := delivery.Retryable(ebTargetError{Code: "ProvisionedThroughputExceededException"})
	transient := delivery.Retryable(ebTargetError{Code: "InternalFailure"})
	for retry := range 30 {
		for _, c := range []struct {
			last delivery.Outcome
			base time.Duration
		}{{throttled, time.Second}, {transient, 50 * time.Millisecond}} {
			bound := cwSubscriptionMaxBackoff
			if retry < 20 {
				bound = min(bound, c.base<<retry)
			}
			if d := cwSubscriptionRetryDelay(retry, c.last); d < 0 || d > bound {
				t.Fatalf("retry %d after %v waited %v, bound %v", retry, c.last.Err, d, bound)
			}
		}
	}
}

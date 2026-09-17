package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/fxamacker/cbor/v2"
)

func init() {
	registerIAMRequestConditionPopulator("cloudwatch", iamPopulateCloudWatchRequestConditionKeys)
}

type cloudWatchConditionRequest struct {
	AlarmActions   []string                      `json:"AlarmActions" cbor:"AlarmActions"`
	RuleDefinition string                        `json:"RuleDefinition" cbor:"RuleDefinition"`
	ResourceARN    string                        `json:"ResourceARN" cbor:"ResourceARN"`
	ManagedRules   []cloudWatchManagedRuleTarget `json:"ManagedRules" cbor:"ManagedRules"`
}

type cloudWatchManagedRuleTarget struct {
	ResourceARN string `json:"ResourceARN" cbor:"ResourceARN"`
}

// iamPopulateCloudWatchRequestConditionKeys adds the Amazon CloudWatch keys
// for the actions an alarm triggers and the resources an Insight Rule reads.
func iamPopulateCloudWatchRequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	switch operation {
	case "PutMetricAlarm", "PutCompositeAlarm", "PutLogAlarm",
		"PutInsightRule", "PutManagedInsightRules", "ListManagedInsightRules":
	default:
		return
	}
	request, ok := cloudWatchDecodeConditionRequest(r, body)
	if !ok {
		return
	}
	switch operation {
	case "PutMetricAlarm", "PutCompositeAlarm", "PutLogAlarm":
		iamSetConditionValues(ctx, "cloudwatch:AlarmActions", request.AlarmActions...)
	case "PutInsightRule":
		iamSetConditionValues(ctx, "cloudwatch:requestInsightRuleLogGroups",
			insightRuleLogGroupNames(request.RuleDefinition)...)
	case "PutManagedInsightRules":
		arns := make([]string, 0, len(request.ManagedRules))
		for _, rule := range request.ManagedRules {
			arns = append(arns, rule.ResourceARN)
		}
		iamSetConditionValues(ctx, "cloudwatch:requestManagedResourceARNs", arns...)
	case "ListManagedInsightRules":
		iamSetConditionValues(ctx, "cloudwatch:requestManagedResourceARNs", request.ResourceARN)
	}
}

// cloudWatchDecodeConditionRequest reads the request members whichever of its
// three protocols carried them: Query form parameters, an awsJson1_0 body, or
// an RPC v2 CBOR body the SDK may gzip.
func cloudWatchDecodeConditionRequest(r *http.Request, body []byte) (cloudWatchConditionRequest, bool) {
	var request cloudWatchConditionRequest
	if r.FormValue("Action") != "" {
		request.AlarmActions = iamQueryList(r, "AlarmActions")
		request.RuleDefinition = r.FormValue("RuleDefinition")
		request.ResourceARN = r.FormValue("ResourceARN")
		for i := 1; ; i++ {
			arn := r.FormValue(fmt.Sprintf("ManagedRules.member.%d.ResourceARN", i))
			if arn == "" {
				break
			}
			request.ManagedRules = append(request.ManagedRules, cloudWatchManagedRuleTarget{ResourceARN: arn})
		}
		return request, true
	}
	if len(body) == 0 {
		return request, false
	}
	if json.Unmarshal(body, &request) == nil {
		return request, true
	}
	raw, err := cwDecompress(body)
	if err != nil {
		return request, false
	}
	return request, cbor.Unmarshal(raw, &request) == nil
}

// insightRuleLogGroupNames reads the log groups a Contributor Insights rule
// definition names in its LogGroupNames member.
func insightRuleLogGroupNames(definition string) []string {
	if definition == "" {
		return nil
	}
	var rule struct {
		LogGroupNames []string `json:"LogGroupNames"`
	}
	if json.Unmarshal([]byte(definition), &rule) != nil {
		return nil
	}
	return rule.LogGroupNames
}

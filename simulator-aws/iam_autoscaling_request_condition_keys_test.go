package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// queryServiceConditionContext runs a query-protocol service's registered
// populators over a form-encoded request, as the gate does.
func queryServiceConditionContext(t *testing.T, service, operation string, form url.Values) map[string][]string {
	t.Helper()
	form.Set("Action", operation)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := map[string][]string{}
	iamRunRequestConditionPopulators(r, service, operation, nil, ctx)
	return ctx
}

func assertServiceConditionContext(t *testing.T, ctx, want map[string][]string) {
	t.Helper()
	for key, values := range want {
		if values == nil {
			if got, ok := ctx[key]; ok {
				t.Errorf("%s = %v, want it absent", key, got)
			}
			continue
		}
		if !reflect.DeepEqual(ctx[key], values) {
			t.Errorf("%s = %v, want %v", key, ctx[key], values)
		}
	}
}

func resetAutoScalingLaunchSources() {
	AwaitSimulatorBackground()
	asLaunchConfigurations = sim.MakeStore[ASLaunchConfiguration](nil, "autoscaling_launch_configurations")
	ec2LaunchTemplates = sim.MakeStore[EC2LaunchTemplate](nil, "ec2_launch_templates")
}

func TestAutoScalingConditionKeysReadTheGroupRequest(t *testing.T) {
	resetAutoScalingLaunchSources()
	ctx := queryServiceConditionContext(t, "autoscaling", "CreateAutoScalingGroup", url.Values{
		"AutoScalingGroupName":               {"web"},
		"LaunchConfigurationName":            {"web-lc"},
		"MinSize":                            {"1"},
		"MaxSize":                            {"4"},
		"VPCZoneIdentifier":                  {"subnet-a, subnet-b"},
		"LoadBalancerNames.member.1":         {"classic-a"},
		"TargetGroupARNs.member.1":           {"arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/tg/1"},
		"TrafficSources.member.1.Identifier": {"arn:aws:vpc-lattice:us-east-1:123456789012:targetgroup/tg-1"},
		"TrafficSources.member.1.Type":       {"vpc-lattice"},
		"CapacityReservationSpecification.CapacityReservationTarget.CapacityReservationIds.member.1":               {"cr-1"},
		"CapacityReservationSpecification.CapacityReservationTarget.CapacityReservationIds.member.2":               {"cr-2"},
		"CapacityReservationSpecification.CapacityReservationTarget.CapacityReservationResourceGroupArns.member.1": {"arn:aws:resource-groups:us-east-1:123456789012:group/crg"},
		"MixedInstancesPolicy.LaunchTemplate.Overrides.member.1.InstanceType":                                      {"m5.large"},
		"MixedInstancesPolicy.LaunchTemplate.Overrides.member.2.WeightedCapacity":                                  {"2"},
		"MixedInstancesPolicy.LaunchTemplate.Overrides.member.3.InstanceType":                                      {"c5.large"},
		"MixedInstancesPolicy.InstancesDistribution.DistributionSegments.member.1.TargetCapacityTypes.member.1":    {"on-demand"},
		"MixedInstancesPolicy.InstancesDistribution.DistributionSegments.member.2.TargetCapacityTypes.member.1":    {"spot"},
		"Operator.Principal": {"ec2.amazonaws.com"},
	})
	assertServiceConditionContext(t, ctx, map[string][]string{
		"autoscaling:LaunchConfigurationName":              {"web-lc"},
		"autoscaling:MinSize":                              {"1"},
		"autoscaling:MaxSize":                              {"4"},
		"autoscaling:VPCZoneIdentifiers":                   {"subnet-a", "subnet-b"},
		"autoscaling:LoadBalancerNames":                    {"classic-a"},
		"autoscaling:TargetGroupARNs":                      {"arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/tg/1"},
		"autoscaling:TrafficSourceIdentifiers":             {"arn:aws:vpc-lattice:us-east-1:123456789012:targetgroup/tg-1"},
		"autoscaling:CapacityReservationIds":               {"cr-1", "cr-2"},
		"autoscaling:CapacityReservationResourceGroupArns": {"arn:aws:resource-groups:us-east-1:123456789012:group/crg"},
		"autoscaling:InstanceTypes":                        {"m5.large", "c5.large"},
		"autoscaling:TargetCapacityTypes":                  {"on-demand", "spot"},
		"autoscaling:OperatorPrincipal":                    {"ec2.amazonaws.com"},
		"autoscaling:LaunchTemplateVersionSpecified":       nil,
		// The group names a launch configuration that does not exist.
		"autoscaling:ImageId": nil,
	})
}

func TestAutoScalingConditionKeysReadTheLaunchConfigurationRequest(t *testing.T) {
	ctx := queryServiceConditionContext(t, "autoscaling", "CreateLaunchConfiguration", url.Values{
		"LaunchConfigurationName":                 {"web-lc"},
		"ImageId":                                 {"ami-0abc"},
		"InstanceType":                            {"t3.micro"},
		"SpotPrice":                               {"0.05"},
		"MetadataOptions.HttpTokens":              {"required"},
		"MetadataOptions.HttpEndpoint":            {"enabled"},
		"MetadataOptions.HttpPutResponseHopLimit": {"2"},
	})
	assertServiceConditionContext(t, ctx, map[string][]string{
		"autoscaling:ImageId":                         {"ami-0abc"},
		"autoscaling:InstanceType":                    {"t3.micro"},
		"autoscaling:SpotPrice":                       {"0.05"},
		"autoscaling:MetadataHttpTokens":              {"required"},
		"autoscaling:MetadataHttpEndpoint":            {"enabled"},
		"autoscaling:MetadataHttpPutResponseHopLimit": {"2"},
	})
}

func TestAutoScalingConditionKeysReadTheScheduledActionSizes(t *testing.T) {
	ctx := queryServiceConditionContext(t, "autoscaling", "PutScheduledUpdateGroupAction", url.Values{
		"AutoScalingGroupName": {"web"},
		"ScheduledActionName":  {"night"},
		"MinSize":              {"0"},
		"MaxSize":              {"2"},
	})
	assertServiceConditionContext(t, ctx, map[string][]string{
		"autoscaling:MinSize": {"0"},
		"autoscaling:MaxSize": {"2"},
	})
}

func TestAutoScalingConditionKeysReadTheAttachedLoadBalancers(t *testing.T) {
	ctx := queryServiceConditionContext(t, "autoscaling", "AttachLoadBalancerTargetGroups", url.Values{
		"AutoScalingGroupName":     {"web"},
		"TargetGroupARNs.member.1": {"arn:tg-1"},
		"TargetGroupARNs.member.2": {"arn:tg-2"},
	})
	assertServiceConditionContext(t, ctx, map[string][]string{"autoscaling:TargetGroupARNs": {"arn:tg-1", "arn:tg-2"}})

	ctx = queryServiceConditionContext(t, "autoscaling", "DetachTrafficSources", url.Values{
		"AutoScalingGroupName":               {"web"},
		"TrafficSources.member.1.Identifier": {"arn:tg-1"},
	})
	assertServiceConditionContext(t, ctx, map[string][]string{"autoscaling:TrafficSourceIdentifiers": {"arn:tg-1"}})

	ctx = queryServiceConditionContext(t, "autoscaling", "DetachLoadBalancers", url.Values{
		"AutoScalingGroupName":       {"web"},
		"LoadBalancerNames.member.1": {"classic-a"},
	})
	assertServiceConditionContext(t, ctx, map[string][]string{"autoscaling:LoadBalancerNames": {"classic-a"}})
}

// The image comes from the launch configuration or launch template version the
// request names, so a policy can hold groups to approved AMIs.
func TestAutoScalingConditionKeysResolveTheImageOfTheNamedLaunchSource(t *testing.T) {
	resetAutoScalingLaunchSources()
	asLaunchConfigurations.Put("web-lc", ASLaunchConfiguration{Name: "web-lc", ImageId: "ami-lc"})
	ec2LaunchTemplates.Put("lt-1", EC2LaunchTemplate{
		LaunchTemplateId:     "lt-1",
		LaunchTemplateName:   "web-lt",
		DefaultVersionNumber: 1,
		LatestVersionNumber:  3,
		Versions: []EC2LaunchTemplateVersion{
			{VersionNumber: 1, Data: EC2LaunchTemplateData{ImageId: "ami-v1"}},
			{VersionNumber: 2, Data: EC2LaunchTemplateData{ImageId: "ami-v2"}},
			{VersionNumber: 3, Data: EC2LaunchTemplateData{ImageId: "ami-v3"}},
		},
	})

	cases := []struct {
		name      string
		operation string
		form      url.Values
		image     []string
		specified []string
	}{
		{"a launch configuration", "UpdateAutoScalingGroup",
			url.Values{"LaunchConfigurationName": {"web-lc"}}, []string{"ami-lc"}, nil},
		{"a template with no version follows its default", "CreateAutoScalingGroup",
			url.Values{"LaunchTemplate.LaunchTemplateName": {"web-lt"}}, []string{"ami-v1"}, []string{"false"}},
		{"a template at $Latest", "CreateAutoScalingGroup",
			url.Values{"LaunchTemplate.LaunchTemplateId": {"lt-1"}, "LaunchTemplate.Version": {"$Latest"}}, []string{"ami-v3"}, []string{"false"}},
		{"a template at a pinned version", "UpdateAutoScalingGroup",
			url.Values{"LaunchTemplate.LaunchTemplateId": {"lt-1"}, "LaunchTemplate.Version": {"2"}}, []string{"ami-v2"}, []string{"true"}},
		{"a mixed instances policy template", "CreateAutoScalingGroup",
			url.Values{
				"MixedInstancesPolicy.LaunchTemplate.LaunchTemplateSpecification.LaunchTemplateName": {"web-lt"},
				"MixedInstancesPolicy.LaunchTemplate.LaunchTemplateSpecification.Version":            {"3"},
			}, []string{"ami-v3"}, []string{"true"}},
		{"an instance refresh's desired template", "StartInstanceRefresh",
			url.Values{
				"AutoScalingGroupName":                                 {"web"},
				"DesiredConfiguration.LaunchTemplate.LaunchTemplateId": {"lt-1"},
				"DesiredConfiguration.LaunchTemplate.Version":          {"2"},
			}, []string{"ami-v2"}, nil},
		{"a template that does not exist", "CreateAutoScalingGroup",
			url.Values{"LaunchTemplate.LaunchTemplateId": {"lt-missing"}, "LaunchTemplate.Version": {"4"}}, nil, []string{"true"}},
		{"a version that does not exist", "CreateAutoScalingGroup",
			url.Values{"LaunchTemplate.LaunchTemplateId": {"lt-1"}, "LaunchTemplate.Version": {"9"}}, nil, []string{"true"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := queryServiceConditionContext(t, "autoscaling", c.operation, c.form)
			assertServiceConditionContext(t, ctx, map[string][]string{
				"autoscaling:ImageId":                        c.image,
				"autoscaling:LaunchTemplateVersionSpecified": c.specified,
			})
		})
	}
}

func TestAutoScalingConditionKeysAreAbsentForAbsentMembers(t *testing.T) {
	resetAutoScalingLaunchSources()
	ctx := queryServiceConditionContext(t, "autoscaling", "UpdateAutoScalingGroup", url.Values{
		"AutoScalingGroupName": {"web"},
	})
	if len(ctx) != 0 {
		t.Errorf("an update naming only the group settled %v", ctx)
	}
	// A key belongs only to the actions that declare it.
	ctx = queryServiceConditionContext(t, "autoscaling", "SetDesiredCapacity", url.Values{
		"AutoScalingGroupName": {"web"},
		"MinSize":              {"1"},
	})
	if len(ctx) != 0 {
		t.Errorf("SetDesiredCapacity settled %v", ctx)
	}
}

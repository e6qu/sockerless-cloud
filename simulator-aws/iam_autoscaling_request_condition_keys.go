package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Amazon EC2 Auto Scaling's request condition keys: the sizes, networks,
// load balancers and launch settings a request asks a group or launch
// configuration to have, which a policy holds to an approved set.

const (
	asLaunchTemplatePrefix              = "LaunchTemplate"
	asMixedInstancesPolicyPrefix        = "MixedInstancesPolicy"
	asRefreshLaunchTemplatePrefix       = "DesiredConfiguration.LaunchTemplate"
	asRefreshMixedInstancesPolicyPrefix = "DesiredConfiguration.MixedInstancesPolicy"
)

func init() {
	registerIAMRequestConditionPopulator("autoscaling", iamPopulateAutoScalingRequestConditionKeys)
}

func iamPopulateAutoScalingRequestConditionKeys(r *http.Request, operation string, _ []byte, ctx map[string][]string) {
	groupWrite := operation == "CreateAutoScalingGroup" || operation == "UpdateAutoScalingGroup"

	if groupWrite || operation == "PutScheduledUpdateGroupAction" {
		asSetInteger(ctx, "autoscaling:MinSize", r.FormValue("MinSize"))
		asSetInteger(ctx, "autoscaling:MaxSize", r.FormValue("MaxSize"))
	}

	if groupWrite {
		asSetList(ctx, "autoscaling:CapacityReservationIds",
			iamQueryList(r, "CapacityReservationSpecification.CapacityReservationTarget.CapacityReservationIds"))
		asSetList(ctx, "autoscaling:CapacityReservationResourceGroupArns",
			iamQueryList(r, "CapacityReservationSpecification.CapacityReservationTarget.CapacityReservationResourceGroupArns"))
		if name := r.FormValue("LaunchConfigurationName"); name != "" {
			ctx["autoscaling:LaunchConfigurationName"] = []string{name}
		}
		asSetList(ctx, "autoscaling:InstanceTypes", asOverrideInstanceTypes(r, asMixedInstancesPolicyPrefix))
		if specified, ok := asLaunchTemplateVersionSpecified(r); ok {
			ctx["autoscaling:LaunchTemplateVersionSpecified"] = []string{strconv.FormatBool(specified)}
		}
		asSetList(ctx, "autoscaling:TargetCapacityTypes", asTargetCapacityTypes(r, asMixedInstancesPolicyPrefix))
		asSetList(ctx, "autoscaling:VPCZoneIdentifiers", asVPCZoneIdentifiers(r.FormValue("VPCZoneIdentifier")))
		if image := asGroupRequestImageID(r, asLaunchTemplatePrefix, asMixedInstancesPolicyPrefix); image != "" {
			ctx["autoscaling:ImageId"] = []string{image}
		}
	}

	switch operation {
	case "CreateAutoScalingGroup":
		asSetList(ctx, "autoscaling:LoadBalancerNames", iamQueryList(r, "LoadBalancerNames"))
		asSetList(ctx, "autoscaling:TargetGroupARNs", iamQueryList(r, "TargetGroupARNs"))
		asSetList(ctx, "autoscaling:TrafficSourceIdentifiers", asTrafficSourceIdentifiers(r))
		if principal := r.FormValue("Operator.Principal"); principal != "" {
			ctx["autoscaling:OperatorPrincipal"] = []string{principal}
		}
	case "AttachLoadBalancers", "DetachLoadBalancers":
		asSetList(ctx, "autoscaling:LoadBalancerNames", iamQueryList(r, "LoadBalancerNames"))
	case "AttachLoadBalancerTargetGroups", "DetachLoadBalancerTargetGroups":
		asSetList(ctx, "autoscaling:TargetGroupARNs", iamQueryList(r, "TargetGroupARNs"))
	case "AttachTrafficSources", "DetachTrafficSources":
		asSetList(ctx, "autoscaling:TrafficSourceIdentifiers", asTrafficSourceIdentifiers(r))
	case "StartInstanceRefresh":
		asSetList(ctx, "autoscaling:TargetCapacityTypes", asTargetCapacityTypes(r, asRefreshMixedInstancesPolicyPrefix))
		if image := asGroupRequestImageID(r, asRefreshLaunchTemplatePrefix, asRefreshMixedInstancesPolicyPrefix); image != "" {
			ctx["autoscaling:ImageId"] = []string{image}
		}
	case "CreateLaunchConfiguration":
		if image := r.FormValue("ImageId"); image != "" {
			ctx["autoscaling:ImageId"] = []string{image}
		}
		if instanceType := r.FormValue("InstanceType"); instanceType != "" {
			ctx["autoscaling:InstanceType"] = []string{instanceType}
		}
		if price := r.FormValue("SpotPrice"); price != "" {
			if _, err := strconv.ParseFloat(price, 64); err == nil {
				ctx["autoscaling:SpotPrice"] = []string{price}
			}
		}
		if endpoint := r.FormValue("MetadataOptions.HttpEndpoint"); endpoint != "" {
			ctx["autoscaling:MetadataHttpEndpoint"] = []string{endpoint}
		}
		if tokens := r.FormValue("MetadataOptions.HttpTokens"); tokens != "" {
			ctx["autoscaling:MetadataHttpTokens"] = []string{tokens}
		}
		asSetInteger(ctx, "autoscaling:MetadataHttpPutResponseHopLimit", r.FormValue("MetadataOptions.HttpPutResponseHopLimit"))
	}
}

func asSetInteger(ctx map[string][]string, key, value string) {
	if value == "" {
		return
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return
	}
	ctx[key] = []string{strconv.FormatInt(n, 10)}
}

func asSetList(ctx map[string][]string, key string, values []string) {
	if len(values) > 0 {
		ctx[key] = values
	}
}

// asVPCZoneIdentifiers splits VPCZoneIdentifier, which carries the group's
// subnets as one comma-separated string.
func asVPCZoneIdentifiers(value string) []string {
	var subnets []string
	for _, subnet := range strings.Split(value, ",") {
		if subnet = strings.TrimSpace(subnet); subnet != "" {
			subnets = append(subnets, subnet)
		}
	}
	return subnets
}

func asTrafficSourceIdentifiers(r *http.Request) []string {
	var identifiers []string
	for i := 1; ; i++ {
		identifier := r.FormValue(fmt.Sprintf("TrafficSources.member.%d.Identifier", i))
		if identifier == "" {
			return identifiers
		}
		identifiers = append(identifiers, identifier)
	}
}

func asOverrideInstanceTypes(r *http.Request, policyPrefix string) []string {
	var types []string
	for i := 1; ; i++ {
		prefix := fmt.Sprintf("%s.LaunchTemplate.Overrides.member.%d", policyPrefix, i)
		if !ec2HasFormPrefix(r, prefix) {
			return types
		}
		if instanceType := r.FormValue(prefix + ".InstanceType"); instanceType != "" {
			types = append(types, instanceType)
		}
	}
}

func asTargetCapacityTypes(r *http.Request, policyPrefix string) []string {
	var types []string
	for i := 1; ; i++ {
		segment := fmt.Sprintf("%s.InstancesDistribution.DistributionSegments.member.%d", policyPrefix, i)
		if !ec2HasFormPrefix(r, segment) {
			return types
		}
		types = append(types, iamQueryList(r, segment+".TargetCapacityTypes")...)
	}
}

// asRequestLaunchTemplatePrefix returns the form prefix of the launch template
// the request names, either directly or through its mixed instances policy.
func asRequestLaunchTemplatePrefix(r *http.Request, templatePrefix, policyPrefix string) (string, bool) {
	for _, prefix := range []string{templatePrefix, policyPrefix + ".LaunchTemplate.LaunchTemplateSpecification"} {
		if r.FormValue(prefix+".LaunchTemplateId") != "" || r.FormValue(prefix+".LaunchTemplateName") != "" {
			return prefix, true
		}
	}
	return "", false
}

// asLaunchTemplateVersionSpecified reports whether the request pins a
// launch template version rather than following $Latest or $Default, which
// is also what a request that names no version follows.
func asLaunchTemplateVersionSpecified(r *http.Request) (specified, named bool) {
	prefix, named := asRequestLaunchTemplatePrefix(r, asLaunchTemplatePrefix, asMixedInstancesPolicyPrefix)
	if !named {
		return false, false
	}
	switch r.FormValue(prefix + ".Version") {
	case "", "$Latest", "$Default":
		return false, true
	}
	return true, true
}

// asGroupRequestImageID returns the AMI the group would launch from: the image
// of the launch configuration or of the launch template version the request
// names, read from the stored resource.
func asGroupRequestImageID(r *http.Request, templatePrefix, policyPrefix string) string {
	if name := r.FormValue("LaunchConfigurationName"); name != "" {
		if config, ok := asLaunchConfigurations.Get(name); ok {
			return config.ImageId
		}
		return ""
	}
	prefix, named := asRequestLaunchTemplatePrefix(r, templatePrefix, policyPrefix)
	if !named {
		return ""
	}
	template, ok := lookupLaunchTemplate(r.FormValue(prefix+".LaunchTemplateId"), r.FormValue(prefix+".LaunchTemplateName"))
	if !ok {
		return ""
	}
	var number int64
	switch version := r.FormValue(prefix + ".Version"); version {
	case "", "$Default":
		number = template.DefaultVersionNumber
	case "$Latest":
		number = template.LatestVersionNumber
	default:
		parsed, err := strconv.ParseInt(version, 10, 64)
		if err != nil {
			return ""
		}
		number = parsed
	}
	for _, v := range template.Versions {
		if v.VersionNumber == number {
			return v.Data.ImageId
		}
	}
	return ""
}

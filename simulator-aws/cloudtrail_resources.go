package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// cloudTrailResources extracts the resources[] a recorded API call acted on, so
// the ResourceName / ResourceType LookupEvents attributes can filter on them.
// Real CloudTrail records the resource(s) an operation touches; the resource
// identifier is carried in the request, so we read it from the buffered awsJson
// body (or, for query-protocol services, the parsed form) and pair it with the
// resource's AWS::Service::Type. Operations that act on no named resource (or
// whose identifier is not in the request) record none — never a fabricated
// resource.
func cloudTrailResources(source, eventName string, reqBody []byte, r *http.Request) []CloudTrailResource {
	get := cloudTrailFieldGetter(reqBody, r)
	add := func(typ, name string) []CloudTrailResource {
		if name == "" {
			return nil
		}
		return []CloudTrailResource{{ResourceType: typ, ResourceName: name}}
	}

	switch source {
	case "ecs.amazonaws.com":
		if name := get("clusterName"); name != "" {
			return add("AWS::ECS::Cluster", name)
		}
		if name := cloudTrailShortName(get("cluster")); name != "" {
			return add("AWS::ECS::Cluster", name)
		}
		if td := get("family", "taskDefinition"); td != "" {
			return add("AWS::ECS::TaskDefinition", cloudTrailShortName(td))
		}
		return nil
	case "dynamodb.amazonaws.com":
		return add("AWS::DynamoDB::Table", get("TableName"))
	case "sqs.amazonaws.com":
		if name := get("QueueName"); name != "" {
			return add("AWS::SQS::Queue", name)
		}
		return add("AWS::SQS::Queue", cloudTrailLastSegment(get("QueueUrl")))
	case "sns.amazonaws.com":
		if arn := get("TopicArn"); arn != "" {
			return add("AWS::SNS::Topic", cloudTrailShortName(arn))
		}
		return add("AWS::SNS::Topic", get("Name"))
	case "kms.amazonaws.com":
		return add("AWS::KMS::Key", cloudTrailShortName(get("KeyId")))
	case "secretsmanager.amazonaws.com":
		return add("AWS::SecretsManager::Secret", cloudTrailShortName(get("SecretId")))
	case "logs.amazonaws.com":
		return add("AWS::Logs::LogGroup", get("logGroupName"))
	case "kinesis.amazonaws.com":
		return add("AWS::Kinesis::Stream", get("StreamName"))
	case "glue.amazonaws.com":
		return add("AWS::Glue::Database", get("DatabaseName", "Name"))
	case "states.amazonaws.com":
		if arn := get("stateMachineArn"); arn != "" {
			return add("AWS::StepFunctions::StateMachine", cloudTrailShortName(arn))
		}
		return add("AWS::StepFunctions::StateMachine", get("name"))
	case "ssm.amazonaws.com":
		return add("AWS::SSM::Parameter", get("Name"))
	case "acm.amazonaws.com":
		return add("AWS::CertificateManager::Certificate", cloudTrailShortName(get("CertificateArn")))
	case "codebuild.amazonaws.com":
		return add("AWS::CodeBuild::Project", get("name", "projectName"))
	case "wafv2.amazonaws.com":
		return add("AWS::WAFv2::WebACL", get("Name"))
	case "events.amazonaws.com":
		return add("AWS::Events::Rule", get("Name"))
	case "application-autoscaling.amazonaws.com":
		return add("AWS::ApplicationAutoScaling::ScalableTarget", get("ResourceId"))
	case "servicediscovery.amazonaws.com":
		return add("AWS::ServiceDiscovery::Service", get("Id", "Name"))
	case "iam.amazonaws.com":
		switch {
		case get("RoleName") != "":
			return add("AWS::IAM::Role", get("RoleName"))
		case get("UserName") != "":
			return add("AWS::IAM::User", get("UserName"))
		case get("PolicyArn") != "":
			return add("AWS::IAM::Policy", cloudTrailShortName(get("PolicyArn")))
		case get("InstanceProfileName") != "":
			return add("AWS::IAM::InstanceProfile", get("InstanceProfileName"))
		}
		return nil
	case "rds.amazonaws.com":
		return add("AWS::RDS::DBInstance", get("DBInstanceIdentifier"))
	case "autoscaling.amazonaws.com":
		return add("AWS::AutoScaling::AutoScalingGroup", get("AutoScalingGroupName"))
	case "elasticloadbalancing.amazonaws.com":
		if arn := get("LoadBalancerArn"); arn != "" {
			return add("AWS::ElasticLoadBalancingV2::LoadBalancer", cloudTrailShortName(arn))
		}
		return add("AWS::ElasticLoadBalancingV2::LoadBalancer", get("Name"))
	case "ec2.amazonaws.com":
		for _, k := range []string{"InstanceId", "VpcId", "SubnetId", "GroupId", "ImageId", "VolumeId"} {
			if v := get(k); v != "" {
				return add(cloudTrailEC2ResourceType(k), v)
			}
		}
		return nil
	}
	return nil
}

// cloudTrailFieldGetter returns a case-insensitive lookup over the request's
// top-level string fields — the awsJson body (PascalCase for some services,
// camelCase for others) or the query-protocol form. Case-insensitivity avoids
// guessing each service's wire casing wrong.
func cloudTrailFieldGetter(reqBody []byte, r *http.Request) func(keys ...string) string {
	fields := map[string]string{}
	if len(reqBody) > 0 {
		raw := map[string]any{}
		if json.Unmarshal(reqBody, &raw) == nil {
			for k, v := range raw {
				if s, ok := v.(string); ok {
					fields[strings.ToLower(k)] = s
				}
			}
		}
	}
	return func(keys ...string) string {
		for _, k := range keys {
			if v, ok := fields[strings.ToLower(k)]; ok && v != "" {
				return v
			}
			if v := r.FormValue(k); v != "" {
				return v
			}
		}
		return ""
	}
}

// cloudTrailShortName returns the last path/colon segment of an ARN or path-like
// identifier (e.g. "arn:aws:ecs:…:cluster/probe" -> "probe"), or the input
// unchanged when it carries no separator. Matches the resource name a caller
// filters LookupEvents by.
func cloudTrailShortName(id string) string {
	if id == "" {
		return ""
	}
	if i := strings.LastIndexAny(id, "/"); i >= 0 {
		return id[i+1:]
	}
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// cloudTrailLastSegment returns the final '/'-delimited segment of a URL.
func cloudTrailLastSegment(u string) string {
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[i+1:]
	}
	return u
}

func cloudTrailEC2ResourceType(field string) string {
	switch field {
	case "InstanceId":
		return "AWS::EC2::Instance"
	case "VpcId":
		return "AWS::EC2::VPC"
	case "SubnetId":
		return "AWS::EC2::Subnet"
	case "GroupId":
		return "AWS::EC2::SecurityGroup"
	case "ImageId":
		return "AWS::EC2::Image"
	case "VolumeId":
		return "AWS::EC2::Volume"
	}
	return "AWS::EC2::Resource"
}

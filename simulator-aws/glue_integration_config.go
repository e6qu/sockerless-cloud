package main

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

var glueTargetNamespacePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9_]*[a-z0-9])?$`)

func glueTargetIsS3TablesBucket(arn string) bool {
	return strings.HasPrefix(arn, "arn:aws:s3tables:") && strings.Contains(arn, ":bucket/")
}

// glueCreateIntegrationConfig applies TargetNamespace's rules to a
// CreateIntegration request's IntegrationConfig: AWS Glue takes the member
// only for an Amazon S3 Tables table bucket target, holds it to a table
// namespace name, and names the namespace after the integration ID when the
// request omits it.
func glueCreateIntegrationConfig(config map[string]any, targetArn, integrationID string) (map[string]any, string, string) {
	namespace, given := config["TargetNamespace"]
	if !given {
		if !glueTargetIsS3TablesBucket(targetArn) {
			return config, "", ""
		}
		if config == nil {
			config = map[string]any{}
		}
		config["TargetNamespace"] = "zetl_" + strings.ReplaceAll(integrationID, "-", "_")
		return config, "", ""
	}
	if !glueTargetIsS3TablesBucket(targetArn) {
		return nil, "InvalidInputException", "TargetNamespace is supported only when the target is an Amazon S3 Tables table bucket"
	}
	name, _ := namespace.(string)
	if len(name) < 1 || len(name) > 255 || !glueTargetNamespacePattern.MatchString(name) {
		return nil, "ValidationException", fmt.Sprintf("1 validation error detected: Value '%s' at 'integrationConfig.targetNamespace' failed to satisfy constraint: Member must satisfy regular expression pattern: ^[a-z0-9]([a-z0-9_]*[a-z0-9])?$", name)
	}
	if strings.HasPrefix(name, "aws") {
		return nil, "InvalidInputException", "TargetNamespace must not begin with the reserved prefix aws"
	}
	return config, "", ""
}

// glueModifyIntegrationConfig merges a ModifyIntegration request's
// IntegrationConfig into the stored one. AWS Glue modifies only
// RefreshInterval; a request that changes another member is refused.
func glueModifyIntegrationConfig(stored, update map[string]any) (map[string]any, string) {
	merged := make(map[string]any, len(stored)+1)
	for k, v := range stored {
		merged[k] = v
	}
	for k, v := range update {
		if k == "RefreshInterval" {
			merged[k] = v
			continue
		}
		if !reflect.DeepEqual(stored[k], v) {
			return nil, fmt.Sprintf("%s cannot be modified; ModifyIntegration changes only RefreshInterval", k)
		}
	}
	return merged, ""
}

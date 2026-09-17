package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// AWS CodeBuild's action condition keys name the request member they read:
// codebuild:environment.image is the image the request asks for, and
// codebuild:vpcConfig.subnets every subnet it lists. A list member keyed by an
// identifier is also addressable per element, as in
// codebuild:environment.environmentVariables/STAGE.value. A Bool key on an
// object or a buildspec says only that the member is present, which is how a
// policy's Null operator refuses a request that sets it.

// codeBuildConditionKeys lists the keys, without their service prefix, with the
// type AWS's service reference declares for each.
var codeBuildConditionKeys = map[string]string{
	"artifacts":                    "Bool",
	"artifacts.bucketOwnerAccess":  "String",
	"artifacts.encryptionDisabled": "Bool",
	"artifacts.location":           "String",
	"authType":                     "String",
	"autoRetryLimit":               "Numeric",
	"buildBatchConfig":             "Bool",
	"buildBatchConfig.restrictions.computeTypesAllowed":                "ArrayOfString",
	"buildBatchConfig.restrictions.fleetsAllowed":                      "ArrayOfString",
	"buildBatchConfig.serviceRole":                                     "String",
	"buildType":                                                        "String",
	"cache":                                                            "Bool",
	"cache.location":                                                   "String",
	"cache.modes":                                                      "ArrayOfString",
	"cache.type":                                                       "String",
	"computeConfiguration":                                             "Bool",
	"computeConfiguration.disk":                                        "Numeric",
	"computeConfiguration.instanceType":                                "String",
	"computeConfiguration.machineType":                                 "String",
	"computeConfiguration.memory":                                      "Numeric",
	"computeConfiguration.vCpu":                                        "Numeric",
	"computeType":                                                      "String",
	"concurrentBuildLimit":                                             "Numeric",
	"encryptionKey":                                                    "String",
	"environment":                                                      "Bool",
	"environment.certificate":                                          "String",
	"environment.computeConfiguration":                                 "Bool",
	"environment.computeConfiguration.disk":                            "Numeric",
	"environment.computeConfiguration.instanceType":                    "String",
	"environment.computeConfiguration.machineType":                     "String",
	"environment.computeConfiguration.memory":                          "Numeric",
	"environment.computeConfiguration.vCpu":                            "Numeric",
	"environment.computeType":                                          "String",
	"environment.environmentVariables":                                 "Bool",
	"environment.environmentVariables.name":                            "ArrayOfString",
	"environment.environmentVariables.value":                           "ArrayOfString",
	"environment.environmentVariables/${name}.value":                   "String",
	"environment.fleet.fleetArn":                                       "ARN",
	"environment.image":                                                "String",
	"environment.imagePullCredentialsType":                             "String",
	"environment.privilegedMode":                                       "Bool",
	"environment.registryCredential":                                   "Bool",
	"environment.registryCredential.credential":                        "String",
	"environment.registryCredential.credentialProvider":                "String",
	"environment.type":                                                 "String",
	"environmentType":                                                  "String",
	"exportConfig.s3Destination.bucket":                                "String",
	"exportConfig.s3Destination.bucketOwner":                           "String",
	"exportConfig.s3Destination.encryptionDisabled":                    "Bool",
	"exportConfig.s3Destination.encryptionKey":                         "String",
	"exportConfig.s3Destination.path":                                  "String",
	"fileSystemLocations.identifier":                                   "ArrayOfString",
	"fileSystemLocations.location":                                     "ArrayOfString",
	"fileSystemLocations.type":                                         "ArrayOfString",
	"fileSystemLocations/${identifier}.location":                       "String",
	"fileSystemLocations/${identifier}.type":                           "String",
	"fleetServiceRole":                                                 "String",
	"imageId":                                                          "String",
	"logsConfig":                                                       "Bool",
	"logsConfig.s3Logs":                                                "Bool",
	"logsConfig.s3Logs.bucketOwnerAccess":                              "String",
	"logsConfig.s3Logs.encryptionDisabled":                             "Bool",
	"logsConfig.s3Logs.location":                                       "String",
	"logsConfig.s3Logs.status":                                         "String",
	"manualCreation":                                                   "Bool",
	"projectVisibility":                                                "String",
	"scopeConfiguration.domain":                                        "String",
	"scopeConfiguration.name":                                          "String",
	"scopeConfiguration.scope":                                         "String",
	"secondaryArtifacts":                                               "Bool",
	"secondaryArtifacts.artifactIdentifier":                            "ArrayOfString",
	"secondaryArtifacts.bucketOwnerAccess":                             "ArrayOfString",
	"secondaryArtifacts.encryptionDisabled":                            "ArrayOfBool",
	"secondaryArtifacts.location":                                      "ArrayOfString",
	"secondaryArtifacts/${artifactIdentifier}.bucketOwnerAccess":       "String",
	"secondaryArtifacts/${artifactIdentifier}.encryptionDisabled":      "Bool",
	"secondaryArtifacts/${artifactIdentifier}.location":                "String",
	"secondarySources":                                                 "Bool",
	"secondarySources.auth.resource":                                   "ArrayOfString",
	"secondarySources.auth.type":                                       "ArrayOfString",
	"secondarySources.buildStatusConfig.context":                       "ArrayOfString",
	"secondarySources.buildStatusConfig.targetUrl":                     "ArrayOfString",
	"secondarySources.buildspec":                                       "Bool",
	"secondarySources.insecureSsl":                                     "ArrayOfBool",
	"secondarySources.location":                                        "ArrayOfString",
	"secondarySources.sourceIdentifier":                                "ArrayOfString",
	"secondarySources/${sourceIdentifier}.auth.resource":               "String",
	"secondarySources/${sourceIdentifier}.auth.type":                   "String",
	"secondarySources/${sourceIdentifier}.buildStatusConfig.context":   "String",
	"secondarySources/${sourceIdentifier}.buildStatusConfig.targetUrl": "String",
	"secondarySources/${sourceIdentifier}.buildspec":                   "Bool",
	"secondarySources/${sourceIdentifier}.insecureSsl":                 "Bool",
	"secondarySources/${sourceIdentifier}.location":                    "String",
	"serverType":                                                       "String",
	"serviceRole":                                                      "String",
	"shouldOverwrite":                                                  "Bool",
	"source":                                                           "Bool",
	"source.auth.resource":                                             "String",
	"source.auth.type":                                                 "String",
	"source.buildStatusConfig.context":                                 "String",
	"source.buildStatusConfig.targetUrl":                               "String",
	"source.buildspec":                                                 "Bool",
	"source.insecureSsl":                                               "Bool",
	"source.location":                                                  "String",
	"token":                                                            "String",
	"username":                                                         "String",
	"vpcConfig":                                                        "Bool",
	"vpcConfig.securityGroupIds":                                       "ArrayOfString",
	"vpcConfig.subnets":                                                "ArrayOfString",
	"vpcConfig.vpcId":                                                  "String",
}

// codeBuildListIdentifiers names the member that identifies an element of each
// list a key can address per element.
var codeBuildListIdentifiers = map[string]string{
	"environment.environmentVariables": "name",
	"fileSystemLocations":              "identifier",
	"secondaryArtifacts":               "artifactIdentifier",
	"secondarySources":                 "sourceIdentifier",
}

// codeBuildBuildOverrides maps the members StartBuild and StartBuildBatch take
// to the project members the keys are named after.
var codeBuildBuildOverrides = map[string]string{
	"artifactsOverride":                "artifacts",
	"autoRetryLimitOverride":           "autoRetryLimit",
	"buildBatchConfigOverride":         "buildBatchConfig",
	"buildStatusConfigOverride":        "source.buildStatusConfig",
	"buildspecOverride":                "source.buildspec",
	"cacheOverride":                    "cache",
	"certificateOverride":              "environment.certificate",
	"computeTypeOverride":              "environment.computeType",
	"encryptionKeyOverride":            "encryptionKey",
	"environmentTypeOverride":          "environment.type",
	"environmentVariablesOverride":     "environment.environmentVariables",
	"fleetOverride":                    "environment.fleet",
	"imageOverride":                    "environment.image",
	"imagePullCredentialsTypeOverride": "environment.imagePullCredentialsType",
	"insecureSslOverride":              "source.insecureSsl",
	"logsConfigOverride":               "logsConfig",
	"privilegedModeOverride":           "environment.privilegedMode",
	"registryCredentialOverride":       "environment.registryCredential",
	"secondaryArtifactsOverride":       "secondaryArtifacts",
	"secondarySourcesOverride":         "secondarySources",
	"serviceRoleOverride":              "serviceRole",
	"sourceAuthOverride":               "source.auth",
	"sourceLocationOverride":           "source.location",
	"sourceTypeOverride":               "source.type",
}

// iamPopulateCodeBuildConditionKeys adds the keys an AWS CodeBuild request
// settles.
func iamPopulateCodeBuildConditionKeys(operation string, body []byte, ctx map[string][]string) {
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return
	}
	if operation == "StartBuild" || operation == "StartBuildBatch" {
		request = codeBuildProjectView(request)
	}
	for key, keyType := range codeBuildConditionKeys {
		list, template, perElement := strings.Cut(key, "/${")
		if !perElement {
			if values := codeBuildValues(codeBuildLookup([]any{request}, key), keyType); len(values) > 0 {
				ctx["codebuild:"+key] = values
			}
			continue
		}
		_, field, _ := strings.Cut(template, "}.")
		for _, element := range codeBuildLookup([]any{request}, list) {
			member, ok := element.(map[string]any)
			if !ok {
				continue
			}
			id, _ := member[codeBuildListIdentifiers[list]].(string)
			if id == "" {
				continue
			}
			if values := codeBuildValues(codeBuildLookup([]any{member}, field), keyType); len(values) > 0 {
				ctx["codebuild:"+list+"/"+id+"."+field] = values
			}
		}
	}
}

// codeBuildProjectView re-homes a build request's override members under the
// project members they override.
func codeBuildProjectView(request map[string]any) map[string]any {
	view := map[string]any{}
	for member, value := range request {
		path, ok := codeBuildBuildOverrides[member]
		if !ok {
			continue
		}
		node := view
		segments := strings.Split(path, ".")
		for _, segment := range segments[:len(segments)-1] {
			child, ok := node[segment].(map[string]any)
			if !ok {
				child = map[string]any{}
				node[segment] = child
			}
			node = child
		}
		node[segments[len(segments)-1]] = value
	}
	return view
}

// codeBuildLookup follows a dotted path through nodes, flattening every list it
// passes through, and returns what it finds at the end.
func codeBuildLookup(nodes []any, path string) []any {
	for _, segment := range strings.Split(path, ".") {
		var next []any
		for _, node := range nodes {
			member, ok := node.(map[string]any)
			if !ok {
				continue
			}
			value, ok := member[segment]
			if !ok || value == nil {
				continue
			}
			if list, isList := value.([]any); isList {
				next = append(next, list...)
			} else {
				next = append(next, value)
			}
		}
		nodes = next
	}
	return nodes
}

// codeBuildValues renders what a key found. A Bool key reports a boolean
// member's value, and for any other member only that it is present.
func codeBuildValues(found []any, keyType string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, value := range found {
		switch v := value.(type) {
		case bool:
			add(strconv.FormatBool(v))
		case string:
			if strings.HasSuffix(keyType, "Bool") {
				add("true")
			} else {
				add(v)
			}
		case float64:
			add(strconv.FormatFloat(v, 'f', -1, 64))
		case map[string]any:
			add("true")
		}
	}
	return out
}

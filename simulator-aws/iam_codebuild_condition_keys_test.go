package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func codeBuildContext(t *testing.T, operation, body string) map[string][]string {
	t.Helper()
	ctx := map[string][]string{}
	iamPopulateCodeBuildConditionKeys(operation, []byte(body), ctx)
	return ctx
}

func TestCodeBuildConditionKeysReadTheProjectRequest(t *testing.T) {
	ctx := codeBuildContext(t, "CreateProject", `{
		"name": "p",
		"source": {"type": "GITHUB", "location": "https://example.com/repo.git", "buildspec": "version: 0.2", "insecureSsl": false},
		"environment": {
			"type": "LINUX_CONTAINER", "image": "aws/codebuild/standard:7.0", "computeType": "BUILD_GENERAL1_SMALL",
			"privilegedMode": true,
			"environmentVariables": [{"name": "STAGE", "value": "BETA"}, {"name": "TOKEN", "value": "t"}]
		},
		"vpcConfig": {"vpcId": "vpc-1", "subnets": ["subnet-a", "subnet-b"], "securityGroupIds": ["sg-1"]},
		"secondarySources": [{"type": "S3", "sourceIdentifier": "extra", "location": "bucket/key"}],
		"concurrentBuildLimit": 3
	}`)
	want := map[string][]string{
		"codebuild:source":                                       {"true"},
		"codebuild:source.location":                              {"https://example.com/repo.git"},
		"codebuild:source.buildspec":                             {"true"},
		"codebuild:source.insecureSsl":                           {"false"},
		"codebuild:environment":                                  {"true"},
		"codebuild:environment.image":                            {"aws/codebuild/standard:7.0"},
		"codebuild:environment.privilegedMode":                   {"true"},
		"codebuild:environment.environmentVariables":             {"true"},
		"codebuild:environment.environmentVariables.name":        {"STAGE", "TOKEN"},
		"codebuild:environment.environmentVariables/STAGE.value": {"BETA"},
		"codebuild:vpcConfig.vpcId":                              {"vpc-1"},
		"codebuild:vpcConfig.subnets":                            {"subnet-a", "subnet-b"},
		"codebuild:secondarySources.sourceIdentifier":            {"extra"},
		"codebuild:secondarySources/extra.location":              {"bucket/key"},
		"codebuild:concurrentBuildLimit":                         {"3"},
	}
	for key, values := range want {
		got := append([]string(nil), ctx[key]...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, values) {
			t.Errorf("%s = %v, want %v", key, got, values)
		}
	}
	// The buildspec key says the member is there; it never carries the text.
	for key, values := range ctx {
		for _, v := range values {
			if strings.Contains(v, "version: 0.2") {
				t.Errorf("%s carries the buildspec text", key)
			}
		}
	}
	if _, ok := ctx["codebuild:cache"]; ok {
		t.Error("codebuild:cache is set for a request with no cache")
	}
}

// StartBuild names its settings as overrides; the keys are named after the
// project members they override.
func TestCodeBuildConditionKeysReadBuildOverrides(t *testing.T) {
	ctx := codeBuildContext(t, "StartBuild", `{
		"projectName": "p",
		"imageOverride": "custom:1",
		"buildspecOverride": "version: 0.2",
		"environmentVariablesOverride": [{"name": "STAGE", "value": "PRODUCTION"}]
	}`)
	for key, want := range map[string]string{
		"codebuild:environment.image":                            "custom:1",
		"codebuild:source.buildspec":                             "true",
		"codebuild:environment.environmentVariables/STAGE.value": "PRODUCTION",
		"codebuild:environment":                                  "true",
	} {
		if got := ctx[key]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want [%s]", key, got, want)
		}
	}
	if _, ok := ctx["codebuild:projectName"]; ok {
		t.Error("a member no key names reached the context")
	}
}

// Every key the populator knows is one AWS declares, with the type it declares.
func TestCodeBuildConditionKeysMatchTheServiceReference(t *testing.T) {
	f, err := os.Open("../specs/cloud-api/aws/service-reference/codebuild.servicereference.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		ConditionKeys []struct {
			Name  string
			Types []string
		}
		Actions []struct {
			ActionConditionKeys []string
		}
	}
	if err := json.NewDecoder(zr).Decode(&ref); err != nil {
		t.Fatal(err)
	}
	declared := map[string]string{}
	for _, key := range ref.ConditionKeys {
		declared[key.Name] = key.Types[0]
	}
	used := map[string]bool{}
	for _, action := range ref.Actions {
		for _, key := range action.ActionConditionKeys {
			if strings.HasPrefix(key, "codebuild:") && !strings.Contains(key, "ResourceTag") {
				used[key] = true
			}
		}
	}
	for key, keyType := range codeBuildConditionKeys {
		if declared["codebuild:"+key] != keyType {
			t.Errorf("codebuild:%s has type %q here and %q in the service reference", key, keyType, declared["codebuild:"+key])
		}
		delete(used, "codebuild:"+key)
	}
	for key := range used {
		t.Errorf("%s is an action condition key the populator does not know", key)
	}
}

package aws_cli_test

import (
	"strings"
	"testing"
)

// TestIAM_ResourceTagConditionCLI reproduces issue #661 over the aws CLI: a
// `DeleteVolume` grant conditioned on `aws:ResourceTag/edd:managed=true` is
// denied on an untagged volume and allowed once the volume carries the tag.
func TestIAM_ResourceTagConditionCLI(t *testing.T) {
	user := "rt-cond-cli"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })

	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user,
		"--policy-name", "tag-scoped",
		"--policy-document", `{"Version":"2012-10-17","Statement":[`+
			`{"Effect":"Allow","Action":["ec2:CreateVolume","ec2:CreateTags","ec2:DescribeVolumes"],"Resource":"*"},`+
			`{"Effect":"Allow","Action":"ec2:DeleteVolume","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/edd:managed":"true"}}}]}`))

	out := runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user, "--output", "json"))
	var ck struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, out, &ck)

	createVol := awsCLI("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "1", "--output", "json")
	createVol.Env = withCreds(createVol.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	volOut := runCLI(t, createVol)
	var v struct {
		VolumeId string `json:"VolumeId"`
	}
	parseJSON(t, volOut, &v)
	if v.VolumeId == "" {
		t.Fatal("create-volume returned no VolumeId")
	}

	// Untagged → denied.
	del := awsCLI("ec2", "delete-volume", "--volume-id", v.VolumeId)
	del.Env = withCreds(del.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	denied := runCLIExpectError(t, del)
	if !strings.Contains(denied, "UnauthorizedOperation") {
		t.Fatalf("delete-volume on untagged volume expected UnauthorizedOperation; got: %s", denied)
	}

	// Tag it.
	tag := awsCLI("ec2", "create-tags", "--resources", v.VolumeId, "--tags", "Key=edd:managed,Value=true")
	tag.Env = withCreds(tag.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	runCLI(t, tag)

	// Now allowed.
	del2 := awsCLI("ec2", "delete-volume", "--volume-id", v.VolumeId)
	del2.Env = withCreds(del2.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	runCLI(t, del2)
}

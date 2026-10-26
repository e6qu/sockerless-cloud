package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2LaunchTemplateCLI drives the fck-nat aws_launch_template shape through
// botocore's query protocol; describe-launch-template-versions returns the
// submitted data.
func TestEC2LaunchTemplateCLI(t *testing.T) {
	t.Parallel()
	data := `{"ImageId":"ami-12345678","InstanceType":"t4g.nano",` +
		`"IamInstanceProfile":{"Name":"fck-nat-profile"},` +
		`"NetworkInterfaces":[{"DeviceIndex":0,"AssociatePublicIpAddress":true,"Groups":["sg-0abc1234"]}],` +
		`"TagSpecifications":[{"ResourceType":"instance","Tags":[{"Key":"Name","Value":"fck-nat"}]}]}`

	ltID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-launch-template",
		"--launch-template-name", "cli-fck-nat-lt",
		"--launch-template-data", data,
		"--query", "LaunchTemplate.LaunchTemplateId", "--output", "text")))
	if !strings.HasPrefix(ltID, "lt-") {
		t.Fatalf("expected an lt- id, got %q", ltID)
	}

	gotID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-launch-templates",
		"--launch-template-names", "cli-fck-nat-lt",
		"--query", "LaunchTemplates[0].LaunchTemplateId", "--output", "text")))
	if gotID != ltID {
		t.Fatalf("describe-launch-templates returned %q, want %q", gotID, ltID)
	}

	img := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-launch-template-versions",
		"--launch-template-id", ltID, "--versions", "$Latest",
		"--query", "LaunchTemplateVersions[0].LaunchTemplateData.ImageId", "--output", "text")))
	if img != "ami-12345678" {
		t.Fatalf("expected ImageId ami-12345678 to round-trip, got %q", img)
	}
	sg := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-launch-template-versions",
		"--launch-template-id", ltID, "--versions", "$Latest",
		"--query", "LaunchTemplateVersions[0].LaunchTemplateData.NetworkInterfaces[0].Groups[0]", "--output", "text")))
	if sg != "sg-0abc1234" {
		t.Fatalf("expected network-interface security group to round-trip, got %q", sg)
	}

	runCLI(t, awsCLI("ec2", "delete-launch-template", "--launch-template-id", ltID))
}

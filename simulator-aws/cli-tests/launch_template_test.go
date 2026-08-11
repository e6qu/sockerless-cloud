package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2LaunchTemplateCLI drives the EC2 Launch Template ops via the aws CLI
// : create with launch-template-data, describe, describe-versions
// (round-tripping the data), delete. This is the fck-nat aws_launch_template
// shape exercised through botocore's query protocol.
func TestEC2LaunchTemplateCLI(t *testing.T) {
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

	// describe-launch-templates by name resolves the template.
	gotID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-launch-templates",
		"--launch-template-names", "cli-fck-nat-lt",
		"--query", "LaunchTemplates[0].LaunchTemplateId", "--output", "text")))
	if gotID != ltID {
		t.Fatalf("describe-launch-templates returned %q, want %q", gotID, ltID)
	}

	// describe-launch-template-versions must echo the submitted data back.
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

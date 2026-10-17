package aws_cli_test

import (
	"strings"
	"sync"
	"testing"
)

var (
	ec2PublicAMIMu sync.Mutex
	ec2PublicAMIs  = map[string]string{}
)

// ec2PublicAMI resolves a public AMI by owner and name through describe-images,
// the way a client finds an AMI on AWS, once per process.
func ec2PublicAMI(t *testing.T, owner, namePattern string) string {
	t.Helper()
	ec2PublicAMIMu.Lock()
	defer ec2PublicAMIMu.Unlock()
	key := owner + " " + namePattern
	if id, ok := ec2PublicAMIs[key]; ok {
		return id
	}
	id := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-images", "--owners", owner,
		"--filters", "Name=name,Values="+namePattern, "Name=architecture,Values=x86_64", "Name=state,Values=available",
		"--query", "Images[0].ImageId", "--output", "text")))
	if id == "" || id == "None" {
		t.Fatalf("no public AMI matches %s %s", owner, namePattern)
	}
	ec2PublicAMIs[key] = id
	return id
}

// ec2AmazonLinuxAMI is the Amazon Linux 2023 x86_64 AMI.
func ec2AmazonLinuxAMI(t *testing.T) string {
	return ec2PublicAMI(t, "amazon", "al2023-ami-2023.*-kernel-6.1-x86_64")
}

// ec2UbuntuAMI is the Ubuntu 24.04 LTS x86_64 AMI Canonical publishes.
func ec2UbuntuAMI(t *testing.T) string {
	return ec2PublicAMI(t, "099720109477", "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*")
}

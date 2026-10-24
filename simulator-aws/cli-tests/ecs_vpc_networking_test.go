package aws_cli_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

const vpcNetBusybox = "mirror.gcr.io/library/busybox:latest"

// vpcServerScript runs a long-lived HTTP server serving "ok" on :80, so probes
// can test real TCP reachability/isolation between tasks.
const vpcServerScript = "mkdir -p /www && echo ok > /www/index.html && httpd -f -p 80 -h /www"

// TestECSVPCNetworking proves the VPC task-networking contract end-to-end,
// probing through the task containers themselves: an ECS task's ENI
// privateIPv4Address is the container's real eth0 address, reachable from
// another task in the same VPC and isolated from a task in a different VPC.
func TestECSVPCNetworking(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	const rangeA, rangeB = "10.120.0.0/16", "10.121.0.0/16"
	vpcA, subnetA := mkVPCSubnet(t, q, rangeA, subnetOf(rangeA))
	vpcB, subnetB := mkVPCSubnet(t, q, rangeB, subnetOf(rangeB))
	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerTaskDef(q, "vpc-server", vpcServerScript)
	registerTaskDef(q, "vpc-client", "trap 'exit 143' TERM; sleep 120 & wait")

	server := runTask(q, "vpc-server", subnetA)
	clientSame := runTask(q, "vpc-client", subnetA)
	clientOther := runTask(q, "vpc-client", subnetB)
	t.Cleanup(func() {
		for _, task := range []string{server, clientSame, clientOther} {
			runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task))
		}
		rmDockerNetworks(ecsVPCNet(vpcA), ecsVPCNet(vpcB))
	})
	waitRunning(t, q, server)
	waitRunning(t, q, clientSame)
	waitRunning(t, q, clientOther)

	// The reported ENI IP is the address the engine gave the container's
	// eth0 on the VPC's network.
	eniIP := taskENIIP(q, server)
	if !strings.HasPrefix(eniIP, prefixOf(rangeA)) {
		t.Fatalf("server ENI IP not in subnet-A CIDR: %q", eniIP)
	}
	if ips := taskEth0IPs(t, server); !containsIP(ips, eniIP) {
		t.Fatalf("reported ENI IP %q is not on the container's eth0 (addresses: %v)", eniIP, ips)
	}

	// Intra-VPC reachable; cross-VPC isolated.
	if code, out := taskWget(t, clientSame, eniIP); code != 0 || !strings.Contains(out, "ok") {
		t.Fatalf("same-VPC task should reach %s: exit=%d out=%q", eniIP, code, out)
	}
	if code, _ := taskWget(t, clientOther, eniIP); code == 0 {
		t.Fatalf("different-VPC task should be isolated from %s", eniIP)
	}
}

func TestECSManagedEBSAwsvpcReachability(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	const vpcRange = "10.254.0.0/16"
	vpcID, subnetID := mkVPCSubnet(t, q, vpcRange, subnetOf(vpcRange))
	t.Cleanup(func() {
		stopCLITasksInSubnet(t, "default", subnetID)
		q("ec2", "delete-subnet", "--subnet-id", subnetID)
		q("ec2", "delete-vpc", "--vpc-id", vpcID)
		rmDockerNetworks(ecsVPCNet(vpcID))
	})
	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	q("ecs", "register-task-definition", "--family", "ebs-vpc-server",
		"--network-mode", "awsvpc", "--requires-compatibilities", "FARGATE", "--cpu", "256", "--memory", "512",
		"--volumes", `[{"name":"workspace","configuredAtLaunch":true}]`,
		"--container-definitions", `[{"name":"app","image":"`+vpcNetBusybox+`","stopTimeout":2,"entryPoint":["sh","-c"],"command":["mkdir -p /workspace/www && echo ebs-ok > /workspace/www/index.html && httpd -f -p 80 -h /workspace/www"],"mountPoints":[{"sourceVolume":"workspace","containerPath":"/workspace"}]}]`,
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")
	registerTaskDef(q, "ebs-vpc-client", "trap 'exit 143' TERM; sleep 120 & wait")

	volumeRole := createCLIECSRole(t, awsCLI, "cli-ebs-vpc-volumes", "ecs.amazonaws.com", cliECSVolumeInfrastructureActions...)
	server := q("ecs", "run-task",
		"--cluster", "default",
		"--task-definition", "ebs-vpc-server",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--volume-configurations", `[{"name":"workspace","managedEBSVolume":{"roleArn":"`+volumeRole+`","sizeInGiB":1,"volumeType":"gp3"}}]`,
		"--query", "tasks[0].taskArn", "--output", "text")
	client := runTask(q, "ebs-vpc-client", subnetID)
	t.Cleanup(func() {
		for _, task := range []string{server, client} {
			runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task))
		}
	})
	waitRunning(t, q, server)
	waitRunning(t, q, client)

	eniIP := taskENIIP(q, server)
	if ips := taskEth0IPs(t, server); !containsIP(ips, eniIP) {
		t.Fatalf("managed-EBS task reported ENI IP %q is not on eth0 (addresses: %v)", eniIP, ips)
	}
	out := q("ecs", "describe-tasks", "--cluster", "default", "--tasks", server, "--output", "json")
	var desc struct {
		Tasks []struct {
			Attachments []struct {
				Type    string `json:"type"`
				Details []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"details"`
			} `json:"attachments"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &desc)
	if len(desc.Tasks) != 1 {
		t.Fatalf("describe managed-EBS server task returned %d tasks", len(desc.Tasks))
	}
	if vol := cliEBSVolumeID(t, desc.Tasks[0].Attachments); !strings.HasPrefix(vol, "vol-") {
		t.Fatalf("managed-EBS task volume id = %q, want vol-*", vol)
	}

	var body string
	var code int
	for attempt := 0; attempt < 10; attempt++ {
		code, body = taskWget(t, client, eniIP)
		if code == 0 && strings.Contains(body, "ebs-ok") {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("same-VPC task should reach managed-EBS task at %s: exit=%d out=%q", eniIP, code, body)
}

func mkVPCSubnet(t *testing.T, q func(...string) string, vpcCidr, snCidr string) (vpcID, subnetID string) {
	t.Helper()
	vpcID = q("ec2", "create-vpc", "--cidr-block", vpcCidr, "--query", "Vpc.VpcId", "--output", "text")
	subnetID = q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", snCidr,
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")
	return vpcID, subnetID
}

// A test names its VPC's /16 as a literal, which is what
// scripts/check-test-network-ranges.go holds apart across the suite: two
// tests that probe the engine for a free range at the same moment both find
// the same one, since neither has placed a task on it yet.

// subnetOf is the first /24 of a VPC's /16.
func subnetOf(vpcRange string) string { return strings.TrimSuffix(vpcRange, "/16") + "/24" }

// prefixOf is the dotted prefix every address of a VPC's first /24 starts with.
func prefixOf(vpcRange string) string { return strings.TrimSuffix(vpcRange, "0.0/16") + "0." }

func registerTaskDef(q func(...string) string, family, script string) {
	q("ecs", "register-task-definition", "--family", family,
		"--network-mode", "awsvpc", "--requires-compatibilities", "FARGATE", "--cpu", "256", "--memory", "512",
		"--container-definitions", `[{"name":"app","image":"`+vpcNetBusybox+`","stopTimeout":2,"entryPoint":["sh","-c"],"command":["`+script+`"]}]`,
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")
}

func runTask(q func(...string) string, family, subnet string) string {
	return runTaskWithNetworkConfiguration(q, family, `awsvpcConfiguration={subnets=[`+subnet+`]}`)
}

func runTaskWithNetworkConfiguration(q func(...string) string, family, networkConfiguration string) string {
	return q("ecs", "run-task", "--cluster", "default", "--task-definition", family,
		"--network-configuration", networkConfiguration,
		"--query", "tasks[0].taskArn", "--output", "text")
}

func taskENIIP(q func(...string) string, taskArn string) string {
	return q("ecs", "describe-tasks", "--cluster", "default", "--tasks", taskArn,
		"--query", "tasks[0].containers[0].networkInterfaces[0].privateIpv4Address", "--output", "text")
}

func ecsVPCNet(vpcID string) string { return "sockerless-sim-vpc-" + vpcID }

func taskID(taskArn string) string {
	parts := strings.Split(taskArn, "/")
	return parts[len(parts)-1]
}

func taskContainerID(t *testing.T, taskArn string) string {
	t.Helper()
	var cid string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		cid = strings.TrimSpace(dockerOut(t, "ps", "-q",
			"-f", "label=sockerless-sim-task="+taskID(taskArn),
			"-f", "label=sockerless-sim-task-container=app"))
		if cid != "" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if cid == "" {
		t.Fatalf("no running container for task %s", taskArn)
	}
	if strings.Contains(cid, "\n") {
		t.Fatalf("multiple app containers for task %s: %q", taskArn, cid)
	}
	return cid
}

// taskEth0IPs reads every IPv4 address on the container's eth0, among which
// the reported ENI IP must be.
func taskEth0IPs(t *testing.T, taskArn string) []string {
	t.Helper()
	cid := taskContainerID(t, taskArn)
	for attempt := 0; attempt < 10; attempt++ {
		var ips []string
		out, _ := exec.Command("docker", "exec", cid, "ip", "-4", "-o", "addr", "show", "eth0").CombinedOutput()
		for _, f := range strings.Fields(string(out)) {
			if strings.HasPrefix(f, "inet") {
				continue
			}
			if i := strings.Index(f, "/"); i > 0 && strings.Count(f[:i], ".") == 3 {
				ips = append(ips, f[:i])
			}
		}
		if len(ips) > 0 {
			return ips
		}
		time.Sleep(time.Second)
	}
	return nil
}

func containsIP(ips []string, want string) bool {
	for _, ip := range ips {
		if ip == want {
			return true
		}
	}
	return false
}

// taskWget fetches http://ip/index.html from inside a task container.
func taskWget(t *testing.T, taskArn, ip string) (int, string) {
	t.Helper()
	return taskWgetURL(t, taskArn, "http://"+ip+"/index.html")
}

func taskWgetURL(t *testing.T, taskArn, url string) (int, string) {
	t.Helper()
	cid := taskContainerID(t, taskArn)
	out, err := exec.Command("docker", "exec", cid, "wget", "-T", "3", "-q", "-O", "-", url).CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		return -1, string(out)
	}
	return 0, string(out)
}

func dockerOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// waitRunning waits with `aws ecs wait tasks-running`, which fails when the
// task stops instead of running.
func waitRunning(t *testing.T, q func(...string) string, taskArn string) {
	t.Helper()
	q("ecs", "wait", "tasks-running", "--cluster", "default", "--tasks", taskArn)
}

func waitTaskContainersGone(t *testing.T, taskArns ...string) {
	t.Helper()
	// Generous deadline: after stop-task the container takes the SIGTERM grace +
	// Docker stop to disappear, which on a loaded CI runner can exceed a tight
	// 30s window (the failure was an intermittent "still running" under load).
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		allGone := true
		for _, taskArn := range taskArns {
			if out := strings.TrimSpace(dockerOut(t, "ps", "-q", "-f", "label=sockerless-sim-task="+taskID(taskArn))); out != "" {
				allGone = false
				break
			}
		}
		if allGone {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("task containers still running for %v", taskArns)
}

// rmDockerNetworks removes simulator VPC networks, retrying while task
// containers detach. No-op for names that don't exist.
func rmDockerNetworks(names ...string) {
	for attempt := 0; attempt < 12; attempt++ {
		pending := false
		for _, n := range names {
			if exec.Command("docker", "network", "inspect", n).Run() != nil {
				continue
			}
			if exec.Command("docker", "network", "rm", n).Run() != nil {
				pending = true
			}
		}
		if !pending {
			return
		}
		time.Sleep(time.Second)
	}
}

// TestECSVPCDeleteVpcAllowsCIDRReuse covers the network cleanup a terraform
// destroy/apply cycle needs: once the task and subnet are gone, DeleteVpc
// removes the VPC's engine network so a new VPC can reuse the same CIDR.
func TestECSVPCDeleteVpcAllowsCIDRReuse(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerTaskDef(q, "vpc-reuse-client", "trap 'exit 143' TERM; sleep 120 & wait")

	const vpcRange = "10.149.0.0/16"
	vpc1, subnet1 := mkVPCSubnet(t, q, vpcRange, subnetOf(vpcRange))
	task1 := runTask(q, "vpc-reuse-client", subnet1)
	waitRunning(t, q, task1)
	runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task1))
	waitTaskContainersGone(t, task1)
	stopCLITasksInSubnet(t, "default", subnet1)
	q("ec2", "delete-subnet", "--subnet-id", subnet1)
	q("ec2", "delete-vpc", "--vpc-id", vpc1)
	if exec.Command("docker", "network", "inspect", ecsVPCNet(vpc1)).Run() == nil {
		t.Fatalf("DeleteVpc left Docker VPC network %s behind", ecsVPCNet(vpc1))
	}

	vpc2, subnet2 := mkVPCSubnet(t, q, vpcRange, subnetOf(vpcRange))
	task2 := runTask(q, "vpc-reuse-client", subnet2)
	t.Cleanup(func() {
		_ = awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task2).Run()
		waitTaskContainersGone(t, task2)
		stopCLITasksInSubnet(t, "default", subnet2)
		_ = awsCLI("ec2", "delete-subnet", "--subnet-id", subnet2).Run()
		_ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpc2).Run()
		rmDockerNetworks(ecsVPCNet(vpc2))
	})
	waitRunning(t, q, task2)
	ips := taskEth0IPs(t, task2)
	inReusedCIDR := false
	for _, ip := range ips {
		if strings.HasPrefix(ip, prefixOf(vpcRange)) {
			inReusedCIDR = true
			break
		}
	}
	if !inReusedCIDR {
		t.Fatalf("recreated VPC task should own an ENI address in the reused CIDR, got eth0 addresses %v", ips)
	}
}

func stopCLITasksInSubnet(t *testing.T, cluster, subnetID string) {
	t.Helper()
	// Amazon ECS refuses DeleteCluster while the cluster has active tasks, so a
	// cluster the test already deleted holds none.
	active := runCLI(t, awsCLI("ecs", "describe-clusters", "--clusters", cluster,
		"--query", "length(clusters[?status=='ACTIVE'])", "--output", "text"))
	if strings.TrimSpace(active) == "0" {
		return
	}
	var arns []string
	for _, desired := range []string{"RUNNING", "STOPPED"} {
		arns = append(arns, strings.Fields(runCLI(t, awsCLI("ecs", "list-tasks", "--cluster", cluster,
			"--desired-status", desired, "--query", "taskArns", "--output", "text")))...)
	}
	var inSubnet []string
	for len(arns) > 0 {
		batch := arns[:min(len(arns), 100)]
		arns = arns[len(batch):]
		var described struct {
			Tasks []struct {
				TaskArn     string `json:"taskArn"`
				LastStatus  string `json:"lastStatus"`
				Attachments []struct {
					Details []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"details"`
				} `json:"attachments"`
			} `json:"tasks"`
		}
		parseJSON(t, runCLI(t, awsCLI(append([]string{"ecs", "describe-tasks", "--cluster", cluster, "--tasks"}, batch...)...)), &described)
		for _, task := range described.Tasks {
			if task.LastStatus == "STOPPED" {
				continue
			}
			for _, attachment := range task.Attachments {
				for _, detail := range attachment.Details {
					if detail.Name == "subnetId" && detail.Value == subnetID {
						inSubnet = append(inSubnet, task.TaskArn)
					}
				}
			}
		}
	}
	if len(inSubnet) == 0 {
		return
	}
	for _, arn := range inSubnet {
		runCLI(t, awsCLI("ecs", "stop-task", "--cluster", cluster, "--task", arn))
	}
	runCLI(t, awsCLI(append([]string{"ecs", "wait", "tasks-stopped", "--cluster", cluster, "--tasks"}, inSubnet...)...))
}

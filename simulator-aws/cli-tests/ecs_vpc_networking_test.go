package aws_cli_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const vpcNetBusybox = "public.ecr.aws/docker/library/busybox:latest"

// vpcServerScript runs a long-lived HTTP server serving "ok" on :80, so probes
// can test real TCP reachability/isolation between tasks.
const vpcServerScript = "mkdir -p /www && echo ok > /www/index.html && httpd -f -p 80 -h /www"

// TestECSVPCNetworking proves the VPC task-networking contract end-to-end and
// is tier-agnostic (works on both the netns and Docker-network fabrics, since it
// probes via the task containers themselves): an ECS task's ENI
// privateIPv4Address is the container's REAL eth0 address, reachable from
// another task in the same VPC and isolated from a task in a different VPC.
func TestECSVPCNetworking(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	octetA := unusedDockerVPCOctet(t, 120, nil)
	octetB := unusedDockerVPCOctet(t, octetA+1, map[int]bool{octetA: true})
	vpcA, subnetA := mkVPCSubnet(t, q, vpcCIDR(octetA), subnetCIDR(octetA))
	vpcB, subnetB := mkVPCSubnet(t, q, vpcCIDR(octetB), subnetCIDR(octetB))
	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerTaskDef(q, "vpc-server", vpcServerScript)
	registerTaskDef(q, "vpc-client", "sleep 120")

	server := runTask(q, "vpc-server", subnetA)
	clientSame := runTask(q, "vpc-client", subnetA)
	clientOther := runTask(q, "vpc-client", subnetB)
	t.Cleanup(func() {
		for _, task := range []string{server, clientSame, clientOther} {
			runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task))
		}
		rmDockerNetworks(ecsVPCNet(vpcA), ecsVPCNet(vpcB), ecsVPCNet(vpcA)+"-egress", ecsVPCNet(vpcB)+"-egress")
	})
	waitRunning(t, q, server)
	waitRunning(t, q, clientSame)
	waitRunning(t, q, clientOther)

	// #516: the reported ENI IP is the container's real eth0 address.
	eniIP := taskENIIP(q, server)
	if !strings.HasPrefix(eniIP, vpcPrefix(octetA)) {
		t.Fatalf("server ENI IP not in subnet-A CIDR: %q", eniIP)
	}
	if real := taskEth0IP(t, server); real != eniIP {
		t.Fatalf("reported ENI IP %q != container's real eth0 IP %q (#516)", eniIP, real)
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
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	octet := unusedDockerVPCOctet(t, 150, nil)
	vpcID, subnetID := mkVPCSubnet(t, q, vpcCIDR(octet), subnetCIDR(octet))
	t.Cleanup(func() {
		q("ec2", "delete-subnet", "--subnet-id", subnetID)
		q("ec2", "delete-vpc", "--vpc-id", vpcID)
		rmDockerNetworks(ecsVPCNet(vpcID), ecsVPCNet(vpcID)+"-egress")
	})
	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	q("ecs", "register-task-definition", "--family", "ebs-vpc-server",
		"--network-mode", "awsvpc", "--requires-compatibilities", "FARGATE", "--cpu", "256", "--memory", "512",
		"--volumes", `[{"name":"workspace","configuredAtLaunch":true}]`,
		"--container-definitions", `[{"name":"app","image":"`+vpcNetBusybox+`","entryPoint":["sh","-c"],"command":["mkdir -p /workspace/www && echo ebs-ok > /workspace/www/index.html && httpd -f -p 80 -h /workspace/www"],"mountPoints":[{"sourceVolume":"workspace","containerPath":"/workspace"}]}]`,
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")
	registerTaskDef(q, "ebs-vpc-client", "sleep 120")

	server := q("ecs", "run-task",
		"--cluster", "default",
		"--task-definition", "ebs-vpc-server",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--volume-configurations", `[{"name":"workspace","managedEBSVolume":{"roleArn":"arn:aws:iam::123456789012:role/ecsInfrastructureRole","sizeInGiB":1,"volumeType":"gp3"}}]`,
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
	if real := taskEth0IP(t, server); real != eniIP {
		t.Fatalf("managed-EBS task reported ENI IP %q != real eth0 IP %q", eniIP, real)
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

// ---- shared helpers (tier-agnostic) ----

func mkVPCSubnet(t *testing.T, q func(...string) string, vpcCidr, snCidr string) (vpcID, subnetID string) {
	t.Helper()
	vpcID = q("ec2", "create-vpc", "--cidr-block", vpcCidr, "--query", "Vpc.VpcId", "--output", "text")
	subnetID = q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", snCidr,
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")
	return vpcID, subnetID
}

func vpcCIDR(octet int) string { return fmt.Sprintf("10.%d.0.0/16", octet) }

func subnetCIDR(octet int) string { return fmt.Sprintf("10.%d.0.0/24", octet) }

func vpcPrefix(octet int) string { return fmt.Sprintf("10.%d.0.", octet) }

func unusedDockerVPCOctet(t *testing.T, start int, exclude map[int]bool) int {
	t.Helper()
	for octet := start; octet < 250; octet++ {
		if exclude != nil && exclude[octet] {
			continue
		}
		name := fmt.Sprintf("sockerless-cidr-probe-%d-%d", os.Getpid(), octet)
		if exec.Command("docker", "network", "create", "--subnet", vpcCIDR(octet), name).Run() != nil {
			continue
		}
		_ = exec.Command("docker", "network", "rm", name).Run()
		return octet
	}
	t.Fatal("no free 10.x.0.0/16 Docker CIDR available for ECS VPC test")
	return 0
}

func registerTaskDef(q func(...string) string, family, script string) {
	q("ecs", "register-task-definition", "--family", family,
		"--network-mode", "awsvpc", "--requires-compatibilities", "FARGATE", "--cpu", "256", "--memory", "512",
		"--container-definitions", `[{"name":"app","image":"`+vpcNetBusybox+`","entryPoint":["sh","-c"],"command":["`+script+`"]}]`,
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

// taskEth0IP reads the container's real eth0 IPv4 (works in both tiers).
func taskEth0IP(t *testing.T, taskArn string) string {
	t.Helper()
	cid := taskContainerID(t, taskArn)
	// Retry: the netns veth is plumbed just as the task reaches RUNNING.
	for attempt := 0; attempt < 10; attempt++ {
		out, _ := exec.Command("docker", "exec", cid, "ip", "-4", "-o", "addr", "show", "eth0").CombinedOutput()
		for _, f := range strings.Fields(string(out)) {
			if strings.HasPrefix(f, "inet") {
				continue
			}
			if i := strings.Index(f, "/"); i > 0 && strings.Count(f[:i], ".") == 3 {
				return f[:i]
			}
		}
		time.Sleep(time.Second)
	}
	return ""
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

func hostPrimaryIPv4(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("ip", "route", "get", "1.1.1.1").CombinedOutput()
	if err != nil {
		t.Fatalf("ip route get host primary IPv4: %v\n%s", err, out)
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "src" && i+1 < len(fields) && net.ParseIP(fields[i+1]).To4() != nil {
			return fields[i+1]
		}
	}
	t.Fatalf("could not parse src IPv4 from ip route output: %s", out)
	return ""
}

func startHostProbeServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen for host egress probe: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("egress-ok\n"))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})
	return "http://" + net.JoinHostPort(hostPrimaryIPv4(t), strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)) + "/probe"
}

// ecsNetnsTierActive mirrors the sim's netns-tier gate (Linux + tools +
// CAP_NET_ADMIN/CAP_SYS_ADMIN) so tier-specific tests only run when ECS will
// actually use the real netns fabric.
func ecsNetnsTierActive() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	for _, bin := range []string{"ip", "nft", "nsenter", "sysctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			return false
		}
	}
	caps, err := linuxEffectiveCapabilitiesForTest()
	if err != nil {
		return false
	}
	return hasLinuxCapability(caps, 12) && hasLinuxCapability(caps, 21)
}

func linuxEffectiveCapabilitiesForTest() (uint64, error) {
	body, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return 0, fmt.Errorf("malformed CapEff line: %q", line)
		}
		return strconv.ParseUint(fields[1], 16, 64)
	}
	return 0, fmt.Errorf("CapEff not found")
}

func hasLinuxCapability(mask uint64, capNumber uint) bool {
	return mask&(uint64(1)<<capNumber) != 0
}

func waitRunning(t *testing.T, q func(...string) string, taskArn string) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		status := q("ecs", "describe-tasks", "--cluster", "default", "--tasks", taskArn,
			"--query", "tasks[0].lastStatus", "--output", "text")
		if status == "RUNNING" {
			return
		}
		if status == "STOPPED" {
			reason := q("ecs", "describe-tasks", "--cluster", "default", "--tasks", taskArn,
				"--query", "tasks[0].stoppedReason", "--output", "text")
			t.Fatalf("task %s stopped before RUNNING: %s", taskArn, reason)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("task %s never reached RUNNING", taskArn)
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

// rmDockerNetworks removes simulator VPC networks (Docker tier), retrying while
// task containers detach. No-op for names that don't exist (netns tier).
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

// TestECSVPCDeleteVpcAllowsCIDRReuse covers the fabric cleanup needed by a
// terraform destroy/apply cycle: once the task and subnet are gone, DeleteVpc
// removes the real backing fabric so a new VPC can reuse the same CIDR.
func TestECSVPCDeleteVpcAllowsCIDRReuse(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerTaskDef(q, "vpc-reuse-client", "sleep 120")

	octet := unusedDockerVPCOctet(t, 140, nil)
	vpc1, subnet1 := mkVPCSubnet(t, q, vpcCIDR(octet), subnetCIDR(octet))
	task1 := runTask(q, "vpc-reuse-client", subnet1)
	waitRunning(t, q, task1)
	runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task1))
	waitTaskContainersGone(t, task1)
	q("ec2", "delete-subnet", "--subnet-id", subnet1)
	q("ec2", "delete-vpc", "--vpc-id", vpc1)
	if exec.Command("docker", "network", "inspect", ecsVPCNet(vpc1)).Run() == nil {
		t.Fatalf("DeleteVpc left Docker VPC network %s behind", ecsVPCNet(vpc1))
	}

	vpc2, subnet2 := mkVPCSubnet(t, q, vpcCIDR(octet), subnetCIDR(octet))
	task2 := runTask(q, "vpc-reuse-client", subnet2)
	t.Cleanup(func() {
		_ = awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task2).Run()
		waitTaskContainersGone(t, task2)
		_ = awsCLI("ec2", "delete-subnet", "--subnet-id", subnet2).Run()
		_ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpc2).Run()
		rmDockerNetworks(ecsVPCNet(vpc2))
	})
	waitRunning(t, q, task2)
	if real := taskEth0IP(t, task2); !strings.HasPrefix(real, vpcPrefix(octet)) {
		t.Fatalf("recreated VPC task should run in reused CIDR, got eth0 %q", real)
	}
}

// TestECSVPCOverlappingCIDR proves the netns fabric does what Docker bridges
// can't: two VPCs with the SAME AWS CIDR (legal — VPCs are isolated) both run
// tasks that get the SAME real ENI IP, with no remapping and full isolation.
// Netns-tier only (the Docker-network tier can't host overlapping bridges).
func TestECSVPCOverlappingCIDR(t *testing.T) {
	if !ecsNetnsTierActive() {
		t.Skip("overlapping VPC CIDRs require the netns fabric (Linux + CAP_NET_ADMIN)")
	}
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	_, subnetA := mkVPCSubnet(t, q, "10.50.0.0/16", "10.50.0.0/24")
	_, subnetB := mkVPCSubnet(t, q, "10.50.0.0/16", "10.50.0.0/24") // same CIDR
	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerTaskDef(q, "ovl-server", vpcServerScript)
	registerTaskDef(q, "ovl-client", "sleep 120")

	serverA := runTask(q, "ovl-server", subnetA)
	clientA := runTask(q, "ovl-client", subnetA)
	clientB := runTask(q, "ovl-client", subnetB)
	t.Cleanup(func() {
		for _, task := range []string{serverA, clientA, clientB} {
			runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task))
		}
	})
	waitRunning(t, q, serverA)
	waitRunning(t, q, clientA)
	waitRunning(t, q, clientB)

	// Both VPCs keep the real CIDR — the server's ENI IP is its real eth0, and a
	// task in VPC-B legitimately gets the same address (separate routing tables).
	ip := taskENIIP(q, serverA)
	if !strings.HasPrefix(ip, "10.50.0.") {
		t.Fatalf("server should keep its real AWS CIDR (no remap): got %q", ip)
	}
	if real := taskEth0IP(t, serverA); real != ip {
		t.Fatalf("reported ENI IP %q != real eth0 IP %q", ip, real)
	}

	// Same-VPC reaches the server; the same-CIDR VPC-B task is fully isolated.
	if code, out := taskWget(t, clientA, ip); code != 0 || !strings.Contains(out, "ok") {
		t.Fatalf("same-VPC task should reach %s: exit=%d out=%q", ip, code, out)
	}
	if code, _ := taskWget(t, clientB, ip); code == 0 {
		t.Fatalf("overlapping-CIDR VPC-B task must be isolated from VPC-A's %s", ip)
	}
}

func TestECSVPCNetnsTaskMetadataLinkLocal(t *testing.T) {
	if !ecsNetnsTierActive() {
		t.Skip("link-local task metadata DNAT requires the netns fabric (Linux + CAP_NET_ADMIN)")
	}
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	_, subnet := mkVPCSubnet(t, q, "10.62.0.0/16", "10.62.0.0/24")
	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerTaskDef(q, "metadata-netns-client", "sleep 120")
	task := runTask(q, "metadata-netns-client", subnet)
	t.Cleanup(func() {
		runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task))
	})
	waitRunning(t, q, task)

	cid := taskContainerID(t, task)
	out, err := exec.Command("docker", "exec", cid, "sh", "-c", `printf '%s\n' "$ECS_CONTAINER_METADATA_URI_V4"; wget -T 3 -q -O - "$ECS_CONTAINER_METADATA_URI_V4/task"`).CombinedOutput()
	if err != nil {
		t.Fatalf("task metadata link-local fetch failed: %v\n%s", err, out)
	}
	lines := strings.SplitN(string(out), "\n", 2)
	if len(lines) != 2 {
		t.Fatalf("metadata probe output missing URI/body split: %q", out)
	}
	if !strings.HasPrefix(lines[0], "http://169.254.170.2/v4/") {
		t.Fatalf("ECS_CONTAINER_METADATA_URI_V4 = %q, want Fargate link-local URI", lines[0])
	}
	if !strings.Contains(lines[1], `"Family":"metadata-netns-client"`) || !strings.Contains(lines[1], `"TaskARN":"`+task+`"`) {
		t.Fatalf("task metadata body did not contain real task identity:\n%s", lines[1])
	}
}

func TestECSVPCNetnsRouteTableEgress(t *testing.T) {
	if !ecsNetnsTierActive() {
		t.Skip("route-table egress enforcement requires the netns fabric (Linux + CAP_NET_ADMIN)")
	}
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	probeURL := startHostProbeServer(t)

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.63.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	isolatedSubnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.63.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")
	publicSubnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.63.2.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")
	privateSubnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.63.3.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")
	igw := q("ec2", "create-internet-gateway", "--query", "InternetGateway.InternetGatewayId", "--output", "text")
	q("ec2", "attach-internet-gateway", "--internet-gateway-id", igw, "--vpc-id", vpc)
	publicRT := q("ec2", "create-route-table", "--vpc-id", vpc, "--query", "RouteTable.RouteTableId", "--output", "text")
	q("ec2", "create-route", "--route-table-id", publicRT, "--destination-cidr-block", "0.0.0.0/0", "--gateway-id", igw)
	q("ec2", "associate-route-table", "--route-table-id", publicRT, "--subnet-id", publicSubnet)
	eipAlloc := q("ec2", "allocate-address", "--domain", "vpc", "--query", "AllocationId", "--output", "text")
	nat := q("ec2", "create-nat-gateway", "--subnet-id", publicSubnet, "--allocation-id", eipAlloc,
		"--query", "NatGateway.NatGatewayId", "--output", "text")
	privateRT := q("ec2", "create-route-table", "--vpc-id", vpc, "--query", "RouteTable.RouteTableId", "--output", "text")
	q("ec2", "create-route", "--route-table-id", privateRT, "--destination-cidr-block", "0.0.0.0/0", "--nat-gateway-id", nat)
	q("ec2", "associate-route-table", "--route-table-id", privateRT, "--subnet-id", privateSubnet)

	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerTaskDef(q, "egress-netns-client", "sleep 120")
	isolatedTask := runTask(q, "egress-netns-client", isolatedSubnet)
	publicTask := runTaskWithNetworkConfiguration(q, "egress-netns-client", `awsvpcConfiguration={subnets=[`+publicSubnet+`],assignPublicIp=ENABLED}`)
	privateTask := runTask(q, "egress-netns-client", privateSubnet)
	t.Cleanup(func() {
		for _, task := range []string{isolatedTask, publicTask, privateTask} {
			runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task))
		}
	})
	waitRunning(t, q, isolatedTask)
	waitRunning(t, q, publicTask)
	waitRunning(t, q, privateTask)

	if code, out := taskWgetURL(t, isolatedTask, probeURL); code == 0 {
		t.Fatalf("isolated subnet task unexpectedly reached host egress probe: exit=%d out=%q", code, out)
	}
	if code, out := taskWgetURL(t, publicTask, probeURL); code != 0 || !strings.Contains(out, "egress-ok") {
		t.Fatalf("public subnet task with assignPublicIp should reach egress probe: exit=%d out=%q", code, out)
	}
	if code, out := taskWgetURL(t, privateTask, probeURL); code != 0 || !strings.Contains(out, "egress-ok") {
		t.Fatalf("private subnet task with NAT route should reach egress probe: exit=%d out=%q", code, out)
	}
}

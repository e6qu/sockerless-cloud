package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// AWS host-metadata services.
// Two distinct metadata layers exist in real AWS:
//
//  1. **EC2 IMDSv2** — `169.254.169.254/latest/meta-data/...` for any
//     EC2-style host (EC2 instance, Fargate task on EC2 launch type).
//     IMDSv2 requires a session token from PUT /latest/api/token.
//
//  2. **ECS task metadata v4** — `${ECS_CONTAINER_METADATA_URI_V4}/task`
//     served per-container on a task-local endpoint. Carries cluster,
//     task ARN, family, container statuses, network interfaces, limits.
//
// Lambda Runtime API (lambda_runtime.go) is its own thing and stays as-is.
//
// Workloads in the sim's Docker hosts reach these endpoints via the
// sim's main listener: cloud-product translators inject
// AWS_EC2_METADATA_SERVICE_ENDPOINT and ECS_CONTAINER_METADATA_URI_V4
// env vars on the workload host so the AWS Go/Python/JS SDKs route
// metadata reads to the sim's port.

// imdsTokens holds active IMDSv2 session tokens. Real AWS scopes them
// per-instance + per-TTL; the sim accepts any presented token that was
// previously issued, mirroring the API contract without enforcing the
// per-instance binding.
var (
	imdsTokens        sync.Map // map[string]time.Time (issued-at)
	imdsInstancesByIP sync.Map // map[string]EC2Instance
)

func registerHostMetadata(srv *sim.Server) {
	// PUT /latest/api/token — IMDSv2 token request.
	srv.HandleFunc("PUT /latest/api/token", func(w http.ResponseWriter, r *http.Request) {
		ttl := r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds")
		if ttl == "" {
			ttl = "21600"
		}
		buf := make([]byte, 28)
		_, _ = rand.Read(buf)
		token := "AQ" + hex.EncodeToString(buf)
		imdsTokens.Store(token, time.Now())
		w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", ttl)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(token))
	})

	mustToken := func(w http.ResponseWriter, r *http.Request) bool {
		token := r.Header.Get("X-aws-ec2-metadata-token")
		if token == "" {
			http.Error(w, "IMDSv2 requires X-aws-ec2-metadata-token", http.StatusUnauthorized)
			return false
		}
		if _, ok := imdsTokens.Load(token); !ok {
			http.Error(w, "unknown IMDSv2 token", http.StatusUnauthorized)
			return false
		}
		return true
	}
	writeText := func(w http.ResponseWriter, s string) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(s))
	}

	// /latest/meta-data/ — top-level index. Real IMDS returns a
	// newline-separated leaf list; minimal coverage here.
	srv.HandleFunc("GET /latest/meta-data/", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		writeText(w, "ami-id\ninstance-id\ninstance-type\nplacement/\niam/\nidentity-credentials/\n")
	})

	srv.HandleFunc("GET /latest/meta-data/instance-id", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		if inst, ok := imdsInstanceForRequest(r); ok {
			writeText(w, inst.InstanceId)
			return
		}
		writeText(w, "i-0abcdef1234567890")
	})
	srv.HandleFunc("GET /latest/meta-data/instance-type", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		if inst, ok := imdsInstanceForRequest(r); ok && inst.InstanceType != "" {
			writeText(w, inst.InstanceType)
			return
		}
		writeText(w, "t3.micro")
	})
	srv.HandleFunc("GET /latest/meta-data/ami-id", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		if inst, ok := imdsInstanceForRequest(r); ok && inst.ImageId != "" {
			writeText(w, inst.ImageId)
			return
		}
		writeText(w, "ami-0123456789abcdef0")
	})
	srv.HandleFunc("GET /latest/meta-data/placement/region", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		writeText(w, defaultIMDSRegion(r))
	})
	srv.HandleFunc("GET /latest/meta-data/placement/availability-zone", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		writeText(w, defaultIMDSRegion(r)+"a")
	})

	// IAM credentials at /latest/meta-data/iam/security-credentials/{role}.
	// Real EC2 returns a JSON document with AccessKeyId, SecretAccessKey,
	// Token, Expiration. Sim returns a stable sim-AK/SK pair.
	srv.HandleFunc("GET /latest/meta-data/iam/security-credentials/", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		writeText(w, "sim-instance-role")
	})
	srv.HandleFunc("GET /latest/meta-data/iam/security-credentials/{role}", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"Code":"Success","LastUpdated":%q,"Type":"AWS-HMAC","AccessKeyId":"ASIASIMACCESSKEY","SecretAccessKey":"sim-secret-access-key","Token":"sim-session-token","Expiration":%q}`,
			time.Now().UTC().Format(time.RFC3339), exp)
	})

	// /latest/dynamic/instance-identity/document — signed JSON document
	// describing the instance. The AWS Go SDK's `imds.GetRegion()` reads
	// this rather than /latest/meta-data/placement/region. Real EC2
	// includes a base64 signature; sim omits the signature (workloads
	// running in the sim don't validate it).
	srv.HandleFunc("GET /latest/dynamic/instance-identity/document", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		region := defaultIMDSRegion(r)
		instanceID := "i-0abcdef1234567890"
		instanceType := "t3.micro"
		imageID := "ami-0123456789abcdef0"
		arch := "x86_64"
		if inst, ok := imdsInstanceForRequest(r); ok {
			instanceID = inst.InstanceId
			instanceType = inst.InstanceType
			imageID = inst.ImageId
			if inst.Architecture != "" {
				arch = inst.Architecture
			}
		}
		_, _ = fmt.Fprintf(w, `{
			"accountId": "000000000000",
			"architecture": %q,
			"availabilityZone": %q,
			"imageId": %q,
			"instanceId": %q,
			"instanceType": %q,
			"region": %q,
			"version": "2017-09-30"
		}`, arch, region+"a", imageID, instanceID, instanceType, region)
	})

	// ECS task metadata v4. Real ECS sets ECS_CONTAINER_METADATA_URI_V4
	// to a per-task local URL like http://169.254.170.2/v4/<id>. Docker-tier
	// tasks reach the same handler through the host callback address; netns-tier
	// tasks reach it through link-local DNAT in the VPC namespace.
	srv.HandleFunc("GET /v4/{id}/task", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "id")
		w.Header().Set("Content-Type", "application/json")
		taskMetadata, ok := ecsTaskMetadataV4(id)
		if !ok {
			http.Error(w, "ECS task metadata not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(taskMetadata)
	})
	srv.HandleFunc("GET /v4/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "id")
		w.Header().Set("Content-Type", "application/json")
		containerMetadata, ok := ecsContainerMetadataV4(id)
		if !ok {
			http.Error(w, "ECS container metadata not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(containerMetadata)
	})
	srv.HandleFunc("GET /v4/{id}/credentials", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "id")
		taskRoleArn := ecsTaskRoleArn(id)
		if taskRoleArn == "" {
			http.Error(w, "Amazon ECS task role not found", http.StatusNotFound)
			return
		}
		roleName := iamRoleNameFromArn(taskRoleArn)
		role, ok := iamRoles.Get(roleName)
		if !ok {
			http.Error(w, "Amazon ECS task role not found", http.StatusNotFound)
			return
		}

		accessKeyID, secretAccessKey, token := stsMintTempCred()
		expiration := time.Now().UTC().Add(time.Hour)
		principalArn := fmt.Sprintf(
			"arn:aws:sts::%s:assumed-role/%s/%s",
			awsAccountID(), role.RoleName, id,
		)
		iamTempCreds.Put(accessKeyID, IAMTempCred{
			AccessKeyID:     accessKeyID,
			SecretAccessKey: secretAccessKey,
			SessionToken:    token,
			RoleName:        role.RoleName,
			PrincipalArn:    principalArn,
			Expiration:      expiration.Format(time.RFC3339),
			CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"RoleArn":         taskRoleArn,
			"AccessKeyId":     accessKeyID,
			"SecretAccessKey": secretAccessKey,
			"Token":           token,
			"Expiration":      expiration.Format(time.RFC3339),
		})
	})
}

func ecsTaskRoleArn(taskID string) string {
	task, ok := ecsTasks.Get(taskID)
	if !ok {
		return ""
	}
	definition, ok := ecsTaskDefinitionForARN(task.TaskDefinitionArn)
	if !ok {
		return ""
	}
	return definition.TaskRoleArn
}

func imdsInstanceForRequest(r *http.Request) (EC2Instance, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	v, ok := imdsInstancesByIP.Load(host)
	if !ok {
		return EC2Instance{}, false
	}
	inst, ok := v.(EC2Instance)
	return inst, ok
}

func defaultIMDSRegion(r *http.Request) string {
	if v := r.URL.Query().Get("region"); v != "" {
		return v
	}
	return "eu-west-1"
}

// simHostMetadataAddr returns the address workload containers use to
// reach the sim's metadata services.
var simListenAddr string

func simHostMetadataAddr() (string, error) {
	port := simListenAddr
	if idx := strings.LastIndex(simListenAddr, ":"); idx >= 0 {
		port = simListenAddr[idx+1:]
	}
	host, err := workloadCallbackHost()
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, port), nil
}

func simHostMetadataPort() (int, error) {
	port := simListenAddr
	if idx := strings.LastIndex(simListenAddr, ":"); idx >= 0 {
		port = simListenAddr[idx+1:]
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("invalid simulator metadata listen port %q", port)
	}
	return n, nil
}

// hostMetadataExtraHosts returns ExtraHosts entries needed for the
// workload to resolve host.docker.internal through the same outer-host
// coordinate visible to a containerized simulator. Docker and Podman can use
// different networks for the simulator and its nested workload containers, so
// the simulator's default route is only a fallback when neither standard host
// alias exists. The AWS SDK respects AWS_EC2_METADATA_SERVICE_ENDPOINT, so
// workloads that go through the SDK don't need the link-local hostname;
// ExtraHosts is best-effort for raw HTTP clients.
func hostMetadataExtraHosts() []string {
	if entries := hostMetadataHostEntries(); len(entries) > 0 {
		out := make([]string, 0, len(entries))
		for _, entry := range entries {
			out = append(out, entry.Name+":"+entry.IP)
		}
		return out
	}
	info := strings.ToLower(sim.RuntimeInfo())
	if strings.Contains(info, "podman") {
		return nil
	}
	return []string{"host.docker.internal:host-gateway"}
}

func hostMetadataHostEntries() []sim.HostEntry {
	if !runningInsideContainer() {
		return nil
	}
	gateway := workloadHostGatewayIPv4(net.LookupHost, defaultRouteGatewayIPv4)
	if gateway == "" {
		return nil
	}
	return []sim.HostEntry{
		{IP: gateway, Name: "host.docker.internal"},
		{IP: gateway, Name: "host.containers.internal"},
	}
}

func rewriteHostDockerInternalEnv(env map[string]string) map[string]string {
	containerized := runningInsideContainer()
	if !containerized {
		return env
	}
	gateway := workloadHostGatewayIPv4(net.LookupHost, defaultRouteGatewayIPv4)
	return rewriteHostDockerInternalEnvForRuntime(env, containerized, gateway)
}

func rewriteHostDockerInternalEnvForRuntime(env map[string]string, containerized bool, gateway string) map[string]string {
	if !containerized || gateway == "" {
		return env
	}
	return rewriteHostDockerInternalEnvWithGateway(env, gateway)
}

func workloadHostGatewayIPv4(lookup func(string) ([]string, error), fallback func() string) string {
	for _, hostname := range []string{"host.docker.internal", "host.containers.internal"} {
		addresses, err := lookup(hostname)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip := net.ParseIP(strings.TrimSpace(address))
			if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() {
				continue
			}
			return ip.To4().String()
		}
	}
	return fallback()
}

func rewriteHostDockerInternalEnvWithGateway(env map[string]string, gateway string) map[string]string {
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = strings.ReplaceAll(value, "host.docker.internal", gateway)
	}
	return out
}

// rewriteSimulatorEndpointForRealVPC maps the outer-host coordinate used by
// cross-platform task definitions onto the task-local link address that the
// real VPC network tier DNATs to the simulator listener. A real-VPC task has no
// route to Docker's host gateway (and a private subnet should not gain one),
// while 169.254.170.2 is already the managed ECS task-metadata route into the
// same listener. Only this simulator listener authority is rewritten; other
// outer-host services remain unreachable from the isolated VPC.
func rewriteSimulatorEndpointForRealVPC(env map[string]string, simulatorPort int) map[string]string {
	out := make(map[string]string, len(env))
	target := "http://" + realexec.ECSTaskMetadataIPv4
	for key, value := range env {
		for _, hostname := range []string{"host.docker.internal", "host.containers.internal"} {
			source := fmt.Sprintf("http://%s:%d", hostname, simulatorPort)
			value = strings.ReplaceAll(value, source, target)
		}
		out[key] = value
	}
	return out
}

func defaultRouteGatewayIPv4() string {
	content, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	return parseDefaultRouteGatewayIPv4(string(content))
}

func parseDefaultRouteGatewayIPv4(content string) string {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		gateway, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil || gateway == 0 {
			continue
		}
		return net.IPv4(
			byte(gateway),
			byte(gateway>>8),
			byte(gateway>>16),
			byte(gateway>>24),
		).String()
	}
	return ""
}

// hostMetadataEnv returns env vars for every AWS workload host so SDKs
// route metadata reads to the sim. Cloud-product translators merge
// these onto the workload's ContainerConfig.Env.
//
// The taskID parameter is the sim-side container ID used as the
// ECS_CONTAINER_METADATA_URI_V4 token. For non-ECS hosts (Lambda)
// pass empty and the env var is omitted.
func hostMetadataEnv(taskID string) (map[string]string, error) {
	addr, err := simHostMetadataAddr()
	if err != nil {
		return nil, err
	}
	env := map[string]string{
		"AWS_EC2_METADATA_SERVICE_ENDPOINT":      "http://" + addr + "/",
		"AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE": "IPv4",
	}
	if taskID != "" {
		env["ECS_CONTAINER_METADATA_URI_V4"] = "http://" + addr + "/v4/" + taskID
		env["ECS_CONTAINER_METADATA_URI"] = "http://" + addr + "/v4/" + taskID
		if ecsTaskRoleArn(taskID) != "" {
			env["AWS_CONTAINER_CREDENTIALS_FULL_URI"] = "http://" + addr + "/v4/" + taskID + "/credentials"
		}
	}
	return env, nil
}

func hostMetadataLinkLocalEnv(taskID string) map[string]string {
	env := map[string]string{
		"AWS_EC2_METADATA_SERVICE_ENDPOINT":      "http://" + realexec.MetadataIPv4 + "/",
		"AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE": "IPv4",
	}
	if taskID != "" {
		base := "http://" + realexec.ECSTaskMetadataIPv4 + "/v4/" + taskID
		env["ECS_CONTAINER_METADATA_URI_V4"] = base
		env["ECS_CONTAINER_METADATA_URI"] = base
		if ecsTaskRoleArn(taskID) != "" {
			env["AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"] = "/v4/" + taskID + "/credentials"
		}
	}
	return env
}

// mergeEnv returns a new map with all keys from `base` and `extra`,
// where `extra` wins on conflict. Both inputs may be nil.
func mergeEnv(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

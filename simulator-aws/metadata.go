package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
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
	imdsInstancesByIP workloadhost.MetadataIndex[EC2Instance]
)

func registerHostMetadata(srv *sim.Server) {
	// A NAT gateway's egress sidecar reports its connections here, under the
	// token the simulator minted for it.
	srv.HandleFunc("POST "+egress.ReportPath, sidecar.EgressReports(ec2TakeNatEgressReport))
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
		writeText(w, "ami-id\nhostname\ninstance-id\ninstance-type\nlocal-hostname\nlocal-ipv4\nplacement/\niam/\nidentity-credentials/\n")
	})

	srv.HandleFunc("GET /latest/meta-data/instance-id", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		if inst, ok := imdsInstancesByIP.ForRequest(r); ok {
			writeText(w, inst.InstanceId)
			return
		}
		writeText(w, "i-0abcdef1234567890")
	})
	srv.HandleFunc("GET /latest/meta-data/instance-type", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		if inst, ok := imdsInstancesByIP.ForRequest(r); ok && inst.InstanceType != "" {
			writeText(w, inst.InstanceType)
			return
		}
		writeText(w, "t3.micro")
	})
	srv.HandleFunc("GET /latest/meta-data/ami-id", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		if inst, ok := imdsInstancesByIP.ForRequest(r); ok && inst.ImageId != "" {
			writeText(w, inst.ImageId)
			return
		}
		writeText(w, "ami-0123456789abcdef0")
	})
	srv.HandleFunc("GET /latest/meta-data/local-ipv4", func(w http.ResponseWriter, r *http.Request) {
		if !mustToken(w, r) {
			return
		}
		if inst, ok := imdsInstancesByIP.ForRequest(r); ok {
			writeText(w, inst.PrivateIpAddress)
			return
		}
		http.NotFound(w, r)
	})
	for _, leaf := range []string{"hostname", "local-hostname"} {
		srv.HandleFunc("GET /latest/meta-data/"+leaf, func(w http.ResponseWriter, r *http.Request) {
			if !mustToken(w, r) {
				return
			}
			if inst, ok := imdsInstancesByIP.ForRequest(r); ok {
				writeText(w, ec2PrivateDNSHostname(inst.PrivateIpAddress)+"."+awsRegion()+".compute.internal")
				return
			}
			http.NotFound(w, r)
		})
	}
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
		if inst, ok := imdsInstancesByIP.ForRequest(r); ok {
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
	// to a per-task local URL like http://169.254.170.2/v4/<id>; tasks reach
	// the same handler through the host callback address.
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
		if !ecsTaskOwnsMetadataRequest(id, r) {
			http.Error(w, "ECS task metadata not found", http.StatusNotFound)
			return
		}
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

		cred := ecsTaskCredential(id, role.RoleName, time.Now().UTC())

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"RoleArn":         taskRoleArn,
			"AccessKeyId":     cred.AccessKeyID,
			"SecretAccessKey": cred.SecretAccessKey,
			"Token":           cred.SessionToken,
			"Expiration":      cred.Expiration,
		})
	})
}

// ecsTaskOwnsMetadataRequest reports whether a request that arrived through a
// network sidecar comes from the awsvpc task whose credentials it asks for:
// the task's own VPC network and its elastic network interface address. The
// task metadata endpoint is task-local on Amazon ECS, so another task's
// containers find nothing there. A request from the simulator's own listener,
// or for a task without an interface of its own, is answered as before.
func ecsTaskOwnsMetadataRequest(taskID string, r *http.Request) bool {
	network := sim.SidecarNetwork(r.Context())
	if network == "" {
		return true
	}
	eniIP, subnetID, ok := ecsTaskENIInfo(taskID)
	if !ok {
		return true
	}
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return network == ecsVPCNetworkName(subnet.VpcId) && host == eniIP
}

const (
	ecsTaskCredentialLifetime = time.Hour
	// ecsTaskCredentialMinRemaining is how much of a held credential's
	// lifetime must remain for the endpoint to keep serving it. The Amazon ECS
	// agent serves a task one credential until it refreshes it ahead of
	// expiry, and SDKs refresh their own copy minutes before expiry; half the
	// lifetime clears every SDK's window.
	ecsTaskCredentialMinRemaining = ecsTaskCredentialLifetime / 2
)

// ecsHeldCredential is the credential the endpoint currently serves a task.
type ecsHeldCredential struct {
	TaskID      string
	AccessKeyID string
}

var (
	ecsTaskCredentials  sim.Store[ecsHeldCredential]
	ecsTaskCredentialMu sync.Mutex
)

// ecsTaskCredential returns the credential a task's role credentials endpoint
// serves at now, minting a new one only when the task holds none for roleName
// or the one it holds is too close to expiry.
func ecsTaskCredential(taskID, roleName string, now time.Time) IAMTempCred {
	ecsTaskCredentialMu.Lock()
	defer ecsTaskCredentialMu.Unlock()
	if held, ok := ecsTaskCredentials.Get(taskID); ok {
		if cred, ok := iamTempCreds.Get(held.AccessKeyID); ok && cred.RoleName == roleName {
			if exp, err := time.Parse(time.RFC3339, cred.Expiration); err == nil && exp.Sub(now) > ecsTaskCredentialMinRemaining {
				return cred
			}
		}
	}
	expiration := now.Add(ecsTaskCredentialLifetime)
	accessKeyID, secretAccessKey, token := stsMintTempCred(expiration)
	cred := IAMTempCred{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		SessionToken:    token,
		RoleName:        roleName,
		PrincipalArn:    fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/%s", awsAccountID(), roleName, taskID),
		Expiration:      expiration.Format(time.RFC3339),
		CreatedAt:       now.Format(time.RFC3339),
	}
	iamTempCreds.Put(accessKeyID, cred)
	ecsTaskCredentials.Put(taskID, ecsHeldCredential{TaskID: taskID, AccessKeyID: accessKeyID})
	return cred
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

func defaultIMDSRegion(r *http.Request) string {
	if v := r.URL.Query().Get("region"); v != "" {
		return v
	}
	return "eu-west-1"
}

// simListenAddr is the listen address main() serves on.
var simListenAddr string

func rewriteHostDockerInternalEnv(env map[string]string) map[string]string {
	containerized := workloadhost.InContainer()
	if !containerized {
		return env
	}
	gateway := workloadhost.OuterHostGatewayIPv4()
	return rewriteHostDockerInternalEnvForRuntime(env, containerized, gateway)
}

func rewriteHostDockerInternalEnvForRuntime(env map[string]string, containerized bool, gateway string) map[string]string {
	if !containerized || gateway == "" {
		return env
	}
	return rewriteHostDockerInternalEnvWithGateway(env, gateway)
}

func rewriteHostDockerInternalEnvWithGateway(env map[string]string, gateway string) map[string]string {
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = strings.ReplaceAll(value, "host.docker.internal", gateway)
	}
	return out
}

// workloadMetadataEnv is the environment that routes a workload's metadata
// reads to origin, the sidecar of its network, which forwards them to the
// simulator with the workload's address. taskID names the Amazon ECS task
// whose metadata and credentials the workload reads; empty for every other
// workload.
func workloadMetadataEnv(origin, taskID string) map[string]string {
	env := map[string]string{
		"AWS_EC2_METADATA_SERVICE_ENDPOINT":      origin + "/",
		"AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE": "IPv4",
	}
	if taskID != "" {
		env["ECS_CONTAINER_METADATA_URI_V4"] = origin + "/v4/" + taskID
		env["ECS_CONTAINER_METADATA_URI"] = origin + "/v4/" + taskID
		if ecsTaskRoleArn(taskID) != "" {
			env["AWS_CONTAINER_CREDENTIALS_FULL_URI"] = origin + "/v4/" + taskID + "/credentials"
		}
	}
	return env
}

// hostNetworkMetadataEnv is workloadMetadataEnv for a workload that shares
// the container instance's network stack: it has no sidecar, so it reaches
// the simulator's listener directly.
func hostNetworkMetadataEnv(taskID string) (map[string]string, error) {
	addr, err := workloadhost.CallbackAddr(simListenAddr)
	if err != nil {
		return nil, err
	}
	return workloadMetadataEnv("http://"+addr, taskID), nil
}

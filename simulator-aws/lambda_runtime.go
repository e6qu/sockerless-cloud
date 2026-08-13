package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// lambdaInvocation represents a single pending Lambda invocation served
// over the Runtime API. Matches the real Lambda contract: a unique
// request ID, the payload the handler polls via /next, a deadline, and
// channels for the handler's /response or /error reply.
type lambdaInvocation struct {
	RequestID   string
	FunctionArn string
	Payload     []byte
	DeadlineMs  int64
	TraceID     string

	// Single-slot queue: /next reads once; /response or /error writes once.
	delivered bool
	mu        sync.Mutex

	done     chan struct{} // closed when response or error received
	response []byte
	errorObj []byte // JSON error payload when /error was called
}

// runtimeAPISidecar is a per-invocation HTTP server that implements the
// AWS Lambda Runtime API for one container. Matches real Lambda where
// each running function container has its own dedicated Runtime API on
// 127.0.0.1:9001; in the simulator it runs on the host and the
// container reaches it through the runtime's workload callback address.
type runtimeAPISidecar struct {
	inv      *lambdaInvocation
	listener net.Listener
	server   *http.Server
	addr     string // host:port address the container sees
	port     int
}

// startRuntimeAPISidecar binds a free port on all interfaces, mounts
// the Runtime API routes, and starts serving in a background
// goroutine. Must bind to 0.0.0.0 (not 127.0.0.1) because the
// function container reaches back via the container runtime's bridge gateway
// on Linux, which is a different interface than loopback. Other platforms use
// their standard host callback name. Returns the sidecar so the caller can
// pass its address into the container and shut the sidecar down after the
// invocation completes.
func startRuntimeAPISidecar(inv *lambdaInvocation) (*runtimeAPISidecar, error) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("runtime API listen: %w", err)
	}
	host, err := runtimeAPIHost()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, fmt.Errorf("runtime API listen: unexpected address type %T", ln.Addr())
	}
	port := addr.Port

	mux := http.NewServeMux()
	s := &runtimeAPISidecar{
		inv:      inv,
		listener: ln,
		addr:     net.JoinHostPort(host, fmt.Sprintf("%d", port)),
		port:     port,
	}

	// GET /2018-06-01/runtime/invocation/next
	mux.HandleFunc("GET /2018-06-01/runtime/invocation/next", s.handleNext)
	// POST /2018-06-01/runtime/invocation/{id}/response
	mux.HandleFunc("POST /2018-06-01/runtime/invocation/{id}/response", s.handleResponse)
	// POST /2018-06-01/runtime/invocation/{id}/error
	mux.HandleFunc("POST /2018-06-01/runtime/invocation/{id}/error", s.handleInvocationError)
	// POST /2018-06-01/runtime/init/error
	mux.HandleFunc("POST /2018-06-01/runtime/init/error", s.handleInitError)

	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  0, // /next is long-poll; no timeout
		WriteTimeout: 0,
	}

	go func() {
		_ = s.server.Serve(ln)
	}()

	return s, nil
}

// Shutdown closes the sidecar listener after the invocation completes.
func (s *runtimeAPISidecar) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
}

// ContainerAddr returns the address the container should use to reach
// this sidecar (via the Docker host-gateway alias).
func (s *runtimeAPISidecar) ContainerAddr() string {
	return s.addr
}

func (s *runtimeAPISidecar) HostPort() int {
	return s.port
}

// handleNext serves GET /2018-06-01/runtime/invocation/next. Blocks
// until the invocation payload is ready (with this design it's already
// queued when the container starts, so it returns immediately the first
// time and hangs on subsequent calls until the server is shut down).
func (s *runtimeAPISidecar) handleNext(w http.ResponseWriter, r *http.Request) {
	s.inv.mu.Lock()
	if s.inv.delivered {
		s.inv.mu.Unlock()
		// Hold the connection open until the sidecar shuts down — real
		// Lambda blocks /next until the next invocation arrives. Our
		// per-invocation sidecar only serves one, so further polls wait
		// for shutdown.
		<-r.Context().Done()
		return
	}
	s.inv.delivered = true
	s.inv.mu.Unlock()

	w.Header().Set("Lambda-Runtime-Aws-Request-Id", s.inv.RequestID)
	w.Header().Set("Lambda-Runtime-Invoked-Function-Arn", s.inv.FunctionArn)
	w.Header().Set("Lambda-Runtime-Deadline-Ms", fmt.Sprintf("%d", s.inv.DeadlineMs))
	if s.inv.TraceID != "" {
		w.Header().Set("Lambda-Runtime-Trace-Id", s.inv.TraceID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.inv.Payload)
}

// handleResponse serves POST /2018-06-01/runtime/invocation/{id}/response.
func (s *runtimeAPISidecar) handleResponse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != s.inv.RequestID {
		sim.AWSErrorf(w, "InvalidRequestID", http.StatusBadRequest,
			"Invocation with id %s doesn't exist", id)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sim.AWSError(w, "InvalidRequestBody", "Failed to read response body", http.StatusBadRequest)
		return
	}
	s.inv.response = body
	select {
	case <-s.inv.done:
		// Already signaled (e.g. duplicate response); ignore.
	default:
		close(s.inv.done)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}

// handleInvocationError serves POST /2018-06-01/runtime/invocation/{id}/error.
func (s *runtimeAPISidecar) handleInvocationError(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != s.inv.RequestID {
		sim.AWSErrorf(w, "InvalidRequestID", http.StatusBadRequest,
			"Invocation with id %s doesn't exist", id)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sim.AWSError(w, "InvalidRequestBody", "Failed to read error body", http.StatusBadRequest)
		return
	}
	s.inv.errorObj = body
	select {
	case <-s.inv.done:
	default:
		close(s.inv.done)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}

// handleInitError serves POST /2018-06-01/runtime/init/error.
func (s *runtimeAPISidecar) handleInitError(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sim.AWSError(w, "InvalidRequestBody", "Failed to read error body", http.StatusBadRequest)
		return
	}
	s.inv.errorObj = body
	select {
	case <-s.inv.done:
	default:
		close(s.inv.done)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}

// runtimeAPIHost returns the coordinate the function container uses to reach
// the simulator host. Linux uses the runtime-reported bridge gateway; desktop
// runtimes use their standard host callback name.
func runtimeAPIHost() (string, error) {
	if v := os.Getenv("SIM_LAMBDA_RUNTIME_HOST"); v != "" {
		return v, nil
	}
	return workloadCallbackHost()
}

// runtimeAPIExtraHosts returns the Docker --add-host entries needed
// for the container to resolve host.docker.internal. Podman 4+ and
// Docker Desktop expose it natively; Linux Docker needs the magic
// `host-gateway` replacement. Podman doesn't support that magic value
// and will error if passed, so we skip ExtraHosts on Podman.
func runtimeAPIExtraHosts() []string {
	info := strings.ToLower(sim.RuntimeInfo())
	if strings.Contains(info, "podman") {
		// Podman already exposes host.docker.internal + host.containers.internal.
		return nil
	}
	return []string{"host.docker.internal:host-gateway"}
}

var (
	lambdaRuntimeAPIAddressSequence atomic.Uint32
	lambdaRuntimeIPMu               sync.Mutex
	lambdaRuntimeIPLeases           = map[string]bool{}
)

type lambdaInvocationNetwork struct {
	networkMode string
	network     string
	// eniAddress is the invocation's elastic network interface IPv4 address in
	// CIDR notation with the VPC CIDR's prefix length, plumbed as the
	// container's secondary address (sim.ContainerConfig.ENIAddress).
	eniAddress     string
	runtimeAPIAddr string
	metadataEnv    map[string]string
	extraHosts     []string
	pause          *sim.ContainerHandle
	invocationID   string
	leasedIP       string
}

func (n *lambdaInvocationNetwork) cleanup() {
	if n == nil {
		return
	}
	if n.pause != nil {
		n.pause.Cancel()
		_ = n.pause.Wait()
		ec2DetachRealLambdaNIC(context.Background(), n.invocationID)
	}
	if n.leasedIP != "" {
		lambdaRuntimeIPMu.Lock()
		delete(lambdaRuntimeIPLeases, n.leasedIP)
		lambdaRuntimeIPMu.Unlock()
	}
}

func acquireLambdaRuntimeIP(vpc *LambdaVpcConfig) (subnetID, privateIP string, err error) {
	if vpc == nil || len(vpc.SubnetIds) == 0 {
		return "", "", fmt.Errorf("AWS Lambda VPC configuration has no subnet")
	}
	lambdaRuntimeIPMu.Lock()
	defer lambdaRuntimeIPMu.Unlock()
	for i, subnetID := range vpc.SubnetIds {
		if i >= len(vpc.SubnetIPv4Allocations) {
			break
		}
		ip := vpc.SubnetIPv4Allocations[i]
		if ip != "" && !lambdaRuntimeIPLeases[ip] {
			lambdaRuntimeIPLeases[ip] = true
			return subnetID, ip, nil
		}
	}
	// A Hyperplane ENI supports many simultaneous connections. The local
	// network substrate needs a distinct interface address for concurrent
	// containers, so scale the ENI realization from the same configured
	// subnet rather than falling back to the container runtime's bridge.
	subnetID = vpc.SubnetIds[0]
	ip, err := AllocateSubnetIP(subnetID)
	if err != nil {
		return "", "", err
	}
	lambdaRuntimeIPLeases[ip] = true
	return subnetID, ip, nil
}

func lambdaRuntimeLinkLocalAddress() string {
	// Keep clear of the EC2 instance metadata and Amazon ECS task metadata
	// addresses. Each active invocation receives a distinct Lambda-service
	// destination, which is routed to its private Runtime API listener.
	n := lambdaRuntimeAPIAddressSequence.Add(1) - 1
	third := 128 + (n/254)%126
	fourth := 1 + n%254
	return fmt.Sprintf("169.254.%d.%d", third, fourth)
}

func startLambdaVpcPauseContainer(invocationID string, sink sim.LogSink) (*sim.ContainerHandle, error) {
	img := sim.ResolveLocalImage(ecsPauseImage())
	platform, err := localImagePlatform(context.Background(), img)
	if err != nil {
		return nil, fmt.Errorf("resolve AWS Lambda VPC pause image platform: %w", err)
	}
	return sim.StartContainerSync(sim.ContainerConfig{
		Image:        img,
		Architecture: platform,
		Command:      []string{"sleep"},
		Args:         []string{"2147483647"},
		Name:         fmt.Sprintf("sockerless-sim-aws-lambda-%s-pause", invocationID[:12]),
		Labels: map[string]string{
			"sockerless-sim-lambda":       invocationID,
			"sockerless-sim-lambda-pause": "true",
		},
		Sandbox: sim.SandboxLambda,
	}, sink)
}

func prepareLambdaInvocationNetwork(
	fn LambdaFunction,
	invocationID string,
	sidecar *runtimeAPISidecar,
	sink sim.LogSink,
) (*lambdaInvocationNetwork, error) {
	metadataEnv, err := hostMetadataEnv("")
	if err != nil {
		return nil, fmt.Errorf("resolve AWS Lambda metadata callback: %w", err)
	}
	out := &lambdaInvocationNetwork{
		runtimeAPIAddr: sidecar.ContainerAddr(),
		metadataEnv:    metadataEnv,
		extraHosts:     runtimeAPIExtraHosts(),
		invocationID:   invocationID,
	}
	if fn.VpcConfig == nil || len(fn.VpcConfig.SubnetIds) == 0 {
		return out, nil
	}

	subnetID, privateIP, err := acquireLambdaRuntimeIP(fn.VpcConfig)
	if err != nil {
		return nil, err
	}
	out.leasedIP = privateIP
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		out.cleanup()
		return nil, fmt.Errorf("AWS Lambda VPC subnet %s no longer exists", subnetID)
	}

	if ec2ECSRealNetAvailable() {
		pause, err := startLambdaVpcPauseContainer(invocationID, sink)
		if err != nil {
			out.cleanup()
			return nil, err
		}
		out.pause = pause
		if err := sim.DisconnectContainerNetworks(pause.ContainerID); err != nil {
			out.cleanup()
			return nil, fmt.Errorf("disconnect AWS Lambda VPC pause container from Docker networks: %w", err)
		}
		pid, err := sim.ContainerPID(pause.ContainerID)
		if err != nil {
			out.cleanup()
			return nil, fmt.Errorf("AWS Lambda VPC pause container PID: %w", err)
		}
		runtimeIPv4 := lambdaRuntimeLinkLocalAddress()
		if err := ec2AttachRealLambdaNIC(
			context.Background(),
			invocationID,
			subnetID,
			pid,
			privateIP,
			fn.VpcConfig.SecurityGroupIds,
			runtimeIPv4,
			sidecar.HostPort(),
		); err != nil {
			out.cleanup()
			return nil, fmt.Errorf("attach AWS Lambda invocation to VPC: %w", err)
		}
		out.networkMode = "container:" + pause.ContainerID
		out.runtimeAPIAddr = net.JoinHostPort(runtimeIPv4, "80")
		out.metadataEnv = hostMetadataLinkLocalEnv("")
		out.extraHosts = nil
		return out, nil
	}

	vpc, ok := ec2Vpcs.Get(subnet.VpcId)
	if !ok {
		out.cleanup()
		return nil, fmt.Errorf("AWS Lambda VPC %s no longer exists", subnet.VpcId)
	}
	if vpc.CidrBlock == "" {
		out.cleanup()
		return nil, fmt.Errorf("AWS Lambda VPC %s has no CIDR block", subnet.VpcId)
	}
	vpcPrefix, perr := netip.ParsePrefix(vpc.CidrBlock)
	if perr != nil {
		out.cleanup()
		return nil, fmt.Errorf("AWS Lambda VPC %s CIDR %q: %w", subnet.VpcId, vpc.CidrBlock, perr)
	}
	networkName := ecsVPCNetworkName(subnet.VpcId)
	if _, err := sim.EnsureVPCNetwork(networkName); err != nil {
		out.cleanup()
		return nil, fmt.Errorf("provision AWS Lambda VPC network for %s: %w", subnet.VpcId, err)
	}
	out.network = networkName
	out.eniAddress = fmt.Sprintf("%s/%d", privateIP, vpcPrefix.Bits())
	return out, nil
}

func lambdaRuntimeImage(runtime string) (string, error) {
	switch runtime {
	case "provided.al2023":
		return "public.ecr.aws/lambda/provided:al2023", nil
	case "provided.al2", "provided", "go1.x":
		return "public.ecr.aws/lambda/provided:al2", nil
	case "nodejs18.x":
		return "public.ecr.aws/lambda/nodejs:18", nil
	case "nodejs20.x":
		return "public.ecr.aws/lambda/nodejs:20", nil
	case "nodejs22.x":
		return "public.ecr.aws/lambda/nodejs:22", nil
	case "nodejs24.x":
		return "public.ecr.aws/lambda/nodejs:24", nil
	case "python3.10":
		return "public.ecr.aws/lambda/python:3.10", nil
	case "python3.11":
		return "public.ecr.aws/lambda/python:3.11", nil
	case "python3.12":
		return "public.ecr.aws/lambda/python:3.12", nil
	case "python3.13":
		return "public.ecr.aws/lambda/python:3.13", nil
	case "python3.14":
		return "public.ecr.aws/lambda/python:3.14", nil
	case "ruby3.2":
		return "public.ecr.aws/lambda/ruby:3.2", nil
	case "ruby3.3":
		return "public.ecr.aws/lambda/ruby:3.3", nil
	case "ruby3.4":
		return "public.ecr.aws/lambda/ruby:3.4", nil
	case "ruby4.0":
		return "public.ecr.aws/lambda/ruby:4.0", nil
	case "java8.al2":
		return "public.ecr.aws/lambda/java:8.al2", nil
	case "java11", "java11.al2023":
		return "public.ecr.aws/lambda/java:11", nil
	case "java17", "java17.al2023":
		return "public.ecr.aws/lambda/java:17", nil
	case "java21":
		return "public.ecr.aws/lambda/java:21", nil
	case "java25":
		return "public.ecr.aws/lambda/java:25", nil
	case "dotnet6":
		return "public.ecr.aws/lambda/dotnet:6", nil
	case "dotnet8":
		return "public.ecr.aws/lambda/dotnet:8", nil
	case "dotnet10":
		return "public.ecr.aws/lambda/dotnet:10", nil
	default:
		return "", fmt.Errorf("runtime %q is not available for new AWS Lambda deployment packages", runtime)
	}
}

func materializeLambdaDeploymentPackage(fn LambdaFunction) (string, error) {
	archiveBytes, err := lambdaDeploymentPackageBytes(fn.Code)
	if err != nil {
		return "", err
	}
	dir, err := createLambdaMountRoot("sockerless-aws-lambda-code-*", "deployment-package")
	if err != nil {
		return "", err
	}
	if err := extractLambdaZIP(dir, archiveBytes); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func materializeLambdaLayers(layerARNs []string) (string, error) {
	if len(layerARNs) == 0 {
		return "", nil
	}
	dir, err := createLambdaMountRoot("sockerless-aws-lambda-layers-*", "layer")
	if err != nil {
		return "", err
	}
	for _, arn := range layerARNs {
		layer, ok := lambdaLayerVersionByARN(arn)
		if !ok {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("layer version %s no longer exists", arn)
		}
		if err := extractLambdaZIP(dir, layer.Content); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("extract layer version %s: %w", arn, err)
		}
	}
	return dir, nil
}

func createLambdaMountRoot(pattern, description string) (string, error) {
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("create %s directory: %w", description, err)
	}
	// os.MkdirTemp deliberately creates mode 0700. AWS's managed runtime runs
	// customer code as sbx_user1051, so a bind mount retaining that host mode
	// makes /var/task or /opt untraversable on Linux. Real Lambda exposes both
	// roots to the sandbox user while keeping the deployment immutable.
	if err := os.Chmod(dir, 0755); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("make %s directory readable by the AWS Lambda sandbox: %w", description, err)
	}
	return dir, nil
}

func extractLambdaZIP(dir string, archiveBytes []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		return fmt.Errorf("deployment package is not a valid ZIP archive: %w", err)
	}
	cleanRoot := filepath.Clean(dir) + string(os.PathSeparator)
	for _, entry := range zr.File {
		target := filepath.Join(dir, filepath.FromSlash(entry.Name))
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), cleanRoot) {
			return fmt.Errorf("deployment package entry %q escapes the task root", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("create deployment-package directory %q: %w", entry.Name, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("create deployment-package parent for %q: %w", entry.Name, err)
		}
		src, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open deployment-package entry %q: %w", entry.Name, err)
		}
		mode := entry.Mode().Perm()
		if mode == 0 {
			mode = 0644
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			_ = src.Close()
			return fmt.Errorf("create deployment-package entry %q: %w", entry.Name, err)
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		_ = src.Close()
		if copyErr != nil {
			return fmt.Errorf("extract deployment-package entry %q: %w", entry.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close deployment-package entry %q: %w", entry.Name, closeErr)
		}
	}
	return nil
}

// invokeLambdaViaRuntimeAPI launches the function container with
// AWS_LAMBDA_RUNTIME_API pointing at a per-invocation sidecar, feeds
// the payload via /next, and returns whatever the handler posts back
// to /response (or /error → X-Amz-Function-Error: Unhandled).
//
// Returns: responseBody, unhandledError (true if /error was posted),
// exitCode from the container. Unhandled errors come back as proper
// Lambda error JSON even when the container itself exits 0.
func invokeLambdaViaRuntimeAPI(fn LambdaFunction, payload []byte) ([]byte, bool, int) {
	var (
		runtimeImage string
		taskRoot     string
		layerRoot    string
	)
	if fn.Code == nil {
		return lambdaErrorPayload("Function has no deployment package"), true, 1
	}
	if fn.Code.ImageUri == "" {
		var err error
		runtimeImage, err = lambdaRuntimeImage(fn.Runtime)
		if err != nil {
			return lambdaErrorPayload(err.Error()), true, 1
		}
		taskRoot, err = materializeLambdaDeploymentPackage(fn)
		if err != nil {
			return lambdaErrorPayload(err.Error()), true, 1
		}
		defer os.RemoveAll(taskRoot)
		layerRoot, err = materializeLambdaLayers(fn.Layers)
		if err != nil {
			return lambdaErrorPayload(err.Error()), true, 1
		}
		if layerRoot != "" {
			defer os.RemoveAll(layerRoot)
		}
	}

	// Build invocation + sidecar.
	requestID := generateUUID()
	timeoutSec := fn.Timeout
	if timeoutSec == 0 {
		timeoutSec = 3
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)

	inv := &lambdaInvocation{
		RequestID:   requestID,
		FunctionArn: fn.FunctionArn,
		Payload:     payload,
		DeadlineMs:  deadline.UnixMilli(),
		TraceID:     generateUUID(),
		done:        make(chan struct{}),
	}

	sidecar, err := startRuntimeAPISidecar(inv)
	if err != nil {
		errBody := lambdaErrorPayload(fmt.Sprintf("Runtime API sidecar start failed: %v", err))
		return errBody, true, 1
	}
	defer sidecar.Shutdown()

	// CloudWatch log group + stream + START log entry.
	logGroup, logStream, logKey, startMs := injectLambdaInvokeLogs(fn.FunctionName, requestID)

	// Image functions honor their image configuration. ZIP functions run in
	// AWS's published managed-runtime base image with the extracted archive at
	// /var/task, which is the filesystem and Runtime API contract Lambda uses.
	var entrypoint, args []string
	var binds []string
	workingDir := ""
	image := fn.Code.ImageUri
	if image == "" {
		image = runtimeImage
		args = []string{fn.Handler}
		binds = []string{taskRoot + ":/var/task:ro,z"}
		if layerRoot != "" {
			binds = append(binds, layerRoot+":/opt:ro,z")
		}
		workingDir = "/var/task"
	} else if fn.ImageConfig != nil {
		entrypoint = fn.ImageConfig.EntryPoint
		args = fn.ImageConfig.Command
		workingDir = fn.ImageConfig.WorkingDirectory
	}
	cmdEnv := map[string]string{
		"AWS_LAMBDA_RUNTIME_API":          sidecar.ContainerAddr(),
		"AWS_LAMBDA_FUNCTION_NAME":        fn.FunctionName,
		"AWS_LAMBDA_FUNCTION_VERSION":     fn.Version,
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": fmt.Sprintf("%d", fn.MemorySize),
		"AWS_REGION":                      awsRegion(),
		"AWS_DEFAULT_REGION":              awsRegion(),
		"AWS_LAMBDA_LOG_GROUP_NAME":       logGroup,
		"AWS_LAMBDA_LOG_STREAM_NAME":      logStream,
		"_HANDLER":                        fn.Handler,
		"AWS_LAMBDA_INITIALIZATION_TYPE":  "on-demand",
	}
	if fn.Environment != nil {
		for k, v := range fn.Environment.Variables {
			cmdEnv[k] = v
		}
	}

	sink := &lambdaLogSink{logGroup: logGroup, logStream: logStream}
	var stderr bytes.Buffer
	collectSink := sim.FuncSink(func(line sim.LogLine) {
		sink.WriteLog(line)
		if line.Stream == "stderr" {
			stderr.WriteString(line.Text)
			stderr.WriteByte('\n')
		}
	})
	platform, err := lambdaDockerPlatform(fn.Architectures)
	if err != nil {
		return lambdaErrorPayload(err.Error()), true, 1
	}
	invocationNetwork, err := prepareLambdaInvocationNetwork(fn, requestID, sidecar, collectSink)
	if err != nil {
		endMs := time.Now().UnixMilli()
		appendLambdaLog(logKey, endMs, fmt.Sprintf("ERROR RequestId: %s VPC attachment failed: %v", requestID, err))
		appendLambdaLog(logKey, endMs+1, fmt.Sprintf("END RequestId: %s", requestID))
		return lambdaErrorPayload(fmt.Sprintf("VPC attachment failed: %v", err)), true, 1
	}
	defer invocationNetwork.cleanup()
	cmdEnv["AWS_LAMBDA_RUNTIME_API"] = invocationNetwork.runtimeAPIAddr

	// Host metadata: Lambda has its Runtime API (above) but workloads
	// may still query EC2 IMDS for region/SA tokens via the AWS SDK.
	// Pass empty taskID — Lambda doesn't expose ECS_CONTAINER_METADATA_URI_V4.
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        sim.ResolveLocalImage(image),
		Architecture: platform,
		Command:      entrypoint,
		Args:         args,
		Env:          mergeEnv(cmdEnv, invocationNetwork.metadataEnv),
		// Timeout is enforced by the sidecar (waiting for /response or
		// error with a deadline); the container itself is given a
		// generous wall-clock budget so slow handlers still surface a
		// proper Lambda timeout instead of a container-level kill.
		Timeout:     time.Duration(timeoutSec+5) * time.Second,
		Name:        fmt.Sprintf("sockerless-sim-aws-lambda-%s", requestID[:12]),
		Labels:      map[string]string{"sockerless-sim-lambda": requestID},
		ExtraHosts:  invocationNetwork.extraHosts,
		Network:     invocationNetwork.network,
		ENIAddress:  invocationNetwork.eniAddress,
		NetworkMode: invocationNetwork.networkMode,
		Sandbox:     sim.SandboxLambda,
		Binds:       binds,
		WorkingDir:  workingDir,
		MemoryBytes: int64(fn.MemorySize) * 1024 * 1024,
	}, collectSink)
	if err != nil {
		endMs := time.Now().UnixMilli()
		appendLambdaLog(logKey, endMs, fmt.Sprintf("ERROR RequestId: %s Container start failed: %v", requestID, err))
		appendLambdaLog(logKey, endMs+1, fmt.Sprintf("END RequestId: %s", requestID))
		return lambdaErrorPayload(fmt.Sprintf("Container start failed: %v", err)), true, 1
	}
	lambdaProcessHandles.Store(requestID, handle)
	defer lambdaProcessHandles.Delete(requestID)

	// Race: handler posts /response|/error, or container exits without
	// posting, or deadline expires.
	var (
		result    []byte
		unhandled bool
		exitCode  int
	)
	waitForContainer := make(chan int, 1)
	go func() {
		res := handle.Wait()
		waitForContainer <- res.ExitCode
	}()

	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()

	select {
	case <-inv.done:
		if inv.errorObj != nil {
			result = inv.errorObj
			unhandled = true
		} else {
			result = inv.response
			if len(result) == 0 {
				result = []byte("{}")
			}
		}
		// Let the container exit on its own so logs drain fully; fall
		// back to cancelling after a short grace window if the handler
		// hangs after posting /response.
		select {
		case exitCode = <-waitForContainer:
		case <-time.After(3 * time.Second):
			handle.Cancel()
			exitCode = <-waitForContainer
		}
	case exitCode = <-waitForContainer:
		// Container exited without calling /response — runtime error.
		result = lambdaErrorPayload(fmt.Sprintf("Runtime exited without providing a reason (exit %d): %s", exitCode, strings.TrimSpace(stderr.String())))
		unhandled = true
	case <-timer.C:
		// Deadline expired.
		handle.Cancel()
		<-waitForContainer
		result = lambdaErrorPayload(fmt.Sprintf("Task timed out after %d.00 seconds", timeoutSec))
		unhandled = true
		exitCode = 1
	}

	// Inject END + REPORT log entries.
	endMs := time.Now().UnixMilli()
	durationMs := float64(time.Since(time.UnixMilli(startMs)).Microseconds()) / 1000.0
	appendLambdaLog(logKey, endMs, fmt.Sprintf("END RequestId: %s", requestID))
	appendLambdaLog(logKey, endMs+1, fmt.Sprintf(
		"REPORT RequestId: %s\tDuration: %.2f ms\tBilled Duration: %d ms\tMemory Size: %d MB\tMax Memory Used: %d MB",
		requestID, durationMs, int64(durationMs)+1, fn.MemorySize, fn.MemorySize/2))
	if unhandled {
		appendLambdaLog(logKey, endMs+2, fmt.Sprintf("ERROR RequestId: %s %s", requestID, strings.TrimSpace(string(result))))
	}

	return result, unhandled, exitCode
}

// injectLambdaInvokeLogs sets up the CloudWatch log group + stream and
// writes the START entry for one invocation. Returns the metadata the
// caller needs to append subsequent entries.
func injectLambdaInvokeLogs(functionName, requestID string) (logGroup, logStream, logKey string, startMs int64) {
	logGroup = fmt.Sprintf("/aws/lambda/%s", functionName)
	now := time.Now()
	startMs = now.UnixMilli()

	if _, exists := cwLogGroups.Get(logGroup); !exists {
		cwLogGroups.Put(logGroup, CWLogGroup{
			LogGroupName: logGroup,
			Arn:          cwLogGroupArn(logGroup),
			CreationTime: startMs,
		})
	}

	hexBytes := make([]byte, 8)
	if _, err := rand.Read(hexBytes); err != nil {
		hexBytes = []byte{0, 0, 0, 0, 0, 0, 0, 0}
	}
	logStream = fmt.Sprintf("%s/[$LATEST]%s", now.Format("2006/01/02"), hex.EncodeToString(hexBytes))
	logKey = cwEventsKey(logGroup, logStream)

	cwLogStreams.Put(logKey, CWLogStream{
		LogStreamName:       logStream,
		LogGroupName:        logGroup,
		CreationTime:        startMs,
		FirstEventTimestamp: startMs,
		LastEventTimestamp:  startMs,
		Arn:                 cwLogStreamArn(logGroup, logStream),
		UploadSequenceToken: "1",
	})
	cwLogEvents.Put(logKey, []CWLogEvent{
		{Timestamp: startMs, Message: fmt.Sprintf("START RequestId: %s Version: $LATEST", requestID), IngestionTime: startMs},
	})
	return
}

// appendLambdaLog adds one event to a stream.
func appendLambdaLog(logKey string, ts int64, msg string) {
	cwLogEvents.Update(logKey, func(events *[]CWLogEvent) {
		*events = append(*events, CWLogEvent{Timestamp: ts, Message: msg, IngestionTime: ts})
	})
}

// lambdaErrorPayload renders a Lambda-style error JSON body.
func lambdaErrorPayload(msg string) []byte {
	body, _ := json.Marshal(map[string]string{
		"errorMessage": msg,
		"errorType":    "Runtime.ExitError",
	})
	return body
}

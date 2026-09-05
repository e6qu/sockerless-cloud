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
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// lambdaInvocation represents a single pending Lambda invocation served
// over the Runtime API. Matches the real Lambda contract: a unique
// request ID, the payload the handler polls via /next, a deadline, and
// channels for the handler's /response or /error reply.
type lambdaInvocation struct {
	RequestID   string
	FunctionArn string
	Payload     []byte
	// TimeoutSec is the function's configured Timeout. It bounds the Invoke
	// phase — "The function's timeout setting limits the duration of the
	// entire Invoke phase" — so the deadline the Runtime API reports is
	// computed when the invocation is handed to the runtime, not when the
	// execution environment was created.
	TimeoutSec int
	TraceID    string

	// Single-slot queue: /next reads once; /response or /error writes once.
	delivered bool
	mu        sync.Mutex

	// initialized is closed when the runtime asks for its first invocation.
	// That request is what ends the Init phase — "The Init phase ends when the
	// runtime and all extensions signal that they are ready by sending a Next
	// API request" — and therefore what starts the invocation timer.
	initialized chan struct{}

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

	simJoinedGo(func() {
		_ = s.server.Serve(ln)
	})

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

	// This request is the runtime signalling that it is initialised: the Init
	// phase ends here and the Invoke phase — the one the function's timeout
	// bounds — begins, so the deadline is measured from now.
	deadline := time.Now().Add(time.Duration(s.inv.TimeoutSec) * time.Second)
	close(s.inv.initialized)

	w.Header().Set("Lambda-Runtime-Aws-Request-Id", s.inv.RequestID)
	w.Header().Set("Lambda-Runtime-Invoked-Function-Arn", s.inv.FunctionArn)
	w.Header().Set("Lambda-Runtime-Deadline-Ms", fmt.Sprintf("%d", deadline.UnixMilli()))
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
		AWSErrorf(w, "InvalidRequestID", http.StatusBadRequest,
			"Invocation with id %s doesn't exist", id)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		AWSError(w, "InvalidRequestBody", "Failed to read response body", http.StatusBadRequest)
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
		AWSErrorf(w, "InvalidRequestID", http.StatusBadRequest,
			"Invocation with id %s doesn't exist", id)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		AWSError(w, "InvalidRequestBody", "Failed to read error body", http.StatusBadRequest)
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
		AWSError(w, "InvalidRequestBody", "Failed to read error body", http.StatusBadRequest)
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
		Sandbox: SandboxLambda,
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
	inv := &lambdaInvocation{
		RequestID:   requestID,
		FunctionArn: fn.FunctionArn,
		Payload:     payload,
		TimeoutSec:  timeoutSec,
		TraceID:     generateUUID(),
		initialized: make(chan struct{}),
		done:        make(chan struct{}),
	}

	sidecar, err := startRuntimeAPISidecar(inv)
	if err != nil {
		errBody := lambdaErrorPayload(fmt.Sprintf("Runtime API sidecar start failed: %v", err))
		return errBody, true, 1
	}
	defer sidecar.Shutdown()

	// CloudWatch log group + stream + START log entry.
	logGroup, logStream, logKey, _ := injectLambdaInvokeLogs(fn.FunctionName, requestID)

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
	// A function that calls an AWS service uses the SDK, which resolves the
	// service endpoint from AWS_ENDPOINT_URL when one is configured. In real
	// Lambda that variable is unset and the SDK resolves to the real regional
	// host; here the services live in this simulator, so the container is given
	// the address it can reach the simulator on. The function's own environment
	// still wins, so a function configured for a specific endpoint keeps it.
	if endpoint := lambdaWorkloadEndpointURL(); endpoint != "" {
		cmdEnv["AWS_ENDPOINT_URL"] = endpoint
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
	//
	// environmentAttempt counts the execution environments this invocation has
	// created: the first, and the one a retried Init phase replaces it with.
	environmentAttempt := 0
	startExecutionEnvironment := func() (*sim.ContainerHandle, error) {
		environmentAttempt++
		return sim.StartContainerSync(sim.ContainerConfig{
			Image:        sim.ResolveLocalImage(image),
			Architecture: platform,
			Command:      entrypoint,
			Args:         args,
			Env:          mergeEnv(cmdEnv, invocationNetwork.metadataEnv),
			// Timeout is enforced by the sidecar (waiting for /response or
			// error with a deadline); the container itself is given a
			// generous wall-clock budget so slow handlers still surface a
			// proper Lambda timeout instead of a container-level kill. The
			// budget spans both lifecycle phases the sidecar bounds — the Init
			// phase's own limit and the Invoke phase's function timeout — since
			// the two run in the same container.
			Timeout: lambdaInitPhaseLimit + time.Duration(timeoutSec+5)*time.Second,
			// Each execution environment is its own sandbox, so a retried Init
			// phase gets its own container rather than the name the cancelled
			// one still holds until the engine finishes removing it.
			Name:        fmt.Sprintf("sockerless-sim-aws-lambda-%s-%d", requestID[:12], environmentAttempt),
			Labels:      map[string]string{"sockerless-sim-lambda": requestID},
			ExtraHosts:  invocationNetwork.extraHosts,
			Network:     invocationNetwork.network,
			ENIAddress:  invocationNetwork.eniAddress,
			NetworkMode: invocationNetwork.networkMode,
			Sandbox:     SandboxLambda,
			Binds:       binds,
			WorkingDir:  workingDir,
			MemoryBytes: int64(fn.MemorySize) * 1024 * 1024,
			// The REPORT entry closing this invocation states the memory the
			// execution environment reached, which is a measurement of the
			// environment the invocation actually ran in.
			TrackMemoryPeak: true,
		}, collectSink)
	}

	// The Init phase begins once the execution environment exists. Creating and
	// starting the container is the sandbox provisioning Lambda performs before
	// INIT_START, so it is outside the Init phase's limit exactly as it is
	// outside the function's timeout.
	handle, err := startExecutionEnvironment()
	initStart := time.Now()
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
	watchContainer := func(h *sim.ContainerHandle) {
		simJoinedGo(func() {
			res := h.Wait()
			waitForContainer <- res.ExitCode
		})
	}
	watchContainer(handle)

	// Init phase. Bootstrapping the runtime and running the function's static
	// code happen here, outside the function timeout: Lambda bounds the Init
	// phase by its own limit and starts the Invoke phase — the one the timeout
	// setting limits — only when the runtime asks for its first invocation.
	initTimer := time.NewTimer(lambdaInitPhaseLimit)
	defer initTimer.Stop()
	var (
		initDuration time.Duration
		initRetried  bool
		initFailed   bool
	)
initPhase:
	for {
		select {
		case <-inv.initialized:
			initDuration = time.Since(initStart)
			break initPhase
		case <-inv.done:
			initDuration = time.Since(initStart)
			// A runtime can only reply once it has been handed an invocation,
			// so a reply that arrives with the readiness signal already raised
			// is an ordinary fast handler whose two events this select saw at
			// once — the Init phase did end, and the Invoke phase below takes
			// the reply immediately.
			if lambdaSignalRaised(inv.initialized) {
				break initPhase
			}
			// Otherwise the runtime reported an initialisation failure to
			// /runtime/init/error rather than asking for work. There is no
			// Invoke phase to bound; the error it posted is the result.
			result = inv.errorObj
			if len(result) == 0 {
				result = lambdaErrorPayload("Runtime failed to initialize")
			}
			unhandled = true
			initFailed = true
			select {
			case exitCode = <-waitForContainer:
			case <-time.After(3 * time.Second):
				handle.Cancel()
				exitCode = <-waitForContainer
			}
			break initPhase
		case exitCode = <-waitForContainer:
			// The runtime died before signalling readiness, so there is no
			// Invoke phase to bound: the invocation ends here.
			initDuration = time.Since(initStart)
			result = lambdaErrorPayload(fmt.Sprintf("Runtime exited without providing a reason (exit %d): %s", exitCode, strings.TrimSpace(stderr.String())))
			unhandled = true
			initFailed = true
			break initPhase
		case <-initTimer.C:
			// "The Init phase is limited to 10 seconds. If all three tasks do
			// not complete within 10 seconds, Lambda retries the Init phase at
			// the time of the first function invocation with the configured
			// function timeout." The retry re-creates the execution
			// environment, and the configured timeout now bounds the retried
			// initialisation together with the invocation that follows it.
			initDuration = time.Since(initStart)
			appendLambdaLog(logKey, time.Now().UnixMilli(), fmt.Sprintf(
				"INIT_REPORT Init Duration: %.2f ms Phase: init Status: timeout",
				float64(initDuration.Microseconds())/1000.0))
			handle.Cancel()
			<-waitForContainer
			initRetried = true
			handle, err = startExecutionEnvironment()
			if err != nil {
				endMs := time.Now().UnixMilli()
				appendLambdaLog(logKey, endMs, fmt.Sprintf("ERROR RequestId: %s Container start failed: %v", requestID, err))
				appendLambdaLog(logKey, endMs+1, fmt.Sprintf("END RequestId: %s", requestID))
				return lambdaErrorPayload(fmt.Sprintf("Container start failed: %v", err)), true, 1
			}
			lambdaProcessHandles.Store(requestID, handle)
			watchContainer(handle)
			break initPhase
		}
	}

	if initRetried {
		// The retried Init phase runs inside the configured function timeout,
		// so the timer below covers initialisation and invocation both, and the
		// duration it produces carries the retried init the way a suppressed
		// init's REPORT does — with no separate Init Duration beside it.
		initDuration = 0
	}
	invokeStart := time.Now()
	invokeDuration := time.Duration(0)

	// Invoke phase. "The function's timeout setting limits the duration of the
	// entire Invoke phase", which begins with the Next request the Init phase
	// ended on — so the timer starts now, not when the container did.
	if !initFailed {
		timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
		defer timer.Stop()

		select {
		case <-inv.done:
			// The Invoke phase ends when the runtime signals it is done, which
			// is this reply — not when the container it runs in finally exits.
			invokeDuration = time.Since(invokeStart)
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
			invokeDuration = time.Since(invokeStart)
			result = lambdaErrorPayload(fmt.Sprintf("Runtime exited without providing a reason (exit %d): %s", exitCode, strings.TrimSpace(stderr.String())))
			unhandled = true
		case <-timer.C:
			// Deadline expired.
			invokeDuration = time.Since(invokeStart)
			handle.Cancel()
			<-waitForContainer
			result = lambdaErrorPayload(fmt.Sprintf("Task timed out after %d.00 seconds", timeoutSec))
			unhandled = true
			exitCode = 1
		}
	}

	// Inject END + REPORT log entries.
	endMs := time.Now().UnixMilli()
	appendLambdaLog(logKey, endMs, fmt.Sprintf("END RequestId: %s", requestID))
	appendLambdaLog(logKey, endMs+1, lambdaReportLine(requestID, invokeDuration, initDuration,
		fn.MemorySize, handle.MemoryPeakBytes()))
	if unhandled {
		appendLambdaLog(logKey, endMs+2, fmt.Sprintf("ERROR RequestId: %s %s", requestID, strings.TrimSpace(string(result))))
	}

	return result, unhandled, exitCode
}

// lambdaInitPhaseLimit is how long Lambda gives the Init phase before it gives
// up on it: "The Init phase is limited to 10 seconds. If all three tasks do not
// complete within 10 seconds, Lambda retries the Init phase at the time of the
// first function invocation with the configured function timeout."
// (AWS Lambda Developer Guide, "Understanding the Lambda execution environment
// lifecycle" § Init phase.) It is not the function's timeout, which limits the
// Invoke phase alone, and it is the on-demand concurrency limit — provisioned
// concurrency, SnapStart and Managed Instances are exempt from it, and this
// simulator initialises on demand.
const lambdaInitPhaseLimit = 10 * time.Second

// lambdaSignalRaised reports whether a lifecycle signal channel has been closed
// already, without waiting for one that has not.
func lambdaSignalRaised(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// lambdaReportLine renders the REPORT entry that closes an invocation.
//
// Duration is the Invoke phase alone and Init Duration is reported beside it,
// which is how Lambda separates the two: its own worked example of a
// three-second function timing out reports `Duration: 3004.92 ms Billed
// Duration: 3117 ms … Init Duration: 111.23 ms` — the duration stops at the
// timeout while the initialisation that preceded it is counted separately.
// Billed Duration is the two rounded up and added, which reproduces that
// example (3005 + 112 = 3117) and the two beside it (134 + 80 = 214,
// 3017 + 84 = 3101).
//
// Init Duration is omitted when there is none to report — a warm invocation, or
// one whose initialisation was retried inside the function timeout and is
// therefore already inside Duration.
//
// Max Memory Used is the memory the execution environment reached, as the
// container engine accounted for it while the invocation ran, rounded up to the
// megabyte the field is expressed in. peakBytes is zero when the engine
// reported no accounting at all, and the entry then omits the field: an absent
// measurement is reported as absent rather than as a number nothing measured.
func lambdaReportLine(requestID string, invokeDuration, initDuration time.Duration, memorySize int, peakBytes uint64) string {
	durationMs := float64(invokeDuration.Microseconds()) / 1000.0
	billedMs := int64(math.Ceil(durationMs))
	initSuffix := ""
	if initDuration > 0 {
		initMs := float64(initDuration.Microseconds()) / 1000.0
		billedMs += int64(math.Ceil(initMs))
		initSuffix = fmt.Sprintf("\tInit Duration: %.2f ms", initMs)
	}
	memoryUsed := ""
	if peakBytes > 0 {
		memoryUsed = fmt.Sprintf("\tMax Memory Used: %d MB", lambdaMemoryMegabytes(peakBytes))
	}
	return fmt.Sprintf(
		"REPORT RequestId: %s\tDuration: %.2f ms\tBilled Duration: %d ms\tMemory Size: %d MB%s%s",
		requestID, durationMs, billedMs, memorySize, memoryUsed, initSuffix)
}

// lambdaMemoryMegabytes expresses a measured byte count in the megabytes a
// REPORT entry reports, which are the megabytes a function's memory size is
// configured in — 1 MB = 1 MiB, the unit the execution environment's cgroup
// limit is set from. A partly used megabyte is a used megabyte, so the
// conversion rounds up.
func lambdaMemoryMegabytes(bytes uint64) uint64 {
	const megabyte = 1024 * 1024
	return (bytes + megabyte - 1) / megabyte
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

// lambdaWorkloadEndpointURL is the simulator's address as a function container
// sees it — the same host its Runtime API arrives on, at the simulator's own
// port. Empty when neither can be determined, in which case the container is
// left with the SDK's own resolution rather than a wrong address.
func lambdaWorkloadEndpointURL() string {
	host, err := runtimeAPIHost()
	if err != nil {
		return ""
	}
	port, err := simHostMetadataPort()
	if err != nil {
		return ""
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

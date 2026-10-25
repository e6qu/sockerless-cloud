package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// lambdaInvocation is one invocation handed to an execution environment's
// runtime over the Runtime API: a request ID, the payload the runtime reads
// from Next, and the reply it posts to the invocation's response or error
// path.
type lambdaInvocation struct {
	RequestID   string
	FunctionArn string
	Payload     []byte
	// TimeoutSec is the function's configured Timeout. It bounds the Invoke
	// phase — "The function's timeout setting limits the duration of the
	// entire Invoke phase" — so the deadline the Runtime API reports counts
	// from when the runtime receives the invocation.
	TimeoutSec int
	TraceID    string

	// done closes once the runtime posted a response or an error.
	done     chan struct{}
	doneOnce sync.Once
	response []byte
	errorObj []byte
	// ended closes when the runtime asks for its next invocation, which is
	// what ends this one's Invoke phase: "The invoke phase ends after the
	// runtime and all extensions signal that they are done by sending a Next
	// API request."
	ended     chan struct{}
	endedOnce sync.Once
}

func newLambdaInvocation(fn LambdaFunction, payload []byte) *lambdaInvocation {
	return &lambdaInvocation{
		RequestID:   sim.NewUUID(),
		FunctionArn: fn.FunctionArn,
		Payload:     payload,
		TimeoutSec:  lambdaTimeoutSeconds(fn),
		TraceID:     sim.NewUUID(),
		done:        make(chan struct{}),
		ended:       make(chan struct{}),
	}
}

func (inv *lambdaInvocation) reply(response, errorObj []byte) {
	inv.doneOnce.Do(func() {
		inv.response, inv.errorObj = response, errorObj
		close(inv.done)
	})
}

// runtimeAPISidecar implements the AWS Lambda Runtime API for one execution
// environment, for as long as the environment lives. Real Lambda gives each
// execution environment its own Runtime API at 127.0.0.1:9001; here it runs
// on the host and the container reaches it through the workload callback
// address.
type runtimeAPISidecar struct {
	listener net.Listener
	server   *http.Server
	addr     string // host:port address the container sees

	// ready closes on the runtime's first Next request, which ends the Init
	// phase: "The Init phase ends when the runtime and all extensions signal
	// that they are ready by sending a Next API request."
	ready     chan struct{}
	readyOnce sync.Once
	readyAt   time.Time
	// initFailed closes once the runtime reported an initialisation error.
	initFailed     chan struct{}
	initFailedOnce sync.Once
	initError      []byte
	// pending hands the next invocation to the runtime's Next request.
	pending chan *lambdaInvocation
	closed  chan struct{}

	mu      sync.Mutex
	current *lambdaInvocation
}

// startRuntimeAPISidecar binds a free port on all interfaces, mounts the
// Runtime API routes, and starts serving in a background goroutine. Must bind
// to 0.0.0.0 (not 127.0.0.1) because the function container reaches back via
// the container runtime's bridge gateway on Linux, which is a different
// interface than loopback. Other platforms use their standard host callback
// name.
func startRuntimeAPISidecar() (*runtimeAPISidecar, error) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("runtime API listen: %w", err)
	}
	host, err := workloadhost.CallbackHost()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, fmt.Errorf("runtime API listen: unexpected address type %T", ln.Addr())
	}

	mux := http.NewServeMux()
	s := &runtimeAPISidecar{
		listener:   ln,
		addr:       net.JoinHostPort(host, fmt.Sprintf("%d", addr.Port)),
		ready:      make(chan struct{}),
		initFailed: make(chan struct{}),
		pending:    make(chan *lambdaInvocation, 1),
		closed:     make(chan struct{}),
	}
	mux.HandleFunc("GET /2018-06-01/runtime/invocation/next", s.handleNext)
	mux.HandleFunc("POST /2018-06-01/runtime/invocation/{id}/response", s.handleResponse)
	mux.HandleFunc("POST /2018-06-01/runtime/invocation/{id}/error", s.handleInvocationError)
	mux.HandleFunc("POST /2018-06-01/runtime/init/error", s.handleInitError)

	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  0, // Next is a long poll
		WriteTimeout: 0,
	}
	bg.JoinedGo(func() {
		_ = s.server.Serve(ln)
	})
	return s, nil
}

// Shutdown closes the sidecar once its environment is gone.
func (s *runtimeAPISidecar) Shutdown() {
	close(s.closed)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
}

// ContainerAddr returns the address the container uses to reach this
// sidecar.
func (s *runtimeAPISidecar) ContainerAddr() string {
	return s.addr
}

// dispatch queues inv for the runtime's next Next request.
func (s *runtimeAPISidecar) dispatch(inv *lambdaInvocation) {
	s.pending <- inv
}

// handleNext serves GET /2018-06-01/runtime/invocation/next: it ends the
// invocation the runtime was working on, then blocks until the environment
// is handed its next one.
func (s *runtimeAPISidecar) handleNext(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	previous := s.current
	s.current = nil
	s.mu.Unlock()
	if previous != nil {
		previous.endedOnce.Do(func() { close(previous.ended) })
	}
	s.readyOnce.Do(func() {
		s.readyAt = time.Now()
		close(s.ready)
	})

	var inv *lambdaInvocation
	select {
	case inv = <-s.pending:
	case <-r.Context().Done():
		return
	case <-s.closed:
		return
	}
	s.mu.Lock()
	s.current = inv
	s.mu.Unlock()

	deadline := time.Now().Add(time.Duration(inv.TimeoutSec) * time.Second)
	w.Header().Set("Lambda-Runtime-Aws-Request-Id", inv.RequestID)
	w.Header().Set("Lambda-Runtime-Invoked-Function-Arn", inv.FunctionArn)
	w.Header().Set("Lambda-Runtime-Deadline-Ms", fmt.Sprintf("%d", deadline.UnixMilli()))
	if inv.TraceID != "" {
		w.Header().Set("Lambda-Runtime-Trace-Id", inv.TraceID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(inv.Payload)
}

// lambdaRuntimeAPIError writes the Runtime API's ErrorResponse, the
// {errorMessage, errorType} document AWS Lambda's runtime interface answers a
// runtime with, rather than an AWS service error.
func lambdaRuntimeAPIError(w http.ResponseWriter, status int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"errorMessage": message, "errorType": errorType})
}

// invocationFor is the invocation the runtime holds under id.
func (s *runtimeAPISidecar) invocationFor(id string) (*lambdaInvocation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil || s.current.RequestID != id {
		return nil, false
	}
	return s.current, true
}

// handleResponse serves POST /2018-06-01/runtime/invocation/{id}/response.
func (s *runtimeAPISidecar) handleResponse(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.invocationFor(r.PathValue("id"))
	if !ok {
		lambdaRuntimeAPIError(w, http.StatusBadRequest, "InvalidRequestID", "Invalid request ID")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		lambdaRuntimeAPIError(w, http.StatusBadRequest, "TruncatedHTTPRequest", "HTTP request detected as truncated")
		return
	}
	inv.reply(body, nil)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}

// handleInvocationError serves POST /2018-06-01/runtime/invocation/{id}/error.
func (s *runtimeAPISidecar) handleInvocationError(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.invocationFor(r.PathValue("id"))
	if !ok {
		lambdaRuntimeAPIError(w, http.StatusBadRequest, "InvalidRequestID", "Invalid request ID")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		lambdaRuntimeAPIError(w, http.StatusBadRequest, "TruncatedHTTPRequest", "HTTP request detected as truncated")
		return
	}
	inv.reply(nil, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}

// handleInitError serves POST /2018-06-01/runtime/init/error.
func (s *runtimeAPISidecar) handleInitError(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		lambdaRuntimeAPIError(w, http.StatusBadRequest, "TruncatedHTTPRequest", "HTTP request detected as truncated")
		return
	}
	s.initFailedOnce.Do(func() {
		s.initError = body
		close(s.initFailed)
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}

var (
	lambdaRuntimeIPMu     sync.Mutex
	lambdaRuntimeIPLeases = map[string]bool{}
)

type lambdaInvocationNetwork struct {
	network string
	// eniAddress is the execution environment's elastic network interface
	// IPv4 address, which it takes on the VPC's network.
	eniAddress     string
	dns            []string
	runtimeAPIAddr string
	extraHosts     []string
	leasedIP       string
}

func (n *lambdaInvocationNetwork) cleanup() {
	if n == nil {
		return
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

// prepareLambdaInvocationNetwork decides where an execution environment
// runs: on the function's VPC network at its elastic network interface
// address, or on the platform network for a function outside any VPC. Either
// way it resolves names and reads its metadata through the network's sidecar.
func prepareLambdaInvocationNetwork(fn LambdaFunction, sidecar *runtimeAPISidecar) (*lambdaInvocationNetwork, error) {
	out := &lambdaInvocationNetwork{
		runtimeAPIAddr: sidecar.ContainerAddr(),
		extraHosts:     workloadhost.ExtraHosts(),
	}
	if fn.VpcConfig == nil || len(fn.VpcConfig.SubnetIds) == 0 {
		network, err := awsPlatformNetwork(context.Background())
		if err != nil {
			return nil, fmt.Errorf("provision the AWS Lambda execution environment's network: %w", err)
		}
		out.network = network.Name
		out.dns = network.DNS()
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

	network, err := ec2EnsureVPCNetwork(context.Background(), subnet.VpcId)
	if err != nil {
		out.cleanup()
		return nil, fmt.Errorf("provision AWS Lambda VPC network for %s: %w", subnet.VpcId, err)
	}
	out.network = network.Name
	out.dns = network.DNS()
	out.eniAddress = privateIP
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
	if err := archive.ExtractZip(archiveBytes, dir, lambdaUnzippedCodeLimit); err != nil {
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
		if err := archive.ExtractZip(layer.Content, dir, lambdaUnzippedCodeLimit); err != nil {
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

// lambdaUnzippedCodeLimit is the most a function's deployment package and its
// layers may unzip to, the CodeSizeUnzipped account limit.
const lambdaUnzippedCodeLimit = 262144000

// lambdaInitPhaseLimit is how long Lambda gives an on-demand Init phase before
// it gives up on it: "The Init phase is limited to 10 seconds. If all three
// tasks do not complete within 10 seconds, Lambda retries the Init phase at
// the time of the first function invocation with the configured function
// timeout." (lambda-runtime-environment.md.gz, § Init phase.) It is not the
// function's timeout, which limits the Invoke phase alone.
const lambdaInitPhaseLimit = 10 * time.Second

// lambdaProvisionedInitLimit is how long a provisioned environment's Init
// phase may run: "For provisioned concurrency, SnapStart, and Managed
// Instances functions, your initialization code can run for up to 15
// minutes. The time limit is 130 seconds or the configured function timeout
// (maximum 900 seconds), whichever is higher."
func lambdaProvisionedInitLimit(fn LambdaFunction) time.Duration {
	return max(130*time.Second, time.Duration(lambdaTimeoutSeconds(fn))*time.Second)
}

// lambdaTimeoutSeconds is the function's configured timeout, or Lambda's
// default of three seconds.
func lambdaTimeoutSeconds(fn LambdaFunction) int {
	if fn.Timeout == 0 {
		return 3
	}
	return fn.Timeout
}

// lambdaInvokeWaitLimit bounds a synchronous invocation: the Init phase's own
// limit, then the function timeout the Invoke phase shares with a retried Init
// phase.
func lambdaInvokeWaitLimit(fn LambdaFunction) time.Duration {
	return lambdaInitPhaseLimit + time.Duration(lambdaTimeoutSeconds(fn))*time.Second
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

// createLambdaLogStream creates the function's log group if it has none and
// a log stream for one execution environment, named as Lambda names it: the
// date, the version in brackets, and an identifier of the environment.
func createLambdaLogStream(fn LambdaFunction) (logGroup, logStream, logKey string) {
	logGroup = fmt.Sprintf("/aws/lambda/%s", fn.FunctionName)
	now := time.Now()
	startMs := now.UnixMilli()

	if _, exists := cwLogGroups.Get(logGroup); !exists {
		cwLogGroups.Put(logGroup, CWLogGroup{
			LogGroupName: logGroup,
			Arn:          cwLogGroupArn(logGroup),
			CreationTime: startMs,
		})
	}

	hexBytes := make([]byte, 16)
	if _, err := rand.Read(hexBytes); err != nil {
		hexBytes = make([]byte, 16)
	}
	version := fn.Version
	if version == "" {
		version = "$LATEST"
	}
	logStream = fmt.Sprintf("%s/[%s]%s", now.Format("2006/01/02"), version, hex.EncodeToString(hexBytes))
	logKey = cwEventsKey(logGroup, logStream)

	cwLogStreams.Put(logKey, CWLogStream{
		LogStreamName:       logStream,
		LogGroupName:        logGroup,
		CreationTime:        startMs,
		FirstEventTimestamp: startMs,
		LastEventTimestamp:  startMs,
		LastIngestionTime:   startMs,
		Arn:                 cwLogStreamArn(logGroup, logStream),
		UploadSequenceToken: "1",
	})
	return logGroup, logStream, logKey
}

// appendLambdaLog adds one event to a stream.
func appendLambdaLog(logKey string, ts int64, msg string) {
	cwAppendLogEvents(logKey, []CWLogEvent{{Timestamp: ts, Message: msg, IngestionTime: ts}}, nil)
}

// lambdaServiceError is an invocation failure AWS Lambda reports as a service
// exception of its own, rather than as the function's error.
type lambdaServiceError struct {
	Status  int
	Code    string
	Message string
	// Reason and Type are the throttle's TooManyRequestsException members.
	Reason string
	Type   string
}

// lambdaWriteServiceError answers a request with the service exception.
func lambdaWriteServiceError(w http.ResponseWriter, e *lambdaServiceError) {
	if e.Reason == "" {
		AWSError(w, e.Code, e.Message, e.Status)
		return
	}
	body := map[string]string{"__type": e.Code, "message": e.Message, "Message": e.Message}
	if model, ok := awsErrorModelOf(w); ok {
		body = awsErrorBody(model, e.Code, e.Message)
	}
	body["Reason"] = e.Reason
	body["Type"] = e.Type
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(body)
}

// lambdaErrorPayload renders a Lambda-style error JSON body.
func lambdaErrorPayload(msg string) []byte {
	body, _ := json.Marshal(map[string]string{
		"errorMessage": msg,
		"errorType":    "Runtime.ExitError",
	})
	return body
}

// lambdaEnvironmentMemoryPeak is the most memory the execution environment
// reached, which the container backend measures through the engine's stats;
// a backend without the measurement reports none.
func lambdaEnvironmentMemoryPeak(handle placement.Handle) uint64 {
	if measured, ok := handle.(interface{ MemoryPeakBytes() uint64 }); ok {
		return measured.MemoryPeakBytes()
	}
	return 0
}

// lambdaReservedEnvironmentKeys are the variables the runtime sets itself,
// which a function's configuration may not.
var lambdaReservedEnvironmentKeys = []string{
	"_HANDLER", "_X_AMZN_TRACE_ID", "AWS_DEFAULT_REGION", "AWS_REGION", "AWS_EXECUTION_ENV",
	"AWS_LAMBDA_FUNCTION_NAME", "AWS_LAMBDA_FUNCTION_MEMORY_SIZE", "AWS_LAMBDA_FUNCTION_VERSION",
	"AWS_LAMBDA_INITIALIZATION_TYPE", "AWS_LAMBDA_LOG_GROUP_NAME", "AWS_LAMBDA_LOG_STREAM_NAME",
	"AWS_ACCESS_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_LAMBDA_RUNTIME_API", "LAMBDA_TASK_ROOT", "LAMBDA_RUNTIME_DIR",
}

// lambdaCheckReservedEnvironment refuses a configuration that sets a reserved
// variable, naming each one it sets.
func lambdaCheckReservedEnvironment(w http.ResponseWriter, env *LambdaEnvironment) bool {
	if env == nil {
		return true
	}
	var used []string
	for _, key := range lambdaReservedEnvironmentKeys {
		if _, ok := env.Variables[key]; ok {
			used = append(used, key)
		}
	}
	if len(used) == 0 {
		return true
	}
	AWSErrorf(w, "InvalidParameterValueException", http.StatusBadRequest,
		"Lambda was unable to configure your environment variables because the environment variables you have provided contains reserved keys that are currently not supported for modification. Reserved keys used in this request: %s",
		strings.Join(used, ", "))
	return false
}

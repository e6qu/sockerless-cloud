package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/funcrt"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// lambdaEnvironment is one AWS Lambda execution environment: a container
// running the function's runtime against a Runtime API of its own. It stays
// initialised between invocations — "Objects declared outside of the
// function's handler method remain initialized" (lambda-runtime-environment
// .md.gz, § Shutdown phase) — and serves one invocation at a time.
type lambdaEnvironment struct {
	id        string
	fn        LambdaFunction
	kind      funcrt.Kind
	api       *runtimeAPISidecar
	network   *lambdaInvocationNetwork
	taskRoot  string
	layerRoot string
	logGroup  string
	logStream string
	logKey    string
	// expires is when the execution role session in the environment's
	// variables would lapse under an invocation that started then.
	expires time.Time
	backend placement.Backend
	config  sim.ContainerConfig

	mu      sync.Mutex
	handle  placement.Handle
	attempt int
	// startedAt is when the backend started the current container's process,
	// which begins its Init phase. It is the backend's instant, not the
	// return of Place: a runtime that bootstraps at once asks for its first
	// invocation before Place returns.
	startedAt time.Time
	stderr    bytes.Buffer
	// initialised reports that the runtime has asked for its first
	// invocation, ending the Init phase.
	initialised bool
	stopOnce    sync.Once
}

// lambdaStartError is why an execution environment could not be set up,
// reported to the invocation that needed it.
type lambdaStartError struct {
	message string
	payload []byte
	service *lambdaServiceError
}

func (e *lambdaStartError) Error() string { return e.message }

func lambdaStartFailure(service *lambdaServiceError, format string, args ...any) *lambdaStartError {
	message := fmt.Sprintf(format, args...)
	return &lambdaStartError{message: message, payload: lambdaErrorPayload(message), service: service}
}

// startLambdaEnvironment provisions an execution environment for fn: its
// deployment package and layers, its Runtime API, its network, its log
// stream and its container. A provisioned environment returns once its
// runtime has initialised; an on-demand one returns once its container runs,
// and the invocation that started it waits out its Init phase.
func startLambdaEnvironment(ctx context.Context, fn LambdaFunction, kind funcrt.Kind) (_ *lambdaEnvironment, err error) {
	env := &lambdaEnvironment{id: sim.NewUUID(), fn: fn, kind: kind}
	defer func() {
		if err != nil {
			env.Stop()
		}
	}()
	var runtimeImage string
	if fn.Code == nil {
		return nil, lambdaStartFailure(nil, "Function has no deployment package")
	}
	if fn.Code.ImageUri == "" {
		if runtimeImage, err = lambdaRuntimeImage(fn.Runtime); err != nil {
			return nil, lambdaStartFailure(nil, "%v", err)
		}
		if env.taskRoot, err = materializeLambdaDeploymentPackage(fn); err != nil {
			return nil, lambdaStartFailure(nil, "%v", err)
		}
		if env.layerRoot, err = materializeLambdaLayers(fn.Layers); err != nil {
			return nil, lambdaStartFailure(nil, "%v", err)
		}
	}
	if env.api, err = startRuntimeAPISidecar(); err != nil {
		return nil, lambdaStartFailure(nil, "Runtime API sidecar start failed: %v", err)
	}
	env.logGroup, env.logStream, env.logKey = createLambdaLogStream(fn)

	// Image functions honor their image configuration. ZIP functions run in
	// AWS's published managed-runtime base image with the extracted archive at
	// /var/task, which is the filesystem and Runtime API contract Lambda uses.
	var entrypoint, args, binds []string
	workingDir := ""
	image := fn.Code.ImageUri
	if image == "" {
		image = runtimeImage
		args = []string{fn.Handler}
		binds = []string{env.taskRoot + ":/var/task:ro,z"}
		if env.layerRoot != "" {
			binds = append(binds, env.layerRoot+":/opt:ro,z")
		}
		workingDir = "/var/task"
	} else if fn.ImageConfig != nil {
		entrypoint = fn.ImageConfig.EntryPoint
		args = fn.ImageConfig.Command
		workingDir = fn.ImageConfig.WorkingDirectory
	}
	initializationType := "on-demand"
	if kind == funcrt.Provisioned {
		initializationType = "provisioned-concurrency"
	}
	cmdEnv := map[string]string{
		"AWS_LAMBDA_FUNCTION_NAME":        fn.FunctionName,
		"AWS_LAMBDA_FUNCTION_VERSION":     fn.Version,
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": fmt.Sprintf("%d", fn.MemorySize),
		"AWS_REGION":                      awsRegion(),
		"AWS_DEFAULT_REGION":              awsRegion(),
		"AWS_LAMBDA_LOG_GROUP_NAME":       env.logGroup,
		"AWS_LAMBDA_LOG_STREAM_NAME":      env.logStream,
		"_HANDLER":                        fn.Handler,
		"AWS_LAMBDA_INITIALIZATION_TYPE":  initializationType,
	}
	// A function that calls an AWS service uses the SDK, which resolves the
	// service endpoint from AWS_ENDPOINT_URL when one is configured. In real
	// Lambda that variable is unset and the SDK resolves to the real regional
	// host; here the services live in this simulator, so the container is given
	// the address it can reach the simulator on. The function's own environment
	// still wins, so a function configured for a specific endpoint keeps it.
	endpoint, err := workloadhost.CallbackAddr(simCallbackListenAddr)
	if err != nil {
		return nil, lambdaStartFailure(nil, "resolve the simulator endpoint: %v", err)
	}
	cmdEnv["AWS_ENDPOINT_URL"] = "http://" + endpoint
	if fn.Environment != nil {
		for k, v := range fn.Environment.Variables {
			cmdEnv[k] = v
		}
	}
	// The function runs under its execution role, whose session Lambda hands
	// it in the environment for the environment's life.
	credentials, credentialsExpire := roleEnvironmentCredentialsExpiring(roleHolder("lambda", fn.FunctionName), fn.Role, fn.FunctionName)
	for k, v := range credentials {
		cmdEnv[k] = v
	}
	if !credentialsExpire.IsZero() {
		env.expires = credentialsExpire.Add(-lambdaInvokeWaitLimit(fn))
	}

	platform, err := lambdaDockerPlatform(fn.Architectures)
	if err != nil {
		return nil, lambdaStartFailure(nil, "%v", err)
	}
	if env.network, err = prepareLambdaInvocationNetwork(fn, env.api); err != nil {
		// AWS Lambda reports a VPC it could not set the function up in as an
		// EC2 client exception of its own, not as the function's error.
		return nil, lambdaStartFailure(&lambdaServiceError{
			Status:  http.StatusBadGateway,
			Code:    "EC2UnexpectedException",
			Message: fmt.Sprintf("AWS Lambda received an unexpected Amazon EC2 client exception while setting up for the Lambda function. %v", err),
		}, "VPC attachment failed: %v", err)
	}
	cmdEnv["AWS_LAMBDA_RUNTIME_API"] = env.network.runtimeAPIAddr

	if env.backend, err = awsPlacement.For(placement.Service{Name: "lambda"}); err != nil {
		return nil, lambdaStartFailure(nil, "%v", err)
	}
	env.config = sim.ContainerConfig{
		Image:        ecrWorkloadImage(image),
		Architecture: platform,
		Command:      entrypoint,
		Args:         args,
		Env:          cmdEnv,
		Labels:       map[string]string{"sockerless-sim-lambda": env.id},
		ExtraHosts:   env.network.extraHosts,
		Network:      env.network.network,
		IPv4Address:  env.network.eniAddress,
		DNS:          env.network.dns,
		Sandbox:      SandboxLambda,
		Binds:        binds,
		WorkingDir:   workingDir,
		MemoryBytes:  int64(fn.MemorySize) * 1024 * 1024,
		// The REPORT entry closing an invocation states the memory the
		// execution environment reached, which is a measurement of the
		// environment the invocation actually ran in.
		TrackMemoryPeak: true,
	}
	// Creating and starting the container is the sandbox provisioning Lambda
	// performs before INIT_START, so it is outside the Init phase's limit
	// exactly as it is outside the function's timeout.
	if err := env.place(ctx); err != nil {
		return nil, lambdaStartFailure(nil, "Container start failed: %v", err)
	}
	if kind == funcrt.Provisioned {
		if err := env.provisionInit(ctx); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// place starts a container for the environment, the first or the one a
// retried Init phase replaces it with.
func (e *lambdaEnvironment) place(ctx context.Context) error {
	registryAuth, err := ecrWorkloadRegistryAuth(e.config.Image)
	if err != nil {
		return err
	}
	cfg := e.config
	cfg.RegistryAuth = registryAuth
	e.mu.Lock()
	e.attempt++
	// Each container is its own sandbox, so a retried Init phase gets its own
	// rather than the name the cancelled one still holds until the engine
	// finishes removing it.
	cfg.Name = fmt.Sprintf("sockerless-sim-aws-lambda-%s-%d", e.id[:12], e.attempt)
	e.mu.Unlock()
	sink := &lambdaLogSink{logGroup: e.logGroup, logStream: e.logStream}
	handle, err := e.backend.Place(ctx, cfg, sim.FuncSink(func(line sim.LogLine) {
		sink.WriteLog(line)
		if line.Stream == "stderr" {
			e.mu.Lock()
			e.stderr.WriteString(line.Text)
			e.stderr.WriteByte('\n')
			e.mu.Unlock()
		}
	}))
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.handle = handle
	e.startedAt = handle.StartedAt()
	e.mu.Unlock()
	return nil
}

// initDuration is how long the Init phase took, from the container's start
// to the runtime's first Next request.
func (e *lambdaEnvironment) initDuration() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.api.readyAt.Sub(e.startedAt)
}

func (e *lambdaEnvironment) currentHandle() placement.Handle {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.handle
}

func (e *lambdaEnvironment) Exited() <-chan struct{} {
	return e.currentHandle().Exited()
}

// Stop shuts the environment down: "If the runtime or an extension does not
// respond to the Shutdown event within the limit, Lambda ends the process
// using a SIGKILL signal", and a function with no registered extensions has
// a limit of 0 ms.
func (e *lambdaEnvironment) Stop() {
	e.stopOnce.Do(func() {
		if h := e.currentHandle(); h != nil {
			h.Cancel()
			<-h.Exited()
		}
		e.release()
	})
}

// release frees what the environment held besides its container.
func (e *lambdaEnvironment) release() {
	if e.api != nil {
		e.api.Shutdown()
	}
	e.network.cleanup()
	if e.taskRoot != "" {
		_ = os.RemoveAll(e.taskRoot)
	}
	if e.layerRoot != "" {
		_ = os.RemoveAll(e.layerRoot)
	}
}

func (e *lambdaEnvironment) exitCode() int {
	return e.currentHandle().Wait().ExitCode
}

func (e *lambdaEnvironment) stderrText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.TrimSpace(e.stderr.String())
}

func (e *lambdaEnvironment) resetStderr() {
	e.mu.Lock()
	e.stderr.Reset()
	e.mu.Unlock()
}

// provisionInit waits out a provisioned environment's Init phase, which
// Lambda runs when provisioned concurrency is configured and reports with an
// INIT_REPORT entry: "SnapStart and provisioned concurrency functions always
// emit INIT_REPORT."
func (e *lambdaEnvironment) provisionInit(ctx context.Context) error {
	e.mu.Lock()
	start := e.startedAt
	e.mu.Unlock()
	limit := time.NewTimer(lambdaProvisionedInitLimit(e.fn) - time.Since(start))
	defer limit.Stop()
	report := func(status string) {
		appendLambdaLog(e.logKey, time.Now().UnixMilli(), fmt.Sprintf(
			"INIT_REPORT Init Duration: %.2f ms Phase: init Status: %s",
			float64(time.Since(start).Microseconds())/1000.0, status))
	}
	select {
	case <-e.api.ready:
		e.initialised = true
		report("success")
		return nil
	case <-e.api.initFailed:
		report("error")
		return lambdaStartFailure(nil, "Runtime failed to initialize: %s", strings.TrimSpace(string(e.api.initError)))
	case <-e.Exited():
		report("error")
		return lambdaStartFailure(nil, "Runtime exited without providing a reason (exit %d): %s", e.exitCode(), e.stderrText())
	case <-limit.C:
		report("timeout")
		return lambdaStartFailure(nil, "The Init phase did not finish within %s", lambdaProvisionedInitLimit(e.fn))
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lambdaInitOutcome is how an on-demand environment's Init phase ended under
// the invocation that started it.
type lambdaInitOutcome int

const (
	lambdaInitDone lambdaInitOutcome = iota
	// lambdaInitRetried replaced the environment's container after the Init
	// phase outran its limit; the retried initialisation runs inside the
	// invocation's function timeout.
	lambdaInitRetried
	lambdaInitFailed
	lambdaInitInterrupted
)

// initPhase waits out an on-demand environment's Init phase. Bootstrapping
// the runtime and running the function's static code happen here, outside the
// function timeout: Lambda bounds the Init phase by its own limit and starts
// the Invoke phase only when the runtime asks for its first invocation.
func (e *lambdaEnvironment) initPhase(ctx context.Context) (lambdaInitOutcome, time.Duration, []byte) {
	e.mu.Lock()
	start := e.startedAt
	e.mu.Unlock()
	timer := time.NewTimer(lambdaInitPhaseLimit - time.Since(start))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return lambdaInitInterrupted, 0, nil
	case <-e.api.ready:
		e.initialised = true
		return lambdaInitDone, e.initDuration(), nil
	case <-e.api.initFailed:
		result := e.api.initError
		if len(result) == 0 {
			result = lambdaErrorPayload("Runtime failed to initialize")
		}
		return lambdaInitFailed, time.Since(start), result
	case <-e.Exited():
		// The runtime died before signalling readiness, so there is no Invoke
		// phase to bound.
		return lambdaInitFailed, time.Since(start), lambdaErrorPayload(fmt.Sprintf(
			"Runtime exited without providing a reason (exit %d): %s", e.exitCode(), e.stderrText()))
	case <-timer.C:
		// "If all three tasks do not complete within 10 seconds, Lambda
		// retries the Init phase at the time of the first function invocation
		// with the configured function timeout." The retry re-creates the
		// sandbox, and the configured timeout now bounds the retried
		// initialisation together with the invocation that follows it.
		appendLambdaLog(e.logKey, time.Now().UnixMilli(), fmt.Sprintf(
			"INIT_REPORT Init Duration: %.2f ms Phase: init Status: timeout",
			float64(time.Since(start).Microseconds())/1000.0))
		old := e.currentHandle()
		old.Cancel()
		<-old.Exited()
		e.resetStderr()
		if err := e.place(ctx); err != nil {
			return lambdaInitFailed, time.Since(start), lambdaErrorPayload(fmt.Sprintf("Container start failed: %v", err))
		}
		return lambdaInitRetried, 0, nil
	}
}

// lambdaInvocationResult is how one invocation on an environment ended.
type lambdaInvocationResult struct {
	payload   []byte
	unhandled bool
	// reusable reports that the runtime finished the invocation and asked
	// for the next one, so the environment serves again. Anything else — a
	// crash, a timeout, an interruption — resets it: "If the Lambda function
	// crashes or times out during the Invoke phase, Lambda resets the
	// execution environment."
	reusable bool
}

// invoke runs one invocation on the environment and writes its START, END and
// REPORT entries to the environment's log stream.
func (e *lambdaEnvironment) invoke(ctx context.Context, fn LambdaFunction, payload []byte) lambdaInvocationResult {
	inv := newLambdaInvocation(fn, payload)
	version := fn.Version
	if version == "" {
		version = "$LATEST"
	}
	appendLambdaLog(e.logKey, time.Now().UnixMilli(), fmt.Sprintf("START RequestId: %s Version: %s", inv.RequestID, version))
	end := func(r lambdaInvocationResult, invokeDuration, initDuration time.Duration) lambdaInvocationResult {
		endMs := time.Now().UnixMilli()
		appendLambdaLog(e.logKey, endMs, fmt.Sprintf("END RequestId: %s", inv.RequestID))
		appendLambdaLog(e.logKey, endMs+1, lambdaReportLine(inv.RequestID, invokeDuration, initDuration,
			fn.MemorySize, lambdaEnvironmentMemoryPeak(e.currentHandle())))
		if r.unhandled {
			appendLambdaLog(e.logKey, endMs+2, fmt.Sprintf("ERROR RequestId: %s %s", inv.RequestID, strings.TrimSpace(string(r.payload))))
		}
		return r
	}
	interrupted := lambdaInvocationResult{payload: lambdaErrorPayload("Invocation interrupted: the simulator is shutting down"), unhandled: true}

	var initDuration time.Duration
	retried := false
	if e.initialised {
		e.resetStderr()
	} else {
		outcome, duration, failure := e.initPhase(ctx)
		switch outcome {
		case lambdaInitInterrupted:
			appendLambdaLog(e.logKey, time.Now().UnixMilli(), fmt.Sprintf("END RequestId: %s", inv.RequestID))
			return interrupted
		case lambdaInitFailed:
			return end(lambdaInvocationResult{payload: failure, unhandled: true}, 0, duration)
		case lambdaInitRetried:
			retried = true
		default:
			initDuration = duration
		}
	}

	// "The function's timeout setting limits the duration of the entire
	// Invoke phase", which a retried Init phase shares, so the timer starts
	// now rather than when the container did.
	invokeStart := time.Now()
	timer := time.NewTimer(time.Duration(inv.TimeoutSec) * time.Second)
	defer timer.Stop()
	timedOut := func() lambdaInvocationResult {
		return end(lambdaInvocationResult{payload: lambdaErrorPayload(fmt.Sprintf("Task timed out after %d.00 seconds", inv.TimeoutSec)), unhandled: true},
			time.Since(invokeStart), initDuration)
	}
	crashed := func() lambdaInvocationResult {
		return end(lambdaInvocationResult{payload: lambdaErrorPayload(fmt.Sprintf(
			"Runtime exited without providing a reason (exit %d): %s", e.exitCode(), e.stderrText())), unhandled: true},
			time.Since(invokeStart), initDuration)
	}
	if retried {
		select {
		case <-ctx.Done():
			return interrupted
		case <-e.api.ready:
			e.initialised = true
		case <-e.api.initFailed:
			return end(lambdaInvocationResult{payload: e.api.initError, unhandled: true}, time.Since(invokeStart), 0)
		case <-e.Exited():
			return crashed()
		case <-timer.C:
			return timedOut()
		}
	}

	e.api.dispatch(inv)
	select {
	case <-ctx.Done():
		return interrupted
	case <-e.Exited():
		return crashed()
	case <-timer.C:
		return timedOut()
	case <-inv.done:
	}
	result := lambdaInvocationResult{payload: inv.response}
	if inv.errorObj != nil {
		result = lambdaInvocationResult{payload: inv.errorObj, unhandled: true}
	} else if len(result.payload) == 0 {
		result.payload = []byte("{}")
	}
	// The reply is the invocation's result; the Invoke phase it belongs to
	// ends when the runtime asks for its next invocation, and an environment
	// whose runtime never does is reset rather than reused.
	select {
	case <-inv.ended:
		result.reusable = true
	case <-e.Exited():
	case <-timer.C:
	case <-ctx.Done():
	}
	return end(result, time.Since(invokeStart), initDuration)
}

// invokeLambdaViaRuntimeAPI runs one invocation of fn on an execution
// environment of its version: an idle one when there is one, else a new one,
// within the function's concurrency. It returns the reply the runtime posted
// to the Runtime API — unhandled when the runtime posted an error, crashed or
// timed out — or the service exception AWS Lambda answers instead, such as
// TooManyRequestsException when no concurrency is left.
//
// Once ctx is done the invocation resets its execution environment and returns
// at once, unhandled: a simulator shutting down does not wait out a function's
// timeout, and the caller that owns ctx decides what an interrupted invocation
// means — a retry the next process makes, or a message that becomes visible
// again.
func invokeLambdaViaRuntimeAPI(ctx context.Context, fn LambdaFunction, payload []byte) ([]byte, bool, *lambdaServiceError) {
	if fn.Code == nil {
		return lambdaErrorPayload("Function has no deployment package"), true, nil
	}
	var lease *funcrt.Lease[*lambdaEnvironment]
	var err error
	for range 3 {
		lease, err = lambdaEnvironments.pool(fn).Acquire(ctx)
		if !errors.Is(err, funcrt.ErrRetired) {
			break
		}
	}
	if err != nil {
		var throttled *funcrt.ThrottledError
		var start *lambdaStartError
		switch {
		case errors.As(err, &throttled):
			return lambdaErrorPayload("Rate Exceeded."), true, lambdaThrottle(throttled)
		case errors.As(err, &start):
			lambdaLogStartFailure(fn, start)
			return start.payload, true, start.service
		}
		return lambdaErrorPayload(err.Error()), true, nil
	}
	result := lease.Env().invoke(ctx, fn, payload)
	if result.reusable {
		lease.Release()
	} else {
		lease.Discard()
	}
	lambdaEnvironmentChanges.Announce(fn.FunctionName)
	return result.payload, result.unhandled, nil
}

// lambdaLogStartFailure records an invocation whose execution environment
// could not be set up, in a log stream of its own.
func lambdaLogStartFailure(fn LambdaFunction, start *lambdaStartError) {
	_, _, logKey := createLambdaLogStream(fn)
	requestID := sim.NewUUID()
	version := fn.Version
	if version == "" {
		version = "$LATEST"
	}
	now := time.Now().UnixMilli()
	appendLambdaLog(logKey, now, fmt.Sprintf("START RequestId: %s Version: %s", requestID, version))
	appendLambdaLog(logKey, now+1, fmt.Sprintf("ERROR RequestId: %s %s", requestID, strings.TrimSpace(string(start.payload))))
	appendLambdaLog(logKey, now+2, fmt.Sprintf("END RequestId: %s", requestID))
}

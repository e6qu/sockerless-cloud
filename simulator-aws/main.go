// Command simulator-aws runs the AWS service simulator.
//
// It simulates the subset of AWS APIs used by the Sockerless ECS and Lambda
// backends: ECS, ECR, CloudWatch Logs, EFS, Cloud Map, Lambda, S3, EC2, IAM, and STS.
//
// Configure with environment variables:
//
//	SIM_LISTEN_ADDR  — listen address (default ":4566")
//	SIM_TLS_CERT     — TLS certificate file (optional)
//	SIM_TLS_KEY      — TLS key file (optional)
//	SIM_RUNTIME      — "docker" by default; "process" starts API-only mode for runs that do not execute workloads
//	SIM_LOG_LEVEL    — log level: trace, debug, info, warn, error (default "info")
//
// SDK configuration:
//
//	export AWS_ENDPOINT_URL=http://localhost:4566
//	export AWS_ACCESS_KEY_ID=test
//	export AWS_SECRET_ACCESS_KEY=test
//	export AWS_DEFAULT_REGION=us-east-1
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

func main() {
	if sim.RunContainerReaper() {
		return
	}
	// A simulator started by a test harness must not outlive it. The harness
	// stops it from its own cleanup, which a killed `go test` never reaches —
	// and the reaper waits on the simulator, so both would linger.
	sim.ExitWithParent()
	cfg := sim.ConfigFromEnv("aws")
	if cfg.ListenAddr == ":8443" {
		cfg.ListenAddr = ":4566" // AWS simulator default port
	}

	// Override from SIM_AWS_PORT if set
	if port := os.Getenv("SIM_AWS_PORT"); port != "" {
		cfg.ListenAddr = ":" + port
	}

	// Stash the listen addr so cloud-product translators can wire
	// AWS_EC2_METADATA_SERVICE_ENDPOINT + ECS_CONTAINER_METADATA_URI_V4
	// onto workload containers.
	simListenAddr = cfg.ListenAddr

	obs, err := sim.InitObservability("sockerless-sim-aws")
	if err != nil {
		log.Fatalf("init observability: %v", err)
	}
	defer func() { _ = obs.Shutdown(context.Background()) }()
	cfg.LogWriter = obs.LogWriter

	srv, _, _, err := buildSimulator(cfg)
	if err != nil {
		log.Fatalf("simulator startup: %v", err)
	}

	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// buildSimulator constructs the simulator server and registers every
// simulated AWS service on it. Split from main so the spec-conformance
// tests can build the full route / operation table in-process and
// validate it against the vendored Smithy models (specs/cloud-api/aws/).
func buildSimulator(cfg sim.Config) (*sim.Server, *sim.AWSRouter, *sim.AWSQueryRouter, error) {
	return buildSimulatorWithOptions(cfg, simulatorBuildOptions{startBackgroundEvaluators: true})
}

type simulatorBuildOptions struct {
	startBackgroundEvaluators bool
}

func buildSimulatorWithOptions(cfg sim.Config, options simulatorBuildOptions) (*sim.Server, *sim.AWSRouter, *sim.AWSQueryRouter, error) {
	srv, err := sim.NewServer(cfg)
	if err != nil {
		return nil, nil, nil, err
	}

	// Runtime wire-shape validation (armed only when
	// SOCKERLESS_SPEC_VALIDATE is set; see spec_validator.go). Wraps the
	// handler chain before any host-addressed data plane does: traffic a
	// data plane intercepts (ELBv2 listeners) is not a Smithy-modeled
	// surface, and validating it against whatever mux pattern its path
	// happens to resemble would misattribute it.
	if err := armSpecValidator(srv); err != nil {
		return nil, nil, nil, err
	}

	// Register AWS JSON services (X-Amz-Target header routing)
	awsRouter := sim.NewAWSRouter()
	// Secrets Manager replication restores replica resource policies while its
	// durable store is opened, so the shared IAM resource-policy store must
	// exist before any service slice performs recovery.
	registerIAMResourcePolicies(srv)
	registerECS(awsRouter, srv)
	registerECR(awsRouter, srv)
	registerCloudWatchLogs(awsRouter, srv)
	registerCloudWatchMetricsJSON(awsRouter)
	registerCloudWatchMonitoringExtra(awsRouter, srv)
	registerCloudWatchAlarmsJSON(awsRouter)
	registerCloudWatchLogAlarmsJSON(awsRouter)
	registerCloudWatchAlarmOpsJSON(awsRouter)
	registerCloudWatchMetricStreamsJSON(awsRouter)
	registerCloudWatchAnomalyInsightJSON(awsRouter)
	registerCloudWatchMiscJSON(awsRouter)
	registerCloudWatchDashboardsJSON(awsRouter)
	registerCloudMap(awsRouter, srv)
	registerSecretsManager(awsRouter, srv)
	registerSSMParameterStore(awsRouter, srv)
	registerSSMDocuments(awsRouter, srv)
	registerSSMMaintenanceWindows(awsRouter, srv)
	registerSSMPatchBaselines(awsRouter, srv)
	registerSSMServiceSettings(awsRouter, srv)
	registerSSMResourceDataSync(awsRouter, srv)
	registerSSMAssociations(awsRouter, srv)
	registerSSMOpsItems(awsRouter, srv)
	registerSSMInventory(awsRouter, srv)
	registerSSMPatchOps(awsRouter, srv)
	registerSSMCloudConnectors(awsRouter, srv)
	registerKMS(awsRouter, srv)
	registerDynamoDB(awsRouter, srv)
	registerACM(awsRouter, srv)
	registerWAFv2(awsRouter, srv)
	registerEventBridge(awsRouter, srv)
	registerKinesis(awsRouter, srv)
	registerKinesisStreaming(awsRouter, srv)
	registerCloudTrail(awsRouter, srv)
	registerCloudTrailLake(awsRouter, srv)
	registerStepFunctions(awsRouter, srv)
	registerCodeBuild(awsRouter, srv)
	registerCodeBuildExtended(awsRouter, srv)
	if options.startBackgroundEvaluators {
		if err := cbRecoverBuilds(); err != nil {
			return nil, nil, nil, fmt.Errorf("restore AWS CodeBuild builds: %w", err)
		}
	}
	registerGlue(awsRouter, srv)
	registerApplicationAutoScaling(awsRouter, srv, options.startBackgroundEvaluators)
	registerBudgets(awsRouter, srv)

	// Register AWS Query Protocol services (Action form parameter routing)
	queryRouter := sim.NewAWSQueryRouter()
	registerEC2(queryRouter, srv)
	registerIAM(queryRouter, srv)
	registerSTS(queryRouter, srv)
	registerSNS(queryRouter, srv)
	registerRDS(queryRouter, srv)
	registerElastiCache(queryRouter, srv)
	registerELBv2(queryRouter, srv)
	registerAutoScaling(queryRouter, srv)
	registerCloudWatchMetricsQuery(queryRouter)
	registerCloudWatchAlarmsQuery(queryRouter)
	registerCloudWatchAlarmOpsQuery(queryRouter)
	registerCloudWatchMetricStreamsQuery(queryRouter)
	registerCloudWatchAnomalyInsightQuery(queryRouter)
	registerCloudWatchMiscQuery(queryRouter)
	registerCloudWatchDashboardsQuery(queryRouter)
	sfnAWSQueryRouter = queryRouter

	// Host-addressed service data planes are registered outside the
	// control-plane routers, AFTER armSpecValidator: a later WrapHandler
	// wraps outside an earlier one, so data-plane traffic is intercepted
	// before the validator (and the mux) ever see it.
	registerELBv2DataPlane(srv)
	registerAmplifyDataPlane(srv)

	// SQS migrated from awsQuery to awsJson1_0 in late 2023. Route
	// it via the JSON router (X-Amz-Target: AmazonSQS.<Op>).
	registerSQS(awsRouter, srv)
	registerSQSMessageMove(awsRouter, srv)
	registerOrganizations(awsRouter, srv)
	registerOrganizationsExtra(awsRouter, srv)

	// POST / handler: check X-Amz-Target first (JSON protocol),
	// fall back to Action parameter (Query Protocol)
	srv.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		// Buffer the request body so the CloudTrail recorder can extract the
		// resources[] the call acted on after the handler has consumed it.
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		rec := &cloudTrailStatusRecorder{ResponseWriter: w}
		// SigV4 authentication: verify the request signature against the
		// presented principal's stored secret before any identity is trusted.
		// Runs before authorization so a forged Credential= can never reach
		// the policy evaluator. Web-identity / SAML assume-role calls are
		// unsigned by design and exempt.
		if !sigv4EnforceControlPlane(rec, r, body) {
			return
		}
		// Call-time IAM enforcement: deny before dispatch when the caller's
		// registered credential lacks the action. Runs after authentication,
		// so the resolved principal is one the caller proved possession of.
		if !iamEnforce(rec, r) {
			return
		}
		if r.Header.Get("X-Amz-Target") != "" {
			awsRouter.ServeHTTP(rec, r)
			cloudTrailRecordAPICall(srv, r, body, rec.statusCode(), rec.Header(), rec.body.Bytes())
			return
		}
		queryRouter.ServeHTTP(rec, r)
		cloudTrailRecordAPICall(srv, r, body, rec.statusCode(), rec.Header(), rec.body.Bytes())
	})

	// Smithy RPCv2 CBOR services (path-based routing)
	registerCloudWatchMetrics(srv, options.startBackgroundEvaluators)

	// REST-based services register directly on the server mux
	registerEFS(srv)
	registerLambda(srv, options.startBackgroundEvaluators)
	registerS3(srv)
	registerFirehose(awsRouter, srv)
	registerACMPrivateCA(awsRouter, srv)
	registerCloudFront(srv)
	registerRoute53(srv, options.startBackgroundEvaluators)
	registerAmplify(srv)
	registerAPIGateway(srv)
	registerAPIGatewayV2(srv)
	registerHostMetadata(srv)

	// Batch — REST/JSON protocol (path-based, no X-Amz-Target)
	registerBatch(srv)
	if options.startBackgroundEvaluators {
		if err := recoverECSTasks(); err != nil {
			return nil, nil, nil, fmt.Errorf("restore Amazon Elastic Container Service tasks: %w", err)
		}
		recoverECSServiceTasks()
	}

	// EventBridge Scheduler — REST/JSON protocol (path-based, no X-Amz-Target)
	registerScheduler(srv)

	// Dashboard summary endpoints for UI

	// Embedded UI (no-op with -tags noui)
	registerUI(srv)

	if options.startBackgroundEvaluators {
		if err := recoverLambdaInvocations(); err != nil {
			return nil, nil, nil, fmt.Errorf("restore AWS Lambda invocations: %w", err)
		}
		recoverStepFunctionsExecutions()
		recoverLambdaDurableExecutions()
		if err := recoverAmplifyComputeInstances(); err != nil {
			return nil, nil, nil, fmt.Errorf("restore AWS Amplify Hosting compute: %w", err)
		}
		recoverAmplifyJobs()
	}

	return srv, awsRouter, queryRouter, nil
}

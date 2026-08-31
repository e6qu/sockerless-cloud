// Command simulator-gcp runs the GCP service simulator.
//
// It simulates the subset of GCP APIs used by the Sockerless Cloud Run and
// Cloud Functions backends: Cloud Run Jobs, Cloud Logging, Cloud DNS, GCS,
// Artifact Registry, and Cloud Functions v2.
//
// Configure with environment variables:
//
//	SIM_LISTEN_ADDR     — HTTP listen address (default ":4567")
//	SIM_GCP_GRPC_PORT   — gRPC listen port for Cloud Logging (default: HTTP port + 1)
//	SIM_TLS_CERT        — TLS certificate file (optional)
//	SIM_TLS_KEY         — TLS key file (optional)
//	SIM_RUNTIME         — "docker" by default; "process" starts API-only mode for runs that do not execute workloads
//	SIM_LOG_LEVEL       — log level: trace, debug, info, warn, error (default "info")
//
// SDK configuration:
//
//	option.WithEndpoint("http://localhost:4567")
//	option.WithoutAuthentication()
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	"google.golang.org/grpc"
)

func main() {
	if sim.RunContainerReaper() {
		return
	}
	// A simulator started by a test harness must not outlive it. The harness
	// stops it from its own cleanup, which a killed `go test` never reaches —
	// and the reaper waits on the simulator, so both would linger.
	sim.ExitWithParent()
	cfg := sim.ConfigFromEnv("gcp")
	if cfg.ListenAddr == ":8443" {
		cfg.ListenAddr = ":4567" // GCP simulator default port
	}

	if port := os.Getenv("SIM_GCP_PORT"); port != "" {
		cfg.ListenAddr = ":" + port
	}

	// Stash the listen addr so cloud-product host translators can wire
	// GCE_METADATA_HOST + sidecar URLs onto workload containers.
	simListenAddr = cfg.ListenAddr

	obs, err := sim.InitObservability("sockerless-sim-gcp")
	if err != nil {
		log.Fatalf("init observability: %v", err)
	}
	defer func() { _ = obs.Shutdown(context.Background()) }()
	cfg.LogWriter = obs.LogWriter

	srv, err := buildSimulator(cfg)
	if err != nil {
		log.Fatalf("simulator startup: %v", err)
	}

	// Enforce data-plane authentication on the served surface: every request
	// carries a valid OAuth2 access token the simulator itself minted, verified
	// against the simulator's signing key, unless it targets an exempt surface
	// (token minters, OpenID Connect discovery/JWKS, health, GCE metadata, the
	// console, application monitoring, or the OCI registry). Applied here so it
	// is the outermost wrap on the final handler chain.
	srv.WrapHandler(bearerAuthMiddleware(srv))

	// Start gRPC server for Cloud Logging
	grpcPort := grpcPortFromConfig(cfg.ListenAddr)
	if p := os.Getenv("SIM_GCP_GRPC_PORT"); p != "" {
		grpcPort = p
	}
	go startGRPCServer(grpcPort)

	// Cloud Spanner backup schedules produce real backups on their crontab
	// occurrences, and Cloud Pub/Sub returns messages whose ack deadline has
	// elapsed to their subscription. Both clocks run only in the serving
	// process — building the route table in-process (route conformance,
	// coverage probing) must not start one.
	go spannerRunBackupScheduleLoop()
	pubsubStartAckDeadlineSweeper()

	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// buildSimulator constructs the simulator server and registers every
// simulated GCP service on it. Split from main so the spec-conformance
// tests can build the full route table in-process and validate it
// against the vendored Discovery documents (specs/cloud-api/gcp/).
func buildSimulator(cfg sim.Config) (*sim.Server, error) {
	srv, err := sim.NewServer(cfg)
	if err != nil {
		return nil, err
	}

	// Load or generate the key every access-token minter signs with and the
	// data-plane bearer middleware verifies against. With persistence enabled
	// the key survives restarts so pre-restart tokens stay verifiable. Fails
	// loud rather than serving unverifiable tokens.
	if err := initAccessTokenSigner(srv.DB()); err != nil {
		return nil, err
	}

	// Initialise the regional CPU quota tracker before route registration —
	// CreateService / UpdateService / CreateFunction handlers debit against
	// the singleton. SIM_GCP_CPU_QUOTA_PER_REGION env wires the budget;
	// unset/zero disables quota enforcement.
	initRegionalCPUQuota()

	// Register GCP service routes (HTTP/REST)
	registerCloudRunJobs(srv)
	registerCloudRunV1Jobs(srv)
	registerCloudRun(srv)
	registerCloudRunServicesV2(srv)
	registerCloudRunWorkerPoolsV2(srv)
	registerCloudRunInstancesV2(srv)
	registerCloudRunV1InstancesWorkerPools(srv)
	registerCloudLogging(srv)
	registerCloudDNS(srv)
	registerGCS(srv)
	registerArtifactRegistry(srv)
	registerCloudFunctions(srv)
	registerOperations(srv)
	registerSecretManager(srv)
	registerCloudKMS(srv)
	registerPubSub(srv)
	registerEventarc(srv)
	registerMemorystoreRedis(srv)
	registerGCPAPIGateway(srv)
	registerCloudSQL(srv)
	registerBigQuery(srv)
	registerFirestore(srv)
	registerCloudBuild(srv)
	registerSpanner(srv)
	registerDataflow(srv)
	registerBigtable(srv)

	// Infrastructure services
	registerServiceUsage(srv)
	registerCompute(srv)
	registerComputeMore(srv)
	registerComputeMore2(srv)
	registerComputeMore4(srv)
	registerComputeWireGroups(srv)
	registerComputeURLMapVerbs(srv)
	registerComputeSettings(srv)
	registerComputeProject(srv)
	registerComputeCatalogs(srv)
	registerComputeRouterNamedSets(srv)
	registerComputeOrganizationOperations(srv)
	computeSignedURLKeys(srv, "backendBuckets", gcpComputeBackendBuckets)
	registerComputeGlobalLBVerbs(srv)
	registerComputeBulkVerbs(srv)
	registerComputeReads(srv)
	registerComputeLastVerbs(srv)
	registerComputeTypedWriteVerbs(srv, "urlMaps", gcpURLMaps, map[string]string{"PATCH": "patch", "PUT": "update"})
	registerComputeTypedWriteVerbs(srv, "healthChecks", gcpHealthChecks, map[string]string{"PATCH": "patch", "PUT": "update"})
	registerComputeRegionalPublicDelegatedPrefixes(srv)
	registerComputePolicies(srv)
	registerComputeMemberVerbs(srv)
	registerComputeReservationVerbs(srv, gcpComputeReservations)
	registerComputeDiskVerbs(srv)
	registerComputeMore3(srv)
	registerComputePacketMirroring(srv)
	registerVPCAccess(srv)
	registerIAM(srv)
	registerOAuth2(srv)
	registerSTS(srv)
	registerComputeMetadata(srv)
	registerTokenDiscovery(srv)

	// Dashboard summary endpoints for UI

	// Embedded UI (no-op with -tags noui)
	registerUI(srv)

	// Runtime wire-shape validation (armed only when
	// SOCKERLESS_SPEC_VALIDATE is set; see spec_validator.go).
	if err := armSpecValidator(srv); err != nil {
		return nil, err
	}

	return srv, nil
}

// grpcPortFromConfig derives the gRPC port from the HTTP listen address.
// Default: HTTP port + 1.
func grpcPortFromConfig(listenAddr string) string {
	// Extract port from ":4567" or "0.0.0.0:4567"
	_, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		// Might be just ":4567"
		portStr = strings.TrimPrefix(listenAddr, ":")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "4568"
	}
	return strconv.Itoa(port + 1)
}

// registerAllGRPCServices mounts every gRPC service the simulator serves.
// The coverage ratchet calls this same function, so a service cannot be
// added to the server without the gate seeing it.
func registerAllGRPCServices(gs *grpc.Server) {
	registerCloudLoggingGRPC(gs)
	registerBigtableGRPC(gs)
	registerBigtableDataGRPC(gs)
	registerFirestoreGRPC(gs)
	registerPubSubGRPC(gs)
	registerSpannerGRPC(gs)
	registerCloudKMSGRPC(gs)
	registerSecretManagerGRPC(gs)
}

func startGRPCServer(port string) {
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("gRPC: failed to listen on :%s: %v", port, err)
	}

	gs := grpc.NewServer()
	registerAllGRPCServices(gs)

	fmt.Fprintf(os.Stderr, "  gRPC Cloud Logging, Bigtable Admin + Data, Firestore, Pub/Sub, Spanner, Cloud KMS, Secret Manager on :%s\n", port)
	if err := gs.Serve(lis); err != nil {
		log.Fatalf("gRPC: failed to serve: %v", err)
	}
}

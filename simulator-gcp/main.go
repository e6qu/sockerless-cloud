// Command simulator-gcp runs the Google Cloud simulator: a local,
// wire-faithful reimplementation of a slice of the Google Cloud public API,
// served over REST on one port and gRPC on the next to the Google Cloud client
// libraries, gcloud and terraform-provider-google. Every service slice
// registers in buildSimulator below.
//
// Configure with environment variables:
//
//	SIM_LISTEN_ADDR     — HTTP listen address (default ":4567")
//	SIM_GCP_PORT        — HTTP port to listen on, ahead of SIM_LISTEN_ADDR
//	SIM_GCP_GRPC_PORT   — gRPC listen port (default: HTTP port + 2, so the default coexists with the Azure simulator's :4568)
//	SIM_GCP_GRPC_TLS_CERT — TLS certificate file the gRPC port serves with (optional; with SIM_GCP_GRPC_TLS_KEY the port serves TLS only)
//	SIM_GCP_GRPC_TLS_KEY  — TLS key file for SIM_GCP_GRPC_TLS_CERT
//	SIM_TLS_CERT        — TLS certificate file (optional)
//	SIM_TLS_KEY         — TLS key file (optional)
//	SIM_CALLBACK_LISTEN_ADDR — plain-HTTP address the workloads' metadata requests reach beside a TLS listener (default: a free port on every interface)
//	SIM_RUNTIME         — "docker" by default; "process" starts API-only mode for runs that do not execute workloads
//	SIM_LOG_LEVEL       — log level: trace, debug, info, warn, error (default "info")
//
// SDK configuration: the endpoint, and the bootstrap service account's key
// as the credential (see iam_bootstrap.go).
//
//	option.WithEndpoint("http://localhost:4567")
//	option.WithAuthCredentialsFile(option.ServiceAccount, "bootstrap-key.json"),
//	which go run ./testutil/cmd/gcp-bootstrap-key writes.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc"
)

func main() {
	sim.Main(sim.Program{
		Provider:    "gcp",
		DefaultPort: "4567",
		Configure: func(cfg *sim.Config) error {
			// Images at the simulator's own port are its registry's.
			simListenAddr = cfg.ListenAddr
			return gcpConfigurePlacement(cfg)
		},
		Build: func(cfg sim.Config) (*sim.Server, error) {
			// Workloads reach the metadata server and the sidecars at the
			// plain-HTTP address the simulator serves them on.
			simCallbackListenAddr = cfg.CallbackListenAddr
			srv, err := buildSimulator(cfg)
			if err != nil {
				return nil, err
			}
			// Every request carries an OAuth2 access token the simulator
			// minted, unless it targets an exempt surface (token minters,
			// OpenID Connect discovery and JWKS, health, GCE metadata, the
			// console, application monitoring, the OCI registry). The
			// outermost wrap, so it sees the final handler chain. Inside it,
			// IAM policy decides every authenticated call.
			srv.WrapHandler(iamGateMiddleware(srv))
			srv.WrapHandler(bearerAuthMiddleware(srv))
			return srv, nil
		},
		Start: startGCPListeners,
	})
}

// startGCPListeners starts what serves beside the HTTP port, and the clocks
// that run only in the serving process: building the route table in-process
// (route conformance, coverage probing) must not start one.
func startGCPListeners(srv *sim.Server, cfg sim.Config) (func(context.Context) error, error) {
	grpcPort := grpcPortFromConfig(cfg.ListenAddr)
	if p := os.Getenv("SIM_GCP_GRPC_PORT"); p != "" {
		grpcPort = p
	}
	if err := startGRPCServer(srv, grpcPort); err != nil {
		return nil, err
	}
	dnsSrv, err := startCloudDNSServer()
	if err != nil {
		return nil, fmt.Errorf("cloud dns: %w", err)
	}
	if err := gcpStartPlacement(context.Background()); err != nil {
		return nil, err
	}
	gcpAdoptComputeInstances(context.Background())
	// Cloud Spanner backup schedules produce real backups on their crontab
	// occurrences, and Cloud Pub/Sub dead-letters messages whose last delivery
	// attempt ran out of ack deadline and pushes push subscriptions' backlogs.
	spannerBackupScheduleTicker().Start(srv, "Cloud Spanner backup schedules", 30*time.Second)
	srv.StartBackground("Pub/Sub dead-letter sweeper", pubsubDeadLetterSweeper)
	startPubSubPush(srv)
	startCloudScheduler(srv)
	startCloudTasks()
	resumeCloudRunWorkloads()
	return func(context.Context) error {
		gcpStopPlacement(cfg.Persist)
		gcpStopNATEgresses()
		if networkSidecars != nil {
			networkSidecars.Close()
		}
		return dnsSrv.Close()
	}, nil
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
	gcpServer = srv

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
	initMediaUploadSessions(srv)

	// Register GCP service routes (HTTP/REST)
	registerCloudRunJobs(srv)
	registerCloudRunV1Jobs(srv)
	registerCloudRun(srv)
	registerCloudRunExport(srv)
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
	registerDiscoveryService(srv)
	registerFirestore(srv)
	registerFirestoreChangeStreams(srv)
	registerCloudBuild(srv)
	registerSpanner(srv)
	registerDataflow(srv)
	registerDataflowConfigStore(srv)
	registerBigtable(srv)

	// Infrastructure services
	registerServiceUsage(srv)
	registerCompute(srv)
	registerComputeMore(srv)
	registerComputeMore2(srv)
	registerComputeMore4(srv)
	registerComputeInterconnectMacsec(srv)
	registerComputePreviewFeatures(srv)
	registerComputeWireGroups(srv)
	registerComputeURLMapVerbs(srv)
	registerComputeSettings(srv)
	registerComputeProject(srv)
	registerComputeCatalogs(srv)
	registerComputeInterconnectDiagnostics(srv)
	registerComputeInterconnectLocations(srv)
	registerComputeInterconnectRemoteLocations(srv)
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
	registerComputeReservationHosts(srv, gcpComputeReservations)
	registerComputeDiskVerbs(srv)
	registerComputeMore3(srv)
	registerComputePacketMirroring(srv)
	registerVPCAccess(srv)
	registerCloudScheduler(srv)
	registerCloudTasks(srv)
	registerIAM(srv)
	if err := iamSeedBootstrapServiceAccount(); err != nil {
		log.Fatalf("seed the bootstrap service account: %v", err)
	}
	registerOAuth2(srv)
	registerSTS(srv)
	registerComputeMetadata(srv)
	registerTokenDiscovery(srv)

	// Audit the calls of every service above; mounted after them so it wraps
	// their front ends too.
	if err := registerAuditLogs(srv); err != nil {
		return nil, err
	}

	if err := registerUI(srv, cfg.ConsoleDir); err != nil {
		return nil, err
	}

	// Runtime wire-shape validation (armed only when
	// SOCKERLESS_SPEC_VALIDATE is set; see spec_validator.go).
	if err := armSpecValidator(srv); err != nil {
		return nil, err
	}

	return srv, nil
}

// grpcPortFromConfig derives the gRPC port from the HTTP listen address: the
// HTTP port plus two. Plus one would put the default at :4568, which is the
// Azure simulator's default HTTP port, and the two cannot both bind it on one
// host.
func grpcPortFromConfig(listenAddr string) string {
	_, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		portStr = strings.TrimPrefix(listenAddr, ":")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "4569"
	}
	return strconv.Itoa(port + 2)
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

// newGRPCServer is the gRPC server with every service mounted, verifying each
// call's credential before it runs.
func newGRPCServer() *grpc.Server {
	gs := grpc.NewServer(
		grpc.ChainUnaryInterceptor(authUnaryInterceptor, auditUnaryInterceptor, iamGateUnaryInterceptor),
		grpc.ChainStreamInterceptor(authStreamInterceptor, iamGateStreamInterceptor),
	)
	registerAllGRPCServices(gs)
	return gs
}

// grpcFrontEnd routes a TLS front end's requests: gRPC calls to gs, the rest
// to the REST surface.
func grpcFrontEnd(srv *sim.Server, gs *grpc.Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			gs.ServeHTTP(w, r)
			return
		}
		srv.ServeHTTP(w, r)
	})
}

// startGRPCServer serves the gRPC surface on the server's lifecycle, so a
// shutdown stops it with the other background workers. Every call carries an
// OAuth2 access token the simulator minted, as on the REST port.
//
// With a certificate configured the port is a TLS front end like Google's,
// where one host answers both protocols: HTTP/2 requests of content type
// application/grpc reach the gRPC services and every other request reaches the
// REST surface. A client whose endpoint setting names one host for both, as
// the Terraform provider's bigtable_custom_endpoint does, then reaches both.
// Google's client libraries send an access token only over TLS, so this is the
// port they authenticate on.
func startGRPCServer(srv *sim.Server, port string) error {
	certFile, keyFile := os.Getenv("SIM_GCP_GRPC_TLS_CERT"), os.Getenv("SIM_GCP_GRPC_TLS_KEY")
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("gRPC: SIM_GCP_GRPC_TLS_CERT and SIM_GCP_GRPC_TLS_KEY must be set together")
	}
	var tlsConfig *tls.Config
	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return fmt.Errorf("gRPC: load the TLS certificate: %w", err)
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return fmt.Errorf("gRPC: listen on :%s: %w", port, err)
	}

	gs := newGRPCServer()

	scheme := "plaintext"
	if tlsConfig != nil {
		scheme = "TLS"
	}
	fmt.Fprintf(os.Stderr, "  gRPC Cloud Logging, Bigtable Admin + Data, Firestore, Pub/Sub, Spanner, Cloud KMS, Secret Manager on :%s (%s)\n", port, scheme)
	if tlsConfig == nil {
		srv.StartBackground("gRPC server", func(ctx context.Context) {
			stop := context.AfterFunc(ctx, gs.Stop)
			defer stop()
			if err := gs.Serve(lis); err != nil {
				log.Fatalf("gRPC: failed to serve: %v", err)
			}
		})
		return nil
	}
	front := &http.Server{
		Handler:           grpcFrontEnd(srv, gs),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 30 * time.Second,
	}
	srv.StartBackground("gRPC server", func(ctx context.Context) {
		stop := context.AfterFunc(ctx, func() {
			if err := front.Close(); err != nil {
				log.Printf("gRPC: close the TLS front end: %v", err)
			}
			gs.Stop()
		})
		defer stop()
		if err := front.Serve(tls.NewListener(lis, tlsConfig)); err != nil && err != http.ErrServerClosed {
			log.Fatalf("gRPC: failed to serve: %v", err)
		}
	})
	return nil
}

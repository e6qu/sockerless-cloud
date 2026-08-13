// Command simulator-azure runs the Azure service simulator.
//
// It simulates the subset of Azure APIs used by the Sockerless ACA and
// Azure Functions backends: Container Apps Jobs, Azure Monitor, Azure Files,
// ACR, Private DNS, Azure Functions, and Application Insights.
//
// Configure with environment variables:
//
//	SIM_LISTEN_ADDR  — listen address (default ":4568")
//	SIM_TLS_CERT     — TLS certificate file (optional)
//	SIM_TLS_KEY      — TLS key file (optional)
//	SIM_RUNTIME      — "docker" by default; "process" starts API-only mode for runs that do not execute workloads
//	SIM_SERVICEBUS_AMQP_LISTEN_ADDR — raw Service Bus AMQP/TLS listen address (optional)
//	SIM_LOG_LEVEL    — log level: trace, debug, info, warn, error (default "info")
//
// SDK configuration:
//
//	Use custom cloud.Configuration with ARM endpoint http://localhost:4568
package main

import (
	"context"
	"log"
	"os"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

func main() {
	if sim.RunContainerReaper() {
		return
	}
	cfg := sim.ConfigFromEnv("azure")
	if cfg.ListenAddr == ":8443" {
		cfg.ListenAddr = ":4568" // Azure simulator default port
	}

	if port := os.Getenv("SIM_AZURE_PORT"); port != "" {
		cfg.ListenAddr = ":" + port
	}
	if _, err := azureAdvertisedEndpointConfigFromEnv(); err != nil {
		log.Fatal(err)
	}

	// Stash listen addr so cloud-product translators can wire
	// IDENTITY_ENDPOINT + IMDS env onto workload containers.
	simListenAddr = cfg.ListenAddr

	obs, err := sim.InitObservability("sockerless-sim-azure")
	if err != nil {
		log.Fatalf("init observability: %v", err)
	}
	defer func() { _ = obs.Shutdown(context.Background()) }()
	cfg.LogWriter = obs.LogWriter

	srv, err := buildSimulator(cfg)
	if err != nil {
		log.Fatalf("simulator startup: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if addr, enabled, err := startAzureDNSFromEnv(ctx); err != nil {
		log.Fatal(err)
	} else if enabled {
		log.Printf("Azure simulator DNS listening on %s", addr)
	}
	if amqpAddr := os.Getenv("SIM_SERVICEBUS_AMQP_LISTEN_ADDR"); amqpAddr != "" {
		certFile := envOrDefault("SIM_SERVICEBUS_AMQP_TLS_CERT", cfg.TLSCert)
		keyFile := envOrDefault("SIM_SERVICEBUS_AMQP_TLS_KEY", cfg.TLSKey)
		ln, err := startSBAMQPTLSListener(ctx, amqpAddr, certFile, keyFile)
		if err != nil {
			log.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		log.Printf("Service Bus raw AMQP/TLS listening on %s", ln.Addr())
	}

	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// buildSimulator constructs the simulator server, applies the ARM
// middleware chain, and registers every simulated Azure service. Split
// from main so the spec-conformance tests can build the full route
// table in-process and validate it against the vendored Swagger specs
// (specs/cloud-api/azure/).
func buildSimulator(cfg sim.Config) (*sim.Server, error) {
	return buildSimulatorWithUI(cfg, true)
}

func buildSimulatorWithUI(cfg sim.Config, includeUI bool) (*sim.Server, error) {
	srv, err := sim.NewServer(cfg)
	if err != nil {
		return nil, err
	}

	// ARM request validation runs after path cleanup and after auth discovery
	// interception, so provider endpoint joins and OAuth/OpenID routes keep
	// their Azure-compatible behavior.
	srv.WrapHandler(AzureARMAPIVersionMiddleware)
	// Bearer verification runs just outside the api-version check so an
	// unauthenticated ARM request gets ARM's 401 before any 400 for a missing
	// api-version, and just inside path cleanup so it sees the collapsed
	// /subscriptions path. AzureAuthMiddleware wraps it, so the OAuth/OpenID
	// endpoints are handled and returned before reaching it and never need a
	// bearer.
	srv.WrapHandler(AzureBearerVerificationMiddleware)
	// Clean double slashes in request paths. The azurerm v3 provider (via
	// go-azure-sdk) appends a trailing slash to the resourceManager endpoint,
	// producing paths like //subscriptions/... Go's default mux would 301
	// redirect these, which changes PUT→GET and breaks creates.
	srv.WrapHandler(CleanPathMiddleware)

	// Wrap with auth middleware for OAuth2 token requests (must be outside
	// the mux to avoid route conflicts with ACR's /v2/{path...}).
	srv.WrapHandler(AzureAuthMiddleware)

	// CORS is outermost: a browser's preflight OPTIONS request for Azure
	// Resource Manager or Microsoft Graph must be answered before the ARM
	// bearer and api-version middlewares run, exactly as real Azure's edge
	// answers a preflight ahead of the resource provider's own auth check —
	// the request carries neither a bearer token nor api-version by design.
	srv.WrapHandler(sim.AzureCORSMiddleware)

	// Register Azure service routes
	// Entra registers first: it initializes the directory stores (and the
	// token-issuance state) that later registrations write at register
	// time — managed identities materialize their service principals into
	// the directory as part of their own registration.
	registerEntra(srv)
	// Monitor must be registered before Container Apps and Functions to
	// initialize the monitorLogs store their log injection uses.
	registerAzureMonitor(srv)
	registerContainerApps(srv)
	registerContainerAppsApps(srv)
	registerContainerAppsIngress(srv)
	registerAzureFiles(srv)
	registerStorageAccounts(srv)
	registerACR(srv)
	registerACRTasks(srv)
	registerPrivateDNS(srv)
	registerAzureFunctions(srv)
	registerWebMore(srv)
	registerWebHybridConnections(srv)
	registerWebPrivateAccess(srv)
	registerWebSitePrivateEndpoints(srv)
	registerAppServicePlanNetworking(srv)
	registerApplicationInsights(srv)

	// Cloud metadata (for Terraform provider metadata_host)
	registerMetadata(srv)

	// Infrastructure services
	registerAzureAsyncOperations(srv)
	registerResourceGroups(srv)
	registerResourcesARM(srv)
	registerTags(srv)
	registerNetwork(srv)
	registerCompute(srv)
	registerManagedIdentity(srv)
	registerKeyVault(srv)
	registerPublicDNS(srv)
	registerBlobDataPlane(srv)
	registerStorageDataPlane(srv)
	registerCacheRedis(srv)
	registerPGFlexibleServer(srv)
	registerCosmosDB(srv)
	registerCosmosAPIs(srv)
	registerCosmosMetrics(srv)
	registerCosmosPEC(srv)
	registerCosmosScripts(srv)
	registerCosmosChangeFeed(srv)
	registerServiceBus(srv)
	registerServiceBusDataPlane(srv)
	registerEventHubs(srv)
	registerEventGrid(srv)
	registerLogicApps(srv)
	registerContainerInstances(srv)
	registerAPIM(srv)
	registerAuthorization(srv)
	registerContainerAppEnvironment(srv)
	registerAppServicePlan(srv)
	registerSubscription(srv)
	registerSubscriptionAlias(srv)
	registerSubscriptionOwnership(srv)
	registerSubscriptionPolicy(srv)
	registerSubscriptionOperations(srv)

	// Embedded UI (no-op with -tags noui).
	if includeUI {
		registerUI(srv)
	}

	// Runtime wire-shape validation (armed only when
	// SOCKERLESS_SPEC_VALIDATE is set; see spec_validator.go). Wrapped
	// last so it captures host-routed data planes too.
	if err := armSpecValidator(srv); err != nil {
		return nil, err
	}

	return srv, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

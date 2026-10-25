// Command simulator-azure runs the Microsoft Azure simulator: a local,
// wire-faithful reimplementation of a slice of the Azure public API — Azure
// Resource Manager, Microsoft Entra and the data planes — served on one port
// to the Azure SDKs, the Azure CLI and terraform-provider-azurerm. Every
// service slice registers in buildSimulator below.
//
// Configure with environment variables:
//
//	SIM_LISTEN_ADDR  — listen address (default ":4568")
//	SIM_AZURE_PORT   — port to listen on, ahead of SIM_LISTEN_ADDR
//	SIM_TLS_CERT     — TLS certificate file (optional)
//	SIM_TLS_KEY      — TLS key file (optional)
//	SIM_CALLBACK_LISTEN_ADDR — plain-HTTP address the workloads' metadata and identity requests reach beside a TLS listener (default: a free port on every interface)
//	SIM_RUNTIME      — "docker" by default; "process" starts API-only mode for runs that do not execute workloads
//	SIM_SERVICEBUS_AMQP_LISTEN_ADDR — raw Service Bus AMQP/TLS listen address (optional)
//	SIM_LOG_LEVEL    — log level: trace, debug, info, warn, error (default "info")
//	SIM_PLACEMENT    — per-service placement backends, as "virtualmachines=microvm,containerapps=container"
//
// SDK configuration:
//
//	Use custom cloud.Configuration with ARM endpoint http://localhost:4568
package main

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
)

func main() {
	sim.Main(sim.Program{
		Provider:    "azure",
		DefaultPort: "4568",
		Configure: func(cfg *sim.Config) error {
			if _, err := azureAdvertisedEndpointConfigFromEnv(); err != nil {
				return err
			}
			return configureAzurePlacement(cfg.Placement)
		},
		Build: func(cfg sim.Config) (*sim.Server, error) {
			// The sidecars forward workloads' metadata requests to the
			// plain-HTTP address the simulator serves them on.
			simListenAddr = cfg.CallbackListenAddr
			return buildSimulator(cfg)
		},
		Start: startAzureListeners,
	})
}

// startAzureListeners starts the DNS listeners, the placement backends and
// the raw Service Bus AMQP listener an operator configured, and adopts the
// virtual machines a previous process of a persistent simulator left running.
func startAzureListeners(_ *sim.Server, cfg sim.Config) (func(context.Context) error, error) {
	ctx, cancel := context.WithCancel(context.Background())
	var dnsServers []*dnsserver.Server
	stop := func(ctx context.Context) error {
		cancel()
		azureReleaseVMWorkloads(ctx)
		stopAzurePlacement(cfg.Persist)
		azureStopNATEgresses()
		if networkSidecars != nil {
			networkSidecars.Close()
		}
		azureRemoveWorkloadNetworks()
		var errs []error
		for _, srv := range dnsServers {
			errs = append(errs, srv.Close())
		}
		return errors.Join(errs...)
	}
	servers, err := startAzureDNSFromEnv()
	if err != nil {
		return nil, errors.Join(err, stop(context.Background()))
	}
	dnsServers = servers
	if err := startAzurePlacement(ctx); err != nil {
		return nil, errors.Join(err, stop(context.Background()))
	}
	azureAdoptVMWorkloads(ctx)
	adoptACAAppReplicas(ctx)
	resumeACAJobExecutions(ctx)
	if amqpAddr := os.Getenv("SIM_SERVICEBUS_AMQP_LISTEN_ADDR"); amqpAddr != "" {
		certFile := sim.EnvOr("SIM_SERVICEBUS_AMQP_TLS_CERT", cfg.TLSCert)
		keyFile := sim.EnvOr("SIM_SERVICEBUS_AMQP_TLS_KEY", cfg.TLSKey)
		ln, err := startSBAMQPTLSListener(ctx, amqpAddr, certFile, keyFile)
		if err != nil {
			return nil, errors.Join(err, stop(context.Background()))
		}
		log.Printf("Service Bus raw AMQP/TLS listening on %s", ln.Addr())
	}
	return stop, nil
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
	// The cross-resource-group move scans every store this build creates
	// (resource_move.go), so the tracked set starts empty here: a second build
	// in the same process must scan its own stores, not the previous build's.
	sim.ResetTrackedStores()
	// The console federation broker's coordinates fail loud at startup exactly
	// like the OIDC config the server validates: an operator who sets any
	// SOCKERLESS_CONSOLE_FEDERATION_* coordinate but not all of them has a
	// deployment error, never a silently degraded broker.
	federation, err := loadConsoleFederation()
	if err != nil {
		return nil, err
	}
	azureConsoleFederation = federation
	// Azure Resource Manager matches paths case-insensitively and its clients
	// spell them differently, so every request is canonicalized before routing.
	cfg.RewriteRequest = azureNormalizeRequestPath
	cfg.TLSConfigForClient = azureTLSConfigForClient
	srv, err := sim.NewServer(cfg)
	if err != nil {
		return nil, err
	}
	azureServer = srv

	// ARM refuses a request beneath a missing or deleting resource group only
	// after it has authenticated the request and checked its api-version.
	srv.WrapHandler(AzureResourceGroupScopeMiddleware)
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
	// The Cosmos DB data plane authenticates with the account's shared keys
	// rather than an ARM bearer, and on its own paths, so its check sits
	// alongside the ARM one — covering every data-plane route rather than
	// waiting for each to opt in.
	srv.WrapHandler(CosmosDataPlaneAuthMiddleware)
	// Clean double slashes in request paths. The azurerm v3 provider (via
	// go-azure-sdk) appends a trailing slash to the resourceManager endpoint,
	// producing paths like //subscriptions/... Go's default mux would 301
	// redirect these, which changes PUT→GET and breaks creates.
	srv.WrapHandler(CleanPathMiddleware)

	// Wrap with auth middleware for OAuth2 token requests (must be outside
	// the mux to avoid route conflicts with ACR's /v2/{path...}).
	srv.WrapHandler(AzureAuthMiddleware)

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
	// App Service Environments own the placement scope App Service plans and
	// sites reference, so their stores exist before those slices register.
	registerWebEnvironments(srv)
	registerWebRecommendations(srv)
	registerWebCloneability(srv)
	registerWebStackCatalogs(srv)
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
	registerMonitorMetrics(srv)
	registerCompute(srv)
	registerManagedIdentity(srv)
	registerWorkloadIdentity(srv)
	registerKeyVault(srv)
	registerKeyVaultManagedHSM(srv)
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

	if includeUI {
		if err := registerUI(srv, cfg.ConsoleDir); err != nil {
			return nil, err
		}
	}

	// Wrap CORS around every slice's dispatcher: Azure's edge answers a
	// browser's preflight before the bearer and api-version checks, and every
	// response a dispatcher writes carries the access-control headers.
	srv.WrapHandler(AzureCORSMiddleware)

	// Runtime wire-shape validation (armed only when
	// SOCKERLESS_SPEC_VALIDATE is set; see spec_validator.go). Wrapped
	// last so it captures host-routed data planes too.
	if err := armSpecValidator(srv); err != nil {
		return nil, err
	}

	return srv, nil
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// envStaticIP derives a stable, environment-unique static IP for a managed
// environment. Real Azure allocates a distinct staticIp per environment; a
// single hardcoded value makes every environment collide, so terraform/SDK
// read-backs across two environments see the same IP. The IP is deterministic
// in the resource ID so re-reads of the same environment are stable.
func envStaticIP(resourceID string) string {
	h := sha256.Sum256([]byte(resourceID))
	// 100.64.0.0/10 is the carrier-grade-NAT range Azure-managed environments
	// surface; pick a host within it deterministically.
	return fmt.Sprintf("100.%d.%d.%d", 64+int(h[0])%64, h[1], h[2])
}

type ContainerAppEnvironment struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties EnvProperties     `json:"properties"`
	// DockerNetworkName is the engine network that backs this environment:
	// its own, or its virtual network's when the environment names an
	// infrastructure subnet of a stored virtual network. Jobs and apps
	// launched in the environment connect to it with their names as DNS
	// aliases, so they resolve each other through the engine's resolver.
	//
	// Sockerless-internal wiring: it persists through the store's JSON
	// marshal but must never appear on the ARM wire — responses go
	// through wire(), which excludes it.
	DockerNetworkName string `json:"dockerNetworkName,omitempty"`
}

// containerAppEnvironmentWire is the ARM response shape for a managed
// environment: the stored record minus sockerless-internal wiring.
type containerAppEnvironmentWire struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties EnvProperties     `json:"properties"`
}

func (e ContainerAppEnvironment) wire() containerAppEnvironmentWire {
	return containerAppEnvironmentWire{
		ID:         e.ID,
		Name:       e.Name,
		Type:       e.Type,
		Location:   e.Location,
		Tags:       e.Tags,
		Properties: e.Properties,
	}
}

// acaEnvironments is the package-level store for Container Apps
// environments. Exposed so containerapps.go can resolve a job's
// environment + backing Docker network when launching executions.
var acaEnvironments sim.Store[ContainerAppEnvironment]

type EnvProperties struct {
	ProvisioningState         string                     `json:"provisioningState"`
	DefaultDomain             string                     `json:"defaultDomain"`
	StaticIp                  string                     `json:"staticIp"`
	AppLogsConfiguration      *AppLogsConfiguration      `json:"appLogsConfiguration,omitempty"`
	VnetConfiguration         *VnetConfiguration         `json:"vnetConfiguration,omitempty"`
	InfrastructureSubnetId    string                     `json:"infrastructureSubnetId,omitempty"`
	ZoneRedundant             bool                       `json:"zoneRedundant"`
	WorkloadProfiles          []WorkloadProfile          `json:"workloadProfiles,omitempty"`
	CustomDomainConfiguration *CustomDomainConfiguration `json:"customDomainConfiguration,omitempty"`
	PeerAuthentication        *PeerAuthentication        `json:"peerAuthentication,omitempty"`
}

type CustomDomainConfiguration struct {
	CustomDomainVerificationId string `json:"customDomainVerificationId"`
}

type PeerAuthentication struct {
	Mtls *Mtls `json:"mtls,omitempty"`
}

type Mtls struct {
	Enabled bool `json:"enabled"`
}

type WorkloadProfile struct {
	Name                string `json:"name"`
	WorkloadProfileType string `json:"workloadProfileType"`
}

type AppLogsConfiguration struct {
	Destination               string                     `json:"destination,omitempty"`
	LogAnalyticsConfiguration *LogAnalyticsConfiguration `json:"logAnalyticsConfiguration,omitempty"`
}

type LogAnalyticsConfiguration struct {
	CustomerId string `json:"customerId,omitempty"`
	SharedKey  string `json:"sharedKey,omitempty"`
}

type VnetConfiguration struct {
	InfrastructureSubnetId string `json:"infrastructureSubnetId,omitempty"`
	Internal               bool   `json:"internal"`
}

// EnvironmentAuthToken mirrors armappcontainers.EnvironmentAuthToken —
// a TrackedResource whose properties carry the token the getAuthtoken
// action issues.
type EnvironmentAuthToken struct {
	ID         string                    `json:"id,omitempty"`
	Name       string                    `json:"name,omitempty"`
	Type       string                    `json:"type,omitempty"`
	Location   string                    `json:"location,omitempty"`
	Tags       map[string]string         `json:"tags,omitempty"`
	Properties EnvironmentAuthTokenProps `json:"properties"`
}

// EnvironmentAuthTokenProps mirrors
// armappcontainers.EnvironmentAuthTokenProperties.
type EnvironmentAuthTokenProps struct {
	Token   string `json:"token,omitempty"`
	Expires string `json:"expires,omitempty"`
}

// CheckNameAvailabilityResponse mirrors the common-types
// CheckNameAvailabilityResponse (checkNameAvailability action).
type CheckNameAvailabilityResponse struct {
	NameAvailable *bool  `json:"nameAvailable,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Message       string `json:"message,omitempty"`
}

// WorkloadProfileStates mirrors armappcontainers.WorkloadProfileStates —
// a ProxyResource (no location) reporting per-profile node counts.
type WorkloadProfileStates struct {
	ID         string                     `json:"id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Properties WorkloadProfileStatesProps `json:"properties,omitempty"`
}

// WorkloadProfileStatesProps mirrors
// armappcontainers.WorkloadProfileStatesProperties.
type WorkloadProfileStatesProps struct {
	MinimumCount *int32 `json:"minimumCount,omitempty"`
	MaximumCount *int32 `json:"maximumCount,omitempty"`
	CurrentCount *int32 `json:"currentCount,omitempty"`
}

// Certificate mirrors armappcontainers.Certificate — a TrackedResource
// for Custom Domain bindings. Secret request members (password, the raw
// PFX/PEM `value`) are stripped from responses, matching real ACA.
type Certificate struct {
	ID         string            `json:"id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Type       string            `json:"type,omitempty"`
	Location   string            `json:"location,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties CertificateProps  `json:"properties,omitempty"`
}

// CertificateProps mirrors armappcontainers.CertificateProperties.
type CertificateProps struct {
	ProvisioningState             string                         `json:"provisioningState,omitempty"`
	CertificateKeyVaultProperties *CertificateKeyVaultProperties `json:"certificateKeyVaultProperties,omitempty"`
	SubjectName                   string                         `json:"subjectName,omitempty"`
	SubjectAlternativeNames       []string                       `json:"subjectAlternativeNames,omitempty"`
	Issuer                        string                         `json:"issuer,omitempty"`
	IssueDate                     string                         `json:"issueDate,omitempty"`
	ExpirationDate                string                         `json:"expirationDate,omitempty"`
	Thumbprint                    string                         `json:"thumbprint,omitempty"`
	Valid                         *bool                          `json:"valid,omitempty"`
	PublicKeyHash                 string                         `json:"publicKeyHash,omitempty"`
}

// CertificateKeyVaultProperties mirrors
// armappcontainers.CertificateKeyVaultProperties.
type CertificateKeyVaultProperties struct {
	Identity    string `json:"identity,omitempty"`
	KeyVaultURL string `json:"keyVaultUrl,omitempty"`
}

// ManagedCertificate mirrors armappcontainers.ManagedCertificate.
type ManagedCertificate struct {
	ID         string                  `json:"id,omitempty"`
	Name       string                  `json:"name,omitempty"`
	Type       string                  `json:"type,omitempty"`
	Location   string                  `json:"location,omitempty"`
	Tags       map[string]string       `json:"tags,omitempty"`
	Properties ManagedCertificateProps `json:"properties,omitempty"`
}

// ManagedCertificateProps mirrors
// armappcontainers.ManagedCertificateProperties.
type ManagedCertificateProps struct {
	ProvisioningState       string `json:"provisioningState,omitempty"`
	SubjectName             string `json:"subjectName,omitempty"`
	Error                   string `json:"error,omitempty"`
	DomainControlValidation string `json:"domainControlValidation,omitempty"`
	ValidationToken         string `json:"validationToken,omitempty"`
}

func registerContainerAppEnvironment(srv *sim.Server) {
	acaEnvironments = sim.MakeStore[ContainerAppEnvironment](srv.DB(), "aca_environments")
	environments := acaEnvironments

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App"

	// PUT - Create or update container app environment
	srv.HandleFunc("PUT "+armBase+"/managedEnvironments/{envName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")

		var req ContainerAppEnvironment
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		resourceID := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName)

		// An environment on a virtual network's infrastructure subnet joins
		// the engine network of the prefix that subnet lies in; any other
		// environment is a networking domain of its own, which matches ACA's
		// managed-VNet model. Either network is created when the
		// environment's first workload attaches. A subnet not stored yet
		// names the network's first prefix; the environment's workloads
		// cannot lease an address in the subnet until it exists.
		dockerNetName := acaEnvironmentNetworkName(resourceID)
		if vnet, ok := acaInfrastructureVNet(req.Properties); ok {
			dockerNetName = dockerNetForVNet(vnet.ID)
			if name, err := azureSubnetNetworkName(acaInfrastructureSubnetID(req.Properties)); err == nil {
				dockerNetName = name
			}
		}

		env := ContainerAppEnvironment{
			ID:       resourceID,
			Name:     envName,
			Type:     "Microsoft.App/managedEnvironments",
			Location: req.Location,
			Tags:     req.Tags,
			Properties: EnvProperties{
				ProvisioningState:      "Succeeded",
				DefaultDomain:          azureEndpointHostname(r, envName),
				StaticIp:               envStaticIP(resourceID),
				AppLogsConfiguration:   req.Properties.AppLogsConfiguration,
				VnetConfiguration:      req.Properties.VnetConfiguration,
				InfrastructureSubnetId: req.Properties.InfrastructureSubnetId,
				ZoneRedundant:          req.Properties.ZoneRedundant,
				WorkloadProfiles:       req.Properties.WorkloadProfiles,
				CustomDomainConfiguration: &CustomDomainConfiguration{
					CustomDomainVerificationId: sim.NewUUID(),
				},
				PeerAuthentication: &PeerAuthentication{
					Mtls: &Mtls{Enabled: false},
				},
			},
			DockerNetworkName: dockerNetName,
		}
		environments.Put(resourceID, env)
		// Linking the workspace creates the custom log tables the
		// environment writes to.
		acaLogWorkspace(resourceID)

		// go-azure-sdk expects 200 for sync creates
		sim.WriteJSON(w, http.StatusOK, env.wire())
	})

	// GET - Get container app environment
	srv.HandleFunc("GET "+armBase+"/managedEnvironments/{envName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		resourceID := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName)

		env, ok := environments.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Managed environment '%s' not found.", envName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, env.wire())
	})

	// DELETE - Delete container app environment. Real Azure ARM returns 202
	// Accepted with the LRO headers and an empty body for an existing
	// environment and 204 No Content when it does not exist.
	srv.HandleFunc("DELETE "+armBase+"/managedEnvironments/{envName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		resourceID := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName)

		env, ok := environments.Get(resourceID)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		env.Properties.ProvisioningState = "Deleting"
		environments.Put(resourceID, env)
		opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
			// A virtual network's engine network goes with the virtual network.
			if env.DockerNetworkName != "" && acaEnvironmentVNetID(env) == "" {
				azureRemoveSidecar(env.DockerNetworkName)
				err := azureLeaveMicroVMNetwork(context.Background(), env.DockerNetworkName)
				if err == nil {
					err = sim.RemoveDockerNetwork(env.DockerNetworkName)
				}
				if err != nil {
					environments.Update(resourceID, func(e *ContainerAppEnvironment) {
						e.Properties.ProvisioningState = "Failed"
					})
					return &AsyncOperationError{
						Code:    "InternalServerError",
						Message: fmt.Sprintf("Failed to delete managed environment '%s': removing its network: %v", envName, err),
					}
				}
			}
			environments.Delete(resourceID)
			return nil
		})
		acaAsyncOpHeaders(w, r, sub, env.Location, opID)
		w.WriteHeader(http.StatusAccepted)
	})

	// PATCH - Update a managed environment. Real ACA models this as a
	// long-running operation that settles to provisioningState=Succeeded;
	// the SDK's body poller reads it off the 200 body.
	srv.HandleFunc("PATCH "+armBase+"/managedEnvironments/{envName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		resourceID := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName)
		env, ok := environments.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Managed environment '%s' not found.", envName)
			return
		}
		var patch json.RawMessage
		if err := sim.ReadJSON(r, &patch); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Merge over the ARM wire shape so sockerless-internal wiring (the
		// backing Docker network) is neither patchable nor exposed.
		merged := env.wire()
		if err := applyARMMergePatch(&merged, patch); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Identity and server-owned fields are not client-writable.
		prior := env
		env.Tags = merged.Tags
		env.Properties = merged.Properties
		env.Properties.ProvisioningState = prior.Properties.ProvisioningState
		env.Properties.DefaultDomain = prior.Properties.DefaultDomain
		env.Properties.StaticIp = prior.Properties.StaticIp
		environments.Put(resourceID, env)
		sim.WriteJSON(w, http.StatusOK, env.wire())
	})

	// GET - List managed environments in a resource group.
	srv.HandleFunc("GET "+armBase+"/managedEnvironments", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments") + "/"
		filtered := environments.Filter(func(e ContainerAppEnvironment) bool {
			return strings.HasPrefix(e.ID, prefix)
		})
		out := make([]containerAppEnvironmentWire, 0, len(filtered))
		for _, e := range filtered {
			out = append(out, e.wire())
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// GET - List managed environments by subscription.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.App/managedEnvironments", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := armSubscriptionID(sub) + "/"
		filtered := environments.Filter(func(e ContainerAppEnvironment) bool {
			return strings.HasPrefix(e.ID, prefix) && strings.Contains(e.ID, "/providers/Microsoft.App/managedEnvironments/")
		})
		out := make([]containerAppEnvironmentWire, 0, len(filtered))
		for _, e := range filtered {
			out = append(out, e.wire())
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// POST /managedEnvironments/{envName}/getAuthtoken — issue an
	// environment auth token. The SDK sends the mixed-case segment
	// verbatim (not in the lowercasing middleware map).
	srv.HandleFunc("POST "+armBase+"/managedEnvironments/{envName}/getAuthtoken", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		resourceID := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName)
		env, ok := environments.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Managed environment '%s' not found.", envName)
			return
		}
		token := EnvironmentAuthToken{
			ID:       resourceID,
			Name:     envName,
			Type:     "Microsoft.App/managedEnvironments",
			Location: env.Location,
			Properties: EnvironmentAuthTokenProps{
				Token:   sim.NewUUID(),
				Expires: time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339),
			},
		}
		sim.WriteJSON(w, http.StatusOK, token)
	})

	// POST /managedEnvironments/{envName}/checknameavailability — check a
	// child resource name. The middleware lowercases the SDK's
	// `checkNameAvailability` segment, so the handler registers lowercase.
	srv.HandleFunc("POST "+armBase+"/managedEnvironments/{envName}/checknameavailability", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		resourceID := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName)
		if _, ok := environments.Get(resourceID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Managed environment '%s' not found.", envName)
			return
		}
		var req struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Name is available when no certificate of that name already exists
		// in the environment.
		certID := resourceID + "/certificates/" + req.Name
		_, certExists := acaEnvCertificates.Get(certID)
		available := !certExists
		resp := CheckNameAvailabilityResponse{NameAvailable: &available}
		if !available {
			resp.Reason = "AlreadyExists"
			resp.Message = fmt.Sprintf("Name %q is already in use.", req.Name)
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// GET /managedEnvironments/{envName}/workloadProfileStates — per-profile
	// node-count states derived from the environment's workload profiles.
	srv.HandleFunc("GET "+armBase+"/managedEnvironments/{envName}/workloadProfileStates", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		resourceID := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName)
		env, ok := environments.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Managed environment '%s' not found.", envName)
			return
		}
		states := make([]WorkloadProfileStates, 0, len(env.Properties.WorkloadProfiles))
		for _, wp := range env.Properties.WorkloadProfiles {
			minc, maxc, cur := int32(0), int32(0), int32(0)
			states = append(states, WorkloadProfileStates{
				ID:   resourceID + "/workloadProfileStates/" + wp.Name,
				Name: wp.Name,
				Type: "Microsoft.App/managedEnvironments/workloadProfileStates",
				Properties: WorkloadProfileStatesProps{
					MinimumCount: &minc,
					MaximumCount: &maxc,
					CurrentCount: &cur,
				},
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": states})
	})

	registerContainerAppEnvironmentStorages(srv, environments)
	registerContainerAppEnvironmentCertificates(srv, environments)
}

// ManagedEnvironmentStorage pairs a sockerless-known name with a
// storage-account + file-share backing it. ACA jobs/apps reference
// these by storage.name to resolve their VolumeMounts.
type ManagedEnvironmentStorage struct {
	ID         string                              `json:"id"`
	Name       string                              `json:"name"`
	Type       string                              `json:"type"`
	Properties ManagedEnvironmentStorageProperties `json:"properties"`
}

// ManagedEnvironmentStorageProperties mirrors
// Microsoft.App/managedEnvironments/storages resources.
type ManagedEnvironmentStorageProperties struct {
	AzureFile *AzureFileProperties `json:"azureFile,omitempty"`
}

// AzureFileProperties carries the per-storage file-share backing.
type AzureFileProperties struct {
	AccountName string `json:"accountName"`
	ShareName   string `json:"shareName"`
	AccountKey  string `json:"accountKey,omitempty"`
	AccessMode  string `json:"accessMode,omitempty"`
}

// acaEnvStorages is the package-level store for
// managedEnvironments/<env>/storages/<name> resources. Exported for
// use by the ACA Jobs/Apps executor to resolve a VolumeName →
// (account, share) for host-side binding.
var acaEnvStorages sim.Store[ManagedEnvironmentStorage]

func registerContainerAppEnvironmentStorages(srv *sim.Server, envs sim.Store[ContainerAppEnvironment]) {
	acaEnvStorages = sim.MakeStore[ManagedEnvironmentStorage](srv.DB(), "aca_env_storages")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/managedEnvironments/{envName}/storages/{storageName}"

	srv.HandleFunc("PUT "+armBase, func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		storageName := sim.PathParam(r, "storageName")

		envID := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName)
		if _, ok := envs.Get(envID); !ok {
			AzureError(w, "ParentResourceNotFound", "Managed environment not found: "+envID, http.StatusNotFound)
			return
		}

		var req ManagedEnvironmentStorage
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := envID + "/storages/" + storageName
		storage := ManagedEnvironmentStorage{
			ID:         id,
			Name:       storageName,
			Type:       "Microsoft.App/managedEnvironments/storages",
			Properties: req.Properties,
		}
		acaEnvStorages.Put(id, storage)
		sim.WriteJSON(w, http.StatusOK, storage)
	})

	srv.HandleFunc("GET "+armBase, func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		storageName := sim.PathParam(r, "storageName")
		id := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName, "storages", storageName)
		storage, ok := acaEnvStorages.Get(id)
		if !ok {
			AzureError(w, "ResourceNotFound", "Storage not found: "+id, http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, storage)
	})

	srv.HandleFunc("DELETE "+armBase, func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		storageName := sim.PathParam(r, "storageName")
		id := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName, "storages", storageName)
		acaEnvStorages.Delete(id)
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/managedEnvironments/{envName}/storages", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		prefix := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName, "storages") + "/"
		filtered := acaEnvStorages.Filter(func(s ManagedEnvironmentStorage) bool {
			return strings.HasPrefix(s.ID, prefix)
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})
}

// LookupEnvStorageBinding returns (storageAccount, shareName, ok) for
// a managed-environment storage reference (envID = the env resource
// path, storageName = the storage sub-resource name). Used by the ACA
// Jobs / Apps executor to resolve Volume{StorageName} → host dir.
func LookupEnvStorageBinding(envID, storageName string) (string, string, bool) {
	id := envID + "/storages/" + storageName
	s, ok := acaEnvStorages.Get(id)
	if !ok || s.Properties.AzureFile == nil {
		return "", "", false
	}
	return s.Properties.AzureFile.AccountName, s.Properties.AzureFile.ShareName, true
}

// acaEnvCertificates / acaEnvManagedCertificates back the
// managedEnvironments/<env>/certificates and /managedCertificates
// sub-resources used for Custom Domain bindings.
var acaEnvCertificates sim.Store[Certificate]
var acaEnvManagedCertificates sim.Store[ManagedCertificate]

func registerContainerAppEnvironmentCertificates(srv *sim.Server, envs sim.Store[ContainerAppEnvironment]) {
	acaEnvCertificates = sim.MakeStore[Certificate](srv.DB(), "aca_env_certs")
	acaEnvManagedCertificates = sim.MakeStore[ManagedCertificate](srv.DB(), "aca_env_managed_certs")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/managedEnvironments/{envName}"

	requireEnv := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		envName := sim.PathParam(r, "envName")
		envID := armResourceID(sub, rg, "Microsoft.App", "managedEnvironments", envName)
		if _, ok := envs.Get(envID); !ok {
			AzureError(w, "ParentResourceNotFound", "Managed environment not found: "+envID, http.StatusNotFound)
			return "", false
		}
		return envID, true
	}

	// PUT - Create or update a certificate (synchronous in real ACA).
	srv.HandleFunc("PUT "+armBase+"/certificates/{certificateName}", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		certName := sim.PathParam(r, "certificateName")
		var req Certificate
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := envID + "/certificates/" + certName
		cert := Certificate{
			ID:       id,
			Name:     certName,
			Type:     "Microsoft.App/managedEnvironments/certificates",
			Location: req.Location,
			Tags:     req.Tags,
			Properties: CertificateProps{
				ProvisioningState:             "Succeeded",
				CertificateKeyVaultProperties: req.Properties.CertificateKeyVaultProperties,
				SubjectName:                   req.Properties.SubjectName,
			},
		}
		acaEnvCertificates.Put(id, cert)
		sim.WriteJSON(w, http.StatusOK, cert)
	})

	// GET - Get a certificate.
	srv.HandleFunc("GET "+armBase+"/certificates/{certificateName}", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		certName := sim.PathParam(r, "certificateName")
		cert, ok := acaEnvCertificates.Get(envID + "/certificates/" + certName)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Certificate '%s' not found.", certName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, cert)
	})

	// PATCH - Update a certificate's tags (synchronous).
	srv.HandleFunc("PATCH "+armBase+"/certificates/{certificateName}", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		certName := sim.PathParam(r, "certificateName")
		id := envID + "/certificates/" + certName
		cert, ok := acaEnvCertificates.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Certificate '%s' not found.", certName)
			return
		}
		var req Certificate
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tags != nil {
			cert.Tags = req.Tags
		}
		acaEnvCertificates.Put(id, cert)
		sim.WriteJSON(w, http.StatusOK, cert)
	})

	// DELETE - Delete a certificate.
	srv.HandleFunc("DELETE "+armBase+"/certificates/{certificateName}", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		certName := sim.PathParam(r, "certificateName")
		if acaEnvCertificates.Delete(envID + "/certificates/" + certName) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// GET - List certificates in the environment.
	srv.HandleFunc("GET "+armBase+"/certificates", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		prefix := envID + "/certificates/"
		filtered := acaEnvCertificates.Filter(func(c Certificate) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})

	// PUT - Create or update a managed certificate. Real ACA models this
	// as a long-running operation; the SDK's body poller terminates on
	// provisioningState=Succeeded read off the 200/201 body.
	srv.HandleFunc("PUT "+armBase+"/managedCertificates/{managedCertificateName}", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		certName := sim.PathParam(r, "managedCertificateName")
		var req ManagedCertificate
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := envID + "/managedCertificates/" + certName
		_, existed := acaEnvManagedCertificates.Get(id)
		dcv := req.Properties.DomainControlValidation
		if dcv == "" {
			dcv = "CNAME"
		}
		cert := ManagedCertificate{
			ID:       id,
			Name:     certName,
			Type:     "Microsoft.App/managedEnvironments/managedCertificates",
			Location: req.Location,
			Tags:     req.Tags,
			Properties: ManagedCertificateProps{
				ProvisioningState:       "Succeeded",
				SubjectName:             req.Properties.SubjectName,
				DomainControlValidation: dcv,
				ValidationToken:         sim.NewUUID(),
			},
		}
		acaEnvManagedCertificates.Put(id, cert)
		if existed {
			sim.WriteJSON(w, http.StatusOK, cert)
		} else {
			sim.WriteJSON(w, http.StatusCreated, cert)
		}
	})

	// GET - Get a managed certificate.
	srv.HandleFunc("GET "+armBase+"/managedCertificates/{managedCertificateName}", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		certName := sim.PathParam(r, "managedCertificateName")
		cert, ok := acaEnvManagedCertificates.Get(envID + "/managedCertificates/" + certName)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Managed certificate '%s' not found.", certName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, cert)
	})

	// PATCH - Update a managed certificate's tags (synchronous).
	srv.HandleFunc("PATCH "+armBase+"/managedCertificates/{managedCertificateName}", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		certName := sim.PathParam(r, "managedCertificateName")
		id := envID + "/managedCertificates/" + certName
		cert, ok := acaEnvManagedCertificates.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Managed certificate '%s' not found.", certName)
			return
		}
		var req ManagedCertificate
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tags != nil {
			cert.Tags = req.Tags
		}
		acaEnvManagedCertificates.Put(id, cert)
		sim.WriteJSON(w, http.StatusOK, cert)
	})

	// DELETE - Delete a managed certificate.
	srv.HandleFunc("DELETE "+armBase+"/managedCertificates/{managedCertificateName}", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		certName := sim.PathParam(r, "managedCertificateName")
		if acaEnvManagedCertificates.Delete(envID + "/managedCertificates/" + certName) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// GET - List managed certificates in the environment.
	srv.HandleFunc("GET "+armBase+"/managedCertificates", func(w http.ResponseWriter, r *http.Request) {
		envID, ok := requireEnv(w, r)
		if !ok {
			return
		}
		prefix := envID + "/managedCertificates/"
		filtered := acaEnvManagedCertificates.Filter(func(c ManagedCertificate) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})
}

// acaInfrastructureSubnetID is the subnet an environment's workloads sit in,
// whichever of the two spellings the request carried it in.
func acaInfrastructureSubnetID(props EnvProperties) string {
	if props.VnetConfiguration != nil && props.VnetConfiguration.InfrastructureSubnetId != "" {
		return props.VnetConfiguration.InfrastructureSubnetId
	}
	return props.InfrastructureSubnetId
}

// acaInfrastructureVNet is the stored virtual network an environment's
// infrastructure subnet belongs to.
func acaInfrastructureVNet(props EnvProperties) (VirtualNetwork, bool) {
	subnetID := acaInfrastructureSubnetID(props)
	if subnetID == "" || azureVnets == nil {
		return VirtualNetwork{}, false
	}
	return azureVnets.Get(azureVNetIDOfSubnet(subnetID))
}

// acaEnvironmentNetworkName names the engine network of an environment on no
// virtual network: the environment's name for the operator and a digest of
// its resource id, so two environments of one name are two networks.
func acaEnvironmentNetworkName(envID string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(envID)))
	return "sim-env-" + strings.ToLower(envID[strings.LastIndex(envID, "/")+1:]) + "-" + hex.EncodeToString(sum[:4])
}

// acaEnvironmentVNetID is the virtual network whose engine network the
// environment joined, or "" for an environment on a network of its own.
func acaEnvironmentVNetID(env ContainerAppEnvironment) string {
	vnet, ok := acaInfrastructureVNet(env.Properties)
	if !ok {
		return ""
	}
	if name, err := azureSubnetNetworkName(acaInfrastructureSubnetID(env.Properties)); err == nil && name == env.DockerNetworkName {
		return vnet.ID
	}
	if dockerNetForVNet(vnet.ID) == env.DockerNetworkName {
		return vnet.ID
	}
	return ""
}

// acaEnvironmentNetwork is the engine network a workload of the environment
// attaches to, with its sidecar as the workload's resolver — that of the
// prefix its infrastructure subnet lies in, created on first use, or the
// environment's own — and the subnet a workload leases its address in when
// the environment is on a virtual network's infrastructure subnet.
func acaEnvironmentNetwork(ctx context.Context, env ContainerAppEnvironment) (azureWorkloadNetwork, string, error) {
	if vnetID := acaEnvironmentVNetID(env); vnetID != "" {
		subnetID := acaInfrastructureSubnetID(env.Properties)
		network, err := azureSubnetNetwork(ctx, placementContainerApps, subnetID)
		if err != nil {
			return azureWorkloadNetwork{}, "", err
		}
		return network, subnetID, nil
	}
	if env.DockerNetworkName == "" {
		return azureWorkloadNetwork{}, "", fmt.Errorf("managed environment %s has no network", env.ID)
	}
	if _, _, err := sim.EnsurePlatformNetwork(ctx, env.DockerNetworkName, nil); err != nil {
		return azureWorkloadNetwork{}, "", fmt.Errorf("managed environment %s: %w", env.ID, err)
	}
	dns, err := azureEnsureSidecar(ctx, env.DockerNetworkName, netip.Addr{})
	if err != nil {
		return azureWorkloadNetwork{}, "", err
	}
	if err := azureJoinMicroVMPlatformNetwork(ctx, placementContainerApps, env.DockerNetworkName); err != nil {
		return azureWorkloadNetwork{}, "", err
	}
	return azureWorkloadNetwork{Network: env.DockerNetworkName, DNS: dns}, "", nil
}

// acaWorkloadNetwork is the network a workload of the environment envID runs
// on: the environment's, or the platform network for a workload whose
// environment is not stored.
func acaWorkloadNetwork(ctx context.Context, envID string) (azureWorkloadNetwork, string, error) {
	if envID != "" {
		if env, ok := acaEnvironments.Get(envID); ok {
			return acaEnvironmentNetwork(ctx, env)
		}
	}
	network, err := azurePlatformNetwork(ctx)
	return network, "", err
}

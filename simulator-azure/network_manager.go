package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Virtual Network Manager (Microsoft.Network/networkManagers) is the
// scope-and-deployment half of Azure's centrally managed networking. A network
// manager declares the management groups and subscriptions it governs and the
// kinds of configuration it is allowed to manage there; the configurations
// themselves are separate resources, and none of them takes effect until a
// commit deploys it to a set of regions. Deployment status is then read back
// per region and per configuration type.
//
// Both halves are executed here rather than echoed. A commit is validated
// against the manager's declared scope accesses and against the configurations
// it names, and it writes a deployment record per target region; the deployment
// status list answers from those records, filtered the way the request asks.
// Because the simulator has no fleet to roll a configuration out to, a commit
// reaches its terminal state within the call — there is no interval during
// which a region is still Deploying — which is why the operation answers with
// the committed state directly.

// NetworkManager mirrors Microsoft.Network/networkManagers.
type NetworkManager struct {
	azureNetworkResourceHeader
	SystemData *SystemData              `json:"systemData,omitempty"`
	Properties NetworkManagerProperties `json:"properties"`
}

// NetworkManagerProperties holds the manager's scope, the configuration types
// it may manage there, and the read-only state the resource provider computes.
type NetworkManagerProperties struct {
	Description                 string               `json:"description,omitempty"`
	NetworkManagerScopes        NetworkManagerScopes `json:"networkManagerScopes"`
	NetworkManagerScopeAccesses []string             `json:"networkManagerScopeAccesses,omitempty"`
	ProvisioningState           string               `json:"provisioningState,omitempty"`
	ResourceGUID                string               `json:"resourceGuid,omitempty"`
}

// NetworkManagerScopes is the set of management groups and subscriptions a
// network manager governs.
type NetworkManagerScopes struct {
	ManagementGroups  []string                         `json:"managementGroups,omitempty"`
	Subscriptions     []string                         `json:"subscriptions,omitempty"`
	CrossTenantScopes []NetworkManagerCrossTenantScope `json:"crossTenantScopes,omitempty"`
}

// NetworkManagerCrossTenantScope is a scope another tenant delegated to this
// manager.
type NetworkManagerCrossTenantScope struct {
	TenantID         string   `json:"tenantId,omitempty"`
	ManagementGroups []string `json:"managementGroups,omitempty"`
	Subscriptions    []string `json:"subscriptions,omitempty"`
}

// NetworkManagerCommit is the body of a commit request and the answer it gets:
// which configurations are deployed to which regions, for one configuration
// type.
type NetworkManagerCommit struct {
	CommitID         string   `json:"commitId,omitempty"`
	TargetLocations  []string `json:"targetLocations"`
	ConfigurationIDs []string `json:"configurationIds,omitempty"`
	CommitType       string   `json:"commitType"`
}

// NetworkManagerDeploymentStatus is what one region holds for one configuration
// type after a commit.
type NetworkManagerDeploymentStatus struct {
	CommitTime       string   `json:"commitTime,omitempty"`
	Region           string   `json:"region,omitempty"`
	DeploymentStatus string   `json:"deploymentStatus,omitempty"`
	ConfigurationIDs []string `json:"configurationIds,omitempty"`
	DeploymentType   string   `json:"deploymentType,omitempty"`
	ErrorMessage     string   `json:"errorMessage,omitempty"`
}

// networkManagerDeployment is one stored deployment record, keyed by the
// manager, region and configuration type it belongs to.
type networkManagerDeployment struct {
	ManagerID string                         `json:"managerId"`
	Status    NetworkManagerDeploymentStatus `json:"status"`
}

// networkManagerConfigurationTypes are the configuration kinds a manager may be
// given access to and may commit.
var networkManagerConfigurationTypes = map[string]bool{
	"SecurityAdmin": true,
	"Connectivity":  true,
	"SecurityUser":  true,
	"Routing":       true,
}

var (
	azureNetworkManagers           sim.Store[NetworkManager]
	azureNetworkManagerDeployments sim.Store[networkManagerDeployment]
)

const azureNetworkManagerType = "Microsoft.Network/networkManagers"

func registerNetworkManagers(srv *sim.Server) {
	azureNetworkManagers = sim.MakeStore[NetworkManager](srv.DB(), "network_managers")
	azureNetworkManagerDeployments = sim.MakeStore[networkManagerDeployment](srv.DB(), "network_manager_deployments")

	registerAzureNetworkResource(srv, azureNetworkResourceSpec[NetworkManager]{
		collection:   "networkManagers",
		nameParam:    "networkManagerName",
		resourceType: azureNetworkManagerType,
		store:        azureNetworkManagers,
		header: func(nm *NetworkManager) *azureNetworkResourceHeader {
			return &nm.azureNetworkResourceHeader
		},
		validate:    validateNetworkManager,
		provision:   provisionNetworkManager,
		afterDelete: deleteNetworkManagerDeployments,
	})

	registerNetworkManagerCommits(srv)
}

// validateNetworkManager rejects a manager the resource provider will not
// accept: one that governs nothing, or one that claims a configuration type
// that does not exist.
func validateNetworkManager(w http.ResponseWriter, _ *http.Request, nm *NetworkManager) bool {
	scopes := nm.Properties.NetworkManagerScopes
	if len(scopes.ManagementGroups) == 0 && len(scopes.Subscriptions) == 0 {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: networkManagerScopes must name at least one management group or subscription.")
		return false
	}
	for _, access := range nm.Properties.NetworkManagerScopeAccesses {
		if !networkManagerConfigurationTypes[access] {
			AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
				"The request format was unexpected: %q is not a network manager configuration type.", access)
			return false
		}
	}
	return true
}

// provisionNetworkManager assigns the read-only state the resource provider
// computes: a resource GUID minted once, the ARM system metadata, and the
// terminal provisioning state.
func provisionNetworkManager(_ context.Context, nm *NetworkManager, previous *NetworkManager) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	nm.Properties.ResourceGUID = generateUUID()
	nm.SystemData = &SystemData{CreatedAt: now, LastModifiedAt: now}
	if previous != nil {
		if previous.Properties.ResourceGUID != "" {
			nm.Properties.ResourceGUID = previous.Properties.ResourceGUID
		}
		if previous.SystemData != nil && previous.SystemData.CreatedAt != "" {
			nm.SystemData.CreatedAt = previous.SystemData.CreatedAt
		}
	}
	// A cross-tenant scope is granted by the other tenant, never declared by
	// the manager's own write, so a request that carries one is ignored.
	nm.Properties.NetworkManagerScopes.CrossTenantScopes = nil
	nm.Properties.ProvisioningState = "Succeeded"
	return nil
}

// deleteNetworkManagerDeployments drops the deployment records of a manager
// that no longer exists, so a manager recreated under the same name starts with
// nothing deployed.
func deleteNetworkManagerDeployments(_ context.Context, id string, _ NetworkManager) {
	for _, deployment := range azureNetworkManagerDeployments.Filter(func(d networkManagerDeployment) bool {
		return strings.EqualFold(d.ManagerID, id)
	}) {
		azureNetworkManagerDeployments.Delete(networkManagerDeploymentKey(
			deployment.ManagerID, deployment.Status.Region, deployment.Status.DeploymentType))
	}
}

func networkManagerDeploymentKey(managerID, region, configurationType string) string {
	return strings.ToLower(managerID) + "|" + strings.ToLower(region) + "|" + configurationType
}

// registerNetworkManagerCommits mounts the two operations that make a network
// manager's configurations take effect and report what took effect where.
func registerNetworkManagerCommits(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	base := armBase + "/networkManagers/{networkManagerName}"

	managerID := func(r *http.Request) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkManagers/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "networkManagerName"))
	}

	srv.HandleFunc("POST "+base+"/commit", func(w http.ResponseWriter, r *http.Request) {
		nm, ok := azureNetworkManagers.Get(managerID(r))
		if !ok {
			azureNetworkResourceNotFound(w, azureNetworkManagerType,
				sim.PathParam(r, "networkManagerName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		var commit NetworkManagerCommit
		if err := sim.ReadJSON(r, &commit); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !validateNetworkManagerCommit(w, nm, commit) {
			return
		}
		commit.CommitID = generateUUID()
		commitTime := time.Now().UTC().Format(time.RFC3339Nano)
		for _, region := range commit.TargetLocations {
			azureNetworkManagerDeployments.Put(
				networkManagerDeploymentKey(nm.ID, region, commit.CommitType),
				networkManagerDeployment{
					ManagerID: nm.ID,
					Status: NetworkManagerDeploymentStatus{
						CommitTime:       commitTime,
						Region:           region,
						DeploymentStatus: "Deployed",
						ConfigurationIDs: commit.ConfigurationIDs,
						DeploymentType:   commit.CommitType,
					},
				})
		}
		sim.WriteJSON(w, http.StatusOK, commit)
	})

	srv.HandleFunc("POST "+base+"/listDeploymentStatus", func(w http.ResponseWriter, r *http.Request) {
		id := managerID(r)
		if _, ok := azureNetworkManagers.Get(id); !ok {
			azureNetworkResourceNotFound(w, azureNetworkManagerType,
				sim.PathParam(r, "networkManagerName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		var query struct {
			Regions         []string `json:"regions,omitempty"`
			DeploymentTypes []string `json:"deploymentTypes,omitempty"`
			SkipToken       string   `json:"skipToken,omitempty"`
		}
		if err := sim.ReadJSON(r, &query); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		statuses := make([]NetworkManagerDeploymentStatus, 0)
		for _, deployment := range azureNetworkManagerDeployments.Filter(func(d networkManagerDeployment) bool {
			return strings.EqualFold(d.ManagerID, id)
		}) {
			if !networkManagerMatches(query.Regions, deployment.Status.Region) {
				continue
			}
			if !networkManagerMatches(query.DeploymentTypes, deployment.Status.DeploymentType) {
				continue
			}
			statuses = append(statuses, deployment.Status)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": statuses})
	})
}

// validateNetworkManagerCommit applies the checks the resource provider applies
// before a commit changes anything: the commit type must be a real
// configuration type the manager is allowed to manage, at least one target
// region must be named, and every configuration the commit names must exist.
func validateNetworkManagerCommit(w http.ResponseWriter, nm NetworkManager, commit NetworkManagerCommit) bool {
	if !networkManagerConfigurationTypes[commit.CommitType] {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: %q is not a network manager configuration type.", commit.CommitType)
		return false
	}
	if len(commit.TargetLocations) == 0 {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: targetLocations must name at least one region.")
		return false
	}
	if len(nm.Properties.NetworkManagerScopeAccesses) > 0 &&
		!networkManagerMatches(nm.Properties.NetworkManagerScopeAccesses, commit.CommitType) {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"Network manager %q has no %s scope access, so it cannot commit a %s configuration.",
			nm.Name, commit.CommitType, commit.CommitType)
		return false
	}
	// A commit deploys configurations that already exist under the manager. The
	// simulator holds none of the network manager configuration resource types,
	// so every configuration a commit names is absent and the commit is
	// refused, exactly as it is for an id that names nothing. A commit with no
	// configurations is the one that removes what a region currently holds, and
	// it is accepted.
	for _, configurationID := range commit.ConfigurationIDs {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", configurationID)
		return false
	}
	return true
}

// networkManagerMatches reports whether a filter list selects a value; an empty
// filter selects everything, which is how the deployment-status query treats an
// omitted region or type list.
func networkManagerMatches(filter []string, value string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, entry := range filter {
		if strings.EqualFold(entry, value) {
			return true
		}
	}
	return false
}

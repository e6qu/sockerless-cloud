package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Virtual Network Manager: a manager declares the management groups and
// subscriptions it governs and the configuration kinds it may manage; a
// configuration takes effect only when a commit deploys it to regions, whose
// status reads back per region and type. A commit is validated against the
// declared scope accesses and the configurations it names, and writes a
// deployment record per region. With no fleet to roll out to, a commit
// reaches its terminal state within the call.

type NetworkManager struct {
	azureNetworkResourceHeader
	SystemData *SystemData              `json:"systemData,omitempty"`
	Properties NetworkManagerProperties `json:"properties"`
}

type NetworkManagerProperties struct {
	Description                 string               `json:"description,omitempty"`
	NetworkManagerScopes        NetworkManagerScopes `json:"networkManagerScopes"`
	NetworkManagerScopeAccesses []string             `json:"networkManagerScopeAccesses,omitempty"`
	ProvisioningState           string               `json:"provisioningState,omitempty"`
	ResourceGUID                string               `json:"resourceGuid,omitempty"`
}

type NetworkManagerScopes struct {
	ManagementGroups  []string                         `json:"managementGroups,omitempty"`
	Subscriptions     []string                         `json:"subscriptions,omitempty"`
	CrossTenantScopes []NetworkManagerCrossTenantScope `json:"crossTenantScopes,omitempty"`
}

type NetworkManagerCrossTenantScope struct {
	TenantID         string   `json:"tenantId,omitempty"`
	ManagementGroups []string `json:"managementGroups,omitempty"`
	Subscriptions    []string `json:"subscriptions,omitempty"`
}

// The body of a commit request and its answer: which configurations deploy to
// which regions, for one configuration type.
type NetworkManagerCommit struct {
	CommitID         string   `json:"commitId,omitempty"`
	TargetLocations  []string `json:"targetLocations"`
	ConfigurationIDs []string `json:"configurationIds,omitempty"`
	CommitType       string   `json:"commitType"`
}

type NetworkManagerDeploymentStatus struct {
	CommitTime       string   `json:"commitTime,omitempty"`
	Region           string   `json:"region,omitempty"`
	DeploymentStatus string   `json:"deploymentStatus,omitempty"`
	ConfigurationIDs []string `json:"configurationIds,omitempty"`
	DeploymentType   string   `json:"deploymentType,omitempty"`
	ErrorMessage     string   `json:"errorMessage,omitempty"`
}

// Keyed by manager, region and configuration type.
type networkManagerDeployment struct {
	ManagerID string                         `json:"managerId"`
	Status    NetworkManagerDeploymentStatus `json:"status"`
}

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

// Refuse a manager that governs nothing or claims a configuration type that
// does not exist.
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

func provisionNetworkManager(_ context.Context, nm *NetworkManager, previous *NetworkManager) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	nm.Properties.ResourceGUID = sim.NewUUID()
	nm.SystemData = &SystemData{CreatedAt: now, LastModifiedAt: now}
	if previous != nil {
		if previous.Properties.ResourceGUID != "" {
			nm.Properties.ResourceGUID = previous.Properties.ResourceGUID
		}
		if previous.SystemData != nil && previous.SystemData.CreatedAt != "" {
			nm.SystemData.CreatedAt = previous.SystemData.CreatedAt
		}
	}
	// The other tenant grants a cross-tenant scope; the manager's own write cannot
	// declare one.
	nm.Properties.NetworkManagerScopes.CrossTenantScopes = nil
	nm.Properties.ProvisioningState = "Succeeded"
	return nil
}

// A manager recreated under the same name starts with nothing deployed.
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

func registerNetworkManagerCommits(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	base := armBase + "/networkManagers/{networkManagerName}"

	managerID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkManagers", sim.PathParam(r, "networkManagerName"))
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
		commit.CommitID = sim.NewUUID()
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

// The commit type must be one the manager may manage, at least one region must
// be named, and every named configuration must exist.
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
	// The simulator holds none of the configuration resource types, so a named
	// configuration is absent and the commit refused. A commit naming none removes
	// what a region holds, and is accepted.
	for _, configurationID := range commit.ConfigurationIDs {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", configurationID)
		return false
	}
	return true
}

// An empty filter selects everything, as an omitted region or type list does.
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

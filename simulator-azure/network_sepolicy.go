package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A service endpoint policy narrows a subnet's service endpoint to the listed
// resources; the subnet applying it enforces it. The read-only subnets property
// resolves from the subnets that reference the policy.

type ServiceEndpointPolicy struct {
	azureNetworkResourceHeader
	Kind       string                          `json:"kind,omitempty"`
	Properties ServiceEndpointPolicyProperties `json:"properties"`
}

type ServiceEndpointPolicyProperties struct {
	ServiceEndpointPolicyDefinitions  []ServiceEndpointPolicyDefinition `json:"serviceEndpointPolicyDefinitions,omitempty"`
	Subnets                           []Subnet                          `json:"subnets,omitempty"`
	ResourceGUID                      string                            `json:"resourceGuid,omitempty"`
	ProvisioningState                 string                            `json:"provisioningState,omitempty"`
	ServiceAlias                      string                            `json:"serviceAlias,omitempty"`
	ContextualServiceEndpointPolicies []string                          `json:"contextualServiceEndpointPolicies,omitempty"`
}

type ServiceEndpointPolicyDefinition struct {
	ID         string                                    `json:"id,omitempty"`
	Name       string                                    `json:"name,omitempty"`
	Type       string                                    `json:"type,omitempty"`
	Etag       string                                    `json:"etag,omitempty"`
	Properties ServiceEndpointPolicyDefinitionProperties `json:"properties"`
}

type ServiceEndpointPolicyDefinitionProperties struct {
	Description       string   `json:"description,omitempty"`
	Service           string   `json:"service,omitempty"`
	ServiceResources  []string `json:"serviceResources,omitempty"`
	ProvisioningState string   `json:"provisioningState,omitempty"`
}

var azureServiceEndpointPolicies sim.Store[ServiceEndpointPolicy]

const azureServiceEndpointPolicyDefinitionType = "Microsoft.Network/serviceEndpointPolicies/serviceEndpointPolicyDefinitions"

func registerNetworkServiceEndpointPolicies(srv *sim.Server) {
	azureServiceEndpointPolicies = sim.MakeStore[ServiceEndpointPolicy](srv.DB(), "network_service_endpoint_policies")

	registerAzureNetworkResource(srv, azureNetworkResourceSpec[ServiceEndpointPolicy]{
		collection: "serviceEndpointPolicies",
		// Microsoft.Network capitalizes the subscription-wide list, and the SDK sends
		// exactly that.
		subCollection: "ServiceEndpointPolicies",
		nameParam:     "serviceEndpointPolicyName",
		resourceType:  "Microsoft.Network/serviceEndpointPolicies",
		store:         azureServiceEndpointPolicies,
		header: func(p *ServiceEndpointPolicy) *azureNetworkResourceHeader {
			return &p.azureNetworkResourceHeader
		},
		provision: func(_ context.Context, p *ServiceEndpointPolicy, previous *ServiceEndpointPolicy) error {
			p.Properties.ResourceGUID = sim.NewUUID()
			if previous != nil && previous.Properties.ResourceGUID != "" {
				p.Properties.ResourceGUID = previous.Properties.ResourceGUID
			}
			p.Properties.ProvisioningState = "Succeeded"
			for i := range p.Properties.ServiceEndpointPolicyDefinitions {
				normalizeServiceEndpointPolicyDefinition(p.ID, &p.Properties.ServiceEndpointPolicyDefinitions[i])
			}
			return nil
		},
		project: func(p *ServiceEndpointPolicy) {
			p.Properties.Subnets = subnetsApplyingServiceEndpointPolicy(p.ID)
		},
	})

	registerServiceEndpointPolicyDefinitions(srv)
}

func normalizeServiceEndpointPolicyDefinition(policyID string, def *ServiceEndpointPolicyDefinition) {
	def.ID = armChildID(policyID, "serviceEndpointPolicyDefinitions", def.Name)
	def.Type = azureServiceEndpointPolicyDefinitionType
	def.Etag = azureNetworkEtag()
	def.Properties.ProvisioningState = "Succeeded"
}

func subnetsApplyingServiceEndpointPolicy(policyID string) []Subnet {
	if azureSubnets == nil {
		return nil
	}
	return azureSubnets.Filter(func(sn Subnet) bool {
		for _, ref := range sn.Properties.ServiceEndpointPolicies {
			if strings.EqualFold(ref.ID, policyID) {
				return true
			}
		}
		return false
	})
}

// Definitions are stored inline on the policy, so the policy's read reports
// what the sub-resource client wrote.
func registerServiceEndpointPolicyDefinitions(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	base := armBase + "/serviceEndpointPolicies/{serviceEndpointPolicyName}/serviceEndpointPolicyDefinitions"

	policyID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "serviceEndpointPolicies", sim.PathParam(r, "serviceEndpointPolicyName"))
	}

	srv.HandleFunc("PUT "+base+"/{serviceEndpointPolicyDefinitionName}", func(w http.ResponseWriter, r *http.Request) {
		var req ServiceEndpointPolicyDefinition
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.Name = sim.PathParam(r, "serviceEndpointPolicyDefinitionName")
		id := policyID(r)
		normalizeServiceEndpointPolicyDefinition(id, &req)
		if !azureServiceEndpointPolicies.Update(id, func(p *ServiceEndpointPolicy) {
			for i := range p.Properties.ServiceEndpointPolicyDefinitions {
				if strings.EqualFold(p.Properties.ServiceEndpointPolicyDefinitions[i].Name, req.Name) {
					p.Properties.ServiceEndpointPolicyDefinitions[i] = req
					return
				}
			}
			p.Properties.ServiceEndpointPolicyDefinitions = append(p.Properties.ServiceEndpointPolicyDefinitions, req)
		}) {
			azureNetworkResourceNotFound(w, "Microsoft.Network/serviceEndpointPolicies",
				sim.PathParam(r, "serviceEndpointPolicyName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, req)
	})

	srv.HandleFunc("GET "+base+"/{serviceEndpointPolicyDefinitionName}", func(w http.ResponseWriter, r *http.Request) {
		policy, ok := azureServiceEndpointPolicies.Get(policyID(r))
		if !ok {
			azureNetworkResourceNotFound(w, "Microsoft.Network/serviceEndpointPolicies",
				sim.PathParam(r, "serviceEndpointPolicyName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		want := sim.PathParam(r, "serviceEndpointPolicyDefinitionName")
		for _, def := range policy.Properties.ServiceEndpointPolicyDefinitions {
			if strings.EqualFold(def.Name, want) {
				sim.WriteJSON(w, http.StatusOK, def)
				return
			}
		}
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Service endpoint policy definition %q was not found.", want)
	})

	srv.HandleFunc("DELETE "+base+"/{serviceEndpointPolicyDefinitionName}", func(w http.ResponseWriter, r *http.Request) {
		want := sim.PathParam(r, "serviceEndpointPolicyDefinitionName")
		removed := false
		azureServiceEndpointPolicies.Update(policyID(r), func(p *ServiceEndpointPolicy) {
			kept := p.Properties.ServiceEndpointPolicyDefinitions[:0]
			for _, def := range p.Properties.ServiceEndpointPolicyDefinitions {
				if strings.EqualFold(def.Name, want) {
					removed = true
					continue
				}
				kept = append(kept, def)
			}
			p.Properties.ServiceEndpointPolicyDefinitions = kept
		})
		if !removed {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		policy, ok := azureServiceEndpointPolicies.Get(policyID(r))
		if !ok {
			azureNetworkResourceNotFound(w, "Microsoft.Network/serviceEndpointPolicies",
				sim.PathParam(r, "serviceEndpointPolicyName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		azureWriteList(w, policy.Properties.ServiceEndpointPolicyDefinitions)
	})
}

package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerNetworkSecurityGroups(srv *sim.Server, armBase string, nsgs sim.Store[NetworkSecurityGroup]) {
	srv.HandleFunc("PUT "+armBase+"/networkSecurityGroups/{nsgName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		nsgName := sim.PathParam(r, "nsgName")

		var req NetworkSecurityGroup
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		resourceID := armResourceID(sub, rg, "Microsoft.Network", "networkSecurityGroups", nsgName)

		for _, rule := range req.Properties.SecurityRules {
			if port := azureInvalidSecurityRulePort(rule.Properties); port != "" {
				azureWriteInvalidPortRange(w, port)
				return
			}
		}
		// Set IDs on security rules
		for i := range req.Properties.SecurityRules {
			req.Properties.SecurityRules[i].ID = fmt.Sprintf("%s/securityRules/%s", resourceID, req.Properties.SecurityRules[i].Name)
			req.Properties.SecurityRules[i].Type = "Microsoft.Network/networkSecurityGroups/securityRules"
			req.Properties.SecurityRules[i].Properties.ProvisioningState = "Succeeded"
		}

		nsg := NetworkSecurityGroup{
			ID:       resourceID,
			Name:     nsgName,
			Type:     "Microsoft.Network/networkSecurityGroups",
			Location: req.Location,
			Tags:     req.Tags,
			Properties: NSGProperties{
				SecurityRules:     req.Properties.SecurityRules,
				ProvisioningState: "Succeeded",
			},
		}
		nsgs.Put(resourceID, nsg)
		azureReapplyIngress()

		// go-azure-sdk expects 200 for sync creates
		sim.WriteJSON(w, http.StatusOK, nsg)
	})

	srv.HandleFunc("GET "+armBase+"/networkSecurityGroups/{nsgName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		nsgName := sim.PathParam(r, "nsgName")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "networkSecurityGroups", nsgName)

		nsg, ok := nsgs.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Network/networkSecurityGroups/%s' under resource group '%s' was not found.", nsgName, rg)
			return
		}
		sim.WriteJSON(w, http.StatusOK, nsg)
	})

	srv.HandleFunc("DELETE "+armBase+"/networkSecurityGroups/{nsgName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		nsgName := sim.PathParam(r, "nsgName")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "networkSecurityGroups", nsgName)

		nsgs.Delete(resourceID)
		azureReapplyIngress()
		w.WriteHeader(http.StatusOK)
	})

	// This is handled via the subnet PUT endpoint above (networkSecurityGroup property on subnet).
	// The azurerm_subnet_network_security_group_association resource just PUTs the subnet
	// with the networkSecurityGroup reference set.

	// The NSG handlers above let callers embed SecurityRules in the
	// NSG body, but armnetwork.SecurityRulesClient CRUDs each rule
	// via its sub-resource path. Expose those so the backend can use
	// the per-rule client.

	// PUT rule
	srv.HandleFunc("PUT "+armBase+"/networkSecurityGroups/{nsgName}/securityRules/{ruleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			nsgName := sim.PathParam(r, "nsgName")
			ruleName := sim.PathParam(r, "ruleName")
			nsgID := armResourceID(sub, rg, "Microsoft.Network", "networkSecurityGroups", nsgName)
			ruleID := fmt.Sprintf("%s/securityRules/%s", nsgID, ruleName)

			nsg, ok := nsgs.Get(nsgID)
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The Resource 'Microsoft.Network/networkSecurityGroups/%s' under resource group '%s' was not found.",
					nsgName, rg)
				return
			}

			var req SecurityRule
			if err := sim.ReadJSON(r, &req); err != nil {
				AzureError(w, "InvalidRequestContent",
					"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if port := azureInvalidSecurityRulePort(req.Properties); port != "" {
				azureWriteInvalidPortRange(w, port)
				return
			}
			req.ID = ruleID
			req.Name = ruleName
			req.Type = "Microsoft.Network/networkSecurityGroups/securityRules"
			req.Properties.ProvisioningState = "Succeeded"

			// Real Azure rejects duplicate priorities within an NSG +
			// direction (a rule cannot share a (Priority, Direction)
			// pair with another rule on the same NSG). The error code
			// is `SecurityRuleParameterPriorityAlreadyTaken`. Match
			// that contract so terraform configurations and SDK callers
			// see the real validation flow instead of a silent overwrite.
			for _, existing := range nsg.Properties.SecurityRules {
				if existing.Name == ruleName {
					continue
				}
				if existing.Properties.Priority == req.Properties.Priority &&
					strings.EqualFold(existing.Properties.Direction, req.Properties.Direction) {
					AzureErrorf(w, "SecurityRuleParameterPriorityAlreadyTaken",
						http.StatusBadRequest,
						"Priority %d is already in use for direction %s by rule %q in NSG %q",
						req.Properties.Priority, req.Properties.Direction, existing.Name, nsgName)
					return
				}
			}

			// Upsert inside NSG.Properties.SecurityRules so GET NSG stays consistent.
			found := false
			for i := range nsg.Properties.SecurityRules {
				if nsg.Properties.SecurityRules[i].Name == ruleName {
					nsg.Properties.SecurityRules[i] = req
					found = true
					break
				}
			}
			if !found {
				nsg.Properties.SecurityRules = append(nsg.Properties.SecurityRules, req)
			}
			nsgs.Put(nsgID, nsg)
			azureReapplyIngress()

			sim.WriteJSON(w, http.StatusOK, req)
		})

	// GET rule
	srv.HandleFunc("GET "+armBase+"/networkSecurityGroups/{nsgName}/securityRules/{ruleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			nsgName := sim.PathParam(r, "nsgName")
			ruleName := sim.PathParam(r, "ruleName")
			nsgID := armResourceID(sub, rg, "Microsoft.Network", "networkSecurityGroups", nsgName)

			nsg, ok := nsgs.Get(nsgID)
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"NSG '%s' not found in resource group '%s'.", nsgName, rg)
				return
			}
			for _, rule := range nsg.Properties.SecurityRules {
				if rule.Name == ruleName {
					sim.WriteJSON(w, http.StatusOK, rule)
					return
				}
			}
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Security rule '%s' not found in NSG '%s'.", ruleName, nsgName)
		})

	// DELETE rule
	srv.HandleFunc("DELETE "+armBase+"/networkSecurityGroups/{nsgName}/securityRules/{ruleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			nsgName := sim.PathParam(r, "nsgName")
			ruleName := sim.PathParam(r, "ruleName")
			nsgID := armResourceID(sub, rg, "Microsoft.Network", "networkSecurityGroups", nsgName)

			nsg, ok := nsgs.Get(nsgID)
			if !ok {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			filtered := nsg.Properties.SecurityRules[:0]
			for _, rule := range nsg.Properties.SecurityRules {
				if rule.Name != ruleName {
					filtered = append(filtered, rule)
				}
			}
			nsg.Properties.SecurityRules = filtered
			nsgs.Put(nsgID, nsg)
			azureReapplyIngress()
			w.WriteHeader(http.StatusOK)
		})

	// LIST rules
	srv.HandleFunc("GET "+armBase+"/networkSecurityGroups/{nsgName}/securityRules",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			nsgName := sim.PathParam(r, "nsgName")
			nsgID := armResourceID(sub, rg, "Microsoft.Network", "networkSecurityGroups", nsgName)

			nsg, ok := nsgs.Get(nsgID)
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"NSG '%s' not found.", nsgName)
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"value": nsg.Properties.SecurityRules,
			})
		})
}

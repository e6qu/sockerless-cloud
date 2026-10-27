package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Private endpoints, the consumer half of Azure Private Link. A create:
//
//  1. leases an address in its subnet for an ordinary network interface;
//  2. opens each requested connection in the target's own
//     privateEndpointConnections collection, so the target lists it and the
//     owner's approval is what the endpoint reads back;
//  3. publishes the target's private address in its custom DNS configuration
//     and, with a private DNS zone group, as A records in the zone;
//  4. starts a connection Approved when the target auto-approves the caller,
//     Pending otherwise.

type PrivateEndpoint struct {
	azureNetworkResourceHeader
	ExtendedLocation *azureExtendedLocation    `json:"extendedLocation,omitempty"`
	Properties       PrivateEndpointProperties `json:"properties"`
}

type PrivateEndpointProperties struct {
	Subnet            *SubResource  `json:"subnet,omitempty"`
	NetworkInterfaces []SubResource `json:"networkInterfaces,omitempty"`
	ProvisioningState string        `json:"provisioningState,omitempty"`
	IPVersionType     string        `json:"ipVersionType,omitempty"`
	// Always present, empty when unused: the service reports both, and the azurerm
	// provider walks them on delete without checking for absence.
	PrivateLinkServiceConnections       []PrivateLinkServiceConnection   `json:"privateLinkServiceConnections"`
	ManualPrivateLinkServiceConnections []PrivateLinkServiceConnection   `json:"manualPrivateLinkServiceConnections"`
	CustomDNSConfigs                    []PrivateEndpointCustomDNSConfig `json:"customDnsConfigs,omitempty"`
	ApplicationSecurityGroups           []SubResource                    `json:"applicationSecurityGroups,omitempty"`
	IPConfigurations                    []PrivateEndpointIPConfiguration `json:"ipConfigurations,omitempty"`
	CustomNetworkInterfaceName          string                           `json:"customNetworkInterfaceName,omitempty"`
}

type PrivateLinkServiceConnection struct {
	ID         string                                 `json:"id,omitempty"`
	Name       string                                 `json:"name,omitempty"`
	Type       string                                 `json:"type,omitempty"`
	Etag       string                                 `json:"etag,omitempty"`
	Properties PrivateLinkServiceConnectionProperties `json:"properties"`
}

// The state belongs to the target and is rendered from there on every read.
type PrivateLinkServiceConnectionProperties struct {
	ProvisioningState                 string                             `json:"provisioningState,omitempty"`
	PrivateLinkServiceID              string                             `json:"privateLinkServiceId,omitempty"`
	GroupIDs                          []string                           `json:"groupIds,omitempty"`
	RequestMessage                    string                             `json:"requestMessage,omitempty"`
	PrivateLinkServiceConnectionState *PrivateLinkServiceConnectionState `json:"privateLinkServiceConnectionState,omitempty"`
}

type PrivateEndpointCustomDNSConfig struct {
	Fqdn        string   `json:"fqdn,omitempty"`
	IPAddresses []string `json:"ipAddresses,omitempty"`
}

// Pins one group member of the target to an address in the subnet.
type PrivateEndpointIPConfiguration struct {
	Name       string                                   `json:"name,omitempty"`
	Type       string                                   `json:"type,omitempty"`
	Etag       string                                   `json:"etag,omitempty"`
	Properties PrivateEndpointIPConfigurationProperties `json:"properties"`
}

type PrivateEndpointIPConfigurationProperties struct {
	GroupID          string `json:"groupId,omitempty"`
	MemberName       string `json:"memberName,omitempty"`
	PrivateIPAddress string `json:"privateIPAddress,omitempty"`
}

var azurePrivateEndpoints sim.Store[PrivateEndpoint]

func registerNetworkPrivateEndpoints(srv *sim.Server) {
	azurePrivateEndpoints = sim.MakeStore[PrivateEndpoint](srv.DB(), "network_private_endpoints")

	registerAzureNetworkResource(srv, azureNetworkResourceSpec[PrivateEndpoint]{
		collection:   "privateEndpoints",
		nameParam:    "privateEndpointName",
		resourceType: "Microsoft.Network/privateEndpoints",
		store:        azurePrivateEndpoints,
		noUpdateTags: true,
		header: func(pe *PrivateEndpoint) *azureNetworkResourceHeader {
			return &pe.azureNetworkResourceHeader
		},
		validate:    validatePrivateEndpoint,
		provision:   provisionPrivateEndpoint,
		project:     projectPrivateEndpoint,
		afterDelete: deletePrivateEndpointResources,
	})

	registerPrivateEndpointDNSZoneGroups(srv)
	registerAvailablePrivateEndpointTypes(srv)
}

func validatePrivateEndpoint(w http.ResponseWriter, _ *http.Request, pe *PrivateEndpoint) bool {
	subnetID := ""
	if pe.Properties.Subnet != nil {
		subnetID = pe.Properties.Subnet.ID
	}
	subnet, ok := azureRequireSubnet(w, subnetID)
	if !ok {
		return false
	}
	// A subnet with private-endpoint network policies enabled rejects the
	// endpoint, as Azure does.
	if strings.EqualFold(subnet.Properties.PrivateEndpointNetworkPolicies, "Enabled") {
		AzureErrorf(w, "PrivateEndpointCannotBeCreatedInSubnetThatHasNetworkPoliciesEnabled",
			http.StatusBadRequest,
			"Private endpoints cannot be created in subnet %q because it has private endpoint network policies enabled.",
			subnet.Name)
		return false
	}
	for _, conn := range privateEndpointRequestedConnections(pe) {
		if conn.Properties.PrivateLinkServiceID == "" {
			AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
				"The request format was unexpected: privateLinkServiceId is required on connection %q.", conn.Name)
			return false
		}
		// A type with no private-link surface can never terminate an endpoint; Azure
		// rejects it at request validation, a permanent client error.
		if _, ok := privateLinkTargetFor(conn.Properties.PrivateLinkServiceID); !ok {
			AzureErrorf(w, "PrivateLinkServiceIdNotValid", http.StatusBadRequest,
				"The resource %q does not support private endpoint connections.",
				conn.Properties.PrivateLinkServiceID)
			return false
		}
	}
	return true
}

// Automatic and manual connections share a shape and differ only in whether
// the owner has to act.
func privateEndpointRequestedConnections(pe *PrivateEndpoint) []PrivateLinkServiceConnection {
	out := make([]PrivateLinkServiceConnection, 0,
		len(pe.Properties.PrivateLinkServiceConnections)+len(pe.Properties.ManualPrivateLinkServiceConnections))
	out = append(out, pe.Properties.PrivateLinkServiceConnections...)
	out = append(out, pe.Properties.ManualPrivateLinkServiceConnections...)
	return out
}

func provisionPrivateEndpoint(ctx context.Context, pe *PrivateEndpoint, previous *PrivateEndpoint) error {
	pe.Properties.ProvisioningState = "Succeeded"
	if pe.Properties.IPVersionType == "" {
		pe.Properties.IPVersionType = "IPv4"
	}

	nicID := privateEndpointNICID(pe, previous)
	nic, err := azureCreatePlatformNIC(ctx, azurePlatformNICSpec{
		ID:                        nicID,
		Name:                      nicID[strings.LastIndex(nicID, "/")+1:],
		Location:                  pe.Location,
		SubnetID:                  pe.Properties.Subnet.ID,
		RequestedIP:               privateEndpointRequestedIP(pe),
		AllocationMethod:          privateEndpointAllocationMethod(pe),
		ApplicationSecurityGroups: pe.Properties.ApplicationSecurityGroups,
	})
	if err != nil {
		return err
	}
	pe.Properties.NetworkInterfaces = []SubResource{{ID: nicID}}
	address := nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress

	stamp := func(conns []PrivateLinkServiceConnection, manual bool) error {
		for i := range conns {
			conn := &conns[i]
			if conn.Name == "" {
				conn.Name = pe.Name
			}
			conn.ID = armChildID(pe.ID, "privateLinkServiceConnections", conn.Name)
			conn.Type = "Microsoft.Network/privateEndpoints/privateLinkServiceConnections"
			conn.Etag = azureNetworkEtag()
			conn.Properties.ProvisioningState = "Succeeded"
			if err := openPrivateEndpointConnection(*pe, *conn, address, manual); err != nil {
				return err
			}
		}
		return nil
	}
	if err := stamp(pe.Properties.PrivateLinkServiceConnections, false); err != nil {
		return err
	}
	if err := stamp(pe.Properties.ManualPrivateLinkServiceConnections, true); err != nil {
		return err
	}

	for i := range pe.Properties.IPConfigurations {
		ipcfg := &pe.Properties.IPConfigurations[i]
		ipcfg.Type = "Microsoft.Network/privateEndpoints/ipConfigurations"
		ipcfg.Etag = azureNetworkEtag()
	}

	// Close the connections an update dropped, so no target keeps one the
	// endpoint no longer requests.
	if previous != nil {
		live := map[string]bool{}
		for _, conn := range privateEndpointRequestedConnections(pe) {
			live[strings.ToLower(privateEndpointConnectionID(conn, pe.Name))] = true
		}
		for _, conn := range privateEndpointRequestedConnections(previous) {
			id := privateEndpointConnectionID(conn, previous.Name)
			if !live[strings.ToLower(id)] {
				closePrivateEndpointConnection(conn, previous.Name)
			}
		}
	}
	return nil
}

// Recompute on every read: the subnet's address, the custom DNS configuration,
// and each connection's state as the target reports it.
func projectPrivateEndpoint(pe *PrivateEndpoint) {
	if pe.Properties.PrivateLinkServiceConnections == nil {
		pe.Properties.PrivateLinkServiceConnections = []PrivateLinkServiceConnection{}
	}
	if pe.Properties.ManualPrivateLinkServiceConnections == nil {
		pe.Properties.ManualPrivateLinkServiceConnections = []PrivateLinkServiceConnection{}
	}
	address := ""
	if len(pe.Properties.NetworkInterfaces) > 0 {
		address = azurePlatformNICPrivateIP(pe.Properties.NetworkInterfaces[0].ID)
	}
	pe.Properties.CustomDNSConfigs = nil
	refresh := func(conns []PrivateLinkServiceConnection) {
		for i := range conns {
			conn := &conns[i]
			conn.Properties.PrivateLinkServiceConnectionState = readPrivateEndpointConnectionState(*conn, pe.Name)
			if fqdn := privateEndpointTargetFqdn(*conn); fqdn != "" && address != "" {
				pe.Properties.CustomDNSConfigs = append(pe.Properties.CustomDNSConfigs,
					PrivateEndpointCustomDNSConfig{Fqdn: fqdn, IPAddresses: []string{address}})
			}
		}
	}
	refresh(pe.Properties.PrivateLinkServiceConnections)
	refresh(pe.Properties.ManualPrivateLinkServiceConnections)
}

func deletePrivateEndpointResources(ctx context.Context, id string, deleted PrivateEndpoint) {
	for _, group := range privateEndpointDNSZoneGroups(id) {
		deletePrivateEndpointDNSRecords(group)
		azurePEDNSZoneGroups.Delete(group.ID)
	}
	for _, conn := range privateEndpointRequestedConnections(&deleted) {
		closePrivateEndpointConnection(conn, deleted.Name)
	}
	for _, ref := range deleted.Properties.NetworkInterfaces {
		_ = azureDeletePlatformNIC(ctx, ref.ID)
	}
}

// Azure names the interface after the endpoint unless the caller named it, and
// the name never changes.
func privateEndpointNICID(pe *PrivateEndpoint, previous *PrivateEndpoint) string {
	if previous != nil && len(previous.Properties.NetworkInterfaces) > 0 {
		return previous.Properties.NetworkInterfaces[0].ID
	}
	name := pe.Properties.CustomNetworkInterfaceName
	if name == "" {
		name = pe.Name + ".nic." + sim.NewUUID()
	}
	rgScope := pe.ID[:strings.Index(pe.ID, "/providers/")]
	return rgScope + "/providers/Microsoft.Network/networkInterfaces/" + name
}

func privateEndpointRequestedIP(pe *PrivateEndpoint) string {
	for _, ipcfg := range pe.Properties.IPConfigurations {
		if ipcfg.Properties.PrivateIPAddress != "" {
			return ipcfg.Properties.PrivateIPAddress
		}
	}
	return ""
}

func privateEndpointAllocationMethod(pe *PrivateEndpoint) string {
	if privateEndpointRequestedIP(pe) != "" {
		return "Static"
	}
	return "Dynamic"
}

// Each target reads and writes the same store its own
// privateEndpointConnections surface serves, so both views are one object.
type azurePrivateLinkTarget struct {
	// armType is what the available-type catalog reports and what a resource ID
	// matches against.
	armType   string
	childType string
	groupIDs  []string
	// The public suffix a group is normally reached at, and the private-link zone
	// Azure publishes its private address in.
	dnsSuffixes func(groupID string) (public string, zone string)

	put func(id, name string, props map[string]any)
	get func(id string) (map[string]any, bool)
	del func(id string)
}

// Accessors run at request time, so they read whatever store the owning
// service registered.
var azurePrivateLinkTargets = []azurePrivateLinkTarget{
	{
		armType:   "Microsoft.KeyVault/vaults",
		childType: "Microsoft.KeyVault/vaults/privateEndpointConnections",
		groupIDs:  []string{"vault"},
		dnsSuffixes: func(string) (string, string) {
			return "vault.azure.net", "privatelink.vaultcore.azure.net"
		},
		put: func(id, name string, props map[string]any) {
			keyVaultPrivConn.Put(id, KeyVaultPrivateEndpointConnection{
				ID: id, Name: name, Etag: azureNetworkEtag(),
				Type: "Microsoft.KeyVault/vaults/privateEndpointConnections", Properties: props,
			})
		},
		get: func(id string) (map[string]any, bool) {
			c, ok := keyVaultPrivConn.Get(id)
			return c.Properties, ok
		},
		del: func(id string) { keyVaultPrivConn.Delete(id) },
	},
	{
		armType:   "Microsoft.Storage/storageAccounts",
		childType: "Microsoft.Storage/storageAccounts/privateEndpointConnections",
		groupIDs:  []string{"blob", "table", "queue", "file", "web", "dfs"},
		dnsSuffixes: func(group string) (string, string) {
			if group == "" {
				group = "blob"
			}
			return group + ".core.windows.net", "privatelink." + group + ".core.windows.net"
		},
		put: func(id, name string, props map[string]any) {
			azureStoragePECs.Put(id, storageARMChild{
				ID: id, Name: name, Etag: azureNetworkEtag(),
				Type: "Microsoft.Storage/storageAccounts/privateEndpointConnections", Properties: props,
			})
		},
		get: func(id string) (map[string]any, bool) {
			c, ok := azureStoragePECs.Get(id)
			return c.Properties, ok
		},
		del: func(id string) { azureStoragePECs.Delete(id) },
	},
	{
		armType:   "Microsoft.DocumentDB/databaseAccounts",
		childType: cosmosPECType,
		groupIDs:  []string{"Sql", "MongoDB", "Cassandra", "Gremlin", "Table"},
		dnsSuffixes: func(string) (string, string) {
			return "documents.azure.com", "privatelink.documents.azure.com"
		},
		put: func(id, name string, props map[string]any) {
			cosmosPECs.Put(id, CosmosPrivateEndpointConnection{
				ID: id, Name: name, Type: cosmosPECType, Properties: props,
			})
		},
		get: func(id string) (map[string]any, bool) {
			c, ok := cosmosPECs.Get(id)
			return c.Properties, ok
		},
		del: func(id string) { cosmosPECs.Delete(id) },
	},
	{
		armType:   "Microsoft.DBforPostgreSQL/flexibleServers",
		childType: "Microsoft.DBforPostgreSQL/flexibleServers/privateEndpointConnections",
		groupIDs:  []string{"postgresqlServer"},
		dnsSuffixes: func(string) (string, string) {
			return "postgres.database.azure.com", "privatelink.postgres.database.azure.com"
		},
		put: func(id, name string, props map[string]any) {
			pgPrivateEndpointCxn.Put(id, PGPrivateEndpointConnection{
				ID: id, Name: name,
				Type: "Microsoft.DBforPostgreSQL/flexibleServers/privateEndpointConnections", Properties: props,
			})
		},
		get: func(id string) (map[string]any, bool) {
			c, ok := pgPrivateEndpointCxn.Get(id)
			return c.Properties, ok
		},
		del: func(id string) { pgPrivateEndpointCxn.Delete(id) },
	},
	{
		armType:   "Microsoft.EventHub/namespaces",
		childType: "Microsoft.EventHub/namespaces/privateEndpointConnections",
		groupIDs:  []string{"namespace"},
		dnsSuffixes: func(string) (string, string) {
			return "servicebus.windows.net", "privatelink.servicebus.windows.net"
		},
		put: func(id, name string, props map[string]any) {
			ehPrivateConns.Put(id, EHPrivateEndpointConnection{
				ID: id, Name: name,
				Type: "Microsoft.EventHub/namespaces/privateEndpointConnections", Properties: props,
			})
		},
		get: func(id string) (map[string]any, bool) {
			c, ok := ehPrivateConns.Get(id)
			return c.Properties, ok
		},
		del: func(id string) { ehPrivateConns.Delete(id) },
	},
	{
		armType:   "Microsoft.Web/sites",
		childType: "Microsoft.Web/sites/privateEndpointConnections",
		groupIDs:  []string{"sites"},
		dnsSuffixes: func(string) (string, string) {
			return "azurewebsites.net", "privatelink.azurewebsites.net"
		},
		put: func(id, name string, props map[string]any) {
			webSitePECs.Put(id, WebSitePrivateEndpointConnection{
				ID: id, Name: name, Type: webSitePECType(id), Properties: props,
			})
		},
		get: func(id string) (map[string]any, bool) {
			c, ok := webSitePECs.Get(id)
			return c.Properties, ok
		},
		del: func(id string) { webSitePECs.Delete(id) },
	},
	{
		armType:   "Microsoft.Web/hostingEnvironments",
		childType: aseResourceType + "/privateEndpointConnections",
		// An App Service Environment publishes no named group: an endpoint reaches the
		// environment itself, as the specification's example shows.
		dnsSuffixes: func(string) (string, string) {
			return "appserviceenvironment.net", "privatelink.appserviceenvironment.net"
		},
		put: func(id, name string, props map[string]any) {
			webEnvironmentPECs.Put(id, WebSitePrivateEndpointConnection{
				ID: id, Name: name, Type: webSitePECType(id), Properties: props,
			})
		},
		get: func(id string) (map[string]any, bool) {
			c, ok := webEnvironmentPECs.Get(id)
			return c.Properties, ok
		},
		del: func(id string) { webEnvironmentPECs.Delete(id) },
	},
	{
		armType:   "Microsoft.Network/privateLinkServices",
		childType: azurePLSConnectionType,
		// A private link service publishes no named groups: a consumer reaches the
		// whole service, and names its DNS itself.
		put: func(id, name string, props map[string]any) {
			conn := NetworkPrivateEndpointConnection{
				ID: id, Name: name, Type: azurePLSConnectionType, Etag: azureNetworkEtag(),
			}
			applyNetworkConnectionProperties(&conn, props)
			azurePLSConnections.Put(id, conn)
		},
		get: func(id string) (map[string]any, bool) {
			conn, ok := azurePLSConnections.Get(id)
			if !ok {
				return nil, false
			}
			return networkConnectionProperties(conn), true
		},
		del: func(id string) { azurePLSConnections.Delete(id) },
	},
}

func applyNetworkConnectionProperties(conn *NetworkPrivateEndpointConnection, props map[string]any) {
	conn.Properties.ProvisioningState = azureMapString(props, "provisioningState")
	conn.Properties.LinkIdentifier = azureMapString(props, "linkIdentifier")
	conn.Properties.PrivateEndpointLocation = azureMapString(props, "privateEndpointLocation")
	if pe, ok := props["privateEndpoint"].(map[string]any); ok {
		conn.Properties.PrivateEndpoint = &PrivateEndpoint{
			azureNetworkResourceHeader: azureNetworkResourceHeader{ID: azureMapString(pe, "id")},
		}
	}
	if state, ok := props["privateLinkServiceConnectionState"].(map[string]any); ok {
		conn.Properties.PrivateLinkServiceConnectionState = &PrivateLinkServiceConnectionState{
			Status:          azureMapString(state, "status"),
			Description:     azureMapString(state, "description"),
			ActionsRequired: azureMapString(state, "actionsRequired"),
		}
	}
}

func networkConnectionProperties(conn NetworkPrivateEndpointConnection) map[string]any {
	props := map[string]any{
		"provisioningState": conn.Properties.ProvisioningState,
		"linkIdentifier":    conn.Properties.LinkIdentifier,
	}
	if conn.Properties.PrivateEndpoint != nil {
		props["privateEndpoint"] = map[string]any{"id": conn.Properties.PrivateEndpoint.ID}
	}
	if state := conn.Properties.PrivateLinkServiceConnectionState; state != nil {
		props["privateLinkServiceConnectionState"] = map[string]any{
			"status":          state.Status,
			"description":     state.Description,
			"actionsRequired": state.ActionsRequired,
		}
	}
	return props
}

func azureMapString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// Resource IDs are case-insensitive.
func privateLinkTargetFor(resourceID string) (azurePrivateLinkTarget, bool) {
	lower := strings.ToLower(resourceID)
	for _, target := range azurePrivateLinkTargets {
		if strings.Contains(lower, "/providers/"+strings.ToLower(target.armType)+"/") {
			return target, true
		}
	}
	return azurePrivateLinkTarget{}, false
}

// Azure names the connection after the endpoint that opened it.
func privateEndpointConnectionID(conn PrivateLinkServiceConnection, endpointName string) string {
	return armChildID(conn.Properties.PrivateLinkServiceID, "privateEndpointConnections", endpointName)
}

// Connected immediately when the target auto-approves the caller's
// subscription; otherwise it waits for the owner.
func openPrivateEndpointConnection(pe PrivateEndpoint, conn PrivateLinkServiceConnection, address string, manual bool) error {
	target, ok := privateLinkTargetFor(conn.Properties.PrivateLinkServiceID)
	if !ok {
		return fmt.Errorf("resource %q does not support private endpoint connections",
			conn.Properties.PrivateLinkServiceID)
	}
	id := privateEndpointConnectionID(conn, pe.Name)
	status, description := "Pending", "Awaiting approval by the owner of the linked resource."
	if !manual && privateEndpointAutoApproved(pe, conn) {
		status, description = "Approved", "Auto-Approved"
	}
	// Keep the owner's decision: rewriting the endpoint must not re-approve a
	// rejected link.
	if existing, found := target.get(id); found {
		if state, ok := existing["privateLinkServiceConnectionState"].(map[string]any); ok {
			if s := azureMapString(state, "status"); s != "" {
				status = s
				description = azureMapString(state, "description")
			}
		}
	}
	target.put(id, pe.Name, map[string]any{
		"privateEndpoint":         map[string]any{"id": pe.ID},
		"privateEndpointLocation": pe.Location,
		"privateLinkServiceConnectionState": map[string]any{
			"status":          status,
			"description":     description,
			"actionsRequired": "None",
		},
		"provisioningState": "Succeeded",
		"linkIdentifier":    privateEndpointLinkIdentifier(id),
		"groupIds":          conn.Properties.GroupIDs,
		"requestMessage":    conn.Properties.RequestMessage,
		"privateIPAddress":  address,
	})
	return nil
}

// A private link service answers from its auto-approval list; every other type
// auto-approves a caller owning the resource, here the same subscription.
func privateEndpointAutoApproved(pe PrivateEndpoint, conn PrivateLinkServiceConnection) bool {
	targetID := conn.Properties.PrivateLinkServiceID
	if pls, ok := azurePrivateLinkServices.Get(targetID); ok {
		return privateLinkServiceAutoApprovesFor(pls, azureSubscriptionOf(pe.ID))
	}
	return strings.EqualFold(azureSubscriptionOf(pe.ID), azureSubscriptionOf(targetID))
}

func azureSubscriptionOf(resourceID string) string {
	parts := strings.Split(strings.TrimPrefix(resourceID, "/"), "/")
	if len(parts) >= 2 && strings.EqualFold(parts[0], "subscriptions") {
		return parts[1]
	}
	return ""
}

// A connection whose target-side object is gone reads Disconnected, as Azure
// reports once the owner removes it.
func readPrivateEndpointConnectionState(conn PrivateLinkServiceConnection, endpointName string) *PrivateLinkServiceConnectionState {
	target, ok := privateLinkTargetFor(conn.Properties.PrivateLinkServiceID)
	if !ok {
		return conn.Properties.PrivateLinkServiceConnectionState
	}
	props, found := target.get(privateEndpointConnectionID(conn, endpointName))
	if !found {
		return &PrivateLinkServiceConnectionState{
			Status:      "Disconnected",
			Description: "The connection was removed by the owner of the linked resource.",
		}
	}
	state, ok := props["privateLinkServiceConnectionState"].(map[string]any)
	if !ok {
		return nil
	}
	return &PrivateLinkServiceConnectionState{
		Status:          azureMapString(state, "status"),
		Description:     azureMapString(state, "description"),
		ActionsRequired: azureMapString(state, "actionsRequired"),
	}
}

func closePrivateEndpointConnection(conn PrivateLinkServiceConnection, endpointName string) {
	if target, ok := privateLinkTargetFor(conn.Properties.PrivateLinkServiceID); ok {
		target.del(privateEndpointConnectionID(conn, endpointName))
	}
}

// A stable number derived from the connection.
func privateEndpointLinkIdentifier(connectionID string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(connectionID)))
	return fmt.Sprintf("%d", 536870912+int64(h.Sum32()%1_000_000))
}

func privateEndpointTargetFqdn(conn PrivateLinkServiceConnection) string {
	target, ok := privateLinkTargetFor(conn.Properties.PrivateLinkServiceID)
	if !ok || target.dnsSuffixes == nil {
		return ""
	}
	group := ""
	if len(conn.Properties.GroupIDs) > 0 {
		group = conn.Properties.GroupIDs[0]
	}
	public, _ := target.dnsSuffixes(group)
	name := conn.Properties.PrivateLinkServiceID[strings.LastIndex(conn.Properties.PrivateLinkServiceID, "/")+1:]
	return name + "." + public
}

// The catalog is the set of targets an endpoint can actually connect to, so it
// cannot drift from the behavior.
func registerAvailablePrivateEndpointTypes(srv *sim.Server) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		out := make([]map[string]any, 0, len(azurePrivateLinkTargets))
		for _, target := range azurePrivateLinkTargets {
			resourceName := target.armType
			out = append(out, map[string]any{
				"name":         resourceName,
				"id":           armSubscriptionResourceID(sub, "Microsoft.Network", "availablePrivateEndpointTypes", resourceName),
				"type":         "Microsoft.Network/availablePrivateEndpointTypes",
				"resourceName": resourceName,
				"displayName":  resourceName,
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	}
	srv.HandleFunc("GET "+azureNetworkSubBase()+"/locations/{location}/availablePrivateEndpointTypes", handler)
	srv.HandleFunc("GET "+azureNetworkArmBase()+"/locations/{location}/availablePrivateEndpointTypes", handler)
}

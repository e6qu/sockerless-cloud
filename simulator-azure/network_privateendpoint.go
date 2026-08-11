package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Private endpoints (Microsoft.Network/privateEndpoints) are the consumer half
// of Azure Private Link. Creating one does four things in the real service, and
// does all four here:
//
//  1. It takes an address out of the subnet it names and puts a network
//     interface there — the same interface type a caller creates directly, in
//     the same subnet fabric, so the address it gets is genuinely allocated and
//     cannot collide with anything else in that subnet.
//  2. For each requested connection it opens a private endpoint connection ON
//     THE TARGET RESOURCE. That connection is not a copy: it is written into
//     the very collection the target's own privateEndpointConnections surface
//     serves, so a Key Vault, a storage account, a Cosmos DB account, a
//     PostgreSQL flexible server, an Event Hubs namespace or another private
//     link service lists the connection the endpoint opened, and the approval
//     decision the target's owner makes there is what the endpoint reads back.
//  3. It publishes the target's private address under a custom DNS
//     configuration, and — when a private DNS zone group is attached — as real
//     A records in the referenced private DNS zone, which the zone's own record
//     surface then serves.
//  4. Connections opened against a target that auto-approves the caller start
//     Approved; everything else starts Pending until the target's owner acts.

// PrivateEndpoint mirrors Microsoft.Network/privateEndpoints.
type PrivateEndpoint struct {
	azureNetworkResourceHeader
	ExtendedLocation *azureExtendedLocation    `json:"extendedLocation,omitempty"`
	Properties       PrivateEndpointProperties `json:"properties"`
}

// PrivateEndpointProperties holds the endpoint's configuration and the
// read-only state the Microsoft.Network resource provider computes for it.
type PrivateEndpointProperties struct {
	Subnet            *SubResource  `json:"subnet,omitempty"`
	NetworkInterfaces []SubResource `json:"networkInterfaces,omitempty"`
	ProvisioningState string        `json:"provisioningState,omitempty"`
	IPVersionType     string        `json:"ipVersionType,omitempty"`
	// Both connection collections are always present, empty when unused: the
	// service reports them unconditionally, and a client that walks them
	// without checking for absence — the HashiCorp provider does exactly that
	// on delete — faults on a document that omits either.
	PrivateLinkServiceConnections       []PrivateLinkServiceConnection   `json:"privateLinkServiceConnections"`
	ManualPrivateLinkServiceConnections []PrivateLinkServiceConnection   `json:"manualPrivateLinkServiceConnections"`
	CustomDNSConfigs                    []PrivateEndpointCustomDNSConfig `json:"customDnsConfigs,omitempty"`
	ApplicationSecurityGroups           []SubResource                    `json:"applicationSecurityGroups,omitempty"`
	IPConfigurations                    []PrivateEndpointIPConfiguration `json:"ipConfigurations,omitempty"`
	CustomNetworkInterfaceName          string                           `json:"customNetworkInterfaceName,omitempty"`
}

// PrivateLinkServiceConnection is one requested connection from an endpoint to
// a linkable resource.
type PrivateLinkServiceConnection struct {
	ID         string                                 `json:"id,omitempty"`
	Name       string                                 `json:"name,omitempty"`
	Type       string                                 `json:"type,omitempty"`
	Etag       string                                 `json:"etag,omitempty"`
	Properties PrivateLinkServiceConnectionProperties `json:"properties"`
}

// PrivateLinkServiceConnectionProperties holds the target and the approval
// state of one connection. The state is read-only on this surface: it belongs
// to the target resource, and is rendered from there on every read.
type PrivateLinkServiceConnectionProperties struct {
	ProvisioningState                 string                             `json:"provisioningState,omitempty"`
	PrivateLinkServiceID              string                             `json:"privateLinkServiceId,omitempty"`
	GroupIDs                          []string                           `json:"groupIds,omitempty"`
	RequestMessage                    string                             `json:"requestMessage,omitempty"`
	PrivateLinkServiceConnectionState *PrivateLinkServiceConnectionState `json:"privateLinkServiceConnectionState,omitempty"`
}

// PrivateEndpointCustomDNSConfig maps a target's fully-qualified name onto the
// addresses the endpoint made it reachable at.
type PrivateEndpointCustomDNSConfig struct {
	Fqdn        string   `json:"fqdn,omitempty"`
	IPAddresses []string `json:"ipAddresses,omitempty"`
}

// PrivateEndpointIPConfiguration pins one group member of the target to a
// specific address in the endpoint's subnet.
type PrivateEndpointIPConfiguration struct {
	Name       string                                   `json:"name,omitempty"`
	Type       string                                   `json:"type,omitempty"`
	Etag       string                                   `json:"etag,omitempty"`
	Properties PrivateEndpointIPConfigurationProperties `json:"properties"`
}

// PrivateEndpointIPConfigurationProperties holds one pinned group member.
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

// validatePrivateEndpoint applies the request-validation rules: an endpoint
// needs a subnet that exists, and every requested connection must name a target.
func validatePrivateEndpoint(w http.ResponseWriter, _ *http.Request, pe *PrivateEndpoint) bool {
	subnetID := ""
	if pe.Properties.Subnet != nil {
		subnetID = pe.Properties.Subnet.ID
	}
	subnet, ok := azureRequireSubnet(w, subnetID)
	if !ok {
		return false
	}
	// A subnet whose private-endpoint network policies are enabled rejects the
	// endpoint, exactly as the real resource provider does.
	if strings.EqualFold(subnet.Properties.PrivateEndpointNetworkPolicies, "Enabled") {
		sim.AzureErrorf(w, "PrivateEndpointCannotBeCreatedInSubnetThatHasNetworkPoliciesEnabled",
			http.StatusBadRequest,
			"Private endpoints cannot be created in subnet %q because it has private endpoint network policies enabled.",
			subnet.Name)
		return false
	}
	for _, conn := range privateEndpointRequestedConnections(pe) {
		if conn.Properties.PrivateLinkServiceID == "" {
			sim.AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
				"The request format was unexpected: privateLinkServiceId is required on connection %q.", conn.Name)
			return false
		}
		// A resource type that publishes no private-link surface can never
		// terminate an endpoint. Azure rejects that during request validation,
		// which is a permanent client error rather than a retryable one.
		if _, ok := privateLinkTargetFor(conn.Properties.PrivateLinkServiceID); !ok {
			sim.AzureErrorf(w, "PrivateLinkServiceIdNotValid", http.StatusBadRequest,
				"The resource %q does not support private endpoint connections.",
				conn.Properties.PrivateLinkServiceID)
			return false
		}
	}
	return true
}

// privateEndpointRequestedConnections returns the automatic and manual
// connections in one list; both carry the same shape and differ only in whether
// the target's owner has to act on them.
func privateEndpointRequestedConnections(pe *PrivateEndpoint) []PrivateLinkServiceConnection {
	out := make([]PrivateLinkServiceConnection, 0,
		len(pe.Properties.PrivateLinkServiceConnections)+len(pe.Properties.ManualPrivateLinkServiceConnections))
	out = append(out, pe.Properties.PrivateLinkServiceConnections...)
	out = append(out, pe.Properties.ManualPrivateLinkServiceConnections...)
	return out
}

// provisionPrivateEndpoint puts the endpoint's interface in the subnet and
// opens a connection on every target it names.
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

	// Stamp identity on each requested connection and open it on its target.
	stamp := func(conns []PrivateLinkServiceConnection, manual bool) error {
		for i := range conns {
			conn := &conns[i]
			if conn.Name == "" {
				conn.Name = pe.Name
			}
			conn.ID = azureNetworkChildID(pe.ID, "privateLinkServiceConnections", conn.Name)
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

	// Close the connections an update dropped, so a target never keeps a
	// connection the endpoint no longer requests.
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

// projectPrivateEndpoint refreshes what the resource provider recomputes on
// every read: the address the subnet allocated, the custom DNS configuration it
// publishes, and each connection's approval state as the target now reports it.
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

// deletePrivateEndpointResources releases the endpoint's interface, closes
// every connection it opened, and removes the DNS records its zone groups
// published.
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

// privateEndpointNICID is the ARM id of the interface the endpoint owns. Azure
// names it after the endpoint unless the caller asked for a specific name, and
// the name never changes once the endpoint exists.
func privateEndpointNICID(pe *PrivateEndpoint, previous *PrivateEndpoint) string {
	if previous != nil && len(previous.Properties.NetworkInterfaces) > 0 {
		return previous.Properties.NetworkInterfaces[0].ID
	}
	name := pe.Properties.CustomNetworkInterfaceName
	if name == "" {
		name = pe.Name + ".nic." + generateUUID()
	}
	rgScope := pe.ID[:strings.Index(pe.ID, "/providers/")]
	return rgScope + "/providers/Microsoft.Network/networkInterfaces/" + name
}

// privateEndpointRequestedIP is the address the caller pinned through an IP
// configuration, if any.
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

// ---------------------------------------------------------------------------
// Target-side private endpoint connections
// ---------------------------------------------------------------------------

// azurePrivateLinkTarget describes how one resource provider records the
// private endpoint connections opened against its resources. Each target reads
// and writes the SAME store its own privateEndpointConnections surface serves,
// so the endpoint's view and the target owner's view are one object rather than
// two that have to be kept in step.
type azurePrivateLinkTarget struct {
	// armType is the target's ARM resource type, e.g.
	// "Microsoft.KeyVault/vaults". It is both what the available-type catalog
	// reports and what a resource id is matched against.
	armType string
	// childType is the ARM type of the connection resource.
	childType string
	// groupIDs are the private-link groups the resource publishes.
	groupIDs []string
	// dnsSuffixes returns the public fully-qualified suffix a group is normally
	// reached at, and the private-link DNS zone Azure publishes its private
	// address in.
	dnsSuffixes func(groupID string) (public string, zone string)

	put func(id, name string, props map[string]any)
	get func(id string) (map[string]any, bool)
	del func(id string)
}

// azurePrivateLinkTargets is the set of resource types this simulator can
// terminate a private endpoint on. Each entry's accessors are evaluated when a
// request runs, so they read whatever store the owning service registered.
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
		armType:   "Microsoft.Network/privateLinkServices",
		childType: azurePLSConnectionType,
		// A private link service publishes no named groups: a consumer reaches
		// the whole service, and its DNS name is the consumer's to choose.
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

// applyNetworkConnectionProperties copies the neutral property bag onto a
// private-link-service connection record.
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

// networkConnectionProperties renders a private-link-service connection back as
// the neutral property bag every other target speaks.
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

// privateLinkTargetFor resolves the target descriptor for a resource id. ARM
// resource ids are case-insensitive, so the type is matched that way.
func privateLinkTargetFor(resourceID string) (azurePrivateLinkTarget, bool) {
	lower := strings.ToLower(resourceID)
	for _, target := range azurePrivateLinkTargets {
		if strings.Contains(lower, "/providers/"+strings.ToLower(target.armType)+"/") {
			return target, true
		}
	}
	return azurePrivateLinkTarget{}, false
}

// privateEndpointConnectionID is the ARM id of the connection on the target
// resource. Azure names it after the private endpoint that opened it.
func privateEndpointConnectionID(conn PrivateLinkServiceConnection, endpointName string) string {
	return conn.Properties.PrivateLinkServiceID + "/privateEndpointConnections/" + endpointName
}

// openPrivateEndpointConnection writes the connection into the target's own
// collection. An endpoint whose target auto-approves the caller's subscription
// is connected immediately; every other connection waits for the target owner.
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
	// An existing connection keeps whatever the target's owner already decided:
	// re-writing the endpoint must not silently re-approve a rejected link.
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

// privateEndpointAutoApproved reports whether the target connects the endpoint
// without the owner acting. A private link service answers from its own
// auto-approval subscription list; every other resource type auto-approves a
// connection whose caller owns the resource, which in this simulator means the
// endpoint and the target sit in the same subscription.
func privateEndpointAutoApproved(pe PrivateEndpoint, conn PrivateLinkServiceConnection) bool {
	targetID := conn.Properties.PrivateLinkServiceID
	if pls, ok := azurePrivateLinkServices.Get(targetID); ok {
		return privateLinkServiceAutoApprovesFor(pls, azureSubscriptionOf(pe.ID))
	}
	return strings.EqualFold(azureSubscriptionOf(pe.ID), azureSubscriptionOf(targetID))
}

// azureSubscriptionOf extracts the subscription id from an ARM resource id.
func azureSubscriptionOf(resourceID string) string {
	parts := strings.Split(strings.TrimPrefix(resourceID, "/"), "/")
	if len(parts) >= 2 && strings.EqualFold(parts[0], "subscriptions") {
		return parts[1]
	}
	return ""
}

// readPrivateEndpointConnectionState renders the connection state from the
// target resource, which owns it. A connection whose target-side object is gone
// reads as Disconnected — what Azure reports once the owner removes it.
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

// privateEndpointLinkIdentifier is the consumer link id Azure reports on a
// connection: a stable number derived from the connection itself.
func privateEndpointLinkIdentifier(connectionID string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(connectionID)))
	return fmt.Sprintf("%d", 536870912+int64(h.Sum32()%1_000_000))
}

// privateEndpointTargetFqdn is the fully-qualified name the endpoint makes the
// target reachable at, used for the endpoint's custom DNS configuration.
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

// ---------------------------------------------------------------------------
// Available private endpoint types
// ---------------------------------------------------------------------------

// registerAvailablePrivateEndpointTypes reports which resource types a private
// endpoint can terminate on in a region. The list is the set of targets this
// simulator can actually connect an endpoint to, so the catalog and the
// behaviour cannot drift apart.
func registerAvailablePrivateEndpointTypes(srv *sim.Server) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		out := make([]map[string]any, 0, len(azurePrivateLinkTargets))
		for _, target := range azurePrivateLinkTargets {
			resourceName := target.armType
			out = append(out, map[string]any{
				"name": resourceName,
				"id": fmt.Sprintf("/subscriptions/%s/providers/Microsoft.Network/availablePrivateEndpointTypes/%s",
					sub, resourceName),
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

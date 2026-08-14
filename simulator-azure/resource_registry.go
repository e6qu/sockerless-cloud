package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Cross-slice enumeration for the Azure Resource Manager resource lists —
// Resources_List (`GET /subscriptions/{sub}/resources`) and
// Resources_ListByResourceGroup. Every tracked resource type contributes the
// store the slice that owns it keeps its rows in, so the Microsoft.Resources
// handlers stay provider-agnostic and a new family is one registration.
//
// Real API:
//
//	https://learn.microsoft.com/en-us/rest/api/resources/resources/list

// azureResourceRow is the generic view ARM answers a resource list with — the
// GenericResourceExpanded schema's identity, placement, classification and
// tags. A resource list never carries a resource's provider-specific
// `properties` document; a client that wants those reads the resource through
// its own provider, which is what the Key Vault cache in
// terraform-provider-azurerm does after reading ids out of this list.
type azureResourceRow struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Location  string            `json:"location,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	ManagedBy string            `json:"managedBy,omitempty"`
	Sku       json.RawMessage   `json:"sku,omitempty"`
	Identity  json.RawMessage   `json:"identity,omitempty"`
	Plan      json.RawMessage   `json:"plan,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`

	// ProvisioningState is an $expand-only member: ARM reports it at the top
	// level of a list row, while the resource itself records it inside its own
	// properties document.
	ProvisioningState string `json:"provisioningState,omitempty"`
}

// azureStoredResource is the subset of a stored resource's wire form the
// generic view is built from. Decoding the row's own JSON — rather than
// reading Go fields — reads the ARM contract every tracked resource already
// implements, so a slice needs no per-type projection function.
type azureStoredResource struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Kind       string            `json:"kind"`
	ManagedBy  string            `json:"managedBy"`
	Sku        json.RawMessage   `json:"sku"`
	Identity   json.RawMessage   `json:"identity"`
	Plan       json.RawMessage   `json:"plan"`
	Tags       map[string]string `json:"tags"`
	Properties struct {
		ProvisioningState string `json:"provisioningState"`
	} `json:"properties"`
}

// azureResourceEnumerator returns the generic rows for every resource one
// slice currently holds.
type azureResourceEnumerator func() []azureResourceRow

// azureTrackedResources maps a tracked resource type — keyed by its lowercased
// "provider/type" spelling, the same key shape resourceMoveHooks uses — to the
// enumeration that reads it out of the slice that owns it.
//
// The table holds only resources ARM tracks: those that carry a location and
// therefore appear in a resource list. A provider's proxy children — a subnet,
// a Service Bus queue, a DNS record set, a role assignment — have no location
// and are reached through their parent's own API, which is why ARM leaves them
// out of this list and so does the simulator.
//
// Every entry reads a package-level store variable through a closure, so the
// read happens at request time, after each slice's register function has
// assigned its store. That is the same reason resourceMoveHooks seeds its
// package-variable-backed hooks statically.
var azureTrackedResources = map[string]azureResourceEnumerator{
	// Microsoft.Web
	"microsoft.web/sites":        azureRowsOf(func() sim.Store[Site] { return azfSites }),
	"microsoft.web/sites/slots":  azureRowsOf(func() sim.Store[Site] { return webSlots }),
	"microsoft.web/serverfarms":  azureRowsOf(func() sim.Store[AppServicePlan] { return azureAppServicePlans }),
	"microsoft.web/certificates": azureRowsOf(func() sim.Store[WebCertificate] { return webCertificates }),
	"microsoft.web/staticsites":  azureRowsOf(func() sim.Store[StaticSiteResource] { return webStaticSites }),

	// Microsoft.Storage
	"microsoft.storage/storageaccounts": azureRowsOf(func() sim.Store[StorageAccount] { return azStorageAccounts }),

	// Microsoft.KeyVault
	"microsoft.keyvault/vaults":      azureRowsOf(func() sim.Store[KeyVault] { return keyVaults }),
	"microsoft.keyvault/managedhsms": azureRowsOf(func() sim.Store[ManagedHSM] { return managedHSMs }),

	// Microsoft.Network
	"microsoft.network/virtualnetworks":                     azureRowsOf(func() sim.Store[VirtualNetwork] { return azureVnets }),
	"microsoft.network/networksecuritygroups":               azureRowsOf(func() sim.Store[NetworkSecurityGroup] { return azureNSGs }),
	"microsoft.network/natgateways":                         azureRowsOf(func() sim.Store[NatGateway] { return azureNatGateways }),
	"microsoft.network/routetables":                         azureRowsOf(func() sim.Store[RouteTable] { return azureRouteTables }),
	"microsoft.network/publicipaddresses":                   azureRowsOf(func() sim.Store[PublicIPAddress] { return azurePublicIPs }),
	"microsoft.network/publicipprefixes":                    azureRowsOf(func() sim.Store[PublicIPPrefix] { return azurePublicIPPrefixes }),
	"microsoft.network/loadbalancers":                       azureRowsOf(func() sim.Store[LoadBalancer] { return azureLBs }),
	"microsoft.network/networkinterfaces":                   azureRowsOf(func() sim.Store[NetworkInterface] { return azureNICs }),
	"microsoft.network/applicationsecuritygroups":           azureRowsOf(func() sim.Store[ApplicationSecurityGroup] { return azureASGs }),
	"microsoft.network/privatelinkservices":                 azureRowsOf(func() sim.Store[PrivateLinkService] { return azurePrivateLinkServices }),
	"microsoft.network/privateendpoints":                    azureRowsOf(func() sim.Store[PrivateEndpoint] { return azurePrivateEndpoints }),
	"microsoft.network/networkprofiles":                     azureRowsOf(func() sim.Store[NetworkProfile] { return azureNetworkProfiles }),
	"microsoft.network/virtualnetworktaps":                  azureRowsOf(func() sim.Store[VirtualNetworkTap] { return azureVirtualNetworkTaps }),
	"microsoft.network/serviceendpointpolicies":             azureRowsOf(func() sim.Store[ServiceEndpointPolicy] { return azureServiceEndpointPolicies }),
	"microsoft.network/applicationgateways":                 azureRowsOf(func() sim.Store[ApplicationGateway] { return azureApplicationGateways }),
	"microsoft.network/networkmanagers":                     azureRowsOf(func() sim.Store[NetworkManager] { return azureNetworkManagers }),
	"microsoft.network/networkwatchers":                     azureRowsOf(func() sim.Store[NetworkWatcher] { return azureNetworkWatchers }),
	"microsoft.network/dnszones":                            azureRowsOf(func() sim.Store[PublicDnsZone] { return azurePublicDNSZones }),
	"microsoft.network/privatednszones":                     azureRowsOf(func() sim.Store[PrivateDnsZone] { return azurePrivateDNSZones }),
	"microsoft.network/privatednszones/virtualnetworklinks": azureRowsOf(func() sim.Store[VNetLink] { return azurePrivateDNSVNetLinks }),

	// Microsoft.Compute
	"microsoft.compute/virtualmachines":            azureRowsOf(func() sim.Store[VirtualMachine] { return azureVMs }),
	"microsoft.compute/virtualmachines/extensions": azureRowsOf(func() sim.Store[VirtualMachineExtension] { return azureVMExtensions }),

	// Microsoft.App
	"microsoft.app/managedenvironments":                     azureRowsOf(func() sim.Store[ContainerAppEnvironment] { return acaEnvironments }),
	"microsoft.app/containerapps":                           azureRowsOf(func() sim.Store[ContainerApp] { return acaApps }),
	"microsoft.app/jobs":                                    azureRowsOf(func() sim.Store[ContainerAppJob] { return acaJobs }),
	"microsoft.app/managedenvironments/certificates":        azureRowsOf(func() sim.Store[Certificate] { return acaEnvCertificates }),
	"microsoft.app/managedenvironments/managedcertificates": azureRowsOf(func() sim.Store[ManagedCertificate] { return acaEnvManagedCertificates }),

	// Microsoft.ContainerInstance / Microsoft.ContainerRegistry
	"microsoft.containerinstance/containergroups": azureRowsOf(func() sim.Store[ACIContainerGroup] { return aciContainerGroups }),
	"microsoft.containerregistry/registries":      azureRowsOf(func() sim.Store[Registry] { return acrRegistries }),

	// Messaging
	"microsoft.servicebus/namespaces":           azureRowsOf(func() sim.Store[SBNamespace] { return sbNamespaces }),
	"microsoft.eventhub/namespaces":             azureRowsOf(func() sim.Store[EHNamespace] { return ehNamespaces }),
	"microsoft.eventgrid/topics":                azureRowsOf(func() sim.Store[EventGridTopic] { return eventGridTopics }),
	"microsoft.eventgrid/domains":               azureRowsOf(func() sim.Store[EventGridTopic] { return eventGridDomains }),
	"microsoft.eventgrid/systemtopics":          azureRowsOf(func() sim.Store[EventGridTopic] { return eventGridSystemTopics }),
	"microsoft.eventgrid/partnertopics":         azureRowsOf(func() sim.Store[EventGridTopic] { return eventGridPartnerTopics }),
	"microsoft.eventgrid/partnerregistrations":  azureRowsOf(func() sim.Store[EventGridTopic] { return eventGridPartnerRegistrations }),
	"microsoft.eventgrid/partnernamespaces":     azureRowsOf(func() sim.Store[EventGridTopic] { return eventGridPartnerNamespaces }),
	"microsoft.eventgrid/partnerconfigurations": azureRowsOf(func() sim.Store[EventGridTopic] { return eventGridPartnerConfigurations }),

	// Data and observability
	"microsoft.documentdb/databaseaccounts":            azureRowsOf(func() sim.Store[CosmosAccount] { return cosmosAccounts }),
	"microsoft.dbforpostgresql/flexibleservers":        azureRowsOf(func() sim.Store[PGFlexibleServer] { return pgServers }),
	"microsoft.cache/redis":                            azureRowsOf(func() sim.Store[RedisCache] { return redisCaches }),
	"microsoft.operationalinsights/workspaces":         azureRowsOf(func() sim.Store[Workspace] { return azureMonitorWorkspaces }),
	"microsoft.insights/components":                    azureRowsOf(func() sim.Store[AppInsightsComponent] { return azureAppInsightsComponents }),
	"microsoft.managedidentity/userassignedidentities": azureRowsOf(func() sim.Store[UserAssignedIdentity] { return azureManagedIdentities }),

	// Integration
	"microsoft.apimanagement/service":                azureRowsOf(func() sim.Store[APIMService] { return apimServices }),
	"microsoft.logic/workflows":                      azureRowsOf(func() sim.Store[LogicWorkflow] { return logicWorkflows }),
	"microsoft.logic/integrationaccounts":            azureRowsOf(func() sim.Store[LogicResource] { return logicIntegrationAccts }),
	"microsoft.logic/integrationserviceenvironments": azureRowsOf(func() sim.Store[LogicResource] { return logicServiceEnvs }),
}

// azureRowsOf adapts one slice's store into an enumerator. The store is read
// through the accessor at request time so a package-level variable a register
// function assigns later — or reassigns when the simulator is rebuilt in the
// same process — is always the one enumerated.
func azureRowsOf[T any](store func() sim.Store[T]) azureResourceEnumerator {
	return func() []azureResourceRow {
		s := store()
		if s == nil {
			return nil
		}
		rows := make([]azureResourceRow, 0, s.Len())
		for _, item := range s.List() {
			rows = append(rows, azureProjectResource(item))
		}
		return rows
	}
}

// azureProjectResource renders one stored resource in the generic view. A row
// that cannot be read back through its own wire form is corrupt stored data,
// which fails loudly rather than being dropped from a list that claims to be
// complete.
func azureProjectResource(item any) azureResourceRow {
	data, err := json.Marshal(item)
	if err != nil {
		panic(fmt.Sprintf("azure resource registry: encoding %T: %v", item, err))
	}
	var stored azureStoredResource
	if err := json.Unmarshal(data, &stored); err != nil {
		panic(fmt.Sprintf("azure resource registry: decoding %T: %v", item, err))
	}
	// Scoping and every filter read the row's own identity. A stored resource
	// carrying neither would be silently absent from a list that reports
	// itself complete, so it is refused here instead.
	if stored.ID == "" || stored.Type == "" {
		panic(fmt.Sprintf("azure resource registry: %T carries no ARM id/type (id=%q type=%q)",
			item, stored.ID, stored.Type))
	}
	return azureResourceRow{
		ID:                stored.ID,
		Name:              stored.Name,
		Type:              stored.Type,
		Location:          stored.Location,
		Kind:              stored.Kind,
		ManagedBy:         stored.ManagedBy,
		Sku:               stored.Sku,
		Identity:          stored.Identity,
		Plan:              stored.Plan,
		Tags:              stored.Tags,
		ProvisioningState: stored.Properties.ProvisioningState,
	}
}

// azureSubscriptionOfID and azureResourceGroupOfID read the scope segments out
// of an ARM resource ID. ARM compares both case-insensitively, so both return
// the ID's own spelling and callers fold the comparison.
func azureSubscriptionOfID(id string) string { return azureIDSegmentAfter(id, "subscriptions") }

func azureResourceGroupOfID(id string) string { return azureIDSegmentAfter(id, "resourcegroups") }

func azureIDSegmentAfter(id, key string) string {
	segments := strings.Split(strings.TrimPrefix(id, "/"), "/")
	for i := 0; i+1 < len(segments); i++ {
		if strings.EqualFold(segments[i], key) {
			return segments[i+1]
		}
	}
	return ""
}

// azureEnumerateResources returns every registered resource in a subscription,
// narrowed to one resource group when resourceGroup is non-empty, ordered by
// resource ID so a paged walk is stable.
func azureEnumerateResources(sub, resourceGroup string) []azureResourceRow {
	rows := make([]azureResourceRow, 0)
	for _, enumerate := range azureTrackedResources {
		for _, row := range enumerate() {
			if !strings.EqualFold(azureSubscriptionOfID(row.ID), sub) {
				continue
			}
			if resourceGroup != "" && !strings.EqualFold(azureResourceGroupOfID(row.ID), resourceGroup) {
				continue
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

// --- $filter ---------------------------------------------------------------

// azureFilterTerm is one comparison of a Resources_List $filter.
type azureFilterTerm struct {
	property string // lowercased property name
	operator string // eq, ne, substringof, startswith
	value    string
}

// azureResourceFilter is a parsed $filter: a disjunction of conjunctions,
// which is the whole grammar the operation's documented forms need — `and`
// binds tighter than `or`, and ARM's resource filter has no parentheses.
type azureResourceFilter struct {
	disjuncts [][]azureFilterTerm
	// tagScoped records that a term selected on a tag name or value. ARM does
	// not return the resources' tags when they were filtered on.
	tagScoped bool
}

// azureFilterProperties are the members ARM compares with eq/ne.
var azureFilterProperties = map[string]bool{
	"name": true, "resourcegroup": true, "resourcetype": true, "location": true,
	"tagname": true, "tagvalue": true,
}

// azureSubstringProperties are the members substringof() reads. ARM supports
// substrings of name and resourceGroup only.
var azureSubstringProperties = map[string]bool{"name": true, "resourcegroup": true}

// parseAzureResourceFilter parses the $filter forms Resources_List documents
// and real clients send: the Azure CLI's `resourceGroup eq`, `name eq`,
// `location eq`, `resourceType eq`, `tagname eq`, `tagvalue eq` and
// `startswith(tagname, …)` conjunctions, and the `substringof(value, property)`
// form the operation documents for name and resourceGroup. A filter naming
// anything else is refused rather than quietly matching everything.
func parseAzureResourceFilter(raw string) (*azureResourceFilter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.ContainsAny(raw, "()") && !strings.Contains(strings.ToLower(raw), "substringof(") &&
		!strings.Contains(strings.ToLower(raw), "startswith(") {
		return nil, fmt.Errorf("grouping parentheses are not supported in %q", raw)
	}
	filter := &azureResourceFilter{}
	for _, disjunct := range azureSplitLogical(raw, "or") {
		terms := make([]azureFilterTerm, 0)
		for _, clause := range azureSplitLogical(disjunct, "and") {
			term, err := parseAzureFilterTerm(clause)
			if err != nil {
				return nil, err
			}
			if term.property == "tagname" || term.property == "tagvalue" {
				filter.tagScoped = true
			}
			terms = append(terms, term)
		}
		if len(terms) == 0 {
			return nil, fmt.Errorf("empty clause in %q", raw)
		}
		filter.disjuncts = append(filter.disjuncts, terms)
	}
	if len(filter.disjuncts) == 0 {
		return nil, fmt.Errorf("empty filter %q", raw)
	}
	return filter, nil
}

// azureSplitLogical splits an OData expression on a logical operator, ignoring
// occurrences inside a quoted literal.
func azureSplitLogical(expr, operator string) []string {
	parts := make([]string, 0, 2)
	needle := " " + operator + " "
	quoted := false
	start := 0
	for i := 0; i < len(expr); i++ {
		if expr[i] == '\'' {
			quoted = !quoted
			continue
		}
		if quoted || i+len(needle) > len(expr) {
			continue
		}
		if strings.EqualFold(expr[i:i+len(needle)], needle) {
			parts = append(parts, expr[start:i])
			start = i + len(needle)
			i += len(needle) - 1
		}
	}
	return append(parts, expr[start:])
}

func parseAzureFilterTerm(clause string) (azureFilterTerm, error) {
	clause = strings.TrimSpace(clause)
	lower := strings.ToLower(clause)

	switch {
	case strings.HasPrefix(lower, "substringof("):
		value, property, err := azureParseCall(clause, "substringof", true)
		if err != nil {
			return azureFilterTerm{}, err
		}
		if !azureSubstringProperties[property] {
			return azureFilterTerm{}, fmt.Errorf("substringof does not support property %q", property)
		}
		return azureFilterTerm{property: property, operator: "substringof", value: value}, nil

	case strings.HasPrefix(lower, "startswith("):
		property, value, err := azureParseCall(clause, "startswith", false)
		if err != nil {
			return azureFilterTerm{}, err
		}
		if property != "tagname" {
			return azureFilterTerm{}, fmt.Errorf("startswith does not support property %q", property)
		}
		return azureFilterTerm{property: property, operator: "startswith", value: value}, nil
	}

	for _, operator := range []string{"eq", "ne"} {
		needle := " " + operator + " "
		index := strings.Index(lower, needle)
		if index < 0 {
			continue
		}
		property := strings.ToLower(strings.TrimSpace(clause[:index]))
		literal, err := azureParseLiteral(clause[index+len(needle):])
		if err != nil {
			return azureFilterTerm{}, err
		}
		if !azureFilterProperties[property] {
			return azureFilterTerm{}, fmt.Errorf("property %q cannot be filtered on", property)
		}
		return azureFilterTerm{property: property, operator: operator, value: literal}, nil
	}
	return azureFilterTerm{}, fmt.Errorf("unrecognised clause %q", clause)
}

// azureParseCall reads a two-argument OData function call. literalFirst says
// which argument is the quoted literal: substringof(value, property) leads
// with it, startswith(property, value) trails it.
func azureParseCall(clause, name string, literalFirst bool) (string, string, error) {
	open := strings.Index(clause, "(")
	if open < 0 || !strings.HasSuffix(strings.TrimSpace(clause), ")") {
		return "", "", fmt.Errorf("malformed %s call %q", name, clause)
	}
	inner := strings.TrimSpace(clause)
	inner = inner[open+1 : len(inner)-1]
	first, second, found := strings.Cut(inner, ",")
	if !found {
		return "", "", fmt.Errorf("%s takes two arguments in %q", name, clause)
	}
	if literalFirst {
		literal, err := azureParseLiteral(first)
		if err != nil {
			return "", "", err
		}
		return literal, strings.ToLower(strings.TrimSpace(second)), nil
	}
	literal, err := azureParseLiteral(second)
	if err != nil {
		return "", "", err
	}
	return strings.ToLower(strings.TrimSpace(first)), literal, nil
}

func azureParseLiteral(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '\'' || raw[len(raw)-1] != '\'' {
		return "", fmt.Errorf("expected a quoted literal, got %q", raw)
	}
	// OData escapes a single quote by doubling it.
	return strings.ReplaceAll(raw[1:len(raw)-1], "''", "'"), nil
}

// matches reports whether a row satisfies the filter.
func (f *azureResourceFilter) matches(row azureResourceRow) bool {
	if f == nil {
		return true
	}
	for _, disjunct := range f.disjuncts {
		satisfied := true
		for _, term := range disjunct {
			if !term.matches(row) {
				satisfied = false
				break
			}
		}
		if satisfied {
			return true
		}
	}
	return false
}

func (t azureFilterTerm) matches(row azureResourceRow) bool {
	switch t.property {
	case "tagname":
		return t.matchesTagKey(row)
	case "tagvalue":
		return t.matchesTagValue(row)
	}

	var actual string
	switch t.property {
	case "name":
		actual = row.Name
	case "resourcegroup":
		actual = azureResourceGroupOfID(row.ID)
	case "resourcetype":
		actual = row.Type
	case "location":
		actual = row.Location
	}
	switch t.operator {
	case "eq":
		return strings.EqualFold(actual, t.value)
	case "ne":
		return !strings.EqualFold(actual, t.value)
	case "substringof":
		return strings.Contains(strings.ToLower(actual), strings.ToLower(t.value))
	}
	return false
}

func (t azureFilterTerm) matchesTagKey(row azureResourceRow) bool {
	for key := range row.Tags {
		switch t.operator {
		case "eq":
			if strings.EqualFold(key, t.value) {
				return true
			}
		case "startswith":
			if strings.HasPrefix(strings.ToLower(key), strings.ToLower(t.value)) {
				return true
			}
		case "ne":
			if strings.EqualFold(key, t.value) {
				return false
			}
		}
	}
	return t.operator == "ne"
}

func (t azureFilterTerm) matchesTagValue(row azureResourceRow) bool {
	for _, value := range row.Tags {
		switch t.operator {
		case "eq":
			if strings.EqualFold(value, t.value) {
				return true
			}
		case "ne":
			if strings.EqualFold(value, t.value) {
				return false
			}
		}
	}
	return t.operator == "ne"
}

// --- the list handlers -----------------------------------------------------

// azureExpandMembers are the additional members $expand can ask a resource
// list to carry. The simulator reports the provisioning state each resource
// records in its own properties document; it records no creation or change
// time for any resource, so those two members are absent exactly as they are
// for a resource ARM holds no such metadata for.
var azureExpandMembers = map[string]bool{
	"provisioningstate": true, "createdtime": true, "changedtime": true,
}

// handleAzureResourceList serves Resources_List and
// Resources_ListByResourceGroup. resourceGroup is empty for the
// subscription-wide spelling.
func handleAzureResourceList(w http.ResponseWriter, r *http.Request, sub, resourceGroup string) {
	query := r.URL.Query()

	filter, err := parseAzureResourceFilter(query.Get("$filter"))
	if err != nil {
		sim.AzureErrorf(w, "InvalidFilterInQueryString", http.StatusBadRequest,
			"The filter '%s' is invalid: %v.", query.Get("$filter"), err)
		return
	}

	expandProvisioningState := false
	for _, member := range strings.Split(query.Get("$expand"), ",") {
		member = strings.ToLower(strings.TrimSpace(member))
		if member == "" {
			continue
		}
		if !azureExpandMembers[member] {
			sim.AzureErrorf(w, "InvalidExpandQueryOptionValue", http.StatusBadRequest,
				"The $expand query option value '%s' is invalid.", member)
			return
		}
		if member == "provisioningstate" {
			expandProvisioningState = true
		}
	}

	rows := make([]azureResourceRow, 0)
	for _, row := range azureEnumerateResources(sub, resourceGroup) {
		if !filter.matches(row) {
			continue
		}
		if !expandProvisioningState {
			row.ProvisioningState = ""
		}
		if filter != nil && filter.tagScoped {
			// ARM does not return the resources' tags when the request
			// filtered on a tag name or value.
			row.Tags = nil
		}
		rows = append(rows, row)
	}

	page, next := armPage(r, rows)
	body := map[string]any{"value": page}
	if next != "" {
		body["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

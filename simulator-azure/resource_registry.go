package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
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

// azureTrackedResource is one resource type's participation in the registry:
// the enumeration a resource list reads, and the reader and writer of the one
// set of tags the resource holds. Azure Resource Manager keeps a single set of
// tags per resource, reachable both through the resource's own API and through
// Microsoft.Resources/tags/default at the resource's scope, so the tags surface
// resolves a scope through this table rather than keeping a plane of its own.
type azureTrackedResource struct {
	// enumerate returns the generic rows for every resource the slice holds.
	enumerate azureResourceEnumerator
	// lookupTags returns the resource's canonical ARM ID — the spelling the
	// slice stored, which is what ARM echoes back — and the tags it holds, or
	// false when no such resource exists. ARM compares resource IDs
	// case-insensitively and so does the lookup.
	lookupTags func(resID string) (id string, tags map[string]string, ok bool)
	// writeTags replaces the resource's tags, reporting whether the resource
	// exists.
	writeTags func(resID string, tags map[string]string) bool
}

// azureTrackedResources maps a tracked resource type — keyed by its lowercased
// "provider/type" spelling, the same key shape resourceMoveHooks uses — to the
// registry entry that reads and writes it in the slice that owns it.
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
var azureTrackedResources = map[string]azureTrackedResource{
	// Microsoft.Web
	"microsoft.web/sites":        azureTracked(func() sim.Store[Site] { return azfSites }),
	"microsoft.web/sites/slots":  azureTracked(func() sim.Store[Site] { return webSlots }),
	"microsoft.web/serverfarms":  azureTracked(func() sim.Store[AppServicePlan] { return azureAppServicePlans }),
	"microsoft.web/certificates": azureTracked(func() sim.Store[WebCertificate] { return webCertificates }),
	"microsoft.web/staticsites":  azureTracked(func() sim.Store[StaticSiteResource] { return webStaticSites }),
	"microsoft.web/hostingenvironments": azureTracked(func() sim.Store[AppServiceEnvironmentResource] {
		return webHostingEnvironments
	}),
	"microsoft.web/kubeenvironments": azureTracked(func() sim.Store[KubeEnvironmentResource] {
		return webKubeEnvironments
	}),

	// Microsoft.Storage
	"microsoft.storage/storageaccounts": azureTracked(func() sim.Store[StorageAccount] { return azStorageAccounts }),

	// Microsoft.KeyVault
	"microsoft.keyvault/vaults":      azureTracked(func() sim.Store[KeyVault] { return keyVaults }),
	"microsoft.keyvault/managedhsms": azureTracked(func() sim.Store[ManagedHSM] { return managedHSMs }),

	// Microsoft.Network
	"microsoft.network/virtualnetworks":                     azureTracked(func() sim.Store[VirtualNetwork] { return azureVnets }),
	"microsoft.network/networksecuritygroups":               azureTracked(func() sim.Store[NetworkSecurityGroup] { return azureNSGs }),
	"microsoft.network/natgateways":                         azureTracked(func() sim.Store[NatGateway] { return azureNatGateways }),
	"microsoft.network/routetables":                         azureTracked(func() sim.Store[RouteTable] { return azureRouteTables }),
	"microsoft.network/publicipaddresses":                   azureTracked(func() sim.Store[PublicIPAddress] { return azurePublicIPs }),
	"microsoft.network/publicipprefixes":                    azureTracked(func() sim.Store[PublicIPPrefix] { return azurePublicIPPrefixes }),
	"microsoft.network/loadbalancers":                       azureTracked(func() sim.Store[LoadBalancer] { return azureLBs }),
	"microsoft.network/networkinterfaces":                   azureTracked(func() sim.Store[NetworkInterface] { return azureNICs }),
	"microsoft.network/applicationsecuritygroups":           azureTracked(func() sim.Store[ApplicationSecurityGroup] { return azureASGs }),
	"microsoft.network/privatelinkservices":                 azureTracked(func() sim.Store[PrivateLinkService] { return azurePrivateLinkServices }),
	"microsoft.network/privateendpoints":                    azureTracked(func() sim.Store[PrivateEndpoint] { return azurePrivateEndpoints }),
	"microsoft.network/networkprofiles":                     azureTracked(func() sim.Store[NetworkProfile] { return azureNetworkProfiles }),
	"microsoft.network/virtualnetworktaps":                  azureTracked(func() sim.Store[VirtualNetworkTap] { return azureVirtualNetworkTaps }),
	"microsoft.network/serviceendpointpolicies":             azureTracked(func() sim.Store[ServiceEndpointPolicy] { return azureServiceEndpointPolicies }),
	"microsoft.network/applicationgateways":                 azureTracked(func() sim.Store[ApplicationGateway] { return azureApplicationGateways }),
	"microsoft.network/networkmanagers":                     azureTracked(func() sim.Store[NetworkManager] { return azureNetworkManagers }),
	"microsoft.network/networkwatchers":                     azureTracked(func() sim.Store[NetworkWatcher] { return azureNetworkWatchers }),
	"microsoft.network/dnszones":                            azureTracked(func() sim.Store[PublicDnsZone] { return azurePublicDNSZones }),
	"microsoft.network/privatednszones":                     azureTracked(func() sim.Store[PrivateDnsZone] { return azurePrivateDNSZones }),
	"microsoft.network/privatednszones/virtualnetworklinks": azureTracked(func() sim.Store[VNetLink] { return azurePrivateDNSVNetLinks }),

	// Microsoft.Compute
	"microsoft.compute/virtualmachines":            azureTracked(func() sim.Store[VirtualMachine] { return azureVMs }),
	"microsoft.compute/virtualmachines/extensions": azureTracked(func() sim.Store[VirtualMachineExtension] { return azureVMExtensions }),

	// Microsoft.App
	"microsoft.app/managedenvironments":                     azureTracked(func() sim.Store[ContainerAppEnvironment] { return acaEnvironments }),
	"microsoft.app/containerapps":                           azureTracked(func() sim.Store[ContainerApp] { return acaApps }),
	"microsoft.app/jobs":                                    azureTracked(func() sim.Store[ContainerAppJob] { return acaJobs }),
	"microsoft.app/managedenvironments/certificates":        azureTracked(func() sim.Store[Certificate] { return acaEnvCertificates }),
	"microsoft.app/managedenvironments/managedcertificates": azureTracked(func() sim.Store[ManagedCertificate] { return acaEnvManagedCertificates }),

	// Microsoft.ContainerInstance / Microsoft.ContainerRegistry
	"microsoft.containerinstance/containergroups": azureTracked(func() sim.Store[ACIContainerGroup] { return aciContainerGroups }),
	"microsoft.containerregistry/registries":      azureTracked(func() sim.Store[Registry] { return acrRegistries }),

	// Messaging
	"microsoft.servicebus/namespaces":           azureTracked(func() sim.Store[SBNamespace] { return sbNamespaces }),
	"microsoft.eventhub/namespaces":             azureTracked(func() sim.Store[EHNamespace] { return ehNamespaces }),
	"microsoft.eventgrid/topics":                azureTracked(func() sim.Store[EventGridTopic] { return eventGridTopics }),
	"microsoft.eventgrid/domains":               azureTracked(func() sim.Store[EventGridTopic] { return eventGridDomains }),
	"microsoft.eventgrid/systemtopics":          azureTracked(func() sim.Store[EventGridTopic] { return eventGridSystemTopics }),
	"microsoft.eventgrid/partnertopics":         azureTracked(func() sim.Store[EventGridTopic] { return eventGridPartnerTopics }),
	"microsoft.eventgrid/partnerregistrations":  azureTracked(func() sim.Store[EventGridTopic] { return eventGridPartnerRegistrations }),
	"microsoft.eventgrid/partnernamespaces":     azureTracked(func() sim.Store[EventGridTopic] { return eventGridPartnerNamespaces }),
	"microsoft.eventgrid/partnerconfigurations": azureTracked(func() sim.Store[EventGridTopic] { return eventGridPartnerConfigurations }),

	// Data and observability
	"microsoft.documentdb/databaseaccounts":            azureTracked(func() sim.Store[CosmosAccount] { return cosmosAccounts }),
	"microsoft.dbforpostgresql/flexibleservers":        azureTracked(func() sim.Store[PGFlexibleServer] { return pgServers }),
	"microsoft.cache/redis":                            azureTracked(func() sim.Store[RedisCache] { return redisCaches }),
	"microsoft.operationalinsights/workspaces":         azureTracked(func() sim.Store[Workspace] { return azureMonitorWorkspaces }),
	"microsoft.insights/components":                    azureTracked(func() sim.Store[AppInsightsComponent] { return azureAppInsightsComponents }),
	"microsoft.managedidentity/userassignedidentities": azureTracked(func() sim.Store[UserAssignedIdentity] { return azureManagedIdentities }),

	// Integration
	"microsoft.apimanagement/service":                azureTracked(func() sim.Store[APIMService] { return apimServices }),
	"microsoft.logic/workflows":                      azureTracked(func() sim.Store[LogicWorkflow] { return logicWorkflows }),
	"microsoft.logic/integrationaccounts":            azureTracked(func() sim.Store[LogicResource] { return logicIntegrationAccts }),
	"microsoft.logic/integrationserviceenvironments": azureTracked(func() sim.Store[LogicResource] { return logicServiceEnvs }),
}

// azureTracked adapts one slice's store into a registry entry. The store is
// read through the accessor at request time so a package-level variable a
// register function assigns later — or reassigns when the simulator is rebuilt
// in the same process — is always the one enumerated and written.
//
// The stored type is checked here, while the table initialises, for the ARM
// `tags` member every tracked resource carries: a type that cannot hold tags
// could be listed but never tagged through Microsoft.Resources/tags/default,
// so the registry refuses it outright rather than answering a write that
// silently lands nowhere.
func azureTracked[T any](store func() sim.Store[T]) azureTrackedResource {
	azureRequireTagsField(reflect.TypeOf((*T)(nil)).Elem())

	// find locates one resource by ARM ID and returns the store, a snapshot of
	// the row, and the key the store holds it under.
	find := func(resID string) (sim.Store[T], T, string, bool) {
		var zero T
		s := store()
		if s == nil {
			return nil, zero, "", false
		}
		if item, ok := s.Get(resID); ok {
			return s, item, resID, true
		}
		// ARM compares resource IDs case-insensitively, and each slice keys its
		// store by the ID spelling the creating request carried, so a lookup
		// that misses on the exact key falls back to the case-folded scan.
		for _, item := range s.List() {
			id := azureProjectResource(item).ID
			if !strings.EqualFold(id, resID) {
				continue
			}
			if _, ok := s.Get(id); !ok {
				panic(fmt.Sprintf("azure resource registry: %T is stored under a key other than its ARM id %q", item, id))
			}
			return s, item, id, true
		}
		return nil, zero, "", false
	}

	return azureTrackedResource{
		enumerate: func() []azureResourceRow {
			s := store()
			if s == nil {
				return nil
			}
			rows := make([]azureResourceRow, 0, s.Len())
			for _, item := range s.List() {
				rows = append(rows, azureProjectResource(item))
			}
			return rows
		},
		lookupTags: func(resID string) (string, map[string]string, bool) {
			_, item, _, ok := find(resID)
			if !ok {
				return "", nil, false
			}
			row := azureProjectResource(item)
			return row.ID, row.Tags, true
		},
		writeTags: func(resID string, tags map[string]string) bool {
			s, item, key, ok := find(resID)
			if !ok {
				return false
			}
			field := azureStoredTagsField(reflect.ValueOf(&item).Elem())
			if len(tags) == 0 {
				// A resource with no tags carries no `tags` member at all,
				// which is how it reads before anything tags it.
				field.Set(reflect.Zero(field.Type()))
			} else {
				field.Set(reflect.ValueOf(tags))
			}
			s.Put(key, item)
			return true
		},
	}
}

// azureTagsMapType is the Go shape of the ARM `tags` member: every tracked
// resource declares it as map[string]string.
var azureTagsMapType = reflect.TypeOf(map[string]string(nil))

// azureRequireTagsField panics unless the stored type declares a settable ARM
// `tags` member. It runs while azureTrackedResources initialises, so a type
// registered without one fails the process at start rather than at the first
// tag write.
func azureRequireTagsField(t reflect.Type) {
	if _, ok := azureTagsFieldIndex(t); !ok {
		panic(fmt.Sprintf("azure resource registry: %s declares no ARM `tags map[string]string` member", t))
	}
}

// azureTagsFieldIndex returns the field-index path to a stored type's ARM
// `tags` member, descending into the embedded envelope structs the
// Microsoft.Network resources marshal inline — the same fields encoding/json
// promotes.
func azureTagsFieldIndex(t reflect.Type) ([]int, bool) {
	if t.Kind() != reflect.Struct {
		return nil, false
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.IsExported() && strings.EqualFold(name, "tags") && field.Type == azureTagsMapType {
			return []int{i}, true
		}
		// An anonymous struct member with no json name of its own is inlined
		// by encoding/json — including the unexported envelope structs the
		// Microsoft.Network resources embed — so its `tags` member is the
		// resource's own. reflect keeps the promoted exported field settable.
		if field.Anonymous && name == "" && field.Type.Kind() == reflect.Struct {
			if nested, ok := azureTagsFieldIndex(field.Type); ok {
				return append([]int{i}, nested...), true
			}
		}
	}
	return nil, false
}

// azureStoredTagsField returns the addressable ARM `tags` member of a stored
// resource. azureRequireTagsField has already proven the member exists for
// every registered type, so a miss here is a corrupt value, not a missing
// field.
func azureStoredTagsField(item reflect.Value) reflect.Value {
	index, ok := azureTagsFieldIndex(item.Type())
	if !ok {
		panic(fmt.Sprintf("azure resource registry: %s lost its ARM tags member", item.Type()))
	}
	return item.FieldByIndex(index)
}

// azureResourceTypeKeyOfID reads the lowercased "provider/type" registry key
// out of an ARM resource ID, folding a nested type into the `parent/child`
// spelling the table uses (`microsoft.web/sites/slots`). It returns false for
// an ID that names no provider resource at all — a subscription or a resource
// group. A management group is a provider resource by ID shape
// (`microsoft.management/managementgroups`) and reads as one here; the tags
// surface routes it separately because the simulator keeps no record for one.
func azureResourceTypeKeyOfID(id string) (string, bool) {
	segments := strings.Split(strings.Trim(id, "/"), "/")
	providers := -1
	for i, segment := range segments {
		if strings.EqualFold(segment, "providers") {
			providers = i
		}
	}
	// providers/{namespace}/{type}/{name}, then /{childType}/{childName}…, so
	// the tail is a namespace followed by an even number of segments.
	if providers < 0 {
		return "", false
	}
	rest := segments[providers+1:]
	if len(rest) < 3 || len(rest)%2 == 0 {
		return "", false
	}
	key := rest[0]
	for i := 1; i < len(rest); i += 2 {
		key += "/" + rest[i]
	}
	return strings.ToLower(key), true
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
	for _, tracked := range azureTrackedResources {
		for _, row := range tracked.enumerate() {
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
		AzureErrorf(w, "InvalidFilterInQueryString", http.StatusBadRequest,
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
			AzureErrorf(w, "InvalidExpandQueryOptionValue", http.StatusBadRequest,
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

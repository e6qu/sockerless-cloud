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

// Cross-slice enumeration for Resources_List and
// Resources_ListByResourceGroup: each tracked type contributes its slice's
// store, so the Microsoft.Resources handlers stay provider-agnostic.
//
// Real API:
//
//	https://learn.microsoft.com/en-us/rest/api/resources/resources/list

// GenericResourceExpanded's identity, placement, classification and tags. A
// list never carries `properties`; the azurerm provider's Key Vault cache reads
// IDs here and each resource through its own provider.
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

	// An $expand-only member: a list row reports it at the top level, while the
	// resource records it inside its properties.
	ProvisioningState string `json:"provisioningState,omitempty"`
}

// Decoding the row's own JSON reads the ARM contract every tracked resource
// implements, so a slice needs no per-type projection.
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

type azureResourceEnumerator func() []azureResourceRow

// Azure Resource Manager keeps one set of tags per resource, reachable through
// its own API and through Microsoft.Resources/tags/default, so the tags surface
// resolves scopes through this table.
type azureTrackedResource struct {
	enumerate azureResourceEnumerator
	// lookupTags returns the stored ID spelling, which Azure Resource Manager
	// echoes, and the tags, matching IDs case-insensitively.
	lookupTags func(resID string) (id string, tags map[string]string, ok bool)
	writeTags  func(resID string, tags map[string]string) bool
}

// Keyed by lowercased "provider/type", as resourceMoveHooks is. Only tracked
// resources — those with a location, which appear in a resource list — belong
// here; proxy children such as subnets, queues or record sets do not. Each
// entry reads its package-level store at request time, after the register
// functions assigned it.
var azureTrackedResources = map[string]azureTrackedResource{
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

	"microsoft.storage/storageaccounts": azureTracked(func() sim.Store[StorageAccount] { return azStorageAccounts }),

	"microsoft.keyvault/vaults":      azureTracked(func() sim.Store[KeyVault] { return keyVaults }),
	"microsoft.keyvault/managedhsms": azureTracked(func() sim.Store[ManagedHSM] { return managedHSMs }),

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

	"microsoft.compute/virtualmachines":            azureTracked(func() sim.Store[VirtualMachine] { return azureVMs }),
	"microsoft.compute/virtualmachines/extensions": azureTracked(func() sim.Store[VirtualMachineExtension] { return azureVMExtensions }),

	"microsoft.app/managedenvironments":                     azureTracked(func() sim.Store[ContainerAppEnvironment] { return acaEnvironments }),
	"microsoft.app/containerapps":                           azureTracked(func() sim.Store[ContainerApp] { return acaApps }),
	"microsoft.app/jobs":                                    azureTracked(func() sim.Store[ContainerAppJob] { return acaJobs }),
	"microsoft.app/managedenvironments/certificates":        azureTracked(func() sim.Store[Certificate] { return acaEnvCertificates }),
	"microsoft.app/managedenvironments/managedcertificates": azureTracked(func() sim.Store[ManagedCertificate] { return acaEnvManagedCertificates }),

	"microsoft.containerinstance/containergroups": azureTracked(func() sim.Store[ACIContainerGroup] { return aciContainerGroups }),
	"microsoft.containerregistry/registries":      azureTracked(func() sim.Store[Registry] { return acrRegistries }),

	"microsoft.servicebus/namespaces":           azureTracked(func() sim.Store[SBNamespace] { return sbNamespaces }),
	"microsoft.eventhub/namespaces":             azureTracked(func() sim.Store[EHNamespace] { return ehNamespaces }),
	"microsoft.eventgrid/topics":                azureTracked(func() sim.Store[EventGridTopic] { return eventGridTopics }),
	"microsoft.eventgrid/domains":               azureTracked(func() sim.Store[EventGridTopic] { return eventGridDomains }),
	"microsoft.eventgrid/systemtopics":          azureTracked(func() sim.Store[EventGridTopic] { return eventGridSystemTopics }),
	"microsoft.eventgrid/partnertopics":         azureTracked(func() sim.Store[EventGridTopic] { return eventGridPartnerTopics }),
	"microsoft.eventgrid/partnerregistrations":  azureTracked(func() sim.Store[EventGridTopic] { return eventGridPartnerRegistrations }),
	"microsoft.eventgrid/partnernamespaces":     azureTracked(func() sim.Store[EventGridTopic] { return eventGridPartnerNamespaces }),
	"microsoft.eventgrid/partnerconfigurations": azureTracked(func() sim.Store[EventGridTopic] { return eventGridPartnerConfigurations }),

	"microsoft.documentdb/databaseaccounts":            azureTracked(func() sim.Store[CosmosAccount] { return cosmosAccounts }),
	"microsoft.dbforpostgresql/flexibleservers":        azureTracked(func() sim.Store[PGFlexibleServer] { return pgServers }),
	"microsoft.cache/redis":                            azureTracked(func() sim.Store[RedisCache] { return redisCaches }),
	"microsoft.operationalinsights/workspaces":         azureTracked(func() sim.Store[Workspace] { return azureMonitorWorkspaces }),
	"microsoft.insights/components":                    azureTracked(func() sim.Store[AppInsightsComponent] { return azureAppInsightsComponents }),
	"microsoft.managedidentity/userassignedidentities": azureTracked(func() sim.Store[UserAssignedIdentity] { return azureManagedIdentities }),

	"microsoft.apimanagement/service":                azureTracked(func() sim.Store[APIMService] { return apimServices }),
	"microsoft.logic/workflows":                      azureTracked(func() sim.Store[LogicWorkflow] { return logicWorkflows }),
	"microsoft.logic/integrationaccounts":            azureTracked(func() sim.Store[LogicResource] { return logicIntegrationAccts }),
	"microsoft.logic/integrationserviceenvironments": azureTracked(func() sim.Store[LogicResource] { return logicServiceEnvs }),
}

// The accessor reads the store at request time, so a rebuild's reassignment
// is the one used. A type without a settable `tags` member is refused here,
// while the table initializes, rather than accepting tag writes that land
// nowhere.
func azureTracked[T any](store func() sim.Store[T]) azureTrackedResource {
	azureRequireTagsField(reflect.TypeOf((*T)(nil)).Elem())

	find := func(resID string) (sim.Store[T], T, string, bool) {
		var zero T
		s := store()
		if s == nil {
			return nil, zero, "", false
		}
		if item, ok := s.Get(resID); ok {
			return s, item, resID, true
		}
		// Each slice keys by the creating request's ID spelling, so a miss on the exact
		// key falls back to a case-folded scan.
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
				// A resource with no tags carries no `tags` member at all.
				field.Set(reflect.Zero(field.Type()))
			} else {
				field.Set(reflect.ValueOf(tags))
			}
			s.Put(key, item)
			return true
		},
	}
}

var azureTagsMapType = reflect.TypeOf(map[string]string(nil))

// Runs while azureTrackedResources initializes, so a bad type fails the process
// at start.
func azureRequireTagsField(t reflect.Type) {
	if _, ok := azureTagsFieldIndex(t); !ok {
		panic(fmt.Sprintf("azure resource registry: %s declares no ARM `tags map[string]string` member", t))
	}
}

// Descends into the embedded envelope structs encoding/json promotes.
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
		// encoding/json inlines an anonymous struct member with no json name, the
		// unexported Microsoft.Network envelopes included; reflect keeps the promoted
		// exported field settable.
		if field.Anonymous && name == "" && field.Type.Kind() == reflect.Struct {
			if nested, ok := azureTagsFieldIndex(field.Type); ok {
				return append([]int{i}, nested...), true
			}
		}
	}
	return nil, false
}

// azureRequireTagsField proved the member exists, so a miss is a corrupt value.
func azureStoredTagsField(item reflect.Value) reflect.Value {
	index, ok := azureTagsFieldIndex(item.Type())
	if !ok {
		panic(fmt.Sprintf("azure resource registry: %s lost its ARM tags member", item.Type()))
	}
	return item.FieldByIndex(index)
}

// A nested type folds into `parent/child` (`microsoft.web/sites/slots`). False
// for a subscription or resource group. A management group reads as a
// provider resource by shape; the tags surface routes it separately.
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

// A row unreadable through its own wire form is corrupt and fails loudly
// rather than vanishing from a list that claims completeness.
func azureProjectResource(item any) azureResourceRow {
	data, err := json.Marshal(item)
	if err != nil {
		panic(fmt.Sprintf("azure resource registry: encoding %T: %v", item, err))
	}
	var stored azureStoredResource
	if err := json.Unmarshal(data, &stored); err != nil {
		panic(fmt.Sprintf("azure resource registry: decoding %T: %v", item, err))
	}
	// Scoping and every filter read the row's identity, so refuse a row without
	// one rather than silently omit it.
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

// Both return the ID's spelling; callers fold the comparison.
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

// Ordered by resource ID so a paged walk is stable.
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

type azureFilterTerm struct {
	property string // lowercased property name
	operator string // eq, ne, substringof, startswith
	value    string
}

// A disjunction of conjunctions: `and` binds tighter than `or`, and the
// resource filter has no parentheses.
type azureResourceFilter struct {
	disjuncts [][]azureFilterTerm
	// Azure Resource Manager omits tags from resources filtered on a tag.
	tagScoped bool
}

var azureFilterProperties = map[string]bool{
	"name": true, "resourcegroup": true, "resourcetype": true, "location": true,
	"tagname": true, "tagvalue": true,
}

// Azure supports substringof() on name and resourceGroup only.
var azureSubstringProperties = map[string]bool{"name": true, "resourcegroup": true}

// The Azure CLI's `resourceGroup`, `name`, `location`, `resourceType`,
// `tagname` and `tagvalue` eq conjunctions and `startswith(tagname, …)`, and the
// documented `substringof(value, property)`. Anything else is refused rather
// than matching everything.
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

// Ignores occurrences inside a quoted literal.
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

// substringof(value, property) leads with the literal, startswith(property,
// value) trails with it.
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

// The simulator records no creation or change times, so only the provisioning
// state is reported.
var azureExpandMembers = map[string]bool{
	"provisioningstate": true, "createdtime": true, "changedtime": true,
}

// resourceGroup is empty for the subscription-wide spelling.
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
			// Azure Resource Manager omits tags when the request filtered on a tag.
			row.Tags = nil
		}
		rows = append(rows, row)
	}

	page, next, pageOK := armPage(w, r, rows, func(row azureResourceRow) string { return row.ID })
	if !pageOK {
		return
	}
	body := map[string]any{"value": page}
	if next != "" {
		body["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

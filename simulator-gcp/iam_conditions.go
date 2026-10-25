package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

// gcpIAMResource is the resource a permission is checked on, as a binding's
// condition sees it through the resource.name, resource.type and
// resource.service attributes. IAM supplies the three only for the resource
// types the IAM Conditions resource attribute reference lists
// (specs/cloud-api/gcp/iam-conditions-resource-attributes.md.gz); for any
// other resource they are absent, and a condition that reads them cannot be
// satisfied.
type gcpIAMResource struct {
	Name    string
	Type    string
	Service string
	// project is the project the resource lives in, from which it inherits
	// tags; "" for a resource outside any project.
	project string
}

// gcpIAMResourceTypes are the resource types the reference lists whose IAM
// policies the simulator holds, by the resource name format the reference
// gives each. The pattern is matched against the policy store key once its
// collection prefix is removed.
var gcpIAMResourceTypes = []struct {
	pattern *regexp.Regexp
	service string
	kind    string
}{
	{regexp.MustCompile(`^projects/[^/]+/datasets/[^/]+$`), "bigquery.googleapis.com", "bigquery.googleapis.com/Dataset"},
	{regexp.MustCompile(`^projects/[^/]+/datasets/[^/]+/models/[^/]+$`), "bigquery.googleapis.com", "bigquery.googleapis.com/Model"},
	{regexp.MustCompile(`^projects/[^/]+/datasets/[^/]+/routines/[^/]+$`), "bigquery.googleapis.com", "bigquery.googleapis.com/Routine"},
	{regexp.MustCompile(`^projects/[^/]+/datasets/[^/]+/tables/[^/]+$`), "bigquery.googleapis.com", "bigquery.googleapis.com/Table"},
	{regexp.MustCompile(`^projects/[^/]+/instances/[^/]+/appProfiles/[^/]+$`), "bigtableadmin.googleapis.com", "bigtableadmin.googleapis.com/AppProfile"},
	{regexp.MustCompile(`^projects/[^/]+/instances/[^/]+/clusters/[^/]+/backups/[^/]+$`), "bigtableadmin.googleapis.com", "bigtableadmin.googleapis.com/Backup"},
	{regexp.MustCompile(`^projects/[^/]+/instances/[^/]+/clusters/[^/]+$`), "bigtableadmin.googleapis.com", "bigtableadmin.googleapis.com/Cluster"},
	{regexp.MustCompile(`^projects/[^/]+/instances/[^/]+/tables/[^/]+$`), "bigtableadmin.googleapis.com", "bigtableadmin.googleapis.com/Table"},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+$`), "cloudkms.googleapis.com", "cloudkms.googleapis.com/KeyRing"},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+$`), "cloudkms.googleapis.com", "cloudkms.googleapis.com/CryptoKey"},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+/cryptoKeyVersions/[^/]+$`), "cloudkms.googleapis.com", "cloudkms.googleapis.com/CryptoKeyVersion"},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/queues/[^/]+$`), "cloudtasks.googleapis.com", "cloudtasks.googleapis.com/Queue"},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/buckets/[^/]+$`), "logging.googleapis.com", "logging.googleapis.com/LogBucket"},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/buckets/[^/]+/views/[^/]+$`), "logging.googleapis.com", "logging.googleapis.com/LogView"},
	{regexp.MustCompile(`^projects/[^/]+/secrets/[^/]+$`), "secretmanager.googleapis.com", "secretmanager.googleapis.com/Secret"},
	{regexp.MustCompile(`^projects/[^/]+/secrets/[^/]+/versions/[^/]+$`), "secretmanager.googleapis.com", "secretmanager.googleapis.com/SecretVersion"},
	{regexp.MustCompile(`^projects/[^/]+/instances/[^/]+/backups/[^/]+$`), "spanner.googleapis.com", "spanner.googleapis.com/Backup"},
	{regexp.MustCompile(`^projects/[^/]+/instances/[^/]+/databases/[^/]+$`), "spanner.googleapis.com", "spanner.googleapis.com/Database"},
	{regexp.MustCompile(`^projects/[^/]+/databases/[^/]+$`), "firestore.googleapis.com", "firestore.googleapis.com/Database"},
	{regexp.MustCompile(`^projects/[^/]+/(global|regions/[^/]+)/backendServices/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/BackendService"},
	{regexp.MustCompile(`^projects/[^/]+/(zones|regions)/[^/]+/disks/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/Disk"},
	{regexp.MustCompile(`^projects/[^/]+/global/firewalls/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/Firewall"},
	{regexp.MustCompile(`^projects/[^/]+/regions/[^/]+/forwardingRules/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/ForwardingRule"},
	{regexp.MustCompile(`^projects/[^/]+/global/forwardingRules/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/GlobalForwardingRule"},
	{regexp.MustCompile(`^projects/[^/]+/global/images/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/Image"},
	{regexp.MustCompile(`^projects/[^/]+/zones/[^/]+/instances/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/Instance"},
	{regexp.MustCompile(`^projects/[^/]+/global/instanceTemplates/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/InstanceTemplate"},
	{regexp.MustCompile(`^projects/[^/]+/global/snapshots/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/Snapshot"},
	{regexp.MustCompile(`^projects/[^/]+/(global|regions/[^/]+)/targetHttpProxies/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/TargetHttpProxy"},
	{regexp.MustCompile(`^projects/[^/]+/(global|regions/[^/]+)/targetHttpsProxies/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/TargetHttpsProxy"},
	{regexp.MustCompile(`^projects/[^/]+/global/targetSslProxies/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/TargetSslProxy"},
	{regexp.MustCompile(`^projects/[^/]+/global/targetTcpProxies/[^/]+$`), "compute.googleapis.com", "compute.googleapis.com/TargetTcpProxy"},
}

// gcpIAMInstancePattern is the name format Bigtable and Spanner instances
// share; the store that holds the instance tells them apart.
var gcpIAMInstancePattern = regexp.MustCompile(`^projects/[^/]+/instances/[^/]+$`)

// gcpIAMResourceNamed is the resource a policy store key denotes. The shared
// store keys most resources by their relative resource name; Compute Engine,
// BigQuery, projects, organizations, service accounts and managed zones key
// theirs under a collection prefix, which is removed here.
func gcpIAMResourceNamed(key string) gcpIAMResource {
	switch {
	case strings.HasPrefix(key, "compute/"):
		return gcpIAMResourceOf(strings.TrimPrefix(key, "compute/"))
	case strings.HasPrefix(key, "bigquery:"):
		return gcpIAMResourceOf(strings.TrimPrefix(key, "bigquery:"))
	case strings.HasPrefix(key, "project/"):
		// The reference lists the Project type and the Resource Manager
		// service without a project name format.
		return gcpIAMResource{Type: "cloudresourcemanager.googleapis.com/Project", Service: "cloudresourcemanager.googleapis.com", project: strings.TrimPrefix(key, "project/")}
	case strings.HasPrefix(key, "organization/"):
		return gcpIAMResource{Name: "organizations/" + strings.TrimPrefix(key, "organization/"), Service: "cloudresourcemanager.googleapis.com"}
	case strings.HasPrefix(key, "serviceAccount/"):
		return gcpIAMResource{project: gcpProjectFromEmail(strings.TrimPrefix(key, "serviceAccount/"))}
	case strings.HasPrefix(key, "dnsManagedZone/"):
		project, _, _ := strings.Cut(strings.TrimPrefix(key, "dnsManagedZone/"), "/")
		return gcpIAMResource{project: project}
	}
	return gcpIAMResourceOf(key)
}

func gcpIAMResourceOf(name string) gcpIAMResource {
	resource := gcpIAMResource{}
	if project := resourceProject(name); project != "_" {
		resource.project = project
	}
	for _, known := range gcpIAMResourceTypes {
		if known.pattern.MatchString(name) {
			resource.Name, resource.Type, resource.Service = name, known.kind, known.service
			break
		}
	}
	if gcpIAMInstancePattern.MatchString(name) {
		if spannerInstances != nil {
			if _, ok := spannerInstances.Get(name); ok {
				resource.Name, resource.Type, resource.Service = name, "spanner.googleapis.com/Instance", "spanner.googleapis.com"
			}
		}
		if bigtableInstances != nil && resource.Type == "" {
			if _, ok := bigtableInstances.Get(name); ok {
				resource.Name, resource.Type, resource.Service = name, "bigtableadmin.googleapis.com/Instance", "bigtableadmin.googleapis.com"
			}
		}
	}
	// Secret Manager names its secrets by project number in the attribute.
	if resource.Service == "secretmanager.googleapis.com" && crmProjects != nil {
		if number, ok := crmProjectNumber(resource.project); ok {
			resource.Name = "projects/" + number + strings.TrimPrefix(resource.Name, "projects/"+resource.project)
		}
	}
	return resource
}

// fullName is the resource's full resource name, which tag bindings name.
func (r gcpIAMResource) fullName() string {
	if r.Service != "" && r.Name != "" {
		return "//" + r.Service + "/" + r.Name
	}
	if r.project != "" {
		return "//cloudresourcemanager.googleapis.com/projects/" + r.project
	}
	return ""
}

// gcpIAMCondition is a binding's condition: a Common Expression Language
// expression over the request and resource attributes.
type gcpIAMCondition struct {
	Expression string `json:"expression"`
}

// gcpConditionTag is one tag in force on the resource, by the identifiers the
// tag functions look keys and values up by: the permanent IDs, the key's
// namespaced name and the value's short name.
type gcpConditionTag struct {
	keyID, keyNamespaced, valueID, valueShort string
}

// gcpConditionResource is the resource variable of a condition: the attribute
// map a condition reads, carrying the tags its tag functions evaluate.
type gcpConditionResource struct {
	traits.Mapper
	tags func() []gcpConditionTag
}

func gcpConditionResourceValue(resource gcpIAMResource) *gcpConditionResource {
	attributes := map[string]any{}
	for attribute, value := range map[string]string{
		"name":    resource.Name,
		"type":    resource.Type,
		"service": resource.Service,
	} {
		if value != "" {
			attributes[attribute] = value
		}
	}
	return &gcpConditionResource{
		Mapper: types.NewStringInterfaceMap(types.DefaultTypeAdapter, attributes),
		tags: sync.OnceValue(func() []gcpConditionTag {
			var tags []gcpConditionTag
			for _, effective := range crmEffectiveTags(resource.fullName()) {
				tag := gcpConditionTag{keyID: effective.TagKey, valueID: effective.TagValue}
				if key, ok := crmTagKeys.Get(effective.TagKey); ok {
					tag.keyNamespaced = key.NamespacedName
				}
				if value, ok := crmTagValues.Get(effective.TagValue); ok {
					tag.valueShort = value.ShortName
				}
				tags = append(tags, tag)
			}
			return tags
		}),
	}
}

var gcpConditionResourceType = cel.MapType(cel.StringType, cel.DynType)

// gcpConditionTagFunction declares one of the resource tag functions the IAM
// Conditions attribute reference offers, evaluated over the tags in force on
// the resource of the request.
func gcpConditionTagFunction(name string, arity int, holds func(tag gcpConditionTag, args []string) bool) cel.EnvOption {
	argTypes := []*cel.Type{gcpConditionResourceType}
	for range arity {
		argTypes = append(argTypes, cel.StringType)
	}
	return cel.Function(name, cel.MemberOverload("resource_"+name, argTypes, cel.BoolType,
		cel.FunctionBinding(func(values ...ref.Val) ref.Val {
			resource, ok := values[0].(*gcpConditionResource)
			if !ok {
				return types.NewErr("%s is a function of the resource", name)
			}
			args := make([]string, 0, arity)
			for _, value := range values[1:] {
				arg, ok := value.Value().(string)
				if !ok {
					return types.NewErr("%s takes string arguments", name)
				}
				args = append(args, arg)
			}
			for _, tag := range resource.tags() {
				if holds(tag, args) {
					return types.True
				}
			}
			return types.False
		})))
}

var gcpConditionEnv = sync.OnceValues(func() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("request", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("resource", gcpConditionResourceType),
		gcpConditionTagFunction("matchTag", 2, func(tag gcpConditionTag, args []string) bool {
			return tag.keyNamespaced != "" && tag.keyNamespaced == args[0] && tag.valueShort == args[1]
		}),
		gcpConditionTagFunction("matchTagId", 2, func(tag gcpConditionTag, args []string) bool {
			return tag.keyID != "" && tag.keyID == args[0] && tag.valueID == args[1]
		}),
		gcpConditionTagFunction("hasTagKey", 1, func(tag gcpConditionTag, args []string) bool {
			return tag.keyNamespaced != "" && tag.keyNamespaced == args[0]
		}),
		gcpConditionTagFunction("hasTagKeyId", 1, func(tag gcpConditionTag, args []string) bool {
			return tag.keyID != "" && tag.keyID == args[0]
		}),
	)
})

var gcpConditionPrograms sync.Map

func gcpConditionProgram(expression string) (cel.Program, error) {
	if cached, ok := gcpConditionPrograms.Load(expression); ok {
		if program, ok := cached.(cel.Program); ok {
			return program, nil
		}
	}
	env, err := gcpConditionEnv()
	if err != nil {
		return nil, err
	}
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	program, err := env.Program(ast)
	if err != nil {
		return nil, err
	}
	gcpConditionPrograms.Store(expression, program)
	return program, nil
}

// gcpConditionHolds reports whether a binding's condition admits a request for
// the resource at the given time. A binding without a condition always
// applies. IAM treats a condition that fails to evaluate — an attribute the
// resource does not supply, a function it does not offer — as false, so the
// binding grants nothing.
func gcpConditionHolds(raw json.RawMessage, resource gcpIAMResource, at time.Time) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var condition gcpIAMCondition
	if err := json.Unmarshal(raw, &condition); err != nil || condition.Expression == "" {
		return false
	}
	program, err := gcpConditionProgram(condition.Expression)
	if err != nil {
		return false
	}
	out, _, err := program.Eval(map[string]any{
		"request":  map[string]any{"time": at},
		"resource": gcpConditionResourceValue(resource),
	})
	if err != nil {
		return false
	}
	holds, ok := out.Value().(bool)
	return ok && holds
}

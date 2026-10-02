package main

import (
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"cel.dev/cel-go/cel"
)

// gcpIAMResource is the resource a permission is checked on, as a binding's
// condition sees it through the resource.name, resource.type and
// resource.service attributes. An empty field is an attribute IAM does not
// supply for the resource, which a condition that reads it cannot satisfy.
type gcpIAMResource struct {
	Name    string
	Type    string
	Service string
}

// gcpIAMResourceTypes names the resource type and service of the collections
// whose IAM policies live in the shared resource store under their canonical
// relative names.
var gcpIAMResourceTypes = []struct {
	pattern *regexp.Regexp
	service string
	kind    string
}{
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/services/[^/]+$`), "run.googleapis.com", "run.googleapis.com/Service"},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/jobs/[^/]+$`), "run.googleapis.com", "run.googleapis.com/Job"},
	{regexp.MustCompile(`^projects/[^/]+/secrets/[^/]+$`), "secretmanager.googleapis.com", "secretmanager.googleapis.com/Secret"},
	{regexp.MustCompile(`^projects/[^/]+/topics/[^/]+$`), "pubsub.googleapis.com", "pubsub.googleapis.com/Topic"},
	{regexp.MustCompile(`^projects/[^/]+/subscriptions/[^/]+$`), "pubsub.googleapis.com", "pubsub.googleapis.com/Subscription"},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+$`), "cloudkms.googleapis.com", "cloudkms.googleapis.com/KeyRing"},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+$`), "cloudkms.googleapis.com", "cloudkms.googleapis.com/CryptoKey"},
}

// gcpIAMResourceNamed is the resource a relative resource name denotes.
func gcpIAMResourceNamed(name string) gcpIAMResource {
	for _, known := range gcpIAMResourceTypes {
		if known.pattern.MatchString(name) {
			return gcpIAMResource{Name: name, Type: known.kind, Service: known.service}
		}
	}
	return gcpIAMResource{Name: name}
}

// gcpIAMCondition is a binding's condition: a Common Expression Language
// expression over the request and resource attributes.
type gcpIAMCondition struct {
	Expression string `json:"expression"`
}

var gcpConditionEnv = sync.OnceValues(func() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("request", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("resource", cel.MapType(cel.StringType, cel.DynType)),
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
	resourceAttributes := map[string]any{}
	for attribute, value := range map[string]string{
		"name":    resource.Name,
		"type":    resource.Type,
		"service": resource.Service,
	} {
		if value != "" {
			resourceAttributes[attribute] = value
		}
	}
	out, _, err := program.Eval(map[string]any{
		"request":  map[string]any{"time": at},
		"resource": resourceAttributes,
	})
	if err != nil {
		return false
	}
	holds, ok := out.Value().(bool)
	return ok && holds
}

package main

import (
	"net/http"
	"strings"
)

// iamRequestConditionPopulator adds the condition keys one service's request
// settles. operation is the action's name without its service prefix.
type iamRequestConditionPopulator func(r *http.Request, operation string, body []byte, ctx map[string][]string)

// iamRequestConditionPopulators holds, per service prefix, the populators a
// file registers from its init function, so each service's keys live beside
// their own tests rather than in one dispatch.
var iamRequestConditionPopulators = map[string][]iamRequestConditionPopulator{}

func registerIAMRequestConditionPopulator(service string, populate iamRequestConditionPopulator) {
	iamRequestConditionPopulators[service] = append(iamRequestConditionPopulators[service], populate)
}

func iamRunRequestConditionPopulators(r *http.Request, service, operation string, body []byte, ctx map[string][]string) {
	for _, populate := range iamRequestConditionPopulators[service] {
		populate(r, operation, body, ctx)
	}
}

// iamRequestWireOperation is the API operation the request makes, which is not
// always the action it is authorized as: a request that tags a resource as it
// creates it is additionally authorized as the service's tagging action, while
// the request itself is the create. A query-protocol service names the
// operation in its Action parameter, an awsJson one in X-Amz-Target.
func iamRequestWireOperation(r *http.Request) string {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		if i := strings.LastIndex(target, "."); i >= 0 {
			return target[i+1:]
		}
		return ""
	}
	return r.FormValue("Action")
}

// iamTagOnCreateOperation is the create operation behind a <service>:CreateAction
// key, or "" when the key does not apply.
//
// AWS performs an additional authorization on a service's tagging action when a
// resource-creating request carries tags ("If tags are specified in the
// resource-creating action, Amazon performs additional authorization on the
// ec2:CreateTags action"), and sets <service>:CreateAction to the name of the
// creating operation — the key the tag-on-create grant is written against. The
// key exists only on that tagging action, which is why the action being
// authorized decides whether it applies at all, and it is absent when the
// caller tags an existing resource with a plain tagging call: such a request is
// making no create, so no create names it.
func iamTagOnCreateOperation(r *http.Request, action, tagAction string, tagged bool) string {
	if action != tagAction || !tagged {
		return ""
	}
	operation := iamRequestWireOperation(r)
	if operation == tagAction {
		return ""
	}
	return operation
}

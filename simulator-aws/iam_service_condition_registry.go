package main

import "net/http"

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

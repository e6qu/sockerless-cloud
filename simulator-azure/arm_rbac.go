package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// armOperation is the Azure RBAC action an Azure Resource Manager request
// performs and the scope ARM checks it at.
type armOperation struct {
	action string
	scope  string
	// collection marks a GET on a collection, which ARM answers with the
	// members the caller may read when it may not read the whole collection.
	collection bool
}

type armAuthMode int

const (
	// armAuthRBAC checks the operation's action against the caller's role
	// assignments.
	armAuthRBAC armAuthMode = iota
	// armAuthAuthenticated admits any authenticated caller: tenant-level
	// provider operations and operation-status polls.
	armAuthAuthenticated
	// armAuthSubscriptionMember admits a caller holding any role assignment
	// in, above or beneath the subscription: reading the subscription itself,
	// its locations and its resource providers.
	armAuthSubscriptionMember
	// armAuthSubscriptionList answers the subscriptions the caller holds any
	// role assignment in.
	armAuthSubscriptionList
)

func armPathSegments(path string) []string {
	var segs []string
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// armOperationOf derives the action of a request the way Azure Resource
// Manager names it: the namespace after the last /providers/ segment, the
// resource types along the path (every other segment), and read, write or
// delete for the verb — or, for a POST, the trailing segment followed by
// /action. A path without a provider namespace is Microsoft.Resources'.
func armOperationOf(method, path string) armOperation {
	segs := armPathSegments(path)
	idx := -1
	for i := len(segs) - 2; i >= 0; i-- {
		if strings.EqualFold(segs[i], "providers") {
			idx = i
			break
		}
	}
	namespace, tail, prefix := "Microsoft.Resources", segs, []string(nil)
	if idx >= 0 {
		namespace, tail, prefix = segs[idx+1], segs[idx+2:], segs[:idx]
	}
	post := method == http.MethodPost && len(tail) > 0
	typed := tail
	if post {
		typed = tail[:len(tail)-1]
	}
	types := []string{namespace}
	for i := 0; i < len(typed); i += 2 {
		types = append(types, typed[i])
	}
	odd := len(tail)%2 == 1
	var verb string
	switch method {
	case http.MethodPut, http.MethodPatch:
		verb = "write"
	case http.MethodDelete:
		verb = "delete"
	case http.MethodPost:
		verb = "action"
		if post {
			types = append(types, tail[len(tail)-1])
		}
	default:
		verb = "read"
	}
	op := armOperation{action: strings.Join(types, "/") + "/" + verb}
	scopeSegs := segs
	switch {
	case (odd || post) && len(tail) == 1 && idx >= 0:
		scopeSegs = prefix
	case odd || post:
		scopeSegs = segs[:len(segs)-1]
	case len(tail) == 0 && idx >= 0:
		scopeSegs = prefix
	}
	op.scope = "/" + strings.Join(scopeSegs, "/")
	op.collection = odd && verb == "read"
	return op
}

// armAuthorizationOf classifies how ARM authorizes a request.
func armAuthorizationOf(method, path string) armAuthMode {
	segs := armPathSegments(path)
	if len(segs) == 0 {
		return armAuthAuthenticated
	}
	read := method == http.MethodGet || method == http.MethodHead
	switch strings.ToLower(segs[0]) {
	case "tenants":
		return armAuthAuthenticated
	case "providers":
		if len(segs) >= 2 && strings.EqualFold(segs[1], "Microsoft.Authorization") {
			return armAuthRBAC
		}
		if len(segs) >= 3 && strings.EqualFold(segs[1], "Microsoft.Management") && strings.EqualFold(segs[2], "managementGroups") {
			return armAuthRBAC
		}
		return armAuthAuthenticated
	case "subscriptions":
	default:
		return armAuthRBAC
	}
	if len(segs) == 1 {
		if read {
			return armAuthSubscriptionList
		}
		return armAuthRBAC
	}
	if !read {
		return armAuthRBAC
	}
	switch {
	case len(segs) == 2:
		return armAuthSubscriptionMember
	case len(segs) == 3 && (strings.EqualFold(segs[2], "locations") || strings.EqualFold(segs[2], "providers")):
		return armAuthSubscriptionMember
	case len(segs) == 4 && strings.EqualFold(segs[2], "providers"):
		return armAuthSubscriptionMember
	case len(segs) == 4 && strings.EqualFold(segs[2], "operationresults"):
		return armAuthAuthenticated
	}
	if len(segs) > 6 && strings.EqualFold(segs[2], "providers") && strings.EqualFold(segs[4], "locations") {
		for _, s := range segs[6:] {
			if strings.Contains(strings.ToLower(s), "operation") {
				return armAuthAuthenticated
			}
		}
	}
	return armAuthRBAC
}

// armAuthorizeAndServe authorizes an authenticated ARM request and serves it,
// writing ARM's refusal instead when Azure RBAC does not grant it.
func armAuthorizeAndServe(w http.ResponseWriter, r *http.Request, next http.Handler, caller rbacCaller) {
	path := strings.Join(armPathSegments(r.URL.Path), "/")
	path = "/" + path
	if refusal := armTenantRefusal(path, caller); refusal != "" {
		azureARMWrongTenant(w, r, refusal)
		return
	}
	op := armOperationOf(r.Method, path)
	switch armAuthorizationOf(r.Method, path) {
	case armAuthAuthenticated:
		next.ServeHTTP(w, r)
		return
	case armAuthSubscriptionList:
		armServeFiltered(w, r, next, func(item map[string]any) bool {
			sub, _ := item["subscriptionId"].(string)
			return sub != "" && rbacReaches(caller, armSubscriptionID(sub))
		})
		return
	case armAuthSubscriptionMember:
		sub := armPathSegments(path)[1]
		if rbacReaches(caller, armSubscriptionID(sub)) || rbacAllows(caller, op.scope, op.action, false) {
			next.ServeHTTP(w, r)
			return
		}
	default:
		if rbacAllows(caller, op.scope, op.action, false) {
			next.ServeHTTP(w, r)
			return
		}
		if op.collection && rbacHoldsBeneath(caller, op.scope) {
			armServeFiltered(w, r, next, func(item map[string]any) bool {
				id, _ := item["id"].(string)
				if id == "" {
					return false
				}
				member := armOperationOf(http.MethodGet, id)
				return rbacAllows(caller, member.scope, member.action, false)
			})
			return
		}
	}
	AzureError(w, "AuthorizationFailed", fmt.Sprintf(
		"The client '%s' with object id '%s' does not have authorization to perform action '%s' over scope '%s' or the scope is invalid. If access was recently granted, please refresh your credentials.",
		caller.name, caller.oid, op.action, op.scope), http.StatusForbidden)
}

// armTenantRefusal names the subscription's tenant when the token was issued
// by another tenant's authority.
func armTenantRefusal(path string, caller rbacCaller) string {
	segs := armPathSegments(path)
	if len(segs) < 2 || !strings.EqualFold(segs[0], "subscriptions") {
		return ""
	}
	tenant := azureSubscriptionTenant(segs[1])
	if strings.EqualFold(tenant, caller.tid) {
		return ""
	}
	return tenant
}

// azureARMWrongTenant writes ARM's 401 InvalidAuthenticationTokenTenant,
// whose challenge names the subscription's own authority.
func azureARMWrongTenant(w http.ResponseWriter, r *http.Request, tenant string) {
	auth := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	issuer := ""
	if claims, err := verifyAzureSimJWT(auth); err == nil {
		issuer, _ = claims["iss"].(string)
	}
	authority := azureAuthBaseURL(r) + "/" + tenant
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer authorization_uri=%q, error="invalid_token", error_description="The access token is from the wrong issuer."`, authority))
	AzureError(w, "InvalidAuthenticationTokenTenant", fmt.Sprintf(
		"The access token is from the wrong issuer '%s'. It must match the tenant 'https://sts.windows.net/%s/' associated with this subscription. Please use the authority (URL) '%s' to get the token. Note, if the subscription is transferred to another tenant there is no impact to the services, but information about new tenant could take time to propagate (up to an hour). If you just transferred your subscription and see this error message, please try back later.",
		issuer, tenant, authority), http.StatusUnauthorized)
}

// armFilterWriter holds a collection response so its members can be filtered
// before it reaches the client.
type armFilterWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (f *armFilterWriter) Header() http.Header { return f.header }

func (f *armFilterWriter) Write(b []byte) (int, error) {
	if f.status == 0 {
		f.status = http.StatusOK
	}
	return f.body.Write(b)
}

func (f *armFilterWriter) WriteHeader(status int) {
	if f.status == 0 {
		f.status = status
	}
}

// armServeFiltered serves a collection read and keeps only the members of its
// `value` array that keep reports true for.
func armServeFiltered(w http.ResponseWriter, r *http.Request, next http.Handler, keep func(map[string]any) bool) {
	rec := &armFilterWriter{header: http.Header{}}
	next.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	body := rec.body.Bytes()
	if rec.status == http.StatusOK && r.Method == http.MethodGet {
		var page map[string]json.RawMessage
		if json.Unmarshal(body, &page) == nil {
			var members []json.RawMessage
			if raw, ok := page["value"]; ok && json.Unmarshal(raw, &members) == nil {
				kept := make([]json.RawMessage, 0, len(members))
				for _, m := range members {
					var member map[string]any
					if json.Unmarshal(m, &member) == nil && keep(member) {
						kept = append(kept, m)
					}
				}
				if encoded, err := json.Marshal(kept); err == nil {
					page["value"] = encoded
					if filtered, err := json.Marshal(page); err == nil {
						body = filtered
					}
				}
			}
		}
	}
	for name, values := range rec.header {
		w.Header()[name] = values
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(rec.status)
	_, _ = w.Write(body)
}

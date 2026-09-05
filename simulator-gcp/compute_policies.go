package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Compute Engine policy collections — security policies and firewall
// policies, each in their project-global, regional and organization spellings.
//
// They are one surface repeated six times: a policy document holding an
// ordered list of rules and a list of associations, with verbs that add, read,
// patch and remove a rule by its priority, attach and detach a target, move the
// policy under a new parent, and clone another policy's rules. Registering them
// once and pointing the registration at each base is what keeps the six honest
// about each other — a rule added through one spelling is read back by the same
// code that serves the rest.
//
// listPreconfiguredExpressionSets returns Google's own catalogue of WAF
// expression sets. It is published, so it is vendored from that publication and
// served — see compute_waf_expression_sets.go. It stays mounted before the
// `{policy}` read, which would otherwise swallow the path and report the method
// as a policy that does not exist: a gap disguised as a served read, which is
// what TestServiceConformance_GCPNoPhantomCoverage exists to catch.

// computePolicyFamily describes one policy collection's declared surface. Each
// toggle mirrors a method the Discovery document declares for that collection
// and nothing more, because a route Google does not publish is a route no
// client will send.
type computePolicyFamily struct {
	base  string // route base, without the trailing resource segment
	store sim.Store[map[string]any]
	kind  string
	scope computeScopeKind

	rules             bool
	packetMirroring   bool
	associations      bool
	listAssociations  bool
	move              bool
	cloneRulesVerb    string // "cloneRules", "copyRules", or empty
	sourceQueryParam  string // the query parameter cloneRules/copyRules reads
	iam               bool
	testIamPermission bool
	setLabels         bool
}

// computePolicyKey names a policy in its store. The organization spelling has
// no project, so the base path itself distinguishes the collections.
func computePolicyKey(r *http.Request, base, name string) string {
	key := base
	for _, param := range []string{"project", "region"} {
		if value := sim.PathParam(r, param); value != "" {
			key = replacePathParam(key, param, value)
		}
	}
	return key + "/" + name
}

func replacePathParam(path, name, value string) string {
	return strings.ReplaceAll(path, "{"+name+"}", value)
}

// computePolicyOperation reports the Operation a policy write returns, in the
// scope the collection lives in.
func computePolicyOperation(r *http.Request, scope computeScopeKind, target, opType string) map[string]any {
	project := sim.PathParam(r, "project")
	segment := "global"
	if scope == cScopeRegion {
		segment = "regions/" + sim.PathParam(r, "region")
	}
	return newComputeOpWithType(project, segment, computeSelfLink(target), opType)
}

// computePolicyRules reads a policy's rule list, which is stored on the policy
// itself exactly as the document describes it.
func computePolicyRules(policy map[string]any, field string) []any {
	rules, _ := policy[field].([]any)
	return rules
}

func computePolicyRuleIndex(rules []any, priority int) int {
	for i, entry := range rules {
		rule, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if computeRulePriority(rule) == priority {
			return i
		}
	}
	return -1
}

func computeRulePriority(rule map[string]any) int {
	switch value := rule["priority"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return 0
}

// computeRequestedPriority reads the priority a rule verb addresses. Google
// defaults it to zero when the caller omits it, which is a real priority.
func computeRequestedPriority(r *http.Request) int {
	value := r.URL.Query().Get("priority")
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

func (f computePolicyFamily) register(srv *sim.Server) {
	resource := f.base + "/{policy}"

	load := func(w http.ResponseWriter, r *http.Request) (string, map[string]any, bool) {
		name := sim.PathParam(r, "policy")
		key := computePolicyKey(r, f.base, name)
		policy, ok := f.store.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "policy %q not found", name)
			return "", nil, false
		}
		return key, policy, true
	}

	// Named before the {policy} read so the catalogue path is not taken for a
	// policy name.
	srv.HandleFunc("GET "+f.base+"/listPreconfiguredExpressionSets", handleWafPreconfiguredExpressionSets)

	// The policy itself.
	srv.HandleFunc("POST "+f.base, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		name, _ := body["name"].(string)
		if name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		key := computePolicyKey(r, f.base, name)
		if _, exists := f.store.Get(key); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "policy %q already exists", name)
			return
		}
		body["kind"] = f.kind
		body["id"] = computeNumericID()
		body["selfLink"] = computeSelfLink(key)
		if _, held := body["rules"]; !held {
			body["rules"] = []any{}
		}
		f.store.Put(key, body)
		sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "insert"))
	})

	srv.HandleFunc("GET "+f.base, func(w http.ResponseWriter, r *http.Request) {
		prefix := computePolicyKey(r, f.base, "")
		items := f.store.Filter(func(policy map[string]any) bool {
			link, _ := policy["selfLink"].(string)
			return strings.HasPrefix(link, computeSelfLink(prefix))
		})
		sort.Slice(items, func(i, j int) bool {
			a, _ := items[i]["name"].(string)
			b, _ := items[j]["name"].(string)
			return a < b
		})
		if items == nil {
			items = []map[string]any{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": f.kind + "List", "items": items})
	})

	srv.HandleFunc("GET "+resource, func(w http.ResponseWriter, r *http.Request) {
		_, policy, ok := load(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, policy)
	})

	srv.HandleFunc("PATCH "+resource, func(w http.ResponseWriter, r *http.Request) {
		key, policy, ok := load(w, r)
		if !ok {
			return
		}
		var patch map[string]any
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		for name, value := range patch {
			policy[name] = value
		}
		f.store.Put(key, policy)
		sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "patch"))
	})

	srv.HandleFunc("DELETE "+resource, func(w http.ResponseWriter, r *http.Request) {
		key, _, ok := load(w, r)
		if !ok {
			return
		}
		f.store.Delete(key)
		sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "delete"))
	})

	if f.rules {
		f.registerRuleVerbs(srv, resource, "rules", "", load)
	}
	if f.packetMirroring {
		f.registerRuleVerbs(srv, resource, "packetMirroringRules", "PacketMirroring", load)
	}
	if f.associations {
		f.registerAssociationVerbs(srv, resource, load)
	}
	if f.listAssociations {
		srv.HandleFunc("GET "+f.base+"/listAssociations", func(w http.ResponseWriter, r *http.Request) {
			target := r.URL.Query().Get("targetResource")
			var found []any
			for _, policy := range f.store.List() {
				for _, entry := range computePolicyRules(policy, "associations") {
					association, ok := entry.(map[string]any)
					if !ok {
						continue
					}
					if attached, _ := association["attachmentTarget"].(string); target == "" || attached == target {
						found = append(found, association)
					}
				}
			}
			if found == nil {
				found = []any{}
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind": f.kind + "ListAssociationsResponse", "associations": found,
			})
		})
	}
	if f.move {
		srv.HandleFunc("POST "+resource+"/move", func(w http.ResponseWriter, r *http.Request) {
			key, policy, ok := load(w, r)
			if !ok {
				return
			}
			parent := r.URL.Query().Get("parentId")
			if parent == "" {
				GCPError(w, http.StatusBadRequest, "parentId is required to move a policy", "INVALID_ARGUMENT")
				return
			}
			policy["parent"] = parent
			f.store.Put(key, policy)
			sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "move"))
		})
	}
	if f.cloneRulesVerb != "" {
		srv.HandleFunc("POST "+resource+"/"+f.cloneRulesVerb, func(w http.ResponseWriter, r *http.Request) {
			key, policy, ok := load(w, r)
			if !ok {
				return
			}
			source := r.URL.Query().Get(f.sourceQueryParam)
			if source == "" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"%s is required to name the policy the rules are copied from", f.sourceQueryParam)
				return
			}
			// The source is named by resource id or by full URL; either way the
			// rules copied are the ones that policy actually holds.
			var copied []any
			for _, candidate := range f.store.List() {
				name, _ := candidate["name"].(string)
				link, _ := candidate["selfLink"].(string)
				if name == source || link == source || strings.HasSuffix(source, "/"+name) {
					copied = computePolicyRules(candidate, "rules")
					break
				}
			}
			if copied == nil {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "source policy %q not found", source)
				return
			}
			policy["rules"] = copied
			f.store.Put(key, policy)
			sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, f.cloneRulesVerb))
		})
	}
	if f.setLabels {
		srv.HandleFunc("POST "+f.base+"/{policy}/setLabels", func(w http.ResponseWriter, r *http.Request) {
			key, policy, ok := load(w, r)
			if !ok {
				return
			}
			var body struct {
				Labels           map[string]string `json:"labels"`
				LabelFingerprint string            `json:"labelFingerprint"`
			}
			if err := sim.ReadJSON(r, &body); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			policy["labels"] = body.Labels
			policy["labelFingerprint"] = computeFingerprint()
			f.store.Put(key, policy)
			sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "setLabels"))
		})
	}
	if f.iam || f.testIamPermission {
		policyName := func(r *http.Request) string {
			return "compute/" + computePolicyKey(r, f.base, sim.PathParam(r, "policy"))
		}
		if f.iam {
			srv.HandleFunc("GET "+f.base+"/{policy}/getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
				handleResourceIAM(w, r, gcpResourcePolicies, policyName(r), "getIamPolicy")
			})
			srv.HandleFunc("POST "+f.base+"/{policy}/setIamPolicy", func(w http.ResponseWriter, r *http.Request) {
				handleResourceIAM(w, r, gcpResourcePolicies, policyName(r), "setIamPolicy")
			})
		}
		srv.HandleFunc("POST "+f.base+"/{policy}/testIamPermissions", func(w http.ResponseWriter, r *http.Request) {
			handleResourceIAM(w, r, gcpResourcePolicies, policyName(r), "testIamPermissions")
		})
	}
}

// registerRuleVerbs mounts the add/get/patch/remove quartet over one rule list.
// The packet-mirroring rules of a network firewall policy are a second list
// with the same semantics, which is why the field and the verb infix are
// parameters rather than literals.
func (f computePolicyFamily) registerRuleVerbs(
	srv *sim.Server,
	resource, field, infix string,
	load func(http.ResponseWriter, *http.Request) (string, map[string]any, bool),
) {
	srv.HandleFunc("POST "+resource+"/add"+infix+"Rule", func(w http.ResponseWriter, r *http.Request) {
		key, policy, ok := load(w, r)
		if !ok {
			return
		}
		var rule map[string]any
		if err := sim.ReadJSON(r, &rule); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		rules := computePolicyRules(policy, field)
		if computePolicyRuleIndex(rules, computeRulePriority(rule)) >= 0 {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"a rule with priority %d already exists", computeRulePriority(rule))
			return
		}
		rules = append(rules, rule)
		// A policy's rules are evaluated in priority order, so they are kept
		// in it rather than in arrival order.
		sort.SliceStable(rules, func(i, j int) bool {
			a, _ := rules[i].(map[string]any)
			b, _ := rules[j].(map[string]any)
			return computeRulePriority(a) < computeRulePriority(b)
		})
		policy[field] = rules
		f.store.Put(key, policy)
		sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "add"+infix+"Rule"))
	})

	srv.HandleFunc("GET "+resource+"/get"+infix+"Rule", func(w http.ResponseWriter, r *http.Request) {
		_, policy, ok := load(w, r)
		if !ok {
			return
		}
		rules := computePolicyRules(policy, field)
		at := computePolicyRuleIndex(rules, computeRequestedPriority(r))
		if at < 0 {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no rule with priority %d", computeRequestedPriority(r))
			return
		}
		sim.WriteJSON(w, http.StatusOK, rules[at])
	})

	srv.HandleFunc("POST "+resource+"/patch"+infix+"Rule", func(w http.ResponseWriter, r *http.Request) {
		key, policy, ok := load(w, r)
		if !ok {
			return
		}
		rules := computePolicyRules(policy, field)
		at := computePolicyRuleIndex(rules, computeRequestedPriority(r))
		if at < 0 {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no rule with priority %d", computeRequestedPriority(r))
			return
		}
		var patch map[string]any
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		existing, _ := rules[at].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		for name, value := range patch {
			existing[name] = value
		}
		rules[at] = existing
		policy[field] = rules
		f.store.Put(key, policy)
		sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "patch"+infix+"Rule"))
	})

	srv.HandleFunc("POST "+resource+"/remove"+infix+"Rule", func(w http.ResponseWriter, r *http.Request) {
		key, policy, ok := load(w, r)
		if !ok {
			return
		}
		rules := computePolicyRules(policy, field)
		at := computePolicyRuleIndex(rules, computeRequestedPriority(r))
		if at < 0 {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no rule with priority %d", computeRequestedPriority(r))
			return
		}
		policy[field] = append(rules[:at], rules[at+1:]...)
		f.store.Put(key, policy)
		sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "remove"+infix+"Rule"))
	})
}

// registerAssociationVerbs mounts the attach, read and detach of a policy's
// associations, which are what bind it to the networks it governs.
func (f computePolicyFamily) registerAssociationVerbs(
	srv *sim.Server,
	resource string,
	load func(http.ResponseWriter, *http.Request) (string, map[string]any, bool),
) {
	find := func(policy map[string]any, name string) int {
		for i, entry := range computePolicyRules(policy, "associations") {
			association, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if got, _ := association["name"].(string); got == name {
				return i
			}
		}
		return -1
	}

	srv.HandleFunc("POST "+resource+"/addAssociation", func(w http.ResponseWriter, r *http.Request) {
		key, policy, ok := load(w, r)
		if !ok {
			return
		}
		var association map[string]any
		if err := sim.ReadJSON(r, &association); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name, _ := association["name"].(string)
		if name == "" {
			// Google derives the association name from the target when the
			// caller leaves it out.
			if target, _ := association["attachmentTarget"].(string); target != "" {
				name = "association-" + computeShortName(target)
				association["name"] = name
			}
		}
		if at := find(policy, name); at >= 0 && r.URL.Query().Get("replaceExistingAssociation") != "true" {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"association %q already exists on this policy", name)
			return
		} else if at >= 0 {
			associations := computePolicyRules(policy, "associations")
			associations[at] = association
			policy["associations"] = associations
			f.store.Put(key, policy)
			sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "addAssociation"))
			return
		}
		policy["associations"] = append(computePolicyRules(policy, "associations"), association)
		f.store.Put(key, policy)
		sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "addAssociation"))
	})

	srv.HandleFunc("GET "+resource+"/getAssociation", func(w http.ResponseWriter, r *http.Request) {
		_, policy, ok := load(w, r)
		if !ok {
			return
		}
		at := find(policy, r.URL.Query().Get("name"))
		if at < 0 {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no association named %q on this policy", r.URL.Query().Get("name"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, computePolicyRules(policy, "associations")[at])
	})

	srv.HandleFunc("POST "+resource+"/removeAssociation", func(w http.ResponseWriter, r *http.Request) {
		key, policy, ok := load(w, r)
		if !ok {
			return
		}
		at := find(policy, r.URL.Query().Get("name"))
		if at < 0 {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no association named %q on this policy", r.URL.Query().Get("name"))
			return
		}
		associations := computePolicyRules(policy, "associations")
		policy["associations"] = append(associations[:at], associations[at+1:]...)
		f.store.Put(key, policy)
		sim.WriteJSON(w, http.StatusOK, computePolicyOperation(r, f.scope, key, "removeAssociation"))
	})
}

// computeShortName is the last path segment of a resource URL.
func computeShortName(value string) string {
	if at := strings.LastIndex(value, "/"); at >= 0 {
		return value[at+1:]
	}
	return value
}

func registerComputePolicies(srv *sim.Server) {
	mk := func(table string) sim.Store[map[string]any] {
		return sim.MakeStore[map[string]any](srv.DB(), table)
	}

	families := []computePolicyFamily{
		// Cloud Armor security policies: project-global, regional, and the
		// organization spelling that carries associations and a move.
		{base: "/compute/v1/projects/{project}/global/securityPolicies", store: mk("compute_security_policies"),
			kind: "compute#securityPolicy", scope: cScopeGlobal, rules: true, setLabels: true},
		{base: "/compute/v1/projects/{project}/regions/{region}/securityPolicies", store: mk("compute_region_security_policies"),
			kind: "compute#securityPolicy", scope: cScopeRegion, rules: true, setLabels: true},
		{base: "/compute/v1/locations/global/securityPolicies", store: mk("compute_org_security_policies"),
			kind: "compute#securityPolicy", scope: cScopeGlobal, rules: true, associations: true,
			listAssociations: true, move: true, cloneRulesVerb: "copyRules", sourceQueryParam: "sourceSecurityPolicy"},

		// Firewall policies, organization spelling. The project-global and
		// regional network firewall policies have a registrar of their own in
		// compute_more3.go, which already serves their rules, associations and
		// IAM.
		{base: "/compute/v1/locations/global/firewallPolicies", store: mk("compute_org_firewall_policies"),
			kind: "compute#firewallPolicy", scope: cScopeGlobal, rules: true, associations: true,
			listAssociations: true, move: true, cloneRulesVerb: "cloneRules", sourceQueryParam: "sourceFirewallPolicy",
			iam: true, testIamPermission: true},
	}
	for _, family := range families {
		family.register(srv)
	}

	// The aggregated read across a project's regions, which only the security
	// policies declare.
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/securityPolicies", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		grouped := map[string]any{}
		add := func(scope string, policy map[string]any) {
			bucket, _ := grouped[scope].(map[string]any)
			if bucket == nil {
				bucket = map[string]any{"securityPolicies": []any{}}
			}
			list, _ := bucket["securityPolicies"].([]any)
			bucket["securityPolicies"] = append(list, policy)
			grouped[scope] = bucket
		}
		for _, family := range families {
			if !strings.Contains(family.base, "securityPolicies") || !strings.Contains(family.base, "{project}") {
				continue
			}
			for _, policy := range family.store.List() {
				link, _ := policy["selfLink"].(string)
				if !strings.Contains(link, "/projects/"+project+"/") {
					continue
				}
				scope := "global"
				if at := strings.Index(link, "/regions/"); at >= 0 {
					region := strings.SplitN(link[at+len("/regions/"):], "/", 2)[0]
					scope = "regions/" + region
				}
				add(scope, policy)
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#securityPolicyAggregatedList", "items": grouped,
		})
	})
}

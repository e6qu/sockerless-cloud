package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The verbs a Compute Engine instance carries beyond its lifecycle each write
// something a later read returns, and the tests beside them read it back. Three
// are declared NotImplemented, since answering them means inventing what the
// hardware would say: the console screenshot, the shielded instance identity
// (the vTPM endorsement key), and the diagnostic interrupt.

// gcpComputeInstanceGroups is the instance-group store, shared so the referrers
// read can report the groups an instance belongs to.
var gcpComputeInstanceGroups sim.Store[storedComputeInstanceGroup]

func registerComputeInstanceVerbs(srv *sim.Server, instances sim.Store[ComputeInstance]) {
	const base = "/compute/v1/projects/{project}/zones/{zone}/instances"

	load := func(w http.ResponseWriter, r *http.Request) (string, ComputeInstance, bool) {
		project, zone, name := sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "name")
		link := computeInstanceSelfLink(project, zone, name)
		instance, ok := instances.Get(link)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return "", ComputeInstance{}, false
		}
		return link, instance, true
	}
	operation := func(r *http.Request, link, opType string) map[string]any {
		return computeZoneOp(sim.PathParam(r, "project"), sim.PathParam(r, "zone"), link, opType)
	}

	// mount serves a verb that changes the instance record; an apply error is
	// INVALID_ARGUMENT.
	mount := func(method, verb string, apply func(*ComputeInstance, *http.Request) error) {
		srv.HandleFunc(method+" "+base+"/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			link, instance, ok := load(w, r)
			if !ok {
				return
			}
			if err := apply(&instance, r); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
				return
			}
			instances.Put(link, instance)
			sim.WriteJSON(w, http.StatusOK, operation(r, link, verb))
		})
	}
	// The four verbs the document declares as PATCH mount as PATCH; the rest are
	// POST.
	write := func(verb string, apply func(*ComputeInstance, *http.Request) error) {
		mount(http.MethodPost, verb, apply)
	}
	patch := func(verb string, apply func(*ComputeInstance, *http.Request) error) {
		mount(http.MethodPatch, verb, apply)
	}

	write("setDeletionProtection", func(instance *ComputeInstance, r *http.Request) error {
		value := r.URL.Query().Get("deletionProtection")
		// Google defaults the flag to true when the verb names no value.
		protected := value == "" || value == "true"
		if value != "" && value != "true" && value != "false" {
			return errComputeInvalid("deletionProtection must be true or false")
		}
		instance.DeletionProtection = protected
		return nil
	})

	write("setMinCpuPlatform", func(instance *ComputeInstance, r *http.Request) error {
		var req struct {
			MinCpuPlatform string `json:"minCpuPlatform"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			return err
		}
		instance.MinCpuPlatform = req.MinCpuPlatform
		instance.CpuPlatform = gcpInstanceCPUPlatform(*instance)
		return nil
	})

	write("setMachineResources", func(instance *ComputeInstance, r *http.Request) error {
		var req struct {
			GuestAccelerators []map[string]any `json:"guestAccelerators"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			return err
		}
		instance.GuestAccelerators = req.GuestAccelerators
		return nil
	})

	write("setScheduling", func(instance *ComputeInstance, r *http.Request) error {
		var scheduling map[string]any
		if err := sim.ReadJSON(r, &scheduling); err != nil {
			return err
		}
		instance.Scheduling = scheduling
		return nil
	})

	write("setServiceAccount", func(instance *ComputeInstance, r *http.Request) error {
		var req struct {
			Email  string   `json:"email"`
			Scopes []string `json:"scopes"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			return err
		}
		account := map[string]any{"email": req.Email}
		if req.Scopes != nil {
			scopes := make([]any, 0, len(req.Scopes))
			for _, scope := range req.Scopes {
				scopes = append(scopes, scope)
			}
			account["scopes"] = scopes
		}
		instance.ServiceAccounts = []map[string]any{account}
		return nil
	})

	patch("updateShieldedInstanceConfig", func(instance *ComputeInstance, r *http.Request) error {
		var config map[string]any
		if err := sim.ReadJSON(r, &config); err != nil {
			return err
		}
		instance.ShieldedInstanceConfig = config
		return nil
	})

	patch("setShieldedInstanceIntegrityPolicy", func(instance *ComputeInstance, r *http.Request) error {
		var policy map[string]any
		if err := sim.ReadJSON(r, &policy); err != nil {
			return err
		}
		instance.ShieldedInstanceIntegrityPolicy = policy
		return nil
	})

	patch("updateDisplayDevice", func(instance *ComputeInstance, r *http.Request) error {
		var device map[string]any
		if err := sim.ReadJSON(r, &device); err != nil {
			return err
		}
		instance.DisplayDevice = device
		return nil
	})

	write("setName", func(instance *ComputeInstance, r *http.Request) error {
		var req struct {
			Name        string `json:"name"`
			CurrentName string `json:"currentName"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			return err
		}
		if req.Name == "" {
			return errComputeInvalid("name is required to rename an instance")
		}
		if req.CurrentName != "" && req.CurrentName != instance.Name {
			return errComputeInvalid("currentName does not name this instance")
		}
		// Google applies the new name at the machine's next restart, so the
		// record keeps its identity and reports the name it will take.
		instance.StatusMessage = "the instance will be renamed to " + req.Name + " on its next restart"
		return nil
	})

	write("addResourcePolicies", func(instance *ComputeInstance, r *http.Request) error {
		var req struct {
			ResourcePolicies []string `json:"resourcePolicies"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			return err
		}
		for _, policy := range req.ResourcePolicies {
			if computeStringInSlice(instance.ResourcePolicies, policy) {
				return errComputeInvalid("resource policy " + policy + " is already attached")
			}
			instance.ResourcePolicies = append(instance.ResourcePolicies, policy)
		}
		return nil
	})

	write("removeResourcePolicies", func(instance *ComputeInstance, r *http.Request) error {
		var req struct {
			ResourcePolicies []string `json:"resourcePolicies"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			return err
		}
		for _, policy := range req.ResourcePolicies {
			at := computeIndexOfString(instance.ResourcePolicies, policy)
			if at < 0 {
				return errComputeInvalid("resource policy " + policy + " is not attached to this instance")
			}
			instance.ResourcePolicies = append(instance.ResourcePolicies[:at], instance.ResourcePolicies[at+1:]...)
		}
		return nil
	})

	write("setDiskAutoDelete", func(instance *ComputeInstance, r *http.Request) error {
		deviceName := r.URL.Query().Get("deviceName")
		if deviceName == "" {
			return errComputeInvalid("deviceName is required to name the disk")
		}
		autoDelete, err := strconv.ParseBool(defaultString(r.URL.Query().Get("autoDelete"), "false"))
		if err != nil {
			return errComputeInvalid("autoDelete must be true or false")
		}
		for i := range instance.Disks {
			if instance.Disks[i].DeviceName == deviceName {
				instance.Disks[i].AutoDelete = autoDelete
				return nil
			}
		}
		return errComputeInvalid("the instance has no disk with device name " + deviceName)
	})

	// The access configurations a network interface is reachable through.
	findInterface := func(instance *ComputeInstance, name string) int {
		for i := range instance.NetworkInterfaces {
			if instance.NetworkInterfaces[i].Name == name {
				return i
			}
		}
		return -1
	}

	write("addAccessConfig", func(instance *ComputeInstance, r *http.Request) error {
		at := findInterface(instance, r.URL.Query().Get("networkInterface"))
		if at < 0 {
			return errComputeInvalid("the instance has no network interface named " +
				r.URL.Query().Get("networkInterface"))
		}
		var config ComputeAccessConfig
		if err := sim.ReadJSON(r, &config); err != nil {
			return err
		}
		if config.Name == "" {
			config.Name = "external-nat"
		}
		for _, existing := range instance.NetworkInterfaces[at].AccessConfigs {
			if existing.Name == config.Name {
				return errComputeInvalid("an access config named " + config.Name + " already exists")
			}
		}
		instance.NetworkInterfaces[at].AccessConfigs = append(
			instance.NetworkInterfaces[at].AccessConfigs, config)
		return nil
	})

	write("updateAccessConfig", func(instance *ComputeInstance, r *http.Request) error {
		at := findInterface(instance, r.URL.Query().Get("networkInterface"))
		if at < 0 {
			return errComputeInvalid("the instance has no network interface named " +
				r.URL.Query().Get("networkInterface"))
		}
		var config ComputeAccessConfig
		if err := sim.ReadJSON(r, &config); err != nil {
			return err
		}
		for i, existing := range instance.NetworkInterfaces[at].AccessConfigs {
			if existing.Name == config.Name {
				instance.NetworkInterfaces[at].AccessConfigs[i] = config
				return nil
			}
		}
		return errComputeInvalid("no access config named " + config.Name + " on that interface")
	})

	write("deleteAccessConfig", func(instance *ComputeInstance, r *http.Request) error {
		at := findInterface(instance, r.URL.Query().Get("networkInterface"))
		if at < 0 {
			return errComputeInvalid("the instance has no network interface named " +
				r.URL.Query().Get("networkInterface"))
		}
		wanted := r.URL.Query().Get("accessConfig")
		configs := instance.NetworkInterfaces[at].AccessConfigs
		for i, existing := range configs {
			if existing.Name == wanted {
				instance.NetworkInterfaces[at].AccessConfigs = append(configs[:i], configs[i+1:]...)
				return nil
			}
		}
		return errComputeInvalid("no access config named " + wanted + " on that interface")
	})

	write("addNetworkInterface", func(instance *ComputeInstance, r *http.Request) error {
		var iface ComputeNetworkInterface
		if err := sim.ReadJSON(r, &iface); err != nil {
			return err
		}
		if iface.Name == "" {
			iface.Name = "nic" + strconv.Itoa(len(instance.NetworkInterfaces))
		}
		if findInterface(instance, iface.Name) >= 0 {
			return errComputeInvalid("an interface named " + iface.Name + " already exists")
		}
		instance.NetworkInterfaces = append(instance.NetworkInterfaces, iface)
		return nil
	})

	patch("updateNetworkInterface", func(instance *ComputeInstance, r *http.Request) error {
		at := findInterface(instance, r.URL.Query().Get("networkInterface"))
		if at < 0 {
			return errComputeInvalid("the instance has no network interface named " +
				r.URL.Query().Get("networkInterface"))
		}
		var iface ComputeNetworkInterface
		if err := sim.ReadJSON(r, &iface); err != nil {
			return err
		}
		iface.Name = instance.NetworkInterfaces[at].Name
		instance.NetworkInterfaces[at] = iface
		return nil
	})

	// deleteNetworkInterface names its interface networkInterfaceName, where
	// every other interface verb calls the same thing networkInterface.
	write("deleteNetworkInterface", func(instance *ComputeInstance, r *http.Request) error {
		wanted := r.URL.Query().Get("networkInterfaceName")
		at := findInterface(instance, wanted)
		if at < 0 {
			return errComputeInvalid("the instance has no network interface named " + wanted)
		}
		instance.NetworkInterfaces = append(
			instance.NetworkInterfaces[:at], instance.NetworkInterfaces[at+1:]...)
		return nil
	})

	// The Instance schema carries a Cloud Armor policy on the access configs of
	// the named interfaces, or of every interface when the request names none.
	write("setSecurityPolicy", func(instance *ComputeInstance, r *http.Request) error {
		var req struct {
			NetworkInterfaces []string `json:"networkInterfaces"`
			SecurityPolicy    string   `json:"securityPolicy"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			return err
		}
		wanted := map[string]bool{}
		for _, name := range req.NetworkInterfaces {
			wanted[name] = true
		}
		applied := false
		for i := range instance.NetworkInterfaces {
			iface := &instance.NetworkInterfaces[i]
			if len(wanted) > 0 && !wanted[iface.Name] {
				continue
			}
			for j := range iface.AccessConfigs {
				iface.AccessConfigs[j].SecurityPolicy = req.SecurityPolicy
				applied = true
			}
		}
		if !applied {
			return errComputeInvalid(
				"the instance has no external address on the named interfaces to apply a security policy to")
		}
		return nil
	})

	// Compute Engine reports each maintenance verb through the instance, which
	// these record.
	for _, verb := range []string{"performMaintenance", "simulateMaintenanceEvent", "reportHostAsFaulty"} {
		verb := verb
		write(verb, func(instance *ComputeInstance, r *http.Request) error {
			instance.StatusMessage = "the host reported " + verb
			return nil
		})
	}

	srv.HandleFunc("GET "+base+"/{name}/referrers", func(w http.ResponseWriter, r *http.Request) {
		link, _, ok := load(w, r)
		if !ok {
			return
		}
		// Instance groups are the collection that refers to an instance, so read the
		// referrers from them.
		referrers := []any{}
		if gcpComputeInstanceGroups == nil {
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind": "compute#instanceListReferrers", "items": []any{},
			})
			return
		}
		for _, group := range gcpComputeInstanceGroups.List() {
			for _, member := range group.Instances {
				if member.Instance == link {
					referrers = append(referrers, map[string]any{
						"kind": "compute#reference", "referrer": group.SelfLink,
						"target": link, "referenceType": "MEMBER_OF",
					})
					break
				}
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#instanceListReferrers", "items": referrers,
		})
	})

	srv.HandleFunc("GET "+base+"/{name}/getEffectiveFirewalls", func(w http.ResponseWriter, r *http.Request) {
		_, instance, ok := load(w, r)
		if !ok {
			return
		}
		networks := map[string]bool{}
		for _, iface := range instance.NetworkInterfaces {
			if iface.Network != "" {
				networks[iface.Network] = true
			}
		}
		rules := []any{}
		if gcpFirewalls != nil {
			for _, firewall := range gcpFirewalls.List() {
				if networks[firewall.Network] {
					rules = append(rules, firewall)
				}
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"firewalls": rules})
	})

	policyName := func(r *http.Request) string {
		return "compute/" + strings.TrimPrefix(
			computeInstanceSelfLink(sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "resource")),
			computeSelfLink(""))
	}
	srv.HandleFunc("GET "+base+"/{resource}/getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		handleResourceIAM(w, r, gcpResourcePolicies, policyName(r), "getIamPolicy")
	})
	srv.HandleFunc("POST "+base+"/{resource}/setIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		handleResourceIAM(w, r, gcpResourcePolicies, policyName(r), "setIamPolicy")
	})
	srv.HandleFunc("POST "+base+"/{resource}/testIamPermissions", func(w http.ResponseWriter, r *http.Request) {
		handleResourceIAM(w, r, gcpResourcePolicies, policyName(r), "testIamPermissions")
	})

	srv.HandleFunc("GET "+base+"/{name}/getGuestAttributes", func(w http.ResponseWriter, r *http.Request) {
		if _, instance, ok := load(w, r); ok {
			handleGetGuestAttributes(w, r, instance)
		}
	})

	// Mount the three inventing verbs so the gap shows on the wire rather than as
	// an instance that does not exist.
	for verb, why := range map[string]string{
		"screenshot": "the console screenshot is a capture of the machine's framebuffer, " +
			"and an invented image is not one",
		"getShieldedInstanceIdentity": "the shielded instance identity is the machine's vTPM " +
			"endorsement key, and generated key material would not be it",
		"sendDiagnosticInterrupt": "a diagnostic interrupt is a non-maskable interrupt the " +
			"hypervisor delivers, which nothing here can deliver",
	} {
		verb, why := verb, why
		method := http.MethodGet
		if verb == "sendDiagnosticInterrupt" {
			method = http.MethodPost
		}
		srv.HandleFunc(method+" "+base+"/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			if _, _, ok := load(w, r); !ok {
				return
			}
			GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED",
				"the simulator serves no %s: %s", verb, why)
		})
	}
}

func computeInstanceSelfLink(project, zone, name string) string {
	return fmt.Sprintf("projects/%s/zones/%s/instances/%s", project, zone, name)
}

// errComputeInvalidMessage is a request Compute Engine would refuse.
type errComputeInvalidMessage string

func (e errComputeInvalidMessage) Error() string { return string(e) }

func errComputeInvalid(message string) error { return errComputeInvalidMessage(message) }

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func computeStringInSlice(values []string, wanted string) bool {
	return computeIndexOfString(values, wanted) >= 0
}

func computeIndexOfString(values []string, wanted string) int {
	for i, value := range values {
		if value == wanted {
			return i
		}
	}
	return -1
}

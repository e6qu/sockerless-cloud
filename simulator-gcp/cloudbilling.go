package main

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Cloud Billing v1 — the billing-account collection, the project links, and
// the service catalog.
//
// A billing account is real control-plane state: created (as an account
// under a parent or as a subaccount of a master), listed, patched, moved
// between organizations, and linked to projects through
// projects.updateBillingInfo — the write terraform-provider-google issues
// when a google_project carries billing_account, whose read
// (projects.getBillingInfo) the provider already exercised against this
// simulator. IAM on an account rides the same per-resource policy store
// every other AIP-141 resource uses.
//
// The service catalog is this installation's own: services.list names the
// services this simulator hosts, under stable identifiers in the format the
// API uses. skus.list is served and empty — a SKU carries published pricing,
// and this installation has no price sheet; an empty catalog is that truth,
// the same way the internet-registry imports of an association never enabled
// are empty. Fabricating Google's public prices here would be inventing
// data the deployment does not have.

// BillingAccount mirrors the v1 wire shape.
type BillingAccount struct {
	Name                 string `json:"name"`
	Open                 bool   `json:"open"`
	DisplayName          string `json:"displayName,omitempty"`
	Parent               string `json:"parent,omitempty"`
	MasterBillingAccount string `json:"masterBillingAccount,omitempty"`
	CurrencyCode         string `json:"currencyCode,omitempty"`
}

// BillingProjectLink records which billing account a project draws on.
type BillingProjectLink struct {
	ProjectId          string `json:"projectId"`
	BillingAccountName string `json:"billingAccountName"`
}

var (
	billingAccounts     sim.Store[BillingAccount]
	billingProjectLinks sim.Store[BillingProjectLink]
)

// billingHostedServices is the catalog of services this simulator hosts,
// in the display names Google Cloud uses. The identifiers are stable across
// restarts because clients key on them.
var billingHostedServices = []struct{ ID, DisplayName string }{
	{"1D2E-7A4C-93F0", "Artifact Registry"},
	{"29E7-DA93-4A50", "BigQuery"},
	{"3B1F-6C82-D514", "Cloud Bigtable"},
	{"4A9D-2E17-8CB6", "Cloud Build"},
	{"5C63-F81B-20A9", "Cloud DNS"},
	{"6E15-93D0-B7C4", "Cloud Functions"},
	{"7F28-1A6E-4D93", "Cloud Key Management Service (KMS)"},
	{"802B-C5D7-E961", "Cloud Logging"},
	{"91D4-3F0A-57BE", "Cloud Memorystore for Redis"},
	{"A237-8E5C-1FD0", "Cloud Pub/Sub"},
	{"B34A-D961-20C8", "Cloud Run"},
	{"C45B-E072-31D9", "Cloud SQL"},
	{"D56C-F183-42EA", "Cloud Spanner"},
	{"E67D-0294-53FB", "Cloud Storage"},
	{"F78E-13A5-640C", "Compute Engine"},
	{"089F-24B6-751D", "Dataflow"},
	{"19A0-35C7-862E", "Eventarc"},
	{"2AB1-46D8-973F", "Secret Manager"},
	{"3BC2-57E9-A840", "Serverless VPC Access"},
	{"4CD3-68FA-B951", "Cloud Firestore"},
}

// billingDefaultAccountID is the deployment's own billing account,
// materialized at startup the way crmEnsureDefaultOrganization materializes
// the organization: Google provisions billing accounts out of band (the API
// creates only subaccounts), so a deployment arrives with one, under a
// stable identifier bootstrap configuration can name.
const billingDefaultAccountID = "0A0A0A-B1B1B1-C2C2C2"

func billingEnsureDefaultAccount() {
	if _, ok := billingAccounts.Get(billingDefaultAccountID); ok {
		return
	}
	billingAccounts.Put(billingDefaultAccountID, BillingAccount{
		Name:         "billingAccounts/" + billingDefaultAccountID,
		Open:         true,
		DisplayName:  "Default Billing Account",
		Parent:       crmDefaultOrganization,
		CurrencyCode: "USD",
	})
}

func registerCloudBilling(srv *sim.Server, resourcePolicies sim.Store[IAMPolicy]) {
	billingAccounts = sim.MakeStore[BillingAccount](srv.DB(), "billing_accounts")
	billingProjectLinks = sim.MakeStore[BillingProjectLink](srv.DB(), "billing_project_links")
	billingEnsureDefaultAccount()

	srv.HandleFunc("GET /v1/billingAccounts", handleBillingListAccounts)
	srv.HandleFunc("POST /v1/billingAccounts", func(w http.ResponseWriter, r *http.Request) {
		handleBillingCreateAccount(w, r, "")
	})
	// The single-segment child carries the colon verbs too: a GET may be the
	// account read or :getIamPolicy, a POST is :move or an IAM write —
	// delegated to the same per-resource policy store the generic dispatcher
	// uses, since these literal routes shadow it.
	srv.HandleFunc("GET /v1/billingAccounts/{accountAction}", func(w http.ResponseWriter, r *http.Request) {
		name, verb, _ := strings.Cut(sim.PathParam(r, "accountAction"), ":")
		account, ok := billingAccounts.Get(name)
		if !ok {
			billingAccountNotFound(w, name)
			return
		}
		switch verb {
		case "":
			sim.WriteJSON(w, http.StatusOK, account)
		case "getIamPolicy":
			handleResourceIAM(w, r, resourcePolicies, "billingAccounts/"+name, "getIamPolicy")
		default:
			gcpMethodNotFound(w)
		}
	})
	srv.HandleFunc("POST /v1/billingAccounts/{accountAction}", func(w http.ResponseWriter, r *http.Request) {
		name, verb, hasVerb := strings.Cut(sim.PathParam(r, "accountAction"), ":")
		if !hasVerb {
			gcpMethodNotFound(w)
			return
		}
		account, ok := billingAccounts.Get(name)
		if !ok {
			billingAccountNotFound(w, name)
			return
		}
		switch verb {
		case "move":
			var req struct {
				DestinationParent string `json:"destinationParent"`
			}
			if err := sim.ReadJSON(r, &req); err != nil || req.DestinationParent == "" {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "destinationParent is required")
				return
			}
			billingMoveAccount(w, account, req.DestinationParent)
		case "setIamPolicy", "testIamPermissions":
			handleResourceIAM(w, r, resourcePolicies, "billingAccounts/"+name, verb)
		default:
			gcpMethodNotFound(w)
		}
	})
	srv.HandleFunc("PATCH /v1/billingAccounts/{account}", handleBillingPatchAccount)
	srv.HandleFunc("GET /v1/billingAccounts/{account}/subAccounts", handleBillingListSubAccounts)
	srv.HandleFunc("POST /v1/billingAccounts/{account}/subAccounts", func(w http.ResponseWriter, r *http.Request) {
		master := sim.PathParam(r, "account")
		if _, ok := billingAccounts.Get(master); !ok {
			billingAccountNotFound(w, master)
			return
		}
		handleBillingCreateAccount(w, r, "billingAccounts/"+master)
	})
	srv.HandleFunc("GET /v1/billingAccounts/{account}/projects", handleBillingListAccountProjects)

	// The organization-scoped spellings address the same collection with the
	// parent taken from the URL. The move spelling here is a GET, exactly as
	// the Discovery document declares it: the destination is the
	// organization in the path.
	srv.HandleFunc("GET /v1/organizations/{organization}/billingAccounts", func(w http.ResponseWriter, r *http.Request) {
		org := "organizations/" + sim.PathParam(r, "organization")
		var out []BillingAccount
		for _, account := range billingAccounts.List() {
			if account.Parent == org {
				out = append(out, account)
			}
		}
		billingWriteAccountList(w, out)
	})
	srv.HandleFunc("POST /v1/organizations/{organization}/billingAccounts", func(w http.ResponseWriter, r *http.Request) {
		handleBillingCreateAccountUnderParent(w, r, "", "organizations/"+sim.PathParam(r, "organization"))
	})
	srv.HandleFunc("GET /v1/organizations/{organization}/billingAccounts/{accountAction}", func(w http.ResponseWriter, r *http.Request) {
		name, verb, _ := strings.Cut(sim.PathParam(r, "accountAction"), ":")
		account, ok := billingAccounts.Get(name)
		if !ok {
			billingAccountNotFound(w, name)
			return
		}
		if verb != "move" {
			gcpMethodNotFound(w)
			return
		}
		billingMoveAccount(w, account, "organizations/"+sim.PathParam(r, "organization"))
	})

	// projects.updateBillingInfo — the link write. The read half lives in
	// cloudresourcemanager.go beside the project it reads.
	srv.HandleFunc("PUT /v1/projects/{project}/billingInfo", handleBillingUpdateProjectInfo)

	// The installation's service catalog.
	srv.HandleFunc("GET /v1/services", handleBillingListServices)
	srv.HandleFunc("GET /v1/services/{service}/skus", handleBillingListSkus)

	registerCloudBillingTaskAPI(srv)
}

// registerCloudBillingTaskAPI declares Cloud Billing's conversational agent
// task surface — tasks, their push notification configs, message send and
// stream, and the on-file payment card — as unserved. It is Google's own
// agent protocol layered onto billing, not billing-account control plane: a
// task's state is that agent's own tracking of work it is doing, a message
// send or stream hands the call to the LLM-backed agent behind it, and the
// payment card is a real financial instrument on file with Google. None of
// that is state this simulator holds or could honestly invent, the same
// reasoning that already declares a licence code or a console screenshot
// unserved elsewhere in this deployment.
//
// The Discovery document spells four of the task operations twice: once as
// the flat "tasks/{tasksId}"-shaped paths registered above the wildcards
// below, and once as generic AIP-127 resource names ({+name}, {+parent})
// that this deployment's own task IDs also satisfy. The wildcard routes
// below answer that second spelling; Go's ServeMux prefers the more
// specific literal routes when both could match the same request.
func registerCloudBillingTaskAPI(srv *sim.Server) {
	unimplemented := func(what, why string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			sim.GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no %s: %s", what, why)
		}
	}
	const taskWhy = "a task's state is Cloud Billing's own conversational agent tracking work it is doing, and an invented status would not be that tracking"
	const cardWhy = "the payment card is a real financial instrument on file with Google, and no card was ever issued to this project"
	const messageWhy = "sending or streaming a message hands it to Cloud Billing's own LLM-backed agent, which this simulator does not run"

	// {tasksAction} carries an optional ":subscribe" colon verb in the same
	// segment: Go's ServeMux requires a wildcard to be the whole segment, so
	// "{tasksId}:subscribe" (id and verb as separate pattern pieces) is not a
	// legal pattern — the same reason billingAccounts above uses
	// {accountAction} rather than a bare {account} for its colon verbs.
	srv.HandleFunc("GET /v1/tasks/{tasksAction}", func(w http.ResponseWriter, r *http.Request) {
		_, verb, hasVerb := strings.Cut(sim.PathParam(r, "tasksAction"), ":")
		if hasVerb && verb != "subscribe" {
			gcpMethodNotFound(w)
			return
		}
		what := "tasks.get"
		if verb == "subscribe" {
			what = "tasks.subscribe"
		}
		unimplemented(what, taskWhy)(w, r)
	})
	srv.HandleFunc("POST /v1/tasks/{tasksAction}", func(w http.ResponseWriter, r *http.Request) {
		_, verb, hasVerb := strings.Cut(sim.PathParam(r, "tasksAction"), ":")
		if !hasVerb || verb != "cancel" {
			gcpMethodNotFound(w)
			return
		}
		unimplemented("tasks.cancel", taskWhy)(w, r)
	})
	srv.HandleFunc("GET /v1/tasks/{tasksId}/pushNotificationConfigs", unimplemented("tasks.pushNotificationConfigs.list", taskWhy))
	srv.HandleFunc("POST /v1/tasks/{tasksId}/pushNotificationConfigs", unimplemented("tasks.pushNotificationConfigs.create", taskWhy))
	srv.HandleFunc("GET /v1/tasks/{tasksId}/pushNotificationConfigs/{pushNotificationConfigsId}", unimplemented("tasks.pushNotificationConfigs.get", taskWhy))
	srv.HandleFunc("DELETE /v1/tasks/{tasksId}/pushNotificationConfigs/{pushNotificationConfigsId}", unimplemented("tasks.pushNotificationConfigs.delete", taskWhy))
	srv.HandleFunc("GET /v1/card", unimplemented("v1.getCard", cardWhy))
	srv.HandleFunc("POST /v1/message:send", unimplemented("message.send", messageWhy))
	srv.HandleFunc("POST /v1/message:stream", unimplemented("message.stream", messageWhy))

	// The mux pattern itself, not just the handler body, has to stay scoped
	// to the tasks collection: the data-plane auth middleware
	// (bearerAuthMiddleware in token_signing.go) decides whether a request
	// needs a credential by asking Go's own mux whether any pattern matches
	// it at all, before this handler ever runs. A bare "/v1/{name...}" would
	// match every GET under /v1/, including paths no method publishes, and
	// the middleware would demand a token for them instead of leaving them
	// to fail closed with 404.
	srv.HandleFunc("GET /v1/tasks/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		name, verb, _ := strings.Cut("tasks/"+sim.PathParam(r, "rest"), ":")
		switch verb {
		case "subscribe":
			unimplemented("tasks.subscribe", taskWhy)(w, r)
		case "":
			what := "tasks.get"
			if strings.Contains(name, "/pushNotificationConfigs/") {
				what = "tasks.pushNotificationConfigs.get"
			}
			unimplemented(what, taskWhy)(w, r)
		default:
			gcpMethodNotFound(w)
		}
	})
	srv.HandleFunc("DELETE /v1/tasks/{rest...}", unimplemented("tasks.pushNotificationConfigs.delete", taskWhy))
	// The POST /v1/{resource...} spelling is the AIP-141 IAM catch-all
	// dispatcher's route (iam.go); it falls through to
	// cloudBillingUnimplementedTaskWrite for the two cases here.
}

// cloudBillingUnimplementedTaskWrite answers the resource-name-form writes
// (POST /v1/{+parent} for tasks.pushNotificationConfigs.create, POST
// /v1/{+name}:cancel for tasks.cancel) that the AIP-141 IAM catch-all
// dispatcher in iam.go falls through to once resource carries no recognized
// IAM colon verb. It reports false for anything else, matching that
// dispatcher's existing "not a recognized verb" contract.
func cloudBillingUnimplementedTaskWrite(w http.ResponseWriter, r *http.Request, resource string) bool {
	name, verb, hasVerb := strings.Cut(resource, ":")
	if !strings.HasPrefix(name, "tasks/") {
		return false
	}
	const why = "a task's state is Cloud Billing's own conversational agent tracking work it is doing, and an invented status would not be that tracking"
	switch {
	case !hasVerb:
		sim.GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no tasks.pushNotificationConfigs.create: %s", why)
	case verb == "cancel":
		sim.GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no tasks.cancel: %s", why)
	default:
		return false
	}
	return true
}

func billingAccountNotFound(w http.ResponseWriter, name string) {
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
		"billing account billingAccounts/%s not found", name)
}

// billingNewAccountID mints an identifier in the API's own XXXXXX-XXXXXX-XXXXXX
// format.
func billingNewAccountID() string {
	raw := make([]byte, 9)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return fmt.Sprintf("%06X-%06X-%06X",
		uint32(raw[0])<<16|uint32(raw[1])<<8|uint32(raw[2]),
		uint32(raw[3])<<16|uint32(raw[4])<<8|uint32(raw[5]),
		uint32(raw[6])<<16|uint32(raw[7])<<8|uint32(raw[8]))
}

func handleBillingCreateAccount(w http.ResponseWriter, r *http.Request, master string) {
	handleBillingCreateAccountUnderParent(w, r, master, "")
}

// handleBillingCreateAccountUnderParent creates an account whose parent the
// route already names — the organization-scoped create spelling.
func handleBillingCreateAccountUnderParent(w http.ResponseWriter, r *http.Request, master, parent string) {
	var req BillingAccount
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.DisplayName == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "displayName is required")
		return
	}
	if master == "" {
		master = req.MasterBillingAccount
	}
	if master != "" {
		masterID := strings.TrimPrefix(master, "billingAccounts/")
		if _, ok := billingAccounts.Get(masterID); !ok {
			billingAccountNotFound(w, masterID)
			return
		}
		master = "billingAccounts/" + masterID
	}
	if parent == "" {
		parent = req.Parent
	}
	id := billingNewAccountID()
	account := BillingAccount{
		Name:                 "billingAccounts/" + id,
		Open:                 true,
		DisplayName:          req.DisplayName,
		Parent:               parent,
		MasterBillingAccount: master,
		CurrencyCode:         defaultStr(req.CurrencyCode, "USD"),
	}
	billingAccounts.Put(id, account)
	sim.WriteJSON(w, http.StatusOK, account)
}

func handleBillingListAccounts(w http.ResponseWriter, r *http.Request) {
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	parent := r.URL.Query().Get("parent")
	// The one filter the API documents: subaccounts of a master.
	var master string
	if filter != "" {
		key, value, ok := strings.Cut(filter, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "master_billing_account") {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"filter supports only master_billing_account=billingAccounts/{id}")
			return
		}
		master = strings.TrimSpace(value)
	}
	var out []BillingAccount
	for _, account := range billingAccounts.List() {
		if master != "" && account.MasterBillingAccount != master {
			continue
		}
		if parent != "" && account.Parent != parent {
			continue
		}
		out = append(out, account)
	}
	billingWriteAccountList(w, out)
}

func billingWriteAccountList(w http.ResponseWriter, accounts []BillingAccount) {
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
	if accounts == nil {
		accounts = []BillingAccount{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"billingAccounts": accounts})
}

func handleBillingPatchAccount(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "account")
	var req BillingAccount
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	updated, ok := billingAccounts.Get(name)
	if !ok {
		billingAccountNotFound(w, name)
		return
	}
	// The API mutates only the display name; updateMask admits nothing else.
	if req.DisplayName != "" {
		updated.DisplayName = req.DisplayName
	}
	billingAccounts.Put(name, updated)
	sim.WriteJSON(w, http.StatusOK, updated)
}

func billingMoveAccount(w http.ResponseWriter, account BillingAccount, destination string) {
	if !strings.HasPrefix(destination, "organizations/") {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"the destination parent must be an organization")
		return
	}
	if _, ok := crmOrganizations.Get(destination); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", destination)
		return
	}
	account.Parent = destination
	billingAccounts.Put(strings.TrimPrefix(account.Name, "billingAccounts/"), account)
	sim.WriteJSON(w, http.StatusOK, account)
}

func handleBillingListSubAccounts(w http.ResponseWriter, r *http.Request) {
	master := "billingAccounts/" + sim.PathParam(r, "account")
	if _, ok := billingAccounts.Get(strings.TrimPrefix(master, "billingAccounts/")); !ok {
		billingAccountNotFound(w, strings.TrimPrefix(master, "billingAccounts/"))
		return
	}
	var out []BillingAccount
	for _, account := range billingAccounts.List() {
		if account.MasterBillingAccount == master {
			out = append(out, account)
		}
	}
	billingWriteAccountList(w, out)
}

func handleBillingListAccountProjects(w http.ResponseWriter, r *http.Request) {
	name := "billingAccounts/" + sim.PathParam(r, "account")
	if _, ok := billingAccounts.Get(sim.PathParam(r, "account")); !ok {
		billingAccountNotFound(w, sim.PathParam(r, "account"))
		return
	}
	var linked []string
	for _, link := range billingProjectLinks.List() {
		if link.BillingAccountName == name {
			linked = append(linked, link.ProjectId)
		}
	}
	sort.Strings(linked)
	out := make([]map[string]any, 0, len(linked))
	for _, projectID := range linked {
		out = append(out, billingProjectInfo(projectID))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"projectBillingInfo": out})
}

func handleBillingUpdateProjectInfo(w http.ResponseWriter, r *http.Request) {
	project, ok := crmResolveProject(sim.PathParam(r, "project"))
	if !ok {
		crmProjectPermissionDenied(w)
		return
	}
	var req struct {
		BillingAccountName string `json:"billingAccountName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.BillingAccountName == "" {
		// An empty name disables billing: the link is removed.
		billingProjectLinks.Delete(project.ProjectId)
		sim.WriteJSON(w, http.StatusOK, billingProjectInfo(project.ProjectId))
		return
	}
	id := strings.TrimPrefix(req.BillingAccountName, "billingAccounts/")
	account, exists := billingAccounts.Get(id)
	if !exists {
		billingAccountNotFound(w, id)
		return
	}
	if !account.Open {
		sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"billing account %s is closed", account.Name)
		return
	}
	billingProjectLinks.Put(project.ProjectId, BillingProjectLink{
		ProjectId:          project.ProjectId,
		BillingAccountName: account.Name,
	})
	sim.WriteJSON(w, http.StatusOK, billingProjectInfo(project.ProjectId))
}

// billingProjectInfo renders a project's ProjectBillingInfo from the link
// store — the one truth both getBillingInfo and updateBillingInfo answer
// from.
func billingProjectInfo(projectID string) map[string]any {
	info := map[string]any{
		"name":               "projects/" + projectID + "/billingInfo",
		"projectId":          projectID,
		"billingAccountName": "",
		"billingEnabled":     false,
	}
	link, ok := billingProjectLinks.Get(projectID)
	if !ok {
		return info
	}
	account, exists := billingAccounts.Get(strings.TrimPrefix(link.BillingAccountName, "billingAccounts/"))
	info["billingAccountName"] = link.BillingAccountName
	info["billingEnabled"] = exists && account.Open
	return info
}

func handleBillingListServices(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0, len(billingHostedServices))
	for _, service := range billingHostedServices {
		out = append(out, map[string]any{
			"name":               "services/" + service.ID,
			"serviceId":          service.ID,
			"displayName":        service.DisplayName,
			"businessEntityName": "businessEntities/GCP",
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"services": out})
}

func handleBillingListSkus(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "service")
	for _, service := range billingHostedServices {
		if service.ID == id {
			// This installation publishes no price sheet, so the service's
			// SKU list is empty — that is its truth, not a placeholder.
			sim.WriteJSON(w, http.StatusOK, map[string]any{"skus": []any{}})
			return
		}
	}
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service services/%s not found", id)
}

package main

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A Cloud Billing account is real control-plane state: created under a parent
// or as a subaccount, listed, patched, moved between organizations, and linked
// to projects through projects.updateBillingInfo. services.list names the
// services this simulator hosts; skus.list is empty because a SKU carries
// published pricing, which this deployment does not have.

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

// billingHostedServices is the catalog of services this simulator hosts, under
// Google Cloud's display names. Clients key on the IDs, so they never change.
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

// billingDefaultAccountID is the deployment's own billing account, created at
// startup as crmEnsureDefaultOrganization creates the organization: Google
// provisions billing accounts out of band, and the API creates only
// subaccounts.
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
	// The single-segment child carries the colon verbs too: GET is the read or
	// :getIamPolicy, POST is :move or an IAM write. These literal routes shadow
	// the generic IAM dispatcher, so they delegate to its policy store.
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
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "destinationParent is required")
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

	// The organization-scoped spellings take the parent from the URL. The move
	// spelling is a GET, as the Discovery document declares it, with the
	// destination organization in the path.
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

	// projects.getBillingInfo lives in cloudresourcemanager.go beside the project
	// it reads.
	srv.HandleFunc("PUT /v1/projects/{project}/billingInfo", handleBillingUpdateProjectInfo)

	srv.HandleFunc("GET /v1/services", handleBillingListServices)
	srv.HandleFunc("GET /v1/services/{service}/skus", handleBillingListSkus)

	registerCloudBillingTaskAPI(srv)
}

// registerCloudBillingTaskAPI declares Cloud Billing's agent task surface
// (tasks, push notification configs, message send and stream, the on-file
// payment card) unserved: an LLM-backed agent and a real payment instrument
// hold that state, and the simulator has neither. The Discovery document
// spells four task operations twice, as flat "tasks/{tasksId}" paths and as
// AIP-127 resource names; the wildcard routes answer the second, and ServeMux
// prefers the literal routes when both match.
func registerCloudBillingTaskAPI(srv *sim.Server) {
	unimplemented := func(what, why string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no %s: %s", what, why)
		}
	}
	const taskWhy = "a task's state is Cloud Billing's own conversational agent tracking work it is doing, and an invented status would not be that tracking"
	const cardWhy = "the payment card is a real financial instrument on file with Google, and no card was ever issued to this project"
	const messageWhy = "sending or streaming a message hands it to Cloud Billing's own LLM-backed agent, which this simulator does not run"

	// A ServeMux wildcard must be a whole segment, so {tasksAction} carries the
	// optional ":subscribe" verb.
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

	// Keep the pattern scoped to /v1/tasks: bearerAuthMiddleware asks the mux
	// whether any pattern matches before demanding a token, and a bare
	// "/v1/{name...}" would make it demand one for paths that should 404.
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

// cloudBillingUnimplementedTaskWrite answers the resource-name-form task
// writes (tasks.pushNotificationConfigs.create, tasks.cancel) that the IAM
// dispatcher in iam.go falls through to; it reports false for anything else.
func cloudBillingUnimplementedTaskWrite(w http.ResponseWriter, r *http.Request, resource string) bool {
	name, verb, hasVerb := strings.Cut(resource, ":")
	if !strings.HasPrefix(name, "tasks/") {
		return false
	}
	const why = "a task's state is Cloud Billing's own conversational agent tracking work it is doing, and an invented status would not be that tracking"
	switch {
	case !hasVerb:
		GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no tasks.pushNotificationConfigs.create: %s", why)
	case verb == "cancel":
		GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no tasks.cancel: %s", why)
	default:
		return false
	}
	return true
}

func billingAccountNotFound(w http.ResponseWriter, name string) {
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
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

// handleBillingCreateAccountUnderParent serves the organization-scoped create,
// whose route names the parent.
func handleBillingCreateAccountUnderParent(w http.ResponseWriter, r *http.Request, master, parent string) {
	var req BillingAccount
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		return
	}
	if req.DisplayName == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "displayName is required")
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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"the destination parent must be an organization")
		return
	}
	if _, ok := crmOrganizations.Get(destination); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", destination)
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
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
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
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
// store both getBillingInfo and updateBillingInfo answer from.
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
			// This deployment publishes no price sheet, so the SKU list is empty.
			sim.WriteJSON(w, http.StatusOK, map[string]any{"skus": []any{}})
			return
		}
	}
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service services/%s not found", id)
}

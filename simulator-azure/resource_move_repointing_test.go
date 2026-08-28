package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The families that joined the cross-resource-group move dispatch alongside
// Microsoft.Network are driven here through the real Resources_MoveResources
// operation on a whole simulator, not through their hook functions, because
// what the move has to get right spans slices: the resource re-homes, the rows
// stored beneath it follow, the credential the resource ID derives is not
// rotated, and every reference another resource holds to it is re-pointed.

const moveTestSubscription = "11111111-2222-3333-4444-555555555555"
const moveTestARMHost = "127.0.0.1:4581"

// newMoveTestServer builds the whole simulator, which is what registers every
// store the repointing pass scans.
func newMoveTestServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulatorWithUI(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "disabled"}, false)
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	// Long-running operations complete in a goroutine. One still running
	// when this test ends would read and write the stores while the next
	// test rebuilds them.
	t.Cleanup(AwaitAzureAsyncOperations)
	return srv
}

// moveARMToken mints the Azure Resource Manager access token every ARM request
// carries, from the simulator's own signing key — the same token its OAuth2
// endpoint issues to a client.
func moveARMToken(t *testing.T) string {
	t.Helper()
	token, err := mintAzureSimJWTForUser(
		EntraUser{OID: "move-test-oid", Sub: "move-test-sub"},
		"move-test-tenant", defaultAzureTokenAudience,
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM token: %v", err)
	}
	return token
}

// moveARM sends one Azure Resource Manager request and returns the status and
// body. It carries the ARM bearer the simulator's bearer middleware requires,
// exactly as a client's request does.
func moveARM(t *testing.T, srv *sim.Server, method, path, body string) (int, []byte) {
	t.Helper()
	url := "http://" + moveTestARMHost + path
	if !strings.Contains(path, "api-version=") {
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		url += separator + "api-version=2021-04-01"
	}
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+moveARMToken(t))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	return resp.StatusCode, rec.Body.Bytes()
}

func moveEnsureRG(t *testing.T, srv *sim.Server, name string) {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodPut,
		fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", moveTestSubscription, name),
		`{"location":"eastus"}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create resource group %s: status %d: %s", name, status, body)
	}
}

// moveResource runs Resources_ValidateMoveResources and then
// Resources_MoveResources for one resource, the sequence a client performs.
func moveResource(t *testing.T, srv *sim.Server, srcRG, dstRG, resourceID string) {
	t.Helper()
	payload := fmt.Sprintf(`{"resources":["%s"],"targetResourceGroup":"/subscriptions/%s/resourceGroups/%s"}`,
		resourceID, moveTestSubscription, dstRG)
	for _, action := range []string{"validateMoveResources", "moveResources"} {
		status, body := moveARM(t, srv, http.MethodPost,
			fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/%s", moveTestSubscription, srcRG, action), payload)
		if status != http.StatusNoContent {
			t.Fatalf("%s %s: status %d: %s", action, resourceID, status, body)
		}
	}
}

// moveResourceRefused runs Resources_ValidateMoveResources for a resource the
// simulator must refuse and returns the ARM error code it answered with.
func moveResourceRefused(t *testing.T, srv *sim.Server, srcRG, dstRG, resourceID string) (int, string) {
	t.Helper()
	payload := fmt.Sprintf(`{"resources":["%s"],"targetResourceGroup":"/subscriptions/%s/resourceGroups/%s"}`,
		resourceID, moveTestSubscription, dstRG)
	status, body := moveARM(t, srv, http.MethodPost,
		fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/validateMoveResources", moveTestSubscription, srcRG), payload)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode move error envelope %q: %v", body, err)
	}
	return status, envelope.Error.Code
}

func moveResourceID(rg, provider, typeName, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/%s/%s",
		moveTestSubscription, rg, provider, typeName, name)
}

// TestMoveAPIManagementServicePinsSubscriptionKeys drives the
// Microsoft.ApiManagement hook. An API Management subscription's keys are
// derived from the subscription's own resource ID, which embeds the service's
// resource group, so a move that only re-homed records would silently
// invalidate every subscription key an API consumer holds.
//
// The proof this test can reach is bounded by the simulator: it serves the API
// Management *control* plane only — there is no gateway data plane on the
// service's own host that a subscription key could be presented to — so the
// strongest statement available is that the material listSecrets serves, which
// IS the Ocp-Apim-Subscription-Key value, is unchanged by the move, and that
// the rotation contract still holds afterwards.
func TestMoveAPIManagementServicePinsSubscriptionKeys(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "apim-move-src", "apim-move-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	const service, subscription = "apim-move-svc", "consumer-a"
	serviceID := moveResourceID(srcRG, "Microsoft.ApiManagement", "service", service)
	status, body := moveARM(t, srv, http.MethodPut, serviceID,
		`{"location":"eastus","sku":{"name":"Developer","capacity":1},"properties":{"publisherEmail":"a@b.test","publisherName":"Move Test"}}`)
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
		t.Fatalf("create API Management service: status %d: %s", status, body)
	}
	subscriptionPath := serviceID + "/subscriptions/" + subscription
	status, body = moveARM(t, srv, http.MethodPut, subscriptionPath,
		`{"properties":{"displayName":"Consumer A","scope":"/apis"}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create subscription: status %d: %s", status, body)
	}

	keysBefore := apimSecrets(t, srv, subscriptionPath)

	moveResource(t, srv, srcRG, dstRG, serviceID)

	movedServiceID := moveResourceID(dstRG, "Microsoft.ApiManagement", "service", service)
	if status, _ := moveARM(t, srv, http.MethodGet, movedServiceID, ""); status != http.StatusOK {
		t.Fatalf("the moved service does not answer under the destination group: status %d", status)
	}
	if status, _ := moveARM(t, srv, http.MethodGet, serviceID, ""); status != http.StatusNotFound {
		t.Fatalf("the moved service still answers under the source group: status %d", status)
	}

	movedSubscription := movedServiceID + "/subscriptions/" + subscription
	if status, _ := moveARM(t, srv, http.MethodGet, movedSubscription, ""); status != http.StatusOK {
		t.Fatalf("the subscription did not move with the service: status %d", status)
	}
	keysAfter := apimSecrets(t, srv, movedSubscription)
	if keysAfter["primaryKey"] != keysBefore["primaryKey"] || keysAfter["secondaryKey"] != keysBefore["secondaryKey"] {
		t.Fatalf("the move rotated the subscription keys: %v != %v", keysAfter, keysBefore)
	}

	// The rotation contract is unchanged by the move.
	azureBumpKeyGen(movedSubscription, "apim-subscription-primary", "")
	if rotated := apimSecrets(t, srv, movedSubscription); rotated["primaryKey"] == keysBefore["primaryKey"] {
		t.Fatal("regenerating the moved subscription's primary key returned the pinned material")
	}
}

func apimSecrets(t *testing.T, srv *sim.Server, subscriptionID string) map[string]string {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodPost, subscriptionID+"/listSecrets", "")
	if status != http.StatusOK {
		t.Fatalf("listSecrets: status %d: %s", status, body)
	}
	var keys map[string]string
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("decode subscription keys: %v", err)
	}
	return keys
}

// TestMoveLogicWorkflowKeepsCallbackURLValid drives the Microsoft.Logic hook.
// A callback URL an operator handed out before a move has to keep working
// after one, and two properties of the workflow decide whether it does: the
// access key that signs it, which is derived from the workflow's resource ID
// and therefore pinned onto the moved ID, and the address it points at, which
// is the workflow's own identifier on the Logic Apps service host and names no
// resource group at all. The URL is compared whole, so either one changing
// fails this.
//
// The proof is bounded by the simulator: it serves the Logic Apps control
// plane, and no endpoint here consumes a callback URL, so the statement is
// that the URL and the signature the workflow computes for it are byte-for-
// byte the ones it issued before the move — which is exactly what a verifier
// would compare — rather than a request accepted by a trigger endpoint.
func TestMoveLogicWorkflowKeepsCallbackURLValid(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "logic-move-src", "logic-move-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	const workflow = "logic-move-wf"
	workflowID := moveResourceID(srcRG, "Microsoft.Logic", "workflows", workflow)
	status, body := moveARM(t, srv, http.MethodPut, workflowID,
		`{"location":"eastus","properties":{"definition":{"$schema":"https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#","contentVersion":"1.0.0.0","triggers":{},"actions":{},"outputs":{}},"parameters":{}}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create workflow: status %d: %s", status, body)
	}

	callbackBefore := logicCallback(t, srv, workflowID)
	if callbackBefore.Value == "" {
		t.Fatal("listCallbackUrl returned no callback URL")
	}
	if strings.Contains(callbackBefore.Value, srcRG) {
		t.Fatalf("the callback URL %q embeds the resource group, so a move would change it", callbackBefore.Value)
	}
	signatureBefore := callbackBefore.Queries.Sig
	valueBefore := callbackBefore.Value

	moveResource(t, srv, srcRG, dstRG, workflowID)

	movedID := moveResourceID(dstRG, "Microsoft.Logic", "workflows", workflow)
	if status, _ := moveARM(t, srv, http.MethodGet, movedID, ""); status != http.StatusOK {
		t.Fatalf("the moved workflow does not answer under the destination group: status %d", status)
	}
	if status, _ := moveARM(t, srv, http.MethodGet, workflowID, ""); status != http.StatusNotFound {
		t.Fatalf("the moved workflow still answers under the source group: status %d", status)
	}

	callbackAfter := logicCallback(t, srv, movedID)
	if callbackAfter.Queries.Sig != signatureBefore {
		t.Fatalf("the move invalidated the signature of an outstanding callback URL: %q != %q",
			callbackAfter.Queries.Sig, signatureBefore)
	}
	if callbackAfter.Value != valueBefore {
		t.Fatalf("the move changed an outstanding callback URL:\n before %q\n after  %q", valueBefore, callbackAfter.Value)
	}
	// The address is the workflow's own on the service host, so neither group
	// appears in it before or after the move.
	for _, group := range []string{srcRG, dstRG} {
		if strings.Contains(callbackAfter.BasePath, group) {
			t.Fatalf("the moved workflow advertises resource group %q in its accessEndpoint: %q", group, callbackAfter.BasePath)
		}
	}

	// The rotation contract is unchanged: regenerating the access key
	// invalidates every outstanding callback URL, exactly as before the move.
	status, body = moveARM(t, srv, http.MethodPost, movedID+"/regenerateAccessKey", `{"keyType":"Primary"}`)
	if status != http.StatusOK {
		t.Fatalf("regenerateAccessKey: status %d: %s", status, body)
	}
	if rotated := logicCallback(t, srv, movedID); rotated.Queries.Sig == signatureBefore {
		t.Fatal("regenerating the moved workflow's access key returned the pinned signature")
	}
}

type logicCallbackBody struct {
	Value    string `json:"value"`
	BasePath string `json:"basePath"`
	Queries  struct {
		Sig string `json:"sig"`
	} `json:"queries"`
}

func logicCallback(t *testing.T, srv *sim.Server, workflowID string) logicCallbackBody {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodPost, workflowID+"/listCallbackUrl", "")
	if status != http.StatusOK {
		t.Fatalf("listCallbackUrl: status %d: %s", status, body)
	}
	var out logicCallbackBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode callback URL: %v", err)
	}
	return out
}

// TestMoveCosmosAccountKeepsKeysAndDocuments drives the Microsoft.DocumentDB
// hook. A Cosmos DB account's four master keys are derived from its resource
// ID, so a move that only re-homed the record would rotate the credential in
// every connection string an application holds.
//
// The proof reaches the data plane for both the data and the credential: a
// document written before the move is read back afterwards through a request
// the data plane authenticates against the account's keys, the connection
// string listConnectionStrings serves is unchanged, and the four master keys
// are unchanged. The key captured before the move still signs a request the
// data plane accepts after it, which is what an application holding that
// credential depends on.
func TestMoveCosmosAccountKeepsKeysAndDocuments(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "cosmos-move-src", "cosmos-move-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	const account, database, container = "cosmosmoveacct", "orders", "items"
	accountID := moveResourceID(srcRG, "Microsoft.DocumentDB", "databaseAccounts", account)
	status, body := moveARM(t, srv, http.MethodPut, accountID,
		`{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard","locations":[{"locationName":"eastus"}]}}`)
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
		t.Fatalf("create Cosmos DB account: status %d: %s", status, body)
	}

	keysBefore := cosmosKeysBody(t, srv, accountID)
	connectionBefore := cosmosConnectionString(t, srv, accountID)

	// A real document operation before the move, over the account's data plane.
	cosmosDataPlanePut(t, srv, account, database, container, "order-1", keysBefore["primaryMasterKey"])
	if got := cosmosDataPlaneGet(t, srv, account, database, container, "order-1", keysBefore["primaryMasterKey"]); got != "order-1" {
		t.Fatalf("the document written before the move reads back as %q", got)
	}

	moveResource(t, srv, srcRG, dstRG, accountID)

	movedID := moveResourceID(dstRG, "Microsoft.DocumentDB", "databaseAccounts", account)
	if status, _ := moveARM(t, srv, http.MethodGet, movedID, ""); status != http.StatusOK {
		t.Fatalf("the moved account does not answer under the destination group: status %d", status)
	}
	if status, _ := moveARM(t, srv, http.MethodGet, accountID, ""); status != http.StatusNotFound {
		t.Fatalf("the moved account still answers under the source group: status %d", status)
	}

	keysAfter := cosmosKeysBody(t, srv, movedID)
	for role, before := range keysBefore {
		if keysAfter[role] != before {
			t.Errorf("the move rotated %s", role)
		}
	}
	if after := cosmosConnectionString(t, srv, movedID); after != connectionBefore {
		t.Fatalf("the connection string an application holds changed across the move:\n%q\n%q", after, connectionBefore)
	}

	// The data plane still serves the account and the document written before
	// the move, presenting the key captured before it.
	if got := cosmosDataPlaneGet(t, srv, account, database, container, "order-1", keysBefore["primaryMasterKey"]); got != "order-1" {
		t.Fatalf("the document written before the move is not readable after it (got %q)", got)
	}

	// The rotation contract is unchanged.
	status, body = moveARM(t, srv, http.MethodPost, movedID+"/regenerateKey", `{"keyKind":"primary"}`)
	if status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("regenerateKey: status %d: %s", status, body)
	}
	if rotated := cosmosKeysBody(t, srv, movedID); rotated["primaryMasterKey"] == keysBefore["primaryMasterKey"] {
		t.Fatal("regenerating the moved account's primary key returned the pinned material")
	}
}

func cosmosKeysBody(t *testing.T, srv *sim.Server, accountID string) map[string]string {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodPost, accountID+"/listKeys", "")
	if status != http.StatusOK {
		t.Fatalf("listKeys: status %d: %s", status, body)
	}
	var keys map[string]string
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("decode Cosmos keys: %v", err)
	}
	return keys
}

func cosmosConnectionString(t *testing.T, srv *sim.Server, accountID string) string {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodPost, accountID+"/listConnectionStrings", "")
	if status != http.StatusOK {
		t.Fatalf("listConnectionStrings: status %d: %s", status, body)
	}
	var out struct {
		ConnectionStrings []struct {
			ConnectionString string `json:"connectionString"`
		} `json:"connectionStrings"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode connection strings: %v", err)
	}
	if len(out.ConnectionStrings) == 0 {
		t.Fatal("listConnectionStrings returned none")
	}
	return out.ConnectionStrings[0].ConnectionString
}

// cosmosDataPlaneRequest addresses the account's document endpoint the way a
// Cosmos client does: the account's own hostname and a master-key
// Authorization token signed with one of the account's keys.
func cosmosDataPlaneRequest(t *testing.T, srv *sim.Server, method, path, account, key, body string) (int, []byte) {
	t.Helper()
	return cosmosSignedRequest(t, srv, method, path, account, key, body)
}

func cosmosDataPlanePut(t *testing.T, srv *sim.Server, account, database, container, id, key string) {
	t.Helper()
	for _, step := range []struct {
		path string
		body string
	}{
		{"/dbs", fmt.Sprintf(`{"id":%q}`, database)},
		{"/dbs/" + database + "/colls", fmt.Sprintf(`{"id":%q}`, container)},
	} {
		status, body := cosmosDataPlaneRequest(t, srv, http.MethodPost, step.path, account, key, step.body)
		if status != http.StatusCreated && status != http.StatusOK && status != http.StatusConflict {
			t.Fatalf("cosmos data plane POST %s: status %d: %s", step.path, status, body)
		}
	}
	path := "/dbs/" + database + "/colls/" + container + "/docs"
	status, body := cosmosDataPlaneRequest(t, srv, http.MethodPost, path, account, key,
		fmt.Sprintf(`{"id":%q,"total":42}`, id))
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("cosmos data plane create document: status %d: %s", status, body)
	}
}

func cosmosDataPlaneGet(t *testing.T, srv *sim.Server, account, database, container, id, key string) string {
	t.Helper()
	path := "/dbs/" + database + "/colls/" + container + "/docs/" + id
	status, body := cosmosDataPlaneRequest(t, srv, http.MethodGet, path, account, key, "")
	if status != http.StatusOK {
		t.Fatalf("cosmos data plane read document: status %d: %s", status, body)
	}
	var doc struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	return doc.ID
}

// TestMoveEventGridPartnerNamespacePinsKeys drives the partner-namespace hook.
// A partner namespace serves the same two access keys a custom topic does, and
// they are derived from its resource ID, so the move has to pin them: the key a
// partner publisher already holds must keep authorizing after the move, and the
// namespace's channels must move with it.
//
// The credential check is the data plane's own — eventGridKeyAuthorizes is the
// function every publish runs its Authorization header through — because the
// simulator routes an /api/events publish to a custom topic or a domain and
// not to a partner namespace, so the key cannot be presented over HTTP here.
func TestMoveEventGridPartnerNamespacePinsKeys(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "eg-partner-src", "eg-partner-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	const namespace, channel = "eg-move-partner-ns", "orders"
	namespaceID := moveResourceID(srcRG, "Microsoft.EventGrid", "partnerNamespaces", namespace)
	status, body := moveARM(t, srv, http.MethodPut, namespaceID,
		`{"location":"eastus","properties":{"partnerRegistrationFullyQualifiedId":"/subscriptions/`+moveTestSubscription+`/providers/Microsoft.EventGrid/partnerRegistrations/contoso"}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create partner namespace: status %d: %s", status, body)
	}
	status, body = moveARM(t, srv, http.MethodPut, namespaceID+"/channels/"+channel,
		`{"properties":{"channelType":"PartnerTopic","partnerTopicInfo":{"azureSubscriptionId":"`+moveTestSubscription+`","resourceGroupName":"`+dstRG+`","name":"downstream"}}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create channel: status %d: %s", status, body)
	}

	keysBefore := eventGridKeys(t, srv, namespaceID)
	if !eventGridKeyAuthorizes(namespaceID, keysBefore["key1"]) {
		t.Fatal("the key listKeys served must authorize before the move")
	}

	moveResource(t, srv, srcRG, dstRG, namespaceID)

	movedID := moveResourceID(dstRG, "Microsoft.EventGrid", "partnerNamespaces", namespace)
	if status, _ := moveARM(t, srv, http.MethodGet, movedID, ""); status != http.StatusOK {
		t.Fatalf("the moved partner namespace does not answer under the destination group: status %d", status)
	}
	if status, _ := moveARM(t, srv, http.MethodGet, namespaceID, ""); status != http.StatusNotFound {
		t.Fatalf("the moved partner namespace still answers under the source group: status %d", status)
	}
	if status, _ := moveARM(t, srv, http.MethodGet, movedID+"/channels/"+channel, ""); status != http.StatusOK {
		t.Fatalf("the channel did not move with the partner namespace: status %d", status)
	}

	keysAfter := eventGridKeys(t, srv, movedID)
	if keysAfter["key1"] != keysBefore["key1"] || keysAfter["key2"] != keysBefore["key2"] {
		t.Fatalf("the move rotated the partner namespace's access keys: %v != %v", keysAfter, keysBefore)
	}
	if !eventGridKeyAuthorizes(movedID, keysBefore["key1"]) {
		t.Fatal("the key captured before the move no longer authorizes a publish to the moved namespace")
	}

	status, body = moveARM(t, srv, http.MethodPost, movedID+"/regenerateKey", `{"keyName":"key1"}`)
	if status != http.StatusOK {
		t.Fatalf("regenerateKey: status %d: %s", status, body)
	}
	if eventGridKeyAuthorizes(movedID, keysBefore["key1"]) {
		t.Fatal("regenerating the moved namespace's key left the pinned material authorizing")
	}
}

// TestMoveEventGridSystemTopicRepointsSource drives the system-topic hook, and
// with it the inbound-reference half of the move. A system topic names the
// Azure resource it observes twice — `source` and `metricResourceId` — so
// moving that source resource has to re-point both, and moving the system topic
// itself must not disturb them.
func TestMoveEventGridSystemTopicRepointsSource(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "eg-system-src", "eg-system-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	const account, topic = "egsystemsrcacct", "eg-move-system-topic"
	accountID := moveResourceID(srcRG, "Microsoft.Storage", "storageAccounts", account)
	status, body := moveARM(t, srv, http.MethodPut, accountID,
		`{"location":"eastus","sku":{"name":"Standard_LRS"},"kind":"StorageV2","properties":{}}`)
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
		t.Fatalf("create storage account: status %d: %s", status, body)
	}
	topicID := moveResourceID(srcRG, "Microsoft.EventGrid", "systemTopics", topic)
	status, body = moveARM(t, srv, http.MethodPut, topicID, fmt.Sprintf(
		`{"location":"eastus","properties":{"source":%q,"topicType":"Microsoft.Storage.StorageAccounts"}}`, accountID))
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create system topic: status %d: %s", status, body)
	}

	// Moving the *source* re-points the reference the system topic holds to it.
	moveResource(t, srv, srcRG, dstRG, accountID)
	movedAccountID := moveResourceID(dstRG, "Microsoft.Storage", "storageAccounts", account)
	if got := eventGridSystemTopicSource(t, srv, topicID); got != movedAccountID {
		t.Fatalf("the system topic still names %q as its source, want the moved account %q", got, movedAccountID)
	}

	// Moving the system topic itself re-homes it without disturbing that
	// reference.
	moveResource(t, srv, srcRG, dstRG, topicID)
	movedTopicID := moveResourceID(dstRG, "Microsoft.EventGrid", "systemTopics", topic)
	if status, _ := moveARM(t, srv, http.MethodGet, movedTopicID, ""); status != http.StatusOK {
		t.Fatalf("the moved system topic does not answer under the destination group: status %d", status)
	}
	if status, _ := moveARM(t, srv, http.MethodGet, topicID, ""); status != http.StatusNotFound {
		t.Fatalf("the moved system topic still answers under the source group: status %d", status)
	}
	if got := eventGridSystemTopicSource(t, srv, movedTopicID); got != movedAccountID {
		t.Fatalf("moving the system topic disturbed its source reference: %q", got)
	}
}

func eventGridKeys(t *testing.T, srv *sim.Server, resourceID string) map[string]string {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodPost, resourceID+"/listKeys", "")
	if status != http.StatusOK {
		t.Fatalf("listKeys: status %d: %s", status, body)
	}
	var keys map[string]string
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("decode Event Grid keys: %v", err)
	}
	return keys
}

func eventGridSystemTopicSource(t *testing.T, srv *sim.Server, topicID string) string {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodGet, topicID, "")
	if status != http.StatusOK {
		t.Fatalf("get system topic: status %d: %s", status, body)
	}
	var topic struct {
		Properties struct {
			Source string `json:"source"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &topic); err != nil {
		t.Fatalf("decode system topic: %v", err)
	}
	return topic.Properties.Source
}

// TestMoveVirtualNetworkRepointsEveryReferrer is the reason the repointing pass
// exists. A virtual network is named by resources that do not move with it — a
// network interface's ip configuration, a private DNS zone's virtual network
// link — and its own subnets are addressed beneath its resource ID. Moving it
// without re-pointing those would leave the fabric naming an address nothing
// answers to.
func TestMoveVirtualNetworkRepointsEveryReferrer(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "net-move-src", "net-move-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	const vnet, zone, link = "net-move-vnet", "internal.test", "net-move-link"
	vnetID := moveResourceID(srcRG, "Microsoft.Network", "virtualNetworks", vnet)
	status, body := moveARM(t, srv, http.MethodPut, vnetID,
		`{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.20.0.0/16"]}}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create virtual network: status %d: %s", status, body)
	}
	zoneID := moveResourceID(srcRG, "Microsoft.Network", "privateDnsZones", zone)
	status, body = moveARM(t, srv, http.MethodPut, zoneID, `{"location":"global"}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create private DNS zone: status %d: %s", status, body)
	}
	linkID := zoneID + "/virtualNetworkLinks/" + link
	status, body = moveARM(t, srv, http.MethodPut, linkID, fmt.Sprintf(
		`{"location":"global","properties":{"virtualNetwork":{"id":%q},"registrationEnabled":false}}`, vnetID))
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create virtual network link: status %d: %s", status, body)
	}

	moveResource(t, srv, srcRG, dstRG, vnetID)

	movedVnetID := moveResourceID(dstRG, "Microsoft.Network", "virtualNetworks", vnet)
	if status, _ := moveARM(t, srv, http.MethodGet, movedVnetID, ""); status != http.StatusOK {
		t.Fatalf("the moved virtual network does not answer under the destination group: status %d", status)
	}
	if status, _ := moveARM(t, srv, http.MethodGet, vnetID, ""); status != http.StatusNotFound {
		t.Fatalf("the moved virtual network still answers under the source group: status %d", status)
	}
	// The referrer, which did not move, now names the moved address.
	if got := vnetLinkTarget(t, srv, linkID); got != movedVnetID {
		t.Fatalf("the virtual network link still names %q, want %q", got, movedVnetID)
	}
}

func vnetLinkTarget(t *testing.T, srv *sim.Server, linkID string) string {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodGet, linkID, "")
	if status != http.StatusOK {
		t.Fatalf("get virtual network link: status %d: %s", status, body)
	}
	var link struct {
		Properties struct {
			VirtualNetwork struct {
				ID string `json:"id"`
			} `json:"virtualNetwork"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &link); err != nil {
		t.Fatalf("decode virtual network link: %v", err)
	}
	return link.Properties.VirtualNetwork.ID
}

// TestMoveRedisCacheRepointsLinkedServer covers the inbound reference an Azure
// Cache for Redis linked server holds: the geo-replication partner names its
// peer by resource ID in linkedRedisCacheId, and that peer is a separate
// top-level resource that does not move with it.
func TestMoveRedisCacheRepointsLinkedServer(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "redis-link-src", "redis-link-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	const primary, secondary = "redis-link-primary", "redis-link-secondary"
	primaryID := moveResourceID(srcRG, "Microsoft.Cache", "Redis", primary)
	secondaryID := moveResourceID(srcRG, "Microsoft.Cache", "Redis", secondary)
	for _, id := range []string{primaryID, secondaryID} {
		status, body := moveARM(t, srv, http.MethodPut, id,
			`{"location":"eastus","properties":{"sku":{"name":"Premium","family":"P","capacity":1}}}`)
		if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
			t.Fatalf("create cache %s: status %d: %s", id, status, body)
		}
	}
	linkID := primaryID + "/linkedServers/" + secondary
	status, body := moveARM(t, srv, http.MethodPut, linkID, fmt.Sprintf(
		`{"properties":{"linkedRedisCacheId":%q,"linkedRedisCacheLocation":"westus","serverRole":"Secondary"}}`, secondaryID))
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
		t.Fatalf("create linked server: status %d: %s", status, body)
	}

	moveResource(t, srv, srcRG, dstRG, secondaryID)

	movedSecondaryID := moveResourceID(dstRG, "Microsoft.Cache", "Redis", secondary)
	if got := redisLinkedCacheID(t, srv, linkID); got != movedSecondaryID {
		t.Fatalf("the linked server still names %q, want the moved cache %q", got, movedSecondaryID)
	}
}

func redisLinkedCacheID(t *testing.T, srv *sim.Server, linkID string) string {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodGet, linkID, "")
	if status != http.StatusOK {
		t.Fatalf("get linked server: status %d: %s", status, body)
	}
	var link struct {
		Properties struct {
			LinkedRedisCacheID string `json:"linkedRedisCacheId"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &link); err != nil {
		t.Fatalf("decode linked server: %v", err)
	}
	return link.Properties.LinkedRedisCacheID
}

// TestMoveRefusesTheTypesAzureRefuses pins the other half of move fidelity.
// Every type here is one Azure Resource Manager will not move between resource
// groups — the move-support table marks it No, or does not list it at all —
// so the simulator answers ResourceMoveNotSupported. A hook for any of them
// would make the simulator diverge from Azure in the permissive direction,
// which is the worse direction.
func TestMoveRefusesTheTypesAzureRefuses(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "refuse-move-src", "refuse-move-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	for _, tc := range []struct {
		provider string
		typeName string
		why      string
	}{
		{"Microsoft.EventGrid", "partnerRegistrations", "published Resource group = No"},
		{"Microsoft.EventGrid", "partnerConfigurations", "absent from the move-support table"},
		{"Microsoft.Network", "privateLinkServices", "published Resource group = No"},
		{"Microsoft.Network", "applicationGateways", "published Resource group = No"},
		{"Microsoft.Network", "natGateways", "published Resource group = No"},
		{"Microsoft.Network", "networkProfiles", "published Resource group = No"},
		{"Microsoft.Network", "virtualNetworkTaps", "published Resource group = No"},
		{"Microsoft.Network", "networkManagers", "absent from the move-support table"},
		{"Microsoft.ContainerInstance", "containerGroups", "published Resource group = No"},
	} {
		id := moveResourceID(srcRG, tc.provider, tc.typeName, "refused-resource")
		status, code := moveResourceRefused(t, srv, srcRG, dstRG, id)
		if code != "ResourceMoveNotSupported" {
			t.Errorf("%s/%s (%s): answered %q, want ResourceMoveNotSupported", tc.provider, tc.typeName, tc.why, code)
		}
		if status != http.StatusConflict {
			t.Errorf("%s/%s: answered status %d, want 409 Conflict", tc.provider, tc.typeName, status)
		}
	}
}

// TestMoveWithoutRepointingLeavesDanglingReferences is the negative control for
// the repointing pass. It re-homes a virtual network the way a hook that only
// re-keyed its own record would — no repointing — and shows the referrer is
// left naming an address nothing answers to. That dangling reference is what
// azureRepointMovedResource exists to prevent.
func TestMoveWithoutRepointingLeavesDanglingReferences(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "norepoint-src", "norepoint-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	const vnet, zone, link = "norepoint-vnet", "norepoint.test", "norepoint-link"
	vnetID := moveResourceID(srcRG, "Microsoft.Network", "virtualNetworks", vnet)
	status, body := moveARM(t, srv, http.MethodPut, vnetID,
		`{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.30.0.0/16"]}}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create virtual network: status %d: %s", status, body)
	}
	zoneID := moveResourceID(srcRG, "Microsoft.Network", "privateDnsZones", zone)
	status, body = moveARM(t, srv, http.MethodPut, zoneID, `{"location":"global"}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create private DNS zone: status %d: %s", status, body)
	}
	linkID := zoneID + "/virtualNetworkLinks/" + link
	status, body = moveARM(t, srv, http.MethodPut, linkID, fmt.Sprintf(
		`{"location":"global","properties":{"virtualNetwork":{"id":%q},"registrationEnabled":false}}`, vnetID))
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create virtual network link: status %d: %s", status, body)
	}

	// The move, minus the repointing pass: the hook re-keys its own record and
	// nothing else happens.
	movedVnetID := moveResourceID(dstRG, "Microsoft.Network", "virtualNetworks", vnet)
	hook, ok := resourceMoveHooks["microsoft.network/virtualnetworks"]
	if !ok {
		t.Fatal("the Microsoft.Network/virtualNetworks hook is not registered")
	}
	hook.move(vnetID, movedVnetID, dstRG)

	if status, _ := moveARM(t, srv, http.MethodGet, movedVnetID, ""); status != http.StatusOK {
		t.Fatal("the unrepointed control did not actually re-home the virtual network, so it proves nothing")
	}
	if got := vnetLinkTarget(t, srv, linkID); got == movedVnetID {
		t.Error("without the repointing pass the referrer already named the moved address; the control cannot detect a dangling reference")
	} else if got != vnetID {
		t.Errorf("the unrepointed referrer names %q, expected the stale source address %q", got, vnetID)
	}
}

// TestMoveContainerRegistryCarriesItsContent covers the interaction between the
// per-registry content keying and the move. A registry's manifests, blobs and
// uploads are keyed by its ARM resource ID — that is what keeps two registries
// from sharing repositories — so a move has to carry them with it: the login
// server is unchanged by the move, and a repository pushed before it must pull
// unchanged after it, with the admin password an operator already holds.
func TestMoveContainerRegistryCarriesItsContent(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "acr-move-src", "acr-move-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	const registry = "acrmovecontentreg"
	registryID := moveResourceID(srcRG, "Microsoft.ContainerRegistry", "registries", registry)
	status, body := moveARM(t, srv, http.MethodPut, registryID,
		`{"location":"eastus","sku":{"name":"Standard"},"properties":{"adminUserEnabled":true}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create registry: status %d: %s", status, body)
	}
	var created Registry
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode registry: %v", err)
	}
	user, passwords := moveACRAdminCredentials(t, srv, registryID)
	auth := acrBasicHeader(user, passwords[0])

	const repo = "moved/app"
	layer := "moved-registry-layer"
	digest := acrTestDigest([]byte(layer))
	acrPushBlob(t, srv, created, auth, repo, digest, layer)
	acrPushManifest(t, srv, created, auth, repo, "v1", acrIsolationManifest)

	moveResource(t, srv, srcRG, dstRG, registryID)

	movedID := moveResourceID(dstRG, "Microsoft.ContainerRegistry", "registries", registry)
	status, body = moveARM(t, srv, http.MethodGet, movedID, "")
	if status != http.StatusOK {
		t.Fatalf("the moved registry does not answer under the destination group: status %d: %s", status, body)
	}
	var moved Registry
	if err := json.Unmarshal(body, &moved); err != nil {
		t.Fatalf("decode moved registry: %v", err)
	}
	if moved.Properties.LoginServer != created.Properties.LoginServer {
		t.Fatalf("the move changed the registry's login server: %q != %q",
			moved.Properties.LoginServer, created.Properties.LoginServer)
	}

	// The admin credential an operator holds still authenticates, and the
	// content pushed before the move is served unchanged.
	movedUser, movedPasswords := moveACRAdminCredentials(t, srv, movedID)
	if movedUser != user || movedPasswords[0] != passwords[0] {
		t.Fatal("the move rotated the registry's admin credential")
	}
	resp, data := acrServe(t, srv, acrAuthorizedRequest(moved, auth, http.MethodGet, "/v2/"+repo+"/manifests/v1"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the manifest pushed before the move is not served after it: status %d: %s", resp.StatusCode, data)
	}
	resp, data = acrServe(t, srv, acrAuthorizedRequest(moved, auth, http.MethodGet, "/v2/"+repo+"/blobs/"+digest))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the blob pushed before the move is not served after it: status %d: %s", resp.StatusCode, data)
	}
	if tags := acrTagsList(t, srv, moved, auth, repo); len(tags) != 1 || tags[0] != "v1" {
		t.Fatalf("the moved registry's tag list lost the tag pushed before the move: %v", tags)
	}
	if repos := acrCatalog(t, srv, moved, auth); len(repos) != 1 || repos[0] != repo {
		t.Fatalf("the moved registry's catalog lost the repository pushed before the move: %v", repos)
	}
}

// moveACRAdminCredentials reads a registry's admin credential through the real
// listCredentials action over the ARM plane the move tests drive.
func moveACRAdminCredentials(t *testing.T, srv *sim.Server, registryID string) (string, []string) {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodPost, registryID+"/listCredentials", "")
	if status != http.StatusOK {
		t.Fatalf("listCredentials: status %d: %s", status, body)
	}
	var creds struct {
		Username  string `json:"username"`
		Passwords []struct {
			Value string `json:"value"`
		} `json:"passwords"`
	}
	if err := json.Unmarshal(body, &creds); err != nil {
		t.Fatalf("decode registry credentials: %v", err)
	}
	values := make([]string, 0, len(creds.Passwords))
	for _, password := range creds.Passwords {
		values = append(values, password.Value)
	}
	if len(values) != 2 {
		t.Fatalf("a registry serves two admin password slots, got %d", len(values))
	}
	return creds.Username, values
}

// TestMovePrivateEndpointFollowsThePrivateLinkResourceType covers the one
// Microsoft.Network type whose movability turns on the individual resource
// rather than on the type. Azure moves a private endpoint only when the
// private-link resource it connects to is one of the types it lists as
// supporting the move, and refuses every other one, so the simulator decides
// the same way through the real Resources_ValidateMoveResources operation.
//
// The endpoints are seeded into the store rather than created over HTTP
// because creating one requires a subnet, and realizing a subnet needs the
// Linux network-namespace fabric this host does not have; the move decision
// itself, which is what this test is about, runs the whole real dispatch.
func TestMovePrivateEndpointFollowsThePrivateLinkResourceType(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, dstRG = "pe-move-src", "pe-move-dst"
	moveEnsureRG(t, srv, srcRG)
	moveEnsureRG(t, srv, dstRG)

	seed := func(name, target string) string {
		id := moveResourceID(srcRG, "Microsoft.Network", "privateEndpoints", name)
		endpoint := PrivateEndpoint{}
		endpoint.ID = id
		endpoint.Name = name
		endpoint.Type = "Microsoft.Network/privateEndpoints"
		endpoint.Location = "eastus"
		if target != "" {
			endpoint.Properties.PrivateLinkServiceConnections = []PrivateLinkServiceConnection{{
				Name: "connection",
				Properties: PrivateLinkServiceConnectionProperties{
					PrivateLinkServiceID: target,
					GroupIDs:             []string{"sql"},
				},
			}}
		}
		azurePrivateEndpoints.Put(id, endpoint)
		return id
	}

	supportedTarget := moveResourceID(srcRG, "Microsoft.DocumentDB", "databaseAccounts", "pemovecosmos")
	supportedID := seed("pe-move-supported", supportedTarget)
	unsupportedTarget := moveResourceID(srcRG, "Microsoft.Storage", "storageAccounts", "pemovestorage")
	unsupportedID := seed("pe-move-unsupported", unsupportedTarget)
	unconnectedID := seed("pe-move-unconnected", "")

	// A private endpoint onto a private-link resource Azure lists moves.
	moveResource(t, srv, srcRG, dstRG, supportedID)
	movedID := moveResourceID(dstRG, "Microsoft.Network", "privateEndpoints", "pe-move-supported")
	if _, ok := azurePrivateEndpoints.Get(movedID); !ok {
		t.Fatal("the private endpoint onto a supported private-link resource did not move")
	}

	// Every other one is refused, exactly as Azure refuses it.
	for _, tc := range []struct{ what, id string }{
		{"a private endpoint onto an unsupported private-link resource", unsupportedID},
		{"a private endpoint connected to nothing", unconnectedID},
	} {
		status, code := moveResourceRefused(t, srv, srcRG, dstRG, tc.id)
		if code != "ResourceMoveNotSupported" {
			t.Errorf("%s: answered %q, want ResourceMoveNotSupported", tc.what, code)
		}
		if status != http.StatusConflict {
			t.Errorf("%s: answered status %d, want 409 Conflict", tc.what, status)
		}
	}
}

// TestMoveRefusesOccupiedDestinationIdentifier drives the check every move
// runs before it re-homes anything. A resource keeps its name across a move, so
// a destination group that already holds one of the same type under that name
// has no free identifier for the moved resource: performing the move would
// address two resources at one identifier and destroy the one already there.
//
// Both halves are asserted, because the refusal is only worth anything if the
// same move onto a free identifier still succeeds: the occupied destination is
// refused with the provider-validation conflict, the resource already there is
// untouched, the source resource is still where it was, and the move into an
// empty group goes through.
func TestMoveRefusesOccupiedDestinationIdentifier(t *testing.T) {
	srv := newMoveTestServer(t)
	const srcRG, occupiedRG, freeRG = "conflict-src", "conflict-occupied", "conflict-free"
	for _, group := range []string{srcRG, occupiedRG, freeRG} {
		moveEnsureRG(t, srv, group)
	}

	const workflow = "conflict-wf"
	definition := `{"location":"eastus","properties":{"definition":{"$schema":"https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#","contentVersion":"1.0.0.0","triggers":{},"actions":{},"outputs":{}},"parameters":{}}}`
	sourceID := moveResourceID(srcRG, "Microsoft.Logic", "workflows", workflow)
	occupiedID := moveResourceID(occupiedRG, "Microsoft.Logic", "workflows", workflow)
	for _, id := range []string{sourceID, occupiedID} {
		if status, body := moveARM(t, srv, http.MethodPut, id, definition); status != http.StatusOK && status != http.StatusCreated {
			t.Fatalf("create workflow %s: status %d: %s", id, status, body)
		}
	}
	occupantBefore := logicCallback(t, srv, occupiedID)

	payload := fmt.Sprintf(`{"resources":["%s"],"targetResourceGroup":"/subscriptions/%s/resourceGroups/%s"}`,
		sourceID, moveTestSubscription, occupiedRG)
	for _, action := range []string{"validateMoveResources", "moveResources"} {
		status, body := moveARM(t, srv, http.MethodPost,
			fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/%s", moveTestSubscription, srcRG, action), payload)
		if status != http.StatusConflict {
			t.Fatalf("%s onto an occupied identifier: status %d: %s", action, status, body)
		}
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Details []struct {
					Code    string `json:"code"`
					Target  string `json:"target"`
					Details []struct {
						Message string `json:"message"`
					} `json:"details"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode %s error: %v: %s", action, err, body)
		}
		if envelope.Error.Code != "ResourceMoveProviderValidationFailed" {
			t.Fatalf("%s error code = %q: %s", action, envelope.Error.Code, body)
		}
		if len(envelope.Error.Details) != 1 || envelope.Error.Details[0].Code != "CannotMoveResource" {
			t.Fatalf("%s error details = %s", action, body)
		}
		if got := envelope.Error.Details[0].Target; got != "Microsoft.Logic/workflows" {
			t.Fatalf("%s error target = %q", action, got)
		}
		if len(envelope.Error.Details[0].Details) != 1 ||
			!strings.Contains(envelope.Error.Details[0].Details[0].Message,
				"have the same name as a resource in the target resource group") {
			t.Fatalf("%s inner detail = %s", action, body)
		}
	}

	// Nothing moved and nothing was overwritten.
	if status, _ := moveARM(t, srv, http.MethodGet, sourceID, ""); status != http.StatusOK {
		t.Fatalf("the refused move removed the source resource: status %d", status)
	}
	if occupantAfter := logicCallback(t, srv, occupiedID); occupantAfter.Value != occupantBefore.Value {
		t.Fatalf("the refused move replaced the resource already at the destination:\n before %q\n after  %q",
			occupantBefore.Value, occupantAfter.Value)
	}

	// The same move into a group that does not hold the name goes through.
	moveResource(t, srv, srcRG, freeRG, sourceID)
	if status, _ := moveARM(t, srv, http.MethodGet, moveResourceID(freeRG, "Microsoft.Logic", "workflows", workflow), ""); status != http.StatusOK {
		t.Fatalf("the move onto a free identifier did not land: status %d", status)
	}
}

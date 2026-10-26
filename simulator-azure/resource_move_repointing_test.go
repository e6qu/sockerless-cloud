package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// These move whole simulators through the real Resources_MoveResources, because
// the move spans slices: the resource re-homes, its rows follow, its derived
// credential is not rotated, and every reference to it is re-pointed.

const moveTestSubscription = azureDefaultSubscriptionID
const moveTestARMHost = "127.0.0.1:4581"

// The whole simulator registers every store the repointing pass scans.
func newMoveTestServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulatorWithUI(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "disabled"}, false)
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	// Long-running operations complete in a goroutine; one still running when
	// this test ends would race the next test's store rebuild.
	t.Cleanup(bg.Await)
	return srv
}

// The token the simulator's own OAuth2 endpoint would issue.
func moveARMToken(t *testing.T) string {
	t.Helper()
	token, err := mintBootstrapSimJWT(defaultAzureTokenAudience,
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM token: %v", err)
	}
	return token
}

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

// Validate, then move, as a client does.
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

// The simulator serves no API Management gateway data plane, so the proof is
// that listSecrets — the Ocp-Apim-Subscription-Key value — is unchanged by the
// move and rotation still works afterwards.
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

	// Rotation still works after the move.
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

// A callback URL issued before a move must keep working: its signing key is
// pinned, and its address is the workflow's identifier on the service host,
// naming no resource group. The URL is compared whole. No endpoint here
// consumes callback URLs, so the proof is byte-for-byte equality, what a
// verifier compares.
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
	// Neither group appears in the address before or after the move.
	for _, group := range []string{srcRG, dstRG} {
		if strings.Contains(callbackAfter.BasePath, group) {
			t.Fatalf("the moved workflow advertises resource group %q in its accessEndpoint: %q", group, callbackAfter.BasePath)
		}
	}

	// Regenerating the access key still invalidates every callback URL.
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

// A document written before the move reads back after it through a request
// signed with the key captured before it; the connection string and the four
// master keys are unchanged.
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

	// The data plane still serves the document, presenting the key captured
	// before the move.
	if got := cosmosDataPlaneGet(t, srv, account, database, container, "order-1", keysBefore["primaryMasterKey"]); got != "order-1" {
		t.Fatalf("the document written before the move is not readable after it (got %q)", got)
	}

	// Rotation still works after the move.
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

// The account's own hostname and a master-key Authorization token, as a Cosmos
// client sends.
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

// A partner namespace's two keys derive from its resource ID and must keep
// authorizing after the move; its channels move with it. The simulator routes
// /api/events to topics and domains only, so the check calls
// eventGridKeyAuthorizes, which every publish runs, directly.
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

// A system topic names its source twice — `source` and `metricResourceId` — so
// moving the source re-points both, and moving the topic disturbs neither.
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

	moveResource(t, srv, srcRG, dstRG, accountID)
	movedAccountID := moveResourceID(dstRG, "Microsoft.Storage", "storageAccounts", account)
	if got := eventGridSystemTopicSource(t, srv, topicID); got != movedAccountID {
		t.Fatalf("the system topic still names %q as its source, want the moved account %q", got, movedAccountID)
	}

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

// Resources that do not move with a virtual network name it — an interface's
// IP configuration, a private DNS zone's link — and its subnets live beneath its
// ID; all must be re-pointed.
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

// A linked server names its geo-replication peer, a separate top-level
// resource, by ID in linkedRedisCacheId.
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

// Every type here is one Azure refuses to move (marked No, or unlisted), so the
// simulator answers ResourceMoveNotSupported; a hook would diverge in the
// permissive direction.
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

// The negative control: without the repointing pass the referrer names an
// address nothing answers to.
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

	// The move, minus the repointing pass.
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

// A registry's content keys by its resource ID, so the move carries it: a
// repository pushed before the move pulls unchanged after it, with the admin
// password held before.
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

// Azure moves a private endpoint only when its private-link resource is a type
// listed as supporting the move. The endpoints are seeded into the store,
// because creating one needs a realized subnet; the move decision runs the
// whole real dispatch.
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

	moveResource(t, srv, srcRG, dstRG, supportedID)
	movedID := moveResourceID(dstRG, "Microsoft.Network", "privateEndpoints", "pe-move-supported")
	if _, ok := azurePrivateEndpoints.Get(movedID); !ok {
		t.Fatal("the private endpoint onto a supported private-link resource did not move")
	}

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

// A resource keeps its name across a move, so a destination already holding
// one of that type and name is refused with the provider-validation conflict,
// leaving both untouched, while the same move into an empty group succeeds.
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

	moveResource(t, srv, srcRG, freeRG, sourceID)
	if status, _ := moveARM(t, srv, http.MethodGet, moveResourceID(freeRG, "Microsoft.Logic", "workflows", workflow), ""); status != http.StatusOK {
		t.Fatalf("the move onto a free identifier did not land: status %d", status)
	}
}

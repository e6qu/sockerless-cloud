package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// TestCosmosSharedKeyMatchesPublishedVector drives the canonicalisation with
// the worked example Microsoft publishes in "Access control on Azure Cosmos DB
// resources" under "Example Encoding" — verb GET, resource type "dbs",
// resource link "dbs/ToDoList", the date below and the key below produce the
// authorization string asserted here. Getting this string wrong is
// unobservable from inside the simulator (every client would simply fail), so
// it is pinned against the vector rather than against our own output.
func TestCosmosSharedKeyMatchesPublishedVector(t *testing.T) {
	const (
		key       = "dsZQi3KtZmCv1ljt3VNWNm7sQUF1y5rJfC6kv5JiwvW0EndXdDku/dkKBp8/ufDToSxLzR4y+O/0H/t4bQtVNw=="
		date      = "Thu, 27 Apr 2017 00:51:12 GMT"
		published = "type%3dmaster%26ver%3d1.0%26sig%3dc09PEVJrgp2uQRkr934kFbTqhByc7TVr3OHyqlu%2bc%2bc%3d"
	)

	resourceType, resourceLink := cosmosSignedResource("/dbs/ToDoList")
	if resourceType != "dbs" || resourceLink != "dbs/ToDoList" {
		t.Fatalf("resource of /dbs/ToDoList = %q, %q; want \"dbs\", \"dbs/ToDoList\"", resourceType, resourceLink)
	}

	stringToSign := cosmosStringToSign(http.MethodGet, resourceType, resourceLink, date)
	if want := "get\ndbs\ndbs/ToDoList\nthu, 27 apr 2017 00:51:12 gmt\n\n"; stringToSign != want {
		t.Fatalf("string to sign = %q, want %q", stringToSign, want)
	}

	tokenType, version, signature := cosmosParseAuthorization(published)
	if tokenType != "master" || version != "1.0" {
		t.Fatalf("published token = type %q ver %q; want master, 1.0", tokenType, version)
	}
	if got := cosmosSignPayload(t, key, stringToSign); got != signature {
		t.Fatalf("signature over the published payload = %q, want the published %q", got, signature)
	}
}

// TestCosmosSignedResourceCoversTheOperationKinds pins the two rules the
// service documents for the resource link — the resource's own link for an
// operation on one resource, its parent's link for a list, create or query
// over a set — across the paths the data plane serves.
func TestCosmosSignedResourceCoversTheOperationKinds(t *testing.T) {
	for _, c := range []struct {
		path         string
		resourceType string
		resourceLink string
	}{
		{"/dbs", "dbs", ""},
		{"/dbs/ToDoList", "dbs", "dbs/ToDoList"},
		{"/dbs/ToDoList/colls", "colls", "dbs/ToDoList"},
		{"/dbs/ToDoList/colls/Items", "colls", "dbs/ToDoList/colls/Items"},
		{"/dbs/ToDoList/colls/Items/docs", "docs", "dbs/ToDoList/colls/Items"},
		{"/dbs/ToDoList/colls/Items/docs/one", "docs", "dbs/ToDoList/colls/Items/docs/one"},
		{"/dbs/ToDoList/colls/Items/pkranges", "pkranges", "dbs/ToDoList/colls/Items"},
		{"/dbs/ToDoList/colls/Items/sprocs/add", "sprocs", "dbs/ToDoList/colls/Items/sprocs/add"},
		{"/offers", "offers", ""},
		{"/", "", ""},
	} {
		resourceType, resourceLink := cosmosSignedResource(c.path)
		if resourceType != c.resourceType || resourceLink != c.resourceLink {
			t.Errorf("resource of %s = %q, %q; want %q, %q", c.path, resourceType, resourceLink, c.resourceType, c.resourceLink)
		}
	}
}

// TestCosmosDataPlaneRefusesUnauthorizedRequests drives a whole simulator: an
// account is provisioned through Azure Resource Manager, its keys are read
// through listKeys, and each of them is put to a document operation. Every key
// the service accepts and every credential it refuses is asserted, so the
// verification cannot be satisfied by a request that carries no proof of the
// key at all.
func TestCosmosDataPlaneRefusesUnauthorizedRequests(t *testing.T) {
	srv := newMoveTestServer(t)
	const account = "authcosmos"
	keys := cosmosProvisionAccount(t, srv, account)

	// The read-write keys authorize a write, which is what a client does first.
	for name, key := range map[string]string{
		"primary":   keys.PrimaryMasterKey,
		"secondary": keys.SecondaryMasterKey,
	} {
		status, body := cosmosSignedRequest(t, srv, http.MethodPost, "/dbs", account, key, fmt.Sprintf(`{"id":"db-%s"}`, name))
		if status != http.StatusCreated && status != http.StatusOK {
			t.Fatalf("%s key create database: status %d: %s", name, status, body)
		}
	}

	// Every key authorizes a read.
	for name, key := range map[string]string{
		"primary":            keys.PrimaryMasterKey,
		"secondary":          keys.SecondaryMasterKey,
		"primary-readonly":   keys.PrimaryReadonlyMasterKey,
		"secondary-readonly": keys.SecondaryReadonlyMasterKey,
	} {
		status, body := cosmosSignedRequest(t, srv, http.MethodGet, "/dbs/db-primary", account, key, "")
		if status != http.StatusOK {
			t.Fatalf("%s key read database: status %d: %s", name, status, body)
		}
	}

	// A read-only key does not authorize a write: it is a different secret, so
	// the signature it produces verifies against neither read-write key.
	for name, key := range map[string]string{
		"primary-readonly":   keys.PrimaryReadonlyMasterKey,
		"secondary-readonly": keys.SecondaryReadonlyMasterKey,
	} {
		status, body := cosmosSignedRequest(t, srv, http.MethodPost, "/dbs", account, key, `{"id":"readonly-write"}`)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s key create database: status %d: %s", name, status, body)
		}
		if !strings.Contains(string(body), "MAC signature") {
			t.Fatalf("%s key write refusal message = %s", name, body)
		}
	}

	// A key that is not the account's does not authorize anything.
	other := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	status, body := cosmosSignedRequest(t, srv, http.MethodGet, "/dbs/db-primary", account, other, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("foreign key read: status %d: %s", status, body)
	}

	// A signature over a different resource than the one addressed does not
	// authorize the request, which is what binds the token to its operation.
	request := cosmosDataPlaneHTTPRequest(t, http.MethodGet, "/dbs/db-primary", account, "")
	date := time.Now().UTC().Format(http.TimeFormat)
	request.Header.Set("x-ms-date", date)
	request.Header.Set("Authorization", cosmosAuthorizationHeader(t, keys.PrimaryMasterKey,
		cosmosStringToSign(http.MethodGet, "dbs", "dbs/some-other-database", date)))
	if status, body := cosmosServe(srv, request); status != http.StatusUnauthorized {
		t.Fatalf("signature over another resource: status %d: %s", status, body)
	}

	// A request with no credential at all is refused, and so is one that names
	// a token type this account cannot verify.
	for name, header := range map[string]string{
		"absent":         "",
		"resource token": "type=resource&ver=1&sig=m32/00W65F8ADb3psljJ0g==;v0kQGihedau1pVGGQmuPgzlEcfsYDWSdfn2kyjDc1qF1aZfPHXzIS=;",
		"malformed":      "some-key-material",
	} {
		request := cosmosDataPlaneHTTPRequest(t, http.MethodGet, "/dbs/db-primary", account, "")
		request.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		if status, body := cosmosServe(srv, request); status != http.StatusUnauthorized {
			t.Fatalf("%s credential: status %d: %s", name, status, body)
		}
	}

	// A correctly signed request that omits the date the signature covers is
	// refused too.
	request = cosmosDataPlaneHTTPRequest(t, http.MethodGet, "/dbs/db-primary", account, "")
	request.Header.Set("Authorization", cosmosAuthorizationHeader(t, keys.PrimaryMasterKey,
		cosmosStringToSign(http.MethodGet, "dbs", "dbs/db-primary", time.Now().UTC().Format(http.TimeFormat))))
	if status, body := cosmosServe(srv, request); status != http.StatusUnauthorized {
		t.Fatalf("request without x-ms-date: status %d: %s", status, body)
	}
}

// TestCosmosDataPlaneHonoursRotatedKeys proves the keys the control plane
// rotates are the ones the data plane verifies: after regenerateKey the old
// primary key stops authorizing and the new one starts.
func TestCosmosDataPlaneHonoursRotatedKeys(t *testing.T) {
	srv := newMoveTestServer(t)
	const account = "rotatecosmos"
	before := cosmosProvisionAccount(t, srv, account)

	status, body := cosmosSignedRequest(t, srv, http.MethodPost, "/dbs", account, before.PrimaryMasterKey, `{"id":"rotatedb"}`)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create database before rotation: status %d: %s", status, body)
	}

	status, body = moveARM(t, srv, http.MethodPost, cosmosTestAccountPath(account)+"/regenerateKey", `{"keyKind":"primary"}`)
	if status != http.StatusOK && status != http.StatusAccepted && status != http.StatusNoContent {
		t.Fatalf("regenerateKey: status %d: %s", status, body)
	}

	after := cosmosAccountKeysOf(t, srv, account)
	if after.PrimaryMasterKey == before.PrimaryMasterKey {
		t.Fatal("regenerateKey did not change the primary key")
	}
	if status, body := cosmosSignedRequest(t, srv, http.MethodGet, "/dbs/rotatedb", account, before.PrimaryMasterKey, ""); status != http.StatusUnauthorized {
		t.Fatalf("rotated-away key read: status %d: %s", status, body)
	}
	if status, body := cosmosSignedRequest(t, srv, http.MethodGet, "/dbs/rotatedb", account, after.PrimaryMasterKey, ""); status != http.StatusOK {
		t.Fatalf("rotated-in key read: status %d: %s", status, body)
	}
}

// cosmosTestKeys mirrors the DatabaseAccountListKeysResult listKeys serves.
type cosmosTestKeys struct {
	PrimaryMasterKey           string `json:"primaryMasterKey"`
	SecondaryMasterKey         string `json:"secondaryMasterKey"`
	PrimaryReadonlyMasterKey   string `json:"primaryReadonlyMasterKey"`
	SecondaryReadonlyMasterKey string `json:"secondaryReadonlyMasterKey"`
}

func cosmosTestAccountPath(account string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/cosmos-auth-rg/providers/Microsoft.DocumentDB/databaseAccounts/%s",
		moveTestSubscription, account)
}

// cosmosProvisionAccount creates the account through Azure Resource Manager and
// returns the keys listKeys serves for it — the same two calls a client makes
// before it touches the data plane.
func cosmosProvisionAccount(t *testing.T, srv *sim.Server, account string) cosmosTestKeys {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodPut, cosmosTestAccountPath(account),
		`{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard"}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create Cosmos account: status %d: %s", status, body)
	}
	return cosmosAccountKeysOf(t, srv, account)
}

func cosmosAccountKeysOf(t *testing.T, srv *sim.Server, account string) cosmosTestKeys {
	t.Helper()
	status, body := moveARM(t, srv, http.MethodPost, cosmosTestAccountPath(account)+"/listKeys", "")
	if status != http.StatusOK {
		t.Fatalf("listKeys: status %d: %s", status, body)
	}
	var keys cosmosTestKeys
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("decode listKeys: %v: %s", err, body)
	}
	if keys.PrimaryMasterKey == "" {
		t.Fatalf("listKeys served no primary key: %s", body)
	}
	return keys
}

// cosmosDataPlaneHTTPRequest addresses an account's document endpoint the way a
// client does — the account's own hostname, which is how the service knows
// whose keys sign the request.
func cosmosDataPlaneHTTPRequest(t *testing.T, method, path, account, body string) *http.Request {
	t.Helper()
	host := account + ".documents.localhost"
	request := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	request.Host = host
	request.Header.Set("x-ms-version", "2018-12-31")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

// cosmosAuthorizationHeader builds the header a client sends: the token
// URL-encoded whole, as every documented client encodes it.
func cosmosAuthorizationHeader(t *testing.T, key, stringToSign string) string {
	t.Helper()
	return url.QueryEscape("type=master&ver=1.0&sig=" + cosmosSignPayload(t, key, stringToSign))
}

// cosmosSignPayload computes the base64 HMAC-SHA256 of a payload under a
// base64-encoded account key, which is the signature a client puts in its
// token. It is written out here rather than taken from the server so the
// server's verification is checked against an independent construction.
func cosmosSignPayload(t *testing.T, key, payload string) string {
	t.Helper()
	material, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("decode account key: %v", err)
	}
	mac := hmac.New(sha256.New, material)
	if _, err := mac.Write([]byte(payload)); err != nil {
		t.Fatalf("sign payload: %v", err)
	}
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// cosmosSignedRequest sends one data-plane request signed with a key, deriving
// the resource type and link from the path exactly as a client does.
func cosmosSignedRequest(t *testing.T, srv *sim.Server, method, path, account, key, body string) (int, []byte) {
	t.Helper()
	request := cosmosDataPlaneHTTPRequest(t, method, path, account, body)
	date := time.Now().UTC().Format(http.TimeFormat)
	request.Header.Set("x-ms-date", date)
	resourceType, resourceLink := cosmosClientResource(path)
	request.Header.Set("Authorization", cosmosAuthorizationHeader(t, key,
		cosmosStringToSign(method, resourceType, resourceLink, date)))
	return cosmosServe(srv, request)
}

// cosmosClientResource is the client side of the resource-link rule: an
// operation over a set of resources signs the parent's link, one on a single
// resource signs its own.
func cosmosClientResource(path string) (string, string) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	switch len(segments) % 2 {
	case 1:
		return segments[len(segments)-1], strings.Join(segments[:len(segments)-1], "/")
	default:
		return segments[len(segments)-2], strings.Join(segments, "/")
	}
}

func cosmosServe(srv *sim.Server, request *http.Request) (int, []byte) {
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	return recorder.Result().StatusCode, recorder.Body.Bytes()
}

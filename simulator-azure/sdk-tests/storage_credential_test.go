package azure_sdk_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
)

// The storage data plane authorizes every request, so this suite's clients
// present the account's real key.
//
// Twenty-three client constructions across six files built with no credential
// at all, and the raw-HTTP helpers sent nothing either — which worked only
// because the plane verified nothing. Converting each construction would have
// meant seven different credential types (azblob, container, blob, blockblob,
// fileservice, file, directory each take their own) for a property that
// belongs to none of them: every request the suite makes flows through
// storageSDKTransport, and "a client holding the account key" is what that
// transport represents. So the signature is applied there, and by the raw
// helpers, to any request that does not already carry a credential.
//
// The accounts these tests address are provisioned on first use through the
// real Azure Resource Manager API — the harness acting as the operator who
// creates an account before handing its coordinates to a workload — and the
// key is the one listKeys serves. Nothing here reads simulator internals.
//
// A request that already carries a credential is left alone. The App Service
// backup tests sign with azblob's own SharedKeyCredential and GetSASURL, and
// those stay the independent check on the simulator's canonicalization: this
// file and the simulator could agree with each other and both be wrong, and
// Microsoft's implementation is what says they are not.

var (
	storageCredMu       sync.Mutex
	storageCredKeys     = map[string]string{}
	storageCredResGroup = "sdk-storage-cred-rg"
)

// storageAccountPrimaryKey returns key1 for an account, provisioning the
// account through Azure Resource Manager when the subscription does not hold
// it yet. It returns "" only when provisioning itself fails, which the caller
// surfaces as an unsigned request the data plane refuses.
func storageAccountPrimaryKey(account string) string {
	storageCredMu.Lock()
	defer storageCredMu.Unlock()
	if key, ok := storageCredKeys[account]; ok {
		return key
	}
	accounts, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	if err != nil {
		return ""
	}
	group := storageCredAccountGroup(accounts, account)
	if group == "" {
		group = storageCredResGroup
		if !storageCredEnsureGroup(group) {
			return ""
		}
		poller, err := accounts.BeginCreate(ctx, group, account, armstorage.AccountCreateParameters{
			Kind:     to.Ptr(armstorage.KindStorageV2),
			SKU:      &armstorage.SKU{Name: to.Ptr(armstorage.SKUNameStandardLRS)},
			Location: to.Ptr("eastus"),
		}, nil)
		if err != nil {
			return ""
		}
		if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			return ""
		}
	}
	keys, err := accounts.ListKeys(ctx, group, account, nil)
	if err != nil {
		return ""
	}
	for _, k := range keys.Keys {
		if k != nil && k.KeyName != nil && *k.KeyName == "key1" && k.Value != nil {
			storageCredKeys[account] = *k.Value
			return *k.Value
		}
	}
	return ""
}

// storageCredAccountGroup returns the resource group already holding an
// account, or "". An account name is a hostname, so at most one holds it.
func storageCredAccountGroup(accounts *armstorage.AccountsClient, account string) string {
	pager := accounts.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return ""
		}
		for _, held := range page.Value {
			if held == nil || held.Name == nil || held.ID == nil || !strings.EqualFold(*held.Name, account) {
				continue
			}
			segments := strings.Split(*held.ID, "/")
			for i, segment := range segments {
				if strings.EqualFold(segment, "resourceGroups") && i+1 < len(segments) {
					return segments[i+1]
				}
			}
		}
	}
	return ""
}

func storageCredEnsureGroup(name string) bool {
	body := strings.NewReader(`{"location":"eastus"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+name+"?api-version=2023-07-01", body)
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 300
}

// storageAccountPrimaryKeyAt provisions an account on another simulator — the
// persistence tests boot their own, at their own endpoint — and returns key1
// from that simulator's listKeys. Raw Azure Resource Manager calls, so the
// flow is the operator's regardless of which instance serves it.
func storageAccountPrimaryKeyAt(endpoint, account string) string {
	storageCredMu.Lock()
	defer storageCredMu.Unlock()
	cacheKey := endpoint + "|" + account
	if key, ok := storageCredKeys[cacheKey]; ok {
		return key
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"test-client-id"},
		"client_secret": {"test-client-secret"},
		"scope":         {"https://management.azure.com/.default"},
	}
	tokenResp, err := http.PostForm(endpoint+"/"+simTenantID+"/oauth2/v2.0/token", form)
	if err != nil {
		return ""
	}
	defer tokenResp.Body.Close()
	var minted struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&minted); err != nil || minted.AccessToken == "" {
		return ""
	}
	bearer := "Bearer " + minted.AccessToken
	arm := func(method, path, body string) int {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint+path, reader)
		if err != nil {
			return 0
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	base := "/subscriptions/" + subscriptionID + "/resourceGroups/" + storageCredResGroup
	if code := arm(http.MethodPut, base+"?api-version=2023-07-01", `{"location":"eastus"}`); code >= 300 {
		return ""
	}
	acct := base + "/providers/Microsoft.Storage/storageAccounts/" + account
	if code := arm(http.MethodPut, acct+"?api-version=2023-05-01",
		`{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"}}`); code >= 300 {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+acct+"/listKeys?api-version=2023-05-01", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var listed struct {
		Keys []struct {
			KeyName string `json:"keyName"`
			Value   string `json:"value"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		return ""
	}
	for _, k := range listed.Keys {
		if k.KeyName == "key1" && k.Value != "" {
			storageCredKeys[cacheKey] = k.Value
			return k.Value
		}
	}
	return ""
}

// storageSignSharedKeyAt signs like storageSignSharedKey with the key of the
// simulator instance at endpoint.
func storageSignSharedKeyAt(req *http.Request, endpoint, host string) {
	if req.Header.Get("Authorization") != "" || req.URL.Query().Get("sig") != "" {
		return
	}
	hostname, _, found := strings.Cut(host, ":")
	if !found {
		hostname = host
	}
	labels := strings.Split(hostname, ".")
	if len(labels) < 2 {
		return
	}
	account, service := labels[0], labels[1]
	switch service {
	case "blob", "file", "queue", "table":
	default:
		return
	}
	key := storageAccountPrimaryKeyAt(endpoint, account)
	if key == "" {
		return
	}
	material, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return
	}
	if req.Header.Get("x-ms-date") == "" {
		req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	}
	stringToSign := storageBlobStringToSign(req, account)
	if service == "table" {
		stringToSign = storageTableStringToSign(req, account)
	}
	mac := hmac.New(sha256.New, material)
	_, _ = mac.Write([]byte(stringToSign))
	req.Header.Set("Authorization",
		"SharedKey "+account+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

// storageSignSharedKey gives a storage data-plane request the Shared Key
// credential a client holding the account key sends. A request that already
// carries a credential — an Authorization header or a SAS — is left untouched.
//
// The canonicalization is written out here rather than borrowed from the
// simulator: a harness signing with the server's own function would agree with
// it by construction and keep agreeing while both were wrong.
func storageSignSharedKey(req *http.Request, host string) {
	if req.Header.Get("Authorization") != "" || req.URL.Query().Get("sig") != "" {
		return
	}
	hostname, _, found := strings.Cut(host, ":")
	if !found {
		hostname = host
	}
	labels := strings.Split(hostname, ".")
	if len(labels) < 2 {
		return
	}
	account, service := labels[0], labels[1]
	switch service {
	case "blob", "file", "queue", "table":
	default:
		return
	}
	key := storageAccountPrimaryKey(account)
	if key == "" {
		return
	}
	material, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return
	}
	if req.Header.Get("x-ms-date") == "" {
		req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	}
	stringToSign := storageBlobStringToSign(req, account)
	if service == "table" {
		// The Table service signs a different, shorter string than the other
		// three: the verb, two content headers and the date, then the resource
		// with only a `comp` parameter — no canonicalized x-ms headers at all.
		stringToSign = storageTableStringToSign(req, account)
	}
	mac := hmac.New(sha256.New, material)
	_, _ = mac.Write([]byte(stringToSign))
	req.Header.Set("Authorization",
		"SharedKey "+account+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

// storageBlobStringToSign renders "Authorize with Shared Key" for the Blob,
// Queue and File services.
func storageBlobStringToSign(req *http.Request, account string) string {
	header := func(name string) string { return req.Header.Get(name) }
	contentLength := ""
	if req.ContentLength > 0 {
		contentLength = strconv.FormatInt(req.ContentLength, 10)
	}
	var canonicalHeaders []string
	for name, values := range req.Header {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-ms-") {
			continue
		}
		canonicalHeaders = append(canonicalHeaders, lower+":"+strings.TrimSpace(strings.Join(values, ",")))
	}
	sort.Strings(canonicalHeaders)

	query := req.URL.Query()
	names := make([]string, 0, len(query))
	for name := range query {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	resource := "/" + account + req.URL.EscapedPath()
	for _, name := range names {
		values := storageQueryValues(query, name)
		sort.Strings(values)
		resource += "\n" + name + ":" + strings.Join(values, ",")
	}

	return strings.Join([]string{
		req.Method,
		header("Content-Encoding"),
		header("Content-Language"),
		contentLength,
		header("Content-MD5"),
		header("Content-Type"),
		header("Date"),
		header("If-Modified-Since"),
		header("If-Match"),
		header("If-None-Match"),
		header("If-Unmodified-Since"),
		header("Range"),
		strings.Join(canonicalHeaders, "\n"),
	}, "\n") + "\n" + resource
}

// storageTableStringToSign renders the Table service's Shared Key string: the
// verb, Content-MD5, Content-Type, the date, and the resource carrying only a
// `comp` parameter.
func storageTableStringToSign(req *http.Request, account string) string {
	date := req.Header.Get("Date")
	if xms := req.Header.Get("x-ms-date"); xms != "" {
		date = xms
	}
	resource := "/" + account + req.URL.EscapedPath()
	if comp := req.URL.Query().Get("comp"); comp != "" {
		resource += "?comp=" + comp
	}
	return strings.Join([]string{
		req.Method,
		req.Header.Get("Content-MD5"),
		req.Header.Get("Content-Type"),
		date,
	}, "\n") + "\n" + resource
}

// storageSignSharedKeyPathStyle signs a path-style storage request — the
// Azurite-compatible `/{account}/{container}/…` form — over the path the
// request actually carries, which is what a real client signs for a path-style
// endpoint. It replaces whatever Authorization the request held, because a
// path-style storage request authenticates with the account key, not with an
// ARM bearer that happens to be on the wire.
func storageSignSharedKeyPathStyleService(req *http.Request, service, account string) {
	key := storageAccountPrimaryKey(account)
	if key == "" {
		return
	}
	material, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return
	}
	if req.Header.Get("x-ms-date") == "" {
		req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	}
	req.Header.Del("Authorization")
	stringToSign := storageBlobStringToSign(req, account)
	if service == "table" {
		stringToSign = storageTableStringToSign(req, account)
	}
	mac := hmac.New(sha256.New, material)
	_, _ = mac.Write([]byte(stringToSign))
	req.Header.Set("Authorization",
		"SharedKey "+account+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

func storageQueryValues(query url.Values, lowered string) []string {
	for name, values := range query {
		if strings.EqualFold(name, lowered) {
			return append([]string(nil), values...)
		}
	}
	return nil
}

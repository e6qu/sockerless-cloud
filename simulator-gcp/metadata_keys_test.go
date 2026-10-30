package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// A Compute Engine instance reads the predefined instance keys from its own
// resource, each in the format the metadata server answers it in.
func TestMetadataServesTheInstanceKeys(t *testing.T) {
	get := metadataTreeServer(t)
	number := gcpTestProjectNumber(t)
	for path, want := range map[string]string{
		"/computeMetadata/v1/instance/cpu-platform":       gcpZoneCPUPlatform,
		"/computeMetadata/v1/instance/description":        "reads its own metadata",
		"/computeMetadata/v1/instance/machine-type":       "projects/" + number + "/machineTypes/e2-small",
		"/computeMetadata/v1/instance/image":              "projects/debian-cloud/global/images/debian-12-bookworm-v20251001",
		"/computeMetadata/v1/instance/maintenance-event":  "NONE",
		"/computeMetadata/v1/instance/hostname":           "metadata-reader.us-central1-a.c." + gcpTestProjectID + ".internal",
		"/computeMetadata/v1/instance/tags":               `["http-server","db-client"]`,
		"/computeMetadata/v1/instance/tags?alt=text":      "http-server\ndb-client",
		"/computeMetadata/v1/instance/name?alt=json":      `"metadata-reader"`,
		"/computeMetadata/v1/instance/id?alt=json":        "4815162342",
		"/computeMetadata/v1/instance/disks/":             "0/\n1/\n",
		"/computeMetadata/v1/instance/disks/0/":           "device-name\nindex\ninterface\nmode\ntype\n",
		"/computeMetadata/v1/instance/disks/1/interface":  "NVME",
		"/computeMetadata/v1/instance/disks/1/mode":       "READ_ONLY",
		"/computeMetadata/v1/instance/disks/?alt=json":    `["0/","1/"]`,
		"/computeMetadata/v1/project/project-id?alt=json": `"` + gcpTestProjectID + `"`,
		"/computeMetadata/v1/instance/disks/?recursive=true": `[{"deviceName":"boot","index":0,"interface":"SCSI","mode":"READ_WRITE","type":"PERSISTENT"},` +
			`{"deviceName":"data","index":1,"interface":"NVME","mode":"READ_ONLY","type":"PERSISTENT"}]`,
		"/computeMetadata/v1/instance/disks/?recursive=true&alt=text": "0/device-name boot\n0/index 0\n0/interface SCSI\n0/mode READ_WRITE\n0/type PERSISTENT\n" +
			"1/device-name data\n1/index 1\n1/interface NVME\n1/mode READ_ONLY\n1/type PERSISTENT\n",
		"/computeMetadata/v1/instance/service-accounts/default/scopes": strings.Join(metadataReaderScopes, "\n"),
	} {
		rec := get(path)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	if rec := get("/computeMetadata/v1/instance/tags"); rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("tags answered as %q, want JSON by default", rec.Header().Get("Content-Type"))
	}
	if rec := get("/computeMetadata/v1/instance/name?alt=xml"); rec.Code != http.StatusBadRequest {
		t.Errorf("alt=xml answered %d, want 400", rec.Code)
	}
	if rec := get("/computeMetadata/v1/instance/region"); rec.Code != http.StatusNotFound {
		t.Errorf("a Compute Engine instance read a region: %d %q", rec.Code, rec.Body.String())
	}
}

// accessTokenScopes reads the scope claim of the token a token read minted.
func accessTokenScopes(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var answer struct {
		AccessToken string `json:"access_token"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &answer) != nil {
		t.Fatalf("token read answered %d %q", rec.Code, rec.Body.String())
	}
	claims, err := verifiedAccessTokenClaims(answer.AccessToken)
	if err != nil {
		t.Fatalf("the token does not verify: %v", err)
	}
	return strings.Fields(claims.Scope)
}

// A token carries the scopes the instance's service account holds, and only
// the subset its `scopes` parameter names when the read enforces scopes.
func TestMetadataTokenScopes(t *testing.T) {
	get := metadataTreeServer(t)
	const token = "/computeMetadata/v1/instance/service-accounts/default/token"
	readOnly := metadataReaderScopes[0]
	if got := accessTokenScopes(t, get(token)); !reflect.DeepEqual(got, metadataReaderScopes) {
		t.Fatalf("token scopes = %v, want the instance's %v", got, metadataReaderScopes)
	}
	if got := accessTokenScopes(t, get(token+"?scopes="+readOnly)); !reflect.DeepEqual(got, metadataReaderScopes) {
		t.Fatalf("scopes without enforce_scopes = %v, want the instance's %v", got, metadataReaderScopes)
	}
	for _, enforce := range []string{"true", "1", "yes", "y"} {
		if got := accessTokenScopes(t, get(token+"?enforce_scopes="+enforce+"&scopes="+readOnly)); !reflect.DeepEqual(got, []string{readOnly}) {
			t.Fatalf("enforce_scopes=%s scopes = %v, want [%s]", enforce, got, readOnly)
		}
	}
	for _, refused := range []string{
		token + "?enforce_scopes=false&scopes=" + readOnly,
		token + "?enforce_scopes=n",
		token + "?enforce_scopes=true&scopes=" + cloudPlatformScope,
	} {
		if rec := get(refused); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d %q, want 400", refused, rec.Code, rec.Body.String())
		}
	}

	gcpInstances.Update(metadataReaderSelfLink, func(inst *ComputeInstance) {
		inst.Metadata.Items = append(inst.Metadata.Items, ComputeInstanceMetadataItem{Key: "enable-access-token-enforce-scopes", Value: "true"})
	})
	if got := accessTokenScopes(t, get(token+"?scopes="+readOnly)); !reflect.DeepEqual(got, []string{readOnly}) {
		t.Fatalf("with enable-access-token-enforce-scopes the scopes = %v, want [%s]", got, readOnly)
	}
}

func identityClaims(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("identity read answered %d %q", rec.Code, rec.Body.String())
	}
	parts := strings.Split(rec.Body.String(), ".")
	if len(parts) != 3 {
		t.Fatalf("identity token %q is no JWT", rec.Body.String())
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// A full-format identity token describes the instance and its project, and
// with licenses=TRUE the codes of the licences its boot image carries.
func TestMetadataIdentityFormats(t *testing.T) {
	get := metadataTreeServer(t)
	const identity = "/computeMetadata/v1/instance/service-accounts/default/identity?audience=https://verifier.example"
	if claims := identityClaims(t, get(identity)); claims["google"] != nil {
		t.Fatalf("a standard token carries %v", claims["google"])
	}

	licence := "projects/" + gcpTestProjectID + "/global/licenses/reader-licence"
	gcpComputeLicenses.Put(licence, map[string]any{"name": "reader-licence", "licenseCode": "5551234", "selfLink": computeSelfLink(licence)})
	image := gcpImageLink(gcpTestProjectID, "licensed")
	gcpComputeImages.Put(image, map[string]any{
		"name": "licensed", "selfLink": computeSelfLink(image),
		"rawDisk":  map[string]any{"source": "mirror.gcr.io/library/debian:12"},
		"licenses": []any{computeSelfLink(licence)},
	})
	gcpInstances.Update(metadataReaderSelfLink, func(inst *ComputeInstance) {
		inst.Disks[0].InitializeParams = map[string]any{"sourceImage": image}
	})

	claims := identityClaims(t, get(identity+"&format=full&licenses=TRUE"))
	engine, _ := claims["google"].(map[string]any)["compute_engine"].(map[string]any)
	want := map[string]any{
		"project_id":                  gcpTestProjectID,
		"zone":                        "us-central1-a",
		"instance_id":                 "4815162342",
		"instance_name":               "metadata-reader",
		"instance_creation_timestamp": float64(1793354400),
		"license_id":                  []any{"5551234"},
	}
	for name, value := range want {
		if !reflect.DeepEqual(engine[name], value) {
			t.Errorf("compute_engine.%s = %v, want %v (claims %v)", name, engine[name], value, engine)
		}
	}
	if _, ok := engine["project_number"].(float64); !ok {
		t.Errorf("compute_engine.project_number = %v, want a number", engine["project_number"])
	}
	full := identityClaims(t, get(identity+"&format=full"))
	if _, listed := full["google"].(map[string]any)["compute_engine"].(map[string]any)["license_id"]; listed {
		t.Error("a full token without licenses=TRUE carries license_id")
	}
	for _, refused := range []string{identity + "&format=compact", identity + "&format=full&licenses=maybe"} {
		if rec := get(refused); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", refused, rec.Code)
		}
	}
}

// A process inside the instance writes, lists and deletes guest attributes
// once enable-guest-attributes is TRUE, and instances.getGuestAttributes
// reads what it wrote.
func TestMetadataGuestAttributes(t *testing.T) {
	srv := metadataReaderServer(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Metadata-Flavor", "Google")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	const base = "/computeMetadata/v1/instance/guest-attributes/"
	if rec := call(http.MethodPut, base+"startup/status", "done"); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %q", rec.Code, rec.Body.String())
	}
	call(http.MethodPut, base+"startup/phase", "2")
	for path, want := range map[string]string{
		base:                     "startup/\n",
		base + "startup/":        "phase\nstatus\n",
		base + "startup/status":  "done",
		base + "?recursive=true": `{"startup":{"phase":"2","status":"done"}}`,
	} {
		if rec := call(http.MethodGet, path, ""); rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	if listing := call(http.MethodGet, "/computeMetadata/v1/instance/", "").Body.String(); !strings.Contains(listing, "\nguest-attributes/\n") {
		t.Errorf("the instance listing names no guest-attributes/: %q", listing)
	}

	inst, _ := gcpInstances.Get(metadataReaderSelfLink)
	for query, want := range map[string]string{
		"":                           `{"kind":"compute#guestAttributes","queryPath":"","queryValue":{"items":[{"key":"phase","namespace":"startup","value":"2"},{"key":"status","namespace":"startup","value":"done"}]}`,
		"?queryPath=startup/":        `"queryValue":{"items":[{"key":"phase","namespace":"startup","value":"2"},{"key":"status","namespace":"startup","value":"done"}]}`,
		"?queryPath=startup/status":  `"queryValue":{"items":[{"key":"status","namespace":"startup","value":"done"}]}`,
		"?variableKey=startup/phase": `"variableValue":"2"`,
	} {
		rec := httptest.NewRecorder()
		handleGetGuestAttributes(rec, httptest.NewRequest(http.MethodGet, "/getGuestAttributes"+query, nil), inst)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("getGuestAttributes%s = %d %s, want it to carry %s", query, rec.Code, rec.Body.String(), want)
		}
	}
	missing := httptest.NewRecorder()
	handleGetGuestAttributes(missing, httptest.NewRequest(http.MethodGet, "/getGuestAttributes?queryPath=absent/", nil), inst)
	if missing.Code != http.StatusNotFound {
		t.Errorf("getGuestAttributes of an absent namespace = %d, want 404", missing.Code)
	}

	for _, want := range []int{http.StatusOK, http.StatusNotFound} {
		if rec := call(http.MethodDelete, base+"startup/status", ""); rec.Code != want {
			t.Errorf("DELETE startup/status = %d, want %d", rec.Code, want)
		}
	}
	if rec := call(http.MethodGet, base+"startup/", ""); rec.Body.String() != "phase\n" {
		t.Errorf("after the DELETE the namespace lists %q", rec.Body.String())
	}
	if rec := call(http.MethodPut, base+"startup/"+strings.Repeat("k", 129), "v"); rec.Code != http.StatusBadRequest {
		t.Errorf("a 129-byte key answered %d, want 400", rec.Code)
	}
	if rec := call(http.MethodPut, base+"start.up/key", "v"); rec.Code != http.StatusBadRequest {
		t.Errorf("a namespace with a dot answered %d, want 400", rec.Code)
	}
	if rec := call(http.MethodPut, base+"startup/big", strings.Repeat("v", guestAttributeValueLimit+1)); rec.Code != http.StatusBadRequest {
		t.Errorf("a value over 256 KiB answered %d, want 400", rec.Code)
	}

	gcpInstances.Update(metadataReaderSelfLink, func(inst *ComputeInstance) { inst.Metadata.Items = nil })
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if rec := call(method, base+"startup/phase", "3"); rec.Code != http.StatusForbidden {
			t.Errorf("%s with guest attributes disabled = %d %q, want 403", method, rec.Code, rec.Body.String())
		}
	}
}

// metadataWorkloadRead reads the metadata tree as a workload that is no
// Compute Engine instance, from an address no instance holds.
func metadataWorkloadRead(t *testing.T, workload gceMetadataWorkload, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "198.51.100.7:4000"
	req.Header.Set("Metadata-Flavor", "Google")
	req = req.WithContext(context.WithValue(req.Context(), gceMetadataWorkloadKey{}, workload))
	rec := httptest.NewRecorder()
	serveMetadataPath(rec, req, strings.TrimPrefix(path, "/computeMetadata/"))
	return rec
}

// A Cloud Run container reads the instance keys its container contract
// states and finds no name or hostname; a Cloud Run worker pool on a VPC
// network reads its interface's address; a Cloud Build step reads no
// instance identity at all.
func TestMetadataServesWhatEachServiceDocuments(t *testing.T) {
	metadataReaderServer(t)
	number := gcpTestProjectNumber(t)
	run := gceMetadataWorkload{project: gcpTestProjectID, cloudRunInstance: "0087244a8c1d", region: "europe-west1"}
	for path, want := range map[string]string{
		"/computeMetadata/v1/instance/id":                              "0087244a8c1d",
		"/computeMetadata/v1/instance/region":                          "projects/" + number + "/regions/europe-west1",
		"/computeMetadata/v1/instance/zone":                            "projects/" + number + "/zones/europe-west1-1",
		"/computeMetadata/v1/instance/network-interfaces/":             "",
		"/computeMetadata/v1/instance/service-accounts/default/scopes": cloudPlatformScope,
	} {
		if rec := metadataWorkloadRead(t, run, path); rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("Cloud Run GET %s = %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	for _, key := range []string{"name", "hostname", "machine-type", "tags", "disks/", "guest-attributes/"} {
		path := "/computeMetadata/v1/instance/" + key
		if rec := metadataWorkloadRead(t, run, path); rec.Code != http.StatusNotFound {
			t.Errorf("Cloud Run GET %s = %d %q, want 404: its contract states no such key", path, rec.Code, rec.Body.String())
		}
	}
	build := gceMetadataWorkload{project: gcpTestProjectID}
	for _, key := range []string{"name", "hostname", "zone", "id", "region", "machine-type"} {
		path := "/computeMetadata/v1/instance/" + key
		if rec := metadataWorkloadRead(t, build, path); rec.Code != http.StatusNotFound {
			t.Errorf("Cloud Build GET %s = %d %q, want 404: Cloud Build documents no such key", path, rec.Code, rec.Body.String())
		}
	}
	if rec := metadataWorkloadRead(t, build, "/computeMetadata/v1/project/project-id"); rec.Body.String() != gcpTestProjectID {
		t.Errorf("Cloud Build reads project %q", rec.Body.String())
	}

	pool := run
	pool.vpcAddress = "10.124.0.5"
	for path, want := range map[string]string{
		"/computeMetadata/v1/instance/network-interfaces/":     "0/\n",
		"/computeMetadata/v1/instance/network-interfaces/0/":   "ip\n",
		"/computeMetadata/v1/instance/network-interfaces/0/ip": "10.124.0.5",
	} {
		if rec := metadataWorkloadRead(t, pool, path); rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("worker pool GET %s = %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}

	const poolName = "projects/" + gcpTestProjectID + "/locations/europe-west1/workerPools/vpc-pool"
	crv2WorkerPools.Put(poolName, WorkerPoolV2{Name: poolName, Template: &WorkerPoolRevisionTemplate{
		VpcAccess: &VpcAccess{NetworkInterfaces: []NetworkInterface{{Network: "default"}}},
	}})
	if !cloudRunDirectVPC(map[string]string{cloudRunWorkerPoolLabel: poolName}) {
		t.Error("a worker pool with Direct VPC is not placed on its subnetwork")
	}
	const plainPool = "projects/" + gcpTestProjectID + "/locations/europe-west1/workerPools/plain-pool"
	crv2WorkerPools.Put(plainPool, WorkerPoolV2{Name: plainPool, Template: &WorkerPoolRevisionTemplate{}})
	if cloudRunDirectVPC(map[string]string{cloudRunWorkerPoolLabel: plainPool}) {
		t.Error("a worker pool without VPC access is placed on a subnetwork")
	}
}

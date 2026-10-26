package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The image's entrypoint starts the host on PORT, which loads the app from
// /home/site/wwwroot and its keys from the store AzureWebJobsSecretStorageType
// selects.
type functionsHostStack struct {
	Runtime        string // FUNCTIONS_WORKER_RUNTIME and the catalogue's stack value, "node"
	RuntimeDisplay string // "Node.js"
	Version        string // "22"
	LinuxFx        string // "Node|22"
	EndOfLife      string
	Image          string
}

// Each on the Functions host image Microsoft publishes under
// mcr.microsoft.com/azure-functions, pinned to the build the floating
// 4-<runtime><version>-appservice tag names.
var functionsHostStacks = []functionsHostStack{
	{
		Runtime: "node", RuntimeDisplay: "Node.js", Version: "22", LinuxFx: "Node|22",
		EndOfLife: "2027-04-30T00:00:00Z",
		Image:     "mcr.microsoft.com/azure-functions/node:4.1054.250-4-node22-appservice",
	},
}

func functionsHostStackNames() []string {
	out := make([]string, 0, len(functionsHostStacks))
	for _, s := range functionsHostStacks {
		out = append(out, s.LinuxFx)
	}
	return out
}

func siteFunctionsHostStack(site *Site) (functionsHostStack, bool) {
	fx := strings.TrimSpace(siteRuntimeStack(site))
	if fx == "" || !siteIsFunctionApp(site) {
		return functionsHostStack{}, false
	}
	for _, s := range functionsHostStacks {
		if strings.EqualFold(fx, s.LinuxFx) {
			return s, true
		}
	}
	return functionsHostStack{}, false
}

// A web stack's image, or the Functions host's.
func sitePlatformImage(site *Site) (string, bool) {
	if stack, ok := siteBuiltInStack(site); ok {
		return stack.Image, true
	}
	if stack, ok := siteFunctionsHostStack(site); ok {
		return stack.Image, true
	}
	return "", false
}

func hostRunFunctionApp(resID string) (*Site, bool) {
	site, ok := azfSites.Get(resID)
	if !ok {
		return nil, false
	}
	if _, ok := siteFunctionsHostStack(&site); !ok {
		return nil, false
	}
	return &site, true
}

// The AzureWebEncryptionKey app setting, else MACHINEKEY_DecryptionKey (which
// az functionapp create sets on Linux), else WEBSITE_AUTH_ENCRYPTION_KEY.
func siteDataProtectionKey(site *Site) string {
	settings := siteAppSettings(site)
	for _, name := range []string{"AzureWebEncryptionKey", "MACHINEKEY_DecryptionKey"} {
		if v := strings.TrimSpace(settings[name]); v != "" {
			return v
		}
	}
	return ensureWebHostKeys(site.ID).EncryptionKey
}

func functionsSecretsDir(siteName string) string {
	return filepath.Join(siteHomeDir(siteName), "data", "Functions", "secrets")
}

type functionsSecretKey struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Encrypted bool   `json:"encrypted"`
}

type functionsHostSecretsFile struct {
	MasterKey       *functionsSecretKey  `json:"masterKey"`
	FunctionKeys    []functionsSecretKey `json:"functionKeys"`
	SystemKeys      []functionsSecretKey `json:"systemKeys"`
	HostName        string               `json:"hostName,omitempty"`
	Source          string               `json:"source,omitempty"`
	DecryptionKeyID string               `json:"decryptionKeyId,omitempty"`
}

type functionsFunctionSecretsFile struct {
	Keys            []functionsSecretKey `json:"keys"`
	HostName        string               `json:"hostName,omitempty"`
	Source          string               `json:"source,omitempty"`
	DecryptionKeyID string               `json:"decryptionKeyId,omitempty"`
}

// Read the store's keys in first, since the host writes keys too, then write
// every row back. Does nothing for a store the simulator does not model.
func syncFunctionsHostSecrets(site *Site) error {
	if err := importFunctionsHostSecrets(site); err != nil {
		return err
	}
	return exportFunctionsHostSecrets(site)
}

func importFunctionsHostSecrets(site *Site) error {
	store, err := siteFunctionsSecretStore(site)
	if store == nil || err != nil {
		return err
	}
	row := ensureWebHostKeys(site.ID)
	key := siteDataProtectionKey(site)
	open := func(k functionsSecretKey) (string, error) {
		if !k.Encrypted {
			return k.Value, nil
		}
		return unprotectFunctionSecret(key, k.Value)
	}
	documents, err := store.list()
	if err != nil {
		return fmt.Errorf("read the function app's secret store: %w", err)
	}
	for _, name := range sortedKeys(documents) {
		data := bytes.TrimPrefix(documents[name], []byte("\xef\xbb\xbf"))
		if name == "host" {
			var f functionsHostSecretsFile
			if err := json.Unmarshal(data, &f); err != nil {
				return fmt.Errorf("decode host secrets: %w", err)
			}
			if f.MasterKey != nil {
				if row.MasterKey, err = open(*f.MasterKey); err != nil {
					return fmt.Errorf("host secrets: master key: %w", err)
				}
			}
			row.FunctionKeys = map[string]string{}
			for _, k := range f.FunctionKeys {
				if row.FunctionKeys[k.Name], err = open(k); err != nil {
					return fmt.Errorf("host secrets: function key %q: %w", k.Name, err)
				}
			}
			row.SystemKeys = map[string]string{}
			for _, k := range f.SystemKeys {
				if row.SystemKeys[k.Name], err = open(k); err != nil {
					return fmt.Errorf("host secrets: system key %q: %w", k.Name, err)
				}
			}
			webHostKeys.Put(site.ID, row)
			continue
		}
		var f functionsFunctionSecretsFile
		if err := json.Unmarshal(data, &f); err != nil {
			return fmt.Errorf("decode secrets of function %q: %w", name, err)
		}
		keys := map[string]string{}
		for _, k := range f.Keys {
			if keys[k.Name], err = open(k); err != nil {
				return fmt.Errorf("secrets of function %q: key %q: %w", name, k.Name, err)
			}
		}
		id := siteFunctionID(site, name)
		webFunctionKeys.Put(id, WebFunctionKeysRow{ID: id, Keys: keys})
	}
	// The host generates and stores its keys the first time it loads them, and
	// ARM loads them through the host.
	if _, stored := documents["host"]; !stored {
		return exportFunctionsHostSecrets(site)
	}
	return nil
}

// The host names a secrets file after the function in lower case and names
// compare case-insensitively, so an existing function's spelling wins.
func siteFunctionID(site *Site, name string) string {
	prefix := site.ID + "/functions/"
	for _, row := range webFunctionKeys.Filter(func(r WebFunctionKeysRow) bool { return strings.HasPrefix(r.ID, prefix) }) {
		if strings.EqualFold(strings.TrimPrefix(row.ID, prefix), name) {
			return row.ID
		}
	}
	for _, fn := range siteFunctions(site) {
		if strings.EqualFold(fn.FunctionName, name) {
			return fn.ID
		}
	}
	return prefix + name
}

// As the host writes them on App Service: each value protected with the
// site's encryption key. A declared function gets the default key the host
// would generate.
func exportFunctionsHostSecrets(site *Site) error {
	store, err := siteFunctionsSecretStore(site)
	if store == nil || err != nil {
		return err
	}
	row := ensureWebHostKeys(site.ID)
	key := siteDataProtectionKey(site)
	seal := func(name, value string) (functionsSecretKey, error) {
		protected, err := protectFunctionSecret(key, value)
		return functionsSecretKey{Name: name, Value: protected, Encrypted: true}, err
	}
	sealAll := func(keys map[string]string) ([]functionsSecretKey, error) {
		out := []functionsSecretKey{}
		for _, name := range sortedKeys(keys) {
			k, err := seal(name, keys[name])
			if err != nil {
				return nil, err
			}
			out = append(out, k)
		}
		return out, nil
	}
	master, err := seal("master", row.MasterKey)
	if err != nil {
		return err
	}
	host := functionsHostSecretsFile{MasterKey: &master, HostName: site.Properties.DefaultHostName, Source: "runtime"}
	if host.FunctionKeys, err = sealAll(row.FunctionKeys); err != nil {
		return err
	}
	if host.SystemKeys, err = sealAll(row.SystemKeys); err != nil {
		return err
	}
	if err := putFunctionsSecrets(store, "host", host); err != nil {
		return err
	}
	for _, fn := range siteFunctions(site) {
		ensureWebFunctionKeys(fn.ID)
	}
	prefix := site.ID + "/functions/"
	for _, fnRow := range webFunctionKeys.Filter(func(r WebFunctionKeysRow) bool { return strings.HasPrefix(r.ID, prefix) }) {
		keys, err := sealAll(fnRow.Keys)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(fnRow.ID, prefix)
		f := functionsFunctionSecretsFile{Keys: keys, HostName: site.Properties.DefaultHostName, Source: "runtime"}
		if err := putFunctionsSecrets(store, strings.ToLower(name), f); err != nil {
			return err
		}
	}
	return nil
}

// From a file store, so the host stops accepting the keys and the next import
// does not restore them. The blob store keeps them, as the host's blob
// repository does.
func removeFunctionSecrets(site *Site, name string) error {
	store, err := siteFunctionsSecretStore(site)
	if store == nil || err != nil {
		return err
	}
	if err := store.remove(strings.ToLower(name)); err != nil {
		return fmt.Errorf("remove the secrets of function %q: %w", name, err)
	}
	return nil
}

func putFunctionsSecrets(store functionsSecretStore, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := store.put(name, data); err != nil {
		return fmt.Errorf("write %s secrets: %w", name, err)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The ARM functions resource's functions and, for a host-run app, every
// function.json in its content, read as Kudu lists them. A declared envelope
// wins.
func siteFunctions(site *Site) []FunctionEnvelope {
	prefix := site.ID + "/functions/"
	out := azfFunctionConfigs.Filter(func(f FunctionEnvelope) bool { return strings.HasPrefix(f.ID, prefix) })
	for i := range out {
		out[i].FunctionName = strings.TrimPrefix(out[i].ID, prefix)
	}
	stack, ok := siteFunctionsHostStack(site)
	if !ok {
		return out
	}
	declared := map[string]bool{}
	for _, f := range out {
		declared[strings.ToLower(f.FunctionName)] = true
	}
	for name, config := range siteContentFunctionConfigs(site) {
		if declared[strings.ToLower(name)] {
			continue
		}
		fn := FunctionEnvelope{
			ID:           prefix + name,
			Name:         site.Name + "/" + name,
			Type:         "Microsoft.Web/sites/functions",
			FunctionName: name,
			Properties: FunctionEnvelopeProperties{
				Config:   config,
				Language: stack.Runtime,
			},
		}
		if route, ok := functionHTTPRoute(name, config); ok {
			fn.Properties.InvokeURLTemplate = "https://" + site.Properties.DefaultHostName + "/api/" + route
		}
		if disabled, _ := config["disabled"].(bool); disabled {
			fn.Properties.IsDisabled = true
		}
		out = append(out, fn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Compared case-insensitively, as the host compares function names.
func siteFunction(site *Site, name string) (FunctionEnvelope, bool) {
	for _, fn := range siteFunctions(site) {
		if strings.EqualFold(fn.FunctionName, name) {
			return fn, true
		}
	}
	return FunctionEnvelope{}, false
}

// The package WEBSITE_RUN_FROM_PACKAGE names once unpacked there, else what
// deployments and the file system wrote.
func siteContentFunctionConfigs(site *Site) map[string]map[string]any {
	out := map[string]map[string]any{}
	root := webWWWRootDir(site.ID)
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name(), "function.json"))
		if err != nil {
			continue
		}
		// The host loads no function from a function.json it cannot parse.
		var config map[string]any
		if json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &config) == nil {
			out[e.Name()] = config
		}
	}
	return out
}

// The httpTrigger's route, else the function's name.
func functionHTTPRoute(name string, config map[string]any) (string, bool) {
	bindings, _ := config["bindings"].([]any)
	for _, b := range bindings {
		m, _ := b.(map[string]any)
		if t, _ := m["type"].(string); !strings.EqualFold(t, "httpTrigger") {
			continue
		}
		if route, _ := m["route"].(string); route != "" {
			return route, true
		}
		return name, true
	}
	return "", false
}

// The site's identity, its instance, and the key its platform components
// encrypt and sign with. A slot's container carries its app's name and its own
// slot name; the production slot is "Production".
func appServicePlatformEnv(site *Site) map[string]string {
	app, slot, isSlot := strings.Cut(site.Name, "/")
	if !isSlot {
		slot = "Production"
	}
	return map[string]string{
		"WEBSITE_SITE_NAME":           app,
		"WEBSITE_SLOT_NAME":           slot,
		"WEBSITE_HOSTNAME":            site.Properties.DefaultHostName,
		"WEBSITE_RESOURCE_GROUP":      site.Properties.ResourceGroup,
		"WEBSITE_INSTANCE_ID":         sim.RandomHex(64),
		"WEBSITE_AUTH_ENCRYPTION_KEY": ensureWebHostKeys(site.ID).EncryptionKey,
	}
}

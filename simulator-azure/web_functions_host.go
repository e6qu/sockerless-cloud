package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// functionsHostStack is one built-in Linux stack a function app runs on: the
// linuxFxVersion that selects it and the Azure Functions host image App
// Service runs for it. The image's entrypoint starts the host on PORT, which
// loads the function app from /home/site/wwwroot and its keys from the secret
// store AzureWebJobsSecretStorageType selects.
type functionsHostStack struct {
	Runtime        string // FUNCTIONS_WORKER_RUNTIME and the catalogue's stack value, "node"
	RuntimeDisplay string // "Node.js"
	Version        string // "22"
	LinuxFx        string // "Node|22"
	EndOfLife      string
	Image          string
}

// functionsHostStacks are the built-in stacks this App Service runs function
// apps on, each on the Functions host image Microsoft publishes for it under
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

// siteFunctionsHostStack returns the stack a function app's linuxFxVersion
// selects, when it is one this App Service runs the Functions host for.
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

// sitePlatformImage is the image App Service supplies for a site on a
// built-in stack: a web stack's image, or the Functions host's.
func sitePlatformImage(site *Site) (string, bool) {
	if stack, ok := siteBuiltInStack(site); ok {
		return stack.Image, true
	}
	if stack, ok := siteFunctionsHostStack(site); ok {
		return stack.Image, true
	}
	return "", false
}

// hostRunFunctionApp returns the production site resID names when it is a
// function app the Functions host runs.
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

// siteDataProtectionKey is the key the host protects stored key values with:
// the AzureWebEncryptionKey app setting, else MACHINEKEY_DecryptionKey (which
// az functionapp create sets on a Linux app), else the platform's
// WEBSITE_AUTH_ENCRYPTION_KEY.
func siteDataProtectionKey(site *Site) string {
	settings := siteAppSettings(site)
	for _, name := range []string{"AzureWebEncryptionKey", "MACHINEKEY_DecryptionKey"} {
		if v := strings.TrimSpace(settings[name]); v != "" {
			return v
		}
	}
	return ensureWebHostKeys(site.ID).EncryptionKey
}

// siteUsesFileSecrets reports whether the host keeps its keys in its file
// secret store, /home/data/Functions/secrets.
func siteUsesFileSecrets(site *Site) bool {
	return strings.EqualFold(strings.TrimSpace(siteAppSettings(site)["AzureWebJobsSecretStorageType"]), "files")
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

// syncFunctionsHostSecrets makes the key rows the ARM key operations serve and
// the host's file secret store hold the same keys: the store's keys are read
// in first, since the host writes keys too, then every row is written back.
// It does nothing for a site whose host keeps its keys elsewhere.
func syncFunctionsHostSecrets(site *Site) error {
	if err := importFunctionsHostSecrets(site); err != nil {
		return err
	}
	return exportFunctionsHostSecrets(site)
}

func importFunctionsHostSecrets(site *Site) error {
	if !siteUsesFileSecrets(site) {
		return nil
	}
	dir := functionsSecretsDir(site.Name)
	row := ensureWebHostKeys(site.ID)
	key := siteDataProtectionKey(site)
	open := func(k functionsSecretKey) (string, error) {
		if !k.Encrypted {
			return k.Value, nil
		}
		return unprotectFunctionSecret(key, k.Value)
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the function app's secret store: %w", err)
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		// The host keeps snapshots of secrets it cannot decrypt beside the
		// live files, as <name>.<timestamp>.snapshot.json.
		if !ok || e.IsDir() || strings.Contains(name, ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("read secrets %s: %w", e.Name(), err)
		}
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
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
	return nil
}

// siteFunctionID is the ARM ID of a site's function. The host names a
// function's secrets file after the function in lower case, and function
// names are case-insensitive, so an existing function's own spelling wins.
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

// exportFunctionsHostSecrets writes the site's key rows into the host's file
// secret store as the host itself writes them on App Service: each value
// protected with the site's encryption key. A function the site declares
// gets the default key the host would generate for it.
func exportFunctionsHostSecrets(site *Site) error {
	if !siteUsesFileSecrets(site) {
		return nil
	}
	dir := functionsSecretsDir(site.Name)
	if err := sim.EnsureWritableDir(dir); err != nil {
		return fmt.Errorf("create the function app's secret store: %w", err)
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
	if err := writeSecretsFile(dir, "host", host); err != nil {
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
		if err := writeSecretsFile(dir, strings.ToLower(name), f); err != nil {
			return err
		}
	}
	return nil
}

// removeFunctionSecretsFile deletes a deleted function's secrets file, so the
// host stops accepting its keys and the next import does not restore them.
func removeFunctionSecretsFile(site *Site, name string) error {
	if !siteUsesFileSecrets(site) {
		return nil
	}
	err := os.Remove(filepath.Join(functionsSecretsDir(site.Name), strings.ToLower(name)+".json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove the secrets of function %q: %w", name, err)
	}
	return nil
}

// writeSecretsFile replaces a secrets file in one rename, so the host's file
// watcher never reads a partial file, and so a file the host created stays
// replaceable.
func writeSecretsFile(dir, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return fmt.Errorf("write %s secrets: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s secrets: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s secrets: %w", name, err)
	}
	if err := os.Chmod(tmp.Name(), 0o666); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s secrets: %w", name, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name+".json")); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s secrets: %w", name, err)
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// siteFunctions lists the functions a site declares: the functions created
// through the ARM functions resource and, for a function app the Functions
// host runs, every function.json in its deployed content, read the way Kudu
// lists functions. A declared envelope wins over the content's.
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

// siteFunction returns the function a site declares under name, compared
// case-insensitively as the host compares function names.
func siteFunction(site *Site, name string) (FunctionEnvelope, bool) {
	for _, fn := range siteFunctions(site) {
		if strings.EqualFold(fn.FunctionName, name) {
			return fn, true
		}
	}
	return FunctionEnvelope{}, false
}

// siteContentFunctionConfigs reads <function>/function.json from the site's
// content: the package WEBSITE_RUN_FROM_PACKAGE names by URL once the site has
// unpacked it, else what its deployments wrote.
func siteContentFunctionConfigs(site *Site) map[string]map[string]any {
	out := map[string]map[string]any{}
	add := func(p string, data []byte) {
		dir, file := path.Split(p)
		dir = strings.TrimSuffix(dir, "/")
		if file != "function.json" || dir == "" || strings.Contains(dir, "/") {
			return
		}
		// The host loads no function from a function.json it cannot parse.
		var config map[string]any
		if json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &config) == nil {
			out[dir] = config
		}
	}
	if isPackageURL(strings.TrimSpace(siteAppSettings(site)["WEBSITE_RUN_FROM_PACKAGE"])) {
		root := filepath.Join(siteHomeDir(site.Name), "site", "wwwroot")
		entries, _ := os.ReadDir(root) // a site not started yet has no unpacked package
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(root, e.Name(), "function.json"))
			if err == nil {
				add(e.Name()+"/function.json", data)
			}
		}
		return out
	}
	prefix := site.ID + "|"
	for _, f := range webSiteContent.Filter(func(f WebSiteContentFile) bool { return strings.HasPrefix(f.ID, prefix) }) {
		add(f.Path, f.Data)
	}
	return out
}

// functionHTTPRoute is the route an HTTP-triggered function answers under
// /api: its httpTrigger binding's route, else the function's name.
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

// appServicePlatformEnv is the environment App Service gives every site's
// container beside its app settings: the site's identity, the instance it runs
// on, and the key the site's platform components encrypt and sign with.
func appServicePlatformEnv(site *Site) map[string]string {
	return map[string]string{
		"WEBSITE_SITE_NAME":           site.Name,
		"WEBSITE_HOSTNAME":            site.Properties.DefaultHostName,
		"WEBSITE_RESOURCE_GROUP":      site.Properties.ResourceGroup,
		"WEBSITE_INSTANCE_ID":         sim.RandomHex(64),
		"WEBSITE_AUTH_ENCRYPTION_KEY": ensureWebHostKeys(site.ID).EncryptionKey,
	}
}

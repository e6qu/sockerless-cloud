package main

import (
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// functionsSecretStore is where the Azure Functions host keeps a function
// app's keys: one JSON document named "host" and one per function, named after
// the function in lower case.
type functionsSecretStore interface {
	// list returns the live documents by name, leaving out the snapshots the
	// host keeps of secrets it cannot decrypt (<name>.<timestamp>.snapshot.json).
	list() (map[string][]byte, error)
	put(name string, data []byte) error
	remove(name string) error
}

// functionsSecretsContainer is the container BlobStorageSecretsRepository
// keeps a function app's keys in, under the app's unique slot name.
const functionsSecretsContainer = "azure-webjobs-secrets"

// siteFunctionsSecretStore resolves the store the Functions host picks for a
// site, as DefaultSecretManagerProvider does: AzureWebJobsSecretStorageType
// "files" selects the file store, and otherwise, with no
// AzureWebJobsSecretStorageSas, the AzureWebJobsStorage connection string's
// Blob service. It returns nil for a store the simulator does not model (Key
// Vault, Kubernetes, Container Apps, a SAS URI) and for an app with no store.
func siteFunctionsSecretStore(site *Site) (functionsSecretStore, error) {
	settings := siteAppSettings(site)
	switch strings.ToLower(strings.TrimSpace(settings["AzureWebJobsSecretStorageType"])) {
	case "files":
		return fileFunctionsSecretStore{dir: functionsSecretsDir(site.Name)}, nil
	case "keyvault", "kubernetes", "containerapps":
		return nil, nil
	}
	if strings.TrimSpace(settings["AzureWebJobsSecretStorageSas"]) != "" {
		return nil, nil
	}
	connection := strings.TrimSpace(settings["AzureWebJobsStorage"])
	if connection == "" {
		return nil, nil
	}
	account, err := functionsStorageAccount(connection)
	if err != nil {
		return nil, err
	}
	// The host's slot name is WEBSITE_SITE_NAME in lower case for the
	// production slot (EnvironmentExtensions.GetAzureWebsiteUniqueSlotName).
	return blobFunctionsSecretStore{account: account, prefix: strings.ToLower(site.Name) + "/"}, nil
}

// functionsStorageAccount resolves the storage account an AzureWebJobsStorage
// connection string names and checks that its credential opens the account's
// Blob service, since the host reaches its keys with that credential.
func functionsStorageAccount(connection string) (string, error) {
	fields := map[string]string{}
	for _, part := range strings.Split(connection, ";") {
		key, value, ok := strings.Cut(part, "=")
		if ok {
			fields[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	account := fields["accountname"]
	if account == "" {
		if endpoint, err := url.Parse(fields["blobendpoint"]); err == nil {
			if label, rest, ok := strings.Cut(endpoint.Hostname(), "."); ok && strings.HasPrefix(rest, "blob.") {
				account = label
			}
		}
	}
	if account == "" {
		return "", errors.New("the AzureWebJobsStorage connection string names no storage account")
	}
	keys, ok := storageAccountKeys(account)
	if !ok {
		return "", fmt.Errorf("the AzureWebJobsStorage connection string names storage account %q, which does not exist", account)
	}
	if key := fields["accountkey"]; key != "" {
		for _, k := range keys {
			if k == key {
				return account, nil
			}
		}
		return "", fmt.Errorf("the AzureWebJobsStorage account key is not a key of storage account %q", account)
	}
	if sas := strings.TrimPrefix(fields["sharedaccesssignature"], "?"); sas != "" {
		q, err := url.ParseQuery(sas)
		if err == nil {
			if ok, _ := blobSASAuthorizes("blob", account, functionsSecretsContainer, "", q, http.MethodPut); ok {
				return account, nil
			}
		}
		return "", fmt.Errorf("the AzureWebJobsStorage shared access signature does not authorize storage account %q", account)
	}
	return "", fmt.Errorf("the AzureWebJobsStorage connection string carries no credential for storage account %q", account)
}

type fileFunctionsSecretStore struct{ dir string }

func (s fileFunctionsSecretStore) list() (map[string][]byte, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || strings.Contains(name, ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[name] = data
	}
	return out, nil
}

// put replaces a secrets file in one rename, so the host's file watcher never
// reads a partial file, and so a file the host created stays replaceable.
func (s fileFunctionsSecretStore) put(name string, data []byte) error {
	if err := sim.EnsureWritableDir(s.dir); err != nil {
		return fmt.Errorf("create the function app's secret store: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".write-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o666); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, name+".json")); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (s fileFunctionsSecretStore) remove(name string) error {
	err := os.Remove(filepath.Join(s.dir, name+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// blobFunctionsSecretStore keeps the documents as block blobs
// azure-webjobs-secrets/<slot name>/<name>.json in the account's Blob service,
// the same blobs the host reads and writes through the data plane.
type blobFunctionsSecretStore struct {
	account string
	prefix  string
}

func (s blobFunctionsSecretStore) list() (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, key := range blobKeysInContainer(s.account, functionsSecretsContainer) {
		obj, ok := blobObjects.Get(key)
		if !ok || obj.Deleted || obj.Snapshot != "" {
			continue
		}
		rest, ok := strings.CutPrefix(obj.Name, s.prefix)
		if !ok {
			continue
		}
		name, ok := strings.CutSuffix(rest, ".json")
		if !ok || strings.ContainsAny(name, "./") {
			continue
		}
		_, data, err := blobData(obj)
		if err != nil {
			return nil, fmt.Errorf("read blob %s/%s: %w", functionsSecretsContainer, obj.Name, err)
		}
		out[name] = data
	}
	return out, nil
}

func (s blobFunctionsSecretStore) put(name string, data []byte) error {
	// The host creates the container when it is missing.
	mirrorARMContainerToBlobPlane(s.account, functionsSecretsContainer, "", nil)
	now := blobNowHTTP()
	b := BlobObject{
		Account:      s.account,
		Container:    functionsSecretsContainer,
		Name:         s.prefix + name + ".json",
		BlobType:     "BlockBlob",
		ContentType:  "application/octet-stream",
		CreationTime: now,
		AccessTier:   blobDefaultTier("BlockBlob"),
	}
	b.AccessTierInferred = true
	if existing, ok := blobObjects.Get(blobObjectKey(b.Account, b.Container, b.Name)); ok && !existing.Deleted {
		b.CreationTime = existing.CreationTime
		b.Lease = existing.Lease
	}
	sum := md5.Sum(data)
	b.ContentMD5 = base64.StdEncoding.EncodeToString(sum[:])
	blobTouch(&b)
	return putBlobWithContents(b, data)
}

// remove keeps a deleted function's secrets: BlobStorageSecretsRepository
// never deletes a blob, and its purge of stale secrets does nothing.
func (s blobFunctionsSecretStore) remove(string) error { return nil }

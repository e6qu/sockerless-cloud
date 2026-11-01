package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// kmsCryptoKeyNameFree refuses a CryptoKey name that is taken or retired:
// Cloud KMS never reuses the name of a deleted CryptoKey.
func kmsCryptoKeyNameFree(name string) error {
	if _, exists := kmsCryptoKeys.Get(name); exists {
		return status.Errorf(codes.AlreadyExists, "CryptoKey %s already exists", name)
	}
	for _, rr := range kmsRetiredResources.List() {
		if rr.OriginalResource == name {
			return status.Errorf(codes.AlreadyExists,
				"CryptoKey %s was deleted and its name is retired as %s; the names of deleted CryptoKeys can't be reused", name, rr.Name)
		}
	}
	return nil
}

// kmsRetireCryptoKey deletes a CryptoKey and records the RetiredResource that
// keeps its name from reuse.
func kmsRetireCryptoKey(name string) kmsRetiredResource {
	rr := kmsRetiredResource{
		Name:             kmsLocationFromName(name) + "/retiredResources/" + kmsRandomHex(16),
		ResourceType:     kmsCryptoKeyResourceType(),
		OriginalResource: name,
		DeleteTime:       kmsNow(),
	}
	kmsRetiredResources.Put(rr.Name, rr)
	kmsCryptoKeys.Delete(name)
	kmsIamPolicies.Delete(name)
	return rr
}

// kmsDoneOperation records a finished delete whose result is
// google.protobuf.Empty and whose metadata is the named Cloud KMS message.
func kmsDoneOperation(resourceName, metadata string, fields map[string]any) Operation {
	parts := strings.Split(resourceName, "/")
	meta := map[string]any{"@type": "type.googleapis.com/google.cloud.kms.v1." + metadata}
	for k, v := range fields {
		meta[k] = v
	}
	return newLRO(parts[1], parts[3], nil, "type.googleapis.com/google.protobuf.Empty", gcpFixedOperationMetadata(meta))
}

func kmsRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// kmsAutokeyResourceTypes are the protected resource types the Autokey
// overview lists under compatible services.
var kmsAutokeyResourceTypes = map[string]bool{
	"alloydb.googleapis.com/Backup":               true,
	"alloydb.googleapis.com/Cluster":              true,
	"apigee.googleapis.com/Instance":              true,
	"apigee.googleapis.com/Organization":          true,
	"apihub.googleapis.com/ApiHubInstance":        true,
	"artifactregistry.googleapis.com/Repository":  true,
	"backupdr.googleapis.com/BackupVault":         true,
	"bigquery.googleapis.com/Dataset":             true,
	"bigtableadmin.googleapis.com/Cluster":        true,
	"composer.googleapis.com/Environment":         true,
	"compute.googleapis.com/Disk":                 true,
	"compute.googleapis.com/Image":                true,
	"compute.googleapis.com/Instance":             true,
	"compute.googleapis.com/MachineImage":         true,
	"container.googleapis.com/Cluster":            true,
	"dataflow.googleapis.com/Job":                 true,
	"dataproc.googleapis.com/Batch":               true,
	"dataproc.googleapis.com/Cluster":             true,
	"dataproc.googleapis.com/Session":             true,
	"dataproc.googleapis.com/SessionTemplate":     true,
	"dataproc.googleapis.com/WorkflowTemplate":    true,
	"datastream.googleapis.com/Stream":            true,
	"file.googleapis.com/Backup":                  true,
	"file.googleapis.com/Instance":                true,
	"gkebackup.googleapis.com/BackupPlan":         true,
	"memorystore.googleapis.com/Instance":         true,
	"pubsub.googleapis.com/Topic":                 true,
	"redis.googleapis.com/Cluster":                true,
	"redis.googleapis.com/Instance":               true,
	"run.googleapis.com/Job":                      true,
	"run.googleapis.com/Service":                  true,
	"secretmanager.googleapis.com/Secret":         true,
	"securesourcemanager.googleapis.com/Instance": true,
	"spanner.googleapis.com/Database":             true,
	"sqladmin.googleapis.com/Instance":            true,
	"storage.googleapis.com/Bucket":               true,
	"vmmigration.googleapis.com/MigratingVm":      true,
	"vmmigration.googleapis.com/Source":           true,
	"workflows.googleapis.com/Workflow":           true,
}

// kmsAutokeyRotationPeriod is the one-year rotation period Autokey gives the
// keys it creates.
const kmsAutokeyRotationPeriod = 365 * 24 * time.Hour

// kmsAutokeyKeyProject resolves the project Autokey creates a resource
// project's keys in from the nearest AutokeyConfig up the resource hierarchy.
func kmsAutokeyKeyProject(resourceProject CRMProject) (CRMProject, error) {
	number := strings.TrimPrefix(resourceProject.Name, "projects/")
	candidates := []string{"projects/" + resourceProject.ProjectId, "projects/" + number}
	parent := resourceProject.Parent
	for {
		for _, node := range candidates {
			cfg, ok := kmsAutokeyConfigs.Get(node + "/autokeyConfig")
			if !ok || (cfg.KeyProject == "" && cfg.KeyProjectResolutionMode == "") {
				continue
			}
			switch cfg.KeyProjectResolutionMode {
			case "DISABLED":
				return CRMProject{}, status.Errorf(codes.FailedPrecondition,
					"Cloud KMS Autokey is disabled for projects/%s by %s/autokeyConfig", resourceProject.ProjectId, node)
			case "RESOURCE_PROJECT":
				return resourceProject, nil
			}
			if cfg.KeyProject == "" {
				return CRMProject{}, status.Errorf(codes.FailedPrecondition,
					"%s/autokeyConfig names no key project", node)
			}
			keyProject, found := crmResolveProject(strings.TrimPrefix(cfg.KeyProject, "projects/"))
			if !found || keyProject.State != "ACTIVE" {
				return CRMProject{}, status.Errorf(codes.FailedPrecondition,
					"the Autokey key project %s configured on %s does not exist", cfg.KeyProject, node)
			}
			return keyProject, nil
		}
		if !strings.HasPrefix(parent, "folders/") {
			break
		}
		candidates = []string{parent}
		folder, ok := crmFolders.Get(parent)
		if !ok {
			break
		}
		parent = folder.Parent
	}
	return CRMProject{}, status.Errorf(codes.FailedPrecondition,
		"Cloud KMS Autokey is not enabled for projects/%s or any folder above it", resourceProject.ProjectId)
}

// kmsCreateKeyHandle provisions the CryptoKey a KeyHandle names, as Autokey
// does: an HSM AES-256-GCM key with a one-year rotation period in the autokey
// key ring of the key project, in the KeyHandle's location.
func kmsCreateKeyHandle(projectRef, location, id, selector string) (kmsKeyHandle, error) {
	if selector == "" {
		return kmsKeyHandle{}, status.Error(codes.InvalidArgument, "resource_type_selector is required")
	}
	if !kmsAutokeyResourceTypes[selector] {
		return kmsKeyHandle{}, status.Errorf(codes.InvalidArgument,
			"resource_type_selector %s is not a resource type Cloud KMS Autokey supports", selector)
	}
	if !kmsLocationValid(location) {
		return kmsKeyHandle{}, status.Errorf(codes.InvalidArgument, "Invalid location %q", location)
	}
	resourceProject, ok := crmResolveProject(projectRef)
	if !ok || resourceProject.State != "ACTIVE" {
		return kmsKeyHandle{}, status.Errorf(codes.NotFound, "project %s not found", projectRef)
	}
	if id == "" {
		id = sim.NewUUID()
	}
	name := fmt.Sprintf("projects/%s/locations/%s/keyHandles/%s", projectRef, location, id)
	if _, exists := kmsKeyHandles.Get(name); exists {
		return kmsKeyHandle{}, status.Errorf(codes.AlreadyExists, "KeyHandle %s already exists", name)
	}
	keyProject, err := kmsAutokeyKeyProject(resourceProject)
	if err != nil {
		return kmsKeyHandle{}, err
	}

	ringName := kmsKeyRingName(keyProject.ProjectId, location, "autokey")
	if _, ok := kmsKeyRings.Get(ringName); !ok {
		kmsKeyRings.Put(ringName, kmsKeyRing{Name: ringName, CreateTime: kmsNow()})
	}
	service, descriptor, _ := strings.Cut(selector, "/")
	keyID := fmt.Sprintf("%s-%s-%s-%s", strings.TrimPrefix(resourceProject.Name, "projects/"),
		strings.TrimSuffix(service, ".googleapis.com"), descriptor, kmsRandomHex(4))
	keyName := ringName + "/cryptoKeys/" + keyID
	if err := kmsCryptoKeyNameFree(keyName); err != nil {
		return kmsKeyHandle{}, err
	}
	versionID, err := kmsCreateVersionForAlg(keyName, "1", "HSM", kmsSymmetricAlgorithm)
	if err != nil {
		return kmsKeyHandle{}, err
	}
	now := time.Now().UTC()
	kmsCryptoKeys.Put(keyName, kmsStoredCryptoKey{
		Name:                     keyName,
		Purpose:                  kmsPurposeEncryptDecrypt,
		CreateTime:               now.Format(time.RFC3339),
		RotationPeriod:           strconv.Itoa(int(kmsAutokeyRotationPeriod.Seconds())) + "s",
		NextRotationTime:         now.Add(kmsAutokeyRotationPeriod).Format(time.RFC3339),
		DestroyScheduledDuration: kmsDestroyScheduledDuration(""),
		ProtectionLevel:          "HSM",
		Algorithm:                kmsSymmetricAlgorithm,
		PrimaryVersionID:         versionID,
		VersionSeq:               1,
		Autokey:                  true,
	})
	kh := kmsKeyHandle{Name: name, KmsKey: keyName, ResourceTypeSelector: selector}
	kmsKeyHandles.Put(name, kh)
	return kh, nil
}

var kmsKeyHandleFilterPattern = regexp.MustCompile(`^\s*resource_type_selector\s*=\s*"([^"]*)"\s*$`)

// kmsListKeyHandles answers a location's KeyHandles, narrowed by the one
// filter ListKeyHandles documents, resource_type_selector="...".
func kmsListKeyHandles(location, filter string) ([]kmsKeyHandle, error) {
	selector := ""
	if strings.TrimSpace(filter) != "" {
		m := kmsKeyHandleFilterPattern.FindStringSubmatch(filter)
		if m == nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid filter %q; use resource_type_selector=\"{SERVICE}.googleapis.com/{TYPE}\"", filter)
		}
		selector = m[1]
	}
	var out []kmsKeyHandle
	for _, kh := range kmsKeyHandles.List() {
		if strings.HasPrefix(kh.Name, location+"/keyHandles/") && (selector == "" || kh.ResourceTypeSelector == selector) {
			out = append(out, kh)
		}
	}
	return out, nil
}

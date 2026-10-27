package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// computeSpecActions maps every vendored Microsoft.Compute operation to the
// action that authorizes it; reads of one resource share an action.
var computeSpecActions = map[string]string{
	"Operations_List":          "Microsoft.Compute/operations/read",
	"ResourceSkus_List":        "Microsoft.Compute/skus/read",
	"Usage_List":               "Microsoft.Compute/locations/usages/read",
	"VirtualMachineSizes_List": "Microsoft.Compute/locations/vmSizes/read",

	"VirtualMachines_Get":                "Microsoft.Compute/virtualMachines/read",
	"VirtualMachines_List":               "Microsoft.Compute/virtualMachines/read",
	"VirtualMachines_ListAll":            "Microsoft.Compute/virtualMachines/read",
	"VirtualMachines_ListByLocation":     "Microsoft.Compute/virtualMachines/read",
	"VirtualMachines_CreateOrUpdate":     "Microsoft.Compute/virtualMachines/write",
	"VirtualMachines_Update":             "Microsoft.Compute/virtualMachines/write",
	"VirtualMachines_Delete":             "Microsoft.Compute/virtualMachines/delete",
	"VirtualMachines_InstanceView":       "Microsoft.Compute/virtualMachines/instanceView/read",
	"VirtualMachines_ListAvailableSizes": "Microsoft.Compute/virtualMachines/vmSizes/read",

	"VirtualMachines_Start":                       "Microsoft.Compute/virtualMachines/start/action",
	"VirtualMachines_PowerOff":                    "Microsoft.Compute/virtualMachines/powerOff/action",
	"VirtualMachines_Restart":                     "Microsoft.Compute/virtualMachines/restart/action",
	"VirtualMachines_Deallocate":                  "Microsoft.Compute/virtualMachines/deallocate/action",
	"VirtualMachines_Generalize":                  "Microsoft.Compute/virtualMachines/generalize/action",
	"VirtualMachines_Capture":                     "Microsoft.Compute/virtualMachines/capture/action",
	"VirtualMachines_ConvertToManagedDisks":       "Microsoft.Compute/virtualMachines/convertToManagedDisks/action",
	"VirtualMachines_Redeploy":                    "Microsoft.Compute/virtualMachines/redeploy/action",
	"VirtualMachines_Reimage":                     "Microsoft.Compute/virtualMachines/reimage/action",
	"VirtualMachines_Reapply":                     "Microsoft.Compute/virtualMachines/reapply/action",
	"VirtualMachines_PerformMaintenance":          "Microsoft.Compute/virtualMachines/performMaintenance/action",
	"VirtualMachines_SimulateEviction":            "Microsoft.Compute/virtualMachines/simulateEviction/action",
	"VirtualMachines_AssessPatches":               "Microsoft.Compute/virtualMachines/assessPatches/action",
	"VirtualMachines_InstallPatches":              "Microsoft.Compute/virtualMachines/installPatches/action",
	"VirtualMachines_RetrieveBootDiagnosticsData": "Microsoft.Compute/virtualMachines/retrieveBootDiagnosticsData/action",

	"VirtualMachineExtensions_Get":            "Microsoft.Compute/virtualMachines/extensions/read",
	"VirtualMachineExtensions_List":           "Microsoft.Compute/virtualMachines/extensions/read",
	"VirtualMachineExtensions_CreateOrUpdate": "Microsoft.Compute/virtualMachines/extensions/write",
	"VirtualMachineExtensions_Update":         "Microsoft.Compute/virtualMachines/extensions/write",
	"VirtualMachineExtensions_Delete":         "Microsoft.Compute/virtualMachines/extensions/delete",
}

// A document added to the vendored set but not here would leave its
// operations unmapped.
var computeSpecFiles = []string{
	"compute-arm-computerpcommon-2022-03-01.swagger.json.gz",
	"compute-arm-skus-2021-07-01.swagger.json.gz",
	"compute-arm-virtualmachine-2022-03-01.swagger.json.gz",
}

// TestComputeOperationCatalogCoversSpec: the catalog carries an action for
// every documented operation and none no operation needs.
func TestComputeOperationCatalogCoversSpec(t *testing.T) {
	catalog := map[string]bool{}
	for _, op := range computeOperationCatalog {
		if catalog[op.Name] {
			t.Errorf("catalog lists action %q twice", op.Name)
		}
		catalog[op.Name] = true
	}

	needed := map[string]bool{}
	operations := 0
	for _, file := range computeSpecFiles {
		for _, operationID := range computeSpecOperationIDs(t, file) {
			operations++
			action, ok := computeSpecActions[operationID]
			if !ok {
				t.Errorf("operation %q has no Azure Resource Manager action mapped; add it here and to computeOperationCatalog", operationID)
				continue
			}
			needed[action] = true
			if !catalog[action] {
				t.Errorf("operation %q needs action %q, which computeOperationCatalog does not list", operationID, action)
			}
		}
	}
	if operations == 0 {
		t.Fatal("the vendored Microsoft.Compute swaggers declared no operations")
	}

	var stale []string
	for action := range catalog {
		if !needed[action] {
			stale = append(stale, action)
		}
	}
	sort.Strings(stale)
	for _, action := range stale {
		t.Errorf("computeOperationCatalog lists action %q, which no documented operation requires", action)
	}
}

func computeSpecOperationIDs(t *testing.T, file string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "specs", "cloud-api", "azure", file))
	if err != nil {
		t.Fatalf("open vendored Microsoft.Compute swagger %s: %v", file, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip vendored Microsoft.Compute swagger %s: %v", file, err)
	}
	defer gz.Close()
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.NewDecoder(gz).Decode(&doc); err != nil {
		t.Fatalf("decode vendored Microsoft.Compute swagger %s: %v", file, err)
	}
	var ids []string
	for _, methods := range doc.Paths {
		for verb, op := range methods {
			switch verb {
			case "get", "put", "post", "patch", "delete", "head":
			default:
				continue // "parameters" and other non-operation members
			}
			if op.OperationID != "" {
				ids = append(ids, op.OperationID)
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// TestAzureHasScopeLeadRecognisesABareScope: only a leading `scope` followed by
// `providers`, or a parameter the document marks. A wider rule would probe
// ordinary operations at an address no client uses.
func TestAzureHasScopeLeadRecognisesABareScope(t *testing.T) {
	scoped := func(raw []string, marked bool) swaggerPath {
		sp := swaggerPath{Raw: raw, PathScopes: map[string]bool{}}
		if marked {
			sp.PathScopes[strings.Trim(raw[0], "{}")] = true
		}
		return sp
	}
	cases := []struct {
		name string
		path swaggerPath
		want bool
	}{
		{"marked scope", scoped([]string{"{scope}", "providers", "Microsoft.Authorization", "roleAssignments"}, true), true},
		{"bare scope before providers", scoped([]string{"{scope}", "providers", "Microsoft.EventGrid", "extensionTopics", "default"}, false), true},
		{"bare scope not before providers", scoped([]string{"{scope}", "something", "else"}, false), false},
		{"a leading parameter that is not a scope", scoped([]string{"{resourceId}", "providers", "Microsoft.EventGrid", "x"}, false), false},
		{"a literal lead", scoped([]string{"subscriptions", "{subscriptionId}", "providers", "x"}, false), false},
		{"nothing but wildcards after the scope", scoped([]string{"{scope}", "{name}"}, true), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := azureHasScopeLead(tc.path); got != tc.want {
				t.Errorf("azureHasScopeLead(%v) = %v, want %v", tc.path.Raw, got, tc.want)
			}
		})
	}
}

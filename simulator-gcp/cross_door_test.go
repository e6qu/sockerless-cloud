package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A service reached over two protocols is served here from one set of stores,
// and sdk-tests/cross_door_test.go proves it by writing through one door and
// observing through the other. This holds that file to the services the server
// actually mounts, so a two-door service cannot arrive uncrossed.
//
// The gate exists because the divergence it guards against is invisible to
// every other test. A suite that drives one door and reads back through the
// same door passes whether the handler did the work or only said it did: Cloud
// Bigtable's REST dropRowRange acknowledged deletes it never performed while
// the gRPC spelling deleted for real, and the long-running Operations service
// kept its own store, so an operation a gRPC call returned was invisible to the
// REST operations door and the reverse. Both survived a green suite. Neither
// survives a crossing.

// crossDoorTests names the test that crosses each mounted gRPC service against
// its REST door.
//
// Several services are crossed by one test where their state is one thing: the
// Cloud Bigtable admin services and the data service share the instance,
// table and row stores a single test exercises end to end, and Pub/Sub's
// Publisher and Subscriber share the queue their test publishes into and pulls
// from.
var crossDoorTests = map[string]string{
	"google.bigtable.admin.v2.BigtableInstanceAdmin":     "TestCrossDoor_BigtableAdminAndData",
	"google.bigtable.admin.v2.BigtableTableAdmin":        "TestCrossDoor_BigtableAdminAndData",
	"google.bigtable.v2.Bigtable":                        "TestCrossDoor_BigtableAdminAndData",
	"google.cloud.kms.v1.KeyManagementService":           "TestCrossDoor_CloudKMS",
	"google.cloud.secretmanager.v1.SecretManagerService": "TestCrossDoor_SecretManager",
	"google.firestore.v1.Firestore":                      "TestCrossDoor_Firestore",
	"google.logging.v2.LoggingServiceV2":                 "TestCrossDoor_CloudLogging",
	"google.longrunning.Operations":                      "TestCrossDoor_Operations",
	"google.pubsub.v1.Publisher":                         "TestCrossDoor_PubSub",
	"google.pubsub.v1.SchemaService":                     "TestCrossDoor_PubSubSchemas",
	"google.pubsub.v1.Subscriber":                        "TestCrossDoor_PubSub",
	"google.spanner.v1.Spanner":                          "TestCrossDoor_Spanner",
}

// crossDoorSingleDoor records a mounted gRPC service the cloud does not also
// serve over REST, with the reason. An entry here is a claim about the cloud's
// own surface, not about this simulator's: adding one to avoid writing a
// crossing would hide exactly what the crossing is for.
var crossDoorSingleDoor = map[string]string{}

func TestCrossDoorEveryTwoDoorServiceIsCrossed(t *testing.T) {
	for name := range grpcRegisteredServices(t) {
		_, crossed := crossDoorTests[name]
		_, singleDoor := crossDoorSingleDoor[name]
		switch {
		case crossed && singleDoor:
			t.Errorf("%s is listed both as crossed and as single-door — it is one or the other", name)
		case !crossed && !singleDoor:
			t.Errorf("%s is mounted on the gRPC server but nothing crosses it against its REST door. "+
				"Add a test to sdk-tests/cross_door_test.go that writes through one protocol and reads "+
				"through the other, or record in crossDoorSingleDoor why the cloud serves it over gRPC alone.", name)
		}
	}
	for name := range crossDoorTests {
		if _, mounted := grpcRegisteredServices(t)[name]; !mounted {
			t.Errorf("%s has a cross-door entry but is not mounted — remove the entry or mount the service", name)
		}
	}
}

// TestCrossDoorTestsExist holds the table to the tests it names: a renamed or
// deleted crossing must fail here rather than leave the table pointing at
// nothing, which would read as coverage that no longer runs.
func TestCrossDoorTestsExist(t *testing.T) {
	declared := crossDoorTestFunctions(t)
	named := make([]string, 0, len(crossDoorTests))
	for _, test := range crossDoorTests {
		named = append(named, test)
	}
	sort.Strings(named)
	for _, test := range named {
		if _, ok := declared[test]; !ok {
			t.Errorf("crossDoorTests names %s, which sdk-tests/cross_door_test.go does not declare", test)
		}
	}
}

// crossDoorTestFunctions returns the test functions sdk-tests/cross_door_test.go
// declares.
func crossDoorTestFunctions(t *testing.T) map[string]struct{} {
	t.Helper()
	path := filepath.Join("sdk-tests", "cross_door_test.go")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the cross-door suite is missing: %v", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	out := map[string]struct{}{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
			continue
		}
		out[fn.Name.Name] = struct{}{}
	}
	return out
}

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

// The gRPC surfaces are measured and ratcheted as the REST ones are. A service
// that embeds its generated Unimplemented server answers every method it does
// not implement with codes.Unimplemented, a correct status that hides the gap.
// The declared methods come from grpc.Server.GetServiceInfo over the services
// registerAllGRPCServices mounts; a method is served when the implementation
// type declares it, read from the syntax tree because reflection names a
// promoted method's wrapper after the outer type. A declared method that
// refuses one unsupported input with Unimplemented still counts as served.

// grpcServiceImplementations names the type behind each registered service.
// TestGRPCEveryRegisteredServiceIsMeasured holds this table to the services
// the server actually mounts, so a new service cannot arrive unmeasured.
var grpcServiceImplementations = map[string]string{
	"google.bigtable.admin.v2.BigtableInstanceAdmin":     "bigtableInstanceAdminGRPC",
	"google.bigtable.admin.v2.BigtableTableAdmin":        "bigtableTableAdminGRPC",
	"google.bigtable.v2.Bigtable":                        "bigtableDataGRPC",
	"google.cloud.kms.v1.KeyManagementService":           "cloudKmsGRPC",
	"google.cloud.secretmanager.v1.SecretManagerService": "secretManagerGRPC",
	"google.firestore.v1.Firestore":                      "firestoreGRPC",
	"google.logging.v2.LoggingServiceV2":                 "loggingServer",
	"google.longrunning.Operations":                      "grpcOperationsService",
	"google.pubsub.v1.Publisher":                         "pubsubPublisherGRPC",
	"google.pubsub.v1.SchemaService":                     "pubsubSchemaGRPC",
	"google.pubsub.v1.Subscriber":                        "pubsubSubscriberGRPC",
	"google.spanner.v1.Spanner":                          "spannerDataGRPC",
}

// grpcMethodFloor is the number of methods each service implements. It only
// rises: a method that stops being declared falls back to Unimplemented, a
// silent regression. Three methods stay unimplemented because answering them
// would mean inventing state the simulator does not hold: Cloud Bigtable's
// OpenMaterializedView (no materialized result set or aggregate evaluator),
// Firestore's ExecutePipeline (no pipeline expression evaluator) and Cloud
// Spanner's FetchCacheUpdate (one SQLite database with no splits, replicas or
// zones to report).
var grpcMethodFloor = map[string]int{
	"google.bigtable.admin.v2.BigtableInstanceAdmin":     34,
	"google.bigtable.admin.v2.BigtableTableAdmin":        35,
	"google.bigtable.v2.Bigtable":                        14,
	"google.cloud.kms.v1.KeyManagementService":           35,
	"google.cloud.secretmanager.v1.SecretManagerService": 17,
	"google.firestore.v1.Firestore":                      16,
	"google.logging.v2.LoggingServiceV2":                 6,
	"google.longrunning.Operations":                      5,
	"google.pubsub.v1.Publisher":                         9,
	"google.pubsub.v1.SchemaService":                     10,
	"google.pubsub.v1.Subscriber":                        16,
	"google.spanner.v1.Spanner":                          16,
}

// grpcDeclaredMethodTotals locks each service's declared method count, so a
// re-vendored proto that adds methods cannot leave them unimplemented behind
// green floors.
var grpcDeclaredMethodTotals = map[string]int{
	"google.bigtable.admin.v2.BigtableInstanceAdmin":     34,
	"google.bigtable.admin.v2.BigtableTableAdmin":        35,
	"google.bigtable.v2.Bigtable":                        15,
	"google.cloud.kms.v1.KeyManagementService":           35,
	"google.cloud.secretmanager.v1.SecretManagerService": 17,
	"google.firestore.v1.Firestore":                      17,
	"google.logging.v2.LoggingServiceV2":                 6,
	"google.longrunning.Operations":                      5,
	"google.pubsub.v1.Publisher":                         9,
	"google.pubsub.v1.SchemaService":                     10,
	"google.pubsub.v1.Subscriber":                        16,
	"google.spanner.v1.Spanner":                          17,
}

func TestGRPCEveryRegisteredServiceIsMeasured(t *testing.T) {
	for name := range grpcRegisteredServices(t) {
		if _, measured := grpcServiceImplementations[name]; !measured {
			t.Errorf("%s is mounted on the gRPC server but has no grpcServiceImplementations entry — "+
				"add its implementation type so the coverage ratchet measures it", name)
		}
	}
	for name := range grpcServiceImplementations {
		if _, mounted := grpcRegisteredServices(t)[name]; !mounted {
			t.Errorf("%s has an implementation entry but is not mounted — remove the entry or mount the service", name)
		}
	}
}

func TestGRPCCoverageFloor(t *testing.T) {
	services := grpcRegisteredServices(t)
	declaredMethods := grpcDeclaredMethodsBySource(t)

	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)

	totalServed, totalDeclared := 0, 0
	for _, name := range names {
		implementation, measured := grpcServiceImplementations[name]
		if !measured {
			continue // reported by TestGRPCEveryRegisteredServiceIsMeasured
		}
		declared := services[name]
		sort.Strings(declared)
		var unserved []string
		for _, method := range declared {
			if _, ok := declaredMethods[implementation+"."+method]; !ok {
				unserved = append(unserved, method)
			}
		}
		served := len(declared) - len(unserved)
		totalServed += served
		totalDeclared += len(declared)
		t.Logf("%-52s %d/%d methods served", name, served, len(declared))

		if total, locked := grpcDeclaredMethodTotals[name]; !locked {
			t.Errorf("%s: no grpcDeclaredMethodTotals entry — add one at its declared count (%d)", name, len(declared))
		} else if len(declared) != total {
			t.Errorf("%s: the generated service declares %d methods, the lock says %d — a re-vendored proto changed the surface. "+
				"Implement the new methods or record why not, then update grpcDeclaredMethodTotals (and grpcMethodFloor if coverage moved).",
				name, len(declared), total)
		}

		floor, held := grpcMethodFloor[name]
		if !held {
			t.Errorf("%s: no grpcMethodFloor entry — add one at its measured coverage (%d)", name, served)
			continue
		}
		if served != floor {
			t.Errorf("%s: coverage %d/%d != floor %d — update grpcMethodFloor (a drop is a regression: the method fell back "+
				"to the embedded Unimplemented server).\n  %d method(s) not implemented:\n    %s",
				name, served, len(declared), floor, len(unserved), strings.Join(unserved, "\n    "))
		}
	}
	t.Logf("TOTAL: %d/%d gRPC methods served", totalServed, totalDeclared)
}

// grpcRegisteredServices mounts the production service set and returns each
// service's declared method names.
func grpcRegisteredServices(t *testing.T) map[string][]string {
	t.Helper()
	server := grpc.NewServer()
	registerAllGRPCServices(server)
	out := map[string][]string{}
	for name, info := range server.GetServiceInfo() {
		methods := make([]string, 0, len(info.Methods))
		for _, method := range info.Methods {
			methods = append(methods, method.Name)
		}
		out[name] = methods
	}
	return out
}

// grpcDeclaredMethodsBySource returns the "<type>.<method>" keys the package
// declares on its own types. A method promoted from an embedded
// Unimplemented server is absent, which is exactly the distinction the
// coverage measures.
func grpcDeclaredMethodsBySource(t *testing.T) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	declared := map[string]struct{}{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			receiver := fn.Recv.List[0].Type
			if star, isPointer := receiver.(*ast.StarExpr); isPointer {
				receiver = star.X
			}
			ident, isIdent := receiver.(*ast.Ident)
			if !isIdent {
				continue
			}
			declared[ident.Name+"."+fn.Name.Name] = struct{}{}
		}
	}
	return declared
}

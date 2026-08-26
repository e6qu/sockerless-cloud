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

// The gRPC surfaces are measured and ratcheted, the way the REST surfaces
// are.
//
// Until this gate existed the gRPC services had no coverage measurement at
// all: the Discovery and Swagger probes speak HTTP, and a gRPC service that
// embeds its generated Unimplemented server answers every method it does
// not implement with codes.Unimplemented — a correct status, and an
// invisible gap. Nothing counted those, so nobody could say how much of
// Cloud Bigtable, Cloud Spanner, Firestore, Pub/Sub, Cloud KMS, Secret
// Manager or Cloud Logging the simulator actually serves over gRPC.
//
// The measurement reads two facts and compares them. The declared methods
// come from the server itself: registerAllGRPCServices mounts exactly what
// production mounts, and grpc.Server.GetServiceInfo reports each service's
// method set from the generated ServiceDesc, which is the vendored proto's.
// The served methods come from the source: a method is served when the
// implementation type declares it, and unserved when it is promoted from
// the embedded Unimplemented server. Reflection cannot tell those apart —
// Go names a promoted method's wrapper after the outer type — so the
// implementation's own declarations are read from the syntax tree.
//
// A method that IS declared and answers codes.Unimplemented for one
// unsupported sub-case (an unsupported Cloud Bigtable row filter, a Cloud
// Spanner DDL statement the translator does not speak) counts as served:
// the method exists and the refusal is the faithful answer for that input,
// not an absent method.

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
// rises: a method that stops being declared falls back to the embedded
// Unimplemented server, which is a silent regression — a client's call
// starts failing with a status the service used to answer properly.
//
// 210 of 213, and the shape of the remainder matters more than the number.
// Complete: both Cloud Bigtable admin services, Cloud KMS, Cloud Logging, the
// long-running Operations service, Pub/Sub's three services, Secret Manager.
//
// The three that remain are not wiring left undone. Each needs state this
// simulator does not hold, and each would have to invent that state to answer
// at all — so each stays on the embedded Unimplemented server, where a client
// gets a clear status instead of a plausible wrong one.
//
// Cloud Bigtable's data service, 14 of 15: OpenMaterializedView. A
// materialized view's rows are the continuously maintained result of the
// GoogleSQL query it was created with. The simulator stores that query string
// and nothing else — no materialized result set, and no evaluator for the
// aggregate queries materialized views are defined by — so a session over one
// could only read rows that were never computed.
//
// Firestore's 16 of 17: ExecutePipeline. Pipeline stages carry expression
// trees from the pipeline function library (field references, comparisons,
// arithmetic, array and map accessors, aggregate accumulators) against
// arbitrary document sources in any stage order. The simulator has the
// structured-query evaluator only, and a pipeline is not expressible as a
// structured query.
//
// Cloud Spanner's 16 of 17: FetchCacheUpdate. Its payload is a location
// cache — split ranges, the groups and zones serving them, and the key
// recipes for choosing among them. The simulator is one SQLite database in
// one process with no splits, replicas or zones, so every field that makes
// the update useful has no source, and there would be nothing to stream after
// a first message: cache updates report splits moving, and no split here
// moves.
var grpcMethodFloor = map[string]int{
	"google.bigtable.admin.v2.BigtableInstanceAdmin":     31,
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

// grpcDeclaredMethodTotals locks each service's declared method count, for
// the reason the REST declared-total locks exist: a re-vendored proto that
// ADDS methods leaves the served counts untouched, so the floors stay green
// while the new methods answer Unimplemented unnoticed.
var grpcDeclaredMethodTotals = map[string]int{
	"google.bigtable.admin.v2.BigtableInstanceAdmin":     31,
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

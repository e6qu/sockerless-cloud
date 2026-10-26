package gcp_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Differential testing against Google's Firestore emulator: the same SDK
// operations run against the simulator (REST) and the emulator (gRPC). The
// emulator is a reference, not a ceiling: where the simulator is more faithful,
// or the transports legitimately differ, fsDiffKnownDivergences records the case
// with its reason and asserts it exactly.
//
// Every scenario also carries the outcome the simulator must produce, asserted
// on its own, so a defect the simulator and the emulator share cannot agree its
// way to green.

type fsDiffResult struct {
	value   string // canonical JSON of the observed document data, or a sentinel
	errCode string // gRPC status code name, "" on success
}

type fsDiffDivergence struct {
	sim    fsDiffResult
	oracle fsDiffResult
	reason string
}

var fsDiffKnownDivergences = map[string]fsDiffDivergence{
	// The gax REST transport maps HTTP 409 ALREADY_EXISTS to codes.Aborted, while
	// the emulator answers codes.AlreadyExists over gRPC: one condition, two
	// transport mappings.
	"create-on-existing": {
		sim:    fsDiffResult{errCode: "Aborted"},
		oracle: fsDiffResult{errCode: "AlreadyExists"},
		reason: "REST transport maps 409 to Aborted; gRPC emulator returns AlreadyExists (same ALREADY_EXISTS condition)",
	},
}

func TestFirestore_DifferentialVsEmulator(t *testing.T) {
	t.Parallel()
	emuHost, stop := startFirestoreEmulator(t)
	defer stop()

	simC := newFSClient(t, "fs-diff")
	oracle, err := firestore.NewClient(ctx, "fs-diff",
		option.WithEndpoint(emuHost),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	if err != nil {
		t.Fatalf("emulator client: %v", err)
	}
	defer oracle.Close()

	for _, sc := range firestoreDiffScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			simRes := fsCapture(sc.run(simC, sc.name))
			// The scenario's own expectation is the primary assertion: it holds
			// whatever the emulator does.
			fsAssertEqual(t, "sim", sc.want(), simRes)

			oracleRes := fsCapture(sc.run(oracle, sc.name))
			if div, ok := fsDiffKnownDivergences[sc.name]; ok {
				fsAssertEqual(t, "sim (documented divergence: "+div.reason+")", div.sim, simRes)
				fsAssertEqual(t, "emulator (documented divergence: "+div.reason+")", div.oracle, oracleRes)
				return
			}
			if simRes != oracleRes {
				t.Errorf("differential mismatch for %q:\n  sim:      %+v\n  emulator: %+v\n"+
					"If the sim is the MORE faithful one, add an fsDiffKnownDivergences entry (do not regress the sim).",
					sc.name, simRes, oracleRes)
			}
		})
	}
}

type fsDiffScenario struct {
	name string
	run  func(c *firestore.Client, name string) (map[string]any, error)
	// Exactly one of wantData and wantErrCode is set.
	wantData    map[string]any
	wantErrCode string
}

func (s fsDiffScenario) want() fsDiffResult {
	if s.wantErrCode != "" {
		return fsDiffResult{errCode: s.wantErrCode}
	}
	return fsDiffResult{value: fsCanon(s.wantData)}
}

func fsCapture(data map[string]any, err error) fsDiffResult {
	if err != nil {
		return fsDiffResult{errCode: status.Code(err).String()}
	}
	return fsDiffResult{value: fsCanon(data)}
}

func fsAssertEqual(t *testing.T, side string, want, got fsDiffResult) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %+v, got %+v", side, want, got)
	}
}

func firestoreDiffScenarios() []fsDiffScenario {
	return []fsDiffScenario{
		{
			name: "set-get-roundtrip",
			run: func(c *firestore.Client, name string) (map[string]any, error) {
				doc := c.Collection(name).Doc("d1")
				if _, err := doc.Set(ctx, map[string]any{"name": "alice", "age": int64(30), "active": true}); err != nil {
					return nil, err
				}
				snap, err := doc.Get(ctx)
				if err != nil {
					return nil, err
				}
				return snap.Data(), nil
			},
			wantData: map[string]any{"name": "alice", "age": int64(30), "active": true},
		},
		{
			name: "increment-transform",
			run: func(c *firestore.Client, name string) (map[string]any, error) {
				doc := c.Collection(name).Doc("d1")
				if _, err := doc.Set(ctx, map[string]any{"n": int64(10)}); err != nil {
					return nil, err
				}
				if _, err := doc.Update(ctx, []firestore.Update{{Path: "n", Value: firestore.Increment(5)}}); err != nil {
					return nil, err
				}
				snap, err := doc.Get(ctx)
				if err != nil {
					return nil, err
				}
				return snap.Data(), nil
			},
			// Increment adds to the stored value; it does not replace it.
			wantData: map[string]any{"n": int64(15)},
		},
		{
			name: "array-union-remove",
			run: func(c *firestore.Client, name string) (map[string]any, error) {
				doc := c.Collection(name).Doc("d1")
				if _, err := doc.Set(ctx, map[string]any{"tags": []string{"a", "b"}}); err != nil {
					return nil, err
				}
				// ArrayUnion appends only the elements not already present.
				if _, err := doc.Update(ctx, []firestore.Update{
					{Path: "tags", Value: firestore.ArrayUnion("b", "c")},
				}); err != nil {
					return nil, err
				}
				// ArrayRemove drops every occurrence of each element it names,
				// and ignores one the array does not hold.
				if _, err := doc.Update(ctx, []firestore.Update{
					{Path: "tags", Value: firestore.ArrayRemove("a", "absent")},
				}); err != nil {
					return nil, err
				}
				snap, err := doc.Get(ctx)
				if err != nil {
					return nil, err
				}
				return snap.Data(), nil
			},
			wantData: map[string]any{"tags": []any{"b", "c"}},
		},
		{
			name: "transaction-read-modify-write",
			run: func(c *firestore.Client, name string) (map[string]any, error) {
				doc := c.Collection(name).Doc("d1")
				if _, err := doc.Set(ctx, map[string]any{"balance": int64(100)}); err != nil {
					return nil, err
				}
				err := c.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
					snap, err := tx.Get(doc)
					if err != nil {
						return err
					}
					bal, err := snap.DataAt("balance")
					if err != nil {
						return err
					}
					return tx.Set(doc, map[string]any{"balance": bal.(int64) - 30})
				})
				if err != nil {
					return nil, err
				}
				snap, err := doc.Get(ctx)
				if err != nil {
					return nil, err
				}
				return snap.Data(), nil
			},
			// The transaction body read 100 and committed 100-30.
			wantData: map[string]any{"balance": int64(70)},
		},
		{
			name: "update-missing-precondition",
			run: func(c *firestore.Client, name string) (map[string]any, error) {
				// Update emits currentDocument.exists=true, so a missing doc is
				// a precondition failure reported as NotFound.
				_, err := c.Collection(name).Doc("nope").Update(ctx, []firestore.Update{{Path: "x", Value: 1}})
				return nil, err
			},
			wantErrCode: "NotFound",
		},
		{
			name: "create-on-existing",
			run: func(c *firestore.Client, name string) (map[string]any, error) {
				doc := c.Collection(name).Doc("d1")
				if _, err := doc.Create(ctx, map[string]any{"n": int64(1)}); err != nil {
					return nil, err
				}
				_, err := doc.Create(ctx, map[string]any{"n": int64(2)}) // already exists
				return nil, err
			},
			// The 409 ALREADY_EXISTS reaches the REST client as Aborted (see
			// fsDiffKnownDivergences).
			wantErrCode: "Aborted",
		},
	}
}

// fsCanon renders numbers through big.Rat, so int64 1 and float64 1.0 compare
// equal, and timestamps as a presence sentinel, since server timestamps vary.
func fsCanon(data map[string]any) string {
	b, _ := json.Marshal(fsCanonValue(data))
	return string(b)
}

func fsCanonValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([][2]any, 0, len(t))
		for _, k := range keys {
			out = append(out, [2]any{k, fsCanonValue(t[k])})
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = fsCanonValue(e)
		}
		return out
	case int64:
		return new(big.Rat).SetInt64(t).RatString()
	case float64:
		return new(big.Rat).SetFloat64(t).RatString()
	case time.Time:
		return "<timestamp>"
	default:
		return t
	}
}

// requireFirestoreEmulatorJava fails with the real reason when the Java on
// PATH is too old; otherwise the emulator refuses in its own output and the
// failure reads as a port timeout ninety seconds later.
func requireFirestoreEmulatorJava(t *testing.T) {
	t.Helper()
	javaPath, err := exec.LookPath("java")
	if err != nil {
		t.Fatalf("the Cloud Firestore emulator runs on Java and none is on PATH; install a Java 21+ runtime (the gcp CI job provides one): %v", err)
	}
	// `java -version` writes to stderr on every release that ever shipped.
	out, err := exec.Command(javaPath, "-version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s -version failed, so the Java the Cloud Firestore emulator would run on cannot be identified: %v\n%s", javaPath, err, out)
	}
	version := regexp.MustCompile(`version "(\d+)`).FindSubmatch(out)
	if version == nil {
		t.Fatalf("could not read a version out of %s -version; the Cloud Firestore emulator requires Java 21 or newer:\n%s", javaPath, out)
	}
	major, err := strconv.Atoi(string(version[1]))
	if err != nil {
		t.Fatalf("could not read a version out of %s -version: %v\n%s", javaPath, err, out)
	}
	if major < 21 {
		t.Fatalf("the Cloud Firestore emulator requires Java 21 or newer and %s is Java %d; install a newer runtime (the gcp CI job installs Temurin 25):\n%s",
			javaPath, major, out)
	}
}

// startFirestoreEmulator launches Google's Firestore emulator through gcloud.
// CI installs the Google Cloud CLI, the cloud-firestore-emulator component and
// a Java 21 or newer runtime; each is required, so a missing one fails loud.
func startFirestoreEmulator(t *testing.T) (host string, stop func()) {
	t.Helper()
	if _, err := exec.LookPath("gcloud"); err != nil {
		t.Fatalf("gcloud is required for the Firestore emulator differential test; install the Google Cloud CLI, the cloud-firestore-emulator component, and a Java 21+ runtime (the gcp CI job provides all three): %v", err)
	}
	requireFirestoreEmulatorJava(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	host = fmt.Sprintf("127.0.0.1:%d", port)

	cmd := exec.Command("gcloud", "beta", "emulators", "firestore", "start",
		"--host-port="+host, "--quiet")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not launch the Firestore emulator; install the cloud-firestore-emulator gcloud component and a Java 21+ runtime (the gcp CI job provides both): %v", err)
	}
	stop = func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		conn, derr := net.DialTimeout("tcp", host, time.Second)
		if derr == nil {
			conn.Close()
			return host, stop
		}
		time.Sleep(500 * time.Millisecond)
	}
	stop()
	t.Fatalf("Firestore emulator did not open %s in time; its output was:\n%s", host, out.String())
	return "", func() {}
}

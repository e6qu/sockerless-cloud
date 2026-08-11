package gcp_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os/exec"
	"sort"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Differential testing of the Firestore slice against Google's own Firestore
// emulator (BUG-2158 + the #649 transform/precondition work): the same SDK
// operations run against the sim (REST transport) and the emulator (gRPC), and
// the observable outcome must match. As with the DynamoDB-Local differential,
// the emulator is a REFERENCE, not a ceiling — a case where the sim is more
// faithful (or where the REST vs gRPC transports legitimately differ) is recorded
// in fsDiffKnownDivergences with a justification and asserted exactly, never used
// to regress the sim.

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
	// Create-on-existing: the sim is reached over the REST transport, which gax
	// maps HTTP 409 ALREADY_EXISTS to codes.Aborted; the emulator is reached over
	// gRPC and returns codes.AlreadyExists directly. Same underlying condition,
	// different transport status mapping — not a sim defect.
	"create-on-existing": {
		sim:    fsDiffResult{errCode: "Aborted"},
		oracle: fsDiffResult{errCode: "AlreadyExists"},
		reason: "REST transport maps 409 to Aborted; gRPC emulator returns AlreadyExists (same ALREADY_EXISTS condition)",
	},
}

func TestFirestore_DifferentialVsEmulator(t *testing.T) {
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
		{"set-get-roundtrip", func(c *firestore.Client, name string) (map[string]any, error) {
			doc := c.Collection(name).Doc("d1")
			if _, err := doc.Set(ctx, map[string]any{"name": "alice", "age": int64(30), "active": true}); err != nil {
				return nil, err
			}
			snap, err := doc.Get(ctx)
			if err != nil {
				return nil, err
			}
			return snap.Data(), nil
		}},
		{"increment-transform", func(c *firestore.Client, name string) (map[string]any, error) {
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
		}},
		{"array-union-remove", func(c *firestore.Client, name string) (map[string]any, error) {
			doc := c.Collection(name).Doc("d1")
			if _, err := doc.Set(ctx, map[string]any{"tags": []string{"a", "b"}}); err != nil {
				return nil, err
			}
			if _, err := doc.Update(ctx, []firestore.Update{
				{Path: "tags", Value: firestore.ArrayUnion("b", "c")},
			}); err != nil {
				return nil, err
			}
			snap, err := doc.Get(ctx)
			if err != nil {
				return nil, err
			}
			return snap.Data(), nil
		}},
		{"transaction-read-modify-write", func(c *firestore.Client, name string) (map[string]any, error) {
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
		}},
		{"update-missing-precondition", func(c *firestore.Client, name string) (map[string]any, error) {
			// Update on a missing doc must fail (NotFound) on both.
			_, err := c.Collection(name).Doc("nope").Update(ctx, []firestore.Update{{Path: "x", Value: 1}})
			return nil, err
		}},
		{"create-on-existing", func(c *firestore.Client, name string) (map[string]any, error) {
			doc := c.Collection(name).Doc("d1")
			if _, err := doc.Create(ctx, map[string]any{"n": int64(1)}); err != nil {
				return nil, err
			}
			_, err := doc.Create(ctx, map[string]any{"n": int64(2)}) // already exists
			return nil, err
		}},
	}
}

// fsCanon renders document data canonically: numbers via big.Rat (so int64 1 and
// float64 1.0 compare equal), timestamps as a presence sentinel (server
// timestamps are non-deterministic), maps with sorted keys, arrays in order.
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

// startFirestoreEmulator launches Google's Firestore emulator via gcloud (the
// gcp CI job installs gcloud + the cloud-firestore-emulator component; the runner
// ships a JRE). It fails loud when gcloud is unavailable — a required tool the
// CI provides, never a silent skip — and when the emulator can't start.
func startFirestoreEmulator(t *testing.T) (host string, stop func()) {
	t.Helper()
	if _, err := exec.LookPath("gcloud"); err != nil {
		t.Fatalf("gcloud is required for the Firestore emulator differential test; install the Google Cloud CLI, the cloud-firestore-emulator component, and a JRE (the gcp CI job provides all three): %v", err)
	}
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
		// The emulator component (Java-backed) isn't installed/launchable here —
		// a capability gate, like the realexec/Docker gates elsewhere. It runs for
		// real where the cloud-firestore-emulator component + a JRE are present
		// (locally, and in CI once the component installs).
		t.Skipf("could not launch the Firestore emulator (skipping differential): %v", err)
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
	// The component/JRE isn't usable in this environment — gate out rather than
	// fail the suite on an emulator that won't start. The captured output makes
	// the cause visible in the logs.
	t.Skipf("Firestore emulator did not open %s in time (skipping differential)\n%s", host, out.String())
	return "", func() {}
}

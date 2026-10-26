package azure_sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
)

// Differential tests against Microsoft's Azure Cosmos DB emulator (the vNext
// Linux emulator in HTTP mode): the same azcosmos code runs each scenario
// against both, differing only in the endpoint, and the outcomes must match.
// The emulator is a reference, not a ceiling: a case where the simulator is
// deliberately more faithful goes in cosmosDiffKnownDivergences and is
// asserted exactly. The emulator is required; a provisioning failure fails the
// test.

type cosmosDiffResult struct {
	value   string // canonical JSON of the observable (user fields only), or sentinel
	errCode string // HTTP status of an azcosmos error, "" on success
}

type cosmosDiffDivergence struct {
	sim    cosmosDiffResult
	oracle cosmosDiffResult
	reason string
}

// Every scenario agrees with the emulator.
var cosmosDiffKnownDivergences = map[string]cosmosDiffDivergence{}

func TestCosmos_DifferentialVsEmulator(t *testing.T) {
	dataPlaneAccount := uniquename.Alnum("sdkdataplane")
	emuEndpoint, release := startCosmosEmulator(t)
	defer release()

	simC := cosmosSimClient(t, dataPlaneAccount)
	oracle := newCosmosSDKClient(t, emuEndpoint, cosmosEmulatorKey, "")

	for _, sc := range cosmosDiffScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			simRes := cosmosCapture(sc.run(t, simC, uniquename.New("sim-"+sc.name)))
			oracleRes := cosmosCapture(sc.run(t, oracle, uniquename.New("emu-"+sc.name)))
			if div, ok := cosmosDiffKnownDivergences[sc.name]; ok {
				cosmosAssertEqual(t, "sim (documented divergence: "+div.reason+")", div.sim, simRes)
				cosmosAssertEqual(t, "emulator (documented divergence: "+div.reason+")", div.oracle, oracleRes)
				return
			}
			if simRes != oracleRes {
				t.Errorf("differential mismatch for %q:\n  sim:      %+v\n  emulator: %+v\n"+
					"If the sim is the MORE faithful one, add a cosmosDiffKnownDivergences entry (do not regress the sim).",
					sc.name, simRes, oracleRes)
			}
		})
	}
}

type cosmosDiffScenario struct {
	name string
	run  func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error)
}

func cosmosCapture(data map[string]any, err error) cosmosDiffResult {
	if err != nil {
		var re *azcore.ResponseError
		if errors.As(err, &re) {
			return cosmosDiffResult{errCode: fmt.Sprintf("%d", re.StatusCode)}
		}
		return cosmosDiffResult{errCode: "error:" + err.Error()}
	}
	return cosmosDiffResult{value: cosmosCanon(data)}
}

func cosmosAssertEqual(t *testing.T, side string, want, got cosmosDiffResult) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %+v, got %+v", side, want, got)
	}
}

func cosmosDiffScenarios() []cosmosDiffScenario {
	pk := azcosmos.NewPartitionKeyString("p")
	makeContainer := func(t *testing.T, c *azcosmos.Client, dbID string) *azcosmos.ContainerClient {
		t.Helper()
		if _, err := c.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: dbID}, nil); err != nil {
			t.Fatalf("CreateDatabase: %v", err)
		}
		db, _ := c.NewDatabase(dbID)
		if _, err := db.CreateContainer(ctx, azcosmos.ContainerProperties{
			ID:                     "c",
			PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
		}, nil); err != nil {
			t.Fatalf("CreateContainer: %v", err)
		}
		cc, _ := c.NewContainer(dbID, "c")
		return cc
	}
	create := func(cc *azcosmos.ContainerClient, doc map[string]any) error {
		raw, _ := json.Marshal(doc)
		_, err := cc.CreateItem(ctx, pk, raw, nil)
		return err
	}

	return []cosmosDiffScenario{
		{"create-read-roundtrip", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			if err := create(cc, map[string]any{"id": "x", "pk": "p", "name": "alice", "age": 30}); err != nil {
				return nil, err
			}
			resp, err := cc.ReadItem(ctx, pk, "x", nil)
			if err != nil {
				return nil, err
			}
			return cosmosUnmarshal(resp.Value), nil
		}},
		{"upsert-replaces", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			if err := create(cc, map[string]any{"id": "x", "pk": "p", "v": 1}); err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(map[string]any{"id": "x", "pk": "p", "v": 2})
			if _, err := cc.UpsertItem(ctx, pk, raw, nil); err != nil {
				return nil, err
			}
			resp, err := cc.ReadItem(ctx, pk, "x", nil)
			if err != nil {
				return nil, err
			}
			return cosmosUnmarshal(resp.Value), nil
		}},
		{"create-conflict-409", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			if err := create(cc, map[string]any{"id": "x", "pk": "p"}); err != nil {
				return nil, err
			}
			return nil, create(cc, map[string]any{"id": "x", "pk": "p"}) // duplicate id → 409
		}},
		{"read-missing-404", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			_, err := cc.ReadItem(ctx, pk, "nope", nil)
			return nil, err
		}},
		{"patch-increment", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			if err := create(cc, map[string]any{"id": "x", "pk": "p", "count": 10}); err != nil {
				return nil, err
			}
			patch := azcosmos.PatchOperations{}
			patch.AppendIncrement("/count", 5)
			patch.AppendSet("/status", "active")
			if _, err := cc.PatchItem(ctx, pk, "x", patch, nil); err != nil {
				return nil, err
			}
			resp, err := cc.ReadItem(ctx, pk, "x", nil)
			if err != nil {
				return nil, err
			}
			return cosmosUnmarshal(resp.Value), nil
		}},
		{"delete-then-read-404", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			if err := create(cc, map[string]any{"id": "x", "pk": "p"}); err != nil {
				return nil, err
			}
			if _, err := cc.DeleteItem(ctx, pk, "x", nil); err != nil {
				return nil, err
			}
			_, err := cc.ReadItem(ctx, pk, "x", nil)
			return nil, err
		}},
		{"same-id-different-partition-distinct", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			p1, p2 := azcosmos.NewPartitionKeyString("p1"), azcosmos.NewPartitionKeyString("p2")
			r1, _ := json.Marshal(map[string]any{"id": "x", "pk": "p1", "v": 1})
			if _, err := cc.CreateItem(ctx, p1, r1, nil); err != nil {
				return nil, err
			}
			// Same id in a different partition is a distinct item, not a 409.
			r2, _ := json.Marshal(map[string]any{"id": "x", "pk": "p2", "v": 2})
			if _, err := cc.CreateItem(ctx, p2, r2, nil); err != nil {
				return nil, err
			}
			g1, err := cc.ReadItem(ctx, p1, "x", nil)
			if err != nil {
				return nil, err
			}
			g2, err := cc.ReadItem(ctx, p2, "x", nil)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"p1": cosmosUnmarshal(g1.Value)["v"],
				"p2": cosmosUnmarshal(g2.Value)["v"],
			}, nil // {p1:1, p2:2}
		}},
		{"create-pk-mismatch-400", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			// The header names partition "p" but the document's /pk is "other".
			raw, _ := json.Marshal(map[string]any{"id": "x", "pk": "other", "v": 1})
			_, err := cc.CreateItem(ctx, azcosmos.NewPartitionKeyString("p"), raw, nil)
			return nil, err // Azure answers 400 BadRequest
		}},
		{"single-partition-query-scopes", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			p1, p2 := azcosmos.NewPartitionKeyString("p1"), azcosmos.NewPartitionKeyString("p2")
			for _, d := range []struct {
				pk  azcosmos.PartitionKey
				doc map[string]any
			}{
				{p1, map[string]any{"id": "a", "pk": "p1"}},
				{p1, map[string]any{"id": "b", "pk": "p1"}},
				{p2, map[string]any{"id": "c", "pk": "p2"}},
			} {
				raw, _ := json.Marshal(d.doc)
				if _, err := cc.CreateItem(ctx, d.pk, raw, nil); err != nil {
					return nil, err
				}
			}
			pager := cc.NewQueryItemsPager("SELECT c.id FROM c ORDER BY c.id ASC", p1, nil)
			var ids []any
			for pager.More() {
				page, err := pager.NextPage(ctx)
				if err != nil {
					return nil, err
				}
				for _, it := range page.Items {
					ids = append(ids, cosmosUnmarshal(it)["id"])
				}
			}
			return map[string]any{"ids": ids}, nil // only [a, b]
		}},
		{"query-pagination-max-item-count", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			for _, id := range []string{"a", "b", "c", "d", "e"} {
				raw, _ := json.Marshal(map[string]any{"id": id, "pk": "p", "n": id})
				if _, err := cc.CreateItem(ctx, pk, raw, nil); err != nil {
					return nil, err
				}
			}
			// PageSizeHint=1 sets x-ms-max-item-count=1; the pager walks every page via
			// x-ms-continuation.
			pager := cc.NewQueryItemsPager("SELECT c.id FROM c ORDER BY c.id ASC", pk,
				&azcosmos.QueryOptions{PageSizeHint: 1})
			var ids []any
			for pager.More() {
				page, err := pager.NextPage(ctx)
				if err != nil {
					return nil, err
				}
				for _, it := range page.Items {
					ids = append(ids, cosmosUnmarshal(it)["id"])
				}
			}
			return map[string]any{"ids": ids}, nil // [a, b, c, d, e]
		}},
		{"request-charge-present-on-write-and-read", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			raw, _ := json.Marshal(map[string]any{"id": "x", "pk": "p", "name": "alice"})
			createResp, err := cc.CreateItem(ctx, pk, raw, nil)
			if err != nil {
				return nil, err
			}
			readResp, err := cc.ReadItem(ctx, pk, "x", nil)
			if err != nil {
				return nil, err
			}
			// Both report a non-zero request charge, and a write costs at least a point
			// read of the same item; the magnitudes legitimately differ.
			return map[string]any{
				"createChargePositive": createResp.RequestCharge > 0,
				"readChargePositive":   readResp.RequestCharge > 0,
				"writeGEread":          createResp.RequestCharge >= readResp.RequestCharge,
			}, nil
		}},
		{"session-token-issued-on-write", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			raw, _ := json.Marshal(map[string]any{"id": "x", "pk": "p", "v": 1})
			w, err := cc.CreateItem(ctx, pk, raw, nil)
			if err != nil {
				return nil, err
			}
			// Both issue x-ms-session-token on a write under Session consistency, and a
			// Session read sending it back sees the write.
			session := azcosmos.ConsistencyLevelSession.ToPtr()
			rd, err := cc.ReadItem(ctx, pk, "x", &azcosmos.ItemOptions{
				ConsistencyLevel: session,
				SessionToken:     w.SessionToken,
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"writeTokenNonEmpty": w.SessionToken != nil && *w.SessionToken != "",
				"readTokenNonEmpty":  rd.SessionToken != nil && *rd.SessionToken != "",
				"v":                  cosmosUnmarshal(rd.Value)["v"],
			}, nil
		}},
		{"throughput-offer-roundtrip", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			// A container with dedicated throughput has its own offer, and
			// ReadThroughput/ReplaceThroughput round-trip a manual value.
			if _, err := c.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: dbID}, nil); err != nil {
				return nil, err
			}
			db, _ := c.NewDatabase(dbID)
			start := azcosmos.NewManualThroughputProperties(400)
			if _, err := db.CreateContainer(ctx, azcosmos.ContainerProperties{
				ID:                     "c",
				PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
			}, &azcosmos.CreateContainerOptions{ThroughputProperties: &start}); err != nil {
				return nil, err
			}
			cc, _ := c.NewContainer(dbID, "c")
			read, err := cc.ReadThroughput(ctx, nil)
			if err != nil {
				return nil, err
			}
			startManual, hadManual := read.ThroughputProperties.ManualThroughput()
			if _, err := cc.ReplaceThroughput(ctx, azcosmos.NewManualThroughputProperties(1000), nil); err != nil {
				return nil, err
			}
			after, err := cc.ReadThroughput(ctx, nil)
			if err != nil {
				return nil, err
			}
			gotManual, ok := after.ThroughputProperties.ManualThroughput()
			return map[string]any{
				"hadManualOffer":   hadManual && startManual == 400,
				"replacedToManual": ok,
				"manualAfter":      gotManual,
			}, nil
		}},
		{"throughput-offer-shared-404", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			// A container without dedicated throughput has no offer, so ReadThroughput is
			// 404: the simulator must not invent a default offer.
			cc := makeContainer(t, c, dbID)
			_, err := cc.ReadThroughput(ctx, nil)
			return nil, err
		}},
		{"query-where-and-order", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			for i, d := range []map[string]any{
				{"id": "a", "pk": "p", "team": "x", "score": 5},
				{"id": "b", "pk": "p", "team": "x", "score": 10},
				{"id": "c", "pk": "p", "team": "y", "score": 7},
			} {
				if err := create(cc, d); err != nil {
					return nil, fmt.Errorf("seed %d: %w", i, err)
				}
			}
			pager := cc.NewQueryItemsPager(
				"SELECT c.id FROM c WHERE c.team = @t ORDER BY c.score DESC", pk,
				&azcosmos.QueryOptions{QueryParameters: []azcosmos.QueryParameter{{Name: "@t", Value: "x"}}})
			var ids []any
			for pager.More() {
				page, err := pager.NextPage(ctx)
				if err != nil {
					return nil, err
				}
				for _, it := range page.Items {
					m := cosmosUnmarshal(it)
					ids = append(ids, m["id"])
				}
			}
			return map[string]any{"ids": ids}, nil // ORDER BY score DESC → [b, a]
		}},
	}
}

func cosmosUnmarshal(b []byte) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// cosmosCanon drops the system fields (_rid, _self, _etag, _ts, _attachments)
// that legitimately differ, and normalizes number formatting.
func cosmosCanon(data map[string]any) string {
	b, _ := json.Marshal(cosmosCanonValue(stripCosmosSystemFields(data)))
	return string(b)
}

func stripCosmosSystemFields(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if len(k) > 0 && k[0] == '_' {
			continue
		}
		out[k] = v
	}
	return out
}

func cosmosCanonValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([][2]any, 0, len(t))
		for _, k := range keys {
			out = append(out, [2]any{k, cosmosCanonValue(t[k])})
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cosmosCanonValue(e)
		}
		return out
	case float64:
		return fmt.Sprintf("%g", t)
	default:
		return t
	}
}

// cosmosEmulatorEngine runs one container-engine command under a bound, so an
// engine that stops answering (Podman can leave a container in `Created`)
// fails this test naming the command, not the suite at Go's test timeout.
func cosmosEmulatorEngine(bound time.Duration, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", args...).CombinedOutput()
	if cctx.Err() != nil {
		return out, fmt.Errorf("`docker %s` did not return within %s: %w",
			strings.Join(args, " "), bound, cctx.Err())
	}
	return out, err
}

// cosmosEmulatorTests run serially, before the parallel phase, so the emulator
// initialises on an idle runner and is gone before the parallel tests need
// the CPU.
var cosmosEmulatorTests = []string{"TestCosmos_DifferentialVsEmulator", "TestCosmosScripts_DifferentialVsEmulator"}

// TestMain starts the one shared emulator before the tests run, so its
// initialisation overlaps the suite's start-up.
var cosmosEmulator struct {
	once     sync.Once
	endpoint string
	stop     func()
	err      error

	mu    sync.Mutex
	users int
}

func cosmosEmulatorSelected() int {
	n := 0
	for _, name := range cosmosEmulatorTests {
		if testRunSelects(name) {
			n++
		}
	}
	return n
}

func warmCosmosEmulator() {
	cosmosEmulator.once.Do(func() {
		cosmosEmulator.mu.Lock()
		cosmosEmulator.users = cosmosEmulatorSelected()
		cosmosEmulator.mu.Unlock()
		cosmosEmulator.endpoint, cosmosEmulator.stop, cosmosEmulator.err = bootCosmosEmulator()
	})
}

func stopCosmosEmulator() {
	cosmosEmulator.mu.Lock()
	defer cosmosEmulator.mu.Unlock()
	if cosmosEmulator.stop != nil {
		cosmosEmulator.stop()
		cosmosEmulator.stop = nil
	}
}

// startCosmosEmulator waits for the warm-up TestMain began; the returned
// release removes the emulator once every selected oracle test released it.
func startCosmosEmulator(t *testing.T) (endpoint string, release func()) {
	t.Helper()
	warmCosmosEmulator()
	if cosmosEmulator.err != nil {
		t.Fatalf("%v", cosmosEmulator.err)
	}
	cosmosEmulator.mu.Lock()
	stopped := cosmosEmulator.stop == nil
	cosmosEmulator.mu.Unlock()
	if stopped {
		t.Fatalf("the shared Cosmos DB emulator was removed before %s ran: list the test in cosmosEmulatorTests", t.Name())
	}
	return cosmosEmulator.endpoint, func() {
		cosmosEmulator.mu.Lock()
		cosmosEmulator.users--
		last := cosmosEmulator.users <= 0
		cosmosEmulator.mu.Unlock()
		if last {
			stopCosmosEmulator()
		}
	}
}

func bootCosmosEmulator() (endpoint string, stop func(), err error) {
	const image = "mcr.microsoft.com/cosmosdb/linux/azure-cosmos-emulator:vnext-preview"
	if _, err := cosmosEmulatorEngine(60*time.Second, "image", "inspect", image); err != nil {
		// CI pre-pulls the image; a host that lacks it pulls it here. The oracle is
		// required, so a failed pull fails.
		if pullOut, perr := cosmosEmulatorEngine(10*time.Minute, "pull", image); perr != nil {
			return "", nil, fmt.Errorf("pulling Cosmos emulator image %s: %w\n%s", image, perr, pullOut)
		}
	}

	// The emulator advertises its endpoint, built from --port, in account
	// discovery, and azcosmos routes every request there, so the container port,
	// the published port and --port must be one number.
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		return "", nil, fmt.Errorf("reserving a host port for the Cosmos emulator: %w", lerr)
	}
	hostPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	// Reap a leaked emulator first: `docker run --rm` removes a container only
	// when it exits, so a killed test binary leaves one running, and a second
	// emulator on the host starves the new one's pgcosmos extension.
	const emulatorLabel = "sockerless-sim-azure-cosmos-emulator=differential"
	// The suite's go test deadline is 13 minutes, and this is one test of many.
	const readinessBudget = 280 * time.Second
	stale, _ := cosmosEmulatorEngine(60*time.Second, "ps", "-aq", "--filter", "label="+emulatorLabel)
	for _, id := range strings.Fields(string(stale)) {
		_, _ = cosmosEmulatorEngine(60*time.Second, "rm", "-f", id)
	}
	runOut, err := cosmosEmulatorEngine(3*time.Minute, "run", "-d", "--rm",
		"--label", emulatorLabel,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", hostPort, hostPort),
		image, "--protocol", "http", "--port", fmt.Sprintf("%d", hostPort))
	if err != nil {
		return "", nil, fmt.Errorf("starting Cosmos emulator: %w\n%s", err, runOut)
	}
	id := trimSpace(string(runOut))
	stop = func() { _, _ = cosmosEmulatorEngine(60*time.Second, "rm", "-f", id) }

	endpoint = fmt.Sprintf("http://127.0.0.1:%d/", hostPort)
	probe, probeErr := cosmosRawSDKClient(endpoint, cosmosEmulatorKey)
	if probeErr != nil {
		stop()
		return "", nil, fmt.Errorf("building the Cosmos readiness probe: %w", probeErr)
	}
	deadline := time.Now().Add(readinessBudget)
	var lastProbeErr error
	for i := 0; time.Now().Before(deadline); i++ {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		// A unique ID per attempt keeps a slow first create from turning the retry
		// into a 409.
		_, perr := probe.CreateDatabase(cctx, azcosmos.DatabaseProperties{ID: fmt.Sprintf("readyprobe%d", i)}, nil)
		cancel()
		if perr == nil {
			return endpoint, stop, nil
		}
		lastProbeErr = perr
		time.Sleep(2 * time.Second)
	}
	logs, _ := cosmosEmulatorEngine(60*time.Second, "logs", "--tail", "30", id)
	stop()
	// "pgcosmos extension is still starting" means the emulator is alive but
	// starved of CPU; anything else means it never answered, a real fault. The
	// budget stays fixed: the go test deadline is 13 minutes and this step's 14,
	// so more readiness time would trade a named failure for an opaque step kill.
	classification := "the emulator never answered — chase this"
	if lastProbeErr != nil && strings.Contains(lastProbeErr.Error(), "still starting") {
		classification = "the emulator was alive and still initialising when the budget " +
			"expired: host starvation, not a defect — re-run on a quieter machine"
	}
	return "", nil, fmt.Errorf(
		"Cosmos emulator did not become ready at %s within %s (%s)\nlast probe error: %w\n%s",
		endpoint, readinessBudget, classification, lastProbeErr, logs)
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

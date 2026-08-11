package azure_sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

// Differential testing of the Cosmos slice against Microsoft's own Cosmos DB
// emulator (the vNext Linux emulator in HTTP mode). The SAME azcosmos client code
// runs every scenario against the sim and the emulator, differing only in the
// endpoint, and the observable outcome must match. As with the DynamoDB-Local and
// Firestore differentials, the emulator is a REFERENCE, not a ceiling: a case
// where the sim is intentionally more faithful is recorded in
// cosmosDiffKnownDivergences and asserted exactly — the sim is never regressed to
// match the oracle.
//
// The emulator is large and slow to start, so this is Docker-gated: it runs for
// real where Docker + the image are available (CI pre-pulls it) and skips with
// diagnostics otherwise, never flaking the suite.

type cosmosDiffResult struct {
	value   string // canonical JSON of the observable (user fields only), or sentinel
	errCode string // HTTP status of an azcosmos error, "" on success
}

type cosmosDiffDivergence struct {
	sim    cosmosDiffResult
	oracle cosmosDiffResult
	reason string
}

// Empty today: every scenario below agrees with the emulator.
var cosmosDiffKnownDivergences = map[string]cosmosDiffDivergence{}

func TestCosmos_DifferentialVsEmulator(t *testing.T) {
	emuEndpoint, stop := startCosmosEmulator(t)
	defer stop()

	simC := newCosmosSDKClient(t, baseURL+"/")
	oracle := newCosmosSDKClient(t, emuEndpoint)

	for _, sc := range cosmosDiffScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			simRes := cosmosCapture(sc.run(t, simC, "sim-"+sc.name))
			oracleRes := cosmosCapture(sc.run(t, oracle, "emu-"+sc.name))
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
			// Same id "x" but a different partition — a DISTINCT item, not a 409.
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
			}, nil // distinct items → {p1:1, p2:2}
		}},
		{"create-pk-mismatch-400", func(t *testing.T, c *azcosmos.Client, dbID string) (map[string]any, error) {
			cc := makeContainer(t, c, dbID)
			// Header says partition "p" but the document's /pk value is "other".
			raw, _ := json.Marshal(map[string]any{"id": "x", "pk": "other", "v": 1})
			_, err := cc.CreateItem(ctx, azcosmos.NewPartitionKeyString("p"), raw, nil)
			return nil, err // real Cosmos → 400 BadRequest
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
			// A query scoped to partition p1 returns only p1's docs.
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
			// PageSizeHint=1 sets x-ms-max-item-count=1; the pager must walk every
			// page (via x-ms-continuation) and return the full ordered set.
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
			// The full ordered set must come back across pages (paged 1-at-a-time).
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
			// The emulator and the sim both report a non-zero RU charge on writes
			// and reads, and a write costs at least as much as a point read of the
			// same item. The exact RU value legitimately differs between the two, so
			// the agreed observable is the presence + the write≥read ordering — not
			// the magnitude.
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
			// Both the emulator and the sim issue an x-ms-session-token on a write
			// under the default Session consistency; a Session read that sends it
			// back observes the write (read-your-writes).
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
			// A container created WITH dedicated throughput has its own offer;
			// ReadThroughput/ReplaceThroughput must round-trip a manual value on both
			// the emulator and the sim. (A container WITHOUT dedicated throughput has
			// no offer → 404; covered by throughput-offer-shared-404 below.)
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
			// A container created WITHOUT dedicated throughput shares throughput and
			// has no offer; ReadThroughput returns 404 on both the emulator and the
			// sim. This guards that the sim does NOT fabricate a default offer.
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

// ── normalization ────────────────────────────────────────────────────────────

func cosmosUnmarshal(b []byte) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// cosmosCanon renders the observable comparably, dropping Cosmos system fields
// (_rid/_self/_etag/_ts/_attachments) that legitimately differ between the sim
// and the emulator, and normalizing numbers so int vs float formatting doesn't
// cause false mismatches.
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
		// JSON numbers decode as float64; render without trailing-zero noise.
		return fmt.Sprintf("%g", t)
	default:
		return t
	}
}

// ── emulator lifecycle ───────────────────────────────────────────────────────

func startCosmosEmulator(t *testing.T) (endpoint string, stop func()) {
	t.Helper()
	const image = "mcr.microsoft.com/cosmosdb/linux/azure-cosmos-emulator:vnext-preview"
	if exec.Command("docker", "image", "inspect", image).Run() != nil {
		// Do not pull a ~GB image inline; CI pre-pulls it. Skip with diagnostics.
		t.Skipf("Cosmos emulator image %s not present locally (CI pre-pulls it); skipping", image)
	}

	// The emulator advertises its data-plane endpoint as a hardcoded
	// http://127.0.0.1:8081/ in the account-discovery response; the azcosmos
	// global endpoint manager then routes every data-plane request there. So the
	// host MUST publish the container on port 8081 (a random host port would let
	// discovery succeed but every subsequent request would route to an unmapped
	// 8081 and hang). Skip cleanly if 8081 is already taken.
	const hostPort = 8081
	if ln, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", hostPort)); lerr != nil {
		t.Skipf("host port %d is in use (the Cosmos emulator advertises 127.0.0.1:%d for its data plane and requires it); skipping differential", hostPort, hostPort)
	} else {
		_ = ln.Close()
	}
	runOut, err := exec.Command("docker", "run", "-d", "--rm",
		"-p", fmt.Sprintf("127.0.0.1:%d:8081", hostPort),
		image, "--protocol", "http").CombinedOutput()
	if err != nil {
		t.Skipf("could not start Cosmos emulator (skipping differential): %v\n%s", err, runOut)
	}
	id := trimSpace(string(runOut))
	stop = func() { _ = exec.Command("docker", "rm", "-f", id).Run() }

	endpoint = fmt.Sprintf("http://127.0.0.1:%d/", hostPort)
	probe := newCosmosSDKClient(t, endpoint)
	deadline := time.Now().Add(280 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		// A unique db id per attempt so a successful create on a slow first
		// attempt isn't masked by a 409 "already exists" on the retry.
		_, perr := probe.CreateDatabase(cctx, azcosmos.DatabaseProperties{ID: fmt.Sprintf("readyprobe%d", i)}, nil)
		cancel()
		if perr == nil {
			return endpoint, stop
		}
		time.Sleep(2 * time.Second)
	}
	logs, _ := exec.Command("docker", "logs", "--tail", "30", id).CombinedOutput()
	stop()
	t.Skipf("Cosmos emulator did not become ready at %s (skipping differential)\n%s", endpoint, logs)
	return "", func() {}
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

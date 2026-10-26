package gcp_sdk_test

import (
	"sort"
	"testing"
	"time"

	"cloud.google.com/go/bigtable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestBigtable_DataPlane drives the Cloud Bigtable data API through
// `cloud.google.com/go/bigtable` over gRPC with the bootstrap credential.
func TestBigtable_DataPlane(t *testing.T) {
	t.Parallel()
	const (
		project  = "bt-data-proj"
		instance = "bt-data-inst"
		table    = "events"
		family   = "cf"
	)

	iac, err := bigtable.NewInstanceAdminClient(ctx, project, simGRPCClientOptions()...)
	require.NoError(t, err)
	defer iac.Close()
	require.NoError(t, iac.CreateInstance(ctx, &bigtable.InstanceConf{
		InstanceId:  instance,
		DisplayName: instance,
		ClusterId:   "c1",
		Zone:        "us-east1-b",
		NumNodes:    1,
	}))

	ac, err := bigtable.NewAdminClient(ctx, project, instance, simGRPCClientOptions()...)
	require.NoError(t, err)
	defer ac.Close()
	require.NoError(t, ac.CreateTable(ctx, table))
	require.NoError(t, ac.CreateColumnFamily(ctx, table, family))

	client, err := bigtable.NewClientWithConfig(ctx, project, instance, bigtableClientConfig, simGRPCClientOptions()...)
	require.NoError(t, err)
	defer client.Close()
	tbl := client.Open(table)

	mut := bigtable.NewMutation()
	mut.Set(family, "name", bigtable.Now(), []byte("alice"))
	require.NoError(t, tbl.Apply(ctx, "user#1", mut))

	row, err := tbl.ReadRow(ctx, "user#1")
	require.NoError(t, err)
	require.Len(t, row[family], 1)
	assert.Equal(t, family+":name", row[family][0].Column)
	assert.Equal(t, "alice", string(row[family][0].Value))

	// Multiple versions + LatestNFilter(1) keeps only the newest cell.
	for _, v := range []string{"v1", "v2", "v3"} {
		m := bigtable.NewMutation()
		m.Set(family, "ver", bigtable.Now(), []byte(v))
		require.NoError(t, tbl.Apply(ctx, "user#1", m))
	}
	row, err = tbl.ReadRow(ctx, "user#1", bigtable.RowFilter(bigtable.ChainFilters(
		bigtable.ColumnFilter("ver"), bigtable.LatestNFilter(1))))
	require.NoError(t, err)
	require.Len(t, row[family], 1, "LatestNFilter(1) must keep one cell")
	assert.Equal(t, "v3", string(row[family][0].Value))

	rmw := bigtable.NewReadModifyWrite()
	rmw.Increment(family, "count", 5)
	if _, err := tbl.ApplyReadModifyWrite(ctx, "user#1", rmw); err != nil {
		t.Fatalf("ApplyReadModifyWrite: %v", err)
	}
	rmw2 := bigtable.NewReadModifyWrite()
	rmw2.Increment(family, "count", 7)
	after, err := tbl.ApplyReadModifyWrite(ctx, "user#1", rmw2)
	require.NoError(t, err)
	var count int64
	for _, ri := range after[family] {
		if ri.Column == family+":count" {
			count = int64(bigEndian(ri.Value))
		}
	}
	assert.Equal(t, int64(12), count, "increment 5 then 7 = 12")

	// CheckAndMutateRow: predicate matches an existing cell → apply trueMutation.
	trueMut := bigtable.NewMutation()
	trueMut.Set(family, "verified", bigtable.Now(), []byte("yes"))
	cond := bigtable.NewCondMutation(bigtable.ColumnFilter("name"), trueMut, nil)
	require.NoError(t, tbl.Apply(ctx, "user#1", cond))
	row, err = tbl.ReadRow(ctx, "user#1", bigtable.RowFilter(bigtable.ColumnFilter("verified")))
	require.NoError(t, err)
	require.Len(t, row[family], 1)
	assert.Equal(t, "yes", string(row[family][0].Value))

	// Write a few keys and scan [user#1, user#3).
	for _, k := range []string{"user#2", "user#3"} {
		m := bigtable.NewMutation()
		m.Set(family, "name", bigtable.Now(), []byte(k))
		require.NoError(t, tbl.Apply(ctx, k, m))
	}
	var scanned []string
	err = tbl.ReadRows(ctx, bigtable.NewRange("user#1", "user#3"), func(r bigtable.Row) bool {
		scanned = append(scanned, r.Key())
		return true
	}, bigtable.RowFilter(bigtable.ColumnFilter("name")))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"user#1", "user#2"}, scanned, "[user#1,user#3) excludes user#3")

	del := bigtable.NewMutation()
	del.DeleteRow()
	require.NoError(t, tbl.Apply(ctx, "user#2", del))
	row, err = tbl.ReadRow(ctx, "user#2")
	require.NoError(t, err)
	assert.Nil(t, row, "deleted row must read back empty")
}

func bigEndian(b []byte) uint64 {
	var n uint64
	for _, x := range b {
		n = n<<8 | uint64(x)
	}
	return n
}

var btFilterCounter int

// btSetupDataPlane provisions an instance and table with the named column
// families; callers name them so every test owns a private keyspace.
func btSetupDataPlane(t *testing.T, project, instance, table string, families ...string) *bigtable.Table {
	t.Helper()

	iac, err := bigtable.NewInstanceAdminClient(ctx, project, simGRPCClientOptions()...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iac.Close() })
	require.NoError(t, iac.CreateInstance(ctx, &bigtable.InstanceConf{
		InstanceId:  instance,
		DisplayName: instance,
		ClusterId:   "c1",
		Zone:        "us-east1-b",
		NumNodes:    1,
	}))

	ac, err := bigtable.NewAdminClient(ctx, project, instance, simGRPCClientOptions()...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ac.Close() })
	require.NoError(t, ac.CreateTable(ctx, table))
	for _, fam := range families {
		require.NoError(t, ac.CreateColumnFamily(ctx, table, fam))
	}

	client, err := bigtable.NewClientWithConfig(ctx, project, instance, bigtableClientConfig, simGRPCClientOptions()...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client.Open(table)
}

// btCellStr flattens a row into "fam:qual=v@ts" strings ordered by family,
// qualifier and descending timestamp, so a subtest can assert exactly which
// cells survived.
func btCellStr(row bigtable.Row) []string {
	type cell struct {
		fam, col string
		ts       int64
		value    string
	}
	var cells []cell
	for fam, items := range row {
		for _, ri := range items {
			cells = append(cells, cell{fam: fam, col: ri.Column, ts: ri.Timestamp.Time().UnixMicro(), value: string(ri.Value)})
		}
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].fam != cells[j].fam {
			return cells[i].fam < cells[j].fam
		}
		if cells[i].col != cells[j].col {
			return cells[i].col < cells[j].col
		}
		return cells[i].ts > cells[j].ts
	})
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = c.fam + "/" + c.col + "=" + c.value
	}
	return out
}

// TestBigtable_DataPlane_Filters runs one subtest per RowFilter over
// deterministic cells and asserts which survive.
func TestBigtable_DataPlane_Filters(t *testing.T) {
	t.Parallel()
	const (
		project  = "bt-filt-proj"
		instance = "bt-filt-inst"
		table    = "filt"
	)

	// Row "r1" holds three versions of f1:a, one f1:b and one f2:a; the per-row
	// limit and offset filters slice the flattened order f1:a@3, f1:a@2, f1:a@1,
	// f1:b@3, f2:a@3.
	seed := func(t *testing.T, tbl *bigtable.Table) {
		t.Helper()
		for _, q := range []struct {
			fam, qual string
			ts        bigtable.Timestamp
			val       string
		}{
			{"f1", "a", 3000, "a3"},
			{"f1", "a", 2000, "a2"},
			{"f1", "a", 1000, "a1"},
			{"f1", "b", 3000, "b3"},
			{"f2", "a", 3000, "c3"},
		} {
			m := bigtable.NewMutation()
			m.Set(q.fam, q.qual, q.ts, []byte(q.val))
			require.NoError(t, tbl.Apply(ctx, "r1", m))
		}
	}

	newTable := func(t *testing.T) *bigtable.Table {
		// t.Name() carries '/', which Bigtable instance IDs refuse, so take the
		// keyspace from a counter.
		btFilterCounter++
		suffix := itoa(btFilterCounter)
		tbl := btSetupDataPlane(t, project, instance+suffix, table, "f1", "f2")
		seed(t, tbl)
		return tbl
	}

	readFiltered := func(t *testing.T, tbl *bigtable.Table, f bigtable.Filter) bigtable.Row {
		row, err := tbl.ReadRow(ctx, "r1", bigtable.RowFilter(f))
		require.NoError(t, err)
		return row
	}

	t.Run("PassAll", func(t *testing.T) {
		tbl := newTable(t)
		row := readFiltered(t, tbl, bigtable.PassAllFilter())
		assert.ElementsMatch(t, []string{
			"f1/f1:a=a3", "f1/f1:a=a2", "f1/f1:a=a1", "f1/f1:b=b3", "f2/f2:a=c3",
		}, btCellStr(row))
	})

	t.Run("BlockAll", func(t *testing.T) {
		tbl := newTable(t)
		row := readFiltered(t, tbl, bigtable.BlockAllFilter())
		assert.Empty(t, row, "BlockAllFilter keeps no cells")
	})

	t.Run("RowKeyFilter", func(t *testing.T) {
		tbl := newTable(t)
		row := readFiltered(t, tbl, bigtable.RowKeyFilter("^r1$"))
		assert.Len(t, btCellStr(row), 5, "row-key matches → all cells survive")
		// Seed a second row: a ReadRows scan over both yields only the matching one.
		m := bigtable.NewMutation()
		m.Set("f1", "a", 3000, []byte("other"))
		require.NoError(t, tbl.Apply(ctx, "other", m))
		var got []string
		require.NoError(t, tbl.ReadRows(ctx, bigtable.InfiniteRange(""), func(r bigtable.Row) bool {
			got = append(got, r.Key())
			return true
		}, bigtable.RowFilter(bigtable.RowKeyFilter("^r1$"))))
		assert.ElementsMatch(t, []string{"r1"}, got)
	})

	t.Run("FamilyFilter", func(t *testing.T) {
		tbl := newTable(t)
		row := readFiltered(t, tbl, bigtable.FamilyFilter("^f2$"))
		assert.ElementsMatch(t, []string{"f2/f2:a=c3"}, btCellStr(row))
	})

	t.Run("ColumnFilter", func(t *testing.T) {
		tbl := newTable(t)
		// Bigtable matches the qualifier regex against the whole qualifier, so "^b$"
		// keeps only "b".
		row := readFiltered(t, tbl, bigtable.ColumnFilter("^b$"))
		assert.ElementsMatch(t, []string{"f1/f1:b=b3"}, btCellStr(row))
	})

	t.Run("ValueFilter", func(t *testing.T) {
		tbl := newTable(t)
		// ValueFilter matches the whole value, so "a2" keeps exactly one cell.
		row := readFiltered(t, tbl, bigtable.ValueFilter("a2"))
		assert.ElementsMatch(t, []string{"f1/f1:a=a2"}, btCellStr(row))
	})

	t.Run("ColumnRangeFilter", func(t *testing.T) {
		tbl := newTable(t)
		// Range [a, b) on family f1 keeps qualifier "a" (all versions).
		row := readFiltered(t, tbl, bigtable.ColumnRangeFilter("f1", "a", "b"))
		assert.ElementsMatch(t, []string{"f1/f1:a=a3", "f1/f1:a=a2", "f1/f1:a=a1"}, btCellStr(row))
	})

	t.Run("TimestampRangeFilterMicros", func(t *testing.T) {
		tbl := newTable(t)
		// [2000, 3000) keeps the @2000 cells only.
		row := readFiltered(t, tbl, bigtable.TimestampRangeFilterMicros(2000, 3000))
		assert.ElementsMatch(t, []string{"f1/f1:a=a2"}, btCellStr(row))
	})

	t.Run("TimestampRangeFilter", func(t *testing.T) {
		tbl := newTable(t)
		// The same window as time.Time, which the client truncates to milliseconds:
		// @2000 becomes 2ms and @3000 3ms, so [2ms, 3ms) keeps only the @2000 cell.
		row := readFiltered(t, tbl, bigtable.TimestampRangeFilter(
			time.UnixMicro(2000), time.UnixMicro(3000)))
		assert.ElementsMatch(t, []string{"f1/f1:a=a2"}, btCellStr(row))
	})

	t.Run("ValueRangeFilter", func(t *testing.T) {
		tbl := newTable(t)
		// ValueRangeFilter is [start, end): ["a1", "a9") keeps the three f1:a cells
		// and drops b3 and c3.
		row := readFiltered(t, tbl, bigtable.ValueRangeFilter([]byte("a1"), []byte("a9")))
		assert.ElementsMatch(t, []string{"f1/f1:a=a3", "f1/f1:a=a2", "f1/f1:a=a1"}, btCellStr(row))
	})

	t.Run("CellsPerRowLimitFilter", func(t *testing.T) {
		tbl := newTable(t)
		// First 2 cells in flattened order: f1:a@3, f1:a@2.
		row := readFiltered(t, tbl, bigtable.CellsPerRowLimitFilter(2))
		assert.ElementsMatch(t, []string{"f1/f1:a=a3", "f1/f1:a=a2"}, btCellStr(row))
	})

	t.Run("CellsPerRowOffsetFilter", func(t *testing.T) {
		tbl := newTable(t)
		// Skip the first 2 cells; remaining: f1:a@1, f1:b@3, f2:a@3.
		row := readFiltered(t, tbl, bigtable.CellsPerRowOffsetFilter(2))
		assert.ElementsMatch(t, []string{"f1/f1:a=a1", "f1/f1:b=b3", "f2/f2:a=c3"}, btCellStr(row))
	})

	t.Run("StripValueFilter", func(t *testing.T) {
		tbl := newTable(t)
		row := readFiltered(t, tbl, bigtable.StripValueFilter())
		for _, items := range row {
			for _, ri := range items {
				assert.Empty(t, ri.Value, "StripValueFilter clears every value")
			}
		}
		// Row structure survives (5 cells), only values are zeroed.
		assert.Len(t, btCellStr(row), 5)
	})

	t.Run("LabelFilter", func(t *testing.T) {
		tbl := newTable(t)
		row := readFiltered(t, tbl, bigtable.LabelFilter("tagged"))
		// LabelFilter is a transformer, not a predicate: it labels every cell
		// it passes and drops none, so the whole row survives.
		assert.ElementsMatch(t, []string{
			"f1/f1:a=a3", "f1/f1:a=a2", "f1/f1:a=a1", "f1/f1:b=b3", "f2/f2:a=c3",
		}, btCellStr(row))
		for _, items := range row {
			for _, ri := range items {
				assert.Contains(t, ri.Labels, "tagged")
			}
		}
	})

	t.Run("ChainFilters", func(t *testing.T) {
		tbl := newTable(t)
		// FamilyFilter(^f1$) then ColumnFilter(^a$) keeps f1:a (all versions).
		row := readFiltered(t, tbl, bigtable.ChainFilters(
			bigtable.FamilyFilter("^f1$"), bigtable.ColumnFilter("^a$")))
		assert.ElementsMatch(t, []string{"f1/f1:a=a3", "f1/f1:a=a2", "f1/f1:a=a1"}, btCellStr(row))
	})

	t.Run("InterleaveFilters", func(t *testing.T) {
		tbl := newTable(t)
		// Union of qualifier "b" and family "f2" → f1:b + f2:a.
		row := readFiltered(t, tbl, bigtable.InterleaveFilters(
			bigtable.ColumnFilter("^b$"), bigtable.FamilyFilter("^f2$")))
		assert.ElementsMatch(t, []string{"f1/f1:b=b3", "f2/f2:a=c3"}, btCellStr(row))
	})

	t.Run("ConditionFilter", func(t *testing.T) {
		tbl := newTable(t)
		// ColumnFilter(^a$) matches, so FamilyFilter(^f2$) applies: only f2:a survives.
		row := readFiltered(t, tbl, bigtable.ConditionFilter(
			bigtable.ColumnFilter("^a$"),
			bigtable.FamilyFilter("^f2$"),
			nil,
		))
		assert.ElementsMatch(t, []string{"f2/f2:a=c3"}, btCellStr(row))
	})

	t.Run("RowSampleFilter_all", func(t *testing.T) {
		tbl := newTable(t)
		// p=1.0 keeps every row.
		row := readFiltered(t, tbl, bigtable.RowSampleFilter(1.0))
		assert.Len(t, btCellStr(row), 5)
	})

	t.Run("RowSampleFilter_none", func(t *testing.T) {
		tbl := newTable(t)
		// p=0.0 keeps no row.
		row := readFiltered(t, tbl, bigtable.RowSampleFilter(0.0))
		assert.Empty(t, row)
	})
}

// TestBigtable_DataPlane_Mutations covers DeleteCellsInColumn,
// DeleteCellsInFamily and DeleteTimestampRange.
func TestBigtable_DataPlane_Mutations(t *testing.T) {
	t.Parallel()
	const (
		project  = "bt-mut-proj"
		instance = "bt-mut-inst"
		table    = "mut"
	)

	t.Run("DeleteCellsInColumn", func(t *testing.T) {
		tbl := btSetupDataPlane(t, project, instance+"-delcol", table, "cf")
		// Three versions of cf:a plus one cf:b.
		for _, tv := range []struct {
			ts  bigtable.Timestamp
			val string
		}{{3000, "a3"}, {2000, "a2"}, {1000, "a1"}} {
			m := bigtable.NewMutation()
			m.Set("cf", "a", tv.ts, []byte(tv.val))
			require.NoError(t, tbl.Apply(ctx, "r", m))
		}
		mb := bigtable.NewMutation()
		mb.Set("cf", "b", 3000, []byte("b3"))
		require.NoError(t, tbl.Apply(ctx, "r", mb))

		del := bigtable.NewMutation()
		del.DeleteCellsInColumn("cf", "a")
		require.NoError(t, tbl.Apply(ctx, "r", del))

		row, err := tbl.ReadRow(ctx, "r")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"cf/cf:b=b3"}, btCellStr(row), "DeleteCellsInColumn removes all versions of cf:a only")
	})

	t.Run("DeleteCellsInFamily", func(t *testing.T) {
		tbl := btSetupDataPlane(t, project, instance+"-delfam", table, "cf", "other")
		require.NoError(t, tbl.Apply(ctx, "r", func() *bigtable.Mutation {
			m := bigtable.NewMutation()
			m.Set("cf", "a", 1000, []byte("a1"))
			m.Set("other", "x", 1000, []byte("x1"))
			return m
		}()))

		del := bigtable.NewMutation()
		del.DeleteCellsInFamily("cf")
		require.NoError(t, tbl.Apply(ctx, "r", del))

		row, err := tbl.ReadRow(ctx, "r")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"other/other:x=x1"}, btCellStr(row), "DeleteCellsInFamily removes only the named family")
	})

	t.Run("DeleteTimestampRange", func(t *testing.T) {
		tbl := btSetupDataPlane(t, project, instance+"-delts", table, "cf")
		for _, tv := range []struct {
			ts  bigtable.Timestamp
			val string
		}{{3000, "a3"}, {2000, "a2"}, {1000, "a1"}} {
			m := bigtable.NewMutation()
			m.Set("cf", "a", tv.ts, []byte(tv.val))
			require.NoError(t, tbl.Apply(ctx, "r", m))
		}

		// Delete versions in [2000, 3000) → removes a2; keeps a3 and a1.
		del := bigtable.NewMutation()
		del.DeleteTimestampRange("cf", "a", 2000, 3000)
		require.NoError(t, tbl.Apply(ctx, "r", del))

		row, err := tbl.ReadRow(ctx, "r")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"cf/cf:a=a3", "cf/cf:a=a1"}, btCellStr(row))
	})
}

func TestBigtable_DataPlane_AppendValue(t *testing.T) {
	t.Parallel()
	tbl := btSetupDataPlane(t, "bt-append-proj", "bt-append-inst", "app", "cf")

	// Each append returns the row with the post-append latest value.
	require.NoError(t, tbl.Apply(ctx, "r", func() *bigtable.Mutation {
		m := bigtable.NewMutation()
		m.Set("cf", "buf", 1000, []byte("hello"))
		return m
	}()))

	rmw1 := bigtable.NewReadModifyWrite()
	rmw1.AppendValue("cf", "buf", []byte(" world"))
	after1, err := tbl.ApplyReadModifyWrite(ctx, "r", rmw1)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(after1["cf"][0].Value))

	rmw2 := bigtable.NewReadModifyWrite()
	rmw2.AppendValue("cf", "buf", []byte("!"))
	after2, err := tbl.ApplyReadModifyWrite(ctx, "r", rmw2)
	require.NoError(t, err)
	assert.Equal(t, "hello world!", string(after2["cf"][0].Value))

	row, err := tbl.ReadRow(ctx, "r", bigtable.RowFilter(bigtable.LatestNFilter(1)))
	require.NoError(t, err)
	assert.Equal(t, "hello world!", string(row["cf"][0].Value))
}

// TestBigtable_DataPlane_ApplyBulk drives MutateRows, which returns a per-row
// error slice besides the top-level error.
func TestBigtable_DataPlane_ApplyBulk(t *testing.T) {
	t.Parallel()
	tbl := btSetupDataPlane(t, "bt-bulk-proj", "bt-bulk-inst", "bulk", "cf")

	mk := func(val string) *bigtable.Mutation {
		m := bigtable.NewMutation()
		m.Set("cf", "v", 1000, []byte(val))
		return m
	}
	rows := []string{"b1", "b2", "b3"}
	muts := []*bigtable.Mutation{mk("v1"), mk("v2"), mk("v3")}

	errs, err := tbl.ApplyBulk(ctx, rows, muts)
	require.NoError(t, err)
	// The client returns a nil slice or one nil per row when every entry
	// succeeds.
	for _, e := range errs {
		assert.NoError(t, e)
	}

	var got []string
	require.NoError(t, tbl.ReadRows(ctx, bigtable.NewRange("b1", "b9"), func(r bigtable.Row) bool {
		got = append(got, r.Key())
		return true
	}))
	assert.ElementsMatch(t, []string{"b1", "b2", "b3"}, got)

	// A mutation on an unknown family fails that entry alone.
	bad := bigtable.NewMutation()
	bad.Set("nope", "v", 1000, []byte("x"))
	errs2, err2 := tbl.ApplyBulk(ctx, []string{"ok1", "bad"}, []*bigtable.Mutation{mk("ok"), bad})
	require.NoError(t, err2, "top-level error is transport-only; per-entry failures come back via errs")
	require.Len(t, errs2, 2, "a partial failure must populate the per-entry slice")
	assert.NoError(t, errs2[0])
	assert.Equal(t, codes.NotFound, status.Code(errs2[1]),
		"an unknown column family must come back as that entry's NotFound: %v", errs2[1])
}

// TestBigtable_DataPlane_SampleRowKeys: the client uses SampleRowKeys to shard
// parallel scans.
func TestBigtable_DataPlane_SampleRowKeys(t *testing.T) {
	t.Parallel()
	tbl := btSetupDataPlane(t, "bt-samp-proj", "bt-samp-inst", "samp", "cf")

	// 150 rows spans the sampler's every-100-rows cadence, so the sample set is
	// more than one point: the first row, the row a hundred on, and the last.
	const rows = 150
	for i := 0; i < rows; i++ {
		m := bigtable.NewMutation()
		m.Set("cf", "v", 1000, []byte("x"))
		require.NoError(t, tbl.Apply(ctx, "k"+pad3(i), m))
	}

	keys, err := tbl.SampleRowKeys(ctx)
	require.NoError(t, err)
	// Exactly the cadence's sample points in ascending key order, the order a
	// client relies on to cut a scan into shards.
	assert.Equal(t, []string{"k000", "k100", "k149"}, keys)
}

func pad3(n int) string {
	s := ""
	switch {
	case n < 10:
		s = "00"
	case n < 100:
		s = "0"
	}
	return s + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

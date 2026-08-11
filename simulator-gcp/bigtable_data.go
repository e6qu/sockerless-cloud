package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/fnv"
	"regexp"
	"sort"
	"sync"
	"time"

	btpb "cloud.google.com/go/bigtable/apiv2/bigtablepb"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Cloud Bigtable Data API (google.bigtable.v2.Bigtable) — the gRPC data plane
// that ReadRows / MutateRow(s) / CheckAndMutateRow / ReadModifyWriteRow /
// SampleRowKeys ride, with a faithful RowFilter evaluator. It mounts on the same
// gRPC server as the existing Bigtable admin services. Storage is an in-memory
// per-table cell store; an operation on a table that admin never created is a
// loud NotFound, and an unsupported RowFilter is a loud Unimplemented — never a
// silent wrong result.

// btCell is one timestamped cell version.
type btCell struct {
	ts     int64
	value  []byte
	labels []string
}

// btTableData holds one table's rows: rowKey → family → qualifier → versions
// (kept newest-first per column).
type btTableData struct {
	mu   sync.Mutex
	rows map[string]map[string]map[string][]btCell
}

var bigtableData = struct {
	mu     sync.Mutex
	tables map[string]*btTableData
}{tables: map[string]*btTableData{}}

// btStoredCell / btStoredTableData mirror btCell / btTableData with exported
// fields so a table's row data JSON round-trips through the durable store.
type btStoredCell struct {
	TimestampMicros int64    `json:"timestampMicros"`
	Value           []byte   `json:"value,omitempty"`
	Labels          []string `json:"labels,omitempty"`
}

type btStoredTableData struct {
	Rows map[string]map[string]map[string][]btStoredCell `json:"rows"`
}

// bigtableRows is the durable copy of every table's row data, keyed by table
// name. The in-memory btTableData is the working copy: every mutation writes
// the table's rows through this store, and a table's first data access after
// a restart hydrates the working copy from it. registerBigtable wires it to
// the same database as the table-metadata store.
var bigtableRows sim.Store[btStoredTableData]

func btRowsToStored(rows map[string]map[string]map[string][]btCell) map[string]map[string]map[string][]btStoredCell {
	out := make(map[string]map[string]map[string][]btStoredCell, len(rows))
	for rowKey, fams := range rows {
		outFams := make(map[string]map[string][]btStoredCell, len(fams))
		for fam, cols := range fams {
			outCols := make(map[string][]btStoredCell, len(cols))
			for qual, cells := range cols {
				outCells := make([]btStoredCell, len(cells))
				for i, c := range cells {
					outCells[i] = btStoredCell{TimestampMicros: c.ts, Value: c.value, Labels: c.labels}
				}
				outCols[qual] = outCells
			}
			outFams[fam] = outCols
		}
		out[rowKey] = outFams
	}
	return out
}

func btRowsFromStored(stored btStoredTableData) map[string]map[string]map[string][]btCell {
	out := make(map[string]map[string]map[string][]btCell, len(stored.Rows))
	for rowKey, fams := range stored.Rows {
		outFams := make(map[string]map[string][]btCell, len(fams))
		for fam, cols := range fams {
			outCols := make(map[string][]btCell, len(cols))
			for qual, cells := range cols {
				outCells := make([]btCell, len(cells))
				for i, c := range cells {
					outCells[i] = btCell{ts: c.TimestampMicros, value: c.Value, labels: c.Labels}
				}
				outCols[qual] = outCells
			}
			outFams[fam] = outCols
		}
		out[rowKey] = outFams
	}
	return out
}

// btPersistTableData writes a table's working-copy rows to the durable store.
// Callers hold td.mu.
func btPersistTableData(name string, td *btTableData) {
	if bigtableRows == nil {
		return
	}
	bigtableRows.Put(name, btStoredTableData{Rows: btRowsToStored(td.rows)})
}

// btDeleteTableData drops a deleted table's rows from the working copy and
// the durable store, so a table recreated under the same name starts empty.
func btDeleteTableData(name string) {
	bigtableData.mu.Lock()
	delete(bigtableData.tables, name)
	bigtableData.mu.Unlock()
	if bigtableRows != nil {
		bigtableRows.Delete(name)
	}
}

func bigtableTableData(name string) *btTableData {
	bigtableData.mu.Lock()
	defer bigtableData.mu.Unlock()
	td, ok := bigtableData.tables[name]
	if !ok {
		td = &btTableData{rows: map[string]map[string]map[string][]btCell{}}
		if bigtableRows != nil {
			if stored, found := bigtableRows.Get(name); found {
				td.rows = btRowsFromStored(stored)
			}
		}
		bigtableData.tables[name] = td
	}
	return td
}

type bigtableDataGRPC struct {
	btpb.UnimplementedBigtableServer
}

func registerBigtableDataGRPC(gs *grpc.Server) {
	btpb.RegisterBigtableServer(gs, &bigtableDataGRPC{})
}

// btRequireTable resolves a data request's table, returning a loud NotFound when
// admin never created it (matching real Bigtable).
func btRequireTable(tableName string) (*btTableData, error) {
	if tableName == "" {
		return nil, status.Error(codes.InvalidArgument, "table_name is required")
	}
	if _, ok := bigtableTables.Get(tableName); !ok {
		return nil, status.Errorf(codes.NotFound, "table %q not found", tableName)
	}
	return bigtableTableData(tableName), nil
}

func btTableFamilies(tableName string) map[string]bool {
	out := map[string]bool{}
	if t, ok := bigtableTables.Get(tableName); ok {
		for fam := range t.ColumnFamilies {
			out[fam] = true
		}
	}
	return out
}

// ── mutations ────────────────────────────────────────────────────────────────

func btApplyMutations(td *btTableData, families map[string]bool, rowKey string, muts []*btpb.Mutation) error {
	nowMicros := time.Now().UnixMicro()
	for _, m := range muts {
		switch mut := m.GetMutation().(type) {
		case *btpb.Mutation_SetCell_:
			sc := mut.SetCell
			fam := sc.GetFamilyName()
			if !families[fam] {
				return status.Errorf(codes.NotFound, "unknown column family %q", fam)
			}
			ts := sc.GetTimestampMicros()
			if ts < 0 {
				ts = nowMicros
			}
			btSetCell(td, rowKey, fam, string(sc.GetColumnQualifier()), btCell{ts: ts, value: sc.GetValue()})
		case *btpb.Mutation_DeleteFromColumn_:
			d := mut.DeleteFromColumn
			btDeleteFromColumn(td, rowKey, d.GetFamilyName(), string(d.GetColumnQualifier()), d.GetTimeRange())
		case *btpb.Mutation_DeleteFromFamily_:
			btDeleteFromFamily(td, rowKey, mut.DeleteFromFamily.GetFamilyName())
		case *btpb.Mutation_DeleteFromRow_:
			delete(td.rows, rowKey)
		default:
			return status.Error(codes.Unimplemented, "unsupported mutation type")
		}
	}
	// Drop a row that has become empty.
	if r, ok := td.rows[rowKey]; ok && len(r) == 0 {
		delete(td.rows, rowKey)
	}
	return nil
}

func btSetCell(td *btTableData, rowKey, fam, qual string, c btCell) {
	row := td.rows[rowKey]
	if row == nil {
		row = map[string]map[string][]btCell{}
		td.rows[rowKey] = row
	}
	cols := row[fam]
	if cols == nil {
		cols = map[string][]btCell{}
		row[fam] = cols
	}
	// A SetCell at an existing timestamp overwrites that version.
	cells := cols[qual]
	for i, ex := range cells {
		if ex.ts == c.ts {
			cells[i] = c
			cols[qual] = cells
			return
		}
	}
	cells = append(cells, c)
	sort.Slice(cells, func(i, j int) bool { return cells[i].ts > cells[j].ts })
	cols[qual] = cells
}

func btDeleteFromColumn(td *btTableData, rowKey, fam, qual string, tr *btpb.TimestampRange) {
	row := td.rows[rowKey]
	if row == nil || row[fam] == nil {
		return
	}
	cells := row[fam][qual]
	if tr == nil || (tr.GetStartTimestampMicros() == 0 && tr.GetEndTimestampMicros() == 0) {
		delete(row[fam], qual)
	} else {
		kept := cells[:0]
		for _, c := range cells {
			inRange := c.ts >= tr.GetStartTimestampMicros() &&
				(tr.GetEndTimestampMicros() == 0 || c.ts < tr.GetEndTimestampMicros())
			if !inRange {
				kept = append(kept, c)
			}
		}
		if len(kept) == 0 {
			delete(row[fam], qual)
		} else {
			row[fam][qual] = kept
		}
	}
	if len(row[fam]) == 0 {
		delete(row, fam)
	}
}

func btDeleteFromFamily(td *btTableData, rowKey, fam string) {
	if row := td.rows[rowKey]; row != nil {
		delete(row, fam)
	}
}

func (s *bigtableDataGRPC) MutateRow(_ context.Context, req *btpb.MutateRowRequest) (*btpb.MutateRowResponse, error) {
	td, err := btRequireTable(req.GetTableName())
	if err != nil {
		return nil, err
	}
	if len(req.GetMutations()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "mutations is required")
	}
	td.mu.Lock()
	defer td.mu.Unlock()
	if err := btApplyMutations(td, btTableFamilies(req.GetTableName()), string(req.GetRowKey()), req.GetMutations()); err != nil {
		return nil, err
	}
	btPersistTableData(req.GetTableName(), td)
	return &btpb.MutateRowResponse{}, nil
}

func (s *bigtableDataGRPC) MutateRows(req *btpb.MutateRowsRequest, srv btpb.Bigtable_MutateRowsServer) error {
	td, err := btRequireTable(req.GetTableName())
	if err != nil {
		return err
	}
	families := btTableFamilies(req.GetTableName())
	td.mu.Lock()
	defer td.mu.Unlock()
	entries := make([]*btpb.MutateRowsResponse_Entry, 0, len(req.GetEntries()))
	for i, e := range req.GetEntries() {
		st := &btpb.MutateRowsResponse_Entry{Index: int64(i), Status: status.New(codes.OK, "").Proto()}
		if err := btApplyMutations(td, families, string(e.GetRowKey()), e.GetMutations()); err != nil {
			s, _ := status.FromError(err)
			st.Status = s.Proto()
		}
		entries = append(entries, st)
	}
	btPersistTableData(req.GetTableName(), td)
	return srv.Send(&btpb.MutateRowsResponse{Entries: entries})
}

func (s *bigtableDataGRPC) CheckAndMutateRow(_ context.Context, req *btpb.CheckAndMutateRowRequest) (*btpb.CheckAndMutateRowResponse, error) {
	td, err := btRequireTable(req.GetTableName())
	if err != nil {
		return nil, err
	}
	td.mu.Lock()
	defer td.mu.Unlock()
	rowKey := string(req.GetRowKey())
	cells := btGatherRow(td, rowKey)
	matched := len(cells) > 0
	if req.GetPredicateFilter() != nil {
		filtered, ferr := btApplyFilter(rowKey, cells, req.GetPredicateFilter())
		if ferr != nil {
			return nil, ferr
		}
		matched = len(filtered) > 0
	}
	muts := req.GetFalseMutations()
	if matched {
		muts = req.GetTrueMutations()
	}
	if len(muts) > 0 {
		if err := btApplyMutations(td, btTableFamilies(req.GetTableName()), rowKey, muts); err != nil {
			return nil, err
		}
		btPersistTableData(req.GetTableName(), td)
	}
	return &btpb.CheckAndMutateRowResponse{PredicateMatched: matched}, nil
}

func (s *bigtableDataGRPC) ReadModifyWriteRow(_ context.Context, req *btpb.ReadModifyWriteRowRequest) (*btpb.ReadModifyWriteRowResponse, error) {
	td, err := btRequireTable(req.GetTableName())
	if err != nil {
		return nil, err
	}
	families := btTableFamilies(req.GetTableName())
	td.mu.Lock()
	defer td.mu.Unlock()
	rowKey := string(req.GetRowKey())
	now := time.Now().UnixMicro()
	for _, rule := range req.GetRules() {
		fam := rule.GetFamilyName()
		if !families[fam] {
			return nil, status.Errorf(codes.NotFound, "unknown column family %q", fam)
		}
		qual := string(rule.GetColumnQualifier())
		latest, ok := btLatestCell(td, rowKey, fam, qual)
		switch r := rule.GetRule().(type) {
		case *btpb.ReadModifyWriteRule_AppendValue:
			var base []byte
			if ok {
				base = latest.value
			}
			btSetCell(td, rowKey, fam, qual, btCell{ts: now, value: append(append([]byte{}, base...), r.AppendValue...)})
		case *btpb.ReadModifyWriteRule_IncrementAmount:
			cur := int64(0)
			if ok && len(latest.value) == 8 {
				cur = int64(binary.BigEndian.Uint64(latest.value))
			}
			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, uint64(cur+r.IncrementAmount))
			btSetCell(td, rowKey, fam, qual, btCell{ts: now, value: buf})
		default:
			return nil, status.Error(codes.Unimplemented, "unsupported read-modify-write rule")
		}
	}
	btPersistTableData(req.GetTableName(), td)
	// The response carries only the new (latest) value of each modified column,
	// not the full version history — matching real Bigtable.
	var modified []btReadCell
	seen := map[string]bool{}
	for _, rule := range req.GetRules() {
		fam, qual := rule.GetFamilyName(), string(rule.GetColumnQualifier())
		key := fam + "\x00" + qual
		if seen[key] {
			continue
		}
		seen[key] = true
		if c, ok := btLatestCell(td, rowKey, fam, qual); ok {
			modified = append(modified, btReadCell{family: fam, qual: qual, ts: c.ts, value: c.value, labels: c.labels})
		}
	}
	sort.Slice(modified, func(i, j int) bool {
		if modified[i].family != modified[j].family {
			return modified[i].family < modified[j].family
		}
		return modified[i].qual < modified[j].qual
	})
	return &btpb.ReadModifyWriteRowResponse{Row: btRowToProto(rowKey, modified)}, nil
}

func btLatestCell(td *btTableData, rowKey, fam, qual string) (btCell, bool) {
	if row := td.rows[rowKey]; row != nil {
		if cells := row[fam][qual]; len(cells) > 0 {
			return cells[0], true // newest-first
		}
	}
	return btCell{}, false
}

// ── reads ────────────────────────────────────────────────────────────────────

// btReadCell is a row's cell flattened for filtering and emission, ordered
// family asc, qualifier asc, timestamp desc.
type btReadCell struct {
	family string
	qual   string
	ts     int64
	value  []byte
	labels []string
}

func btGatherRow(td *btTableData, rowKey string) []btReadCell {
	row := td.rows[rowKey]
	if row == nil {
		return nil
	}
	fams := make([]string, 0, len(row))
	for f := range row {
		fams = append(fams, f)
	}
	sort.Strings(fams)
	var out []btReadCell
	for _, f := range fams {
		quals := make([]string, 0, len(row[f]))
		for q := range row[f] {
			quals = append(quals, q)
		}
		sort.Strings(quals)
		for _, q := range quals {
			for _, c := range row[f][q] { // already newest-first
				out = append(out, btReadCell{family: f, qual: q, ts: c.ts, value: c.value, labels: c.labels})
			}
		}
	}
	return out
}

func (s *bigtableDataGRPC) ReadRows(req *btpb.ReadRowsRequest, srv btpb.Bigtable_ReadRowsServer) error {
	td, err := btRequireTable(req.GetTableName())
	if err != nil {
		return err
	}
	td.mu.Lock()
	keys := make([]string, 0, len(td.rows))
	for k := range td.rows {
		keys = append(keys, k)
	}
	td.mu.Unlock()
	sort.Strings(keys)
	if req.GetReversed() {
		for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
			keys[i], keys[j] = keys[j], keys[i]
		}
	}

	want := btRowSetPredicate(req.GetRows())
	limit := req.GetRowsLimit()
	emitted := int64(0)
	for _, k := range keys {
		if !want(k) {
			continue
		}
		td.mu.Lock()
		cells := btGatherRow(td, k)
		td.mu.Unlock()
		if req.GetFilter() != nil {
			cells, err = btApplyFilter(k, cells, req.GetFilter())
			if err != nil {
				return err
			}
		}
		if len(cells) == 0 {
			continue
		}
		if err := btSendRow(srv, k, cells); err != nil {
			return err
		}
		emitted++
		if limit > 0 && emitted >= limit {
			break
		}
	}
	return nil
}

func btSendRow(srv btpb.Bigtable_ReadRowsServer, rowKey string, cells []btReadCell) error {
	chunks := make([]*btpb.ReadRowsResponse_CellChunk, 0, len(cells))
	for i, c := range cells {
		chunk := &btpb.ReadRowsResponse_CellChunk{
			FamilyName:      &wrapperspb.StringValue{Value: c.family},
			Qualifier:       &wrapperspb.BytesValue{Value: []byte(c.qual)},
			TimestampMicros: c.ts,
			Value:           c.value,
			Labels:          c.labels,
		}
		if i == 0 {
			chunk.RowKey = []byte(rowKey)
		}
		if i == len(cells)-1 {
			chunk.RowStatus = &btpb.ReadRowsResponse_CellChunk_CommitRow{CommitRow: true}
		}
		chunks = append(chunks, chunk)
	}
	return srv.Send(&btpb.ReadRowsResponse{Chunks: chunks})
}

// btRowSetPredicate builds a row-key predicate from a RowSet (nil → all rows).
func btRowSetPredicate(rs *btpb.RowSet) func(string) bool {
	if rs == nil || (len(rs.GetRowKeys()) == 0 && len(rs.GetRowRanges()) == 0) {
		return func(string) bool { return true }
	}
	exact := map[string]bool{}
	for _, k := range rs.GetRowKeys() {
		exact[string(k)] = true
	}
	ranges := rs.GetRowRanges()
	return func(k string) bool {
		if exact[k] {
			return true
		}
		for _, rr := range ranges {
			if btInRowRange(k, rr) {
				return true
			}
		}
		return false
	}
}

func btInRowRange(k string, rr *btpb.RowRange) bool {
	switch sk := rr.GetStartKey().(type) {
	case *btpb.RowRange_StartKeyClosed:
		if k < string(sk.StartKeyClosed) {
			return false
		}
	case *btpb.RowRange_StartKeyOpen:
		if k <= string(sk.StartKeyOpen) {
			return false
		}
	}
	switch ek := rr.GetEndKey().(type) {
	case *btpb.RowRange_EndKeyClosed:
		if k > string(ek.EndKeyClosed) {
			return false
		}
	case *btpb.RowRange_EndKeyOpen:
		if k >= string(ek.EndKeyOpen) {
			return false
		}
	}
	return true
}

func btRowToProto(rowKey string, cells []btReadCell) *btpb.Row {
	row := &btpb.Row{Key: []byte(rowKey)}
	famIdx := map[string]*btpb.Family{}
	colIdx := map[string]*btpb.Column{}
	for _, c := range cells {
		fam := famIdx[c.family]
		if fam == nil {
			fam = &btpb.Family{Name: c.family}
			row.Families = append(row.Families, fam)
			famIdx[c.family] = fam
		}
		ckey := c.family + "\x00" + c.qual
		col := colIdx[ckey]
		if col == nil {
			col = &btpb.Column{Qualifier: []byte(c.qual)}
			fam.Columns = append(fam.Columns, col)
			colIdx[ckey] = col
		}
		col.Cells = append(col.Cells, &btpb.Cell{TimestampMicros: c.ts, Value: c.value, Labels: c.labels})
	}
	return row
}

// ── SampleRowKeys ────────────────────────────────────────────────────────────

func (s *bigtableDataGRPC) SampleRowKeys(req *btpb.SampleRowKeysRequest, srv btpb.Bigtable_SampleRowKeysServer) error {
	td, err := btRequireTable(req.GetTableName())
	if err != nil {
		return err
	}
	td.mu.Lock()
	keys := make([]string, 0, len(td.rows))
	for k := range td.rows {
		keys = append(keys, k)
	}
	td.mu.Unlock()
	sort.Strings(keys)
	// Emit a sample roughly every 100 rows plus a trailing empty key, with a
	// monotonically increasing cumulative offset — the contract the client uses
	// to shard scans.
	offset := int64(0)
	for i, k := range keys {
		offset += 1024
		if i%100 == 0 || i == len(keys)-1 {
			if err := srv.Send(&btpb.SampleRowKeysResponse{RowKey: []byte(k), OffsetBytes: offset}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ── RowFilter ────────────────────────────────────────────────────────────────

// btApplyFilter evaluates a RowFilter against a row's cells, returning the
// surviving cells. An unsupported filter type is a loud Unimplemented error, not
// a silent pass/block.
func btApplyFilter(rowKey string, cells []btReadCell, f *btpb.RowFilter) ([]btReadCell, error) {
	if f == nil {
		return cells, nil
	}
	switch ft := f.GetFilter().(type) {
	case *btpb.RowFilter_PassAllFilter:
		return cells, nil
	case *btpb.RowFilter_BlockAllFilter:
		return nil, nil
	case *btpb.RowFilter_Sink:
		return cells, nil
	case *btpb.RowFilter_Chain_:
		out := cells
		var err error
		for _, sub := range ft.Chain.GetFilters() {
			out, err = btApplyFilter(rowKey, out, sub)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case *btpb.RowFilter_Interleave_:
		var out []btReadCell
		for _, sub := range ft.Interleave.GetFilters() {
			part, err := btApplyFilter(rowKey, cells, sub)
			if err != nil {
				return nil, err
			}
			out = append(out, part...)
		}
		return out, nil
	case *btpb.RowFilter_Condition_:
		pred, err := btApplyFilter(rowKey, cells, ft.Condition.GetPredicateFilter())
		if err != nil {
			return nil, err
		}
		if len(pred) > 0 {
			return btApplyFilter(rowKey, cells, ft.Condition.GetTrueFilter())
		}
		return btApplyFilter(rowKey, cells, ft.Condition.GetFalseFilter())
	case *btpb.RowFilter_RowKeyRegexFilter:
		re, err := btCompileRE(ft.RowKeyRegexFilter)
		if err != nil {
			return nil, err
		}
		if re.MatchString(rowKey) {
			return cells, nil
		}
		return nil, nil
	case *btpb.RowFilter_FamilyNameRegexFilter:
		re, err := btCompileRE([]byte(ft.FamilyNameRegexFilter))
		if err != nil {
			return nil, err
		}
		return btFilterCells(cells, func(c btReadCell) bool { return re.MatchString(c.family) }), nil
	case *btpb.RowFilter_ColumnQualifierRegexFilter:
		re, err := btCompileRE(ft.ColumnQualifierRegexFilter)
		if err != nil {
			return nil, err
		}
		return btFilterCells(cells, func(c btReadCell) bool { return re.MatchString(c.qual) }), nil
	case *btpb.RowFilter_ValueRegexFilter:
		re, err := btCompileRE(ft.ValueRegexFilter)
		if err != nil {
			return nil, err
		}
		return btFilterCells(cells, func(c btReadCell) bool { return re.Match(c.value) }), nil
	case *btpb.RowFilter_ColumnRangeFilter:
		cr := ft.ColumnRangeFilter
		return btFilterCells(cells, func(c btReadCell) bool {
			return c.family == cr.GetFamilyName() && btQualInRange(c.qual, cr)
		}), nil
	case *btpb.RowFilter_TimestampRangeFilter:
		tr := ft.TimestampRangeFilter
		return btFilterCells(cells, func(c btReadCell) bool {
			return c.ts >= tr.GetStartTimestampMicros() &&
				(tr.GetEndTimestampMicros() == 0 || c.ts < tr.GetEndTimestampMicros())
		}), nil
	case *btpb.RowFilter_ValueRangeFilter:
		vr := ft.ValueRangeFilter
		return btFilterCells(cells, func(c btReadCell) bool { return btValueInRange(c.value, vr) }), nil
	case *btpb.RowFilter_CellsPerColumnLimitFilter:
		return btCellsPerColumnLimit(cells, int(ft.CellsPerColumnLimitFilter)), nil
	case *btpb.RowFilter_CellsPerRowLimitFilter:
		n := int(ft.CellsPerRowLimitFilter)
		if n >= 0 && n < len(cells) {
			return cells[:n], nil
		}
		return cells, nil
	case *btpb.RowFilter_CellsPerRowOffsetFilter:
		n := int(ft.CellsPerRowOffsetFilter)
		if n > 0 {
			if n >= len(cells) {
				return nil, nil
			}
			return cells[n:], nil
		}
		return cells, nil
	case *btpb.RowFilter_StripValueTransformer:
		if ft.StripValueTransformer {
			out := make([]btReadCell, len(cells))
			for i, c := range cells {
				c.value = nil
				out[i] = c
			}
			return out, nil
		}
		return cells, nil
	case *btpb.RowFilter_ApplyLabelTransformer:
		out := make([]btReadCell, len(cells))
		for i, c := range cells {
			c.labels = append(append([]string{}, c.labels...), ft.ApplyLabelTransformer)
			out[i] = c
		}
		return out, nil
	case *btpb.RowFilter_RowSampleFilter:
		// Deterministic per-row sampling: keep the row when a stable hash of its
		// key falls under the requested probability (a sim can't use real
		// randomness without becoming non-reproducible).
		h := fnv.New64a()
		_, _ = h.Write([]byte(rowKey))
		frac := float64(h.Sum64()%10000) / 10000.0
		if frac < ft.RowSampleFilter {
			return cells, nil
		}
		return nil, nil
	default:
		return nil, status.Errorf(codes.Unimplemented, "unsupported RowFilter %T", ft)
	}
}

func btFilterCells(cells []btReadCell, keep func(btReadCell) bool) []btReadCell {
	var out []btReadCell
	for _, c := range cells {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

func btCellsPerColumnLimit(cells []btReadCell, n int) []btReadCell {
	if n <= 0 {
		return cells
	}
	counts := map[string]int{}
	var out []btReadCell
	for _, c := range cells {
		key := c.family + "\x00" + c.qual
		if counts[key] < n {
			out = append(out, c)
			counts[key]++
		}
	}
	return out
}

func btQualInRange(qual string, cr *btpb.ColumnRange) bool {
	switch sk := cr.GetStartQualifier().(type) {
	case *btpb.ColumnRange_StartQualifierClosed:
		if qual < string(sk.StartQualifierClosed) {
			return false
		}
	case *btpb.ColumnRange_StartQualifierOpen:
		if qual <= string(sk.StartQualifierOpen) {
			return false
		}
	}
	switch ek := cr.GetEndQualifier().(type) {
	case *btpb.ColumnRange_EndQualifierClosed:
		if qual > string(ek.EndQualifierClosed) {
			return false
		}
	case *btpb.ColumnRange_EndQualifierOpen:
		if qual >= string(ek.EndQualifierOpen) {
			return false
		}
	}
	return true
}

func btValueInRange(value []byte, vr *btpb.ValueRange) bool {
	switch sv := vr.GetStartValue().(type) {
	case *btpb.ValueRange_StartValueClosed:
		if bytes.Compare(value, sv.StartValueClosed) < 0 {
			return false
		}
	case *btpb.ValueRange_StartValueOpen:
		if bytes.Compare(value, sv.StartValueOpen) <= 0 {
			return false
		}
	}
	switch ev := vr.GetEndValue().(type) {
	case *btpb.ValueRange_EndValueClosed:
		if bytes.Compare(value, ev.EndValueClosed) > 0 {
			return false
		}
	case *btpb.ValueRange_EndValueOpen:
		if bytes.Compare(value, ev.EndValueOpen) >= 0 {
			return false
		}
	}
	return true
}

// btCompileRE compiles a Bigtable RE2 pattern as a full-string (anchored) match,
// matching the data API's implicit anchoring.
func btCompileRE(pat []byte) (*regexp.Regexp, error) {
	re, err := regexp.Compile("^(?:" + string(pat) + ")$")
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid regex filter: %v", err)
	}
	return re, nil
}

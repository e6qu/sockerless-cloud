package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	btpb "cloud.google.com/go/bigtable/apiv2/bigtablepb"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
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

// btDeleteTableData drops a deleted table's rows from the working copy and the
// durable store, and its change history with them, so a table recreated under
// the same name starts empty and its change stream reports nothing that
// happened to the table it replaced.
func btDeleteTableData(name string) {
	bigtableData.mu.Lock()
	delete(bigtableData.tables, name)
	bigtableData.mu.Unlock()
	if bigtableRows != nil {
		bigtableRows.Delete(name)
	}
	bigtableChanges.mu.Lock()
	delete(bigtableChanges.logs, name)
	bigtableChanges.mu.Unlock()
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

// btValidateMutations rejects an entry the table cannot accept before any of
// it is applied, so a rejected mutation leaves the row exactly as it was —
// the single-row atomicity real Bigtable gives MutateRow.
func btValidateMutations(families map[string]bool, muts []*btpb.Mutation) error {
	for _, m := range muts {
		switch mut := m.GetMutation().(type) {
		case *btpb.Mutation_SetCell_:
			if fam := mut.SetCell.GetFamilyName(); !families[fam] {
				return status.Errorf(codes.NotFound, "unknown column family %q", fam)
			}
		case *btpb.Mutation_DeleteFromColumn_, *btpb.Mutation_DeleteFromFamily_, *btpb.Mutation_DeleteFromRow_:
		default:
			return status.Error(codes.Unimplemented, "unsupported mutation type")
		}
	}
	return nil
}

// btApplyMutations applies one row's mutations and records them in the table's
// change log. It is the one path every row mutation takes, so the log it
// writes is the table's complete record of applied changes.
func btApplyMutations(tableName string, td *btTableData, families map[string]bool, rowKey string, muts []*btpb.Mutation) error {
	if err := btValidateMutations(families, muts); err != nil {
		return err
	}
	nowMicros := time.Now().UnixMicro()
	for _, m := range muts {
		switch mut := m.GetMutation().(type) {
		case *btpb.Mutation_SetCell_:
			sc := mut.SetCell
			ts := sc.GetTimestampMicros()
			if ts < 0 {
				ts = nowMicros
			}
			btSetCell(td, rowKey, sc.GetFamilyName(), string(sc.GetColumnQualifier()), btCell{ts: ts, value: sc.GetValue()})
		case *btpb.Mutation_DeleteFromColumn_:
			d := mut.DeleteFromColumn
			btDeleteFromColumn(td, rowKey, d.GetFamilyName(), string(d.GetColumnQualifier()), d.GetTimeRange())
		case *btpb.Mutation_DeleteFromFamily_:
			btDeleteFromFamily(td, rowKey, mut.DeleteFromFamily.GetFamilyName())
		case *btpb.Mutation_DeleteFromRow_:
			delete(td.rows, rowKey)
		}
	}
	// Drop a row that has become empty.
	if r, ok := td.rows[rowKey]; ok && len(r) == 0 {
		delete(td.rows, rowKey)
	}
	btRecordChange(tableName, rowKey, muts)
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
	if err := btApplyMutations(req.GetTableName(), td, btTableFamilies(req.GetTableName()), string(req.GetRowKey()), req.GetMutations()); err != nil {
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
		if err := btApplyMutations(req.GetTableName(), td, families, string(e.GetRowKey()), e.GetMutations()); err != nil {
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
		if err := btApplyMutations(req.GetTableName(), td, btTableFamilies(req.GetTableName()), rowKey, muts); err != nil {
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
	// Every rule is checked before any of them writes, so a rejected
	// read-modify-write leaves the row untouched.
	for _, rule := range req.GetRules() {
		if fam := rule.GetFamilyName(); !families[fam] {
			return nil, status.Errorf(codes.NotFound, "unknown column family %q", fam)
		}
		switch rule.GetRule().(type) {
		case *btpb.ReadModifyWriteRule_AppendValue, *btpb.ReadModifyWriteRule_IncrementAmount:
		default:
			return nil, status.Error(codes.Unimplemented, "unsupported read-modify-write rule")
		}
	}
	// The cells the rules produce are the change the table really recorded, so
	// they go to the change log as the SetCells they are.
	applied := make([]*btpb.Mutation, 0, len(req.GetRules()))
	setCell := func(fam, qual string, value []byte) {
		btSetCell(td, rowKey, fam, qual, btCell{ts: now, value: value})
		applied = append(applied, &btpb.Mutation{Mutation: &btpb.Mutation_SetCell_{SetCell: &btpb.Mutation_SetCell{
			FamilyName:      fam,
			ColumnQualifier: []byte(qual),
			TimestampMicros: now,
			Value:           value,
		}}})
	}
	for _, rule := range req.GetRules() {
		fam := rule.GetFamilyName()
		qual := string(rule.GetColumnQualifier())
		latest, ok := btLatestCell(td, rowKey, fam, qual)
		switch r := rule.GetRule().(type) {
		case *btpb.ReadModifyWriteRule_AppendValue:
			var base []byte
			if ok {
				base = latest.value
			}
			setCell(fam, qual, append(append([]byte{}, base...), r.AppendValue...))
		case *btpb.ReadModifyWriteRule_IncrementAmount:
			cur := int64(0)
			if ok && len(latest.value) == 8 {
				cur = int64(binary.BigEndian.Uint64(latest.value))
			}
			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, uint64(cur+r.IncrementAmount))
			setCell(fam, qual, buf)
		}
	}
	btPersistTableData(req.GetTableName(), td)
	btRecordChange(req.GetTableName(), rowKey, applied)
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

// btInRowRange reports whether a row key falls in a range. An empty end key is
// the end of the table, not the empty string: that is how a client spells an
// unbounded range, and how the last change-stream partition names the rest of
// the key space.
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
		if len(ek.EndKeyClosed) > 0 && k > string(ek.EndKeyClosed) {
			return false
		}
	case *btpb.RowRange_EndKeyOpen:
		if len(ek.EndKeyOpen) > 0 && k >= string(ek.EndKeyOpen) {
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

// ── instance coordinates ─────────────────────────────────────────────────────

// btInstanceOfTable returns the instance a table resource name belongs to.
func btInstanceOfTable(tableName string) string {
	if i := strings.Index(tableName, "/tables/"); i >= 0 {
		return tableName[:i]
	}
	return ""
}

// btRequireInstance resolves an instance the admin surface created, returning a
// loud NotFound when it never did.
func btRequireInstance(name string) error {
	if name == "" {
		return status.Error(codes.InvalidArgument, "name is required")
	}
	if _, ok := bigtableInstances.Get(name); !ok {
		return status.Errorf(codes.NotFound, "instance %q not found", name)
	}
	return nil
}

// btRequireAppProfile checks the routing profile a data request names. The
// "default" profile is the instance's implicit one and is not a stored
// resource; any other id must have been created.
func btRequireAppProfile(instanceName, appProfileID string) error {
	if appProfileID == "" || appProfileID == "default" {
		return nil
	}
	name := instanceName + "/appProfiles/" + appProfileID
	if _, ok := bigtableAppProfiles.Get(name); !ok {
		return status.Errorf(codes.NotFound, "app profile %q not found", name)
	}
	return nil
}

// btInstanceCluster returns the id and zone of the cluster serving an
// instance. An instance without a cluster serves nothing, so a data-plane
// answer that has to name its serving cluster fails loudly instead of
// reporting an empty one.
func btInstanceCluster(instanceName string) (id, zone string, err error) {
	prefix := instanceName + "/clusters/"
	clusters := bigtableClusters.Filter(func(c bigtableCluster) bool { return strings.HasPrefix(c.Name, prefix) })
	if len(clusters) == 0 {
		return "", "", status.Errorf(codes.FailedPrecondition, "instance %q has no cluster", instanceName)
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Name < clusters[j].Name })
	c := clusters[0]
	zone = c.Location
	if i := strings.LastIndex(zone, "/"); i >= 0 {
		zone = zone[i+1:]
	}
	return strings.TrimPrefix(c.Name, prefix), zone, nil
}

// ── connection keep-alive and client configuration ───────────────────────────

func (s *bigtableDataGRPC) PingAndWarm(_ context.Context, req *btpb.PingAndWarmRequest) (*btpb.PingAndWarmResponse, error) {
	if err := btRequireInstance(req.GetName()); err != nil {
		return nil, err
	}
	if err := btRequireAppProfile(req.GetName(), req.GetAppProfileId()); err != nil {
		return nil, err
	}
	// Warming is real work here: a table's rows live in the durable store until
	// its first data access hydrates the working copy, so warming the instance
	// hydrates its tables and the first read after it pays no hydration cost.
	prefix := req.GetName() + "/tables/"
	for _, t := range bigtableTables.Filter(func(t bigtableTable) bool { return strings.HasPrefix(t.Name, prefix) }) {
		bigtableTableData(t.Name)
	}
	return &btpb.PingAndWarmResponse{}, nil
}

func (s *bigtableDataGRPC) GetClientConfiguration(_ context.Context, req *btpb.GetClientConfigurationRequest) (*btpb.ClientConfiguration, error) {
	if err := btRequireInstance(req.GetInstanceName()); err != nil {
		return nil, err
	}
	if err := btRequireAppProfile(req.GetInstanceName(), req.GetAppProfileId()); err != nil {
		return nil, err
	}
	// The configuration reports what this server is: it serves the session API
	// for the point operations that protocol carries, so the whole session
	// share belongs on it, and its configuration is fixed for the life of the
	// process, so there is nothing for a client to poll for. The channel-pool,
	// session-pool, load-balancing and telemetry directives are left unset:
	// they instruct a client about a fleet of frontends, and this server has no
	// fleet to describe.
	return &btpb.ClientConfiguration{
		SessionConfiguration: &btpb.SessionClientConfiguration{SessionLoad: 1},
		Polling:              &btpb.ClientConfiguration_StopPolling{StopPolling: true},
	}, nil
}

// ── change stream ────────────────────────────────────────────────────────────

// btChangeRecord is one applied row mutation, as the change stream reports it.
type btChangeRecord struct {
	sequence   int64
	rowKey     string
	commitTime time.Time
	mutations  []*btpb.Mutation
}

// btChangeLogLimit bounds the change history one table retains. Real Bigtable
// retains a table's change stream for its change_stream_retention_period; the
// simulator retains a fixed number of records and refuses, loudly, to start a
// stream before the oldest one it still holds.
const btChangeLogLimit = 10000

// btChangeLog is a table's record of the mutations applied to it, in commit
// order. btApplyMutations and ReadModifyWriteRow — the only two paths that
// change a row — append to it, so a stream reading it reports mutations that
// really happened and never invents one.
//
// The log lives for the process. A restart hydrates a table's rows from the
// durable store but starts its change history empty, so a stream opened after
// a restart reports the changes made since the restart and refuses a start
// point before it.
type btChangeLog struct {
	mu       sync.Mutex
	records  []btChangeRecord
	next     int64
	evicted  int64         // records dropped to stay within btChangeLogLimit
	appended chan struct{} // closed and replaced on every append
}

var bigtableChanges = struct {
	mu   sync.Mutex
	logs map[string]*btChangeLog
}{logs: map[string]*btChangeLog{}}

func btTableChangeLog(tableName string) *btChangeLog {
	bigtableChanges.mu.Lock()
	defer bigtableChanges.mu.Unlock()
	l, ok := bigtableChanges.logs[tableName]
	if !ok {
		l = &btChangeLog{appended: make(chan struct{})}
		bigtableChanges.logs[tableName] = l
	}
	return l
}

// btRecordChange appends the mutations one row write applied.
func btRecordChange(tableName, rowKey string, muts []*btpb.Mutation) {
	if len(muts) == 0 {
		return
	}
	recorded := make([]*btpb.Mutation, 0, len(muts))
	for _, m := range muts {
		clone := &btpb.Mutation{}
		proto.Merge(clone, m)
		recorded = append(recorded, clone)
	}
	l := btTableChangeLog(tableName)
	l.mu.Lock()
	l.next++
	l.records = append(l.records, btChangeRecord{
		sequence:   l.next,
		rowKey:     rowKey,
		commitTime: time.Now().UTC(),
		mutations:  recorded,
	})
	if len(l.records) > btChangeLogLimit {
		drop := len(l.records) - btChangeLogLimit
		l.records = append([]btChangeRecord(nil), l.records[drop:]...)
		l.evicted += int64(drop)
	}
	close(l.appended)
	l.appended = make(chan struct{})
	l.mu.Unlock()
}

// btChangesSince returns the records after cursor together with the channel
// that closes on the next append. Reading both under one lock means a record
// appended after the call still wakes the caller.
func (l *btChangeLog) btChangesSince(cursor int64) ([]btChangeRecord, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []btChangeRecord
	for _, r := range l.records {
		if r.sequence > cursor {
			out = append(out, r)
		}
	}
	return out, l.appended
}

// btChangeCursorAt returns the cursor a stream starting at startTime reads
// from: the sequence of the newest record committed before it. A start time
// older than the retained history cannot be served from what the log holds.
func (l *btChangeLog) btChangeCursorAt(startTime time.Time) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.evicted > 0 && (len(l.records) == 0 || startTime.Before(l.records[0].commitTime)) {
		return 0, status.Errorf(codes.OutOfRange,
			"start_time %s is older than the retained change history", startTime.Format(time.RFC3339Nano))
	}
	cursor := l.evicted
	for _, r := range l.records {
		if !r.commitTime.Before(startTime) {
			break
		}
		cursor = r.sequence
	}
	return cursor, nil
}

// btChangeHead returns the sequence of the newest recorded change.
func (l *btChangeLog) btChangeHead() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.next
}

func (s *bigtableDataGRPC) GenerateInitialChangeStreamPartitions(req *btpb.GenerateInitialChangeStreamPartitionsRequest, srv btpb.Bigtable_GenerateInitialChangeStreamPartitionsServer) error {
	if _, err := btRequireTable(req.GetTableName()); err != nil {
		return err
	}
	if err := btRequireAppProfile(btInstanceOfTable(req.GetTableName()), req.GetAppProfileId()); err != nil {
		return err
	}
	// The simulator holds each table whole, in one process. One tablet means
	// one partition, and the empty start and end keys are its whole key space.
	return srv.Send(&btpb.GenerateInitialChangeStreamPartitionsResponse{Partition: btFullTablePartition()})
}

// btFullTablePartition is the partition covering a table's entire key space.
func btFullTablePartition() *btpb.StreamPartition {
	return &btpb.StreamPartition{RowRange: &btpb.RowRange{
		StartKey: &btpb.RowRange_StartKeyClosed{StartKeyClosed: []byte{}},
		EndKey:   &btpb.RowRange_EndKeyOpen{EndKeyOpen: []byte{}},
	}}
}

func (s *bigtableDataGRPC) ReadChangeStream(req *btpb.ReadChangeStreamRequest, srv btpb.Bigtable_ReadChangeStreamServer) error {
	if _, err := btRequireTable(req.GetTableName()); err != nil {
		return err
	}
	instance := btInstanceOfTable(req.GetTableName())
	if err := btRequireAppProfile(instance, req.GetAppProfileId()); err != nil {
		return err
	}
	clusterID, _, err := btInstanceCluster(instance)
	if err != nil {
		return err
	}

	partition := req.GetPartition()
	if partition == nil {
		partition = btFullTablePartition()
	}
	inPartition := func(rowKey string) bool { return btInRowRange(rowKey, partition.GetRowRange()) }

	log := btTableChangeLog(req.GetTableName())
	cursor, err := btChangeStreamStart(log, req, partition)
	if err != nil {
		return err
	}

	heartbeat := 5 * time.Second
	if d := req.GetHeartbeatDuration(); d != nil {
		if heartbeat = d.AsDuration(); heartbeat <= 0 {
			return status.Error(codes.InvalidArgument, "heartbeat_duration must be positive")
		}
	}
	var endTime time.Time
	if ts := req.GetEndTime(); ts != nil {
		endTime = ts.AsTime()
	}

	ctx := srv.Context()
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		records, appended := log.btChangesSince(cursor)
		for _, rec := range records {
			if !endTime.IsZero() && rec.commitTime.After(endTime) {
				return btCloseChangeStream(srv, partition, cursor)
			}
			cursor = rec.sequence
			if !inPartition(rec.rowKey) {
				continue
			}
			if err := srv.Send(btDataChangeResponse(rec, clusterID)); err != nil {
				return err
			}
		}
		if !endTime.IsZero() && !time.Now().Before(endTime) {
			return btCloseChangeStream(srv, partition, cursor)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-appended:
		case <-ticker.C:
			if err := srv.Send(&btpb.ReadChangeStreamResponse{
				StreamRecord: &btpb.ReadChangeStreamResponse_Heartbeat_{Heartbeat: &btpb.ReadChangeStreamResponse_Heartbeat{
					ContinuationToken:     btContinuationToken(partition, cursor),
					EstimatedLowWatermark: timestamppb.New(time.Now().UTC()),
				}},
			}); err != nil {
				return err
			}
		}
	}
}

// btChangeStreamStart resolves where a ReadChangeStream begins: after the
// position its continuation tokens name, at its start time, or — with neither
// set — at the current end of the log, so only later changes are delivered.
func btChangeStreamStart(log *btChangeLog, req *btpb.ReadChangeStreamRequest, partition *btpb.StreamPartition) (int64, error) {
	switch start := req.GetStartFrom().(type) {
	case *btpb.ReadChangeStreamRequest_ContinuationTokens:
		tokens := start.ContinuationTokens.GetTokens()
		if len(tokens) == 0 {
			return 0, status.Error(codes.InvalidArgument, "continuation_tokens must not be empty")
		}
		cursor := int64(-1)
		for _, t := range tokens {
			if !proto.Equal(t.GetPartition().GetRowRange(), partition.GetRowRange()) {
				return 0, status.Error(codes.InvalidArgument, "continuation token partition does not cover the requested partition")
			}
			seq, err := strconv.ParseInt(t.GetToken(), 10, 64)
			if err != nil {
				return 0, status.Errorf(codes.InvalidArgument, "invalid continuation token %q", t.GetToken())
			}
			// A merge delivers one token per merged partition; reading from the
			// earliest of them delivers every change none of them has seen.
			if cursor < 0 || seq < cursor {
				cursor = seq
			}
		}
		return cursor, nil
	case *btpb.ReadChangeStreamRequest_StartTime:
		return log.btChangeCursorAt(start.StartTime.AsTime())
	}
	return log.btChangeHead(), nil
}

func btContinuationToken(partition *btpb.StreamPartition, cursor int64) *btpb.StreamContinuationToken {
	return &btpb.StreamContinuationToken{Partition: partition, Token: strconv.FormatInt(cursor, 10)}
}

// btCloseChangeStream ends a stream that reached its end_time, handing back the
// position a later stream resumes from.
func btCloseChangeStream(srv btpb.Bigtable_ReadChangeStreamServer, partition *btpb.StreamPartition, cursor int64) error {
	return srv.Send(&btpb.ReadChangeStreamResponse{
		StreamRecord: &btpb.ReadChangeStreamResponse_CloseStream_{CloseStream: &btpb.ReadChangeStreamResponse_CloseStream{
			Status:             status.New(codes.OK, "").Proto(),
			ContinuationTokens: []*btpb.StreamContinuationToken{btContinuationToken(partition, cursor)},
		}},
	})
}

// btDataChangeResponse renders one recorded change. The mutations travel whole
// — the simulator holds each value in memory and never splits one across
// messages — and the tiebreaker is zero because a single-cluster instance has
// no concurrent conflicting write to resolve.
func btDataChangeResponse(rec btChangeRecord, clusterID string) *btpb.ReadChangeStreamResponse {
	chunks := make([]*btpb.ReadChangeStreamResponse_MutationChunk, 0, len(rec.mutations))
	for _, m := range rec.mutations {
		chunks = append(chunks, &btpb.ReadChangeStreamResponse_MutationChunk{Mutation: m})
	}
	return &btpb.ReadChangeStreamResponse{
		StreamRecord: &btpb.ReadChangeStreamResponse_DataChange_{DataChange: &btpb.ReadChangeStreamResponse_DataChange{
			Type:                  btpb.ReadChangeStreamResponse_DataChange_USER,
			SourceClusterId:       clusterID,
			RowKey:                []byte(rec.rowKey),
			CommitTimestamp:       timestamppb.New(rec.commitTime),
			Chunks:                chunks,
			Done:                  true,
			Token:                 strconv.FormatInt(rec.sequence, 10),
			EstimatedLowWatermark: timestamppb.New(rec.commitTime),
		}},
	}
}

// ── session API (OpenTable / OpenAuthorizedView / OpenMaterializedView) ──────

// The session entry points are bidirectional streams carrying the same point
// reads and mutations the unary data plane serves: a client opens a session
// against a table or an authorized view, then sends virtual RPCs — one
// SessionReadRowRequest or SessionMutateRowRequest each — and receives a
// response per rpc_id. They read and write the one per-table cell store, so a
// row mutated over a session is visible to ReadRows and vice versa.

// btSessionStream is the shape the three session entry points share: the
// generated server streams differ only in the RPC they belong to.
type btSessionStream interface {
	Context() context.Context
	Send(*btpb.SessionResponse) error
	Recv() (*btpb.SessionRequest, error)
}

// btSession is one opened session: the response payload the open handshake
// answers with, the cluster serving it, and the handler that executes each
// virtual RPC's payload.
type btSession struct {
	openPayload []byte
	clusterID   string
	zoneID      string
	serve       func(payload []byte) ([]byte, error)
}

// btServeSession runs the session protocol: an open handshake, then virtual
// RPCs until the client closes the session or the stream ends.
func btServeSession(stream btSessionStream, open func(payload []byte) (*btSession, error)) error {
	req, err := stream.Recv()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	openReq := req.GetOpenSession()
	if openReq == nil {
		return status.Error(codes.InvalidArgument, "the first request on a session stream must be open_session")
	}
	session, err := open(openReq.GetPayload())
	if err != nil {
		return err
	}
	clusterInfo := &btpb.ClusterInformation{ClusterId: session.clusterID, ZoneId: session.zoneID}
	if err := stream.Send(&btpb.SessionResponse{Payload: &btpb.SessionResponse_OpenSession{
		OpenSession: &btpb.OpenSessionResponse{
			Payload: session.openPayload,
			Backend: &btpb.BackendIdentifier{ApplicationFrontendZone: session.zoneID},
		},
	}}); err != nil {
		return err
	}

	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch payload := req.GetPayload().(type) {
		case *btpb.SessionRequest_CloseSession:
			return nil
		case *btpb.SessionRequest_OpenSession:
			return status.Error(codes.InvalidArgument, "the session is already open")
		case *btpb.SessionRequest_VirtualRpc:
			vrpc := payload.VirtualRpc
			started := time.Now()
			result, serveErr := session.serve(vrpc.GetPayload())
			if serveErr != nil {
				if err := stream.Send(&btpb.SessionResponse{Payload: &btpb.SessionResponse_Error{
					Error: &btpb.ErrorResponse{
						RpcId:       vrpc.GetRpcId(),
						ClusterInfo: clusterInfo,
						Status:      status.Convert(serveErr).Proto(),
					},
				}}); err != nil {
					return err
				}
				continue
			}
			if err := stream.Send(&btpb.SessionResponse{Payload: &btpb.SessionResponse_VirtualRpc{
				VirtualRpc: &btpb.VirtualRpcResponse{
					RpcId:       vrpc.GetRpcId(),
					ClusterInfo: clusterInfo,
					Stats:       &btpb.SessionRequestStats{BackendLatency: durationpb.New(time.Since(started))},
					Payload:     result,
				},
			}}); err != nil {
				return err
			}
		default:
			return status.Error(codes.InvalidArgument, "session request payload is required")
		}
	}
}

// btSessionPermissions splits a session's requested permission into what it may
// read and what it may write. An unset permission opens the session for both,
// the access a client gets when it asks for none in particular. The table,
// authorized-view and materialized-view permission enums are separate types
// with identical numbering, so the split is made on the number they share.
func btSessionPermissions(permission int32) (canRead, canWrite bool) {
	switch permission {
	case int32(btpb.OpenTableRequest_PERMISSION_READ):
		return true, false
	case int32(btpb.OpenTableRequest_PERMISSION_WRITE):
		return false, true
	default:
		return true, true
	}
}

func (s *bigtableDataGRPC) OpenTable(stream btpb.Bigtable_OpenTableServer) error {
	return btServeSession(stream, func(payload []byte) (*btSession, error) {
		var open btpb.OpenTableRequest
		if err := proto.Unmarshal(payload, &open); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "open_session payload is not an OpenTableRequest: %v", err)
		}
		tableName := open.GetTableName()
		td, err := btRequireTable(tableName)
		if err != nil {
			return nil, err
		}
		instance := btInstanceOfTable(tableName)
		if err := btRequireAppProfile(instance, open.GetAppProfileId()); err != nil {
			return nil, err
		}
		clusterID, zoneID, err := btInstanceCluster(instance)
		if err != nil {
			return nil, err
		}
		canRead, canWrite := btSessionPermissions(int32(open.GetPermission()))
		openPayload, err := proto.Marshal(&btpb.OpenTableResponse{})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encoding OpenTableResponse: %v", err)
		}
		return &btSession{
			openPayload: openPayload,
			clusterID:   clusterID,
			zoneID:      zoneID,
			serve: func(payload []byte) ([]byte, error) {
				var req btpb.TableRequest
				if err := proto.Unmarshal(payload, &req); err != nil {
					return nil, status.Errorf(codes.InvalidArgument, "virtual RPC payload is not a TableRequest: %v", err)
				}
				var resp btpb.TableResponse
				switch p := req.GetPayload().(type) {
				case *btpb.TableRequest_ReadRow:
					row, err := btSessionReadRow(td, nil, p.ReadRow, canRead)
					if err != nil {
						return nil, err
					}
					resp.Payload = &btpb.TableResponse_ReadRow{ReadRow: &btpb.SessionReadRowResponse{Row: row}}
				case *btpb.TableRequest_MutateRow:
					if err := btSessionMutateRow(tableName, td, nil, p.MutateRow, canWrite); err != nil {
						return nil, err
					}
					resp.Payload = &btpb.TableResponse_MutateRow{MutateRow: &btpb.SessionMutateRowResponse{}}
				default:
					return nil, status.Error(codes.InvalidArgument, "TableRequest carries no read_row or mutate_row")
				}
				return proto.Marshal(&resp)
			},
		}, nil
	})
}

func (s *bigtableDataGRPC) OpenAuthorizedView(stream btpb.Bigtable_OpenAuthorizedViewServer) error {
	return btServeSession(stream, func(payload []byte) (*btSession, error) {
		var open btpb.OpenAuthorizedViewRequest
		if err := proto.Unmarshal(payload, &open); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "open_session payload is not an OpenAuthorizedViewRequest: %v", err)
		}
		viewName := open.GetAuthorizedViewName()
		subset, tableName, err := btAuthorizedViewSubset(viewName)
		if err != nil {
			return nil, err
		}
		td, err := btRequireTable(tableName)
		if err != nil {
			return nil, err
		}
		instance := btInstanceOfTable(tableName)
		if err := btRequireAppProfile(instance, open.GetAppProfileId()); err != nil {
			return nil, err
		}
		clusterID, zoneID, err := btInstanceCluster(instance)
		if err != nil {
			return nil, err
		}
		canRead, canWrite := btSessionPermissions(int32(open.GetPermission()))
		openPayload, err := proto.Marshal(&btpb.OpenAuthorizedViewResponse{})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encoding OpenAuthorizedViewResponse: %v", err)
		}
		return &btSession{
			openPayload: openPayload,
			clusterID:   clusterID,
			zoneID:      zoneID,
			serve: func(payload []byte) ([]byte, error) {
				var req btpb.AuthorizedViewRequest
				if err := proto.Unmarshal(payload, &req); err != nil {
					return nil, status.Errorf(codes.InvalidArgument, "virtual RPC payload is not an AuthorizedViewRequest: %v", err)
				}
				var resp btpb.AuthorizedViewResponse
				switch p := req.GetPayload().(type) {
				case *btpb.AuthorizedViewRequest_ReadRow:
					row, err := btSessionReadRow(td, subset, p.ReadRow, canRead)
					if err != nil {
						return nil, err
					}
					resp.Payload = &btpb.AuthorizedViewResponse_ReadRow{ReadRow: &btpb.SessionReadRowResponse{Row: row}}
				case *btpb.AuthorizedViewRequest_MutateRow:
					if err := btSessionMutateRow(tableName, td, subset, p.MutateRow, canWrite); err != nil {
						return nil, err
					}
					resp.Payload = &btpb.AuthorizedViewResponse_MutateRow{MutateRow: &btpb.SessionMutateRowResponse{}}
				default:
					return nil, status.Error(codes.InvalidArgument, "AuthorizedViewRequest carries no read_row or mutate_row")
				}
				return proto.Marshal(&resp)
			},
		}, nil
	})
}

// btSessionReadRow serves one session read from the table's cell store,
// through the same filter evaluator ReadRows uses.
func btSessionReadRow(td *btTableData, subset *btViewSubset, req *btpb.SessionReadRowRequest, canRead bool) (*btpb.Row, error) {
	if !canRead {
		return nil, status.Error(codes.PermissionDenied, "the session was opened without read permission")
	}
	rowKey := string(req.GetKey())
	if subset != nil && !subset.coversRow(rowKey) {
		return nil, status.Errorf(codes.PermissionDenied, "row %q is outside the authorized view", rowKey)
	}
	td.mu.Lock()
	cells := btGatherRow(td, rowKey)
	td.mu.Unlock()
	if subset != nil {
		cells = subset.filterCells(cells)
	}
	var err error
	if cells, err = btApplyFilter(rowKey, cells, req.GetFilter()); err != nil {
		return nil, err
	}
	if len(cells) == 0 {
		return nil, nil
	}
	return btRowToProto(rowKey, cells), nil
}

// btSessionMutateRow applies one session mutation to the table's cell store,
// through the same path MutateRow takes — including its change-log record.
func btSessionMutateRow(tableName string, td *btTableData, subset *btViewSubset, req *btpb.SessionMutateRowRequest, canWrite bool) error {
	if !canWrite {
		return status.Error(codes.PermissionDenied, "the session was opened without write permission")
	}
	if len(req.GetMutations()) == 0 {
		return status.Error(codes.InvalidArgument, "mutations is required")
	}
	rowKey := string(req.GetKey())
	if subset != nil {
		if err := subset.authorizeMutations(rowKey, req.GetMutations()); err != nil {
			return err
		}
	}
	td.mu.Lock()
	defer td.mu.Unlock()
	if err := btApplyMutations(tableName, td, btTableFamilies(tableName), rowKey, req.GetMutations()); err != nil {
		return err
	}
	btPersistTableData(tableName, td)
	return nil
}

// ── authorized-view subsets ──────────────────────────────────────────────────

// btViewSubset is the slice of a table an authorized view exposes: the row
// prefixes it covers and, per column family, the qualifiers within it. It is
// read from the AuthorizedView resource the admin surface stored, so a session
// on a view sees exactly what the view was created with.
type btViewSubset struct {
	rowPrefixes []string
	families    map[string]btViewFamilySubset
}

type btViewFamilySubset struct {
	qualifiers        []string
	qualifierPrefixes []string
}

func (v *btViewSubset) coversRow(rowKey string) bool {
	for _, p := range v.rowPrefixes {
		if strings.HasPrefix(rowKey, p) {
			return true
		}
	}
	return false
}

func (v *btViewSubset) coversColumn(family, qualifier string) bool {
	fam, ok := v.families[family]
	if !ok {
		return false
	}
	for _, q := range fam.qualifiers {
		if q == qualifier {
			return true
		}
	}
	for _, p := range fam.qualifierPrefixes {
		if strings.HasPrefix(qualifier, p) {
			return true
		}
	}
	return false
}

func (v *btViewSubset) filterCells(cells []btReadCell) []btReadCell {
	return btFilterCells(cells, func(c btReadCell) bool { return v.coversColumn(c.family, c.qual) })
}

// authorizeMutations rejects a write that would touch a cell outside the view.
func (v *btViewSubset) authorizeMutations(rowKey string, muts []*btpb.Mutation) error {
	if !v.coversRow(rowKey) {
		return status.Errorf(codes.PermissionDenied, "row %q is outside the authorized view", rowKey)
	}
	for _, m := range muts {
		switch mut := m.GetMutation().(type) {
		case *btpb.Mutation_SetCell_:
			if !v.coversColumn(mut.SetCell.GetFamilyName(), string(mut.SetCell.GetColumnQualifier())) {
				return status.Errorf(codes.PermissionDenied, "column %q:%q is outside the authorized view",
					mut.SetCell.GetFamilyName(), mut.SetCell.GetColumnQualifier())
			}
		case *btpb.Mutation_DeleteFromColumn_:
			if !v.coversColumn(mut.DeleteFromColumn.GetFamilyName(), string(mut.DeleteFromColumn.GetColumnQualifier())) {
				return status.Errorf(codes.PermissionDenied, "column %q:%q is outside the authorized view",
					mut.DeleteFromColumn.GetFamilyName(), mut.DeleteFromColumn.GetColumnQualifier())
			}
		default:
			// A family- or row-wide delete reaches columns the view does not
			// expose, so it is not a mutation a view session can express.
			return status.Error(codes.PermissionDenied, "an authorized view session can only mutate columns the view exposes")
		}
	}
	return nil
}

// btAuthorizedViewSubset reads an authorized view's subset from the resource
// the admin surface stored, and returns the table it is a view of.
func btAuthorizedViewSubset(viewName string) (*btViewSubset, string, error) {
	if viewName == "" {
		return nil, "", status.Error(codes.InvalidArgument, "authorized_view_name is required")
	}
	idx := strings.Index(viewName, "/authorizedViews/")
	if idx < 0 {
		return nil, "", status.Errorf(codes.InvalidArgument, "%q is not an authorized view name", viewName)
	}
	resource, ok := bigtableAuthViews.Get(viewName)
	if !ok {
		return nil, "", status.Errorf(codes.NotFound, "authorized view %q not found", viewName)
	}
	view, ok := resource["subsetView"].(map[string]any)
	if !ok {
		return nil, "", status.Errorf(codes.FailedPrecondition, "authorized view %q has no subsetView", viewName)
	}
	subset := &btViewSubset{families: map[string]btViewFamilySubset{}}
	prefixes, err := btDecodeBytesList(view["rowPrefixes"])
	if err != nil {
		return nil, "", status.Errorf(codes.FailedPrecondition, "authorized view %q: rowPrefixes: %v", viewName, err)
	}
	subset.rowPrefixes = prefixes
	families, _ := view["familySubsets"].(map[string]any)
	for family, raw := range families {
		spec, _ := raw.(map[string]any)
		qualifiers, err := btDecodeBytesList(spec["qualifiers"])
		if err != nil {
			return nil, "", status.Errorf(codes.FailedPrecondition, "authorized view %q: family %q qualifiers: %v", viewName, family, err)
		}
		qualifierPrefixes, err := btDecodeBytesList(spec["qualifierPrefixes"])
		if err != nil {
			return nil, "", status.Errorf(codes.FailedPrecondition, "authorized view %q: family %q qualifierPrefixes: %v", viewName, family, err)
		}
		subset.families[family] = btViewFamilySubset{qualifiers: qualifiers, qualifierPrefixes: qualifierPrefixes}
	}
	return subset, viewName[:idx], nil
}

// btDecodeBytesList decodes the base64 the REST admin surface stores a repeated
// bytes field as.
func btDecodeBytesList(raw any) ([]string, error) {
	list, _ := raw.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		encoded, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("expected a base64 string, got %T", item)
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decoding %q: %w", encoded, err)
		}
		out = append(out, string(decoded))
	}
	return out, nil
}

// ── SQL over the data plane (PrepareQuery / ExecuteQuery) ────────────────────

// Bigtable's SQL surface is served for the query shape the simulator can
// answer from what it holds: `SELECT * FROM <table>`, which reads the table's
// stored rows. The result set is the one GoogleSQL defines for it — a `_key`
// BYTES column followed by one MAP<BYTES, BYTES> column per column family,
// each carrying that row's latest value per qualifier.
//
// Every other query is refused with a loud Unimplemented naming what is
// served. A simulator that answered an unparsed query with an empty result set
// would report "no rows" for a query that has rows, which is worse than a
// refusal: the client cannot tell the difference.

// btPreparedQuery is one prepared statement: the table it reads and the result
// schema ExecuteQuery must produce for it.
type btPreparedQuery struct {
	tableName  string
	columns    []*btpb.ColumnMetadata
	validUntil time.Time
}

var bigtablePreparedQueries = struct {
	mu      sync.Mutex
	queries map[string]btPreparedQuery
}{queries: map[string]btPreparedQuery{}}

// btPreparedQueryLifetime is how long a prepared query stays usable. Real
// Bigtable expires a prepared query so clients re-plan against a changed
// schema; the simulator expires it for the same reason, and a column family
// added after preparation reaches the client through the refreshed plan.
const btPreparedQueryLifetime = time.Hour

var btSelectStarQuery = regexp.MustCompile("(?is)^\\s*SELECT\\s+\\*\\s+FROM\\s+`?([A-Za-z0-9_][A-Za-z0-9_.-]*)`?\\s*;?\\s*$")

// btPlanQuery resolves a query string against the instance's tables, returning
// the table it reads and the columns its result set carries.
func btPlanQuery(instanceName, query string) (string, []*btpb.ColumnMetadata, error) {
	match := btSelectStarQuery.FindStringSubmatch(query)
	if match == nil {
		return "", nil, status.Errorf(codes.Unimplemented,
			"the simulator serves `SELECT * FROM <table>`; it cannot plan %q", query)
	}
	tableName := instanceName + "/tables/" + match[1]
	table, ok := bigtableTables.Get(tableName)
	if !ok {
		return "", nil, status.Errorf(codes.NotFound, "table %q not found", tableName)
	}
	bytesType := &btpb.Type{Kind: &btpb.Type_BytesType{BytesType: &btpb.Type_Bytes{}}}
	columns := []*btpb.ColumnMetadata{{Name: "_key", Type: bytesType}}
	for _, family := range btSortedFamilies(table) {
		columns = append(columns, &btpb.ColumnMetadata{
			Name: family,
			Type: &btpb.Type{Kind: &btpb.Type_MapType{MapType: &btpb.Type_Map{
				KeyType:   bytesType,
				ValueType: bytesType,
			}}},
		})
	}
	return tableName, columns, nil
}

func btSortedFamilies(table bigtableTable) []string {
	families := make([]string, 0, len(table.ColumnFamilies))
	for family := range table.ColumnFamilies {
		families = append(families, family)
	}
	sort.Strings(families)
	return families
}

func (s *bigtableDataGRPC) PrepareQuery(_ context.Context, req *btpb.PrepareQueryRequest) (*btpb.PrepareQueryResponse, error) {
	if err := btRequireInstance(req.GetInstanceName()); err != nil {
		return nil, err
	}
	if err := btRequireAppProfile(req.GetInstanceName(), req.GetAppProfileId()); err != nil {
		return nil, err
	}
	if req.GetProtoFormat() == nil {
		return nil, status.Error(codes.InvalidArgument, "data_format must be proto_format")
	}
	if len(req.GetParamTypes()) > 0 {
		return nil, status.Errorf(codes.Unimplemented,
			"the simulator serves `SELECT * FROM <table>`, which binds no parameters")
	}
	tableName, columns, err := btPlanQuery(req.GetInstanceName(), req.GetQuery())
	if err != nil {
		return nil, err
	}
	prepared := []byte(generateUUID())
	validUntil := time.Now().Add(btPreparedQueryLifetime)
	bigtablePreparedQueries.mu.Lock()
	// An expired plan can never be executed again, so it is dropped here rather
	// than held for the life of the process.
	for token, query := range bigtablePreparedQueries.queries {
		if time.Now().After(query.validUntil) {
			delete(bigtablePreparedQueries.queries, token)
		}
	}
	bigtablePreparedQueries.queries[string(prepared)] = btPreparedQuery{
		tableName:  tableName,
		columns:    columns,
		validUntil: validUntil,
	}
	bigtablePreparedQueries.mu.Unlock()
	return &btpb.PrepareQueryResponse{
		Metadata:      &btpb.ResultSetMetadata{Schema: &btpb.ResultSetMetadata_ProtoSchema{ProtoSchema: &btpb.ProtoSchema{Columns: columns}}},
		PreparedQuery: prepared,
		ValidUntil:    timestamppb.New(validUntil),
	}, nil
}

func (s *bigtableDataGRPC) ExecuteQuery(req *btpb.ExecuteQueryRequest, srv btpb.Bigtable_ExecuteQueryServer) error {
	if err := btRequireInstance(req.GetInstanceName()); err != nil {
		return err
	}
	if err := btRequireAppProfile(req.GetInstanceName(), req.GetAppProfileId()); err != nil {
		return err
	}
	if len(req.GetParams()) > 0 || len(req.GetViewParameters()) > 0 {
		return status.Errorf(codes.Unimplemented,
			"the simulator serves `SELECT * FROM <table>`, which binds no parameters")
	}

	prepared := req.GetPreparedQuery()
	query := req.GetQuery()
	if (len(prepared) == 0) == (query == "") {
		return status.Error(codes.InvalidArgument, "exactly one of query and prepared_query is required")
	}

	var plan btPreparedQuery
	if len(prepared) > 0 {
		if req.GetProtoFormat() != nil {
			return status.Error(codes.InvalidArgument, "data_format must be empty when prepared_query is set")
		}
		bigtablePreparedQueries.mu.Lock()
		stored, ok := bigtablePreparedQueries.queries[string(prepared)]
		bigtablePreparedQueries.mu.Unlock()
		if !ok {
			return status.Error(codes.FailedPrecondition, "the prepared query is not known to this server")
		}
		if time.Now().After(stored.validUntil) {
			return status.Error(codes.FailedPrecondition, "the prepared query has expired")
		}
		plan = stored
	} else {
		tableName, columns, err := btPlanQuery(req.GetInstanceName(), query)
		if err != nil {
			return err
		}
		plan = btPreparedQuery{tableName: tableName, columns: columns}
	}

	// A resume token names how many rows the interrupted attempt delivered, so
	// a resumed execution continues after them.
	delivered := 0
	if token := req.GetResumeToken(); len(token) > 0 {
		n, err := strconv.Atoi(string(token))
		if err != nil || n < 0 {
			return status.Errorf(codes.InvalidArgument, "invalid resume_token %q", token)
		}
		delivered = n
	}

	td, err := btRequireTable(plan.tableName)
	if err != nil {
		return err
	}
	td.mu.Lock()
	keys := make([]string, 0, len(td.rows))
	for k := range td.rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]*btpb.Value, 0, len(keys)*len(plan.columns))
	for _, key := range keys[min(delivered, len(keys)):] {
		values = append(values, btQueryRowValues(td, key, plan.columns)...)
	}
	td.mu.Unlock()

	batch, err := proto.Marshal(&btpb.ProtoRows{Values: values})
	if err != nil {
		return status.Errorf(codes.Internal, "encoding result rows: %v", err)
	}
	checksum := crc32.Checksum(batch, crc32.MakeTable(crc32.Castagnoli))
	return srv.Send(&btpb.ExecuteQueryResponse{Response: &btpb.ExecuteQueryResponse_Results{
		Results: &btpb.PartialResultSet{
			PartialRows:   &btpb.PartialResultSet_ProtoRowsBatch{ProtoRowsBatch: &btpb.ProtoRowsBatch{BatchData: batch}},
			BatchChecksum: &checksum,
			ResumeToken:   []byte(strconv.Itoa(len(keys))),
		},
	}})
}

// btQueryRowValues renders one row as the flat value sequence a ProtoRows
// batch carries: the row key, then one map per column family holding that
// family's latest value per qualifier. Callers hold td.mu.
func btQueryRowValues(td *btTableData, rowKey string, columns []*btpb.ColumnMetadata) []*btpb.Value {
	values := make([]*btpb.Value, 0, len(columns))
	values = append(values, &btpb.Value{Kind: &btpb.Value_BytesValue{BytesValue: []byte(rowKey)}})
	row := td.rows[rowKey]
	for _, column := range columns[1:] {
		cells := row[column.GetName()]
		qualifiers := make([]string, 0, len(cells))
		for qualifier := range cells {
			qualifiers = append(qualifiers, qualifier)
		}
		sort.Strings(qualifiers)
		entries := make([]*btpb.Value, 0, len(qualifiers))
		for _, qualifier := range qualifiers {
			versions := cells[qualifier]
			if len(versions) == 0 {
				continue
			}
			entries = append(entries, &btpb.Value{Kind: &btpb.Value_ArrayValue{ArrayValue: &btpb.ArrayValue{Values: []*btpb.Value{
				{Kind: &btpb.Value_BytesValue{BytesValue: []byte(qualifier)}},
				{Kind: &btpb.Value_BytesValue{BytesValue: versions[0].value}}, // newest-first
			}}}})
		}
		values = append(values, &btpb.Value{Kind: &btpb.Value_ArrayValue{ArrayValue: &btpb.ArrayValue{Values: entries}}})
	}
	return values
}

// OpenMaterializedView opens a session on a materialized view, whose rows are
// the continuously maintained result of the GoogleSQL query the view was
// created with. The simulator stores that query string and nothing else: it
// materializes no result set, and it has no evaluator for the aggregate
// queries materialized views are defined by (GROUP BY over column families
// with windowed accumulators). A session on such a view could only read rows
// that were never computed, so the method stays on the embedded Unimplemented
// server and a client gets a clear status instead.

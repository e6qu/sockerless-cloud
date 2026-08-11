package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	fspb "cloud.google.com/go/firestore/apiv1/firestorepb"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	latpb "google.golang.org/genproto/googleapis/type/latlng"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	structpb "google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Firestore v1 gRPC data plane. This slice mounts the
// google.firestore.v1.Firestore service on the shared gRPC server so the
// high-level cloud.google.com/go/firestore client (which is gRPC-only) can talk
// to the simulator — the REST slice already serves the low-level REST client.
//
// Storage is the same fsDocuments store the REST slice owns; every RPC reads
// and writes that store through the existing helpers (fsApplyWrite,
// fsEvaluateQuery, fsEvalPrecondition, the value converters below). No document
// state lives here, and no query/transform/precondition logic is duplicated.
//
// Transactions share the fsTransactions store and the same opaque-token model
// as the REST slice: BeginTransaction issues a token pinning a read snapshot,
// reads carrying the token report that snapshot time, Commit applies the
// writes and retires the token, Rollback retires it.

// fsTimestampLayout matches nowTimestamp's millisecond-truncated RFC3339 form.
const fsTimestampLayout = "2006-01-02T15:04:05.000Z"

type firestoreGRPC struct {
	fspb.UnimplementedFirestoreServer
}

func registerFirestoreGRPC(gs *grpc.Server) {
	fspb.RegisterFirestoreServer(gs, &firestoreGRPC{})
}

// ---------------------------------------------------------------------------
// proto <-> REST-store converters
// ---------------------------------------------------------------------------

// fsValueToProto converts the REST JSON typed-value shape to the proto Value.
func fsValueToProto(v FSValue) *fspb.Value {
	switch {
	case v.NullValue != nil:
		return &fspb.Value{ValueType: &fspb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
	case v.BooleanValue != nil:
		return &fspb.Value{ValueType: &fspb.Value_BooleanValue{BooleanValue: *v.BooleanValue}}
	case v.IntegerValue != "":
		n, _ := strconv.ParseInt(v.IntegerValue, 10, 64)
		return &fspb.Value{ValueType: &fspb.Value_IntegerValue{IntegerValue: n}}
	case v.DoubleValue != nil:
		return &fspb.Value{ValueType: &fspb.Value_DoubleValue{DoubleValue: *v.DoubleValue}}
	case v.TimestampValue != "":
		return &fspb.Value{ValueType: &fspb.Value_TimestampValue{TimestampValue: fsTimestampToProto(v.TimestampValue)}}
	case v.StringValue != "":
		return &fspb.Value{ValueType: &fspb.Value_StringValue{StringValue: v.StringValue}}
	case v.BytesValue != "":
		b, err := base64.StdEncoding.DecodeString(v.BytesValue)
		if err != nil {
			b = []byte(v.BytesValue)
		}
		return &fspb.Value{ValueType: &fspb.Value_BytesValue{BytesValue: b}}
	case v.ReferenceValue != "":
		return &fspb.Value{ValueType: &fspb.Value_ReferenceValue{ReferenceValue: v.ReferenceValue}}
	case v.GeoPointValue != nil:
		return &fspb.Value{ValueType: &fspb.Value_GeoPointValue{GeoPointValue: &latpb.LatLng{
			Latitude:  v.GeoPointValue.Latitude,
			Longitude: v.GeoPointValue.Longitude,
		}}}
	case v.ArrayValue != nil:
		out := &fspb.ArrayValue{}
		out.Values = make([]*fspb.Value, 0, len(v.ArrayValue.Values))
		for _, e := range v.ArrayValue.Values {
			out.Values = append(out.Values, fsValueToProto(e))
		}
		return &fspb.Value{ValueType: &fspb.Value_ArrayValue{ArrayValue: out}}
	case v.MapValue != nil:
		out := &fspb.MapValue{Fields: make(map[string]*fspb.Value, len(v.MapValue.Fields))}
		for k, mv := range v.MapValue.Fields {
			out.Fields[k] = fsValueToProto(mv)
		}
		return &fspb.Value{ValueType: &fspb.Value_MapValue{MapValue: out}}
	default:
		return &fspb.Value{ValueType: &fspb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
	}
}

// fsValueFromProto converts the proto Value to the REST JSON typed-value shape.
func fsValueFromProto(v *fspb.Value) FSValue {
	if v == nil {
		return FSValue{}
	}
	switch vt := v.ValueType.(type) {
	case *fspb.Value_NullValue:
		return FSValue{NullValue: structpb.NullValue_NULL_VALUE}
	case *fspb.Value_BooleanValue:
		b := vt.BooleanValue
		return FSValue{BooleanValue: &b}
	case *fspb.Value_IntegerValue:
		return FSValue{IntegerValue: strconv.FormatInt(vt.IntegerValue, 10)}
	case *fspb.Value_DoubleValue:
		d := vt.DoubleValue
		return FSValue{DoubleValue: &d}
	case *fspb.Value_TimestampValue:
		return FSValue{TimestampValue: fsTimestampFromProto(vt.TimestampValue)}
	case *fspb.Value_StringValue:
		return FSValue{StringValue: vt.StringValue}
	case *fspb.Value_BytesValue:
		return FSValue{BytesValue: base64.StdEncoding.EncodeToString(vt.BytesValue)}
	case *fspb.Value_ReferenceValue:
		return FSValue{ReferenceValue: vt.ReferenceValue}
	case *fspb.Value_GeoPointValue:
		if vt.GeoPointValue == nil {
			return FSValue{GeoPointValue: &FSGeoPoint{}}
		}
		return FSValue{GeoPointValue: &FSGeoPoint{
			Latitude:  vt.GeoPointValue.GetLatitude(),
			Longitude: vt.GeoPointValue.GetLongitude(),
		}}
	case *fspb.Value_ArrayValue:
		if vt.ArrayValue == nil {
			return FSValue{ArrayValue: &FSArrayValue{}}
		}
		out := &FSArrayValue{Values: make([]FSValue, 0, len(vt.ArrayValue.Values))}
		for _, e := range vt.ArrayValue.Values {
			out.Values = append(out.Values, fsValueFromProto(e))
		}
		return FSValue{ArrayValue: out}
	case *fspb.Value_MapValue:
		if vt.MapValue == nil {
			return FSValue{MapValue: &FSMapValue{}}
		}
		out := &FSMapValue{Fields: make(map[string]FSValue, len(vt.MapValue.Fields))}
		for k, mv := range vt.MapValue.Fields {
			out.Fields[k] = fsValueFromProto(mv)
		}
		return FSValue{MapValue: out}
	default:
		return FSValue{}
	}
}

// fsDocToProto converts a stored FSDocument to its proto form.
func fsDocToProto(d FSDocument) *fspb.Document {
	out := &fspb.Document{
		Name:       d.Name,
		CreateTime: fsTimestampToProto(d.CreateTime),
		UpdateTime: fsTimestampToProto(d.UpdateTime),
		Fields:     make(map[string]*fspb.Value, len(d.Fields)),
	}
	for k, v := range d.Fields {
		out.Fields[k] = fsValueToProto(v)
	}
	return out
}

// fsDocFromProto converts a proto Document to the REST-store shape.
func fsDocFromProto(d *fspb.Document) FSDocument {
	out := FSDocument{
		Name:       d.GetName(),
		CreateTime: fsTimestampFromProto(d.GetCreateTime()),
		UpdateTime: fsTimestampFromProto(d.GetUpdateTime()),
		Fields:     make(map[string]FSValue, len(d.GetFields())),
	}
	for k, v := range d.GetFields() {
		out.Fields[k] = fsValueFromProto(v)
	}
	return out
}

func fsTimestampToProto(s string) *timestamppb.Timestamp {
	if s == "" {
		return nil
	}
	t, err := time.Parse(fsTimestampLayout, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return nil
		}
	}
	return timestamppb.New(t.UTC().Truncate(time.Millisecond))
}

func fsTimestampFromProto(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Truncate(time.Millisecond).Format(fsTimestampLayout)
}

// ---------------------------------------------------------------------------
// request-field converters
// ---------------------------------------------------------------------------

func fsPreconditionFromProto(p *fspb.Precondition) *fsPrecondition {
	if p == nil {
		return nil
	}
	switch ct := p.ConditionType.(type) {
	case *fspb.Precondition_Exists:
		b := ct.Exists
		return &fsPrecondition{Exists: &b}
	case *fspb.Precondition_UpdateTime:
		return &fsPrecondition{UpdateTime: fsTimestampFromProto(ct.UpdateTime)}
	}
	return nil
}

func fsWriteFromProto(w *fspb.Write) fsWrite {
	out := fsWrite{CurrentDocument: fsPreconditionFromProto(w.GetCurrentDocument())}
	if mask := w.GetUpdateMask(); mask != nil {
		fp := mask.GetFieldPaths()
		out.UpdateMask = &struct {
			FieldPaths []string `json:"fieldPaths"`
		}{FieldPaths: fp}
	}
	switch op := w.Operation.(type) {
	case *fspb.Write_Update:
		upd := fsDocFromProto(op.Update)
		out.Update = &upd
	case *fspb.Write_Delete:
		out.Delete = op.Delete
	case *fspb.Write_Transform:
		out.Transform = fsDocTransformFromProto(op.Transform)
	}
	if uts := w.GetUpdateTransforms(); len(uts) > 0 {
		out.UpdateTransforms = make([]fsFieldTransform, 0, len(uts))
		for _, t := range uts {
			out.UpdateTransforms = append(out.UpdateTransforms, fsFieldTransformFromProto(t))
		}
	}
	return out
}

func fsDocTransformFromProto(t *fspb.DocumentTransform) *fsDocumentTransform {
	if t == nil {
		return nil
	}
	out := &fsDocumentTransform{Document: t.GetDocument()}
	for _, ft := range t.GetFieldTransforms() {
		out.FieldTransforms = append(out.FieldTransforms, fsFieldTransformFromProto(ft))
	}
	return out
}

func fsFieldTransformFromProto(t *fspb.DocumentTransform_FieldTransform) fsFieldTransform {
	out := fsFieldTransform{FieldPath: t.GetFieldPath()}
	switch tt := t.TransformType.(type) {
	case *fspb.DocumentTransform_FieldTransform_SetToServerValue:
		if tt.SetToServerValue == fspb.DocumentTransform_FieldTransform_REQUEST_TIME {
			out.SetToServerValue = fsServerValueEnum("REQUEST_TIME")
		}
	case *fspb.DocumentTransform_FieldTransform_Increment:
		v := fsValueFromProto(tt.Increment)
		out.Increment = &v
	case *fspb.DocumentTransform_FieldTransform_Maximum:
		v := fsValueFromProto(tt.Maximum)
		out.Maximum = &v
	case *fspb.DocumentTransform_FieldTransform_Minimum:
		v := fsValueFromProto(tt.Minimum)
		out.Minimum = &v
	case *fspb.DocumentTransform_FieldTransform_AppendMissingElements:
		out.AppendMissingElements = fsArrayValueFromProto(tt.AppendMissingElements)
	case *fspb.DocumentTransform_FieldTransform_RemoveAllFromArray:
		out.RemoveAllFromArray = fsArrayValueFromProto(tt.RemoveAllFromArray)
	}
	return out
}

func fsArrayValueFromProto(a *fspb.ArrayValue) *FSArrayValue {
	if a == nil {
		return &FSArrayValue{}
	}
	out := &FSArrayValue{Values: make([]FSValue, 0, len(a.GetValues()))}
	for _, e := range a.GetValues() {
		out.Values = append(out.Values, fsValueFromProto(e))
	}
	return out
}

// fsWriteResultFromMap converts an fsApplyWrite result map to a WriteResult.
func fsWriteResultFromMap(m map[string]any) *fspb.WriteResult {
	wr := &fspb.WriteResult{}
	if ut, ok := m["updateTime"].(string); ok {
		wr.UpdateTime = fsTimestampToProto(ut)
	}
	if trs, ok := m["transformResults"].([]FSValue); ok {
		wr.TransformResults = make([]*fspb.Value, 0, len(trs))
		for _, r := range trs {
			wr.TransformResults = append(wr.TransformResults, fsValueToProto(r))
		}
	}
	return wr
}

// fsGRPCErr converts an fsWriteError to a gRPC status error.
func fsGRPCErr(e *fsWriteError) error {
	if e == nil {
		return nil
	}
	return status.Error(fsCode(e.grpcCode), e.message)
}

func fsCode(n int) codes.Code {
	switch n {
	case fsGRPCNotFound:
		return codes.NotFound
	case fsGRPCAlreadyExists:
		return codes.AlreadyExists
	case fsGRPCFailedPrecondition:
		return codes.FailedPrecondition
	case 3:
		return codes.InvalidArgument
	default:
		return codes.Unknown
	}
}

// ---------------------------------------------------------------------------
// query evaluation (reuses the REST slice's pure helpers)
// ---------------------------------------------------------------------------

// fsEvaluateQuery runs a structured query against the store, returning the
// matching documents in order. It reuses fsWhereMatches, fsCompareToCursor,
// fsCursorIndex, and fsProjectDocument — the same evaluation path as the REST
// runQuery handler, with no duplicated filter/sort/cursor logic.
func fsEvaluateQuery(parent string, q fsStructuredQuery) []FSDocument {
	if len(q.From) == 0 || q.From[0].CollectionID == "" {
		return nil
	}
	collection := strings.TrimSuffix(parent, "/") + "/" + q.From[0].CollectionID
	docs := fsDocuments.Filter(func(d FSDocument) bool {
		return fsCollectionParent(d.Name) == collection && fsWhereMatches(d, q.Where)
	})

	sort.SliceStable(docs, func(i, j int) bool {
		for _, ob := range q.OrderBy {
			path := ob.Field.FieldPath
			if path == "" || path == "__name__" {
				continue
			}
			cmp := fsCompareValues(docs[i].Fields[path], docs[j].Fields[path])
			if cmp == 0 {
				continue
			}
			if fsEnum(ob.Direction) == "DESCENDING" {
				return cmp > 0
			}
			return cmp < 0
		}
		return docs[i].Name < docs[j].Name
	})

	if q.StartAt != nil {
		docs = docs[fsCursorIndex(docs, q.OrderBy, q.StartAt):]
	}
	if q.EndAt != nil {
		docs = docs[:fsCursorIndex(docs, q.OrderBy, q.EndAt)]
	}

	if q.Offset > 0 {
		if q.Offset >= len(docs) {
			docs = nil
		} else {
			docs = docs[q.Offset:]
		}
	}
	if q.Limit != nil && *q.Limit >= 0 && *q.Limit < len(docs) {
		docs = docs[:*q.Limit]
	}

	if q.Select != nil {
		projected := make([]FSDocument, 0, len(docs))
		for _, d := range docs {
			projected = append(projected, fsProjectDocument(d, q.Select))
		}
		docs = projected
	}
	return docs
}

// fsStructuredQueryFromProto converts a proto StructuredQuery to the REST-slice
// shape the shared query evaluator consumes.
func fsStructuredQueryFromProto(q *fspb.StructuredQuery) fsStructuredQuery {
	if q == nil {
		return fsStructuredQuery{}
	}
	out := fsStructuredQuery{
		Offset: int(q.GetOffset()),
	}
	for _, f := range q.GetFrom() {
		out.From = append(out.From, struct {
			CollectionID string `json:"collectionId"`
		}{CollectionID: f.GetCollectionId()})
	}
	out.Where = fsFilterFromProto(q.GetWhere())
	for _, ob := range q.GetOrderBy() {
		out.OrderBy = append(out.OrderBy, fsOrderBy{
			Field: struct {
				FieldPath string `json:"fieldPath"`
			}{FieldPath: ob.GetField().GetFieldPath()},
			Direction: fsDirectionEnum(fsDirectionName(int(ob.GetDirection()))),
		})
	}
	if sel := q.GetSelect(); sel != nil {
		proj := &fsProjection{}
		for _, f := range sel.GetFields() {
			proj.Fields = append(proj.Fields, struct {
				FieldPath string `json:"fieldPath"`
			}{FieldPath: f.GetFieldPath()})
		}
		out.Select = proj
	}
	if c := fsCursorFromProto(q.GetStartAt()); c != nil {
		out.StartAt = c
	}
	if c := fsCursorFromProto(q.GetEndAt()); c != nil {
		out.EndAt = c
	}
	if l := q.GetLimit(); l != nil {
		n := int(l.GetValue())
		out.Limit = &n
	}
	return out
}

func fsDirectionName(n int) string {
	switch fspb.StructuredQuery_Direction(n) {
	case fspb.StructuredQuery_ASCENDING:
		return "ASCENDING"
	case fspb.StructuredQuery_DESCENDING:
		return "DESCENDING"
	}
	return ""
}

func fsFilterFromProto(f *fspb.StructuredQuery_Filter) *fsFilter {
	if f == nil {
		return nil
	}
	switch ft := f.FilterType.(type) {
	case *fspb.StructuredQuery_Filter_CompositeFilter:
		cf := &struct {
			Op      fsCompositeOpEnum `json:"op"`
			Filters []fsFilter        `json:"filters"`
		}{}
		if ft.CompositeFilter.GetOp() != fspb.StructuredQuery_CompositeFilter_AND {
			cf.Op = fsCompositeOpEnum("OR")
		} else {
			cf.Op = fsCompositeOpEnum("AND")
		}
		for _, sub := range ft.CompositeFilter.GetFilters() {
			cf.Filters = append(cf.Filters, *fsFilterFromProto(sub))
		}
		return &fsFilter{CompositeFilter: cf}
	case *fspb.StructuredQuery_Filter_FieldFilter:
		ff := &struct {
			Field struct {
				FieldPath string `json:"fieldPath"`
			} `json:"field"`
			Op    fsFieldOpEnum `json:"op"`
			Value FSValue       `json:"value"`
		}{}
		ff.Field.FieldPath = ft.FieldFilter.GetField().GetFieldPath()
		ff.Op = fsFieldOpEnum(fsFieldOpName(int(ft.FieldFilter.GetOp())))
		ff.Value = fsValueFromProto(ft.FieldFilter.GetValue())
		return &fsFilter{FieldFilter: ff}
	case *fspb.StructuredQuery_Filter_UnaryFilter:
		uf := &struct {
			Op    fsUnaryOpEnum `json:"op"`
			Field struct {
				FieldPath string `json:"fieldPath"`
			} `json:"field"`
		}{}
		uf.Op = fsUnaryOpEnum(fsUnaryOpName(int(ft.UnaryFilter.GetOp())))
		uf.Field.FieldPath = ft.UnaryFilter.GetField().GetFieldPath()
		return &fsFilter{UnaryFilter: uf}
	}
	return nil
}

func fsFieldOpName(n int) string {
	switch fspb.StructuredQuery_FieldFilter_Operator(n) {
	case fspb.StructuredQuery_FieldFilter_LESS_THAN:
		return "LESS_THAN"
	case fspb.StructuredQuery_FieldFilter_LESS_THAN_OR_EQUAL:
		return "LESS_THAN_OR_EQUAL"
	case fspb.StructuredQuery_FieldFilter_GREATER_THAN:
		return "GREATER_THAN"
	case fspb.StructuredQuery_FieldFilter_GREATER_THAN_OR_EQUAL:
		return "GREATER_THAN_OR_EQUAL"
	case fspb.StructuredQuery_FieldFilter_EQUAL:
		return "EQUAL"
	case fspb.StructuredQuery_FieldFilter_NOT_EQUAL:
		return "NOT_EQUAL"
	case fspb.StructuredQuery_FieldFilter_ARRAY_CONTAINS:
		return "ARRAY_CONTAINS"
	case fspb.StructuredQuery_FieldFilter_IN:
		return "IN"
	case fspb.StructuredQuery_FieldFilter_ARRAY_CONTAINS_ANY:
		return "ARRAY_CONTAINS_ANY"
	case fspb.StructuredQuery_FieldFilter_NOT_IN:
		return "NOT_IN"
	}
	return ""
}

func fsUnaryOpName(n int) string {
	switch fspb.StructuredQuery_UnaryFilter_Operator(n) {
	case fspb.StructuredQuery_UnaryFilter_IS_NAN:
		return "IS_NAN"
	case fspb.StructuredQuery_UnaryFilter_IS_NOT_NAN:
		return "IS_NOT_NAN"
	case fspb.StructuredQuery_UnaryFilter_IS_NULL:
		return "IS_NULL"
	case fspb.StructuredQuery_UnaryFilter_IS_NOT_NULL:
		return "IS_NOT_NULL"
	}
	return ""
}

func fsCursorFromProto(c *fspb.Cursor) *fsCursor {
	if c == nil {
		return nil
	}
	out := &fsCursor{Before: c.GetBefore()}
	for _, v := range c.GetValues() {
		out.Values = append(out.Values, fsValueFromProto(v))
	}
	return out
}

// ---------------------------------------------------------------------------
// field-mask projection for GetDocument / ListDocuments
// ---------------------------------------------------------------------------

func fsProjectDocByMask(d FSDocument, mask *fspb.DocumentMask) FSDocument {
	if mask == nil || len(mask.GetFieldPaths()) == 0 {
		return d
	}
	out := FSDocument{
		Name:       d.Name,
		CreateTime: d.CreateTime,
		UpdateTime: d.UpdateTime,
		Fields:     map[string]FSValue{},
	}
	for _, p := range mask.GetFieldPaths() {
		if p == "__name__" {
			continue
		}
		if v, ok := d.Fields[p]; ok {
			out.Fields[p] = v
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// transactions
// ---------------------------------------------------------------------------

// fsBeginTxnBytes creates a transaction token (opaque bytes), pins a read
// snapshot time, and persists it in the shared fsTransactions store.
func fsBeginTxnBytes(readOnly bool, readTime string) []byte {
	token := []byte(generateUUID())
	if readTime == "" {
		readTime = fsNow()
	}
	fsTransactions.Put(string(token), fsTxn{ID: string(token), ReadTime: readTime, ReadOnly: readOnly})
	return token
}

// fsReadTimeForTxnBytes resolves the snapshot time for a byte-encoded token.
func fsReadTimeForTxnBytes(b []byte) (string, bool) {
	return fsReadTimeForTxn(string(b))
}

// ---------------------------------------------------------------------------
// Firestore RPCs
// ---------------------------------------------------------------------------

func (s *firestoreGRPC) GetDocument(_ context.Context, req *fspb.GetDocumentRequest) (*fspb.Document, error) {
	doc, ok := fsDocuments.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Document not found: %s", req.GetName())
	}
	return fsDocToProto(fsProjectDocByMask(doc, req.GetMask())), nil
}

func (s *firestoreGRPC) CreateDocument(_ context.Context, req *fspb.CreateDocumentRequest) (*fspb.Document, error) {
	parent := strings.TrimSuffix(req.GetParent(), "/")
	collectionID := req.GetCollectionId()
	if collectionID == "" {
		return nil, status.Error(codes.InvalidArgument, "collection_id is required")
	}
	docID := req.GetDocumentId()
	if docID == "" {
		docID = generateUUID()
	}
	name := parent + "/" + collectionID + "/" + docID
	if _, ok := fsDocuments.Get(name); ok {
		return nil, status.Errorf(codes.AlreadyExists, "Document already exists: %s", name)
	}
	incoming := fsDocFromProto(req.GetDocument())
	incoming.Name = name
	stored := fsPutDocument(incoming)
	return fsDocToProto(fsProjectDocByMask(stored, req.GetMask())), nil
}

func (s *firestoreGRPC) UpdateDocument(_ context.Context, req *fspb.UpdateDocumentRequest) (*fspb.Document, error) {
	doc := req.GetDocument()
	if doc == nil || doc.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "document.name is required")
	}
	upd := fsDocFromProto(doc)
	wr := fsWrite{
		Update:          &upd,
		CurrentDocument: fsPreconditionFromProto(req.GetCurrentDocument()),
	}
	if mask := req.GetUpdateMask(); mask != nil {
		fp := mask.GetFieldPaths()
		wr.UpdateMask = &struct {
			FieldPaths []string `json:"fieldPaths"`
		}{FieldPaths: fp}
	}
	res, e := fsApplyWrite(wr)
	if e != nil {
		return nil, fsGRPCErr(e)
	}
	_ = res
	stored, ok := fsDocuments.Get(doc.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Document not found: %s", doc.GetName())
	}
	return fsDocToProto(fsProjectDocByMask(stored, req.GetMask())), nil
}

func (s *firestoreGRPC) DeleteDocument(_ context.Context, req *fspb.DeleteDocumentRequest) (*emptypb.Empty, error) {
	wr := fsWrite{
		Delete:          req.GetName(),
		CurrentDocument: fsPreconditionFromProto(req.GetCurrentDocument()),
	}
	if _, e := fsApplyWrite(wr); e != nil {
		return nil, fsGRPCErr(e)
	}
	return &emptypb.Empty{}, nil
}

func (s *firestoreGRPC) ListDocuments(_ context.Context, req *fspb.ListDocumentsRequest) (*fspb.ListDocumentsResponse, error) {
	parent := strings.TrimSuffix(req.GetParent(), "/")
	collectionID := req.GetCollectionId()

	var prefix string
	if collectionID == "" {
		// Without a collection ID, list immediate documents across all
		// collections under parent.
		prefix = parent + "/"
	} else {
		prefix = parent + "/" + collectionID + "/"
	}

	docs := fsDocuments.Filter(func(d FSDocument) bool {
		if !strings.HasPrefix(d.Name, prefix) {
			return false
		}
		rest := strings.TrimPrefix(d.Name, prefix)
		return rest != "" && !strings.Contains(rest, "/")
	})
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })

	// Index-based pagination, mirroring the REST paginateList token scheme.
	start := 0
	if tok := req.GetPageToken(); tok != "" {
		n, err := strconv.Atoi(tok)
		if err != nil || n < 0 || n > len(docs) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid page_token %q", tok)
		}
		start = n
	}
	end := len(docs)
	if size := int(req.GetPageSize()); size > 0 && start+size < end {
		end = start + size
	}
	page := docs[start:end]

	resp := &fspb.ListDocumentsResponse{Documents: make([]*fspb.Document, 0, len(page))}
	for _, d := range page {
		resp.Documents = append(resp.Documents, fsDocToProto(fsProjectDocByMask(d, req.GetMask())))
	}
	if end < len(docs) {
		resp.NextPageToken = strconv.Itoa(end)
	}
	return resp, nil
}

func (s *firestoreGRPC) ListCollectionIds(_ context.Context, req *fspb.ListCollectionIdsRequest) (*fspb.ListCollectionIdsResponse, error) {
	parent := strings.TrimSuffix(req.GetParent(), "/") + "/"
	seen := map[string]struct{}{}
	for _, d := range fsDocuments.List() {
		if !strings.HasPrefix(d.Name, parent) {
			continue
		}
		rest := strings.TrimPrefix(d.Name, parent)
		segs := strings.Split(rest, "/")
		if len(segs) >= 2 {
			seen[segs[0]] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return &fspb.ListCollectionIdsResponse{CollectionIds: ids}, nil
}

func (s *firestoreGRPC) BatchGetDocuments(req *fspb.BatchGetDocumentsRequest, srv fspb.Firestore_BatchGetDocumentsServer) error {
	readTime, ok := fsReadTimeForTxnBytes(req.GetTransaction())
	if !ok {
		return status.Error(codes.InvalidArgument, "Invalid transaction.")
	}
	// A new_transaction begins a fresh transaction reported on the first
	// response in the stream.
	if nt := req.GetNewTransaction(); nt != nil {
		tok := fsBeginTxnBytes(nt.GetReadOnly() != nil, "")
		if err := srv.Send(&fspb.BatchGetDocumentsResponse{
			Transaction: tok,
			ReadTime:    fsTimestampToProto(fsReadTimeForTxnBytesOrNow(tok)),
		}); err != nil {
			return err
		}
	}
	rt := fsTimestampToProto(readTime)
	for _, name := range req.GetDocuments() {
		if doc, ok := fsDocuments.Get(name); ok {
			if err := srv.Send(&fspb.BatchGetDocumentsResponse{
				Result: &fspb.BatchGetDocumentsResponse_Found{
					Found: fsDocToProto(fsProjectDocByMask(doc, req.GetMask())),
				},
				ReadTime: rt,
			}); err != nil {
				return err
			}
		} else {
			if err := srv.Send(&fspb.BatchGetDocumentsResponse{
				Result:   &fspb.BatchGetDocumentsResponse_Missing{Missing: name},
				ReadTime: rt,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// fsReadTimeForTxnBytesOrNow is fsReadTimeForTxnBytes for a freshly-minted
// token, falling back to now if resolution fails.
func fsReadTimeForTxnBytesOrNow(b []byte) string {
	if rt, ok := fsReadTimeForTxnBytes(b); ok {
		return rt
	}
	return fsNow()
}

func (s *firestoreGRPC) RunQuery(req *fspb.RunQueryRequest, srv fspb.Firestore_RunQueryServer) error {
	readTime, ok := fsReadTimeForTxnBytes(req.GetTransaction())
	if !ok {
		return status.Error(codes.InvalidArgument, "Invalid transaction.")
	}
	sq := fsStructuredQueryFromProto(req.GetStructuredQuery())
	if len(sq.From) == 0 || sq.From[0].CollectionID == "" {
		return status.Error(codes.InvalidArgument, "structuredQuery.from[0].collectionId is required")
	}

	if nt := req.GetNewTransaction(); nt != nil {
		tok := fsBeginTxnBytes(nt.GetReadOnly() != nil, "")
		if err := srv.Send(&fspb.RunQueryResponse{
			Transaction: tok,
			ReadTime:    fsTimestampToProto(fsReadTimeForTxnBytesOrNow(tok)),
		}); err != nil {
			return err
		}
	}

	docs := fsEvaluateQuery(req.GetParent(), sq)
	rt := fsTimestampToProto(readTime)
	for _, d := range docs {
		if err := srv.Send(&fspb.RunQueryResponse{Document: fsDocToProto(d), ReadTime: rt}); err != nil {
			return err
		}
	}
	// A terminal response with no document marks the end of the stream.
	return srv.Send(&fspb.RunQueryResponse{
		ReadTime:             rt,
		ContinuationSelector: &fspb.RunQueryResponse_Done{Done: true},
	})
}

func (s *firestoreGRPC) RunAggregationQuery(req *fspb.RunAggregationQueryRequest, srv fspb.Firestore_RunAggregationQueryServer) error {
	readTime, ok := fsReadTimeForTxnBytes(req.GetTransaction())
	if !ok {
		return status.Error(codes.InvalidArgument, "Invalid transaction.")
	}
	saq := req.GetStructuredAggregationQuery()
	if saq == nil {
		return status.Error(codes.InvalidArgument, "structuredAggregationQuery is required")
	}
	sq := fsStructuredQueryFromProto(saq.GetStructuredQuery())
	if len(sq.From) == 0 || sq.From[0].CollectionID == "" {
		return status.Error(codes.InvalidArgument, "structuredQuery.from[0].collectionId is required")
	}

	if nt := req.GetNewTransaction(); nt != nil {
		tok := fsBeginTxnBytes(nt.GetReadOnly() != nil, "")
		if err := srv.Send(&fspb.RunAggregationQueryResponse{
			Transaction: tok,
			ReadTime:    fsTimestampToProto(fsReadTimeForTxnBytesOrNow(tok)),
		}); err != nil {
			return err
		}
	}

	docs := fsEvaluateQuery(req.GetParent(), sq)
	aggFields := map[string]*fspb.Value{}
	for i, a := range saq.GetAggregations() {
		alias := a.GetAlias()
		if alias == "" {
			alias = fmt.Sprintf("field_%d", i+1)
		}
		switch {
		case a.GetCount() != nil:
			count := int64(len(docs))
			if upTo := a.GetCount().GetUpTo(); upTo != nil && upTo.GetValue() > 0 && upTo.GetValue() < count {
				count = upTo.GetValue()
			}
			aggFields[alias] = &fspb.Value{ValueType: &fspb.Value_IntegerValue{IntegerValue: count}}
		case a.GetSum() != nil:
			sum, isDouble := fsAggregateNumeric(docs, a.GetSum().GetField().GetFieldPath())
			if isDouble {
				aggFields[alias] = &fspb.Value{ValueType: &fspb.Value_DoubleValue{DoubleValue: sum}}
			} else {
				aggFields[alias] = &fspb.Value{ValueType: &fspb.Value_IntegerValue{IntegerValue: int64(sum)}}
			}
		case a.GetAvg() != nil:
			_, isDouble := fsAggregateNumeric(docs, a.GetAvg().GetField().GetFieldPath())
			_ = isDouble // avg is always a double
			if len(docs) == 0 {
				aggFields[alias] = &fspb.Value{ValueType: &fspb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
			} else {
				sum, _ := fsAggregateNumeric(docs, a.GetAvg().GetField().GetFieldPath())
				aggFields[alias] = &fspb.Value{ValueType: &fspb.Value_DoubleValue{DoubleValue: sum / float64(len(docs))}}
			}
		}
	}
	return srv.Send(&fspb.RunAggregationQueryResponse{
		Result:   &fspb.AggregationResult{AggregateFields: aggFields},
		ReadTime: fsTimestampToProto(readTime),
	})
}

// fsAggregateNumeric sums the numeric values of fieldPath across docs, skipping
// non-numeric values. isDouble reports whether any summed value was a double
// (the sum is a double iff any operand was; empty sets sum to 0 as integer).
func fsAggregateNumeric(docs []FSDocument, fieldPath string) (sum float64, isDouble bool) {
	for _, d := range docs {
		v, ok := d.Fields[fieldPath]
		if !ok {
			continue
		}
		if v.DoubleValue != nil {
			isDouble = true
			sum += *v.DoubleValue
			continue
		}
		if v.IntegerValue != "" {
			if n, err := strconv.ParseFloat(v.IntegerValue, 64); err == nil {
				sum += n
			}
		}
	}
	return sum, isDouble
}

func (s *firestoreGRPC) BeginTransaction(_ context.Context, req *fspb.BeginTransactionRequest) (*fspb.BeginTransactionResponse, error) {
	opts := req.GetOptions()
	readOnly := opts.GetReadOnly() != nil
	readTime := ""
	if readOnly && opts.GetReadOnly().GetReadTime() != nil {
		readTime = fsTimestampFromProto(opts.GetReadOnly().GetReadTime())
	}
	tok := fsBeginTxnBytes(readOnly, readTime)
	return &fspb.BeginTransactionResponse{Transaction: tok}, nil
}

func (s *firestoreGRPC) Commit(_ context.Context, req *fspb.CommitRequest) (*fspb.CommitResponse, error) {
	// A transactional commit consumes the transaction: an unknown or
	// already-consumed token is rejected, and committing always retires the
	// token (a transaction commits at most once).
	if tx := req.GetTransaction(); len(tx) > 0 {
		if _, ok := fsTransactions.Get(string(tx)); !ok {
			return nil, status.Error(codes.InvalidArgument, "Invalid transaction.")
		}
		defer fsTransactions.Delete(string(tx))
	}
	resp := &fspb.CommitResponse{
		WriteResults: make([]*fspb.WriteResult, 0, len(req.GetWrites())),
		CommitTime:   fsTimestampToProto(fsNow()),
	}
	for _, w := range req.GetWrites() {
		res, e := fsApplyWrite(fsWriteFromProto(w))
		if e != nil {
			// Commit is atomic: the first failing write aborts the whole commit.
			return nil, fsGRPCErr(e)
		}
		resp.WriteResults = append(resp.WriteResults, fsWriteResultFromMap(res))
	}
	return resp, nil
}

func (s *firestoreGRPC) Rollback(_ context.Context, req *fspb.RollbackRequest) (*emptypb.Empty, error) {
	tx := req.GetTransaction()
	if len(tx) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Transaction is required.")
	}
	if _, ok := fsTransactions.Get(string(tx)); !ok {
		return nil, status.Error(codes.InvalidArgument, "Invalid transaction.")
	}
	fsTransactions.Delete(string(tx))
	return &emptypb.Empty{}, nil
}

func (s *firestoreGRPC) BatchWrite(_ context.Context, req *fspb.BatchWriteRequest) (*fspb.BatchWriteResponse, error) {
	// BatchWrite is non-atomic: each write succeeds or fails independently and
	// the response always carries a per-write google.rpc.Status.
	resp := &fspb.BatchWriteResponse{
		WriteResults: make([]*fspb.WriteResult, 0, len(req.GetWrites())),
		Status:       make([]*rpcstatus.Status, 0, len(req.GetWrites())),
	}
	for _, w := range req.GetWrites() {
		res, e := fsApplyWrite(fsWriteFromProto(w))
		if e != nil {
			resp.WriteResults = append(resp.WriteResults, &fspb.WriteResult{})
			resp.Status = append(resp.Status, &rpcstatus.Status{
				Code:    int32(fsCode(e.grpcCode)),
				Message: e.message,
			})
			continue
		}
		resp.WriteResults = append(resp.WriteResults, fsWriteResultFromMap(res))
		resp.Status = append(resp.Status, &rpcstatus.Status{Code: int32(codes.OK)})
	}
	return resp, nil
}

func (s *firestoreGRPC) PartitionQuery(_ context.Context, req *fspb.PartitionQueryRequest) (*fspb.PartitionQueryResponse, error) {
	// The simulator holds the whole collection in one process, so the faithful
	// partition of any query is a single (empty) cursor — the client runs the
	// query unpartitioned. Real Firestore returns split cursors for parallel
	// execution; with nothing to split across, one partition is the correct
	// answer and the response shape is preserved.
	_ = req
	return &fspb.PartitionQueryResponse{}, nil
}

// Listen and Write are bidirectional / streaming pipelines whose target-id
// assignment and stream-token bookkeeping have no REST analogue in the existing
// slice. They remain on the UnimplementedFirestoreServer default so a client
// that calls them gets a clear Unimplemented status rather than a partial or
// synthetic stream. ExecutePipeline is a newer RPC outside the
// cloud.google.com/go/firestore high-level client surface exercised here and is
// likewise left as the Unimplemented default.

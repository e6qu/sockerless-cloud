package main

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// The AIP-136 custom methods on a document parent. Each shares its URI shape
// with CreateDocument, so the POST dispatcher splits the trailing segment on a
// colon and routes here rather than creating a collection named after the verb.
func fsDocumentVerbHandled(w http.ResponseWriter, r *http.Request, parentPath, verb string) bool {
	switch verb {
	case "listCollectionIds":
		fsHandleListCollectionIds(w, r, parentPath)
	case "runAggregationQuery":
		fsHandleRunAggregationQuery(w, r, parentPath)
	case "partitionQuery":
		fsHandlePartitionQuery(w, r, parentPath)
	default:
		return false
	}
	return true
}

// fsHandleListCollectionIds reports the immediate child collections of a
// document or of the database root, which is what the parent addresses.
func fsHandleListCollectionIds(w http.ResponseWriter, r *http.Request, parentPath string) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	parent := fsFullName(project, database, strings.Trim(parentPath, "/"))
	var req struct {
		PageSize  int    `json:"pageSize"`
		PageToken string `json:"pageToken"`
	}
	_ = sim.ReadJSON(r, &req)

	prefix := strings.TrimSuffix(parent, "/") + "/"
	seen := map[string]bool{}
	for _, doc := range fsDocuments.Filter(func(d FSDocument) bool {
		return strings.HasPrefix(d.Name, prefix)
	}) {
		rest := strings.TrimPrefix(doc.Name, prefix)
		if collection, _, found := strings.Cut(rest, "/"); found || rest != "" {
			seen[collection] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	response := map[string]any{}
	if len(ids) > 0 {
		response["collectionIds"] = ids
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

// fsHandleRunAggregationQuery evaluates COUNT, SUM and AVG over the same
// documents runQuery selects, so an aggregation and the query it wraps can
// never disagree.
func fsHandleRunAggregationQuery(w http.ResponseWriter, r *http.Request, parentPath string) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	parent := fsFullName(project, database, strings.Trim(parentPath, "/"))
	var req struct {
		StructuredAggregationQuery struct {
			StructuredQuery fsStructuredQuery `json:"structuredQuery"`
			Aggregations    []struct {
				Alias string `json:"alias"`
				Count *struct {
					UpTo string `json:"upTo"`
				} `json:"count"`
				Sum *struct {
					Field struct {
						FieldPath string `json:"fieldPath"`
					} `json:"field"`
				} `json:"sum"`
				Avg *struct {
					Field struct {
						FieldPath string `json:"fieldPath"`
					} `json:"field"`
				} `json:"avg"`
			} `json:"aggregations"`
		} `json:"structuredAggregationQuery"`
		Transaction string `json:"transaction"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid runAggregationQuery body: %v", err)
		return
	}
	if _, ok := fsReadTimeForTxn(req.Transaction); !ok {
		sim.GCPError(w, http.StatusBadRequest, "Invalid transaction.", "INVALID_ARGUMENT")
		return
	}
	query := req.StructuredAggregationQuery.StructuredQuery
	if len(query.From) == 0 || query.From[0].CollectionID == "" {
		sim.GCPError(w, http.StatusBadRequest,
			"structuredAggregationQuery.structuredQuery.from[0].collectionId is required", "INVALID_ARGUMENT")
		return
	}
	if len(req.StructuredAggregationQuery.Aggregations) == 0 {
		sim.GCPError(w, http.StatusBadRequest,
			"structuredAggregationQuery.aggregations must not be empty", "INVALID_ARGUMENT")
		return
	}
	collection := strings.TrimSuffix(parent, "/") + "/" + query.From[0].CollectionID
	docs := fsDocuments.Filter(func(d FSDocument) bool {
		return fsCollectionParent(d.Name) == collection && fsWhereMatches(d, query.Where)
	})
	if query.Limit != nil && *query.Limit > 0 && len(docs) > *query.Limit {
		docs = docs[:*query.Limit]
	}

	fields := map[string]any{}
	for i, aggregation := range req.StructuredAggregationQuery.Aggregations {
		alias := aggregation.Alias
		if alias == "" {
			alias = "field_" + strconv.Itoa(i+1)
		}
		switch {
		case aggregation.Count != nil:
			count := len(docs)
			// upTo caps what the service will count, so a caller asking for
			// "at least N" is not charged for the whole collection.
			if limit, err := strconv.Atoi(aggregation.Count.UpTo); err == nil && limit > 0 && count > limit {
				count = limit
			}
			fields[alias] = map[string]any{"integerValue": strconv.Itoa(count)}
		case aggregation.Sum != nil:
			total, _ := fsNumericAggregate(docs, aggregation.Sum.Field.FieldPath)
			fields[alias] = fsNumericValue(total)
		case aggregation.Avg != nil:
			total, counted := fsNumericAggregate(docs, aggregation.Avg.Field.FieldPath)
			if counted == 0 {
				// Averaging nothing has no value, which the service reports as
				// a null rather than as zero. The enum's own spelling is the
				// string NULL_VALUE, not a JSON null.
				fields[alias] = map[string]any{"nullValue": "NULL_VALUE"}
				continue
			}
			fields[alias] = map[string]any{"doubleValue": total / float64(counted)}
		default:
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"aggregation %q names none of count, sum or avg", alias)
			return
		}
	}
	// The generated client decodes a single RunAggregationQueryResponse here,
	// not the streamed array runQuery returns — the two verbs differ on the
	// wire despite sharing a query.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"result":   map[string]any{"aggregateFields": fields},
		"readTime": fsNow(),
	})
}

// fsNumericAggregate totals a field across documents, reporting how many
// carried a number. Firestore's SUM and AVG skip documents where the field is
// absent or not numeric rather than failing the query.
func fsNumericAggregate(docs []FSDocument, fieldPath string) (total float64, counted int) {
	for _, doc := range docs {
		value, ok := doc.Fields[fieldPath]
		if !ok {
			continue
		}
		switch {
		case value.IntegerValue != "":
			parsed, err := strconv.ParseFloat(value.IntegerValue, 64)
			if err != nil {
				continue
			}
			total += parsed
			counted++
		case value.DoubleValue != nil:
			total += *value.DoubleValue
			counted++
		}
	}
	return total, counted
}

// fsNumericValue reports a SUM the way Firestore does: an integral total comes
// back as an integerValue, anything else as a doubleValue.
func fsNumericValue(total float64) map[string]any {
	if total == float64(int64(total)) {
		return map[string]any{"integerValue": strconv.FormatInt(int64(total), 10)}
	}
	return map[string]any{"doubleValue": total}
}

// fsHandlePartitionQuery splits a collection-group query into cursors a client
// can run in parallel. The service returns at most partitionCount-1 cursors and
// fewer when the data does not fill them; with one partition's worth of
// documents that is none at all, which is a complete answer rather than an
// empty one.
func fsHandlePartitionQuery(w http.ResponseWriter, r *http.Request, parentPath string) {
	project, database := sim.PathParam(r, "project"), sim.PathParam(r, "database")
	parent := fsFullName(project, database, strings.Trim(parentPath, "/"))
	var req struct {
		StructuredQuery fsStructuredQuery `json:"structuredQuery"`
		PartitionCount  string            `json:"partitionCount"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid partitionQuery body: %v", err)
		return
	}
	if len(req.StructuredQuery.From) == 0 || req.StructuredQuery.From[0].CollectionID == "" {
		sim.GCPError(w, http.StatusBadRequest,
			"structuredQuery.from[0].collectionId is required", "INVALID_ARGUMENT")
		return
	}
	partitions := int64(1)
	if req.PartitionCount != "" {
		parsed, err := strconv.ParseInt(req.PartitionCount, 10, 64)
		if err != nil || parsed < 1 {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"partitionCount %q is not a positive integer", req.PartitionCount)
			return
		}
		partitions = parsed
	}
	collection := req.StructuredQuery.From[0].CollectionID
	prefix := strings.TrimSuffix(parent, "/") + "/"
	docs := fsDocuments.Filter(func(d FSDocument) bool {
		return strings.HasPrefix(d.Name, prefix) && fsCollectionID(d.Name) == collection
	})
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })

	response := map[string]any{}
	if stride := int64(len(docs)) / partitions; stride > 0 && partitions > 1 {
		cursors := make([]any, 0, partitions-1)
		for i := int64(1); i < partitions; i++ {
			at := i * stride
			if at >= int64(len(docs)) {
				break
			}
			cursors = append(cursors, map[string]any{
				"values": []any{map[string]any{"referenceValue": docs[at].Name}},
				"before": true,
			})
		}
		if len(cursors) > 0 {
			response["partitions"] = cursors
		}
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

// fsCollectionID names the collection a document sits directly in.
func fsCollectionID(name string) string {
	parent := fsCollectionParent(name)
	return parent[strings.LastIndex(parent, "/")+1:]
}

// registerFSDocumentVerbs mounts the root-level custom methods. The
// document-parent ones ride the CreateDocument dispatcher, which cannot reach
// these: they hang off the collection segment itself.
func registerFSDocumentVerbs(srv *sim.Server) {
	// documents:write is the REST spelling of the bidirectional Write stream.
	// A REST caller gets one request and one response, so the stream's resume
	// tokens are answered as a stream that begins and ends within the call —
	// the writes themselves apply exactly as a commit's do.
	write := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			StreamID    string            `json:"streamId"`
			StreamToken string            `json:"streamToken"`
			Writes      []fsWrite         `json:"writes"`
			Labels      map[string]string `json:"labels"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid write body: %v", err)
			return
		}
		results := make([]map[string]any, 0, len(req.Writes))
		for _, one := range req.Writes {
			result, failure := fsApplyWrite(one)
			if failure != nil {
				sim.GCPError(w, failure.httpStatus, failure.message, failure.status)
				return
			}
			results = append(results, result)
		}
		response := map[string]any{
			"streamToken": fsStreamToken(),
			"commitTime":  fsNow(),
		}
		if len(results) > 0 {
			response["writeResults"] = results
		}
		// streamId comes back only on the message that opened the stream,
		// which for a REST caller is the one that carried no id.
		if req.StreamID == "" {
			response["streamId"] = generateUUID()
		}
		sim.WriteJSON(w, http.StatusOK, response)
	}
	srv.HandleFunc("POST /v1/projects/{project}/databases/{database}/documents:write", write)
}

// fsStreamToken mints the opaque position token a Write response carries.
func fsStreamToken() string {
	return base64.StdEncoding.EncodeToString([]byte(fsNow()))
}

// handleFSDatabasesVerb serves databases:clone and databases:restore. Both mint
// a new database from an existing source, so both refuse a destination id that
// is already taken and a source that does not exist.
func handleFSDatabasesVerb(w http.ResponseWriter, r *http.Request) {
	collection, verb, found := strings.Cut(sim.PathParam(r, "databasesVerb"), ":")
	if !found || collection != "databases" {
		gcpMethodNotFound(w)
		return
	}
	project := sim.PathParam(r, "project")
	var req struct {
		DatabaseID   string `json:"databaseId"`
		Backup       string `json:"backup"`
		PitrSnapshot *struct {
			Database     string `json:"database"`
			SnapshotTime string `json:"snapshotTime"`
		} `json:"pitrSnapshot"`
		Tags map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s body: %v", verb, err)
		return
	}
	if req.DatabaseID == "" {
		sim.GCPError(w, http.StatusBadRequest, "databaseId is required", "INVALID_ARGUMENT")
		return
	}
	destination := fsDatabaseName(project, req.DatabaseID)
	if _, exists := fsDatabases.Get(destination); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Database already exists: %s", destination)
		return
	}

	var source map[string]any
	switch verb {
	case "clone":
		if req.PitrSnapshot == nil || req.PitrSnapshot.Database == "" {
			sim.GCPError(w, http.StatusBadRequest, "pitrSnapshot.database is required", "INVALID_ARGUMENT")
			return
		}
		existing, ok := fsDatabases.Get(req.PitrSnapshot.Database)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Database not found: %s", req.PitrSnapshot.Database)
			return
		}
		source = existing.Body
	case "restore":
		if req.Backup == "" {
			sim.GCPError(w, http.StatusBadRequest, "backup is required", "INVALID_ARGUMENT")
			return
		}
		backup, ok := fsBackups.Get(req.Backup)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Backup not found: %s", req.Backup)
			return
		}
		source = backup.Body
	default:
		gcpMethodNotFound(w)
		return
	}

	now := fsNow()
	body := map[string]any{}
	for k, v := range source {
		body[k] = v
	}
	body["name"] = destination
	body["uid"] = generateUUID()
	body["createTime"] = now
	body["updateTime"] = now
	if len(req.Tags) > 0 {
		body["tags"] = req.Tags
	}
	fsDatabases.Put(destination, fsResource{Name: destination, Body: body})
	sim.WriteJSON(w, http.StatusOK,
		fsNewAdminOp(project, req.DatabaseID, body, "type.googleapis.com/google.firestore.admin.v1.Database", nil))
}

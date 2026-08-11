package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// AWS Glue connection metadata APIs can address the native Data Catalog
// without a ConnectionName. That path is implemented directly from durable
// Glue databases/tables and Amazon S3 objects. Connector-backed calls validate
// the durable connection and fail with the service's federation-source error
// when no connector runtime can answer the external data source.

type glueEntityRequest struct {
	CatalogId           string            `json:"CatalogId"`
	ConnectionName      string            `json:"ConnectionName"`
	DataStoreApiVersion string            `json:"DataStoreApiVersion"`
	ParentEntityName    string            `json:"ParentEntityName"`
	EntityName          string            `json:"EntityName"`
	Limit               int64             `json:"Limit"`
	ConnectionOptions   map[string]string `json:"ConnectionOptions"`
	FilterPredicate     string            `json:"FilterPredicate"`
	NextToken           string            `json:"NextToken"`
	OrderBy             string            `json:"OrderBy"`
	SelectedFields      []string          `json:"SelectedFields"`
}

func glueEntityConnection(w http.ResponseWriter, req glueEntityRequest) (GlueConnection, bool) {
	if req.ConnectionName == "" {
		return GlueConnection{}, true
	}
	connection, ok := glueConnections.Get(req.ConnectionName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Connection not found: "+req.ConnectionName)
		return GlueConnection{}, false
	}
	if connection.ConnectionType != "DYNAMODB" {
		glueWriteError(w, "FederationSourceException", "The configured connection source did not provide an entity metadata runtime")
		return GlueConnection{}, false
	}
	return connection, true
}

func handleGlueListEntities(w http.ResponseWriter, r *http.Request) {
	var req glueEntityRequest
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if _, ok := glueEntityConnection(w, req); !ok {
		return
	}
	parent := strings.Trim(req.ParentEntityName, "/.")
	entities := make([]map[string]any, 0)
	if req.ConnectionName != "" {
		if parent != "" {
			glueWriteError(w, "EntityNotFoundException", "Parent entity not found: "+req.ParentEntityName)
			return
		}
		for _, table := range ddbTables.List() {
			entities = append(entities, map[string]any{
				"EntityName":       table.TableName,
				"Label":            table.TableName,
				"Category":         "tables",
				"IsParentEntity":   false,
				"CustomProperties": map[string]string{"tableArn": table.TableArn},
			})
		}
	} else if parent == "" {
		for _, database := range glueDatabases.List() {
			entities = append(entities, map[string]any{
				"EntityName":     database.Name,
				"Label":          database.Name,
				"Description":    database.Description,
				"Category":       "databases",
				"IsParentEntity": true,
			})
		}
	} else {
		database, ok := glueDatabases.Get(parent)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Parent entity not found: "+req.ParentEntityName)
			return
		}
		for _, table := range glueTables.List() {
			if table.DatabaseName != database.Name {
				continue
			}
			description := table.Parameters["comment"]
			entities = append(entities, map[string]any{
				"EntityName":     database.Name + "." + table.Name,
				"Label":          table.Name,
				"Description":    description,
				"Category":       "tables",
				"IsParentEntity": false,
				"CustomProperties": map[string]string{
					"databaseName": database.Name,
					"tableName":    table.Name,
				},
			})
		}
	}
	sort.Slice(entities, func(i, j int) bool {
		return glueBusinessStringField(entities[i], "EntityName") < glueBusinessStringField(entities[j], "EntityName")
	})
	start, err := glueEntityTokenOffset(req.NextToken)
	if err != nil {
		glueWriteError(w, "InvalidInputException", err.Error())
		return
	}
	if start > len(entities) {
		start = len(entities)
	}
	const pageSize = 100
	end := start + pageSize
	if end > len(entities) {
		end = len(entities)
	}
	response := map[string]any{"Entities": entities[start:end]}
	if end < len(entities) {
		response["NextToken"] = strconv.Itoa(end)
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func glueEntityTokenOffset(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(token)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid NextToken")
	}
	return offset, nil
}

func glueResolveNativeEntity(identifier string) (GlueTable, bool) {
	normalized := strings.NewReplacer("/", ".", "::", ".").Replace(identifier)
	parts := strings.Split(normalized, ".")
	if len(parts) < 2 {
		return GlueTable{}, false
	}
	database := parts[len(parts)-2]
	table := parts[len(parts)-1]
	value, ok := glueTables.Get(glueTableKey(database, table))
	return value, ok
}

func handleGlueDescribeEntity(w http.ResponseWriter, r *http.Request) {
	var req glueEntityRequest
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if _, ok := glueEntityConnection(w, req); !ok {
		return
	}
	var fields []map[string]any
	if req.ConnectionName != "" {
		table, ok := ddbTables.Get(req.EntityName)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Entity not found: "+req.EntityName)
			return
		}
		fields = glueDynamoDBEntityFields(table)
	} else {
		table, ok := glueResolveNativeEntity(req.EntityName)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Entity not found: "+req.EntityName)
			return
		}
		fields = glueEntityFields(table)
	}
	start, err := glueEntityTokenOffset(req.NextToken)
	if err != nil {
		glueWriteError(w, "InvalidInputException", err.Error())
		return
	}
	if start > len(fields) {
		start = len(fields)
	}
	const pageSize = 100
	end := start + pageSize
	if end > len(fields) {
		end = len(fields)
	}
	response := map[string]any{"Fields": fields[start:end]}
	if end < len(fields) {
		response["NextToken"] = strconv.Itoa(end)
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func glueEntityFields(table GlueTable) []map[string]any {
	columns := glueEntityColumnMaps(table.StorageDescriptor["Columns"])
	fields := make([]map[string]any, 0, len(columns)+len(table.PartitionKeys))
	for _, column := range columns {
		fields = append(fields, glueEntityField(column, false))
	}
	for _, column := range table.PartitionKeys {
		fields = append(fields, glueEntityField(column, true))
	}
	return fields
}

func glueDynamoDBEntityFields(table DDBTable) []map[string]any {
	keyTypes := make(map[string]string, len(table.KeySchema))
	for _, key := range table.KeySchema {
		keyTypes[key.AttributeName] = key.KeyType
	}
	fields := make([]map[string]any, 0, len(table.AttributeDefinitions))
	for _, attribute := range table.AttributeDefinitions {
		fieldType := "STRING"
		switch attribute.AttributeType {
		case "N":
			fieldType = "DECIMAL"
		case "B":
			fieldType = "BINARY"
		}
		_, primary := keyTypes[attribute.AttributeName]
		fields = append(fields, map[string]any{
			"FieldName":                attribute.AttributeName,
			"Label":                    attribute.AttributeName,
			"FieldType":                fieldType,
			"NativeDataType":           attribute.AttributeType,
			"IsNullable":               !primary,
			"IsFilterable":             true,
			"IsPartitionable":          primary,
			"IsPrimaryKey":             primary,
			"IsRetrievable":            true,
			"IsCreateable":             true,
			"IsUpdateable":             true,
			"IsUpsertable":             true,
			"IsDefaultOnCreate":        false,
			"SupportedFilterOperators": []string{"EQUAL_TO", "LESS_THAN", "GREATER_THAN"},
		})
	}
	sort.Slice(fields, func(i, j int) bool {
		return glueBusinessStringField(fields[i], "FieldName") < glueBusinessStringField(fields[j], "FieldName")
	})
	return fields
}

func glueDynamoDBEntityRecords(tableName string) ([]map[string]any, bool) {
	if _, ok := ddbTables.Get(tableName); !ok {
		return nil, false
	}
	keys := ddbTableSortedKeys(tableName + "/")
	records := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		item, ok := ddbItems.Get(key)
		if !ok {
			continue
		}
		record := make(map[string]any, len(item))
		for name, value := range item {
			record[name] = glueDynamoDBAttributeValue(value)
		}
		records = append(records, record)
	}
	return records, true
}

func glueDynamoDBAttributeValue(value any) any {
	attribute, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if scalar, ok := attribute["S"]; ok {
		return fmt.Sprint(scalar)
	}
	if scalar, ok := attribute["N"]; ok {
		number := fmt.Sprint(scalar)
		if integer, err := strconv.ParseInt(number, 10, 64); err == nil {
			return integer
		}
		if decimal, err := strconv.ParseFloat(number, 64); err == nil {
			return decimal
		}
		return number
	}
	if scalar, ok := attribute["B"]; ok {
		return scalar
	}
	if scalar, ok := attribute["BOOL"]; ok {
		return scalar
	}
	if _, ok := attribute["NULL"]; ok {
		return nil
	}
	if raw, ok := attribute["M"].(map[string]any); ok {
		result := make(map[string]any, len(raw))
		for name, nested := range raw {
			result[name] = glueDynamoDBAttributeValue(nested)
		}
		return result
	}
	if raw, ok := attribute["L"].([]any); ok {
		result := make([]any, 0, len(raw))
		for _, nested := range raw {
			result = append(result, glueDynamoDBAttributeValue(nested))
		}
		return result
	}
	for _, setType := range []string{"SS", "NS", "BS"} {
		if raw, ok := attribute[setType].([]any); ok {
			result := make([]any, 0, len(raw))
			for _, nested := range raw {
				if setType == "NS" {
					result = append(result, glueDynamoDBAttributeValue(map[string]any{"N": nested}))
				} else {
					result = append(result, nested)
				}
			}
			return result
		}
	}
	return attribute
}

func glueEntityColumnMaps(value any) []map[string]any {
	switch columns := value.(type) {
	case []map[string]any:
		return columns
	case []any:
		result := make([]map[string]any, 0, len(columns))
		for _, value := range columns {
			if column, ok := value.(map[string]any); ok {
				result = append(result, column)
			}
		}
		return result
	default:
		return nil
	}
}

func glueEntityField(column map[string]any, partition bool) map[string]any {
	name, _ := column["Name"].(string)
	typeName, _ := column["Type"].(string)
	description, _ := column["Comment"].(string)
	return map[string]any{
		"FieldName":         name,
		"Label":             name,
		"Description":       description,
		"FieldType":         glueEntityFieldType(typeName),
		"NativeDataType":    typeName,
		"IsNullable":        true,
		"IsFilterable":      true,
		"IsPartitionable":   partition,
		"IsRetrievable":     true,
		"IsCreateable":      false,
		"IsUpdateable":      false,
		"IsUpsertable":      false,
		"IsDefaultOnCreate": false,
	}
}

func glueEntityFieldType(typeName string) string {
	base := strings.ToLower(strings.TrimSpace(typeName))
	if index := strings.IndexByte(base, '<'); index >= 0 {
		base = base[:index]
	}
	if index := strings.IndexByte(base, '('); index >= 0 {
		base = base[:index]
	}
	switch base {
	case "tinyint", "byte":
		return "BYTE"
	case "smallint", "short":
		return "SMALLINT"
	case "int", "integer":
		return "INT"
	case "bigint", "long":
		return "BIGINT"
	case "float":
		return "FLOAT"
	case "double":
		return "DOUBLE"
	case "decimal":
		return "DECIMAL"
	case "boolean":
		return "BOOLEAN"
	case "date":
		return "DATE"
	case "timestamp":
		return "TIMESTAMP"
	case "binary":
		return "BINARY"
	case "array":
		return "ARRAY"
	case "map":
		return "MAP"
	case "struct":
		return "STRUCT"
	case "union", "uniontype":
		return "UNION"
	default:
		return "STRING"
	}
}

func handleGlueGetEntityRecords(w http.ResponseWriter, r *http.Request) {
	var req glueEntityRequest
	if !glueDecodeBusinessRequest(w, r, &req) {
		return
	}
	if _, ok := glueEntityConnection(w, req); !ok {
		return
	}
	if req.Limit < 1 {
		glueWriteError(w, "InvalidInputException", "Limit must be greater than zero")
		return
	}
	var records []map[string]any
	if req.ConnectionName != "" {
		var ok bool
		records, ok = glueDynamoDBEntityRecords(req.EntityName)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Entity not found: "+req.EntityName)
			return
		}
	} else {
		table, ok := glueResolveNativeEntity(req.EntityName)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Entity not found: "+req.EntityName)
			return
		}
		var err error
		records, err = glueEntityReadRecords(table)
		if err != nil {
			glueWriteError(w, "FederationSourceException", err.Error())
			return
		}
	}
	records = glueEntityFilterRecords(records, req.FilterPredicate)
	glueEntitySortRecords(records, req.OrderBy)
	start, err := glueEntityTokenOffset(req.NextToken)
	if err != nil {
		glueWriteError(w, "InvalidInputException", err.Error())
		return
	}
	if start > len(records) {
		start = len(records)
	}
	limit := int(req.Limit)
	end := start + limit
	if end > len(records) {
		end = len(records)
	}
	selected := make([]map[string]any, 0, end-start)
	for _, record := range records[start:end] {
		selected = append(selected, glueEntitySelectFields(record, req.SelectedFields))
	}
	response := map[string]any{"Records": selected}
	if end < len(records) {
		response["NextToken"] = strconv.Itoa(end)
	}
	glueWriteJSON(w, http.StatusOK, response)
}

func glueEntityReadRecords(table GlueTable) ([]map[string]any, error) {
	location, _ := table.StorageDescriptor["Location"].(string)
	if location == "" {
		return []map[string]any{}, nil
	}
	bucket, prefix, ok := strings.Cut(strings.TrimPrefix(location, "s3://"), "/")
	if !strings.HasPrefix(location, "s3://") || !ok || bucket == "" {
		return nil, fmt.Errorf("table location is not an Amazon S3 URI: %s", location)
	}
	objects := s3Objects.Filter(func(object S3Object) bool {
		return strings.HasPrefix(object.Key, bucket+"/"+strings.TrimPrefix(prefix, "/"))
	})
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	columns := glueEntityColumnMaps(table.StorageDescriptor["Columns"])
	records := make([]map[string]any, 0)
	for _, object := range objects {
		parsed, err := glueEntityParseObject(object, table, columns)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", object.Key, err)
		}
		records = append(records, parsed...)
	}
	return records, nil
}

func glueEntityParseObject(object S3Object, table GlueTable, columns []map[string]any) ([]map[string]any, error) {
	serde, _ := table.StorageDescriptor["SerdeInfo"].(map[string]any)
	library, _ := serde["SerializationLibrary"].(string)
	inputFormat, _ := table.StorageDescriptor["InputFormat"].(string)
	lowerFormat := strings.ToLower(library + " " + inputFormat + " " + object.ContentType + " " + object.Key)
	if strings.Contains(lowerFormat, "json") || strings.HasSuffix(strings.ToLower(object.Key), ".json") ||
		strings.HasSuffix(strings.ToLower(object.Key), ".jsonl") {
		return glueEntityParseJSON(object.Data)
	}
	if strings.Contains(lowerFormat, "csv") || strings.Contains(lowerFormat, "textinputformat") ||
		strings.HasSuffix(strings.ToLower(object.Key), ".csv") {
		return glueEntityParseCSV(object.Data, columns, table.Parameters)
	}
	return nil, fmt.Errorf("unsupported table input format %q", inputFormat)
}

func glueEntityParseJSON(data []byte) ([]map[string]any, error) {
	var array []map[string]any
	if err := json.Unmarshal(data, &array); err == nil {
		return array, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	records := make([]map[string]any, 0)
	for {
		var record map[string]any
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if len(records) == 0 && len(bytes.TrimSpace(data)) > 0 {
		return nil, fmt.Errorf("object does not contain JSON records")
	}
	return records, nil
}

func glueEntityParseCSV(data []byte, columns []map[string]any, parameters map[string]string) ([]map[string]any, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		name, _ := column["Name"].(string)
		names = append(names, name)
	}
	if parameters["skip.header.line.count"] == "1" && len(rows) > 0 {
		rows = rows[1:]
	}
	records := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		record := make(map[string]any, len(names))
		for index, name := range names {
			if index < len(row) {
				record[name] = row[index]
			}
		}
		records = append(records, record)
	}
	return records, nil
}

func glueEntityFilterRecords(records []map[string]any, predicate string) []map[string]any {
	predicate = strings.TrimSpace(predicate)
	if predicate == "" {
		return records
	}
	left, right, ok := strings.Cut(predicate, "=")
	if !ok {
		return records
	}
	field := strings.Trim(strings.TrimSpace(left), "`\"")
	expected := strings.Trim(strings.TrimSpace(right), "'\"")
	filtered := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if fmt.Sprint(record[field]) == expected {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func glueEntitySortRecords(records []map[string]any, orderBy string) {
	fields := strings.Fields(strings.TrimSpace(orderBy))
	if len(fields) == 0 {
		return
	}
	field := strings.Trim(fields[0], "`\"")
	descending := len(fields) > 1 && strings.EqualFold(fields[1], "DESC")
	sort.SliceStable(records, func(i, j int) bool {
		left, right := fmt.Sprint(records[i][field]), fmt.Sprint(records[j][field])
		if descending {
			return left > right
		}
		return left < right
	})
}

func glueEntitySelectFields(record map[string]any, fields []string) map[string]any {
	if len(fields) == 0 {
		return record
	}
	selected := make(map[string]any, len(fields))
	for _, field := range fields {
		if value, ok := record[field]; ok {
			selected[field] = value
		}
	}
	return selected
}

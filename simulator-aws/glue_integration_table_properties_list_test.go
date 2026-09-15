package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func glueTablePropsCall(t *testing.T, handler http.HandlerFunc, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec.Code, out
}

func listedTables(t *testing.T, out map[string]any) []string {
	t.Helper()
	entries, ok := out["IntegrationTablePropertiesList"].([]any)
	require.True(t, ok, "IntegrationTablePropertiesList is a list: %v", out)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		entry := e.(map[string]any)
		names = append(names, entry["ResourceArn"].(string)+"/"+entry["TableName"].(string))
	}
	return names
}

func TestGlueListIntegrationTablePropertiesFiltersAndPages(t *testing.T) {
	buildConformanceSimulator(t)
	const src = "arn:aws:dynamodb:us-east-1:000000000000:table/src"
	const dst = "arn:aws:glue:us-east-1:000000000000:database/dst"
	for _, p := range []map[string]any{
		{"ResourceArn": src, "TableName": "orders", "SourceTableConfig": map[string]any{"Fields": []string{"id"}}},
		{"ResourceArn": src, "TableName": "users"},
		{"ResourceArn": dst, "TableName": "orders", "TargetTableConfig": map[string]any{"TargetTableName": "orders_copy"}},
	} {
		code, _ := glueTablePropsCall(t, handleGlueCreateIntegrationTableProperties, p)
		require.Equal(t, http.StatusOK, code)
	}

	code, out := glueTablePropsCall(t, handleGlueListIntegrationTableProperties, map[string]any{})
	require.Equal(t, http.StatusOK, code)
	// Ordered by resource ARN, then table: arn:aws:dynamodb sorts before arn:aws:glue.
	require.Equal(t, []string{src + "/orders", src + "/users", dst + "/orders"}, listedTables(t, out))
	require.NotContains(t, out, "Marker")

	_, out = glueTablePropsCall(t, handleGlueListIntegrationTableProperties, map[string]any{
		"Filters": []map[string]any{{"Name": "SourceArn", "Values": []string{src}}},
	})
	require.Equal(t, []string{src + "/orders", src + "/users"}, listedTables(t, out))

	_, out = glueTablePropsCall(t, handleGlueListIntegrationTableProperties, map[string]any{
		"Filters": []map[string]any{{"Name": "TargetTableName", "Values": []string{"orders"}}},
	})
	require.Equal(t, []string{src + "/orders", dst + "/orders"}, listedTables(t, out))

	seen := []string{}
	marker := ""
	for i := 0; i < 4; i++ {
		req := map[string]any{"MaxRecords": 1}
		if marker != "" {
			req["Marker"] = marker
		}
		_, page := glueTablePropsCall(t, handleGlueListIntegrationTableProperties, req)
		seen = append(seen, listedTables(t, page)...)
		next, _ := page["Marker"].(string)
		if next == "" {
			break
		}
		marker = next
	}
	require.Equal(t, []string{src + "/orders", src + "/users", dst + "/orders"}, seen, "pages cover every entry once, in order")

	code, out = glueTablePropsCall(t, handleGlueListIntegrationTableProperties, map[string]any{
		"Filters": []map[string]any{{"Name": "Status", "Values": []string{"ACTIVE"}}},
	})
	require.Equal(t, http.StatusBadRequest, code, "%v", out)
}

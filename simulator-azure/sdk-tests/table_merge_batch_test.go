package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTableClient(t *testing.T, account, table string) *aztables.Client {
	t.Helper()
	svc, err := aztables.NewServiceClientWithNoCredential(storageSDKURL(t, account, "table"),
		&aztables.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = svc.CreateTable(ctx, table, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.DeleteTable(ctx, table, nil) })
	return svc.NewClient(table)
}

// TestTableMergeVsReplace pins the Update Entity semantics through the real
// aztables SDK: UpdateModeMerge overlays only the supplied properties (omitted
// ones survive), while UpdateModeReplace drops omitted properties wholesale.
func TestTableMergeVsReplace(t *testing.T) {
	client := newTableClient(t, "sdktablemerge", "MergeTable")

	// Seed an entity with two non-key properties.
	seed := map[string]any{
		"PartitionKey": "p", "RowKey": "r",
		"Alpha": "one", "Beta": "two",
	}
	b, _ := json.Marshal(seed)
	_, err := client.AddEntity(ctx, b, nil)
	require.NoError(t, err)

	// MERGE supplying only Alpha — Beta must be preserved.
	mergeEntity := map[string]any{"PartitionKey": "p", "RowKey": "r", "Alpha": "merged"}
	mb, _ := json.Marshal(mergeEntity)
	_, err = client.UpdateEntity(ctx, mb, &aztables.UpdateEntityOptions{UpdateMode: aztables.UpdateModeMerge})
	require.NoError(t, err)

	got, err := client.GetEntity(ctx, "p", "r", nil)
	require.NoError(t, err)
	var afterMerge map[string]any
	require.NoError(t, json.Unmarshal(got.Value, &afterMerge))
	assert.Equal(t, "merged", afterMerge["Alpha"], "merge updated Alpha")
	assert.Equal(t, "two", afterMerge["Beta"], "merge preserved the omitted Beta")

	// REPLACE supplying only Alpha — Beta must be dropped.
	replaceEntity := map[string]any{"PartitionKey": "p", "RowKey": "r", "Alpha": "replaced"}
	rb, _ := json.Marshal(replaceEntity)
	_, err = client.UpdateEntity(ctx, rb, &aztables.UpdateEntityOptions{UpdateMode: aztables.UpdateModeReplace})
	require.NoError(t, err)

	got, err = client.GetEntity(ctx, "p", "r", nil)
	require.NoError(t, err)
	var afterReplace map[string]any
	require.NoError(t, json.Unmarshal(got.Value, &afterReplace))
	assert.Equal(t, "replaced", afterReplace["Alpha"])
	_, betaPresent := afterReplace["Beta"]
	assert.False(t, betaPresent, "replace must drop the omitted Beta")
}

// TestTableFilterDatetimeLiteral pins the OData $filter typed-literal handling:
// a `Timestamp gt datetime'…'` filter (the aztables EDMDateTime form) selects by
// the unwrapped value, not by comparing against the literal word "datetime".
func TestTableFilterDatetimeLiteral(t *testing.T) {
	client := newTableClient(t, "sdktablefilter", "FilterTable")

	// Two entities carrying an explicit EDM datetime property at different times.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, when := range []time.Time{old, recent} {
		ent := aztables.EDMEntity{
			Entity:     aztables.Entity{PartitionKey: "p", RowKey: fmt.Sprintf("r%d", i)},
			Properties: map[string]any{"Created": aztables.EDMDateTime(when)},
		}
		b, err := json.Marshal(ent)
		require.NoError(t, err)
		_, err = client.AddEntity(ctx, b, nil)
		require.NoError(t, err)
	}

	// Filter Created gt 2025 → only the recent (2030) entity.
	cutoff := "Created gt datetime'2025-01-01T00:00:00Z'"
	pager := client.NewListEntitiesPager(&aztables.ListEntitiesOptions{Filter: &cutoff})
	var rows []map[string]any
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, raw := range page.Entities {
			var m map[string]any
			require.NoError(t, json.Unmarshal(raw, &m))
			rows = append(rows, m)
		}
	}
	require.Len(t, rows, 1, "datetime filter must select only the post-cutoff entity")
	assert.Equal(t, "r1", rows[0]["RowKey"])
}

// TestTableTransactionalBatch pins the $batch transactional-batch handler: a
// multi-op change-set (two inserts) is replayed and the entities are queryable
// afterwards.
func TestTableTransactionalBatch(t *testing.T) {
	client := newTableClient(t, "sdktablebatch", "BatchTable")

	mk := func(rk, v string) []byte {
		b, _ := json.Marshal(map[string]any{"PartitionKey": "pk", "RowKey": rk, "Val": v})
		return b
	}
	actions := []aztables.TransactionAction{
		{ActionType: aztables.TransactionTypeAdd, Entity: mk("r1", "one")},
		{ActionType: aztables.TransactionTypeAdd, Entity: mk("r2", "two")},
	}
	_, err := client.SubmitTransaction(ctx, actions, nil)
	require.NoError(t, err, "transactional batch must succeed")

	// Both entities must now exist.
	for _, tc := range []struct{ rk, want string }{{"r1", "one"}, {"r2", "two"}} {
		got, err := client.GetEntity(ctx, "pk", tc.rk, nil)
		require.NoError(t, err, "entity %s must exist after batch", tc.rk)
		var m map[string]any
		require.NoError(t, json.Unmarshal(got.Value, &m))
		assert.Equal(t, tc.want, m["Val"])
	}
}

// TestTableSelectProjection pins the $select projection: a query selecting one
// non-key column returns only that column (plus the system keys), dropping the
// others.
func TestTableSelectProjection(t *testing.T) {
	client := newTableClient(t, "sdktableselect", "SelectTable")

	b, _ := json.Marshal(map[string]any{"PartitionKey": "p", "RowKey": "r", "Keep": "yes", "Drop": "no"})
	_, err := client.AddEntity(ctx, b, nil)
	require.NoError(t, err)

	pager := client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Select: ptr("Keep"),
	})
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Entities, 1)
	var m map[string]any
	require.NoError(t, json.Unmarshal(page.Entities[0], &m))
	assert.Equal(t, "yes", m["Keep"], "selected column present")
	_, dropPresent := m["Drop"]
	assert.False(t, dropPresent, "$select must drop unselected columns")
}

func ptr[T any](v T) *T { return &v }

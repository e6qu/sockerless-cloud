package azure_sdk_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCosmos_SessionTokenRoundTrip proves a write returns an x-ms-session-token
// and a subsequent Session-consistency read that sends that token back succeeds
// (read-your-writes), driven through the official azcosmos client — the session
// machinery in cosmos_consistency.go.
func TestCosmos_SessionTokenRoundTrip(t *testing.T) {
	client := newCosmosSDKClient(t, baseURL+"/")
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "sessdb"}, nil)
	require.NoError(t, err)
	db, _ := client.NewDatabase("sessdb")
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "sessc",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
	}, nil)
	require.NoError(t, err)
	cc, _ := client.NewContainer("sessdb", "sessc")

	pk := azcosmos.NewPartitionKeyString("p")
	raw, _ := json.Marshal(map[string]any{"id": "x", "pk": "p", "v": 1})
	writeResp, err := cc.CreateItem(ctx, pk, raw, nil)
	require.NoError(t, err, "CreateItem")
	require.NotNil(t, writeResp.SessionToken, "a write must return a session token")
	require.NotEmpty(t, *writeResp.SessionToken)

	// Read-your-writes: a Session read that sends the write's token back observes
	// the write and itself returns a session token.
	session := azcosmos.ConsistencyLevelSession.ToPtr()
	readResp, err := cc.ReadItem(ctx, pk, "x", &azcosmos.ItemOptions{
		ConsistencyLevel: session,
		SessionToken:     writeResp.SessionToken,
	})
	require.NoError(t, err, "Session ReadItem with prior write token")
	require.NotNil(t, readResp.SessionToken, "a Session read must echo a session token")
	var got map[string]any
	require.NoError(t, json.Unmarshal(readResp.Value, &got))
	assert.EqualValues(t, 1, got["v"])

	// A second write advances the session token monotonically past the first.
	raw2, _ := json.Marshal(map[string]any{"id": "x", "pk": "p", "v": 2})
	w2, err := cc.UpsertItem(ctx, pk, raw2, nil)
	require.NoError(t, err, "UpsertItem")
	require.NotNil(t, w2.SessionToken)
	assert.NotEqual(t, *writeResp.SessionToken, *w2.SessionToken,
		"a later write must issue a distinct (advanced) session token")
}

// TestCosmos_ConsistencyEscalationRejected proves the sim honors the account's
// consistency ceiling: the default account consistency is Session, so a request
// asking for Strong (a stronger level) is rejected with 400 BadRequest, exactly
// as real Cosmos rejects an escalation past the account maximum.
func TestCosmos_ConsistencyEscalationRejected(t *testing.T) {
	client := newCosmosSDKClient(t, baseURL+"/")
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "consdb"}, nil)
	require.NoError(t, err)
	db, _ := client.NewDatabase("consdb")
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "consc",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
	}, nil)
	require.NoError(t, err)
	cc, _ := client.NewContainer("consdb", "consc")

	pk := azcosmos.NewPartitionKeyString("p")
	raw, _ := json.Marshal(map[string]any{"id": "x", "pk": "p"})
	if _, err := cc.CreateItem(ctx, pk, raw, nil); err != nil {
		t.Fatalf("seed CreateItem: %v", err)
	}

	// Strong > the account's Session ceiling → 400.
	strong := azcosmos.ConsistencyLevelStrong.ToPtr()
	_, err = cc.ReadItem(ctx, pk, "x", &azcosmos.ItemOptions{ConsistencyLevel: strong})
	require.Error(t, err, "Strong read must be rejected against a Session-max account")
	var re *azcore.ResponseError
	require.True(t, errors.As(err, &re), "want an azcore.ResponseError")
	assert.Equal(t, 400, re.StatusCode, "escalation past the account max is a 400")

	// Eventual (weaker than Session) is permitted.
	eventual := azcosmos.ConsistencyLevelEventual.ToPtr()
	_, err = cc.ReadItem(ctx, pk, "x", &azcosmos.ItemOptions{ConsistencyLevel: eventual})
	require.NoError(t, err, "Eventual (weaker) read must be permitted")
}

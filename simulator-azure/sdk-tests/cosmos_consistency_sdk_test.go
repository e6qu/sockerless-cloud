package azure_sdk_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCosmos_SessionTokenRoundTrip: a write returns x-ms-session-token, and a
// Session read that sends it back sees the write.
func TestCosmos_SessionTokenRoundTrip(t *testing.T) {
	t.Parallel()
	dataPlaneAccount := uniquename.Alnum("sdkdataplane")
	client := cosmosSimClient(t, dataPlaneAccount)
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

	// The read itself returns a session token.
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

	// A second write advances the session token past the first.
	raw2, _ := json.Marshal(map[string]any{"id": "x", "pk": "p", "v": 2})
	w2, err := cc.UpsertItem(ctx, pk, raw2, nil)
	require.NoError(t, err, "UpsertItem")
	require.NotNil(t, w2.SessionToken)
	assert.NotEqual(t, *writeResp.SessionToken, *w2.SessionToken,
		"a later write must issue a distinct (advanced) session token")
}

// TestCosmos_ConsistencyEscalationRejected: the account's default consistency
// is Session, so a request for Strong is refused with 400 BadRequest, as Azure
// refuses an escalation past the account level.
func TestCosmos_ConsistencyEscalationRejected(t *testing.T) {
	t.Parallel()
	dataPlaneAccount := uniquename.Alnum("sdkdataplane")
	client := cosmosSimClient(t, dataPlaneAccount)
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

	strong := azcosmos.ConsistencyLevelStrong.ToPtr()
	_, err = cc.ReadItem(ctx, pk, "x", &azcosmos.ItemOptions{ConsistencyLevel: strong})
	require.Error(t, err, "Strong read must be rejected against a Session-max account")
	var re *azcore.ResponseError
	require.True(t, errors.As(err, &re), "want an azcore.ResponseError")
	assert.Equal(t, 400, re.StatusCode, "escalation past the account max is a 400")

	// Eventual is weaker than Session, so it is permitted.
	eventual := azcosmos.ConsistencyLevelEventual.ToPtr()
	_, err = cc.ReadItem(ctx, pk, "x", &azcosmos.ItemOptions{ConsistencyLevel: eventual})
	require.NoError(t, err, "Eventual (weaker) read must be permitted")
}

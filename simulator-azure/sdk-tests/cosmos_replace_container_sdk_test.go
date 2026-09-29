package azure_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCosmos_ReplaceContainer drives ContainerClient.Replace (a PUT of the
// container's properties to /dbs/{db}/colls/{coll}): it turns a container's
// time to live on and off after creation, and cannot change its partition key.
func TestCosmos_ReplaceContainer(t *testing.T) {
	client := cosmosSimClient(t, cosmosDataPlaneAccount)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "replacedb"}, nil)
	require.NoError(t, err)
	db, err := client.NewDatabase("replacedb")
	require.NoError(t, err)
	properties := azcosmos.ContainerProperties{
		ID:                     "replacec",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
	}
	_, err = db.CreateContainer(ctx, properties, nil)
	require.NoError(t, err)
	container, err := client.NewContainer("replacedb", "replacec")
	require.NoError(t, err)

	properties.DefaultTimeToLive = to.Ptr[int32](3600)
	replaced, err := container.Replace(ctx, properties, nil)
	require.NoError(t, err)
	require.NotNil(t, replaced.ContainerProperties.DefaultTimeToLive, "Replace answers the replaced defaultTtl")
	assert.EqualValues(t, 3600, *replaced.ContainerProperties.DefaultTimeToLive)
	read, err := container.Read(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, read.ContainerProperties.DefaultTimeToLive, "a read after Replace reports the new defaultTtl")
	assert.EqualValues(t, 3600, *read.ContainerProperties.DefaultTimeToLive)
	assert.Equal(t, []string{"/pk"}, read.ContainerProperties.PartitionKeyDefinition.Paths)

	moved := properties
	moved.PartitionKeyDefinition = azcosmos.PartitionKeyDefinition{Paths: []string{"/other"}}
	_, err = container.Replace(ctx, moved, nil)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "a partition-key change is refused: %v", err)
	assert.Equal(t, http.StatusBadRequest, respErr.StatusCode)
	assert.Regexp(t, `"code":\s*"BadRequest"`, err.Error())
	assert.Contains(t, err.Error(), "partition key of a container cannot be changed")

	properties.DefaultTimeToLive = nil
	_, err = container.Replace(ctx, properties, nil)
	require.NoError(t, err)
	read, err = container.Read(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, read.ContainerProperties.DefaultTimeToLive, "a Replace without defaultTtl turns time to live off")
	assert.Equal(t, []string{"/pk"}, read.ContainerProperties.PartitionKeyDefinition.Paths)
}

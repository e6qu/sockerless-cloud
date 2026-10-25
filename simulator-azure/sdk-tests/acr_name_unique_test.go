package azure_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A registry's name is its login server's host name, so a second resource
// group cannot create a registry under a name the first holds; the holder
// can write its own registry again.
func TestACR_RegistryNameIsUniqueAcrossResourceGroups(t *testing.T) {
	t.Parallel()
	first, second := uniquename.New("acr-unique-a"), uniquename.New("acr-unique-b")
	ensureRG(t, first)
	ensureRG(t, second)
	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name := uniquename.Alnum("acrunique")
	registry := armcontainerregistry.Registry{
		Location: to.Ptr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: to.Ptr(armcontainerregistry.SKUNameBasic)},
	}
	create := func(rg string) error {
		poller, err := client.BeginCreate(ctx, rg, name, registry, nil)
		if err != nil {
			return err
		}
		_, err = poller.PollUntilDone(ctx, nil)
		return err
	}
	require.NoError(t, create(first))
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, first, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})

	err = create(second)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "creating a taken name fails: %v", err)
	assert.Equal(t, http.StatusConflict, respErr.StatusCode)
	assert.Equal(t, "AlreadyInUse", respErr.ErrorCode)

	require.NoError(t, create(first), "the holder writes its own registry again")
}

package main

import (
	"sync"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/stretchr/testify/require"
)

// Callers that seal under an AWS owned key at the same moment, as a parallel
// Terraform apply's DB cluster and DB instance do, all seal under the one key
// material the first of them created.
func TestAWSOwnedKeyMaterialIsCreatedOnce(t *testing.T) {
	bg.Await()
	kmsKeyMaterial = sim.MakeStore[[]byte](nil, "kms_key_material")
	const callers = 16
	sealed := make([][]byte, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			sealed[i], errs[i] = rdsSealMasterPassword("MasterPassword-123!")
		}()
	}
	close(start)
	wg.Wait()
	for i := range callers {
		require.NoError(t, errs[i])
		_, plaintext, ok := kmsDecryptBytes(sealed[i])
		require.True(t, ok, "caller %d sealed under key material that no longer exists", i)
		require.Equal(t, "MasterPassword-123!", string(plaintext))
	}
}

package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// A siteConfig property the simulator does not act on reads back as written,
// and a PATCH decoded onto the stored configuration keeps what it leaves out.
func TestSiteConfigKeepsPropertiesItDoesNotModel(t *testing.T) {
	var stored SiteConfig
	require.NoError(t, json.Unmarshal([]byte(`{"linuxFxVersion":"NODE|20-lts","use32BitWorkerProcess":true,"http20Enabled":false}`), &stored))
	require.Equal(t, "NODE|20-lts", stored.LinuxFxVersion)

	out, err := json.Marshal(stored)
	require.NoError(t, err)
	require.JSONEq(t, `{"linuxFxVersion":"NODE|20-lts","use32BitWorkerProcess":true,"http20Enabled":false}`, string(out))

	patched := stored
	require.NoError(t, json.Unmarshal([]byte(`{"http20Enabled":true,"alwaysOn":true}`), &patched))
	out, err = json.Marshal(patched)
	require.NoError(t, err)
	require.JSONEq(t, `{"linuxFxVersion":"NODE|20-lts","alwaysOn":true,"use32BitWorkerProcess":true,"http20Enabled":true}`, string(out))

	out, err = json.Marshal(stored)
	require.NoError(t, err)
	require.JSONEq(t, `{"linuxFxVersion":"NODE|20-lts","use32BitWorkerProcess":true,"http20Enabled":false}`, string(out),
		"decoding a PATCH onto a copy leaves the stored configuration as it was")
}

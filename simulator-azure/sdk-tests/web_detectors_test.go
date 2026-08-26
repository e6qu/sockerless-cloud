package azure_sdk_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the App Service diagnostics family, which the simulator
// computes from the site's live workload container and its own records:
//
//	GET  .../sites/{siteName}/diagnostics (+ slot)
//	GET  .../sites/{siteName}/diagnostics/{diagnosticCategory} (+ slot)
//	GET  .../sites/{siteName}/diagnostics/{diagnosticCategory}/detectors (+ slot)
//	GET  .../sites/{siteName}/diagnostics/{diagnosticCategory}/detectors/{detectorName} (+ slot)
//	POST .../sites/{siteName}/diagnostics/{diagnosticCategory}/detectors/{detectorName}/execute (+ slot)
//	GET  .../sites/{siteName}/diagnostics/{diagnosticCategory}/analyses (+ slot)
//	GET  .../sites/{siteName}/diagnostics/{diagnosticCategory}/analyses/{analysisName} (+ slot)
//	POST .../sites/{siteName}/diagnostics/{diagnosticCategory}/analyses/{analysisName}/execute (+ slot)
//	GET  .../sites/{siteName}/detectors (+ slot)
//	GET  .../sites/{siteName}/detectors/{detectorName} (+ slot)

// TestSDK_Diagnostics_DetectorsMeasureTheWorkloadContainer proves the
// detectors report measurements rather than canned answers:
//
//   - before the site's container starts, every container-backed detector says
//     it measured nothing — the negative control;
//   - once it is running, the memory detector reports the engine's own limit
//     and usage counters and the thread detector reports the container's real
//     process table;
//   - the restart detector reports nothing until the site is actually
//     restarted, and reports that restart afterwards;
//   - a detector of Microsoft's catalog whose input the simulator does not
//     hold answers a declared gap naming that input, and a name that is not a
//     detector at all is a 404.
func TestSDK_Diagnostics_DetectorsMeasureTheWorkloadContainer(t *testing.T) {
	rg := "sdk-detector-rg"
	site := "detector-app"
	host := createSiteForContainers(t, rg, site)

	client, err := armappservice.NewDiagnosticsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	webApps, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = webApps.CreateOrUpdateSiteContainer(ctx, rg, site, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(true),
			StartUpCommand: to.Ptr("probe-retry detector-ok"),
		},
	}, nil)
	require.NoError(t, err)

	// The site publishes the one diagnostic category the simulator can
	// measure, and nothing else exists.
	categories := pagerAll(t, client.NewListSiteDiagnosticCategoriesPager(rg, site, nil),
		func(p armappservice.DiagnosticsClientListSiteDiagnosticCategoriesResponse) []*armappservice.DiagnosticCategory {
			return p.Value
		})
	require.Len(t, categories, 1)
	require.Equal(t, "availability", *categories[0].Name)
	category, err := client.GetSiteDiagnosticCategory(ctx, rg, site, "availability", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, *category.Properties.Description)
	_, err = client.GetSiteDiagnosticCategory(ctx, rg, site, "nosuchcategory", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")

	// Negative control: with no container started, the container-backed
	// detectors report that they measured nothing rather than zeros.
	before := executeDetector(t, client, rg, site, "sitememoryanalysis")
	assert.False(t, *before.Properties.IssueDetected)
	assert.Empty(t, before.Properties.Data,
		"a site with no container has no memory sample to report")
	beforeCrashes := executeDetector(t, client, rg, site, "sitecrashes")
	assert.False(t, *beforeCrashes.Properties.IssueDetected)
	assert.Empty(t, beforeCrashes.Properties.AbnormalTimePeriods)

	// Start the container the way a request to the site does.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/function", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "invoking the site must start its container: %s", body)

	// The detector catalog is Microsoft's, named and ranked the way its own
	// listing names and ranks it.
	detectors := pagerAll(t, client.NewListSiteDetectorsPager(rg, site, "availability", nil),
		func(p armappservice.DiagnosticsClientListSiteDetectorsResponse) []*armappservice.DetectorDefinitionResource {
			return p.Value
		})
	names := map[string]*armappservice.DetectorDefinitionResource{}
	for _, det := range detectors {
		names[*det.Name] = det
	}
	for _, want := range []string{"sitecrashes", "sitecpuanalysis", "sitememoryanalysis", "threadcount", "siterestartuserinitiated", "deployment"} {
		require.Contains(t, names, want)
	}
	require.Equal(t, "Site Crash Events", *names["sitecrashes"].Properties.DisplayName)
	require.Equal(t, float64(9), *names["sitecrashes"].Properties.Rank)
	single, err := client.GetSiteDetector(ctx, rg, site, "availability", "sitecrashes", nil)
	require.NoError(t, err)
	require.Equal(t, "Site Crash Events", *single.Properties.DisplayName)

	// Memory: the numbers are the container engine's own counters.
	memory := executeDetector(t, client, rg, site, "sitememoryanalysis")
	require.NotEmpty(t, memory.Properties.Data, "a running container has a memory sample")
	limit := detectorValue(t, memory.Properties.Data[0], "MemoryLimitBytes")
	limitBytes, err := strconv.ParseInt(limit, 10, 64)
	require.NoError(t, err)
	assert.Positive(t, limitBytes, "the memory limit is the engine's own counter")
	assert.Equal(t, "false", detectorValue(t, memory.Properties.Data[0], "OOMKilled"))
	assert.False(t, *memory.Properties.IssueDetected,
		"a container the kernel has not killed reports no memory issue")

	// Threads: the count is the container's real process table.
	threads := executeDetector(t, client, rg, site, "threadcount")
	require.NotEmpty(t, threads.Properties.Data, "a running container has processes")
	require.NotEmpty(t, threads.Properties.Metrics)
	require.NotEmpty(t, threads.Properties.Metrics[0].Values)
	assert.Positive(t, *threads.Properties.Metrics[0].Values[0].Total,
		"a running container has at least one thread")

	// CPU: sampled with the engine's throttling counters beside it.
	cpu := executeDetector(t, client, rg, site, "sitecpuanalysis")
	require.NotEmpty(t, cpu.Properties.Data)
	assert.NotEmpty(t, detectorValue(t, cpu.Properties.Data[0], "CpuPercentage"))

	// Restarts: nothing until the site is restarted, then exactly that restart.
	quiet := executeDetector(t, client, rg, site, "siterestartuserinitiated")
	assert.False(t, *quiet.Properties.IssueDetected,
		"a site nobody restarted reports no restart")
	assert.Empty(t, quiet.Properties.Data)

	_, err = webApps.Restart(ctx, rg, site, nil)
	require.NoError(t, err)
	restarted := executeDetector(t, client, rg, site, "siterestartuserinitiated")
	require.True(t, *restarted.Properties.IssueDetected,
		"the restart the site just went through must be reported")
	require.Len(t, restarted.Properties.Data, 1)
	assert.Equal(t, "Restart", detectorValue(t, restarted.Properties.Data[0], "Operation"))
	assert.Equal(t, "UserInitiated", detectorValue(t, restarted.Properties.Data[0], "Cause"))
	require.Len(t, restarted.Properties.AbnormalTimePeriods, 1)
	assert.Equal(t, armappservice.IssueTypeUserIssue, *restarted.Properties.AbnormalTimePeriods[0].Type)

	// The analysis aggregates the detectors it names and carries their events.
	analyses := pagerAll(t, client.NewListSiteAnalysesPager(rg, site, "availability", nil),
		func(p armappservice.DiagnosticsClientListSiteAnalysesResponse) []*armappservice.AnalysisDefinition {
			return p.Value
		})
	analysisNames := []string{}
	for _, a := range analyses {
		analysisNames = append(analysisNames, *a.Name)
	}
	assert.ElementsMatch(t, []string{"apprestartanalysis", "memoryanalysis"}, analysisNames)
	definition, err := client.GetSiteAnalysis(ctx, rg, site, "availability", "apprestartanalysis", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, *definition.Properties.Description)
	analysis, err := client.ExecuteSiteAnalysis(ctx, rg, site, "availability", "apprestartanalysis", nil)
	require.NoError(t, err)
	sources := []string{}
	for _, payload := range analysis.Properties.Payload {
		sources = append(sources, *payload.Source)
	}
	assert.Contains(t, sources, "siterestartuserinitiated")
	require.NotEmpty(t, analysis.Properties.AbnormalTimePeriods,
		"the restart the analysis aggregates must appear as an abnormal period")

	// The site's own detector collection renders the same measurements.
	responses := pagerAll(t, client.NewListSiteDetectorResponsesPager(rg, site, nil),
		func(p armappservice.DiagnosticsClientListSiteDetectorResponsesResponse) []*armappservice.DetectorResponse {
			return p.Value
		})
	require.NotEmpty(t, responses)
	byName := map[string]*armappservice.DetectorResponse{}
	for _, r := range responses {
		byName[*r.Name] = r
	}
	require.Contains(t, byName, "sitecrashes")
	require.NotNil(t, byName["sitecrashes"].Properties.Status)
	assert.NotEmpty(t, *byName["sitecrashes"].Properties.Status.Message)
	// The restart this test performed is in the table the detector renders —
	// the same measurement, in the other family's shape.
	one, err := client.GetSiteDetectorResponse(ctx, rg, site, "siterestartuserinitiated", nil)
	require.NoError(t, err)
	require.NotEmpty(t, one.Properties.Dataset)
	require.NotNil(t, one.Properties.Dataset[0].Table)
	require.Len(t, one.Properties.Dataset[0].Table.Rows, 1)
	assert.Equal(t, armappservice.InsightStatusInfo, *one.Properties.Status.StatusID)

	// Declared gaps: a detector of Microsoft's catalog the simulator cannot
	// measure names the input it would need; a name that is no detector at all
	// is not found.
	for _, gap := range []string{"servicehealth", "siteswap", "workeravailability", "committedmemoryusage"} {
		_, err = client.GetSiteDetectorResponse(ctx, rg, site, gap, nil)
		require.Error(t, err, gap)
		assert.Contains(t, err.Error(), "NotImplemented", gap)
		assert.Contains(t, err.Error(), "it would need", gap)
	}
	_, err = client.ExecuteSiteDetector(ctx, rg, site, "siteswap", "availability", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NotImplemented")
	_, err = client.ExecuteSiteAnalysis(ctx, rg, site, "availability", "perfanalysis", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NotImplemented")
	_, err = client.GetSiteDetector(ctx, rg, site, "availability", "nosuchdetector", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")

	// The slot spelling of the family is mounted on the site's slots.
	slotPoller, err := webApps.BeginCreateOrUpdateSlot(ctx, rg, site, "staging", armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotCategories := pagerAll(t, client.NewListSiteDiagnosticCategoriesSlotPager(rg, site, "staging", nil),
		func(p armappservice.DiagnosticsClientListSiteDiagnosticCategoriesSlotResponse) []*armappservice.DiagnosticCategory {
			return p.Value
		})
	require.Len(t, slotCategories, 1)
	assert.Contains(t, *slotCategories[0].ID, "/slots/staging/diagnostics/availability")
	slotDetector, err := client.GetSiteDetectorResponseSlot(ctx, rg, site, "sitecrashes", "staging", nil)
	require.NoError(t, err)
	assert.Contains(t, *slotDetector.ID, "/slots/staging/detectors/sitecrashes")
}

// TestSDK_Diagnostics_HostingEnvironmentPublishesNoDetectors covers the two
// environment-scoped diagnostics operations. Every detector the simulator
// computes measures one site's workload container, and an environment has
// none of its own, so it publishes an empty collection and reports any name
// asked of it as absent.
func TestSDK_Diagnostics_HostingEnvironmentPublishesNoDetectors(t *testing.T) {
	// An App Service Environment is placed in a virtual network subnet, and
	// creating a real subnet needs the Linux network capabilities the
	// simulator's fabric is built on. A host that cannot provide them skips;
	// Linux runs the test.
	requireNetworkHost(t)
	requireResourceGroup(t, aseRG)
	subnetID := requireSubnet(t, aseRG, "sdk-ase-diag-vnet", "10.62.0.0/16", "ase-diag-subnet", "10.62.1.0/24")
	environments := environmentsClient(t)
	poller, err := environments.BeginCreateOrUpdate(ctx, aseRG, "sdk-ase-diag", armappservice.EnvironmentResource{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.Environment{
			VirtualNetwork: &armappservice.VirtualNetworkProfile{ID: to.Ptr(subnetID)},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	diagnostics, err := armappservice.NewDiagnosticsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	responses := pagerAll(t, diagnostics.NewListHostingEnvironmentDetectorResponsesPager(aseRG, "sdk-ase-diag", nil),
		func(p armappservice.DiagnosticsClientListHostingEnvironmentDetectorResponsesResponse) []*armappservice.DetectorResponse {
			return p.Value
		})
	assert.Empty(t, responses)
	_, err = diagnostics.GetHostingEnvironmentDetectorResponse(ctx, aseRG, "sdk-ase-diag", "runtimeavailability", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")

	items, err := environments.ListDiagnostics(ctx, aseRG, "sdk-ase-diag", nil)
	require.NoError(t, err)
	assert.Empty(t, items.HostingEnvironmentDiagnosticsArray)
	_, err = environments.GetDiagnosticsItem(ctx, aseRG, "sdk-ase-diag", "anything", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")

	deletePoller, err := environments.BeginDelete(ctx, aseRG, "sdk-ase-diag", nil)
	require.NoError(t, err)
	_, err = deletePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func executeDetector(t *testing.T, client *armappservice.DiagnosticsClient, rg, site, detector string) armappservice.DiagnosticsClientExecuteSiteDetectorResponse {
	t.Helper()
	res, err := client.ExecuteSiteDetector(ctx, rg, site, detector, "availability", nil)
	require.NoError(t, err, "executing detector %q", detector)
	require.NotNil(t, res.Properties)
	require.NotNil(t, res.Properties.IssueDetected)
	return res
}

// detectorValue reads one named column out of a detector's data row.
func detectorValue(t *testing.T, row []*armappservice.NameValuePair, column string) string {
	t.Helper()
	for _, pair := range row {
		if pair.Name != nil && *pair.Name == column {
			if pair.Value == nil {
				return ""
			}
			return *pair.Value
		}
	}
	t.Fatalf("detector row carries no column %q", column)
	return ""
}

package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A component's capabilities, available features and quota status derive from
// its own billing features, set through currentbillingfeatures: what its plan
// allows, the plans it could move to beside its own, and its ingested volume
// against its cap.

func registerInsightsFeatures(srv *sim.Server, armBase string,
	billing sim.Store[AppInsightsBillingFeatures],
	defaultBilling AppInsightsBillingFeatures,
	billingKey func(*http.Request) string,
) {
	features := func(r *http.Request) AppInsightsBillingFeatures {
		held, ok := billing.Get(billingKey(r))
		if !ok {
			// A copy, because every component without a record shares the default.
			return insightsDefaultBilling(defaultBilling)
		}
		return held
	}

	srv.HandleFunc("GET "+armBase+"/components/{componentName}/featurecapabilities",
		func(w http.ResponseWriter, r *http.Request) {
			held := features(r)
			enterprise := insightsIsEnterprise(held)
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				// The Enterprise plan adds continuous export and the higher throttle.
				"SupportExportData":    enterprise,
				"BurstThrottlePolicy":  insightsThrottlePolicy(enterprise),
				"MetadataClass":        "Default",
				"LiveStreamMetrics":    true,
				"ApplicationMap":       true,
				"WorkItemIntegration":  enterprise,
				"PowerBIIntegration":   enterprise,
				"OpenSchema":           enterprise,
				"ProactiveDetection":   true,
				"AnalyticsIntegration": true,
				"MultipleStepWebTest":  enterprise,
				"ApiAccessLevel":       "Full",
				"TrackingType":         "Standard",
				"DailyCap":             held.DataVolumeCap.Cap,
				"DailyCapResetTime":    held.DataVolumeCap.ResetTime,
				"ThrottleRate":         insightsThrottleRate(enterprise),
			})
		})

	// ComponentAvailableFeatures_Get: the plans the component could be on, its own
	// among them.
	srv.HandleFunc("GET "+armBase+"/components/{componentName}/getavailablebillingfeatures",
		func(w http.ResponseWriter, r *http.Request) {
			held := features(r)
			current := "Basic"
			if len(held.CurrentBillingFeatures) > 0 {
				current = held.CurrentBillingFeatures[0]
			}
			result := []any{}
			for _, plan := range []string{"Basic", "Application Insights Enterprise"} {
				enterprise := plan != "Basic"
				result = append(result, map[string]any{
					"FeatureName":   plan,
					"MeterId":       plan,
					"ResouceId":     billingKey(r),
					"IsHidden":      false,
					"IsMainFeature": plan == current,
					"Capabilities": []any{
						map[string]any{
							"Name":        "DailyCap",
							"Description": "The daily volume this plan caps ingestion at.",
							// The document types every capability value as a string.
							"Value":   strconv.FormatFloat(held.DataVolumeCap.Cap, 'f', -1, 64),
							"Unit":    "GB",
							"MeterId": plan,
						},
						map[string]any{
							"Name":        "SupportExportData",
							"Description": "Whether continuous export is available on this plan.",
							"Value":       insightsBoolText(enterprise),
							"Unit":        "",
							"MeterId":     plan,
						},
					},
					"SupportedAddonFeatures": "",
				})
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"Result": result})
		})

	// ComponentQuotaStatus_Get counts the volume from the telemetry written.
	srv.HandleFunc("GET "+armBase+"/components/{componentName}/quotastatus",
		func(w http.ResponseWriter, r *http.Request) {
			held := features(r)
			component, _ := azureAppInsightsComponents.Get(billingKey(r))
			appID := component.Properties.AppID
			throttled := insightsIngestedGB(appID) > held.DataVolumeCap.Cap
			status := map[string]any{
				"AppId":             appID,
				"ShouldBeThrottled": throttled,
			}
			if throttled {
				// The throttle lasts until the cap resets, at the hour the cap names.
				status["ExpirationTime"] = insightsNextCapReset(held.DataVolumeCap.ResetTime).
					Format(time.RFC3339)
			}
			sim.WriteJSON(w, http.StatusOK, status)
		})
}

func insightsIsEnterprise(held AppInsightsBillingFeatures) bool {
	for _, feature := range held.CurrentBillingFeatures {
		if feature != "Basic" {
			return true
		}
	}
	return false
}

// The burst allowance differs by plan.
func insightsThrottlePolicy(enterprise bool) string {
	if enterprise {
		return "Burst"
	}
	return "Standard"
}

func insightsThrottleRate(enterprise bool) int {
	if enterprise {
		return 32768
	}
	return 16384
}

func insightsBoolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// Measured from the rows the log store holds, so the two agree.
func insightsIngestedGB(appID string) float64 {
	rows := 0
	for eventType := range insightsEventTables {
		held, _ := insightsEventRows(appID, eventType)
		rows += len(held)
	}
	// Application Insights sizes ingestion at roughly a kilobyte per row.
	return float64(rows) / (1024 * 1024)
}

func insightsNextCapReset(resetHour int) time.Time {
	now := time.Now().UTC()
	reset := time.Date(now.Year(), now.Month(), now.Day(), resetHour, 0, 0, 0, time.UTC)
	if !reset.After(now) {
		reset = reset.AddDate(0, 0, 1)
	}
	return reset
}

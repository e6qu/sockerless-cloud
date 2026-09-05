package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// What a component's billing plan entitles it to, and whether it is over the
// cap that plan set.
//
// All three reads are derived from the component's own billing features, which
// a client sets through currentbillingfeatures: the capabilities are what the
// plan it is on allows, the available features are the plans it could move to
// beside the one it is on, and the quota status compares the volume the
// component has actually ingested against the cap it set. None of them is a
// published price list — the meters are the plan names the component itself
// carries.

// registerInsightsFeatures mounts the three reads.
func registerInsightsFeatures(srv *sim.Server, armBase string,
	billing sim.Store[AppInsightsBillingFeatures],
	defaultBilling AppInsightsBillingFeatures,
	billingKey func(*http.Request) string,
) {
	features := func(r *http.Request) AppInsightsBillingFeatures {
		held, ok := billing.Get(billingKey(r))
		if !ok {
			// A copy, because the default is shared by every component that
			// has no record of its own.
			return insightsDefaultBilling(defaultBilling)
		}
		return held
	}

	// ComponentFeatureCapabilities_Get.
	srv.HandleFunc("GET "+armBase+"/components/{componentName}/featurecapabilities",
		func(w http.ResponseWriter, r *http.Request) {
			held := features(r)
			enterprise := insightsIsEnterprise(held)
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				// The capabilities the plan allows. The Enterprise plan is the
				// one that adds continuous export and the higher throttle, so
				// the plan the component is on decides these rather than a
				// fixed answer.
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

	// ComponentAvailableFeatures_Get — the plans this component could be on,
	// each carrying the capabilities it would bring. The one it is on is
	// among them, which is what makes the list a choice rather than a catalog.
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
							// The document types a capability's value as a
							// string whatever it describes, so the cap is
							// rendered rather than sent as the number it is.
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

	// ComponentQuotaStatus_Get — whether the component has ingested past the
	// cap it set. The volume is counted from the telemetry it actually wrote,
	// so it moves when the application writes.
	srv.HandleFunc("GET "+armBase+"/components/{componentName}/quotastatus",
		func(w http.ResponseWriter, r *http.Request) {
			held := features(r)
			appID := sim.PathParam(r, "componentName")
			throttled := insightsIngestedGB(appID) > held.DataVolumeCap.Cap
			status := map[string]any{
				"AppId":             appID,
				"ShouldBeThrottled": throttled,
			}
			if throttled {
				// The throttle lasts until the cap resets, which is the hour
				// of the day the component's own cap names.
				status["ExpirationTime"] = insightsNextCapReset(held.DataVolumeCap.ResetTime).
					Format(time.RFC3339)
			}
			sim.WriteJSON(w, http.StatusOK, status)
		})
}

// insightsIsEnterprise reports whether the component is on the plan that adds
// the capabilities the Basic plan does not carry.
func insightsIsEnterprise(held AppInsightsBillingFeatures) bool {
	for _, feature := range held.CurrentBillingFeatures {
		if feature != "Basic" {
			return true
		}
	}
	return false
}

// insightsThrottlePolicy and insightsThrottleRate are the burst allowance the
// plan carries. They differ by plan, which is the whole reason the capabilities
// read exists.
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

// insightsBoolText renders a capability value, which the document types as a
// string whatever it describes.
func insightsBoolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// insightsIngestedGB is how much telemetry the component has written, in the
// unit the cap is set in. It is measured from the rows the log store holds for
// the application rather than tracked separately, so the two cannot disagree.
func insightsIngestedGB(appID string) float64 {
	rows := 0
	for eventType := range insightsEventTables {
		held, _ := insightsEventRows(appID, eventType)
		rows += len(held)
	}
	// A telemetry row is roughly a kilobyte on the wire, which is the figure
	// Application Insights sizes ingestion with.
	return float64(rows) / (1024 * 1024)
}

// insightsNextCapReset is when the daily cap next resets: the hour of the day
// the component's cap names, on the next day it comes round.
func insightsNextCapReset(resetHour int) time.Time {
	now := time.Now().UTC()
	reset := time.Date(now.Year(), now.Month(), now.Day(), resetHour, 0, 0, 0, time.UTC)
	if !reset.After(now) {
		reset = reset.AddDate(0, 0, 1)
	}
	return reset
}
